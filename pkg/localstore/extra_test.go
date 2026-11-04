package localstore

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func seedExtra(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not present")
	}
	db := filepath.Join(t.TempDir(), "items.db")
	ddl := `CREATE TABLE items (
	  atm_id TEXT, type TEXT, status TEXT, severity TEXT, title TEXT,
	  description TEXT, current_location TEXT, last_modified TEXT,
	  reproduction_state TEXT, reproduction_env TEXT);
	INSERT INTO items VALUES
	  ('ATM-1','Bug','Queued','','t1','d','Issues','2026-07-01','reproduced-live','bench, "q" ' || char(10) || 'line2'),
	  ('ATM-2','Bug','Queued','','t2','d','Issues','2026-07-01',NULL,NULL),
	  ('ATM-3','Bug','Queued','','t3','d','Fixed','2026-07-01','UNREPRODUCED','');`
	if out, err := exec.Command("sqlite3", db, ddl).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v: %s", err, out)
	}
	return db
}

func TestLoadItemsExtraReadsPresentColumnsAndAlignsRows(t *testing.T) {
	db := seedExtra(t)
	items, absent, err := LoadItemsExtra(context.Background(), db, []string{"reproduction_state", "reproduction_env", "investigation_state"})
	if err != nil {
		t.Fatal(err)
	}
	if len(absent) != 1 || absent[0] != "investigation_state" {
		t.Fatalf("absent = %v", absent)
	}
	by := map[string]map[string]string{}
	for _, it := range items {
		by[it.Key] = it.States
	}
	if by["ATM-1"]["reproduction_state"] != "reproduced-live" || by["ATM-1"]["reproduction_env"] != "bench, \"q\" \nline2" {
		t.Fatalf("ATM-1 = %q", by["ATM-1"])
	}
	if by["ATM-2"]["reproduction_state"] != "" || by["ATM-2"]["reproduction_env"] != "" {
		t.Fatalf("NULL must read as empty text: %q", by["ATM-2"])
	}
	if by["ATM-3"]["reproduction_state"] != "UNREPRODUCED" {
		t.Fatalf("ATM-3 = %q", by["ATM-3"])
	}
	if _, has := by["ATM-1"]["investigation_state"]; has {
		t.Fatal("an absent column must not be invented")
	}
}

func TestLoadItemsExtraNoExtraEqualsLoadItems(t *testing.T) {
	db := seedExtra(t)
	a, err := LoadItems(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	b, absent, err := LoadItemsExtra(context.Background(), db, nil)
	if err != nil || len(absent) != 0 || len(a) != len(b) {
		t.Fatalf("a=%d b=%d absent=%v err=%v", len(a), len(b), absent, err)
	}
	for i := range a {
		if a[i].States != nil || b[i].States != nil {
			t.Fatal("States must stay nil without extra columns")
		}
	}
}

func TestLoadItemsExtraAllAbsentStillLoadsBaseColumns(t *testing.T) {
	db := seedExtra(t)
	items, absent, err := LoadItemsExtra(context.Background(), db, []string{"nope_a", "nope_b"})
	if err != nil || len(items) != 3 || len(absent) != 2 {
		t.Fatalf("items=%d absent=%v err=%v", len(items), absent, err)
	}
}

func TestLoadItemsExtraRefusesUnsafeColumnNames(t *testing.T) {
	db := seedExtra(t)
	for _, bad := range []string{"a; DROP TABLE items", "Upper", "a-b", "", "1x", "a b", "a\"b"} {
		if _, _, err := LoadItemsExtra(context.Background(), db, []string{bad}); err == nil || !strings.Contains(err.Error(), "identifier") {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestLoadItemsExtraNeverWrites(t *testing.T) {
	db := seedExtra(t)
	before, _ := exec.Command("sqlite3", db, "select count(*) from items").Output()
	if _, _, err := LoadItemsExtra(context.Background(), db, []string{"reproduction_state"}); err != nil {
		t.Fatal(err)
	}
	after, _ := exec.Command("sqlite3", db, "select count(*) from items").Output()
	if string(before) != string(after) {
		t.Fatal("row count changed")
	}
}
