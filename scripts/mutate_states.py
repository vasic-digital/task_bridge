#!/usr/bin/env python3
"""mutate_states.py - paired mutation proof for the state-field sync.

Purpose:      each mutant breaks ONE load-bearing behaviour in a scratch COPY of the
              module (an exact, unique source substring is replaced); the named test(s)
              MUST fail. A mutant that survives means a test proves nothing. The real
              tree is never edited.
Usage:        python3 scripts/mutate_states.py [--jobs N] [--only ID[,ID]] [--list]
Output:       one line per mutant (KILLED / SURVIVED / UNRESOLVED), then the summary line
                MUTATION killed=N survived=S unresolved=U total=T control=OK
              Exit 0 only when S == 0, U == 0 and the control (unmutated copy) is green.
Unresolved:   the anchor text was not found exactly once, the mutant does not compile, or
              a killer test name matches no test in the control run.
Dependencies: python3, go, sqlite3 CLI (the E2E killers also use the sqlite3 python module).
"""
import argparse
import concurrent.futures as cf
import os
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENV = dict(os.environ)
ENV.setdefault("GOFLAGS", "-mod=mod")
ENV.setdefault("GOPROXY", "off")


def G(i, f, old, new, pkg, run):
    return dict(id=i, file=f, old=old, new=new, kind="go", pkg=pkg, run=run)


def E(i, f, old, new, tests):
    return dict(id=i, file=f, old=old, new=new, kind="e2e", tests=tests)


def P(i, f, old, new, tests):
    return dict(id=i, file=f, old=old, new=new, kind="py", tests=tests)


ST = "./pkg/states"
SE = "./pkg/syncengine"
CL = "./pkg/client"
LS = "./pkg/localstore"
CM = "./cmd/task_bridge"

MUTANTS = [
    # ---- pkg/states/spec.go
    G("S01-dup-key-allowed", "pkg/states/spec.go", "if seen[f.Key] {", "if false {", ST, "TestLoadSpecRefusals"),
    G("S02-unset-not-checked", "pkg/states/spec.go", 'if f.Unset == "" || !vs[f.Unset] {', 'if f.Unset == "" {', ST, "TestLoadSpecRefusals"),
    G("S03-enum-value-charset-open", "pkg/states/spec.go", "enumValRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)", "enumValRe = regexp.MustCompile(`^.*$`)", ST, "TestLoadSpecRefusals"),
    G("S04-unknown-members-allowed", "pkg/states/spec.go", "dec.DisallowUnknownFields()", "", ST, "TestLoadSpecRefusals"),
    G("S05-version-unchecked", "pkg/states/spec.go", "if s.Version != SpecVersion {", "if false {", ST, "TestLoadSpecRefusals"),
    G("S06-enum-needs-no-choice", "pkg/states/spec.go", "if len(f.Values) < 2 {", "if len(f.Values) < 1 {", ST, "TestLoadSpecRefusals"),
    # ---- pkg/states/values.go
    G("V01-unknown-state-accepted", "pkg/states/values.go", "ok := false", "ok := true", ST, "TestUnknownStateRefused"),
    G("V02-empty-enum-not-unset", "pkg/states/values.go", "res.Values[f.Key] = f.Unset", 'res.Values[f.Key] = ""', ST, "TestNormalizeEnum|TestNormalizeAbsentKeysAreUnset"),
    G("V03-enum-case-insensitive", "pkg/states/values.go", "if t == allowed {", "if strings.EqualFold(t, allowed) {", ST, "TestUnknownStateRefused"),
    G("V04-clamp-off-by-one", "pkg/states/values.go", "utf8.RuneCountInString(t) > lim {", "utf8.RuneCountInString(t) >= lim {", ST, "TestNormalizeTextClamp"),
    G("V05-text-not-trimmed", "pkg/states/values.go", "return strings.TrimSpace(s), nil", "return s, nil", ST, "TestNormalizeTextCanonical|TestPropNormalizeIdempotent"),
    G("V06-control-chars-allowed", "pkg/states/values.go", "if r == '\\n' || r == '\\t' {", "if r < 0x20 {", ST, "TestNormalizeTextCanonical"),
    G("V07-nbsp-not-folded", "pkg/states/values.go", 's = strings.ReplaceAll(s, "\\u00a0", " ")', "", ST, "TestNormalizeTextCanonical"),
    G("V08-crlf-kept", "pkg/states/values.go", 's := strings.ReplaceAll(raw, "\\r\\n", "\\n")', "s := raw", ST, "TestNormalizeTextCanonical"),
    # ---- pkg/states/block.go
    G("B01-backslash-not-escaped", "pkg/states/block.go", "b.WriteString(`\\\\`)", "b.WriteRune(r)", ST, "TestEncodeDecodeProperty|TestBlockRoundTripThroughChannels|TestRenderBlockGolden"),
    G("B02-markdown-escape-not-tolerated", "pkg/states/block.go", "case n < 0x80 && strings.ContainsRune(asciiPunct, n):", "case false:", ST, "TestBlockRoundTripThroughChannels"),
    G("B03-escaped-markers-missed", "pkg/states/block.go", 'loose := strings.ReplaceAll(strings.TrimSpace(line), `\\`, "")', "loose := strings.TrimSpace(line)", ST, "TestEscapedMarkersRecognised|TestBlockRoundTripThroughChannels"),
    G("B04-unterminated-block-not-malformed", "pkg/states/block.go", "if inside {", "if false {", ST, "TestParseMalformed"),
    G("B05-stray-end-not-malformed", "pkg/states/block.go", "case e && !inside:", "case false:", ST, "TestParseMalformed"),
    G("B06-nested-begin-not-malformed", "pkg/states/block.go", "case b && inside:", "case false:", ST, "TestParseMalformed"),
    G("B07-compose-single-newline", "pkg/states/block.go", 'return b + "\\n\\n" + block', 'return b + "\\n" + block', ST, "TestComposeCases|TestRenderBlockGolden|TestComposeParseRoundTripProperty"),
    G("B08-compose-keeps-trailing-blanks", "pkg/states/block.go", 'b := strings.TrimRight(base, " \\t\\r\\n")', "b := base", ST, "TestComposeCases|TestComposeParseRoundTripProperty"),
    G("B09-unset-lines-always-rendered", "pkg/states/block.go", "if s.IsUnset(f.Key, v) && !materializeUnset {", "if false {", ST, "TestRenderBlockEmptyWhenAllUnset|TestRenderBlockGolden"),
    G("B10-render-ignores-key-filter", "pkg/states/block.go", "if !want[f.Key] {", "if false {", ST, "TestRenderBlockGolden"),
    G("B11-values-not-encoded", "pkg/states/block.go", 'lines = append(lines, strings.TrimRight(f.Key+": "+encodeText(v), " "))', 'lines = append(lines, f.Key+": "+v)', ST, "TestRenderBlockGolden|TestBlockRoundTripThroughChannels|TestMarkerInjectionImpossible"),
    G("B12-escaped-keys-missed", "pkg/states/block.go", 'key = strings.TrimSpace(strings.ReplaceAll(t[:i], `\\`, ""))', "key = strings.TrimSpace(t[:i])", ST, "TestEscapedMarkersRecognised|TestBlockRoundTripThroughChannels"),
    G("B13-foreign-lines-uncounted", "pkg/states/block.go", "pb.Foreign++", "", ST, "TestParseDuplicateForeignUnknown"),
    # ---- pkg/states/carriers.go
    G("C01-name-match-case-sensitive", "pkg/states/carriers.go", "n = strings.ToLower(strings.TrimSpace(n))", "n = strings.TrimSpace(n)", ST, "TestNormalizeName|TestResolveExtraOptionsAllowedAndReported"),
    G("C02-ambiguous-name-picks-first", "pkg/states/carriers.go", "case len(cands) > 1:", "case false:", ST, "TestResolveMixedAndBadFields"),
    G("C03-missing-options-accepted", "pkg/states/carriers.go", "if len(missing) > 0 {", "if false {", ST, "TestResolveMixedAndBadFields"),
    G("C04-dropdown-type-unchecked", "pkg/states/carriers.go", 'if d.Type != "drop_down" {', "if false {", ST, "TestResolveMixedAndBadFields"),
    G("C05-field-mode-not-fail-closed", "pkg/states/carriers.go", "if mode == ModeField && len(unresolved) > 0 {", "if false {", ST, "TestResolveModes"),
    G("C06-block-mode-uses-fields", "pkg/states/carriers.go", "if mode == ModeBlock {", "if false {", ST, "TestResolveModes"),
    G("C07-text-type-unchecked", "pkg/states/carriers.go", 'if d.Type != "text" && d.Type != "short_text" {', "if false {", ST, "TestResolveTextFieldOfWrongType"),
    # ---- pkg/states/diff.go
    G("D01-field-always-drifts", "pkg/states/diff.go", "have := haveFieldValue(f, rf)\n\t\tif have == w {", "have := haveFieldValue(f, rf)\n\t\tif false {", ST, "TestComputeFieldMatrix|TestPropApplyTwiceIsZeroDiff"),
    G("D02-block-value-drift-lost", "pkg/states/diff.go", "lineChanges = append(lineChanges, Change{Key: key, Carrier: CarrierBlock, Op: op, Have: have, Want: w})", "_ = op", ST, "TestComputeBlockMatrix|TestPropApplyTwiceIsZeroDiff"),
    G("D03-block-never-removed", "pkg/states/diff.go", "case !wantNonEmpty && pb.Count > 0:", "case false:", ST, "TestComputeBlockMatrix"),
    G("D04-malformed-block-rewritten", "pkg/states/diff.go", "case pb.Malformed:", "case false:", ST, "TestComputeBlockMatrix"),
    G("D05-plain-source-rewritten", "pkg/states/diff.go", "case rs.DescSource == DescPlain:", "case false:", ST, "TestComputeBlockMatrix"),
    G("D06-duplicate-block-kept", "pkg/states/diff.go", "structural := pb.Count > 1 || pb.Foreign > 0", "structural := pb.Foreign > 0", ST, "TestComputeBlockMatrix"),
    G("D07-stale-key-kept", "pkg/states/diff.go", "stale = true", "stale = false", ST, "TestComputeStaleKeyAfterBlockToFieldMigration"),
    G("D08-materialize-not-added", "pkg/states/diff.go", "if !(opt.MaterializeUnset && pb.Count == 0) {", "if true {", ST, "TestComputeMaterializeUnset"),
    G("D09-unresolved-option-hidden", "pkg/states/diff.go", 'if rf.OptionName == "" {', "if false {", ST, "TestComputeFieldMatrix"),
    G("D10-clear-sent-as-set", "pkg/states/diff.go", "Carrier: CarrierField, Op: op,", "Carrier: CarrierField, Op: func() Op { _ = op; return OpSet }(),", ST, "TestComputeFieldMatrix"),
    G("D11-long-values-flood-the-report", "pkg/states/diff.go", "if len(r) <= maxShown {", "if true {", ST, "TestDiffSummaryShortensLongValues"),
    # ---- pkg/syncengine/reconcile.go
    G("R01-state-drift-still-in-sync", "pkg/syncengine/reconcile.go", "e.InSync = false", "e.InSync = e.InSync", SE, "TestPlanStatesDriftBreaksInSync"),
    G("R02-states-only-still-creates", "pkg/syncengine/reconcile.go", "if !opts.StatesOnly {", "if true {", SE, "TestStatesOnlyTouchesNothingElse"),
    G("R03-states-only-pushes-column", "pkg/syncengine/reconcile.go", "li.Status) && !opts.StatesOnly", "li.Status)", SE, "TestStatesOnlyTouchesNothingElse"),
    G("R04-states-only-pushes-title", "pkg/syncengine/reconcile.go", "else if e.TitleDrift && !opts.StatesOnly {", "else if e.TitleDrift {", SE, "TestStatesOnlyTouchesNothingElse"),
    G("R05-states-only-pushes-labels", "pkg/syncengine/reconcile.go", "hasLbl && !opts.StatesOnly {", "hasLbl {", SE, "TestStatesOnlyTouchesNothingElse"),
    G("R06-auth-rejection-keeps-going", "pkg/syncengine/reconcile.go", "if len(errs) > 0 && (opts.StopOnError || authStop) {", "if len(errs) > 0 && opts.StopOnError {", SE, "TestAuthRejectionStopsTheBatch"),
    G("R07-overwritten-block-not-restored", "pkg/syncengine/reconcile.go", "if descBase != nil {", "if false {", SE, "TestFullUpdateOverwriteIsRepairedInTheSamePass"),
    G("R08-readback-skipped", "pkg/syncengine/reconcile.go", "if wrote && len(serrs) == 0 && opts.VerifyReadback {", "if false {", SE, "TestReadbackMismatchIsAnError"),
    G("R09-create-drops-block", "pkg/syncengine/reconcile.go", "if cd.BlockOp == states.BlockAdd {", "if false {", SE, "TestCreateCarriesTheBlockInOneCallAndFieldsAfter"),
    G("R10-no-states-client-check", "pkg/syncengine/reconcile.go", "if scl, ok = cl.(client.StatesClient); !ok {", "if scl, ok = cl.(client.StatesClient); false && !ok {", SE, "TestApplyRefusesAClientWithoutStatesSupport"),
    G("R11-overwrite-base-forgotten", "pkg/syncengine/reconcile.go", "descBase = &base", "_ = base", SE, "TestFullUpdateOverwriteIsRepairedInTheSamePass"),
    G("R12-states-only-without-context", "pkg/syncengine/reconcile.go", "if opts.StatesOnly && opts.States == nil {", "if false {", SE, "TestStatesOnlyRequiresAContext"),
    G("R13-auth-flag-not-reported", "pkg/syncengine/reconcile.go", "res.AuthRejected = true", "_ = authStop", SE, "TestAuthRejectionStopsTheBatch"),
    # ---- pkg/syncengine/states.go
    G("P01-dry-run-creates-fields", "pkg/syncengine/states.go", "if !po.Apply {", "if false {", SE, "TestPrepareStatesCreationGuards"),
    G("P02-creation-uncapped", "pkg/syncengine/states.go", "if len(missing) > limit {", "if false {", SE, "TestPrepareStatesCreationGuards"),
    G("P03-duplicate-guard-removed", "pkg/syncengine/states.go", "if hasName(fresh, m.Key) {", "if false && hasName(fresh, m.Key) {", SE, "TestPrepareStatesCreationGuards"),
    G("P04-unsupported-keeps-trying", "pkg/syncengine/states.go", "rep.CreateUnsupported = true", "rep.CreateUnsupported = true\n\t\t\t\t\t\tcontinue", SE, "TestPrepareStatesCreationGuards"),
    G("P05-creates-without-the-flag", "pkg/syncengine/states.go", "if po.CreateMissing && po.Mode != states.ModeBlock {", "if po.Mode != states.ModeBlock {", SE, "TestPrepareStatesCreationGuards"),
    G("P06-clear-sent-as-set", "pkg/syncengine/states.go", "if ch.Op == states.OpClear {", "if false {", SE, "TestApplyFieldCarrierSetsClearsAndIsIdempotent"),
    G("P07-option-name-sent-not-id", "pkg/syncengine/states.go", "value = id", "value = ch.Want; _ = id", SE, "TestApplyFieldCarrierSetsClearsAndIsIdempotent|TestCreateCarriesTheBlockInOneCallAndFieldsAfter"),
    G("P08-refusals-not-counted", "pkg/syncengine/states.go", "c.Refused++", "", SE, "TestRefusedStatePartsWriteNothing|TestPlainDescriptionSourceIsRefused"),
    G("P09-create-auth-error-ignored", "pkg/syncengine/states.go", "if client.IsAuth(cerr) {", "if false {", SE, "TestPrepareStatesCreationGuards"),
    G("P10-readback-diff-ignored", "pkg/syncengine/states.go", "again.Drift() || again.Refused() {", "false {", SE, "TestReadbackMismatchIsAnError"),
    # ---- pkg/client
    G("K01-markdown-source-not-requested", "pkg/client/states_http.go", 'q.Set("include_markdown_description", "true")', "", CL, "TestListTasksStatesModeQueryParameters"),
    G("K02-last-page-inverted", "pkg/client/states_http.go", "hasMore = !*resp.LastPage", "hasMore = *resp.LastPage", CL, "TestPagingRules"),
    G("K03-empty-page-continues", "pkg/client/states_http.go", "if len(resp.Tasks) == 0 {", "if false {", CL, "TestPagingRules"),
    G("K04-forbidden-not-auth", "pkg/client/states_http.go", "(er.Response.StatusCode == http.StatusUnauthorized || er.Response.StatusCode == http.StatusForbidden)", "(er.Response.StatusCode == http.StatusUnauthorized)", CL, "TestAuthClassificationOnEveryNewCall"),
    G("K05-description-written-as-plain", "pkg/client/states_http.go", 'map[string]interface{}{"markdown_description": markdown}', 'map[string]interface{}{"description": markdown}', CL, "TestSetClearAndDescriptionRequests"),
    G("K06-clear-uses-post", "pkg/client/states_http.go", 'http.MethodDelete, "task/"+url.PathEscape(taskID)+"/field/"', 'http.MethodPost, "task/"+url.PathEscape(taskID)+"/field/"', CL, "TestSetClearAndDescriptionRequests"),
    G("K07-create-404-not-mapped", "pkg/client/states_http.go", "case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:", "case 0:", CL, "TestCreateFieldBodyAndUnsupportedMapping"),
    G("K08-orderindex-not-resolved", "pkg/client/states_http.go", "if o.OrderIndex == int(v) {", "if false {", CL, "TestListTasksStatesModeQueryAndDecoding"),
    G("K09-get-task-without-markdown", "pkg/client/clickup.go", "return c.getTaskStates(ctx, taskID)", "return Task{}, ErrNotImplemented", CL, "TestGetTaskStatesModeAsksForMarkdown"),
    G("K10-list-ignores-states-mode", "pkg/client/clickup.go", "return c.listTasksStates(ctx, listID, page, sinceMS)", "return nil, false, ErrNotImplemented", CL, "TestListTasksStatesMode"),
    G("K11-full-page-rule-lost", "pkg/client/states_http.go", "hasMore := len(resp.Tasks) == clickPageSize", "hasMore := false", CL, "TestPagingRules"),
    G("K12-empty-text-counts-as-set", "pkg/client/states_http.go", 'ok && v != "" {', "ok {", CL, "TestListTasksStatesModeQueryAndDecoding"),
    G("K13-markdown-source-misreported", "pkg/client/clickup.go", 'case t.MarkdownDescription != "":', "case false:", CL, "TestListTasksStatesModeQueryAndDecoding"),
    G("K14-dropdown-by-id-unresolved", "pkg/client/states_http.go", "if o.ID == v {", "if false {", CL, "TestListTasksStatesModeQueryAndDecoding"),
    # ---- pkg/localstore
    G("L01-column-name-unchecked", "pkg/localstore/localstore.go", "columnRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)", "columnRe = regexp.MustCompile(`^.*$`)", LS, "TestLoadItemsExtraRefusesUnsafeColumnNames"),
    G("L02-absent-column-assumed-present", "pkg/localstore/localstore.go", "if cols[c] {", "if true || cols[c] {", LS, "TestLoadItemsExtraReadsPresentColumnsAndAlignsRows|TestLoadItemsExtraAllAbsentStillLoadsBaseColumns"),
    G("L03-extra-values-dropped", "pkg/localstore/localstore.go", "st[c] = v", 'st[c] = ""; _ = v', LS, "TestLoadItemsExtraReadsPresentColumnsAndAlignsRows"),
    # ---- cmd/task_bridge
    G("M01-refusals-exit-zero", "cmd/task_bridge/main.go", "if plan.StateCounts().Refused > 0 {", "if false {", CM, "TestExitForStates"),
    G("M02-refusal-exit-code-collides", "cmd/task_bridge/main.go", "exitStatesRefused = 4", "exitStatesRefused = 1", CM, "TestExitForStates"),
    E("M03-states-only-flag-dropped", "cmd/task_bridge/main.go", "StatesOnly: *statesOnly,", "StatesOnly: false,", ["TestStagedRollout.test_states_only_leaves_status_and_creates_alone"]),
    E("M04-readback-always-off", "cmd/task_bridge/main.go", "VerifyReadback: sc != nil && !*noReadback}", "VerifyReadback: false && !*noReadback}", ["TestFaults.test_ignored_write_is_detected_by_the_readback"]),
    E("M05-dry-run-may-create-fields", "cmd/task_bridge/main.go", "CreateMissing: *createFields, Apply: *apply,", "CreateMissing: *createFields, Apply: true,", ["TestFieldCreation.test_dry_run_lists_would_create_and_calls_nothing"]),
    E("M06-absent-columns-do-not-disable", "cmd/task_bridge/main.go", "spec = nil", "_ = spec", ["TestRefusals.test_missing_columns_disable_states_loudly"]),
    E("M07-strict-flag-ignored", "cmd/task_bridge/main.go", "if *statesStrict || *statesOnly {", "if false && *statesStrict || *statesOnly {", ["TestRefusals.test_missing_columns_disable_states_loudly"]),
    E("M08-apply-auth-exit-lost", "cmd/task_bridge/main.go", "if res.AuthRejected {", "if false {", ["TestFaults.test_401_mid_apply_stops_the_batch"]),
    E("M09-spec-env-ignored", "cmd/task_bridge/main.go", 'os.Getenv("TASK_BRIDGE_STATES_SPEC"), "JSON spec', '"", "JSON spec', ["TestAllBoards.test_three_boards_each_get_only_their_own_states_and_converge"]),
    E("M10-carrier-mode-ignored", "cmd/task_bridge/main.go", "Mode: mode, CreateMissing", "Mode: func() states.CarrierMode { _ = mode; return states.ModeAuto }(), CreateMissing", ["TestFieldCarrier.test_carrier_block_ignores_existing_fields"]),
    E("M11-unknown-state-exit-zero", "cmd/task_bridge/main.go", "return exitForStates(plan)", "return 0", ["TestRefusals.test_unknown_state_is_refused_other_items_still_sync"]),
    # ---- the mock server (test infrastructure must itself be right)
    P("X01-fault-after-ignored", "scripts/mock_clickup_server.py", 'if r["_seen"] <= r["after"]:', "if False:", ["TestFaultInjection.test_after_skips_matching_requests"]),
    P("X02-fault-times-ignored", "scripts/mock_clickup_server.py", 'if r["times"] >= 0 and r["_fired"] >= r["times"]:', "if False:", ["TestFaultInjection.test_times_limits_firing"]),
    P("X03-token-not-checked", "scripts/mock_clickup_server.py", 'auth_ok = self.headers.get("Authorization") == st.token', "auth_ok = True", ["TestMockBasics.test_wrong_token_is_401"]),
    P("X04-tags-honoured-on-update", "scripts/mock_clickup_server.py", "# ClickUp ignores \"tags\" in the update body (verified live 2026-07-27 in the bridge).", "t['tags'] = [x.lower() for x in body.get('tags', t['tags'])]", ["TestMockBasics.test_update_ignores_tags"]),
    P("X05-dropdown-accepts-names", "scripts/mock_clickup_server.py", 'if o["id"] == value:', 'if o["id"] == value or o["name"] == value:', ["TestMockBasics.test_dropdown_set_requires_option_id"]),
    P("X06-log-stores-token", "scripts/mock_clickup_server.py", '"body": body, "auth_ok": auth_ok, "status": None}', '"body": body, "auth_ok": auth_ok, "status": None, "authorization": self.headers.get("Authorization")}', ["TestMockBasics.test_request_log_never_holds_the_token"]),
]


def copy_tree(dst):
    shutil.copytree(ROOT, dst, ignore=shutil.ignore_patterns(".git", "bin", ".gocache", "__pycache__", "*.pyc"))


def sh(cmd, cwd, timeout=600):
    return subprocess.run(cmd, cwd=cwd, env=ENV, capture_output=True, text=True, timeout=timeout)


def run_killer(m, work):
    if m["kind"] == "go":
        r = sh(["go", "test", m["pkg"], "-run", m["run"], "-count=1"], work)
        return r.returncode != 0, r
    if m["kind"] == "e2e":
        r = sh([sys.executable, "scripts/e2e_states.py"] + m["tests"], work)
        return r.returncode != 0, r
    r = sh([sys.executable, "scripts/test_mock_clickup_server.py"] + m["tests"], work)
    return r.returncode != 0, r


def run_mutant(m):
    work = tempfile.mkdtemp(prefix="tb_mut.")
    try:
        shutil.rmtree(work)
        copy_tree(work)
        p = os.path.join(work, m["file"])
        src = open(p, encoding="utf-8").read()
        if src.count(m["old"]) != 1:
            return m["id"], "UNRESOLVED", "anchor found %d times in %s" % (src.count(m["old"]), m["file"])
        open(p, "w", encoding="utf-8").write(src.replace(m["old"], m["new"]))
        if m["kind"] != "py":
            b = sh(["go", "build", "./..."], work)
            if b.returncode != 0:
                return m["id"], "UNRESOLVED", "does not compile: " + b.stderr.strip().splitlines()[-1:][0] if b.stderr.strip() else "does not compile"
        else:
            c = sh([sys.executable, "-m", "py_compile", p], work)
            if c.returncode != 0:
                return m["id"], "UNRESOLVED", "does not compile"
        killed, r = run_killer(m, work)
        if killed:
            return m["id"], "KILLED", ""
        return m["id"], "SURVIVED", ""
    finally:
        shutil.rmtree(work, ignore_errors=True)


def control():
    work = tempfile.mkdtemp(prefix="tb_ctl.")
    try:
        shutil.rmtree(work)
        copy_tree(work)
        r = sh(["go", "test", "./...", "-count=1"], work)
        if r.returncode != 0:
            return False, "go test failed:\n" + r.stdout[-2000:] + r.stderr[-2000:]
        # every named Go killer must match at least one passing test
        seen = {}
        for m in MUTANTS:
            if m["kind"] != "go":
                continue
            key = (m["pkg"], m["run"])
            if key in seen:
                continue
            v = sh(["go", "test", m["pkg"], "-run", m["run"], "-count=1", "-v"], work)
            seen[key] = len(re.findall(r"^--- PASS", v.stdout, re.M))
            if v.returncode != 0 or seen[key] == 0:
                return False, "killer %s %s matches %d passing tests (rc=%d)" % (m["pkg"], m["run"], seen[key], v.returncode)
        e = sh([sys.executable, "scripts/e2e_states.py"], work, timeout=900)
        if e.returncode != 0:
            return False, "E2E control failed:\n" + e.stdout[-3000:] + e.stderr[-3000:]
        mk = sh([sys.executable, "scripts/test_mock_clickup_server.py"], work)
        if mk.returncode != 0:
            return False, "mock self-test control failed:\n" + mk.stdout[-2000:] + mk.stderr[-2000:]
        return True, "go ./... green, %d distinct Go killers match tests, E2E green, mock self-test green" % len(seen)
    finally:
        shutil.rmtree(work, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--jobs", type=int, default=6)
    ap.add_argument("--only", default="")
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--skip-control", action="store_true")
    a = ap.parse_args()
    ms = MUTANTS
    if a.only:
        want = set(a.only.split(","))
        ms = [m for m in MUTANTS if m["id"] in want or m["id"].split("-")[0] in want]
    if a.list:
        for m in ms:
            print(m["id"], m["file"])
        return 0
    ctl_ok = True
    ran_control = not a.skip_control
    if not a.skip_control:
        ctl_ok, msg = control()
        print("CONTROL %s: %s" % ("OK" if ctl_ok else "FAILED", msg), flush=True)
        if not ctl_ok:
            print("MUTATION killed=0 survived=0 unresolved=0 total=%d control=FAILED" % len(ms))
            return 2
    res = []
    with cf.ThreadPoolExecutor(max_workers=a.jobs) as ex:
        for mid, verdict, why in ex.map(run_mutant, ms):
            print("%-10s %s%s" % (verdict, mid, ("  (" + why + ")") if why else ""), flush=True)
            res.append(verdict)
    k, s, u = res.count("KILLED"), res.count("SURVIVED"), res.count("UNRESOLVED")
    print("MUTATION killed=%d survived=%d unresolved=%d total=%d control=%s" % (k, s, u, len(ms), "OK" if ran_control and ctl_ok else "SKIPPED"))
    return 0 if (s == 0 and u == 0) else 1


if __name__ == "__main__":
    sys.exit(main())
