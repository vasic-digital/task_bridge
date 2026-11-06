package states

import (
	"strings"
	"testing"
)

// The one stored and printed spelling of the "blocked" reproduction state (CLAUDE.md 1c, one-spelling ruling): the tracker schema's CHECK
// holds exactly this token. The short word `blocked-no-device` is an INPUT alias of the tracker's own writer only; it is never a stored or
// printed form, so it is not in this vocabulary and a row carrying it is refused loudly.
const (
	blockedLong  = "blocked-no-device-with-recorded-blocker"
	blockedShort = "blocked-no-device"
)

func TestBlockedStateSpellingIsTheLongForm(t *testing.T) {
	s := mustSpec(t)
	// CONTROL: the long token is accepted, kept verbatim, and survives block render -> parse.
	r := s.Normalize(map[string]string{"reproduction_state": blockedLong})
	if len(r.Problems) != 0 || r.Values["reproduction_state"] != blockedLong {
		t.Fatalf("long spelling must be accepted verbatim: %+v", r)
	}
	block := s.RenderBlock([]string{"reproduction_state"}, r.Values, false)
	if !strings.Contains(block, "reproduction_state: "+blockedLong) {
		t.Fatalf("block does not print the long spelling: %q", block)
	}
	if got := s.ParseBlock(block).Values["reproduction_state"]; got != blockedLong {
		t.Fatalf("round trip changed the spelling: %q", got)
	}
	// the short alias is refused (STATE_INVALID path), never mapped, never defaulted to UNSET
	r = s.Normalize(map[string]string{"reproduction_state": blockedShort})
	if len(r.Problems) != 1 || r.Problems[0].Key != "reproduction_state" {
		t.Fatalf("short alias must be refused: %+v", r)
	}
	if v, present := r.Values["reproduction_state"]; present {
		t.Fatalf("a refused value must be omitted, got %q", v)
	}
	// vocabulary: long present, short absent, and no token is a strict prefix of another (so a truncated spelling can never match by prefix)
	f, _ := s.Field("reproduction_state")
	have := map[string]bool{}
	for _, v := range f.Values {
		have[v] = true
	}
	if !have[blockedLong] || have[blockedShort] {
		t.Fatalf("vocabulary wrong: %v", f.Values)
	}
	for _, a := range f.Values {
		for _, b := range f.Values {
			if a != b && strings.HasPrefix(b, a) {
				t.Errorf("vocabulary token %q is a prefix of %q", a, b)
			}
		}
	}
}
