package syncengine

import (
	"reflect"
	"testing"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
)

// TestPlanReconcileBuckets is the load-bearing bucketing proof: it reproduces
// the five real-board categories in miniature and asserts each row lands in the
// correct bucket, that DELETE is always empty, and that ClickUp-only tasks are
// NEVER deleted (§9/§11.4.122).
func TestPlanReconcileBuckets(t *testing.T) {
	local := []mapper.LocalItem{
		{Key: "ATM-100", Status: "Queued", Title: "matched in-sync"},
		{Key: "ATM-101", Status: "In progress", Title: "matched drifted"},
		{Key: "ATM-200", Status: "Queued", Title: "local only -> CREATE"},
		{Key: "SPK-010", Status: "Fixed (→ Fixed.md)", Title: "local only spk -> CREATE"},
	}
	remote := []client.Task{
		{ID: "t1", Name: "[ATM-100] matched in-sync", Status: "Queued"}, // -> UPDATE (in-sync)
		{ID: "t2", Name: "[ATM-101] matched drifted", Status: "Queued"}, // -> UPDATE (drift: want "In progress")
		{ID: "t3", Name: "[ATM-999] keyed orphan", Status: "Queued"},    // -> INVESTIGATE
		{ID: "t4", Name: "[ATM-DERIVED-042] derived", Status: "Queued"}, // -> UNKEYED
		{ID: "t5", Name: "free-form no key", Status: "Queued"},          // -> UNKEYED
	}

	plan := PlanReconcile(local, remote)
	got := plan.Counts()
	want := map[string]int{"UPDATE": 2, "CREATE": 2, "INVESTIGATE": 1, "DELETE": 0, "UNKEYED": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}

	// INVESTIGATE must be the keyed orphan, never deleted.
	if len(plan.Investigate) != 1 || plan.Investigate[0].Key != "ATM-999" {
		t.Fatalf("INVESTIGATE = %+v, want single ATM-999", plan.Investigate)
	}
	if len(plan.Delete) != 0 {
		t.Fatalf("DELETE must always be empty in DIFF mode, got %+v", plan.Delete)
	}

	// CREATE must be the two local-only items (sorted).
	if len(plan.Create) != 2 || plan.Create[0].Key != "ATM-200" || plan.Create[1].Key != "SPK-010" {
		t.Fatalf("CREATE = %+v, want [ATM-200 SPK-010]", plan.Create)
	}

	// UPDATE drift detection: ATM-100 in-sync, ATM-101 drifted.
	byKey := map[string]PlanEntry{}
	for _, e := range plan.Update {
		byKey[e.Key] = e
	}
	if byKey["ATM-100"].Detail != "status in-sync" {
		t.Errorf("ATM-100 detail = %q, want in-sync", byKey["ATM-100"].Detail)
	}
	if byKey["ATM-101"].Detail == "status in-sync" {
		t.Errorf("ATM-101 must be flagged status-drift, got %q", byKey["ATM-101"].Detail)
	}
}

// TestPlanReconcileDeterministic proves N runs yield byte-identical plans
// (§11.4.50) — the precondition for a trustworthy captured diff artifact.
func TestPlanReconcileDeterministic(t *testing.T) {
	local := []mapper.LocalItem{
		{Key: "ATM-003", Status: "Queued"}, {Key: "ATM-001", Status: "Queued"},
		{Key: "ATM-002", Status: "Queued"}, {Key: "ATM-900", Status: "Queued"},
	}
	remote := []client.Task{
		{ID: "b", Name: "[ATM-002] two", Status: "Queued"},
		{ID: "a", Name: "[ATM-001] one", Status: "Queued"},
		{ID: "z", Name: "[ATM-777] orphan", Status: "Queued"},
		{ID: "y", Name: "[ATM-DERIVED-9] d", Status: "Queued"},
	}
	first := PlanReconcile(local, remote)
	for i := 0; i < 5; i++ {
		if got := PlanReconcile(local, remote); !reflect.DeepEqual(got, first) {
			t.Fatalf("iter %d diverged from first plan", i)
		}
	}
	// Sorted order sanity: CREATE keys ascending.
	if len(first.Create) != 2 || first.Create[0].Key != "ATM-003" || first.Create[1].Key != "ATM-900" {
		t.Fatalf("CREATE sort = %+v, want [ATM-003 ATM-900]", first.Create)
	}
}

// TestIndexLocalDedupNewestWins pins the same-key-in-both-trackers rule: the
// most-recently-modified row wins, no silent drop (§11.4.93 composite identity).
func TestIndexLocalDedupNewestWins(t *testing.T) {
	local := []mapper.LocalItem{
		{Key: "ATM-050", Status: "Queued", Location: "Issues", LastModified: "2026-01-01 00:00:00"},
		{Key: "ATM-050", Status: "Fixed (→ Fixed.md)", Location: "Fixed", LastModified: "2026-07-01 00:00:00"},
	}
	m := indexLocal(local)
	if len(m) != 1 || m["ATM-050"].Status != "Fixed (→ Fixed.md)" {
		t.Fatalf("dedup: got %+v, want newest (Fixed) wins", m["ATM-050"])
	}
}

// TestIndexLocalDedupDeterministicTieBreak pins the N5 fix: when two rows for
// the same key share an EXACT last_modified, the winner is chosen by a
// deterministic secondary key (itemTieKey), so the result is IDENTICAL no matter
// what order the rows arrive in (SQL row order is not guaranteed stable across
// queries — §11.4.50). Without the tie-break the winner is whichever row the
// slice happened to present first, and the two orderings below diverge.
func TestIndexLocalDedupDeterministicTieBreak(t *testing.T) {
	// Same key, SAME timestamp; differ only in the fields itemTieKey ranks.
	a := mapper.LocalItem{Key: "ATM-060", Status: "Queued", Location: "Issues", Title: "aaa", LastModified: "2026-07-01 00:00:00"}
	b := mapper.LocalItem{Key: "ATM-060", Status: "Fixed (→ Fixed.md)", Location: "Fixed", Title: "zzz", LastModified: "2026-07-01 00:00:00"}

	fwd := indexLocal([]mapper.LocalItem{a, b})
	rev := indexLocal([]mapper.LocalItem{b, a})
	if !reflect.DeepEqual(fwd["ATM-060"], rev["ATM-060"]) {
		t.Fatalf("tie-break is order-dependent: forward=%+v reverse=%+v", fwd["ATM-060"], rev["ATM-060"])
	}
	// The deterministic winner is the lexicographically-greater canonical tuple:
	// "Issues…" > "Fixed…", so row a wins in BOTH orders.
	if got := fwd["ATM-060"]; got.Location != "Issues" || got.Title != "aaa" {
		t.Fatalf("tie-break winner = %+v, want the greater-tuple row a (Location=Issues,Title=aaa)", got)
	}
}
