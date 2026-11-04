#!/usr/bin/env python3
"""e2e_states.py - end-to-end proof of the state-field sync against the offline mock.

Purpose:      drive the REAL task_bridge binary (reconcile, dry-run and --apply)
              against scripts/mock_clickup_server.py with a REAL SQLite items DB,
              covering: block / field / mixed carriers, guarded field creation,
              idempotence (apply twice = zero writes), unset and removal, refused
              values, malformed blocks, 401 / 429 / 500 / partial pages / lossy
              Markdown channel / ignored writes, staged roll-out, all three boards.
Usage:        python3 scripts/e2e_states.py            (builds the binary itself)
              TB_BIN=/path/to/task_bridge python3 scripts/e2e_states.py
Outputs:      unittest report; exit 0 only if every scenario passes. The final line is
              "E2E tests=N failures=0 errors=0".
Side-effects: temp files under $TMPDIR only; a loopback socket. No real ClickUp call:
              the token is a fixed fake string and the base URL is the mock.
Dependencies: python3, go (to build), sqlite3 python module, the sqlite3 CLI.
"""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
from mock_clickup_server import MockClickUp  # noqa: E402

TOKEN = "MOCK-TOKEN-NOT-A-REAL-CREDENTIAL"
SPEC_PATH = os.environ.get("TB_E2E_SPEC") or os.path.join(ROOT, "pkg", "states", "testdata", "spec_repro.json")
with open(SPEC_PATH) as _fh:
    SPEC = json.load(_fh)
KEYS = [f["key"] for f in SPEC["fields"]]
BIN = os.environ.get("TB_BIN", "")
_tmp_bin_dir = None

BASE_COLS = ["atm_id", "type", "status", "severity", "title", "description", "current_location", "last_modified"]

ITEMS = {
    "ATM-1": dict(type="Bug", status="Queued", title="alpha", description="desc one", reproduction_state="reproduced-live",
                  investigation_state="in-progress", reproduction_evidence="qa/x.txt", reproduction_red_cmd="bash r.sh",
                  reproduction_green_cmd="bash g.sh", reproduction_env="bench-1"),
    "ATM-2": dict(type="Task", status="In progress", title="beta", description="desc two"),
    "ATM-3": dict(type="Feature", status="Queued", title="gamma", description="", reproduction_state="UNREPRODUCED"),
    "SPK-1": dict(type="Bug", status="Queued", title="delta", description="desc four", investigation_state="closed-no-defect",
                  investigation_root_cause="line1\nline2 *bold* [x] C:\\tmp"),
    "MVR-1": dict(type="Task", status="Queued", title="eps", description="desc five", reproduction_state="blocked-no-device"),
}
COLUMN = {"Queued": "to do", "In progress": "in progress"}


def build_binary():
    global BIN, _tmp_bin_dir
    if BIN:
        return
    _tmp_bin_dir = tempfile.mkdtemp(prefix="tb_e2e_bin.")
    BIN = os.path.join(_tmp_bin_dir, "task_bridge")
    env = dict(os.environ)
    env.setdefault("GOFLAGS", "-mod=mod")
    r = subprocess.run(["go", "build", "-o", BIN, "./cmd/task_bridge"], cwd=ROOT, env=env, capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit("go build failed:\n" + r.stdout + r.stderr)


def make_db(path, items, with_state_cols=True, extra_rows=()):
    con = sqlite3.connect(path)
    cols = BASE_COLS + (KEYS if with_state_cols else [])
    con.execute("CREATE TABLE items (%s)" % ", ".join("%s TEXT" % c for c in cols))
    rows = dict(items)
    for key, it in rows.items():
        vals = {"atm_id": key, "type": it["type"], "status": it["status"], "severity": "", "title": it["title"],
                "description": it.get("description", ""), "current_location": "Issues", "last_modified": "2026-11-01 00:00:00"}
        for k in KEYS:
            if with_state_cols:
                vals[k] = it.get(k)
        c = list(vals)
        con.execute("INSERT INTO items (%s) VALUES (%s)" % (",".join(c), ",".join("?" * len(c))), [vals[x] for x in c])
    con.commit()
    con.close()


def remote_task(key, it, **over):
    t = {"id": "r-" + key, "name": "[%s] %s" % (key, it["title"]), "markdown_description": it.get("description", ""),
         "status": COLUMN[it["status"]], "tags": [it["type"].lower(), "status:" + it["status"].lower()]}
    t.update(over)
    return t


def field_defs(only=None, skip=()):
    out = []
    for f in SPEC["fields"]:
        if only is not None and f["key"] not in only:
            continue
        if f["key"] in skip:
            continue
        d = {"id": "cf-" + f["key"], "name": f["key"]}
        if f["kind"] == "enum":
            d["type"] = "drop_down"
            d["options"] = [v for v in f["values"] if v != f["unset"]]
        else:
            d["type"] = "text"
        out.append(d)
    return out


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="tb_e2e.")
        self.mock = MockClickUp(TOKEN)
        self.url = self.mock.start()
        self.db = os.path.join(self.tmp, "items.db")
        make_db(self.db, ITEMS)

    def tearDown(self):
        self.mock.stop()
        shutil.rmtree(self.tmp, ignore_errors=True)

    # -- mock control ------------------------------------------------------------
    def seed(self, lists, config=None, faults=None):
        st = self.mock.state
        with st.lock:
            st.reset({"lists": lists, "config": config or {}, "faults": faults or []})

    def seed_atm(self, fields=None, config=None, faults=None, tasks=None, extra_lists=()):
        tasks = tasks if tasks is not None else [remote_task(k, v) for k, v in ITEMS.items() if k.startswith("ATM")]
        self.seed([{"id": "L-ATM", "fields": fields or [], "tasks": tasks}] + list(extra_lists), config, faults)

    def faults(self, rules):
        with self.mock.state.lock:
            self.mock.state.add_faults(rules)

    def log(self):
        return list(self.mock.state.log)

    def writes(self, since=0):
        return [e for e in self.log()[since:] if e["method"] != "GET"]

    def nlog(self):
        return len(self.log())

    def task(self, lid, tid):
        return self.mock.state.lists[lid]["tasks"][tid]

    # -- running the binary --------------------------------------------------------
    def run_tb(self, list_id="L-ATM", prefix="ATM", apply=False, extra=(), db=None, spec=SPEC_PATH, env_extra=None, out=None):
        build_binary()
        cmd = [BIN, "reconcile", "--list", list_id, "--db", db or self.db, "--prefix", prefix, "--min-interval-ms", "1"]
        if spec:
            cmd += ["--states-spec", spec]
        if apply:
            cmd.append("--apply")
        if out:
            cmd += ["--out", out]
        cmd += list(extra)
        env = {k: v for k, v in os.environ.items() if not k.startswith(("CLICKUP", "TASK_BRIDGE"))}
        env.update({"CLICKUP_API_KEY": TOKEN, "TASK_BRIDGE_BASE_URL": self.url})
        env.update(env_extra or {})
        r = subprocess.run(cmd, env=env, capture_output=True, text=True, timeout=120)
        self.assertNotIn(TOKEN, r.stdout + r.stderr, "the token value leaked into the output")
        return r

    def assertRC(self, r, rc):
        self.assertEqual(r.returncode, rc, "rc=%d\nSTDOUT:\n%s\nSTDERR:\n%s" % (r.returncode, r.stdout, r.stderr))


class TestDryRun(Base):
    def test_dry_run_default_writes_nothing_and_reports_diff(self):
        self.seed_atm()
        r = self.run_tb()
        self.assertRC(r, 0)
        self.assertEqual(self.writes(), [])
        self.assertIn("DRY-RUN", r.stdout)
        self.assertIn("state-field diff: tasks_with_drift=2", r.stdout)
        self.assertIn("ATM-1", r.stdout)
        self.assertIn("reproduction_state[BLOCK]", r.stdout)
        # every spec key is reported with its carrier and the exact provisioning table
        for k in KEYS:
            self.assertIn(k, r.stdout)
        self.assertIn("to use the FIELD carrier an operator provisions", r.stdout)
        self.assertIn("drop_down", r.stdout)

    def test_create_flag_without_spec_is_a_usage_error(self):
        self.seed_atm()
        self.assertRC(self.run_tb(spec=None, extra=["--create-missing-fields"]), 2)

    def test_dry_run_artifact_is_json_with_state_diff(self):
        self.seed_atm()
        out = os.path.join(self.tmp, "plan.json")
        r = self.run_tb(out=out)
        self.assertRC(r, 0)
        with open(out) as fh:
            a = json.load(fh)
        self.assertIn("states", a)
        self.assertEqual(a["states"]["counts"]["drift"], 2)
        e = [x for x in a["plan"]["update"] if x["key"] == "ATM-1"][0]
        self.assertTrue(e["state_drift"])
        self.assertEqual(e["state_diff"]["block_op"], "ADD")
        self.assertNotIn("RemoteDescription", json.dumps(a))


class TestBlockCarrier(Base):
    def test_apply_block_then_second_apply_is_zero_writes(self):
        self.seed_atm()
        r = self.run_tb(apply=True)
        self.assertRC(r, 0)
        d1 = self.task("L-ATM", "r-ATM-1")["markdown_description"]
        self.assertTrue(d1.startswith("desc one\n\n[TB-STATES:BEGIN v1]"), d1)
        self.assertIn("reproduction_state: reproduced-live", d1)
        self.assertIn("reproduction_green_cmd: bash g.sh", d1)
        self.assertTrue(d1.rstrip().endswith("[TB-STATES:END]"))
        self.assertEqual(self.task("L-ATM", "r-ATM-2")["markdown_description"], "desc two")  # no states: untouched
        d3 = self.task("L-ATM", "r-ATM-3")["markdown_description"]
        self.assertTrue(d3.startswith("[TB-STATES:BEGIN"), d3)  # empty base description
        n = self.nlog()
        r2 = self.run_tb(apply=True)
        self.assertRC(r2, 0)
        self.assertEqual(self.writes(n), [], "second apply must write nothing")
        r3 = self.run_tb()
        self.assertIn("tasks_with_drift=0", r3.stdout)
        self.assertEqual(self.task("L-ATM", "r-ATM-1")["markdown_description"], d1)

    def test_unset_removes_block_and_roundtrips(self):
        self.seed_atm()
        self.run_tb(apply=True)
        make_db_path = os.path.join(self.tmp, "items2.db")
        items = json.loads(json.dumps(ITEMS))
        for k in KEYS:
            items["ATM-1"].pop(k, None)
        make_db(make_db_path, items)
        r = self.run_tb(apply=True, db=make_db_path)
        self.assertRC(r, 0)
        self.assertEqual(self.task("L-ATM", "r-ATM-1")["markdown_description"], "desc one")
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True, db=make_db_path), 0)
        self.assertEqual(self.writes(n), [])

    def test_materialize_unset_is_opt_in_and_idempotent(self):
        self.seed_atm()
        r = self.run_tb(apply=True, extra=["--states-materialize-unset"])
        self.assertRC(r, 0)
        d2 = self.task("L-ATM", "r-ATM-2")["markdown_description"]
        self.assertIn("reproduction_state: UNSET", d2)
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True, extra=["--states-materialize-unset"]), 0)
        self.assertEqual(self.writes(n), [])

    def test_create_new_item_carries_block_in_one_post(self):
        self.seed_atm(tasks=[remote_task("ATM-2", ITEMS["ATM-2"]), remote_task("ATM-3", ITEMS["ATM-3"])])
        r = self.run_tb(apply=True)
        self.assertRC(r, 0)
        posts = [e for e in self.writes() if e["method"] == "POST" and e["path"].endswith("/list/L-ATM/task")]
        self.assertEqual(len(posts), 1)
        self.assertIn("[TB-STATES:BEGIN", posts[0]["body"]["markdown_description"])
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True), 0)
        self.assertEqual(self.writes(n), [])

    def test_lossy_markdown_channel_converges(self):
        self.seed_atm(faults=[{"method": "GET", "path": "/api/v2/(list/L-ATM/task|task/)", "action": "escape_markdown", "times": -1}])
        self.assertRC(self.run_tb(apply=True), 0)
        n = self.nlog()
        r = self.run_tb(apply=True)
        self.assertRC(r, 0)
        self.assertEqual(self.writes(n), [], "an escaped read-back must not cause a perpetual rewrite")

    def test_value_with_markers_and_special_chars_roundtrips(self):
        self.seed_atm(tasks=[remote_task("ATM-1", ITEMS["ATM-1"])])
        items = json.loads(json.dumps(ITEMS))
        items["ATM-1"]["reproduction_evidence"] = "x\n[TB-STATES:END]\nreproduction_state: UNREPRODUCED \\n *_[]()"
        p = os.path.join(self.tmp, "evil.db")
        make_db(p, items)
        self.assertRC(self.run_tb(apply=True, db=p), 0)
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True, db=p), 0)
        self.assertEqual(self.writes(n), [])
        self.assertIn("reproduction_state: reproduced-live", self.task("L-ATM", "r-ATM-1")["markdown_description"])


class TestFieldCarrier(Base):
    def test_fields_mode_sets_values_and_is_idempotent(self):
        self.seed_atm(fields=field_defs())
        r = self.run_tb(apply=True)
        self.assertRC(r, 0)
        t = self.task("L-ATM", "r-ATM-1")
        self.assertEqual(t["markdown_description"], "desc one", "FIELD carrier must not touch the description")
        st = self.mock.state.lists["L-ATM"]["fields"]
        rs = [f for f in st if f["name"] == "reproduction_state"][0]
        opt = rs["type_config"]["options"][t["values"]["cf-reproduction_state"]]
        self.assertEqual(opt["name"], "reproduced-live")
        self.assertEqual(t["values"]["cf-reproduction_green_cmd"], "bash g.sh")
        self.assertNotIn("cf-investigation_evidence", t["values"])  # empty: not set
        sets = [e for e in self.writes() if "/field/" in e["path"]]
        self.assertTrue(all(e["method"] == "POST" for e in sets))
        # the drop-down value sent is the option ID, never the name
        ds = [e for e in sets if e["path"].endswith("/field/cf-reproduction_state") and "/task/r-ATM-1/" in e["path"]][0]
        self.assertEqual(ds["body"]["value"], "cf-reproduction_state-opt%d" % t["values"]["cf-reproduction_state"])
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True), 0)
        self.assertEqual(self.writes(n), [])
        self.assertIn("tasks_with_drift=0", self.run_tb().stdout)

    def test_dropdown_value_returned_as_option_id_is_understood(self):
        self.seed_atm(fields=field_defs(), config={"dropdown_value_style": "id"})
        self.assertRC(self.run_tb(apply=True), 0)
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True), 0)
        self.assertEqual(self.writes(n), [])

    def test_clearing_a_value_deletes_the_field_value_only(self):
        self.seed_atm(fields=field_defs())
        self.run_tb(apply=True)
        items = json.loads(json.dumps(ITEMS))
        items["ATM-1"].pop("reproduction_state")
        items["ATM-1"].pop("reproduction_env")
        p = os.path.join(self.tmp, "i2.db")
        make_db(p, items)
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True, db=p), 0)
        dels = [e for e in self.writes(n) if e["method"] == "DELETE"]
        self.assertEqual(sorted(e["path"].rsplit("/", 1)[1] for e in dels), ["cf-reproduction_env", "cf-reproduction_state"])
        self.assertTrue(all("/field/" in e["path"] for e in dels), "only field values may be deleted")
        t = self.task("L-ATM", "r-ATM-1")
        self.assertNotIn("cf-reproduction_state", t["values"])

    def test_mixed_mode_some_fields_some_block(self):
        self.seed_atm(fields=field_defs(only=["reproduction_state", "reproduction_env"]))
        r = self.run_tb(apply=True)
        self.assertRC(r, 0)
        t = self.task("L-ATM", "r-ATM-1")
        self.assertIn("cf-reproduction_state", t["values"])
        d = t["markdown_description"]
        self.assertNotIn("reproduction_state:", d)
        self.assertIn("investigation_state: in-progress", d)
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True), 0)
        self.assertEqual(self.writes(n), [])

    def test_field_with_wrong_options_falls_back_and_is_reported(self):
        defs = field_defs()
        defs[0]["options"] = ["UNREPRODUCED"]
        self.seed_atm(fields=defs)
        r = self.run_tb()
        self.assertRC(r, 0)
        self.assertIn("OPTIONS_DRIFT", r.stdout)
        self.assertIn("missing options", r.stdout)

    def test_carrier_field_fails_closed_when_fields_missing(self):
        self.seed_atm(fields=field_defs(skip=["reproduction_env"]))
        r = self.run_tb(apply=True, extra=["--carrier", "field"])
        self.assertRC(r, 2)
        self.assertEqual(self.writes(), [])

    def test_carrier_block_ignores_existing_fields(self):
        self.seed_atm(fields=field_defs())
        self.assertRC(self.run_tb(apply=True, extra=["--carrier", "block"]), 0)
        self.assertEqual([e for e in self.writes() if "/field/" in e["path"]], [])
        self.assertIn("[TB-STATES:BEGIN", self.task("L-ATM", "r-ATM-1")["markdown_description"])


class TestFieldCreation(Base):
    def test_dry_run_lists_would_create_and_calls_nothing(self):
        self.seed_atm()
        r = self.run_tb(extra=["--create-missing-fields"])
        self.assertRC(r, 0)
        self.assertEqual(self.writes(), [])
        self.assertIn("WOULD try to create", r.stdout)

    def test_apply_without_flag_never_creates(self):
        self.seed_atm()
        self.assertRC(self.run_tb(apply=True), 0)
        self.assertEqual([e for e in self.writes() if e["path"].endswith("/field")], [])

    def test_apply_with_flag_creates_once_then_idempotent(self):
        self.seed_atm()
        r = self.run_tb(apply=True, extra=["--create-missing-fields"])
        self.assertRC(r, 0)
        creates = [e for e in self.writes() if e["method"] == "POST" and e["path"].endswith("/list/L-ATM/field")]
        self.assertEqual(len(creates), 8)
        self.assertEqual(sorted(c["body"]["name"] for c in creates), sorted(KEYS))
        self.assertEqual(len(self.mock.state.lists["L-ATM"]["fields"]), 8)
        self.assertIn("CREATED custom fields", r.stdout)
        t = self.task("L-ATM", "r-ATM-1")
        self.assertEqual(t["markdown_description"], "desc one")
        for f in self.mock.state.lists["L-ATM"]["fields"]:
            self.assertIn(f["type"], ("drop_down", "text"))
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True, extra=["--create-missing-fields"]), 0)
        self.assertEqual(self.writes(n), [], "second run must neither create nor write")

    def test_cap_on_creations(self):
        self.seed_atm()
        self.assertRC(self.run_tb(apply=True, extra=["--create-missing-fields", "--max-field-creates", "3"]), 0)
        self.assertEqual(len(self.mock.state.lists["L-ATM"]["fields"]), 3)

    def test_unsupported_create_falls_back_to_block_and_says_so(self):
        self.seed_atm(config={"field_create": "404"})
        r = self.run_tb(apply=True, extra=["--create-missing-fields"])
        self.assertRC(r, 0)
        self.assertIn("FIELD_CREATE_UNSUPPORTED", r.stdout)
        self.assertEqual(len([e for e in self.writes() if e["path"].endswith("/field")]), 1, "must stop after the first 404")
        self.assertIn("[TB-STATES:BEGIN", self.task("L-ATM", "r-ATM-1")["markdown_description"])

    def test_create_error_is_listed_and_does_not_abort_the_sync(self):
        self.seed_atm(config={"field_create": "400"})
        r = self.run_tb(apply=True, extra=["--create-missing-fields"])
        self.assertRC(r, 0)
        self.assertIn("FIELD_CREATE_ERROR", r.stdout)
        self.assertIn("[TB-STATES:BEGIN", self.task("L-ATM", "r-ATM-1")["markdown_description"])

    def test_never_creates_a_duplicate_named_field(self):
        self.seed_atm(fields=field_defs(only=["reproduction_state"]))
        self.assertRC(self.run_tb(apply=True, extra=["--create-missing-fields"]), 0)
        names = [f["name"] for f in self.mock.state.lists["L-ATM"]["fields"]]
        self.assertEqual(len(names), len(set(names)))
        self.assertEqual(len(names), 8)


class TestFaults(Base):
    def test_401_on_listing_exits_5_without_writes_or_token(self):
        self.seed_atm()
        self.faults([{"method": "GET", "path": "/list/L-ATM/field", "action": "status", "status": 401, "times": -1}])
        r = self.run_tb(apply=True)
        self.assertRC(r, 5)
        self.assertEqual(self.writes(), [])

    def test_wrong_token_is_a_401_exit_5(self):
        self.seed_atm()
        r = self.run_tb(env_extra={"CLICKUP_API_KEY": "WRONG-TOKEN-VALUE-XYZ"})
        self.assertNotIn("WRONG-TOKEN-VALUE-XYZ", r.stdout + r.stderr)
        self.assertRC(r, 5)

    def test_401_mid_apply_stops_the_batch(self):
        self.seed_atm()
        self.faults([{"method": "PUT", "path": "/task/", "action": "status", "status": 401, "times": -1}])
        r = self.run_tb(apply=True)
        self.assertRC(r, 5)
        puts = [e for e in self.writes() if e["method"] == "PUT"]
        self.assertEqual(len(puts), 1, "the batch must stop at the first credentials rejection")

    def test_429_is_retried_on_the_new_endpoints(self):
        self.seed_atm(fields=field_defs())
        self.faults([{"method": "POST", "path": "/field/cf-reproduction_state", "action": "status", "status": 429, "retry_after": "0", "times": 1}])
        r = self.run_tb(apply=True)
        self.assertRC(r, 0)
        posts = [e for e in self.log() if e["path"].endswith("/task/r-ATM-1/field/cf-reproduction_state")]
        self.assertEqual([e["status"] for e in posts], [429, 200])
        self.assertEqual(posts[0]["body"], posts[1]["body"], "the retried request must replay the same body")

    def test_500_on_a_write_is_an_error_exit_1_and_the_next_run_converges(self):
        self.seed_atm()
        self.faults([{"method": "PUT", "path": "/task/r-ATM-1$", "action": "status", "status": 500, "times": 1}])
        r = self.run_tb(apply=True)
        self.assertRC(r, 1)
        self.assertIn("apply-error", r.stderr)
        self.assertNotIn("[TB-STATES", self.task("L-ATM", "r-ATM-1")["markdown_description"])
        self.assertIn("[TB-STATES", self.task("L-ATM", "r-ATM-3")["markdown_description"], "other items must still be processed")
        self.assertRC(self.run_tb(apply=True), 0)
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True), 0)
        self.assertEqual(self.writes(n), [])

    def test_partial_page_does_not_truncate_the_board(self):
        items = {"ATM-%d" % i: dict(type="Task", status="Queued", title="t%d" % i, description="d") for i in range(1, 251)}
        p = os.path.join(self.tmp, "big.db")
        make_db(p, items)
        tasks = [remote_task(k, v) for k, v in items.items()]
        # Page 0 holds only 60 of its 100 slots (the rest filtered) and is NOT the last page.
        self.seed_atm(tasks=tasks, faults=[{"method": "GET", "path": "/list/L-ATM/task", "action": "short_page", "keep": 60, "after": 0, "times": 1}])
        r = self.run_tb(db=p)
        self.assertRC(r, 0)
        pages = [e["query"]["page"] for e in self.log() if e["path"].endswith("/list/L-ATM/task")]
        self.assertEqual(pages, ["0", "1", "2"], "a short page with last_page=false must not end the listing")
        self.assertIn("remote_tasks=210", r.stdout)

    def test_pagination_without_last_page_flag_uses_the_full_page_rule(self):
        items = {"ATM-%d" % i: dict(type="Task", status="Queued", title="t%d" % i, description="d") for i in range(1, 251)}
        p = os.path.join(self.tmp, "big2.db")
        make_db(p, items)
        self.seed_atm(tasks=[remote_task(k, v) for k, v in items.items()], config={"last_page": False})
        r = self.run_tb(db=p)
        self.assertRC(r, 0)
        self.assertIn("remote_tasks=250", r.stdout)

    def test_omit_markdown_source_refuses_the_rewrite(self):
        self.seed_atm(faults=[{"method": "GET", "path": "/list/L-ATM/task", "action": "omit_markdown", "times": -1}])
        r = self.run_tb(apply=True)
        self.assertRC(r, 4)
        puts = [e["path"] for e in self.writes() if e["method"] == "PUT"]
        self.assertNotIn("/api/v2/task/r-ATM-1", puts, "a non-empty description must not be rewritten without the Markdown source")
        self.assertEqual(self.task("L-ATM", "r-ATM-1")["markdown_description"], "desc one")
        self.assertIn("DESC_SOURCE_UNAVAILABLE", r.stdout)

    def test_ignored_write_is_detected_by_the_readback(self):
        self.seed_atm()
        self.faults([{"method": "PUT", "path": "/task/r-ATM-1$", "action": "ignore_write", "times": -1}])
        r = self.run_tb(apply=True)
        self.assertRC(r, 1)
        self.assertIn("READBACK_MISMATCH", r.stderr)

    def test_no_readback_flag_skips_the_verifying_get(self):
        self.seed_atm()
        self.run_tb(apply=True, extra=["--no-readback"])
        gets_after_put = [e for e in self.log() if e["method"] == "GET" and e["path"].endswith("/task/r-ATM-1")]
        self.assertEqual(gets_after_put, [])


class TestRefusals(Base):
    def test_unknown_state_is_refused_other_items_still_sync(self):
        items = json.loads(json.dumps(ITEMS))
        items["ATM-3"]["reproduction_state"] = "reproduced"
        p = os.path.join(self.tmp, "bad.db")
        make_db(p, items)
        self.seed_atm()
        r = self.run_tb(apply=True, db=p)
        self.assertRC(r, 4)
        self.assertIn("STATE_INVALID:reproduction_state", r.stdout)
        self.assertEqual(self.task("L-ATM", "r-ATM-3")["markdown_description"], "")
        self.assertIn("[TB-STATES", self.task("L-ATM", "r-ATM-1")["markdown_description"])

    def test_malformed_remote_block_is_never_touched(self):
        broken = "human text\n[TB-STATES:BEGIN v1]\nreproduction_state: UNREPRODUCED\nTail text that must survive"
        tasks = [remote_task("ATM-1", ITEMS["ATM-1"], markdown_description=broken)]
        self.seed_atm(tasks=tasks)
        r = self.run_tb(apply=True)
        self.assertRC(r, 4)
        self.assertEqual(self.task("L-ATM", "r-ATM-1")["markdown_description"], broken)
        self.assertIn("BLOCK_MALFORMED", r.stdout)

    def test_missing_columns_disable_states_loudly(self):
        p = os.path.join(self.tmp, "old.db")
        make_db(p, ITEMS, with_state_cols=False)
        # The remote already carries a block from an earlier run. A DB without the
        # columns must NOT read as "everything is UNSET" and wipe it.
        existing = "desc one\n\n[TB-STATES:BEGIN v1] x\nreproduction_state: reproduced-live\n[TB-STATES:END]"
        self.seed_atm(tasks=[remote_task("ATM-1", ITEMS["ATM-1"], markdown_description=existing)])
        r = self.run_tb(apply=True, db=p)
        self.assertRC(r, 0)
        self.assertIn("STATES DISABLED", r.stdout)
        self.assertEqual([e for e in self.writes() if e["method"] == "PUT"], [])
        self.assertEqual(self.task("L-ATM", "r-ATM-1")["markdown_description"], existing)
        self.assertRC(self.run_tb(apply=True, db=p, extra=["--states-strict"]), 2)

    def test_bad_spec_is_a_usage_error(self):
        self.seed_atm()
        bad = os.path.join(self.tmp, "bad.json")
        with open(bad, "w") as fh:
            fh.write('{"version": 9, "fields": []}')
        self.assertRC(self.run_tb(spec=bad), 2)
        self.assertRC(self.run_tb(spec=os.path.join(self.tmp, "missing.json")), 2)

    def test_states_only_without_spec_is_a_usage_error(self):
        self.seed_atm()
        self.assertRC(self.run_tb(spec=None, apply=True, extra=["--states-only"]), 2)


class TestStagedRollout(Base):
    def test_states_only_leaves_status_and_creates_alone(self):
        tasks = [remote_task("ATM-1", ITEMS["ATM-1"], status="complete"), remote_task("ATM-2", ITEMS["ATM-2"])]
        self.seed_atm(tasks=tasks)  # ATM-3 has no remote task (would be a CREATE)
        r = self.run_tb(apply=True, extra=["--states-only"])
        self.assertRC(r, 0)
        puts = [e for e in self.writes() if e["method"] == "PUT"]
        self.assertTrue(all(set(e["body"]) == {"markdown_description"} for e in puts), puts)
        self.assertEqual(self.task("L-ATM", "r-ATM-1")["status"], "complete")
        self.assertEqual([e for e in self.writes() if e["path"].endswith("/list/L-ATM/task")], [])
        r2 = self.run_tb(apply=True)
        self.assertRC(r2, 0)
        self.assertEqual(self.task("L-ATM", "r-ATM-1")["status"], "to do")

    def test_status_drift_with_states_in_one_pass_keeps_the_block(self):
        tasks = [remote_task("ATM-1", ITEMS["ATM-1"], status="complete")]
        self.seed_atm(tasks=tasks)
        self.assertRC(self.run_tb(apply=True), 0)
        t = self.task("L-ATM", "r-ATM-1")
        self.assertEqual(t["status"], "to do")
        self.assertIn("[TB-STATES:BEGIN", t["markdown_description"])
        self.assertTrue(t["markdown_description"].startswith("desc one"))
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True), 0)
        self.assertEqual(self.writes(n), [])


class TestAllBoards(Base):
    def test_three_boards_each_get_only_their_own_states_and_converge(self):
        lists = []
        for lid, pre in (("L-ATM", "ATM"), ("L-SPK", "SPK"), ("L-MVR", "MVR")):
            lists.append({"id": lid, "fields": [], "tasks": [remote_task(k, v) for k, v in ITEMS.items() if k.startswith(pre)]})
        self.seed(lists)
        env = {"TASK_BRIDGE_STATES_SPEC": SPEC_PATH}
        for lid, pre in (("L-ATM", "ATM"), ("L-SPK", "SPK"), ("L-MVR", "MVR")):
            self.assertRC(self.run_tb(list_id=lid, prefix=pre, apply=True, spec=None, env_extra=env), 0)
        self.assertIn("investigation_state: closed-no-defect", self.task("L-SPK", "r-SPK-1")["markdown_description"])
        self.assertIn("reproduction_state: blocked-no-device", self.task("L-MVR", "r-MVR-1")["markdown_description"])
        self.assertNotIn("closed-no-defect", self.task("L-ATM", "r-ATM-1")["markdown_description"])
        spk = self.task("L-SPK", "r-SPK-1")["markdown_description"]
        self.assertIn("investigation_root_cause: line1\\nline2 *bold* [x] C:\\\\tmp", spk)
        n = self.nlog()
        for lid, pre in (("L-ATM", "ATM"), ("L-SPK", "SPK"), ("L-MVR", "MVR")):
            self.assertRC(self.run_tb(list_id=lid, prefix=pre, apply=True, spec=None, env_extra=env), 0)
        self.assertEqual(self.writes(n), [])


class TestNeverDeletes(Base):
    def test_no_task_is_ever_deleted(self):
        self.seed_atm(fields=field_defs(only=["reproduction_state"]))
        self.run_tb(apply=True, extra=["--create-missing-fields"])
        self.run_tb(apply=True)
        for e in self.log():
            if e["method"] == "DELETE":
                self.assertIn("/field/", e["path"], "a DELETE other than a field value: %s" % e["path"])


class TestClamp(Base):
    def test_clamped_text_never_hits_the_remote_length_limit_and_converges(self):
        spec = {"version": 1, "fields": [{"key": "reproduction_state", "kind": "enum", "values": ["UNSET", "UNREPRODUCED", "reproduced-live"], "unset": "UNSET"},
                                         {"key": "reproduction_evidence", "kind": "text", "max_runes": 40}]}
        sp = os.path.join(self.tmp, "short.json")
        with open(sp, "w") as fh:
            json.dump(spec, fh)
        items = json.loads(json.dumps(ITEMS))
        items["ATM-1"]["reproduction_evidence"] = "e" * 200
        p = os.path.join(self.tmp, "long.db")
        make_db(p, items)
        fields = [{"id": "cf-reproduction_evidence", "name": "reproduction_evidence", "type": "text"},
                  {"id": "cf-reproduction_state", "name": "reproduction_state", "type": "drop_down", "options": ["UNREPRODUCED", "reproduced-live"]}]
        self.seed_atm(fields=fields, config={"max_text_len": 50})
        r = self.run_tb(apply=True, db=p, spec=sp)
        self.assertRC(r, 0)
        self.assertEqual(self.task("L-ATM", "r-ATM-1")["values"]["cf-reproduction_evidence"], "e" * 40)
        n = self.nlog()
        self.assertRC(self.run_tb(apply=True, db=p, spec=sp), 0)
        self.assertEqual(self.writes(n), [])


class TestUnchangedWithoutSpec(Base):
    def test_without_a_spec_the_run_is_the_original_one(self):
        self.seed_atm()
        r = self.run_tb(spec=None, apply=True)
        self.assertRC(r, 0)
        self.assertNotIn("state", r.stdout.lower().replace("status", ""))
        self.assertEqual(self.writes(), [])
        self.assertTrue(all("include_markdown_description" not in e["query"] for e in self.log() if e["path"].endswith("/task")))


if __name__ == "__main__":
    res = unittest.main(exit=False, verbosity=1)
    r = res.result
    print("E2E tests=%d failures=%d errors=%d" % (r.testsRun, len(r.failures), len(r.errors)))
    if _tmp_bin_dir:
        shutil.rmtree(_tmp_bin_dir, ignore_errors=True)
    sys.exit(0 if r.wasSuccessful() else 1)
