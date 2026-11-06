package states

import (
	"strings"
	"testing"
)

func TestLoadSpecAccepts(t *testing.T) {
	s := mustSpec(t)
	if got := len(s.Fields); got != 8 {
		t.Fatalf("fields = %d, want 8", got)
	}
	if s.ID() != "TB-STATES" {
		t.Fatalf("ID = %q", s.ID())
	}
	want := []string{"reproduction_state", "investigation_state", "reproduction_evidence", "reproduction_red_cmd",
		"reproduction_green_cmd", "reproduction_env", "investigation_root_cause", "investigation_evidence"}
	if strings.Join(s.Keys(), ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v", s.Keys())
	}
	f, _ := s.Field("reproduction_state")
	if strings.Join(f.NonUnsetValues(), ",") != "UNREPRODUCED,blocked-no-device-with-recorded-blocker,not-reproducible-with-evidence,reproduced-host-fixture,reproduced-live" {
		t.Fatalf("non-unset values = %v", f.NonUnsetValues())
	}
}

func TestLoadSpecRefusals(t *testing.T) {
	cases := map[string]string{
		"unknown-member":  `{"version":1,"fields":[{"key":"a","kind":"text"}],"extra":1}`,
		"wrong-version":   `{"version":2,"fields":[{"key":"a","kind":"text"}]}`,
		"no-fields":       `{"version":1,"fields":[]}`,
		"dup-key":         `{"version":1,"fields":[{"key":"a","kind":"text"},{"key":"a","kind":"text"}]}`,
		"bad-key":         `{"version":1,"fields":[{"key":"A-b","kind":"text"}]}`,
		"enum-one-value":  `{"version":1,"fields":[{"key":"a","kind":"enum","values":["X"],"unset":"X"}]}`,
		"unset-not-in":    `{"version":1,"fields":[{"key":"a","kind":"enum","values":["X","Y"],"unset":"Z"}]}`,
		"unset-missing":   `{"version":1,"fields":[{"key":"a","kind":"enum","values":["X","Y"]}]}`,
		"value-space":     `{"version":1,"fields":[{"key":"a","kind":"enum","values":["X y","Z"],"unset":"Z"}]}`,
		"value-dup":       `{"version":1,"fields":[{"key":"a","kind":"enum","values":["X","X"],"unset":"X"}]}`,
		"text-with-vals":  `{"version":1,"fields":[{"key":"a","kind":"text","values":["X"]}]}`,
		"enum-maxrunes":   `{"version":1,"fields":[{"key":"a","kind":"enum","values":["X","Y"],"unset":"X","max_runes":5}]}`,
		"neg-maxrunes":    `{"version":1,"fields":[{"key":"a","kind":"text","max_runes":-1}]}`,
		"bad-kind":        `{"version":1,"fields":[{"key":"a","kind":"number"}]}`,
		"bad-block-id":    `{"version":1,"block_id":"x","fields":[{"key":"a","kind":"text"}]}`,
		"trailing-data":   `{"version":1,"fields":[{"key":"a","kind":"text"}]} {}`,
		"not-json":        `nope`,
		"value-injection": `{"version":1,"fields":[{"key":"a","kind":"enum","values":["X\nY","Z"],"unset":"Z"}]}`,
	}
	for name, in := range cases {
		if _, err := LoadSpec([]byte(in)); err == nil {
			t.Errorf("%s: spec accepted, want refusal", name)
		} else if _, ok := err.(*ErrSpec); !ok {
			t.Errorf("%s: error type %T, want *ErrSpec", name, err)
		}
	}
}
