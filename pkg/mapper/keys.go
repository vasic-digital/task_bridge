// Key parsing for the TITLE-PREFIX matching mode (minimum-viable sync).
//
// REALITY (verified live, FACT): the existing remote tasks carry NO custom
// fields — their cross-system key lives as a `[ATM-NNN]` / `[SPK-NNN]` prefix
// embedded in the task TITLE. The full P2 design keyed on a custom field
// (`ATM_ID`); this file implements the title-prefix key that the live board
// actually uses, so reconcile matches real tasks (§11.4.6 — match reality, not
// the assumed schema).
package mapper

import "regexp"

// keyInTitle matches a bracketed workable-item key ANCHORED to the START of the
// task title (after optional leading whitespace) — the documented title-PREFIX
// convention this bridge writes (TitleWithKey) and the live board uses. The key
// is `<PREFIX>-<NNN>` where PREFIX is >=2 uppercase letters and NNN is
// one-or-more digits (e.g. ATM-013, SPK-042, MVR-001).
//
// Anchoring to the prefix (`^\s*`) is the SAFE parse (constitution §11.4.111 —
// match the documented identity, not any bracketed token that happens to appear
// in prose). A key mentioned MID-title (e.g. `[ATM-DERIVED-9] … [ATM-013]`, or
// free-form prose that references an item id) is NOT a genuine prefix key and
// MUST NOT bind the task to that item — it parses as UNKEYED and is left to an
// operator decision. (Verified against the live board: prefix-anchoring
// reclassifies ZERO of the 268 real tasks — every real key is a genuine prefix.)
//
// It also DELIBERATELY does NOT match `[ATM-DERIVED-042]`: after the prefix the
// pattern requires `-<digits>`, but a DERIVED task has `-DERIVED-...` where a
// non-digit follows the first dash — so those tasks parse as UNKEYED and are
// never mistaken for a real item key (they need a separate operator decision).
var keyInTitle = regexp.MustCompile(`^\s*\[([A-Z]{2,}-\d+)\]`)

// ParseKey extracts the `[XXX-NNN]` item key from the START of a remote task
// title (the title-PREFIX convention). ok is false when the title carries no
// recognizable prefix key (e.g. an `[ATM-DERIVED-*]` task, a free-form task with
// no bracketed key, or a task whose only `[XXX-NNN]` token appears mid-title).
func ParseKey(title string) (key string, ok bool) {
	m := keyInTitle.FindStringSubmatch(title)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// TitleWithKey renders a remote task name that embeds the item key as the
// title prefix (the on-board convention this bridge matches). Used on CREATE so
// a newly-pushed task is round-trip-matchable by ParseKey.
func TitleWithKey(key, title string) string {
	return "[" + key + "] " + title
}
