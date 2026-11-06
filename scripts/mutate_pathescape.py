#!/usr/bin/env python3
"""mutate_pathescape.py - paired mutations of the request-path hardening in pkg/client/clickup.go.

Purpose:      prove that pkg/client/pathseg_test.go FAILS when any one of the escapes or refusals in the
              library-backed task id / tag name calls is removed (a test that passes on a broken client is a bluff).
Usage:        python3 scripts/mutate_pathescape.py        (needs go and the offline module cache; GOFLAGS=-mod=mod GOPROXY=off are set)
Method:       the task_bridge tree is copied to a temp dir; per mutant ONE exact-substring edit, scoped to one function of
              clickup.go, must match exactly once; then `go test -run <guard tests> ./pkg/client` runs. A mutant is KILLED when
              the guard tests fail while the code still COMPILES (a compile error is INVALID: it proves nothing about the test).
Control:      before any mutant the unedited copy must PASS (needle: the harness can tell pass from fail), and one deliberately
              neutral edit (a comment) must SURVIVE.
Exit:         0 all mutants KILLED, control PASS, neutral edit SURVIVED | 1 otherwise.
"""
import os
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
TESTS = "TestPathSegmentsOfRemoteValuesAreEscaped|TestDotSegmentsAndEmptyAreRefusedNotSent"
TARGET = "pkg/client/clickup.go"

# (id, function marker, old, new)
MUTANTS = [
    ("P01-AddTag-tag-unescaped", "func (c *clickupClient) AddTag(", 'name, err := pathSeg("tag name", tag)', "name, err := tag, error(nil)"),
    ("P02-AddTag-id-unescaped", "func (c *clickupClient) AddTag(", 'id, err := pathSeg("task id", taskID)', "id, err := taskID, error(nil)"),
    ("P03-RemoveTag-tag-unescaped", "func (c *clickupClient) RemoveTag(", 'name, err := pathSeg("tag name", tag)', "name, err := tag, error(nil)"),
    ("P04-RemoveTag-id-unescaped", "func (c *clickupClient) RemoveTag(", 'id, err := pathSeg("task id", taskID)', "id, err := taskID, error(nil)"),
    ("P05-UpdateTask-id-unescaped", "func (c *clickupClient) UpdateTask(", 'id, err := pathSeg("task id", t.ID)', "id, err := t.ID, error(nil)"),
    ("P06-GetTask-id-unescaped", "func (c *clickupClient) GetTask(", 'id, err := pathSeg("task id", taskID)', "id, err := taskID, error(nil)"),
    ("P07-escape-removed", "func pathSeg(", "return url.PathEscape(v), nil", "return v, nil"),
    ("P08-dot-guard-removed", "func pathSeg(", 'if v == "" || v == "." || v == ".." {', "if false {"),
    ("P09-dotdot-allowed", "func pathSeg(", 'v == ".."', 'v == "..\\x00"'),
    ("P10-empty-allowed", "func pathSeg(", 'if v == "" || ', "if "),
]
NEUTRAL = ("N01-comment-only", "func pathSeg(", "func pathSeg(what, v string) (string, error) {", "func pathSeg(what, v string) (string, error) {\n\t// neutral edit")


def run_tests(tree):
    env = dict(os.environ, GOFLAGS="-mod=mod", GOPROXY="off")
    p = subprocess.run(["go", "test", "-count=1", "-run", TESTS, "./pkg/client"], cwd=tree, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600)
    return p.returncode, p.stdout.decode("utf-8", "replace")


def apply(tree, fn_marker, old, new):
    path = os.path.join(tree, TARGET)
    src = open(path, encoding="utf-8").read()
    start = src.find(fn_marker)
    if start < 0 or src.count(fn_marker) != 1:
        return "INVALID", "function marker %r matches %d times" % (fn_marker, src.count(fn_marker))
    end = src.find("\n}\n", start)
    body = src[start:end]
    if body.count(old) != 1:
        return "INVALID", "the edit matches %d times in %s, want exactly 1" % (body.count(old), fn_marker)
    open(path, "w", encoding="utf-8").write(src[:start] + body.replace(old, new) + src[end:])
    return None, ""


def fresh(work, name):
    d = os.path.join(work, name)
    shutil.copytree(ROOT, d, ignore=shutil.ignore_patterns(".git", "bin", "__pycache__"))
    return d


def main():
    work = tempfile.mkdtemp(prefix="mut_pathescape.")
    try:
        rc, out = run_tests(fresh(work, "control"))
        if rc != 0:
            print("HARNESS-NEEDLE FAILED: the unedited tree does not pass the guard tests:\n" + out[-1500:])
            return 1
        mid, fn, old, new = NEUTRAL
        d = fresh(work, "neutral")
        bad, why = apply(d, fn, old, new)
        if bad:
            print("HARNESS-NEEDLE FAILED: neutral edit %s is INVALID (%s)" % (mid, why))
            return 1
        nrc, nout = run_tests(d)
        if nrc != 0:
            print("HARNESS-NEEDLE FAILED: neutral edit %s made the guard tests fail:\n%s" % (mid, nout[-800:]))
            return 1
        print("HARNESS-NEEDLE ok (unedited tree PASS; neutral edit %s SURVIVED as it must)" % mid)
        killed = bad_n = 0
        for mid, fn, old, new in MUTANTS:
            d = fresh(work, mid)
            bad, why = apply(d, fn, old, new)
            if bad:
                print("MUTANT %s INVALID (%s)" % (mid, why))
                bad_n += 1
                continue
            rc, out = run_tests(d)
            if rc == 0:
                print("MUTANT %s SURVIVED: the guard tests passed on the broken client" % mid)
                bad_n += 1
            elif re.search(r"build failed|\[build failed\]|cannot use|undefined:|declared and not used|syntax error", out):
                print("MUTANT %s INVALID (does not compile, proves nothing): %s" % (mid, out.strip().splitlines()[-1][:160]))
                bad_n += 1
            else:
                first = [ln.strip() for ln in out.splitlines() if "pathseg_test.go" in ln][:1]
                print("MUTANT %s KILLED by %s" % (mid, first[0][:170] if first else "a guard test"))
                killed += 1
        print("MUTANTS killed=%d not_killed_or_invalid=%d total=%d" % (killed, bad_n, len(MUTANTS)))
        return 0 if bad_n == 0 else 1
    finally:
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
