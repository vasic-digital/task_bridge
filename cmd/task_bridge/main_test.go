package main

import (
	"testing"

	"github.com/vasic-digital/task_bridge/pkg/mapper"
	"github.com/vasic-digital/task_bridge/pkg/syncengine"
)

// TestExitForApply pins the N2 fix: a gated push with ANY collected per-item
// error MUST yield a non-zero exit code (no silent partial-failure success,
// §11.4.1). A clean push exits 0.
func TestExitForApply(t *testing.T) {
	cases := []struct {
		name string
		res  syncengine.ApplyResult
		want int
	}{
		{"clean push", syncengine.ApplyResult{Created: 5, Updated: 3}, 0},
		{"single error", syncengine.ApplyResult{Errors: []string{"CREATE ATM-1: boom"}}, 1},
		{"partial success WITH errors", syncengine.ApplyResult{Created: 2, Updated: 1, Errors: []string{"a", "b"}}, 1},
		{"zero everything", syncengine.ApplyResult{}, 0},
	}
	for _, c := range cases {
		if got := exitForApply(c.res); got != c.want {
			t.Errorf("%s: exitForApply(%+v) = %d, want %d", c.name, c.res, got, c.want)
		}
	}
}

// TestFilterByPrefix pins the MULTI-BOARD routing filter (operator mandate:
// "attend to MULTIPLE per-context boards, not just the 2 defaults ... don't
// hardcode one"). Without a per-consumer filter, `reconcile --apply` would
// push the WHOLE local items table into EVERY configured board, duplicating
// every item across every board it is not scoped to — a real, high-blast-radius
// side effect on the operator's live ClickUp workspace (§11.4.101). The filter
// stays fully generic (§11.4.28): it matches on the KEY PREFIX already defined
// by pkg/mapper's title-prefix convention (<PREFIX>-<NNN>), never on any
// project-specific literal — the consumer supplies the prefix set (env/flag),
// this engine carries none of its own.
func TestFilterByPrefix(t *testing.T) {
	items := []mapper.LocalItem{
		{Key: "ATM-1"},
		{Key: "ATM-2"},
		{Key: "MVR-1"},
		{Key: "SPK-1"},
		{Key: "atm-3"},        // lowercase key on a row is still an ATM item
		{Key: "ATM-DERIVED-9"}, // not a genuine <PREFIX>-<NNN> key
		{Key: ""},
	}
	cases := []struct {
		name      string
		prefixCSV string
		wantKeys  []string
	}{
		{"empty filter = no filtering (all items pass through)", "",
			[]string{"ATM-1", "ATM-2", "MVR-1", "SPK-1", "atm-3", "ATM-DERIVED-9", ""}},
		{"single prefix", "ATM",
			[]string{"ATM-1", "ATM-2", "atm-3"}},
		{"multiple prefixes, comma-separated", "MVR,SPK",
			[]string{"MVR-1", "SPK-1"}},
		{"whitespace + case tolerated in the prefix list itself", " atm , mvr ",
			[]string{"ATM-1", "ATM-2", "MVR-1", "atm-3"}},
		{"prefix with zero matches yields zero items (never all-items fallback)", "XXX",
			nil},
	}
	for _, c := range cases {
		got := filterByPrefix(items, c.prefixCSV)
		gotKeys := make([]string, 0, len(got))
		for _, it := range got {
			gotKeys = append(gotKeys, it.Key)
		}
		if len(gotKeys) != len(c.wantKeys) {
			t.Errorf("%s: got %d items %v, want %d items %v", c.name, len(gotKeys), gotKeys, len(c.wantKeys), c.wantKeys)
			continue
		}
		for i := range gotKeys {
			if gotKeys[i] != c.wantKeys[i] {
				t.Errorf("%s: item[%d] = %q, want %q (full got=%v want=%v)", c.name, i, gotKeys[i], c.wantKeys[i], gotKeys, c.wantKeys)
			}
		}
	}
}
