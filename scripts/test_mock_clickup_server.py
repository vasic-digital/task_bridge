#!/usr/bin/env python3
"""test_mock_clickup_server.py - self-test of the offline mock (test infrastructure must itself be right).

Purpose:      prove the mock's token check, task/tag/custom-field semantics and its
              fault-injection rules behave as documented, so the E2E suite's verdicts
              rest on a verified stand-in.
Usage:        python3 scripts/test_mock_clickup_server.py [TestClass.test_name ...]
Outputs:      unittest report; the final line is "MOCK-SELFTEST tests=N failures=F errors=E".
Side-effects: a loopback socket only.
Dependencies: python3 standard library.
"""
import json
import os
import sys
import unittest
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mock_clickup_server import MockClickUp, md_escape, plain_of  # noqa: E402

TOKEN = "SELFTEST-TOKEN-VALUE"


class Base(unittest.TestCase):
    def setUp(self):
        self.m = MockClickUp(TOKEN)
        self.base = self.m.start()
        self.m.state.reset({"lists": [{"id": "L", "fields": [
            {"id": "f1", "name": "state", "type": "drop_down", "options": ["A", "B"]},
            {"id": "f2", "name": "env", "type": "text"}],
            "tasks": [{"id": "t1", "name": "[ATM-1] x", "markdown_description": "d", "tags": ["Bug"]}]}]})

    def tearDown(self):
        self.m.stop()

    def call(self, method, path, body=None, token=TOKEN):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Authorization", token)
        if data:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req) as r:
                return r.status, json.loads(r.read() or b"{}"), r.headers
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read() or b"{}"), e.headers

    def admin(self, method, path, body=None):
        root = self.base[: -len("api/v2/")]
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(root + path, data=data, method=method)
        with urllib.request.urlopen(req) as r:
            return json.loads(r.read())


class TestMockBasics(Base):
    def test_wrong_token_is_401(self):
        code, body, _ = self.call("GET", "user", token="WRONG")
        self.assertEqual(code, 401)
        self.assertEqual(body["ECODE"], "OAUTH_025")
        self.assertEqual(self.call("GET", "user")[0], 200)

    def test_request_log_never_holds_the_token(self):
        self.call("GET", "user")
        self.call("GET", "user", token="WRONG-ONE")
        dump = json.dumps(self.admin("GET", "__admin/requests"))
        self.assertNotIn(TOKEN, dump)
        self.assertNotIn("WRONG-ONE", dump)
        self.assertIn('"auth_ok": true', dump)

    def test_list_pagination_and_last_page(self):
        tasks = [{"name": "[ATM-%d] t" % i} for i in range(250)]
        self.m.state.reset({"lists": [{"id": "L", "fields": [], "tasks": tasks}]})
        sizes = []
        for p in range(3):
            _, b, _ = self.call("GET", "list/L/task?page=%d" % p)
            sizes.append((len(b["tasks"]), b["last_page"]))
        self.assertEqual(sizes, [(100, False), (100, False), (50, True)])

    def test_markdown_only_when_asked(self):
        _, b, _ = self.call("GET", "list/L/task")
        self.assertNotIn("markdown_description", b["tasks"][0])
        _, b, _ = self.call("GET", "list/L/task?include_markdown_description=true")
        self.assertEqual(b["tasks"][0]["markdown_description"], "d")

    def test_update_ignores_tags(self):
        self.call("PUT", "task/t1/", {"tags": ["x", "y"], "name": "new"})
        _, b, _ = self.call("GET", "task/t1")
        self.assertEqual([t["name"] for t in b["tags"]], ["bug"])
        self.assertEqual(b["name"], "new")

    def test_tags_are_lowercased_and_added_removed_via_endpoints(self):
        self.call("POST", "task/t1/tag/Status:Queued")
        _, b, _ = self.call("GET", "task/t1")
        self.assertIn("status:queued", [t["name"] for t in b["tags"]])
        self.call("DELETE", "task/t1/tag/Status:Queued")
        _, b, _ = self.call("GET", "task/t1")
        self.assertNotIn("status:queued", [t["name"] for t in b["tags"]])

    def test_tag_names_are_percent_decoded(self):
        # a label with a space (status:In progress) is sent as status:In%20progress and must be stored as the decoded, lowercased name
        self.call("POST", "task/t1/tag/status:In%20progress")
        _, b, _ = self.call("GET", "task/t1")
        names = [t["name"] for t in b["tags"]]
        self.assertIn("status:in progress", names)
        self.assertNotIn("status:in%20progress", names)
        self.call("DELETE", "task/t1/tag/status:In%20progress")
        _, b, _ = self.call("GET", "task/t1")
        self.assertNotIn("status:in progress", [t["name"] for t in b["tags"]])

    def test_dropdown_set_requires_option_id(self):
        self.assertEqual(self.call("POST", "task/t1/field/f1", {"value": "A"})[0], 400)
        self.assertEqual(self.call("POST", "task/t1/field/f1", {"value": "f1-opt1"})[0], 200)
        _, b, _ = self.call("GET", "task/t1")
        cf = [c for c in b["custom_fields"] if c["id"] == "f1"][0]
        self.assertEqual(cf["value"], 1)  # orderindex style by default
        self.assertEqual(self.call("DELETE", "task/t1/field/f1")[0], 200)
        _, b, _ = self.call("GET", "task/t1")
        self.assertNotIn("value", [c for c in b["custom_fields"] if c["id"] == "f1"][0])

    def test_dropdown_value_style_id(self):
        self.m.state.config["dropdown_value_style"] = "id"
        self.call("POST", "task/t1/field/f1", {"value": "f1-opt0"})
        _, b, _ = self.call("GET", "task/t1")
        self.assertEqual([c for c in b["custom_fields"] if c["id"] == "f1"][0]["value"], "f1-opt0")

    def test_text_length_limit(self):
        self.m.state.config["max_text_len"] = 5
        self.assertEqual(self.call("POST", "task/t1/field/f2", {"value": "123456"})[0], 400)
        self.assertEqual(self.call("POST", "task/t1/field/f2", {"value": "12345"})[0], 200)

    def test_create_field_modes(self):
        body = {"name": "n", "type": "text"}
        self.assertEqual(self.call("POST", "list/L/field", body)[0], 200)
        self.assertEqual(self.call("POST", "list/L/field", body)[0], 400)  # duplicate name
        self.m.state.config["field_create"] = "404"
        self.assertEqual(self.call("POST", "list/L/field", {"name": "m", "type": "text"})[0], 404)
        self.m.state.config["field_create"] = "400"
        self.assertEqual(self.call("POST", "list/L/field", {"name": "m", "type": "text"})[0], 400)

    def test_create_task_validates_status(self):
        self.assertEqual(self.call("POST", "list/L/task", {"name": "n", "status": "bogus"})[0], 400)
        code, b, _ = self.call("POST", "list/L/task", {"name": "n", "status": "to do", "markdown_description": "md"})
        self.assertEqual(code, 200)
        self.assertEqual(b["markdown_description"], "md")

    def test_markdown_helpers(self):
        self.assertEqual(md_escape("a_b*c\\\\d\\n"), "a\\_b\\*c\\\\d\\n")
        self.assertEqual(plain_of("a\\_b\\*c"), "a_b*c")


class TestFaultInjection(Base):
    def rule(self, **kw):
        base = {"method": "GET", "path": "/user", "action": "status", "status": 500}
        base.update(kw)
        self.admin("POST", "__admin/faults", {"faults": [base]})

    def test_times_limits_firing(self):
        self.rule(times=2)
        self.assertEqual([self.call("GET", "user")[0] for _ in range(4)], [500, 500, 200, 200])

    def test_after_skips_matching_requests(self):
        self.rule(after=2, times=1)
        self.assertEqual([self.call("GET", "user")[0] for _ in range(4)], [200, 200, 500, 200])

    def test_forever_and_method_match(self):
        self.rule(times=-1, method="POST", path="/field/")
        self.assertEqual(self.call("POST", "task/t1/field/f2", {"value": "x"})[0], 500)
        self.assertEqual(self.call("POST", "task/t1/field/f2", {"value": "x"})[0], 500)
        self.assertEqual(self.call("GET", "task/t1")[0], 200)

    def test_429_carries_retry_after(self):
        self.rule(status=429, retry_after="3")
        code, _, hdr = self.call("GET", "user")
        self.assertEqual((code, hdr["Retry-After"]), (429, "3"))

    def test_short_page_and_drop_last_page_and_omit_markdown(self):
        tasks = [{"name": "[ATM-%d] t" % i, "markdown_description": "m"} for i in range(30)]
        self.m.state.reset({"lists": [{"id": "L", "fields": [], "tasks": tasks}]})
        self.rule(path="/list/L/task", action="short_page", keep=5, times=1)
        _, b, _ = self.call("GET", "list/L/task?page=0")
        self.assertEqual((len(b["tasks"]), b["last_page"]), (5, False))
        _, b, _ = self.call("GET", "list/L/task?page=0")
        self.assertEqual(len(b["tasks"]), 30)
        self.rule(path="/list/L/task", action="drop_last_page", times=-1)
        _, b, _ = self.call("GET", "list/L/task?page=0")
        self.assertNotIn("last_page", b)
        self.rule(path="/list/L/task", action="omit_markdown", times=-1)
        _, b, _ = self.call("GET", "list/L/task?page=0&include_markdown_description=true")
        self.assertNotIn("markdown_description", b["tasks"][0])

    def test_escape_markdown_and_ignore_write(self):
        self.call("PUT", "task/t1", {"markdown_description": "a_b*c"})
        self.rule(path="/task/t1", action="escape_markdown", times=-1)
        _, b, _ = self.call("GET", "task/t1?include_markdown_description=true")
        self.assertEqual(b["markdown_description"], "a\\_b\\*c")
        self.rule(method="PUT", path="/task/t1", action="ignore_write", times=-1)
        self.call("PUT", "task/t1", {"markdown_description": "changed"})
        self.assertEqual(self.m.state.lists["L"]["tasks"]["t1"]["markdown_description"], "a_b*c")


if __name__ == "__main__":
    res = unittest.main(exit=False, verbosity=1)
    r = res.result
    print("MOCK-SELFTEST tests=%d failures=%d errors=%d" % (r.testsRun, len(r.failures), len(r.errors)))
    sys.exit(0 if r.wasSuccessful() else 1)
