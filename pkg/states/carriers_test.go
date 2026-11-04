package states

import (
	"strings"
	"testing"
)

func ddDef(id, name string, opts ...string) FieldDef {
	d := FieldDef{ID: id, Name: name, Type: "drop_down"}
	for i, o := range opts {
		d.Options = append(d.Options, OptionDef{ID: id + "-o" + itoa(i), Name: o, OrderIndex: i})
	}
	return d
}

func fullDefs(s *Spec) []FieldDef {
	var defs []FieldDef
	for _, f := range s.Fields {
		if f.Kind == KindEnum {
			defs = append(defs, ddDef("cf-"+f.Key, f.Key, f.NonUnsetValues()...))
		} else {
			defs = append(defs, FieldDef{ID: "cf-" + f.Key, Name: f.Key, Type: "text"})
		}
	}
	return defs
}

func TestResolveAllProvisioned(t *testing.T) {
	s := mustSpec(t)
	p, err := s.ResolveCarriers(fullDefs(s), ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Keys(CarrierField)) != 8 || len(p.Keys(CarrierBlock)) != 0 {
		t.Fatalf("carriers: %v / %v", p.Keys(CarrierField), p.Keys(CarrierBlock))
	}
	if got := s.Provisions(p); len(got) != 0 {
		t.Fatalf("provisions = %v, want none", got)
	}
}

func TestResolveNoFieldsFallsBackToBlock(t *testing.T) {
	s := mustSpec(t)
	p, err := s.ResolveCarriers(nil, ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Keys(CarrierBlock)) != 8 {
		t.Fatalf("block keys = %v", p.Keys(CarrierBlock))
	}
	prov := s.Provisions(p)
	if len(prov) != 8 || prov[0].Key != "reproduction_state" || prov[0].Type != "drop_down" || len(prov[0].Options) != 5 || prov[2].Type != "text" {
		t.Fatalf("provisions = %+v", prov)
	}
}

func TestResolveMixedAndBadFields(t *testing.T) {
	s := mustSpec(t)
	defs := fullDefs(s)
	// reproduction_state: drop one option; investigation_state: wrong type; evidence: duplicate; red_cmd: renamed with spaces/case.
	defs[0] = ddDef("cf-rs", "reproduction_state", "UNREPRODUCED", "reproduced-live")
	defs[1] = FieldDef{ID: "cf-is", Name: "investigation_state", Type: "short_text"}
	defs = append(defs, FieldDef{ID: "cf-dup", Name: "Reproduction Evidence", Type: "text"})
	defs[3] = FieldDef{ID: "cf-red", Name: "Reproduction RED command", Type: "text"} // does not normalise to reproduction_red_cmd
	defs[4] = FieldDef{ID: "cf-green", Name: "Reproduction  Green Cmd", Type: "short_text"}
	p, err := s.ResolveCarriers(defs, ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]ResStatus{}
	for _, r := range p.Res {
		status[r.Key] = r.Status
	}
	want := map[string]ResStatus{
		"reproduction_state":     ResOptionsDrift,
		"investigation_state":    ResTypeMismatch,
		"reproduction_evidence":  ResAmbiguous,
		"reproduction_red_cmd":   ResMissing,
		"reproduction_green_cmd": ResOK,
		"reproduction_env":       ResOK,
	}
	for k, w := range want {
		if status[k] != w {
			t.Errorf("%s = %s, want %s", k, status[k], w)
		}
	}
	if p.Of("reproduction_green_cmd") != CarrierField || p.Of("reproduction_state") != CarrierBlock {
		t.Errorf("carrier choice wrong")
	}
	if d, ok := p.Def("reproduction_green_cmd"); !ok || d.ID != "cf-green" {
		t.Errorf("def = %+v", d)
	}
}

func TestResolveTextFieldOfWrongType(t *testing.T) {
	s := mustSpec(t)
	defs := fullDefs(s)
	defs[2] = FieldDef{ID: "cf-ev", Name: "reproduction_evidence", Type: "number"}
	p, err := s.ResolveCarriers(defs, ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if p.Res[2].Status != ResTypeMismatch || p.Of("reproduction_evidence") != CarrierBlock {
		t.Fatalf("a text field of type number must not be used: %+v", p.Res[2])
	}
}

func TestResolveExtraOptionsAllowedAndReported(t *testing.T) {
	s := mustSpec(t)
	defs := fullDefs(s)
	defs[0] = ddDef("cf-rs", "Reproduction State", "UNREPRODUCED", "reproduced-live", "reproduced-host-fixture", "not-reproducible-with-evidence", "blocked-no-device", "UNSET", "legacy-x")
	p, _ := s.ResolveCarriers(defs, ModeAuto)
	if p.Res[0].Status != ResOK || strings.Join(p.Res[0].ExtraOptions, ",") != "legacy-x" {
		t.Fatalf("res = %+v", p.Res[0])
	}
}

func TestResolveModes(t *testing.T) {
	s := mustSpec(t)
	if p, err := s.ResolveCarriers(fullDefs(s), ModeBlock); err != nil || len(p.Keys(CarrierField)) != 0 {
		t.Fatalf("block mode must ignore fields: %v %v", p.Keys(CarrierField), err)
	}
	if _, err := s.ResolveCarriers(nil, ModeField); err == nil {
		t.Fatal("field mode with no fields must fail closed")
	}
	if _, err := s.ResolveCarriers(fullDefs(s), ModeField); err != nil {
		t.Fatalf("field mode with all fields: %v", err)
	}
	if _, err := ParseMode("both"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{
		"Reproduction State": "reproduction_state", " reproduction-state ": "reproduction_state", "REPRODUCTION__STATE": "reproduction_state",
		"reproduction_state": "reproduction_state", "-x-": "x",
	} {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOptionID(t *testing.T) {
	d := ddDef("f", "x", "A", "B")
	if id, ok := OptionID(d, "B"); !ok || id != "f-o1" {
		t.Fatalf("id = %q %v", id, ok)
	}
	if _, ok := OptionID(d, "C"); ok {
		t.Fatal("unknown option resolved")
	}
}
