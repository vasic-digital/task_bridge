// Status vocabulary + GROUPING (operator rule 2026-07-27).
//
// The local §11.4.93 closed-set status maps to TWO remote concepts:
//
//	(a) the EXISTING ClickUp board COLUMN it GROUPS into — statusToColumn, a
//	    MANY-to-ONE map onto only the columns the board actually has (the value
//	    pushed as the task Status). The board has a small fixed column set, so our
//	    ~11 lifecycle statuses are grouped one-or-more into each column. This is
//	    the fix for the prior 400 "Status does not exist" errors (each of our
//	    lifecycle-only names was pushed 1:1 to a non-existent column).
//	(b) the EXACT-status LABEL word — statusToRemote (the §11.4.33 `(→ Fixed.md)`
//	    migration marker stripped) — attached to every task as a `status:<word>`
//	    ClickUp tag (StatusLabel) so the PRECISE state stays visible + trackable +
//	    filterable even after grouping ("All statuses still must be added as proper
//	    labels we may track and see!" — operator, 2026-07-27).
//
// Both maps are LITERAL constants, never derived (§11.4.6). The column set is
// read LIVE from the board (see statusToColumn doc), never invented, never
// created — grouping targets ONLY existing columns.
package mapper

import "strings"

// statusToRemote is the EXACT remote LABEL word (P2 §3 table). It is the word
// attached as the `status:<word>` tracking tag (StatusLabel) and the exact value
// round-tripped on pull (StatusToLocal). DB->CU strips the §11.4.33 `(→ Fixed.md)`
// migration marker; CU->DB re-attaches it. This map is UNCHANGED by the grouping
// work — grouping lives in statusToColumn below.
var statusToRemote = map[string]string{
	"Queued":                   "Queued",
	"In progress":              "In progress",
	"Ready for testing":        "Ready for testing",
	"In testing":               "In testing",
	"Reopened":                 "Reopened",
	"Operator-blocked":         "Operator-blocked",
	"Fixed (→ Fixed.md)":       "Fixed",
	"Implemented (→ Fixed.md)": "Implemented",
	"Completed (→ Fixed.md)":   "Completed",
	"Obsolete (→ Fixed.md)":    "Obsolete",
	"Deleted":                  "Deleted", // P0 §5.5 tombstone
}

// statusToColumn GROUPS each local status into one of the EXISTING ClickUp board
// status COLUMNS. The 4 target columns were read LIVE 2026-07-27 from lists
// 901818394542 / 901818658244 / 901819412403 (all inherit, via folder/space,
// the identical set: `to do` [open], `in progress` [custom], `obsolete` [custom],
// `complete` [closed]). The map targets ONLY those existing columns and NEVER
// invents nor creates one (§11.4.6 + no blind board mutation). Because grouping
// is lossy, the exact state is preserved as the `status:<word>` LABEL
// (StatusLabel) on every task.
//
// Grouping rationale (each target is an EXISTING column, closest by lifecycle
// meaning; no "blocked" column exists so Operator-blocked -> the in-flight
// column, and Deleted -> the terminal-but-not-done column):
//
//	to do        <- Queued
//	in progress  <- In progress, Ready for testing, In testing, Reopened, Operator-blocked
//	complete     <- Fixed, Implemented, Completed
//	obsolete     <- Obsolete, Deleted
var statusToColumn = map[string]string{
	"Queued":                   "to do",
	"In progress":              "in progress",
	"Ready for testing":        "in progress",
	"In testing":               "in progress",
	"Reopened":                 "in progress",
	"Operator-blocked":         "in progress", // no "blocked" column exists; closest in-flight column
	"Fixed (→ Fixed.md)":       "complete",
	"Implemented (→ Fixed.md)": "complete",
	"Completed (→ Fixed.md)":   "complete",
	"Obsolete (→ Fixed.md)":    "obsolete",
	"Deleted":                  "obsolete", // terminal tombstone; closest terminal-not-done column
}

// StatusLabelPrefix marks a ClickUp tag as a lifecycle-STATUS label (distinct
// from the Type tag Bug/Feature/Task), so the exact grouped-away state stays
// filterable + visible. ClickUp lowercases tag names, so all comparisons here
// are case-insensitive.
const StatusLabelPrefix = "status:"

// columnToLocal maps each existing COLUMN back to a REPRESENTATIVE local status.
// Grouping is many-to-one, so this reverse is LOSSY — the exact state is
// recovered from the `status:<word>` label FIRST (StatusFromLabel); this map is
// only the fallback when a pulled task carries no status label.
var columnToLocal = map[string]string{
	"to do":       "Queued",
	"in progress": "In progress",
	"obsolete":    "Obsolete (→ Fixed.md)",
	"complete":    "Fixed (→ Fixed.md)",
}

// remoteToLocal is the EXACT label-word reverse of statusToRemote (single source
// of truth). Used by StatusToLocal (lossless, exact word round-trip).
var remoteToLocal = func() map[string]string {
	m := make(map[string]string, len(statusToRemote))
	for local, remote := range statusToRemote {
		m[strings.ToLower(remote)] = local
	}
	return m
}()

// StatusToRemote maps a local status to its EXACT remote LABEL word. ok is false
// for a value outside the closed set — the caller MUST NOT guess (§11.4.6).
func StatusToRemote(local string) (remote string, ok bool) {
	r, ok := statusToRemote[local]
	return r, ok
}

// StatusToLocal maps an EXACT remote LABEL word back to the local closed-set
// value (case-insensitive). ok is false when the word is not in our vocab — a
// drift the engine surfaces rather than silently coercing (P2 §3 DZ-9).
func StatusToLocal(remote string) (local string, ok bool) {
	l, ok := remoteToLocal[strings.ToLower(strings.TrimSpace(remote))]
	return l, ok
}

// StatusColumn maps a local status to the EXISTING ClickUp COLUMN it groups into
// — the value PUSHED as the task Status. ok is false for an out-of-vocab status
// (never guessed, §11.4.6). This is the grouping that makes the push land in a
// column the board actually has (fixing the prior 400 "Status does not exist").
func StatusColumn(local string) (column string, ok bool) {
	c, ok := statusToColumn[local]
	return c, ok
}

// StatusLabel returns the `status:<word>` tracking LABEL for a local status (the
// §11.4.33 marker stripped — it is a doc-file pointer, not part of the lifecycle
// STATE). ok is false for an out-of-vocab status. Attached as a ClickUp tag so
// the exact state stays visible after grouping (operator rule 2026-07-27).
func StatusLabel(local string) (label string, ok bool) {
	w, ok := statusToRemote[local]
	if !ok {
		return "", false
	}
	return StatusLabelPrefix + w, true
}

// StatusFromLabel recovers the EXACT local status from a task's tags by reading
// its `status:<word>` label (case-insensitive; ClickUp lowercases tag names).
// ok is false when no recognized status label is present. This is the lossless
// pull path (preferred over the lossy ColumnToLocalRepresentative).
func StatusFromLabel(tags []string) (local string, ok bool) {
	for _, t := range tags {
		tl := strings.ToLower(strings.TrimSpace(t))
		if !strings.HasPrefix(tl, StatusLabelPrefix) {
			continue
		}
		word := strings.TrimSpace(tl[len(StatusLabelPrefix):])
		if l, found := remoteToLocal[word]; found {
			return l, true
		}
	}
	return "", false
}

// IsStatusLabel reports whether a tag is a lifecycle-status label (`status:<…>`,
// case-insensitive). Used by the update path to leave Type/other tags untouched
// while reconciling ONLY the status label.
func IsStatusLabel(tag string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), StatusLabelPrefix)
}

// ColumnToLocalRepresentative maps a COLUMN back to a REPRESENTATIVE local status
// (LOSSY — grouping is many-to-one; see columnToLocal). ok is false for an
// unknown column. Prefer StatusFromLabel for the exact state.
func ColumnToLocalRepresentative(column string) (local string, ok bool) {
	l, ok := columnToLocal[strings.ToLower(strings.TrimSpace(column))]
	return l, ok
}

// RemoteStatusMatchesLocal reports whether the remote task's COLUMN already
// equals the grouped ClickUp column for the local status (case-insensitive).
// Used by the reconcile diff to decide whether a matched item's column is
// drifted. (The exact-status label is carried additively via StatusLabel; column
// convergence is what gates the UPDATE push.)
func RemoteStatusMatchesLocal(remoteStatus, localStatus string) bool {
	want, ok := StatusColumn(localStatus)
	if !ok {
		return false // unknown local status: cannot claim it matches
	}
	return strings.EqualFold(strings.TrimSpace(remoteStatus), strings.TrimSpace(want))
}
