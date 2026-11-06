package syncengine

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
)

// markerRe is a consumer-style description version marker (one capture group).
var markerRe = regexp.MustCompile(`Content-Sha256:\s*([0-9a-f]{64})`)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func descWith(h string) string { return "Source-Issue: x\nContent-Sha256: " + h + "\nbody" }

// TestDuplicateRemoteKeysAreReportedNotWritten: two remote tasks with one item key
// are BOTH listed under Duplicate, neither is an UPDATE, the key is not CREATEd
// again, and Counts() keeps its pinned five-bucket shape. CONTROL: with a single
// remote task the same key is a plain in-sync UPDATE and Duplicate is empty.
func TestDuplicateRemoteKeysAreReportedNotWritten(t *testing.T) {
	local := []mapper.LocalItem{{Key: "ATM-1831", Status: "Queued", Title: "usb hang"}, {Key: "ATM-1832", Status: "Queued", Title: "uapp"}}
	one := []client.Task{
		{ID: "t1", Name: "[ATM-1831] usb hang", Status: "to do", Tags: []string{"status:queued"}},
	}
	pc := PlanReconcile(local, one)
	if len(pc.Duplicate) != 0 || len(pc.Update) != 1 || !pc.Update[0].InSync || len(pc.Create) != 1 || pc.Create[0].Key != "ATM-1832" {
		t.Fatalf("CONTROL plan wrong: update=%+v create=%+v dup=%+v", pc.Update, pc.Create, pc.Duplicate)
	}
	two := append(append([]client.Task{}, one...), client.Task{ID: "t2", Name: "[ATM-1831] usb hang (copy)", Status: "to do"})
	p := PlanReconcile(local, two)
	if len(p.Duplicate) != 2 || p.Duplicate[0].TaskID != "t1" || p.Duplicate[1].TaskID != "t2" {
		t.Fatalf("both tasks of the duplicated key must be listed, got %+v", p.Duplicate)
	}
	if !reflect.DeepEqual(p.DuplicateKeys(), []string{"ATM-1831"}) {
		t.Fatalf("DuplicateKeys = %v", p.DuplicateKeys())
	}
	if len(p.Update) != 0 {
		t.Fatalf("a duplicated key must not be an UPDATE row: %+v", p.Update)
	}
	for _, c := range p.Create {
		if c.Key == "ATM-1831" {
			t.Fatalf("a duplicated key must not be CREATEd a third time: %+v", p.Create)
		}
	}
	if got, want := p.Counts(), map[string]int{"UPDATE": 0, "CREATE": 1, "INVESTIGATE": 0, "DELETE": 0, "UNKEYED": 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Counts() shape changed: %v", got)
	}
	// a duplicated key that NO local item owns stays an INVESTIGATE orphan per task (no new semantics for out-of-scope keys)
	orphans := PlanReconcile(nil, []client.Task{{ID: "a", Name: "[ATM-5] x"}, {ID: "b", Name: "[ATM-5] y"}})
	if len(orphans.Duplicate) != 0 || len(orphans.Investigate) != 2 {
		t.Fatalf("out-of-scope duplicates must stay INVESTIGATE: %+v", orphans)
	}
}

// TestApplyNeverTouchesDuplicates: Apply over a plan with a duplicated key makes zero writes for it, still creates the
// other missing item, and never deletes.
func TestApplyNeverTouchesDuplicates(t *testing.T) {
	local := []mapper.LocalItem{{Key: "ATM-1831", Status: "Queued", Title: "a"}, {Key: "ATM-1832", Status: "Queued", Title: "b"}}
	remote := []client.Task{{ID: "t1", Name: "[ATM-1831] a", Status: "to do"}, {ID: "t2", Name: "[ATM-1831] a", Status: "to do"}}
	plan := PlanReconcile(local, remote)
	m := &mockClient{}
	res, err := ApplyWith(context.Background(), m, mapper.New(), "L", plan, local, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || len(m.createReq) != 1 || !strings.HasPrefix(m.createReq[0].Name, "[ATM-1832]") {
		t.Fatalf("only the missing ATM-1832 may be created: %+v", m.createReq)
	}
	if len(m.updateReq) != 0 || len(m.addTagReq) != 0 || len(m.remTagReq) != 0 || len(m.deleteReq) != 0 {
		t.Fatalf("duplicated key must produce no update/tag/delete call: upd=%v add=%v rem=%v del=%v", m.updateReq, m.addTagReq, m.remTagReq, m.deleteReq)
	}
}

// TestDescMarkerDriftIsDetected pins the marker rule: drift only when a marker is configured AND the local description carries one AND the
// remote value differs (or is absent). A configured marker with an equal value, a local text without marker, and no marker at all are in sync.
func TestDescMarkerDriftIsDetected(t *testing.T) {
	li := mapper.LocalItem{Key: "ATM-1831", Status: "Queued", Title: "t", Description: descWith(hashA)}
	mk := func(desc string) client.Task {
		return client.Task{ID: "t1", Name: "[ATM-1831] t", Status: "to do", Tags: []string{"status:queued"}, Description: desc}
	}
	cases := []struct {
		name   string
		marker *regexp.Regexp
		local  mapper.LocalItem
		remote string
		drift  bool
	}{
		{"equal marker is in sync (CONTROL)", markerRe, li, descWith(hashA), false},
		{"different marker drifts", markerRe, li, descWith(hashB), true},
		{"remote without marker drifts", markerRe, li, "Source-Issue: x\nbody", true},
		{"marker off never drifts", nil, li, descWith(hashB), false},
		{"local without marker never drifts", markerRe, mapper.LocalItem{Key: "ATM-1831", Status: "Queued", Title: "t", Description: "no marker here"}, descWith(hashB), false},
	}
	for _, c := range cases {
		p := PlanReconcileWith([]mapper.LocalItem{c.local}, []client.Task{mk(c.remote)}, PlanOptions{DescMarker: c.marker})
		if len(p.Update) != 1 {
			t.Fatalf("%s: update rows = %d", c.name, len(p.Update))
		}
		e := p.Update[0]
		if e.DescDrift != c.drift || e.InSync == c.drift {
			t.Errorf("%s: DescDrift=%v InSync=%v detail=%q, want drift=%v", c.name, e.DescDrift, e.InSync, e.Detail, c.drift)
		}
		if c.drift && !strings.Contains(e.Detail, "description-drift") {
			t.Errorf("%s: detail %q lacks description-drift", c.name, e.Detail)
		}
	}
}

// TestApplyDescDriftWritesNameAndDescriptionOnly: a description-only drift is repaired by ONE name+description update (no status, no tags,
// no label call), and re-planning against the updated remote is in sync, so the second apply writes nothing (idempotent).
func TestApplyDescDriftWritesNameAndDescriptionOnly(t *testing.T) {
	li := mapper.LocalItem{Key: "ATM-1831", Status: "Queued", Title: "t", Type: "Bug", Description: descWith(hashA)}
	remote := client.Task{ID: "t1", Name: "[ATM-1831] t", Status: "to do", Tags: []string{"status:queued"}, Description: descWith(hashB)}
	plan := PlanReconcileWith([]mapper.LocalItem{li}, []client.Task{remote}, PlanOptions{DescMarker: markerRe})
	m := &mockClient{}
	res, err := ApplyWith(context.Background(), m, mapper.New(), "L", plan, []mapper.LocalItem{li}, ApplyOptions{})
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("apply: %v %v", err, res.Errors)
	}
	if len(m.updateReq) != 1 {
		t.Fatalf("exactly one update expected, got %d: %+v", len(m.updateReq), m.updateReq)
	}
	u := m.updateReq[0]
	if u.ID != "t1" || u.Name != "[ATM-1831] t" || u.Description != li.Description || u.Status != "" || len(u.Tags) != 0 {
		t.Fatalf("the update must carry name+description only: %+v", u)
	}
	if len(m.addTagReq)+len(m.remTagReq)+len(m.createReq)+len(m.deleteReq) != 0 {
		t.Fatalf("no label/create/delete call expected")
	}
	// simulate the remote after the write; the second plan must be in sync and the second apply silent
	remote.Description = u.Description
	plan2 := PlanReconcileWith([]mapper.LocalItem{li}, []client.Task{remote}, PlanOptions{DescMarker: markerRe})
	if len(plan2.Update) != 1 || !plan2.Update[0].InSync {
		t.Fatalf("second plan not in sync: %+v", plan2.Update)
	}
	m2 := &mockClient{}
	if _, err := ApplyWith(context.Background(), m2, mapper.New(), "L", plan2, []mapper.LocalItem{li}, ApplyOptions{}); err != nil || len(m2.updateReq)+len(m2.addTagReq)+len(m2.createReq) != 0 {
		t.Fatalf("second apply must write nothing: %v %+v", err, m2)
	}
}

// planAndApplyMarker is planAndApply with the description marker on.
func planAndApplyMarker(t *testing.T, m *memClient, local []mapper.LocalItem, sc *StatesContext, o ApplyOptions) (Plan, ApplyResult) {
	t.Helper()
	remote, err := FetchAllRemote(context.Background(), m, "L")
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanReconcileWith(local, remote, PlanOptions{States: sc, DescMarker: markerRe})
	o.States = sc
	res, err := ApplyWith(context.Background(), m, mapper.New(), "L", plan, local, o)
	if err != nil {
		t.Fatal(err)
	}
	return plan, res
}

// TestStatesOnlyDoesNotRewriteDescription: the staged roll-out switch stays states-only even when a description marker has drifted: the only
// write is the managed state block, the base description keeps the OLD marker, status and name are untouched.
func TestStatesOnlyDoesNotRewriteDescription(t *testing.T) {
	sc := blockCtx(t)
	li := item("ATM-1", "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
	li.Description = descWith(hashA)
	m := newMem(nil)
	tk := seedInSync(m, li)
	tk.desc = descWith(hashB)
	_, res := planAndApplyMarker(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{StatesOnly: true})
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	if !strings.Contains(tk.desc, hashB) || strings.Contains(tk.desc, hashA) {
		t.Fatalf("states-only rewrote the base description: %q", tk.desc)
	}
	for _, c := range m.writeCalls() {
		if !strings.HasPrefix(c, "SetDescription") {
			t.Fatalf("StatesOnly made a non-state call: %s", c)
		}
	}
}

// TestDescDriftRewriteKeepsTheManagedBlock: a normal apply rewrites the description to the new marker AND re-adds the state block in the
// same pass; the next plan is empty (converges) and the next apply makes no write.
func TestDescDriftRewriteKeepsTheManagedBlock(t *testing.T) {
	sc := blockCtx(t)
	li := item("ATM-1", "Queued", map[string]string{"reproduction_state": "UNREPRODUCED"})
	li.Description = descWith(hashA)
	m := newMem(nil)
	tk := seedInSync(m, li)
	tk.desc = descWith(hashB)
	plan, res := planAndApplyMarker(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{})
	if len(res.Errors) != 0 || !plan.Update[0].DescDrift {
		t.Fatalf("errors=%v plan=%+v", res.Errors, plan.Update)
	}
	if !strings.Contains(tk.desc, hashA) || strings.Contains(tk.desc, hashB) {
		t.Fatalf("description not rewritten to the new marker: %q", tk.desc)
	}
	if !strings.Contains(tk.desc, "reproduction_state: UNREPRODUCED") {
		t.Fatalf("managed state block lost after the description rewrite: %q", tk.desc)
	}
	mark := m.mark()
	plan2, res2 := planAndApplyMarker(t, m, []mapper.LocalItem{li}, sc, ApplyOptions{})
	if len(res2.Errors) != 0 || !plan2.Update[0].InSync || len(m.writesSince(mark)) != 0 {
		t.Fatalf("second pass must be in sync and silent: sync=%v writes=%v err=%v", plan2.Update[0].InSync, m.writesSince(mark), res2.Errors)
	}
}
