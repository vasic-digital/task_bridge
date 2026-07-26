package syncengine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
)

// mockClient is a fake client.Client (fakes are permitted ONLY in unit tests,
// §11.4.27). It records every write attempt so a test can assert exactly which
// remote calls Apply made — and, critically, which it did NOT make (DeleteTask
// must stay empty; INVESTIGATE/UNKEYED/in-sync rows must produce zero calls).
type mockClient struct {
	createErr error // when set, every CreateTask fails with this error
	updateErr error // when set, every UpdateTask fails with this error

	// pages is the FetchAllRemote fixture: pages[i] is the i-th page. hasMore is
	// derived as (i+1 < len(pages)), so an exact-page-multiple board is modelled
	// with a trailing empty page — mirroring the real ClickUp client, whose
	// hasMore signal is "the page came back full".
	pages [][]client.Task

	createReq []client.Task // every CreateTask arg (including failed attempts)
	updateReq []client.Task // every UpdateTask arg (including failed attempts)
	deleteReq []string      // every DeleteTask arg — MUST stay empty (§9/§11.4.122)
	listCalls int
}

func (m *mockClient) ListTasks(_ context.Context, _ string, page int, _ int64) ([]client.Task, bool, error) {
	m.listCalls++
	if page < 0 || page >= len(m.pages) {
		return nil, false, nil
	}
	return m.pages[page], page+1 < len(m.pages), nil
}

func (m *mockClient) GetTask(context.Context, string) (client.Task, error) {
	return client.Task{}, client.ErrNotImplemented
}

func (m *mockClient) CreateTask(_ context.Context, _ string, t client.Task) (client.Task, error) {
	m.createReq = append(m.createReq, t)
	if m.createErr != nil {
		return client.Task{}, m.createErr
	}
	return t, nil
}

func (m *mockClient) UpdateTask(_ context.Context, t client.Task) error {
	m.updateReq = append(m.updateReq, t)
	return m.updateErr
}

func (m *mockClient) SetCustomField(context.Context, string, string, string) error {
	return client.ErrNotImplemented
}

func (m *mockClient) DeleteTask(_ context.Context, taskID string) error {
	m.deleteReq = append(m.deleteReq, taskID)
	return nil
}

// TestApplyNeverDeletesSkipsInSyncPushesDriftAndCreate is the load-bearing Apply
// proof (B1). One realistic plan exercises all five buckets and asserts:
//
//	(a) DeleteTask is NEVER called (never-delete proven at the Apply layer);
//	(b) INVESTIGATE + UNKEYED rows produce ZERO client calls;
//	(c) an in-sync UPDATE row is SKIPPED (no UpdateTask);
//	(d) a drift UPDATE row calls UpdateTask; a CREATE row calls CreateTask;
//	(e) [covered by TestApplyCollectsPerItemErrors] per-item errors are collected.
func TestApplyNeverDeletesSkipsInSyncPushesDriftAndCreate(t *testing.T) {
	const listID = "test-list-123"
	local := []mapper.LocalItem{
		{Key: "ATM-100", Status: "Queued", Title: "already in sync"},      // -> UPDATE in-sync (skip)
		{Key: "ATM-101", Status: "In progress", Title: "drifted"},         // -> UPDATE drift (UpdateTask)
		{Key: "ATM-200", Status: "Queued", Title: "local only -> CREATE"}, // -> CREATE (CreateTask)
	}
	remote := []client.Task{
		{ID: "t1", Name: "[ATM-100] already in sync", Status: "Queued"}, // in-sync
		{ID: "t2", Name: "[ATM-101] drifted", Status: "Queued"},         // drift (want In progress)
		{ID: "t9", Name: "[ATM-999] keyed orphan", Status: "Queued"},    // INVESTIGATE (no call)
		{ID: "tu", Name: "free-form no key", Status: "Queued"},          // UNKEYED (no call)
	}
	plan := PlanReconcile(local, remote)
	// Sanity: the plan must have the buckets the assertions below depend on.
	if c := plan.Counts(); c["UPDATE"] != 2 || c["CREATE"] != 1 || c["INVESTIGATE"] != 1 || c["UNKEYED"] != 1 {
		t.Fatalf("precondition plan.Counts() = %v, want UPDATE=2 CREATE=1 INVESTIGATE=1 UNKEYED=1", plan.Counts())
	}

	mc := &mockClient{}
	res, err := Apply(context.Background(), mc, mapper.New(), listID, plan, local)
	if err != nil {
		t.Fatalf("Apply returned top-level error: %v", err)
	}

	// (a) NEVER delete.
	if len(mc.deleteReq) != 0 {
		t.Fatalf("(a) DeleteTask was called %d time(s) %v — Apply MUST NEVER delete (§9/§11.4.122)", len(mc.deleteReq), mc.deleteReq)
	}
	// (b) INVESTIGATE + UNKEYED produced NO client calls: exactly one CREATE +
	// one UPDATE were attempted, nothing else. (ATM-999 keyed-orphan and the
	// free-form UNKEYED task must not appear anywhere in the write requests.)
	if len(mc.createReq) != 1 || len(mc.updateReq) != 1 {
		t.Fatalf("(b) write attempts = create:%d update:%d, want exactly 1 create + 1 update (INVESTIGATE/UNKEYED must be untouched)", len(mc.createReq), len(mc.updateReq))
	}
	for _, u := range mc.updateReq {
		if u.ID == "t9" || u.ID == "tu" {
			t.Fatalf("(b) UPDATE touched a non-UPDATE task id %q", u.ID)
		}
	}
	// (c) the in-sync row (t1 / ATM-100) was SKIPPED: it must not be updated.
	for _, u := range mc.updateReq {
		if u.ID == "t1" {
			t.Fatalf("(c) in-sync task t1 (ATM-100) was updated — it MUST be skipped")
		}
	}
	// (d) drift row -> UpdateTask on t2; CREATE row -> CreateTask into listID.
	if mc.updateReq[0].ID != "t2" {
		t.Errorf("(d) UpdateTask target = %q, want t2 (the drifted ATM-101)", mc.updateReq[0].ID)
	}
	if got := mc.createReq[0].Name; got != "[ATM-200] local only -> CREATE" {
		t.Errorf("(d) CreateTask name = %q, want the [ATM-200]-prefixed title", got)
	}
	if res.Created != 1 || res.Updated != 1 || len(res.Errors) != 0 {
		t.Fatalf("(d) result = created:%d updated:%d errors:%v, want 1/1/none", res.Created, res.Updated, res.Errors)
	}
}

// TestApplyCollectsPerItemErrors proves (e): per-item CREATE and UPDATE failures
// are COLLECTED into res.Errors (never aborting the batch, never lost) and the
// success counters stay 0. This is the precondition N2 relies on: Apply returns
// a nil top-level error while res.Errors is non-empty, so the CLI is the layer
// that must translate a non-empty res.Errors into a non-zero exit.
func TestApplyCollectsPerItemErrors(t *testing.T) {
	const listID = "test-list-err"
	local := []mapper.LocalItem{
		{Key: "ATM-300", Status: "Queued", Title: "will fail create"}, // CREATE
		{Key: "ATM-301", Status: "In progress", Title: "will fail update"},
	}
	remote := []client.Task{
		{ID: "t301", Name: "[ATM-301] will fail update", Status: "Queued"}, // drift
	}
	plan := PlanReconcile(local, remote)
	if c := plan.Counts(); c["CREATE"] != 1 || c["UPDATE"] != 1 {
		t.Fatalf("precondition plan.Counts() = %v, want CREATE=1 UPDATE=1", plan.Counts())
	}

	mc := &mockClient{
		createErr: errors.New("boom-create"),
		updateErr: errors.New("boom-update"),
	}
	res, err := Apply(context.Background(), mc, mapper.New(), listID, plan, local)
	if err != nil {
		t.Fatalf("Apply returned top-level error %v — per-item errors must be collected, not returned", err)
	}
	if res.Created != 0 || res.Updated != 0 {
		t.Fatalf("counters = created:%d updated:%d, want 0/0 on failure", res.Created, res.Updated)
	}
	if len(res.Errors) != 2 {
		t.Fatalf("res.Errors = %v (len %d), want 2 collected per-item errors", res.Errors, len(res.Errors))
	}
	// Both attempts must have actually been made (proof the errors are real, not
	// short-circuited): one create attempt + one update attempt were issued.
	if len(mc.createReq) != 1 || len(mc.updateReq) != 1 {
		t.Fatalf("attempts = create:%d update:%d, want 1/1", len(mc.createReq), len(mc.updateReq))
	}
	if len(mc.deleteReq) != 0 {
		t.Fatalf("DeleteTask called on the error path — MUST NEVER delete")
	}
}

// TestApplyCreateLocalItemVanished proves the defensive branch where a CREATE
// plan row references a key that is no longer in the local slice handed to Apply
// (a race between plan-build and apply): the row is reported as an error, never
// silently pushed with stale/empty data.
func TestApplyCreateLocalItemVanished(t *testing.T) {
	plan := Plan{Create: []PlanEntry{{Kind: ActionCreate, Key: "ATM-777"}}}
	mc := &mockClient{}
	res, err := Apply(context.Background(), mc, mapper.New(), "list", plan, nil /* local is empty */)
	if err != nil {
		t.Fatalf("Apply top-level error: %v", err)
	}
	if len(mc.createReq) != 0 {
		t.Fatalf("CreateTask was called for a vanished item — must be skipped")
	}
	if res.Created != 0 || len(res.Errors) != 1 {
		t.Fatalf("result = created:%d errors:%v, want 0 created + 1 collected error", res.Created, res.Errors)
	}
}

// TestFetchAllRemotePagination proves FetchAllRemote pages through the entire
// list and DROPS NOTHING (N1): a 250-task/3-page board and a 200-task exact-
// page-multiple board both return every task exactly once, and the defensive
// page cap aborts a never-terminating API rather than spinning forever.
func TestFetchAllRemotePagination(t *testing.T) {
	makePage := func(start, n int) []client.Task {
		p := make([]client.Task, n)
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("t%04d", start+i)
			p[i] = client.Task{ID: id, Name: "[ATM-" + id + "] task"}
		}
		return p
	}

	t.Run("250-tasks-3-pages", func(t *testing.T) {
		// 100 + 100 + 50: the short 3rd page ends pagination.
		mc := &mockClient{pages: [][]client.Task{makePage(0, 100), makePage(100, 100), makePage(200, 50)}}
		all, err := FetchAllRemote(context.Background(), mc, "L")
		if err != nil {
			t.Fatalf("FetchAllRemote error: %v", err)
		}
		assertNoDrops(t, all, 250)
		if mc.listCalls != 3 {
			t.Errorf("listCalls = %d, want 3", mc.listCalls)
		}
	})

	t.Run("200-exact-page-multiple", func(t *testing.T) {
		// 100 + 100 + trailing empty page: an exact multiple needs the empty page
		// to signal "no more" (the real client's full-page hasMore semantics).
		mc := &mockClient{pages: [][]client.Task{makePage(0, 100), makePage(100, 100), {}}}
		all, err := FetchAllRemote(context.Background(), mc, "L")
		if err != nil {
			t.Fatalf("FetchAllRemote error: %v", err)
		}
		assertNoDrops(t, all, 200)
		if mc.listCalls != 3 {
			t.Errorf("listCalls = %d, want 3 (2 full pages + 1 empty terminator)", mc.listCalls)
		}
	})

	t.Run("never-terminating-API-hits-page-cap", func(t *testing.T) {
		// A client that ALWAYS claims hasMore must be aborted by the defensive
		// cap, not spin forever — proven by an error result (never a partial PASS).
		mc := &loopingClient{}
		_, err := FetchAllRemote(context.Background(), mc, "L")
		if err == nil {
			t.Fatalf("FetchAllRemote must return an error when the page cap is exceeded")
		}
	})
}

// assertNoDrops verifies exactly want tasks came back, each ID present exactly
// once (no dropped page, no duplicate).
func assertNoDrops(t *testing.T, all []client.Task, want int) {
	t.Helper()
	if len(all) != want {
		t.Fatalf("got %d tasks, want %d", len(all), want)
	}
	seen := make(map[string]int, len(all))
	for _, task := range all {
		seen[task.ID]++
	}
	if len(seen) != want {
		t.Fatalf("got %d unique ids, want %d (a page was dropped or duplicated)", len(seen), want)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("task id %q appeared %d times, want exactly 1", id, n)
		}
	}
}

// loopingClient always reports hasMore=true, forcing FetchAllRemote to hit its
// defensive page cap. Every other method is unused here.
type loopingClient struct{}

func (loopingClient) ListTasks(context.Context, string, int, int64) ([]client.Task, bool, error) {
	return []client.Task{{ID: "x"}}, true, nil
}
func (loopingClient) GetTask(context.Context, string) (client.Task, error) {
	return client.Task{}, client.ErrNotImplemented
}
func (loopingClient) CreateTask(context.Context, string, client.Task) (client.Task, error) {
	return client.Task{}, client.ErrNotImplemented
}
func (loopingClient) UpdateTask(context.Context, client.Task) error { return client.ErrNotImplemented }
func (loopingClient) SetCustomField(context.Context, string, string, string) error {
	return client.ErrNotImplemented
}
func (loopingClient) DeleteTask(context.Context, string) error { return client.ErrNotImplemented }
