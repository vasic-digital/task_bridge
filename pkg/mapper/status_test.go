package mapper

import (
	"strings"
	"testing"
)

// TestStatusVocabRoundTrip pins the P2 §3 status map: DB->CU strips the
// §11.4.33 `(→ Fixed.md)` marker; CU->DB re-attaches it; unknown values are
// surfaced, never guessed (§11.4.6).
func TestStatusVocabRoundTrip(t *testing.T) {
	cases := []struct {
		local  string
		remote string
	}{
		{"Queued", "Queued"},
		{"In progress", "In progress"},
		{"Operator-blocked", "Operator-blocked"},
		{"Fixed (→ Fixed.md)", "Fixed"},
		{"Implemented (→ Fixed.md)", "Implemented"},
		{"Completed (→ Fixed.md)", "Completed"},
		{"Obsolete (→ Fixed.md)", "Obsolete"},
	}
	for _, c := range cases {
		r, ok := StatusToRemote(c.local)
		if !ok || r != c.remote {
			t.Errorf("StatusToRemote(%q) = (%q,%v), want (%q,true)", c.local, r, ok, c.remote)
		}
		back, ok := StatusToLocal(c.remote)
		if !ok || back != c.local {
			t.Errorf("StatusToLocal(%q) = (%q,%v), want (%q,true)", c.remote, back, ok, c.local)
		}
	}
}

func TestStatusUnknownIsSurfaced(t *testing.T) {
	if _, ok := StatusToRemote("Nonsense"); ok {
		t.Error("StatusToRemote must reject an out-of-vocab local status")
	}
	if _, ok := StatusToLocal("Backlog"); ok {
		t.Error("StatusToLocal must reject an out-of-vocab remote status")
	}
}

// TestRemoteStatusMatchesLocal underpins the UPDATE status-drift detection:
// case-insensitive equality via the grouped COLUMN, and no false-match on
// unmapped. Post-grouping (2026-07-27) the match is against the ClickUp column,
// so local "Fixed (→ Fixed.md)" is in-sync when the remote column is "complete".
func TestRemoteStatusMatchesLocal(t *testing.T) {
	if !RemoteStatusMatchesLocal("Complete", "Fixed (→ Fixed.md)") {
		t.Error("remote column 'Complete' should match local 'Fixed (→ Fixed.md)' (grouped, case-insensitive)")
	}
	if RemoteStatusMatchesLocal("in progress", "Queued") {
		t.Error("drifted statuses must NOT match (Queued groups to 'to do', not 'in progress')")
	}
	if RemoteStatusMatchesLocal("Anything", "Nonsense-local") {
		t.Error("an unmapped local status can never claim a match")
	}
}

// TestStatusColumnGrouping pins the operator 2026-07-27 grouping map: our ~11
// lifecycle statuses group one-or-more into the 4 EXISTING board columns
// {to do, in progress, obsolete, complete}. An out-of-vocab status is surfaced
// (never guessed, §11.4.6).
func TestStatusColumnGrouping(t *testing.T) {
	cases := map[string]string{
		"Queued":                   "to do",
		"In progress":              "in progress",
		"Ready for testing":        "in progress",
		"In testing":               "in progress",
		"Reopened":                 "in progress",
		"Operator-blocked":         "in progress",
		"Fixed (→ Fixed.md)":       "complete",
		"Implemented (→ Fixed.md)": "complete",
		"Completed (→ Fixed.md)":   "complete",
		"Obsolete (→ Fixed.md)":    "obsolete",
		"Deleted":                  "obsolete",
	}
	for local, wantCol := range cases {
		got, ok := StatusColumn(local)
		if !ok || got != wantCol {
			t.Errorf("StatusColumn(%q) = (%q,%v), want (%q,true)", local, got, ok, wantCol)
		}
	}
	// Every grouped column MUST be one of the four the board actually has.
	allowed := map[string]bool{"to do": true, "in progress": true, "obsolete": true, "complete": true}
	for local := range cases {
		col, _ := StatusColumn(local)
		if !allowed[col] {
			t.Errorf("StatusColumn(%q) = %q — NOT an existing board column (would 400)", local, col)
		}
	}
	if _, ok := StatusColumn("Nonsense"); ok {
		t.Error("StatusColumn must reject an out-of-vocab status")
	}
}

// TestStatusLabelRoundTrip pins the exact-status LABEL: every status maps to a
// `status:<word>` tag (marker stripped) and StatusFromLabel recovers the EXACT
// local status from a task's tags (case-insensitive; ClickUp lowercases tags),
// so grouping never loses the precise state ("labels we may track and see").
func TestStatusLabelRoundTrip(t *testing.T) {
	cases := map[string]string{
		"Queued":             "status:Queued",
		"Operator-blocked":   "status:Operator-blocked",
		"Ready for testing":  "status:Ready for testing",
		"Fixed (→ Fixed.md)": "status:Fixed",
		"Deleted":            "status:Deleted",
	}
	for local, wantLbl := range cases {
		lbl, ok := StatusLabel(local)
		if !ok || lbl != wantLbl {
			t.Errorf("StatusLabel(%q) = (%q,%v), want (%q,true)", local, lbl, ok, wantLbl)
		}
		// ClickUp lowercases tag names — StatusFromLabel must recover regardless.
		back, ok := StatusFromLabel([]string{"Bug", strings.ToLower(lbl)})
		if !ok || back != local {
			t.Errorf("StatusFromLabel(lowercased %q) = (%q,%v), want (%q,true)", lbl, back, ok, local)
		}
	}
	if _, ok := StatusLabel("Nonsense"); ok {
		t.Error("StatusLabel must reject an out-of-vocab status")
	}
	// A task with only a Type tag (no status label) yields no status.
	if _, ok := StatusFromLabel([]string{"Feature"}); ok {
		t.Error("StatusFromLabel must not match a non-status tag")
	}
}
