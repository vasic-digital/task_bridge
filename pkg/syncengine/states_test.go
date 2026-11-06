package syncengine

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
	"github.com/vasic-digital/task_bridge/pkg/states"
)

func blockCtx(t testing.TB) *StatesContext {
	spec := loadSpec(t)
	p, err := spec.ResolveCarriers(nil, states.ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	return &StatesContext{Spec: spec, Plan: p}
}

func fieldCtx(t testing.TB) (*StatesContext, []states.FieldDef) {
	spec := loadSpec(t)
	defs := allFieldDefs(spec)
	p, err := spec.ResolveCarriers(defs, states.ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	return &StatesContext{Spec: spec, Plan: p}, defs
}

func item(key, status string, st map[string]string) mapper.LocalItem {
	return mapper.LocalItem{Key: key, Type: "Bug", Status: status, Title: "title " + key, Description: "desc " + key, States: st}
}

func TestPlanWithoutStatesIsUnchanged(t *testing.T) {
	local := []mapper.LocalItem{item("ATM-1", "Queued", map[string]string{"reproduction_state": "reproduced-live"})}
	m := newMem(nil)
	seedInSync(m, local[0])
	remote, _ := FetchAllRemote(context.Background(), m, "L")
	p := PlanReconcile(local, remote)
	if !p.Update[0].InSync || p.Update[0].StateDiff != nil || p.Update[0].StateDrift {
		t.Fatalf("a plan without a states context must not look at states: %+v", p.Update[0])
	}
}

func TestPlanStatesDriftBreaksInSync(t *testing.T) {
	sc := blockCtx(t)
	li := item("ATM-1", "Queued", map[string]string{"reproduction_state": "reproduced-live"})
	m := newMem(nil)
	seedInSync(m, li)
	remote, _ := FetchAllRemote(context.Background(), m, "L")
	p := PlanReconcileWith([]mapper.LocalItem{li}, remote, PlanOptions{States: sc})
	e := p.Update[0]
	if e.InSync || !e.StateDrift || e.StateDiff.BlockOp != states.BlockAdd {
		t.Fatalf("entry = %+v", e)
	}
	if !strings.Contains(e.Detail, "state-drift") {
		t.Fatalf("detail = %q", e.Detail)
	}
	if c := p.StateCounts(); c.Drift != 1 || c.BlockOps != 1 || c.Refused != 0 {
		t.Fatalf("counts = %+v", c)
	}
	// All-UNSET item: still in sync.
	li2 := item("ATM-2", "Queued", nil)
	seedInSync(m, li2)
	remote, _ = FetchAllRemote(context.Background(), m, "L")
	p = PlanReconcileWith([]mapper.LocalItem{li2}, remote, PlanOptions{States: sc})
	if !p.Update[0].InSync {
		t.Fatalf("all-UNSET item with no block must be in sync: %+v", p.Update[0])
	}
}

func TestApplyBlockThenReplanIsEmptyAndWritesNothing(t *testing.T) {
	sc := blockCtx(t)
	local := []mapper.LocalItem{
		item("ATM-1", "Queued", map[string]string{"reproduction_state": "reproduced-live", "reproduction_env": "bench"}),
		item("ATM-2", "In progress", nil),
	}
	m := newMem(nil)
	for _, li := range local {
		seedInSync(m, li)
	}
	_, res := planAndApply(t, m, local, sc, ApplyOptions{VerifyReadback: true})
	if len(res.Errors) != 0 || res.Updated != 1 {
		t.Fatalf("res = %+v", res)
	}
	d := m.tasks["r-ATM-1"].desc
	if !strings.HasPrefix(d, "desc ATM-1\n\n[TB-STATES:BEGIN v1]") || !strings.Contains(d, "reproduction_state: reproduced-live") {
		t.Fatalf("desc = %q", d)
	}
	if m.tasks["r-ATM-2"].desc != "desc ATM-2" {
		t.Fatalf("task without state was touched: %q", m.tasks["r-ATM-2"].desc)
	}
	i := m.mark()
	plan, res2 := planAndApply(t, m, local, sc, ApplyOptions{VerifyReadback: true})
	if plan.StateCounts().Drift != 0 || res2.Updated != 0 || len(m.writesSince(i)) != 0 {
		t.Fatalf("second pass wrote: %v counts=%+v", m.writesSince(i), plan.StateCounts())
	}
	for _, c := range m.calls {
		if strings.HasPrefix(c, "DeleteTask") {
			t.Fatalf("a task was deleted: %s", c)
		}
	}
}

func TestApplyFieldCarrierSetsClearsAndIsIdempotent(t *testing.T) {
	sc, defs := fieldCtx(t)
	li := item("ATM-1", "Queued", map[string]string{"reproduction_state": "reproduced-live", "investigation_evidence": "ev"})
	m := newMem(defs)
	seedInSync(m, li)
	_, res := planAndApply(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{VerifyReadback: true})
	if len(res.Errors) != 0 {
		t.Fatalf("errors %v", res.Errors)
	}
	if m.tasks["r-ATM-1"].values["cf-reproduction_state"] != "reproduced-live" || m.tasks["r-ATM-1"].values["cf-investigation_evidence"] != "ev" {
		t.Fatalf("values = %v", m.tasks["r-ATM-1"].values)
	}
	if m.tasks["r-ATM-1"].desc != "desc ATM-1" {
		t.Fatalf("description changed under the FIELD carrier: %q", m.tasks["r-ATM-1"].desc)
	}
	i := m.mark()
	li.States = map[string]string{"reproduction_state": "UNSET"}
	_, res = planAndApply(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{VerifyReadback: true})
	ws := m.writesSince(i)
	if len(ws) != 2 || !strings.HasPrefix(ws[0], "ClearFieldValue") || !strings.HasPrefix(ws[1], "ClearFieldValue") {
		t.Fatalf("writes = %v, want two clears", ws)
	}
	i = m.mark()
	planAndApply(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{VerifyReadback: true})
	if len(m.writesSince(i)) != 0 {
		t.Fatalf("not idempotent: %v", m.writesSince(i))
	}
}

func TestFullUpdateOverwriteIsRepairedInTheSamePass(t *testing.T) {
	sc := blockCtx(t)
	li := item("ATM-1", "Queued", map[string]string{"reproduction_state": "reproduced-live"})
	m := newMem(nil)
	tk := seedInSync(m, li)
	// Seed the in-sync block, then drift the column so the full update (which
	// overwrites the description with the local one) runs.
	tk.desc = states.Compose("desc ATM-1", sc.Spec.RenderBlock(sc.Plan.Keys(states.CarrierBlock), sc.Spec.Normalize(li.States).Values, false))
	tk.status = "complete"
	_, res := planAndApply(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{VerifyReadback: true})
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if tk.status != "to do" {
		t.Fatalf("status = %q", tk.status)
	}
	if !strings.Contains(tk.desc, "reproduction_state: reproduced-live") || !strings.HasPrefix(tk.desc, "desc ATM-1") {
		t.Fatalf("block lost after the full update: %q", tk.desc)
	}
	i := m.mark()
	planAndApply(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{VerifyReadback: true})
	if len(m.writesSince(i)) != 0 {
		t.Fatalf("not idempotent: %v", m.writesSince(i))
	}
}

func TestStatesOnlyTouchesNothingElse(t *testing.T) {
	sc := blockCtx(t)
	drifted := item("ATM-1", "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
	newItem := item("ATM-9", "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
	m := newMem(nil)
	tk := seedInSync(m, drifted)
	tk.status = "complete"
	tk.name = "[ATM-1] stale title"
	tk.tags = []string{"bug"} // the exact-status label is missing too: StatesOnly must not repair it
	_, res := planAndApply(t, m, []mapper.LocalItem{drifted, newItem}, sc, ApplyOptions{StatesOnly: true})
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	if countPrefix(m.calls, "AddTag") != 0 || countPrefix(m.calls, "RemoveTag") != 0 {
		t.Fatalf("StatesOnly touched labels: %v", m.calls)
	}
	for _, c := range m.writeCalls() {
		if !strings.HasPrefix(c, "SetDescription") {
			t.Fatalf("StatesOnly made a non-state call: %s", c)
		}
	}
	if tk.status != "complete" || tk.name != "[ATM-1] stale title" {
		t.Fatalf("status/title touched: %q %q", tk.status, tk.name)
	}
	if countPrefix(m.calls, "CreateTask") != 0 {
		t.Fatal("StatesOnly created a task")
	}
}

func TestStatesOnlyRequiresAContext(t *testing.T) {
	_, err := ApplyWith(context.Background(), newMem(nil), mapper.New(), "L", Plan{}, nil, ApplyOptions{StatesOnly: true})
	if err == nil {
		t.Fatal("StatesOnly without a states context must be refused")
	}
}

func TestApplyRefusesAClientWithoutStatesSupport(t *testing.T) {
	_, err := ApplyWith(context.Background(), &mockClient{}, mapper.New(), "L", Plan{}, nil, ApplyOptions{States: blockCtx(t)})
	if err == nil || !strings.Contains(err.Error(), "StatesClient") {
		t.Fatalf("err = %v", err)
	}
}

func TestRefusedStatePartsWriteNothing(t *testing.T) {
	sc := blockCtx(t)
	bad := item("ATM-1", "Queued", map[string]string{"reproduction_state": "reproduced"})
	malformed := item("ATM-2", "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
	plain := item("ATM-3", "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
	m := newMem(nil)
	seedInSync(m, bad)
	seedInSync(m, malformed).desc = "x\n[TB-STATES:BEGIN v1]\nreproduction_state: UNREPRODUCED\nsurvivor"
	seedInSync(m, plain)
	plan, _ := planAndApply(t, m, []mapper.LocalItem{bad, malformed, plain}, sc, ApplyOptions{})
	c := plan.StateCounts()
	if c.Refused != 2 || c.Invalid != 1 || c.Malformed != 1 {
		t.Fatalf("counts = %+v", c)
	}
	if m.tasks["r-ATM-2"].desc != "x\n[TB-STATES:BEGIN v1]\nreproduction_state: UNREPRODUCED\nsurvivor" {
		t.Fatalf("malformed description touched: %q", m.tasks["r-ATM-2"].desc)
	}
	if m.tasks["r-ATM-1"].desc != "desc ATM-1" {
		t.Fatalf("invalid item touched")
	}
	// ATM-3 is valid and was written.
	if !strings.Contains(m.tasks["r-ATM-3"].desc, "UNREPRODUCED") {
		t.Fatalf("valid item not written: %q", m.tasks["r-ATM-3"].desc)
	}
}

func TestPlainDescriptionSourceIsRefused(t *testing.T) {
	sc := blockCtx(t)
	li := item("ATM-1", "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
	m := newMem(nil)
	seedInSync(m, li)
	m.plainOnly = true
	plan, _ := planAndApply(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{})
	if plan.StateCounts().NoSource != 1 || countPrefix(m.calls, "SetDescription") != 0 {
		t.Fatalf("counts=%+v calls=%v", plan.StateCounts(), m.calls)
	}
}

func TestAuthRejectionStopsTheBatch(t *testing.T) {
	sc := blockCtx(t)
	var local []mapper.LocalItem
	m := newMem(nil)
	for _, k := range []string{"ATM-1", "ATM-2", "ATM-3", "ATM-4"} {
		li := item(k, "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
		local = append(local, li)
		seedInSync(m, li)
	}
	m.failCall["SetDescription"] = client.ErrUnauthorized
	_, res := planAndApply(t, m, local, sc, ApplyOptions{})
	if !res.AuthRejected || !res.Stopped || countPrefix(m.calls, "SetDescription") != 1 {
		t.Fatalf("res=%+v calls=%v", res, m.calls)
	}
	if res.Remaining != 3 {
		t.Fatalf("remaining = %d, want 3", res.Remaining)
	}
}

func TestOtherErrorsDoNotStopTheBatchWithoutStopOnError(t *testing.T) {
	sc := blockCtx(t)
	var local []mapper.LocalItem
	m := newMem(nil)
	for _, k := range []string{"ATM-1", "ATM-2"} {
		li := item(k, "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
		local = append(local, li)
		seedInSync(m, li)
	}
	m.failCall["SetDescription r-ATM-1"] = errors.New("HTTP 500")
	_, res := planAndApply(t, m, local, sc, ApplyOptions{})
	if len(res.Errors) != 1 || res.AuthRejected || !strings.Contains(m.tasks["r-ATM-2"].desc, "UNREPRODUCED") {
		t.Fatalf("res=%+v", res)
	}
}

func TestReadbackMismatchIsAnError(t *testing.T) {
	sc := blockCtx(t)
	li := item("ATM-1", "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
	m := newMem(nil)
	seedInSync(m, li)
	m.ignoreDescWrites = true
	_, res := planAndApply(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{VerifyReadback: true})
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "READBACK_MISMATCH") {
		t.Fatalf("errors = %v", res.Errors)
	}
	m2 := newMem(nil)
	seedInSync(m2, li)
	m2.ignoreDescWrites = true
	_, res = planAndApply(t, m2, []mapper.LocalItem{li}, sc, ApplyOptions{VerifyReadback: false})
	if len(res.Errors) != 0 {
		t.Fatalf("without read-back the ignored write is not noticed: %v", res.Errors)
	}
}

func TestCreateCarriesTheBlockInOneCallAndFieldsAfter(t *testing.T) {
	sc := blockCtx(t)
	li := item("ATM-7", "Queued", map[string]string{"reproduction_state": "reproduced-live"})
	m := newMem(nil)
	_, res := planAndApply(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{VerifyReadback: true})
	if res.Created != 1 || len(res.Errors) != 0 {
		t.Fatalf("res=%+v", res)
	}
	if countPrefix(m.calls, "SetDescription") != 0 {
		t.Fatalf("block must ride in the create call: %v", m.calls)
	}
	if !strings.Contains(m.tasks["n1"].desc, "reproduction_state: reproduced-live") {
		t.Fatalf("desc = %q", m.tasks["n1"].desc)
	}
	// FIELD carrier: fields are set after the create.
	fsc, defs := fieldCtx(t)
	m2 := newMem(defs)
	_, res = planAndApply(t, m2, []mapper.LocalItem{li}, fsc, ApplyOptions{VerifyReadback: true})
	if res.Created != 1 || len(res.Errors) != 0 || m2.tasks["n1"].values["cf-reproduction_state"] != "reproduced-live" {
		t.Fatalf("res=%+v values=%v", res, m2.tasks["n1"].values)
	}
	if m2.tasks["n1"].desc != "desc ATM-7" {
		t.Fatalf("desc = %q", m2.tasks["n1"].desc)
	}
}

func TestPrepareStatesCreationGuards(t *testing.T) {
	spec := loadSpec(t)
	run := func(m *memClient, po PrepareOptions) (*StatesContext, ProvisionReport, error) {
		return PrepareStates(context.Background(), m, "L", spec, po)
	}
	// dry-run + flag: lists, calls nothing.
	m := newMem(nil)
	_, rep, err := run(m, PrepareOptions{Mode: states.ModeAuto, CreateMissing: true})
	if err != nil || len(rep.WouldCreate) != 8 || countPrefix(m.calls, "CreateField") != 0 {
		t.Fatalf("dry-run: rep=%+v err=%v calls=%v", rep, err, m.calls)
	}
	// apply without the flag: no creation.
	m = newMem(nil)
	_, rep, _ = run(m, PrepareOptions{Mode: states.ModeAuto, Apply: true})
	if countPrefix(m.calls, "CreateField") != 0 || len(rep.Created) != 0 {
		t.Fatalf("apply without flag created: %v", m.calls)
	}
	// both: creates, capped.
	m = newMem(nil)
	sc, rep, err := run(m, PrepareOptions{Mode: states.ModeAuto, Apply: true, CreateMissing: true, MaxCreates: 3})
	if err != nil || len(rep.Created) != 3 || len(sc.Plan.Keys(states.CarrierField)) != 3 {
		t.Fatalf("capped: rep=%+v err=%v", rep, err)
	}
	// both, uncapped: all eight, then everything is FIELD.
	m = newMem(nil)
	sc, rep, _ = run(m, PrepareOptions{Mode: states.ModeAuto, Apply: true, CreateMissing: true})
	if len(rep.Created) != 8 || len(sc.Plan.Keys(states.CarrierBlock)) != 0 || len(rep.ToProvision) != 0 {
		t.Fatalf("all: rep=%+v", rep)
	}
	// unsupported: stops after the first attempt, falls back to the block.
	m = newMem(nil)
	m.createUnsupported = true
	sc, rep, err = run(m, PrepareOptions{Mode: states.ModeAuto, Apply: true, CreateMissing: true})
	if err != nil || !rep.CreateUnsupported || countPrefix(m.calls, "CreateField") != 1 || len(sc.Plan.Keys(states.CarrierBlock)) != 8 {
		t.Fatalf("unsupported: rep=%+v calls=%v", rep, m.calls)
	}
	// a field that appears between the listing and the create is never duplicated.
	m = newMem(nil)
	m.relistAddsField = "reproduction_state"
	_, rep, _ = run(m, PrepareOptions{Mode: states.ModeAuto, Apply: true, CreateMissing: true, MaxCreates: 1})
	if countPrefix(m.calls, "CreateField") != 0 {
		t.Fatalf("duplicate create attempted: %v", m.calls)
	}
	// a wrong-type field is not "missing": it is never re-created.
	m = newMem([]states.FieldDef{{ID: "x", Name: "reproduction_state", Type: "short_text"}})
	_, rep, _ = run(m, PrepareOptions{Mode: states.ModeAuto, Apply: true, CreateMissing: true})
	for _, c := range m.calls {
		if strings.HasPrefix(c, "CreateField reproduction_state") {
			t.Fatalf("re-created a mismatched field: %v", m.calls)
		}
	}
	// creation error: reported, other keys still tried, auth error stops.
	m = newMem(nil)
	m.failCall["CreateField reproduction_state"] = errors.New("HTTP 400")
	_, rep, _ = run(m, PrepareOptions{Mode: states.ModeAuto, Apply: true, CreateMissing: true})
	if len(rep.CreateErrors) != 1 || len(rep.Created) != 7 {
		t.Fatalf("rep=%+v", rep)
	}
	m = newMem(nil)
	m.failCall["CreateField"] = client.ErrUnauthorized
	_, rep, _ = run(m, PrepareOptions{Mode: states.ModeAuto, Apply: true, CreateMissing: true})
	if countPrefix(m.calls, "CreateField") != 1 {
		t.Fatalf("auth error did not stop the creations: %v", m.calls)
	}
	// block mode lists nothing; field mode fails closed.
	m = newMem(nil)
	if _, _, err := run(m, PrepareOptions{Mode: states.ModeBlock}); err != nil || countPrefix(m.calls, "ListFields") != 0 {
		t.Fatalf("block mode: %v %v", err, m.calls)
	}
	m = newMem(nil)
	if _, _, err := run(m, PrepareOptions{Mode: states.ModeField}); err == nil {
		t.Fatal("field mode with no fields must fail closed")
	}
	// a failing listing is an error.
	m = newMem(nil)
	m.failCall["ListFields"] = errors.New("boom")
	if _, _, err := run(m, PrepareOptions{Mode: states.ModeAuto}); err == nil {
		t.Fatal("listing failure swallowed")
	}
}

func TestPlanWithStatesIsDeterministicAndGolden(t *testing.T) {
	sc := blockCtx(t)
	local := []mapper.LocalItem{
		item("ATM-3", "Queued", map[string]string{"reproduction_state": "blocked-no-device-with-recorded-blocker"}),
		item("ATM-1", "Queued", map[string]string{"reproduction_state": "reproduced-live", "reproduction_red_cmd": "bash r.sh\nsecond"}),
		item("ATM-2", "In progress", map[string]string{"investigation_state": "bogus"}),
		item("ATM-5", "Queued", map[string]string{"investigation_state": "in-progress"}),
	}
	m := newMem(nil)
	seedInSync(m, local[0])
	seedInSync(m, local[1])
	seedInSync(m, local[2])
	remote, _ := FetchAllRemote(context.Background(), m, "L")
	first, _ := json.MarshalIndent(PlanReconcileWith(local, remote, PlanOptions{States: sc}), "", "  ")
	for i := 0; i < 5; i++ {
		again, _ := json.MarshalIndent(PlanReconcileWith(local, remote, PlanOptions{States: sc}), "", "  ")
		if string(again) != string(first) {
			t.Fatal("plan is not deterministic")
		}
	}
	p := filepath.Join("testdata", "plan_states.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(p, append(first, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (UPDATE_GOLDEN=1 go test)", err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(first)) {
		t.Fatalf("golden differs:\n%s", first)
	}
	if strings.Contains(string(first), "desc ATM") {
		t.Fatal("the remote description leaked into the artifact")
	}
}

func TestStateReportLinesCapAndOrder(t *testing.T) {
	sc := blockCtx(t)
	var local []mapper.LocalItem
	m := newMem(nil)
	for _, k := range []string{"ATM-1", "ATM-2", "ATM-3"} {
		li := item(k, "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
		local = append(local, li)
		seedInSync(m, li)
	}
	remote, _ := FetchAllRemote(context.Background(), m, "L")
	plan := PlanReconcileWith(local, remote, PlanOptions{States: sc})
	lines, omitted := plan.StateReportLines(2)
	if len(lines) != 2 || omitted != 1 || !strings.Contains(lines[0], "ATM-1") {
		t.Fatalf("lines=%v omitted=%d", lines, omitted)
	}
	lines, omitted = plan.StateReportLines(0)
	if len(lines) != 3 || omitted != 0 {
		t.Fatalf("uncapped: %v %d", lines, omitted)
	}
}

// TestPropEngineApplyTwiceIsZeroWrites: random items, random starting remote
// state, both carriers: one pass converges and a second pass makes no write.
func TestPropEngineApplyTwiceIsZeroWrites(t *testing.T) {
	r := rand.New(rand.NewSource(77))
	rsVals := []string{"UNSET", "UNREPRODUCED", "reproduced-live", "reproduced-host-fixture", "not-reproducible-with-evidence", "blocked-no-device-with-recorded-blocker"}
	isVals := []string{"UNSET", "not-started", "in-progress", "root-cause-found-with-evidence", "root-cause-unconfirmed", "closed-no-defect"}
	texts := []string{"", "a", "x y", "line1\nline2", "tab\there", `back\slash`, "[brackets] *star* _under_", "TB-STATES:END", "é漢"}
	for round := 0; round < 60; round++ {
		useFields := round%2 == 0
		var sc *StatesContext
		var defs []states.FieldDef
		if useFields {
			sc, defs = fieldCtx(t)
		} else {
			sc = blockCtx(t)
		}
		m := newMem(defs)
		var local []mapper.LocalItem
		for i := 0; i < 8; i++ {
			st := map[string]string{
				"reproduction_state": rsVals[r.Intn(len(rsVals))], "investigation_state": isVals[r.Intn(len(isVals))],
				"reproduction_evidence": texts[r.Intn(len(texts))], "reproduction_red_cmd": texts[r.Intn(len(texts))],
				"investigation_root_cause": texts[r.Intn(len(texts))],
			}
			li := item("ATM-"+string(rune('1'+i)), []string{"Queued", "In progress"}[r.Intn(2)], st)
			local = append(local, li)
			if r.Intn(4) != 0 {
				tk := seedInSync(m, li)
				if r.Intn(2) == 0 { // stale pre-existing state
					old := item(li.Key, li.Status, map[string]string{"reproduction_state": rsVals[r.Intn(len(rsVals))], "reproduction_evidence": texts[r.Intn(len(texts))]})
					planAndApplyOne(t, m, old, sc)
					_ = tk
				}
			}
		}
		planAndApply(t, m, local, sc, ApplyOptions{VerifyReadback: true})
		i := m.mark()
		plan, res := planAndApply(t, m, local, sc, ApplyOptions{VerifyReadback: true})
		if w := m.writesSince(i); len(w) != 0 || len(res.Errors) != 0 || plan.StateCounts().Drift != 0 {
			t.Fatalf("round %d (fields=%v): second pass wrote %v errors=%v counts=%+v", round, useFields, w, res.Errors, plan.StateCounts())
		}
	}
}

func planAndApplyOne(t *testing.T, m *memClient, li mapper.LocalItem, sc *StatesContext) {
	t.Helper()
	remote, _ := FetchAllRemote(context.Background(), m, "L")
	plan := PlanReconcileWith([]mapper.LocalItem{li}, remote, PlanOptions{States: sc})
	if _, err := ApplyWith(context.Background(), m, mapper.New(), "L", plan, []mapper.LocalItem{li}, ApplyOptions{States: sc}); err != nil {
		t.Fatal(err)
	}
}
