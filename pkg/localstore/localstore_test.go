package localstore

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLoadItemsRealSQLite is an INTEGRATION test (§11.4.27): it builds a real
// SQLite DB with the §11.4.93 items columns via the sqlite3 CLI and reads it
// back through LoadItems — the real read path, no fake.
func TestLoadItemsRealSQLite(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not present — skipping real-DB read test")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "items.db")
	ddl := `CREATE TABLE items (
	  atm_id TEXT, type TEXT, status TEXT, severity TEXT, title TEXT,
	  description TEXT, current_location TEXT, last_modified TEXT);
	INSERT INTO items VALUES
	  ('ATM-013','Bug','Queued','High','a title with, comma and "quote"','desc','Issues','2026-07-01 00:00:00'),
	  ('SPK-042','Feature','Fixed (→ Fixed.md)','','spk title','spk desc','Fixed','2026-07-02 00:00:00');`
	if out, err := exec.Command("sqlite3", db, ddl).CombinedOutput(); err != nil {
		t.Fatalf("seed DB: %v: %s", err, out)
	}

	items, err := LoadItems(context.Background(), db)
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	byKey := map[string]struct{ typ, status, loc string }{}
	for _, it := range items {
		byKey[it.Key] = struct{ typ, status, loc string }{it.Type, it.Status, it.Location}
	}
	if got := byKey["ATM-013"]; got.typ != "Bug" || got.status != "Queued" || got.loc != "Issues" {
		t.Errorf("ATM-013 = %+v, want Bug/Queued/Issues", got)
	}
	if got := byKey["SPK-042"]; got.typ != "Feature" || got.status != "Fixed (→ Fixed.md)" || got.loc != "Fixed" {
		t.Errorf("SPK-042 = %+v, want Feature/Fixed/Fixed", got)
	}
}

func TestLoadItemsMissingPath(t *testing.T) {
	if _, err := LoadItems(context.Background(), ""); err == nil {
		t.Fatal("empty DB path must error")
	}
}

func TestParseRowsEmpty(t *testing.T) {
	items, err := parseRows([]byte("  \n"))
	if err != nil {
		t.Fatalf("empty output should not error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("empty output should yield 0 items, got %d", len(items))
	}
}
