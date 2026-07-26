package mapper

import "testing"

// TestParseKey pins the title-PREFIX key parser: it must recognize a real item
// key (ATM/SPK/MVR-NNN) ONLY when it is a genuine prefix, MUST NOT mistake an
// [ATM-DERIVED-*] task for a real key (the discriminator that keeps the 57
// derived tasks out of the INVESTIGATE bucket), and MUST NOT bind a task to an
// item whose key appears only MID-title (the N4 embedded-key safety case —
// §11.4.111 match the documented identity, not any bracketed prose token).
func TestParseKey(t *testing.T) {
	cases := []struct {
		title   string
		wantKey string
		wantOK  bool
	}{
		{"[ATM-013] Netflix locked to SD", "ATM-013", true},
		{"[SPK-042] speaker discovery", "SPK-042", true},
		{"[MVR-001] some item", "MVR-001", true},
		// Leading whitespace before the prefix is allowed (^\s*).
		{"  [ATM-013] indented prefix", "ATM-013", true},
		{"\t[SPK-042] tab-indented", "SPK-042", true},
		// N4: a key that appears only MID-title is NOT a prefix key -> UNKEYED.
		// Previously the lenient (any-position) parser mis-matched "ATM-373"
		// here; prefix-anchoring correctly leaves it for an operator decision.
		{"prefix noise [ATM-373] trailing", "", false},
		// N4: the embedded-key case — a DERIVED (unkeyed) prefix followed by a
		// real key later in the title MUST parse as UNKEYED (the mid-title
		// [ATM-013] must NOT bind the task to item ATM-013).
		{"[ATM-DERIVED-9] real [ATM-013] embedded", "", false},
		{"[ATM-013] real prefix [ATM-999] later noise", "ATM-013", true}, // first (prefix) key wins
		// DERIVED must NOT parse as a key (letters follow the first dash).
		{"[ATM-DERIVED-042] derived task", "", false},
		{"[ATM-DERIVED-013] another derived", "", false},
		// No bracketed key at all.
		{"just a free-form task title", "", false},
		{"", "", false},
		// lowercase prefix is not a valid key.
		{"[atm-013] lowercase", "", false},
		// single-letter prefix is not a valid key (>=2 letters required).
		{"[A-013] too short", "", false},
	}
	for _, c := range cases {
		gotKey, gotOK := ParseKey(c.title)
		if gotKey != c.wantKey || gotOK != c.wantOK {
			t.Errorf("ParseKey(%q) = (%q,%v), want (%q,%v)", c.title, gotKey, gotOK, c.wantKey, c.wantOK)
		}
	}
}

// TestTitleWithKeyRoundTrips proves a built title is re-parseable by ParseKey
// (so a task created by this bridge is matchable on the next reconcile).
func TestTitleWithKeyRoundTrips(t *testing.T) {
	name := TitleWithKey("ATM-500", "brand new item")
	got, ok := ParseKey(name)
	if !ok || got != "ATM-500" {
		t.Fatalf("round-trip: ParseKey(%q) = (%q,%v), want (ATM-500,true)", name, got, ok)
	}
}
