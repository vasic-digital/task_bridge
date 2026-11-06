#!/usr/bin/env python3
"""mock_clickup_server.py - an OFFLINE ClickUp API v2 stand-in for deterministic tests.

Purpose:      emulate exactly the endpoints task_bridge uses (tasks, tags, custom
              fields) with scripted fault injection, so every code path of the
              state-field sync can be driven without the real service or a token.
Usage:        python3 mock_clickup_server.py [--port N] [--token T] [--ready-file F]
              (as a library: MockClickUp(token).start() returns the base URL.)
Inputs:       HTTP requests under /api/v2/... ; admin requests under /__admin/...
Outputs:      JSON responses; a request log at GET /__admin/requests (method, path,
              query, body, status, auth_ok). The Authorization VALUE is never stored
              or echoed - only whether it matched.
Side-effects: none outside the process (in-memory state, one listening socket on
              127.0.0.1).
Dependencies: python3 standard library only.
Fidelity:     modelled on the public ClickUp v2 documentation and on the live
              behaviour recorded in the task_bridge sources (tags ignored by task
              update, 100 tasks per page, drop-down set by option id). Where the
              live behaviour is UNCONFIRMED the mock exposes it as a switch so both
              readings are tested: config.dropdown_value_style (orderindex|id),
              config.field_create (supported|404|400), config.last_page (true|false),
              faults escape_markdown / omit_markdown.

Admin API
  POST /__admin/reset   {"lists":[{"id","fields":[..],"tasks":[..]}],"config":{..},"faults":[..]}
  POST /__admin/faults  {"faults":[..]}                       append fault rules
  GET  /__admin/state                                          full state dump
  GET  /__admin/requests                                       request log

Fault rule: {"method":"GET|POST|PUT|DELETE|*", "path":"<regex>", "after":K, "times":N,
             "action":"status|short_page|escape_markdown|omit_markdown|plain_only|drop_last_page|ignore_write",
             "status":401|429|500|..., "retry_after":"0", "keep":N}
  "after" matching requests are skipped first; the rule then fires "times" times
  (-1 = forever). Counters are per rule, so a scenario is fully deterministic.
"""
import argparse
import json
import re
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlparse

PAGE = 100
MD_PUNCT = "*_[]()#+!|~<>`"
STATUSES = {"to do": "open", "in progress": "custom", "complete": "closed", "obsolete": "custom"}


def md_escape(text):
    """Simulate a Markdown re-serialiser: escape ordinary punctuation, keep \\\\ \\n \\t tokens."""
    out, i = [], 0
    while i < len(text):
        c = text[i]
        if c == "\\" and i + 1 < len(text) and text[i + 1] in "\\nt":
            out.append(c + text[i + 1])
            i += 2
            continue
        if c in MD_PUNCT:
            out.append("\\")
        out.append(c)
        i += 1
    return "".join(out)


def plain_of(md):
    """Naive plain-text rendering: CommonMark backslash-escapes are dropped."""
    return re.sub(r"\\([!\"#$%&'()*+,\-./:;<=>?@\[\\\]^_`{|}~])", r"\1", md or "")


class State:
    def __init__(self, token):
        self.token = token
        self.lock = threading.Lock()
        self.reset({})

    def reset(self, spec):
        self.lists = {}
        self.config = {"dropdown_value_style": "orderindex", "field_create": "supported",
                       "last_page": True, "max_text_len": 100000, "page_size": PAGE}
        self.config.update(spec.get("config", {}))
        self.faults = []
        self.log = []
        self.counter = 0
        self.clock = 1700000000000
        for lst in spec.get("lists", []):
            self.add_list(lst)
        self.add_faults(spec.get("faults", []))

    def tick(self):
        self.clock += 1000
        return str(self.clock)

    def add_faults(self, rules):
        for r in rules:
            r = dict(r)
            r.setdefault("method", "*")
            r.setdefault("after", 0)
            r.setdefault("times", 1)
            r["_seen"] = 0
            r["_fired"] = 0
            r["_re"] = re.compile(r["path"])
            self.faults.append(r)

    def add_list(self, spec):
        lid = spec["id"]
        fields = []
        for f in spec.get("fields", []):
            f = dict(f)
            f.setdefault("type_config", {})
            if f["type"] == "drop_down":
                opts = []
                for i, o in enumerate(f.get("options", [])):
                    opts.append({"id": "%s-opt%d" % (f["id"], i), "name": o, "orderindex": i, "color": None})
                f["type_config"] = {"options": opts}
            f.pop("options", None)
            fields.append(f)
        tasks = {}
        for t in spec.get("tasks", []):
            self.counter += 1
            tid = t.get("id") or "t%d" % self.counter
            tasks[tid] = {
                "id": tid, "name": t["name"], "markdown_description": t.get("markdown_description", t.get("description", "")),
                "status": t.get("status", "to do"), "tags": [x.lower() for x in t.get("tags", [])],
                "values": dict(t.get("values", {})), "date_updated": self.tick(), "order": self.counter,
            }
        self.lists[lid] = {"fields": fields, "tasks": tasks}

    # ---- rendering -------------------------------------------------------------
    def field_by_id(self, fid):
        for lst in self.lists.values():
            for f in lst["fields"]:
                if f["id"] == fid:
                    return lst, f
        return None, None

    def task_list(self, tid):
        for lid, lst in self.lists.items():
            if tid in lst["tasks"]:
                return lid, lst
        return None, None

    def render_task(self, lst, t, include_md, mods):
        md = t["markdown_description"]
        if "escape_markdown" in mods:
            md = md_escape(md)
        out = {
            "id": t["id"], "custom_id": None, "name": t["name"], "text_content": plain_of(t["markdown_description"]),
            "description": plain_of(t["markdown_description"]),
            "status": {"status": t["status"], "color": "#000000", "type": STATUSES.get(t["status"], "custom"), "orderindex": 0},
            "orderindex": t["order"], "date_created": "1700000000000", "date_updated": t["date_updated"],
            "tags": [{"name": x} for x in t["tags"]], "archived": False, "url": "http://mock/t/" + t["id"],
            "custom_fields": [],
        }
        if include_md and "omit_markdown" not in mods and "plain_only" not in mods:
            out["markdown_description"] = md
        if "plain_only" in mods:
            out["description"] = plain_of(t["markdown_description"])
        style = self.config["dropdown_value_style"]
        for f in lst["fields"]:
            cf = {"id": f["id"], "name": f["name"], "type": f["type"], "type_config": f["type_config"], "date_created": "1700000000000", "hide_from_guests": False}
            if f["id"] in t["values"]:
                v = t["values"][f["id"]]
                if f["type"] == "drop_down":
                    opts = f["type_config"]["options"]
                    if style == "id":
                        cf["value"] = opts[v]["id"]
                    else:
                        cf["value"] = v
                else:
                    cf["value"] = v
            out["custom_fields"].append(cf)
        return out


class Handler(BaseHTTPRequestHandler):
    server_version = "MockClickUp/1"
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # silence the default stderr access log
        pass

    # -- plumbing ---------------------------------------------------------------
    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b""
        if not raw:
            return None
        try:
            return json.loads(raw.decode("utf-8"))
        except ValueError:
            return {"__unparseable__": True}

    def _send(self, code, obj, headers=None):
        data = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(data)

    def _handle(self, method):
        st = self.server.state
        u = urlparse(self.path)
        q = parse_qs(u.query)
        body = self._body()
        with st.lock:
            if u.path.startswith("/__admin/"):
                return self._admin(st, method, u.path, body)
            auth_ok = self.headers.get("Authorization") == st.token
            entry = {"n": len(st.log) + 1, "method": method, "path": u.path, "query": {k: v[0] for k, v in q.items()},
                     "body": body, "auth_ok": auth_ok, "status": None}
            st.log.append(entry)
            mods = set()
            for r in st.faults:
                if r["method"] not in ("*", method) or not r["_re"].search(u.path):
                    continue
                r["_seen"] += 1
                if r["_seen"] <= r["after"]:
                    continue
                if r["times"] >= 0 and r["_fired"] >= r["times"]:
                    continue
                r["_fired"] += 1
                if r["action"] == "status":
                    entry["status"] = r["status"]
                    hdr = {}
                    if r["status"] == 429:
                        hdr["Retry-After"] = str(r.get("retry_after", "0"))
                    return self._send(r["status"], {"err": "injected fault", "ECODE": "MOCK_%d" % r["status"]}, hdr)
                mods.add(r["action"])
                if r["action"] == "short_page":
                    entry["short_keep"] = r.get("keep", 1)
            if not u.path.startswith("/api/v2/"):
                entry["status"] = 404
                return self._send(404, {"err": "route not found", "ECODE": "MOCK_404"})
            if not auth_ok:
                entry["status"] = 401
                return self._send(401, {"err": "Token invalid", "ECODE": "OAUTH_025"})
            # The upstream library addresses some resources with a trailing slash
            # (PUT task/<id>/); the real service accepts both, so the mock does too.
            code, obj = self._route(st, method, u.path[len("/api/v2/"):].rstrip("/"), q, body, mods, entry)
            entry["status"] = code
            return self._send(code, obj)

    # -- admin ------------------------------------------------------------------
    def _admin(self, st, method, path, body):
        if path == "/__admin/reset" and method == "POST":
            st.reset(body or {})
            return self._send(200, {"ok": True})
        if path == "/__admin/faults" and method == "POST":
            st.add_faults((body or {}).get("faults", []))
            return self._send(200, {"ok": True, "rules": len(st.faults)})
        if path == "/__admin/requests" and method == "GET":
            return self._send(200, {"requests": st.log})
        if path == "/__admin/state" and method == "GET":
            dump = {lid: {"fields": l["fields"], "tasks": l["tasks"]} for lid, l in st.lists.items()}
            return self._send(200, {"lists": dump, "config": st.config})
        return self._send(404, {"err": "unknown admin route"})

    # -- API --------------------------------------------------------------------
    def _route(self, st, method, path, q, body, mods, entry):
        parts = path.split("/")
        if parts == ["user"] and method == "GET":
            return 200, {"user": {"id": 1, "username": "mock"}}
        if parts == ["team"] and method == "GET":
            return 200, {"teams": [{"id": "1", "name": "mock"}]}
        if len(parts) == 3 and parts[0] == "list" and parts[2] == "task":
            lst = st.lists.get(parts[1])
            if lst is None:
                return 404, {"err": "List not found", "ECODE": "ITEM_137"}
            if method == "GET":
                return self._list_tasks(st, lst, q, mods, entry)
            if method == "POST":
                return self._create_task(st, lst, body)
        if len(parts) == 3 and parts[0] == "list" and parts[2] == "field":
            lst = st.lists.get(parts[1])
            if lst is None:
                return 404, {"err": "List not found", "ECODE": "ITEM_137"}
            if method == "GET":
                return 200, {"fields": lst["fields"]}
            if method == "POST":
                return self._create_field(st, lst, body)
        if len(parts) == 2 and parts[0] == "task":
            lid, lst = st.task_list(parts[1])
            if lst is None:
                return 404, {"err": "Task not found", "ECODE": "ITEM_013"}
            t = lst["tasks"][parts[1]]
            if method == "GET":
                return 200, st.render_task(lst, t, q.get("include_markdown_description", ["false"])[0] == "true", mods)
            if method == "PUT":
                if "ignore_write" in mods:
                    return 200, {"id": t["id"]}
                return self._update_task(st, t, body)
        if len(parts) == 4 and parts[0] == "task" and parts[2] == "tag":
            lid, lst = st.task_list(parts[1])
            if lst is None:
                return 404, {"err": "Task not found", "ECODE": "ITEM_013"}
            t = lst["tasks"][parts[1]]
            tag = unquote(parts[3]).lower()  # the real service decodes the path segment: a label with a space arrives as %20
            if method == "POST":
                if tag not in t["tags"]:
                    t["tags"].append(tag)
                t["date_updated"] = st.tick()
                return 200, {}
            if method == "DELETE":
                if tag in t["tags"]:
                    t["tags"].remove(tag)
                t["date_updated"] = st.tick()
                return 200, {}
        if len(parts) == 4 and parts[0] == "task" and parts[2] == "field":
            if "ignore_write" in mods:
                return 200, {}
            return self._task_field(st, method, parts[1], parts[3], body)
        return 404, {"err": "route not found", "ECODE": "MOCK_404"}

    def _list_tasks(self, st, lst, q, mods, entry):
        page = int(q.get("page", ["0"])[0])
        include_md = q.get("include_markdown_description", ["false"])[0] == "true"
        ordered = sorted(lst["tasks"].values(), key=lambda t: t["order"])
        size = st.config["page_size"]
        chunk = ordered[page * size:(page + 1) * size]
        last = (page + 1) * size >= len(ordered)
        if "short_page" in mods:
            chunk = chunk[:entry.get("short_keep", 1)]
            last = False
        resp = {"tasks": [st.render_task(lst, t, include_md, mods) for t in chunk]}
        if st.config["last_page"] and "drop_last_page" not in mods:
            resp["last_page"] = last
        return 200, resp

    def _status_ok(self, name):
        return name in STATUSES

    def _create_task(self, st, lst, body):
        body = body or {}
        status = body.get("status") or "to do"
        if not self._status_ok(status):
            return 400, {"err": "Status does not exist", "ECODE": "CRTSK_001"}
        st.counter += 1
        tid = "t%d" % st.counter
        md = body.get("markdown_description")
        if md is None:
            md = body.get("description", "")
        lst["tasks"][tid] = {"id": tid, "name": body.get("name", ""), "markdown_description": md, "status": status,
                             "tags": [x.lower() for x in body.get("tags", [])], "values": {}, "date_updated": st.tick(), "order": st.counter}
        for cf in body.get("custom_fields", []):
            self._set_value(st, lst["tasks"][tid], lst, cf.get("id"), cf.get("value"))
        return 200, st.render_task(lst, lst["tasks"][tid], True, set())

    def _update_task(self, st, t, body):
        body = body or {}
        if "name" in body:
            t["name"] = body["name"]
        if "status" in body:
            if not self._status_ok(body["status"]):
                return 400, {"err": "Status does not exist", "ECODE": "ITEM_015"}
            t["status"] = body["status"]
        # ClickUp ignores "tags" in the update body (verified live 2026-07-27 in the bridge).
        if "markdown_description" in body:
            t["markdown_description"] = body["markdown_description"]
        elif "description" in body:
            t["markdown_description"] = body["description"]
        t["date_updated"] = st.tick()
        return 200, {"id": t["id"]}

    def _create_field(self, st, lst, body):
        mode = st.config["field_create"]
        if mode == "404":
            return 404, {"err": "route not found", "ECODE": "MOCK_404"}
        if mode == "400":
            return 400, {"err": "field creation is not permitted for this token", "ECODE": "FIELD_001"}
        body = body or {}
        if any(f["name"].lower() == str(body.get("name", "")).lower() for f in lst["fields"]):
            return 400, {"err": "a field with this name already exists", "ECODE": "FIELD_002"}
        st.counter += 1
        fid = "cf%d" % st.counter
        f = {"id": fid, "name": body["name"], "type": body["type"], "type_config": {}}
        if body["type"] == "drop_down":
            opts = body.get("type_config", {}).get("options", [])
            f["type_config"] = {"options": [{"id": "%s-opt%d" % (fid, i), "name": o["name"], "orderindex": i, "color": None} for i, o in enumerate(opts)]}
        lst["fields"].append(f)
        return 200, {"field": f}

    def _set_value(self, st, t, lst, fid, value):
        f = next((x for x in lst["fields"] if x["id"] == fid), None)
        if f is None:
            return 404, {"err": "Field not found", "ECODE": "FIELD_010"}
        if f["type"] == "drop_down":
            for o in f["type_config"]["options"]:
                if o["id"] == value:
                    t["values"][fid] = o["orderindex"]
                    t["date_updated"] = st.tick()
                    return 200, {}
            return 400, {"err": "Value not found", "ECODE": "FIELD_011"}
        if f["type"] in ("text", "short_text"):
            if not isinstance(value, str):
                return 400, {"err": "Value must be a string", "ECODE": "FIELD_012"}
            if len(value) > st.config["max_text_len"]:
                return 400, {"err": "Value too long", "ECODE": "FIELD_013"}
            t["values"][fid] = value
            t["date_updated"] = st.tick()
            return 200, {}
        return 400, {"err": "unsupported field type", "ECODE": "FIELD_014"}

    def _task_field(self, st, method, tid, fid, body):
        lid, lst = st.task_list(tid)
        if lst is None:
            return 404, {"err": "Task not found", "ECODE": "ITEM_013"}
        t = lst["tasks"][tid]
        if method == "POST":
            if not isinstance(body, dict) or "value" not in body:
                return 400, {"err": "value required", "ECODE": "FIELD_015"}
            return self._set_value(st, t, lst, fid, body["value"])
        if method == "DELETE":
            if not any(x["id"] == fid for x in lst["fields"]):
                return 404, {"err": "Field not found", "ECODE": "FIELD_010"}
            t["values"].pop(fid, None)
            t["date_updated"] = st.tick()
            return 200, {}
        return 404, {"err": "route not found", "ECODE": "MOCK_404"}

    def do_GET(self):
        self._handle("GET")

    def do_POST(self):
        self._handle("POST")

    def do_PUT(self):
        self._handle("PUT")

    def do_DELETE(self):
        self._handle("DELETE")


class MockClickUp:
    """Library handle: start() -> base URL ending in /api/v2/ ; stop()."""

    def __init__(self, token="MOCK-TOKEN-NOT-A-REAL-CREDENTIAL", port=0):
        self.state = State(token)
        self.httpd = ThreadingHTTPServer(("127.0.0.1", port), Handler)
        self.httpd.state = self.state
        self.httpd.daemon_threads = True
        self.thread = None

    @property
    def port(self):
        return self.httpd.server_address[1]

    def start(self):
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()
        return "http://127.0.0.1:%d/api/v2/" % self.port

    def stop(self):
        self.httpd.shutdown()
        self.httpd.server_close()


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--port", type=int, default=0)
    ap.add_argument("--token", default="MOCK-TOKEN-NOT-A-REAL-CREDENTIAL")
    ap.add_argument("--ready-file", default="")
    a = ap.parse_args()
    m = MockClickUp(a.token, a.port)
    url = m.start()
    if a.ready_file:
        with open(a.ready_file, "w") as fh:
            fh.write(url + "\n")
    print("mock ClickUp listening on", url, flush=True)
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        m.stop()


if __name__ == "__main__":
    sys.exit(main())
