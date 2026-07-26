package mapper

import "testing"

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
// case-insensitive equality via the mapped form, and no false-match on unmapped.
func TestRemoteStatusMatchesLocal(t *testing.T) {
	if !RemoteStatusMatchesLocal("fixed", "Fixed (→ Fixed.md)") {
		t.Error("remote 'fixed' should match local 'Fixed (→ Fixed.md)' (case-insensitive)")
	}
	if RemoteStatusMatchesLocal("In progress", "Queued") {
		t.Error("drifted statuses must NOT match")
	}
	if RemoteStatusMatchesLocal("Anything", "Nonsense-local") {
		t.Error("an unmapped local status can never claim a match")
	}
}
