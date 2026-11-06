#!/usr/bin/env python3
"""scale_demo.py - run the state-field sync at the size of the real tracker, offline.

Purpose:      take a COPY of a workable-items DB (the real one has ~1600 items), add the
              state columns to the copy, set states on ~70 rows, and drive three boards
              (one per key prefix found) of the offline mock through: dry-run, apply, apply
              again (must write nothing), a handful of state edits, apply (must touch only
              those tasks). Prints the evidence table.
Usage:        python3 scripts/scale_demo.py --db /path/to/COPY.db --spec pkg/states/testdata/spec_repro.json
Outputs:      a table of requests per phase; exit 0 only when idempotence and the
              touch-only-the-edited-tasks property hold.
Side-effects: writes only the scratch copy it is given (--work) and a loopback socket.
Dependencies: python3, go, sqlite3 CLI and python module.
"""
import argparse
import json
import os
import random
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
from mock_clickup_server import MockClickUp  # noqa: E402

ap = argparse.ArgumentParser()
ap.add_argument("--db", required=True, help="a COPY of an items DB (never the live one)")
ap.add_argument("--spec", default=os.path.join(ROOT, "pkg", "states", "testdata", "spec_repro.json"))
ap.add_argument("--work", default="")
ap.add_argument("--rows", type=int, default=70)
a = ap.parse_args()

work = a.work or tempfile.mkdtemp(prefix="tb_scale.")
os.makedirs(work, exist_ok=True)
db = os.path.join(work, "scale.db")
shutil.copyfile(a.db, db)
spec = json.load(open(a.spec))
keys = [f["key"] for f in spec["fields"]]
con = sqlite3.connect(db)
cols = {r[1] for r in con.execute("pragma table_info(items)")}
for k in keys:
    if k not in cols:
        con.execute("ALTER TABLE items ADD COLUMN %s TEXT" % k)
rnd = random.Random(1103)
rows = [r[0] for r in con.execute("select rowid from items where atm_id like 'ATM-%' order by rowid limit ?", (a.rows * 3,))]
rnd.shuffle(rows)
enum_vals = {f["key"]: [v for v in f["values"] if v != f["unset"]] for f in spec["fields"] if f["kind"] == "enum"}
for rid in rows[: a.rows]:
    vals = {}
    for k, vs in enum_vals.items():
        if rnd.random() < 0.8:
            vals[k] = rnd.choice(vs)
    vals["reproduction_evidence"] = "qa-results/demo/%d.txt" % rid
    vals["reproduction_red_cmd"] = "bash gate.sh --mutant %d" % rid
    for k, v in vals.items():
        con.execute("UPDATE items SET %s=? WHERE rowid=?" % k, (v, rid))
con.commit()
prefixes = sorted({r[0] for r in con.execute("select substr(atm_id,1,instr(atm_id,'-')-1) from items")})
counts = {p: con.execute("select count(*) from items where atm_id like ?", (p + "-%",)).fetchone()[0] for p in prefixes}
con.close()

env_go = dict(os.environ, GOFLAGS=os.environ.get("GOFLAGS", "-mod=mod"), GOPROXY=os.environ.get("GOPROXY", "off"))
binp = os.path.join(work, "task_bridge")
subprocess.run(["go", "build", "-o", binp, "./cmd/task_bridge"], cwd=ROOT, env=env_go, check=True)

m = MockClickUp("SCALE-TOKEN-NOT-REAL")
url = m.start()
with m.state.lock:
    m.state.reset({"lists": [{"id": "L-" + p, "fields": [], "tasks": []} for p in prefixes]})


# NB: --min-interval-ms 0 means "use the 700 ms default" (clickup.go), so the offline run passes 1.
def run(p, apply, extra=()):
    env = {k: v for k, v in os.environ.items() if not k.startswith(("CLICKUP", "TASK_BRIDGE"))}
    env.update({"CLICKUP_API_KEY": "SCALE-TOKEN-NOT-REAL", "TASK_BRIDGE_BASE_URL": url})
    cmd = [binp, "reconcile", "--list", "L-" + p, "--db", db, "--prefix", p, "--min-interval-ms", "1", "--states-spec", a.spec] + (["--apply"] if apply else []) + list(extra)
    t0 = time.time()
    r = subprocess.run(cmd, env=env, capture_output=True, text=True)
    return r, time.time() - t0


def writes(since):
    return [e for e in m.state.log[since:] if e["method"] != "GET"]


ok = True
print("%-28s %-5s %8s %8s %8s %8s" % ("phase", "board", "rc", "requests", "writes", "seconds"))
for phase, apply in (("1 dry-run (empty board)", False), ("2 apply", True), ("3 apply again", True), ("4 dry-run again", False)):
    for p in prefixes:
        n0 = len(m.state.log)
        r, dt = run(p, apply)
        w = len(writes(n0))
        print("%-28s %-5s %8d %8d %8d %8.1f" % (phase, p, r.returncode, len(m.state.log) - n0, w, dt))
        if phase.startswith("3") and w != 0:
            ok = False
        if phase.startswith("4") and "tasks_with_drift=0" not in r.stdout:
            ok = False
        if r.returncode != 0:
            ok = False
            print(r.stdout[-1500:], r.stderr[-1500:])
# edit a handful of states and re-apply: only those tasks may be written
con = sqlite3.connect(db)
edited = [r[0] for r in con.execute("select atm_id from items where atm_id like 'ATM-%' and reproduction_state is not null and reproduction_state<>'' limit 5")]
for k in edited:
    con.execute("UPDATE items SET reproduction_state='blocked-no-device-with-recorded-blocker', investigation_state='in-progress' WHERE atm_id=?", (k,))
con.commit()
con.close()
n0 = len(m.state.log)
r, dt = run("ATM", True)
ws = writes(n0)
touched = sorted({e["path"].split("/task/")[1].split("/")[0] for e in ws if "/task/" in e["path"] and e["method"] in ("PUT", "POST")})
print("%-28s %-5s %8d %8d %8d %8.1f   edited=%d tasks written=%d" % ("5 edit 5 states, apply", "ATM", r.returncode, len(m.state.log) - n0, len(ws), dt, len(edited), len(touched)))
if len(touched) > len(edited) or r.returncode != 0:
    ok = False
deletes = [e for e in m.state.log if e["method"] == "DELETE" and "/field/" not in e["path"] and "/tag/" not in e["path"]]
print("task DELETE calls: %d   board sizes (local): %s" % (len(deletes), counts))
if deletes:
    ok = False
m.stop()
print("SCALE", "OK" if ok else "FAILED")
sys.exit(0 if ok else 1)
