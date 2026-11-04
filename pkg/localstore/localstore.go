// Package localstore reads the §11.4.93 workable-items SQLite schema into the
// engine's neutral mapper.LocalItem model.
//
// DECOUPLING (§11.4.28): this targets the CONSTITUTION-UNIVERSAL §11.4.93
// `items` schema (every constitution project shares it) — NOT any one project's
// identifiers. It carries zero project-specific values; the DB path is injected.
//
// It reads via the `sqlite3` CLI (`-json` mode) rather than an in-process
// driver so task_bridge adds ZERO new Go module dependency (its only third-party
// dep stays raksul/go-clickup) and needs no cgo. The read is real (real DB,
// real sqlite3 binary) — an integration read, never a fake (§11.4.27).
package localstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/vasic-digital/task_bridge/pkg/mapper"
)

// itemsQuery selects the neutral subset of the §11.4.93 items table. It reads
// EVERY row (both Issues and Fixed locations) — reconcile spans the whole
// tracker. Column names are the §11.4.93 canonical schema.
const itemsQuery = `SELECT atm_id, type, status,
  COALESCE(severity,'')    AS severity,
  title,
  COALESCE(description,'') AS description,
  current_location,
  last_modified
FROM items;`

// row mirrors the -json object shape for one items row.
type row struct {
	ATMID        string `json:"atm_id"`
	Type         string `json:"type"`
	Status       string `json:"status"`
	Severity     string `json:"severity"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Location     string `json:"current_location"`
	LastModified string `json:"last_modified"`
}

// LoadItems runs the query against the injected DB path via `sqlite3 -json` and
// returns the items as neutral LocalItems. Errors are explicit (missing sqlite3,
// missing DB, malformed JSON) — never a silent empty result (§11.4.6).
func LoadItems(ctx context.Context, dbPath string) ([]mapper.LocalItem, error) {
	items, _, err := LoadItemsExtra(ctx, dbPath, nil)
	return items, err
}

// columnRe is the only shape of extra column name interpolated into SQL.
var columnRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// LoadItemsExtra is LoadItems plus consumer-defined extra TEXT columns of the
// items table (the state fields). Each extra column that exists is read as text
// (NULL becomes ""); each that does not exist is returned in absent and is NOT
// invented. The caller decides what an absent column means (the engine disables
// the feature loudly instead of treating the values as UNSET).
func LoadItemsExtra(ctx context.Context, dbPath string, extra []string) (items []mapper.LocalItem, absent []string, err error) {
	if dbPath == "" {
		return nil, nil, fmt.Errorf("localstore: empty DB path")
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil, fmt.Errorf("localstore: sqlite3 CLI not found in PATH: %w", err)
	}
	for _, c := range extra {
		if !columnRe.MatchString(c) {
			return nil, nil, fmt.Errorf("localstore: extra column %q is not a plain lower-case identifier", c)
		}
	}
	query := itemsQuery
	var present []string
	if len(extra) > 0 {
		cols, err := tableColumns(ctx, dbPath)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range extra {
			if cols[c] {
				present = append(present, c)
			} else {
				absent = append(absent, c)
			}
		}
		if len(present) > 0 {
			var sel []string
			for _, c := range present {
				sel = append(sel, fmt.Sprintf("COALESCE(CAST(%s AS TEXT),'') AS %s", c, c))
			}
			query = strings.Replace(itemsQuery, "  last_modified\nFROM items;",
				"  last_modified,\n  "+strings.Join(sel, ",\n  ")+"\nFROM items;", 1)
			if query == itemsQuery {
				return nil, nil, fmt.Errorf("localstore: internal: items query anchor not found")
			}
		}
	}
	// -readonly: never mutate the SSoT; -json: structured, delimiter-safe output.
	cmd := exec.CommandContext(ctx, "sqlite3", "-readonly", "-json", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, nil, fmt.Errorf("localstore: sqlite3 failed: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, nil, fmt.Errorf("localstore: sqlite3 failed: %w", err)
	}
	items, err = parseRows(out)
	if err != nil {
		return nil, nil, err
	}
	if len(present) > 0 {
		if err := attachExtra(out, present, items); err != nil {
			return nil, nil, err
		}
	}
	return items, absent, nil
}

// tableColumns returns the set of column names of the items table.
func tableColumns(ctx context.Context, dbPath string) (map[string]bool, error) {
	cmd := exec.CommandContext(ctx, "sqlite3", "-readonly", "-json", dbPath, "PRAGMA table_info(items);")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("localstore: table_info failed: %w", err)
	}
	var cols []struct {
		Name string `json:"name"`
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return nil, fmt.Errorf("localstore: table_info returned nothing (no items table?)")
	}
	if err := json.Unmarshal([]byte(t), &cols); err != nil {
		return nil, fmt.Errorf("localstore: parse table_info: %w", err)
	}
	m := make(map[string]bool, len(cols))
	for _, c := range cols {
		m[c.Name] = true
	}
	return m, nil
}

// attachExtra copies the extra columns of each -json row onto the item parsed
// from the same row (parseRows preserves row order).
func attachExtra(out []byte, cols []string, items []mapper.LocalItem) error {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return fmt.Errorf("localstore: parse sqlite3 -json (extra): %w", err)
	}
	if len(rows) != len(items) {
		return fmt.Errorf("localstore: internal: %d rows vs %d items", len(rows), len(items))
	}
	for i, r := range rows {
		st := make(map[string]string, len(cols))
		for _, c := range cols {
			v, _ := r[c].(string)
			st[c] = v
		}
		items[i].States = st
	}
	return nil
}

// parseRows decodes the `sqlite3 -json` array. sqlite3 emits empty output (not
// `[]`) for a zero-row result, which is treated as an empty slice.
func parseRows(out []byte) ([]mapper.LocalItem, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return []mapper.LocalItem{}, nil
	}
	var rows []row
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, fmt.Errorf("localstore: parse sqlite3 -json: %w", err)
	}
	items := make([]mapper.LocalItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, mapper.LocalItem{
			Key:          r.ATMID,
			Title:        r.Title,
			Description:  r.Description,
			Type:         r.Type,
			Status:       r.Status,
			Severity:     r.Severity,
			Location:     r.Location,
			LastModified: r.LastModified,
		})
	}
	return items, nil
}
