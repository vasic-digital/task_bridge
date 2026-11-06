package main

import (
	"reflect"
	"testing"

	"github.com/vasic-digital/task_bridge/pkg/mapper"
	"github.com/vasic-digital/task_bridge/pkg/syncengine"
)

func itemsOf(keys ...string) []mapper.LocalItem {
	var out []mapper.LocalItem
	for _, k := range keys {
		out = append(out, mapper.LocalItem{Key: k, Status: "Queued"})
	}
	return out
}

func keysOf(items []mapper.LocalItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

// TestFilterByKeys: only the named keys survive (CONTROL: the others are present in the input), a missing key is reported not dropped,
// a malformed key is an error, duplicates in the list are harmless, and both rows of a key present twice (Issues and Fixed) are kept.
func TestFilterByKeys(t *testing.T) {
	in := append(itemsOf("ATM-1", "ATM-1831", "ATM-1832", "SPK-7"), mapper.LocalItem{Key: "ATM-1831", Status: "Fixed (→ Fixed.md)"})
	out, missing, err := filterByKeys(in, "ATM-1831, ATM-1832,ATM-1831")
	if err != nil || len(missing) != 0 {
		t.Fatalf("err=%v missing=%v", err, missing)
	}
	if got := keysOf(out); !reflect.DeepEqual(got, []string{"ATM-1831", "ATM-1832", "ATM-1831"}) {
		t.Fatalf("kept %v", got)
	}
	_, missing, err = filterByKeys(in, "ATM-1831,ATM-9999")
	if err != nil || !reflect.DeepEqual(missing, []string{"ATM-9999"}) {
		t.Fatalf("missing key must be reported: %v %v", missing, err)
	}
	for _, bad := range []string{"atm-1", "ATM", "ATM-1;DROP", "ATM-1-2", "A-1", ",,", ""} {
		if _, _, err := filterByKeys(in, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestExitForPlan: 4 outranks 6, 6 is a plan with duplicates, 0 otherwise (CONTROL: a plan without duplicates is 0).
func TestExitForPlan(t *testing.T) {
	if rc := exitForPlan(syncengine.Plan{}); rc != 0 {
		t.Fatalf("empty plan rc=%d", rc)
	}
	dup := syncengine.Plan{Duplicate: []syncengine.PlanEntry{{Key: "ATM-1"}, {Key: "ATM-1"}}}
	if rc := exitForPlan(dup); rc != exitDuplicate || exitDuplicate != 6 {
		t.Fatalf("duplicate rc=%d", rc)
	}
}
