package states

import (
	"math/rand"
	"strings"
	"testing"
)

func TestNormalizeEnum(t *testing.T) {
	s := mustSpec(t)
	ok := map[string]string{"": "UNSET", "  ": "UNSET", "UNSET": "UNSET", " reproduced-live ": "reproduced-live", "UNREPRODUCED": "UNREPRODUCED"}
	for in, want := range ok {
		r := s.Normalize(map[string]string{"reproduction_state": in})
		if len(r.Problems) != 0 || r.Values["reproduction_state"] != want {
			t.Errorf("enum %q -> %q problems=%v, want %q", in, r.Values["reproduction_state"], r.Problems, want)
		}
	}
}

// TestUnknownStateRefused is the closed-vocabulary proof: every near-miss of a
// legal token is refused, never mapped to UNSET or to the nearest value.
func TestUnknownStateRefused(t *testing.T) {
	s := mustSpec(t)
	for _, in := range []string{"unset", "Unset", "REPRODUCED-LIVE", "reproduced_live", "reproduced", "reproduced-live!", "blocked", "maybe", "0", "null", "none", "reproduced-live,UNSET", "reproduced\nlive"} {
		r := s.Normalize(map[string]string{"reproduction_state": in})
		if len(r.Problems) != 1 || r.Problems[0].Key != "reproduction_state" {
			t.Errorf("%q: problems=%v, want exactly one refusal", in, r.Problems)
		}
		if v, present := r.Values["reproduction_state"]; present {
			t.Errorf("%q: refused value leaked into Values as %q", in, v)
		}
	}
	// Each state's vocabulary is separate: an investigation token is not a reproduction token.
	r := s.Normalize(map[string]string{"reproduction_state": "in-progress"})
	if len(r.Problems) != 1 {
		t.Errorf("cross-vocabulary token accepted: %v", r)
	}
}

func TestNormalizeTextCanonical(t *testing.T) {
	s := mustSpec(t)
	r := s.Normalize(map[string]string{"reproduction_red_cmd": "  bash a.sh\r\nline2\rline3 \n\n"})
	if got := r.Values["reproduction_red_cmd"]; got != "bash a.sh\nline2\nline3" {
		t.Fatalf("text = %q", got)
	}
	r = s.Normalize(map[string]string{"reproduction_red_cmd": "x\x00y"})
	if len(r.Problems) != 1 {
		t.Fatalf("NUL accepted: %v", r)
	}
	r = s.Normalize(map[string]string{"reproduction_red_cmd": "x\x1b[0my"})
	if len(r.Problems) != 1 {
		t.Fatalf("ESC accepted: %v", r)
	}
	r = s.Normalize(map[string]string{"reproduction_red_cmd": string([]byte{0xff, 0xfe})})
	if len(r.Problems) != 1 {
		t.Fatalf("invalid UTF-8 accepted: %v", r)
	}
	r = s.Normalize(map[string]string{"reproduction_red_cmd": "a\u00a0b"})
	if r.Values["reproduction_red_cmd"] != "a b" {
		t.Fatalf("NBSP not folded: %q", r.Values["reproduction_red_cmd"])
	}
}

func TestNormalizeTextClamp(t *testing.T) {
	s := mustSpec(t)
	long := strings.Repeat("é", 2500) // limit for reproduction_red_cmd is 2000 runes
	r := s.Normalize(map[string]string{"reproduction_red_cmd": long})
	got := r.Values["reproduction_red_cmd"]
	if n := len([]rune(got)); n != 2000 {
		t.Fatalf("clamped to %d runes, want 2000", n)
	}
	if len(r.Clamped) != 1 || r.Clamped[0] != "reproduction_red_cmd" {
		t.Fatalf("clamped = %v", r.Clamped)
	}
	exact := strings.Repeat("x", 2000)
	r = s.Normalize(map[string]string{"reproduction_red_cmd": exact})
	if len(r.Clamped) != 0 || r.Values["reproduction_red_cmd"] != exact {
		t.Fatalf("value at the limit was clamped")
	}
}

func TestNormalizeAbsentKeysAreUnset(t *testing.T) {
	s := mustSpec(t)
	r := s.Normalize(map[string]string{})
	if r.Values["reproduction_state"] != "UNSET" || r.Values["investigation_state"] != "UNSET" || r.Values["reproduction_env"] != "" {
		t.Fatalf("absent keys: %v", r.Values)
	}
	if len(r.Values) != 8 {
		t.Fatalf("values = %d, want all 8 keys", len(r.Values))
	}
}

// TestPropNormalizeIdempotent: Normalize(Normalize(x)) == Normalize(x) for random text.
func TestPropNormalizeIdempotent(t *testing.T) {
	s := mustSpec(t)
	r := rand.New(rand.NewSource(20261103))
	for i := 0; i < 3000; i++ {
		in := randString(r, 60)
		first := s.Normalize(map[string]string{"reproduction_env": in})
		if len(first.Problems) != 0 {
			continue
		}
		second := s.Normalize(map[string]string{"reproduction_env": first.Values["reproduction_env"]})
		if len(second.Problems) != 0 || second.Values["reproduction_env"] != first.Values["reproduction_env"] {
			t.Fatalf("not idempotent for %q: %q then %q", in, first.Values["reproduction_env"], second.Values["reproduction_env"])
		}
	}
}
