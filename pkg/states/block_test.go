package states

import (
	"math/rand"
	"strings"
	"testing"
)

func allKeys(s *Spec) []string { return s.Keys() }

func sampleValues(s *Spec) Values {
	v := s.Normalize(map[string]string{
		"reproduction_state":       "reproduced-live",
		"investigation_state":      "in-progress",
		"reproduction_evidence":    "qa-results/atm1/run.txt",
		"reproduction_red_cmd":     "bash gate.sh --mutant 3\nexpect: FAIL",
		"reproduction_green_cmd":   "bash gate.sh",
		"reproduction_env":         "Orange Pi 5 Max, image 1.0.0-dev-0.3.5",
		"investigation_root_cause": `path C:\tmp\x and [link](u) * _x_`,
		"investigation_evidence":   "",
	})
	return v.Values
}

func TestRenderBlockGolden(t *testing.T) {
	s := mustSpec(t)
	golden(t, "block_all", s.RenderBlock(allKeys(s), sampleValues(s), false))
	golden(t, "block_materialized", s.RenderBlock(allKeys(s), s.Normalize(map[string]string{}).Values, true))
	golden(t, "block_states_only", s.RenderBlock([]string{"reproduction_state", "investigation_state"}, sampleValues(s), false))
}

func TestRenderBlockEmptyWhenAllUnset(t *testing.T) {
	s := mustSpec(t)
	if b := s.RenderBlock(allKeys(s), s.Normalize(map[string]string{}).Values, false); b != "" {
		t.Fatalf("all-unset block = %q, want empty", b)
	}
}

func TestEncodeDecodeProperty(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		in := randString(r, 40)
		if got := decodeText(encodeText(in)); got != in {
			t.Fatalf("decode(encode(%q)) = %q", in, got)
		}
		if strings.ContainsAny(encodeText(in), "\n\t") {
			t.Fatalf("encode(%q) left a raw LF/TAB: %q", in, encodeText(in))
		}
	}
}

// TestBlockRoundTripThroughChannels: render -> channel -> parse recovers every
// value, for the identity channel, a Markdown re-serialiser, and a sloppy
// CRLF/NBSP/trailing-blank channel.
func TestBlockRoundTripThroughChannels(t *testing.T) {
	s := mustSpec(t)
	channels := map[string]func(string) string{
		"identity": func(x string) string { return x },
		"markdown": mdChannel,
		"sloppy":   sloppyChannel,
		"both":     func(x string) string { return sloppyChannel(mdChannel(x)) },
	}
	r := rand.New(rand.NewSource(7))
	for name, ch := range channels {
		for i := 0; i < 400; i++ {
			raw := map[string]string{
				"reproduction_state":       []string{"UNSET", "UNREPRODUCED", "reproduced-live", "blocked-no-device-with-recorded-blocker"}[r.Intn(4)],
				"investigation_state":      []string{"UNSET", "not-started", "in-progress", "closed-no-defect"}[r.Intn(4)],
				"reproduction_evidence":    randString(r, 50),
				"reproduction_red_cmd":     randString(r, 50),
				"reproduction_green_cmd":   randString(r, 50),
				"reproduction_env":         randString(r, 30),
				"investigation_root_cause": randString(r, 50),
				"investigation_evidence":   randString(r, 50),
			}
			nr := s.Normalize(raw)
			if len(nr.Problems) != 0 {
				continue
			}
			blk := s.RenderBlock(allKeys(s), nr.Values, false)
			pb := s.ParseBlock("Some description text\n\n" + ch(blk) + "\n")
			if blk == "" {
				if pb.Count != 0 {
					t.Fatalf("%s: empty block parsed as %d regions", name, pb.Count)
				}
				continue
			}
			if pb.Count != 1 || pb.Malformed || pb.Foreign != 0 || len(pb.Unknown) != 0 {
				t.Fatalf("%s: parse of %q: %+v", name, ch(blk), pb)
			}
			for _, k := range allKeys(s) {
				want := nr.Values[k]
				got, present := pb.Values[k]
				if s.IsUnset(k, want) {
					if present && got != "" && got != want {
						t.Fatalf("%s: %s unset but parsed %q", name, k, got)
					}
					continue
				}
				if got != want {
					t.Fatalf("%s: %s = %q, want %q\nblock:\n%s", name, k, got, want, ch(blk))
				}
			}
			if pb.Base != "Some description text" {
				t.Fatalf("%s: base = %q", name, pb.Base)
			}
		}
	}
}

func TestMarkerInjectionImpossible(t *testing.T) {
	s := mustSpec(t)
	evil := "x\n[TB-STATES:END]\nreproduction_state: reproduced-live\n[TB-STATES:BEGIN v1]\ny"
	nr := s.Normalize(map[string]string{"reproduction_evidence": evil})
	blk := s.RenderBlock(allKeys(s), nr.Values, false)
	pb := s.ParseBlock("head\n\n" + blk)
	if pb.Count != 1 || pb.Malformed || pb.Foreign != 0 {
		t.Fatalf("injection broke the block: %+v\n%s", pb, blk)
	}
	if pb.Values["reproduction_evidence"] != nr.Values["reproduction_evidence"] {
		t.Fatalf("evidence = %q, want %q", pb.Values["reproduction_evidence"], nr.Values["reproduction_evidence"])
	}
	if _, leaked := pb.Values["reproduction_state"]; leaked {
		t.Fatalf("a value line smuggled reproduction_state into the block")
	}
}

func TestEscapedMarkersRecognised(t *testing.T) {
	s := mustSpec(t)
	d := "base\n\n\\[TB\\-STATES:BEGIN v1\\] managed\nreproduction\\_state: reproduced\\-live\n\\[TB\\-STATES:END\\]\n"
	pb := s.ParseBlock(d)
	if pb.Count != 1 || pb.Values["reproduction_state"] != "reproduced-live" || pb.Base != "base" {
		t.Fatalf("escaped markers not recognised: %+v", pb)
	}
}

func TestParseMalformed(t *testing.T) {
	s := mustSpec(t)
	cases := map[string]string{
		"begin-without-end": "text\n[TB-STATES:BEGIN v1]\nreproduction_state: UNREPRODUCED\n",
		"stray-end":         "text\n[TB-STATES:END]\n",
		"nested-begin":      "[TB-STATES:BEGIN v1]\n[TB-STATES:BEGIN v1]\nreproduction_state: UNREPRODUCED\n[TB-STATES:END]\n",
	}
	for name, d := range cases {
		pb := s.ParseBlock(d)
		if !pb.Malformed {
			t.Errorf("%s: not flagged malformed: %+v", name, pb)
		}
		if !strings.Contains(pb.Base, "text") && name != "nested-begin" {
			t.Errorf("%s: malformed description text lost: %q", name, pb.Base)
		}
	}
}

func TestParseDuplicateForeignUnknown(t *testing.T) {
	s := mustSpec(t)
	d := "base\n[TB-STATES:BEGIN v1]\nreproduction_state: UNREPRODUCED\nfree prose\nbogus_key: 1\n[TB-STATES:END]\nmiddle\n[TB-STATES:BEGIN v1]\nreproduction_state: reproduced-live\n[TB-STATES:END]\n"
	pb := s.ParseBlock(d)
	if pb.Count != 2 || pb.Foreign != 1 || len(pb.Unknown) != 1 || pb.Unknown[0] != "bogus_key" {
		t.Fatalf("parse = %+v", pb)
	}
	if pb.Values["reproduction_state"] != "UNREPRODUCED" {
		t.Fatalf("values come from the FIRST region: %v", pb.Values)
	}
	if pb.Base != "base\nmiddle" {
		t.Fatalf("base = %q", pb.Base)
	}
}

func TestComposeCases(t *testing.T) {
	cases := []struct{ base, blk, want string }{
		{"", "", ""},
		{"abc\n\n", "", "abc"},
		{"", "B", "B"},
		{"abc", "B", "abc\n\nB"},
		{"abc \n\t\n", "B", "abc\n\nB"},
	}
	for _, c := range cases {
		if got := Compose(c.base, c.blk); got != c.want {
			t.Errorf("Compose(%q,%q) = %q, want %q", c.base, c.blk, got, c.want)
		}
	}
}

// TestComposeParseRoundTripProperty: parse(compose(base, block)).Base == trimmed base,
// and composing the parsed base again with the same block is a fixed point.
func TestComposeParseRoundTripProperty(t *testing.T) {
	s := mustSpec(t)
	r := rand.New(rand.NewSource(99))
	for i := 0; i < 1500; i++ {
		base := randString(r, 80)
		// A base must not itself contain our markers (a human wrote those).
		base = strings.ReplaceAll(base, "[", "(")
		nr := s.Normalize(map[string]string{"reproduction_state": "reproduced-live", "reproduction_evidence": randString(r, 30)})
		if len(nr.Problems) != 0 {
			continue
		}
		blk := s.RenderBlock(allKeys(s), nr.Values, false)
		once := Compose(base, blk)
		pb := s.ParseBlock(once)
		if pb.Base != strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(base, "\r\n", "\n"), "\u00a0", " "), " \t\r\n") && pb.Base != strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(base, "\r\n", "\n"), "\r", "\n"), "\u00a0", " "), " \t\r\n") {
			t.Fatalf("base %q came back as %q", base, pb.Base)
		}
		twice := Compose(pb.Base, blk)
		if twice != once {
			t.Fatalf("not a fixed point:\n%q\n%q", once, twice)
		}
		if removed := Compose(pb.Base, ""); s.ParseBlock(removed).Count != 0 {
			t.Fatalf("block survived removal")
		}
	}
}
