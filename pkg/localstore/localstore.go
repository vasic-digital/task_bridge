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
	if dbPath == "" {
		return nil, fmt.Errorf("localstore: empty DB path")
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("localstore: sqlite3 CLI not found in PATH: %w", err)
	}
	// -readonly: never mutate the SSoT; -json: structured, delimiter-safe output.
	cmd := exec.CommandContext(ctx, "sqlite3", "-readonly", "-json", dbPath, itemsQuery)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("localstore: sqlite3 failed: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("localstore: sqlite3 failed: %w", err)
	}
	return parseRows(out)
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
