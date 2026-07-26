// Bidirectional status-vocabulary map (P2 §3): the local §11.4.93 closed-set
// status <-> the ClickUp per-list status NAME. The map is a LITERAL constant,
// never derived (§11.4.6). DB->CU strips the §11.4.33 `(→ Fixed.md)` migration
// marker; CU->DB re-attaches it on the closure terminals.
package mapper

import "strings"

// statusToRemote is the single source of truth (P2 §3 table). The reverse map
// is built once from it.
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

// remoteToLocal is derived ONCE from statusToRemote (single source of truth).
var remoteToLocal = func() map[string]string {
	m := make(map[string]string, len(statusToRemote))
	for local, remote := range statusToRemote {
		m[strings.ToLower(remote)] = local
	}
	return m
}()

// StatusToRemote maps a local status to its ClickUp status NAME. ok is false
// for a value outside the closed set — the caller MUST NOT guess (§11.4.6).
func StatusToRemote(local string) (remote string, ok bool) {
	r, ok := statusToRemote[local]
	return r, ok
}

// StatusToLocal maps a ClickUp status NAME back to the local closed-set value
// (case-insensitive). ok is false when the remote status is not in our vocab —
// a drift the engine surfaces rather than silently coercing (P2 §3 DZ-9).
func StatusToLocal(remote string) (local string, ok bool) {
	l, ok := remoteToLocal[strings.ToLower(strings.TrimSpace(remote))]
	return l, ok
}

// RemoteStatusMatchesLocal reports whether the remote status NAME already
// equals the ClickUp form of the local status (case-insensitive). Used by the
// reconcile diff to decide whether a matched item's status is drifted.
func RemoteStatusMatchesLocal(remoteStatus, localStatus string) bool {
	want, ok := StatusToRemote(localStatus)
	if !ok {
		return false // unknown local status: cannot claim it matches
	}
	return strings.EqualFold(strings.TrimSpace(remoteStatus), strings.TrimSpace(want))
}
