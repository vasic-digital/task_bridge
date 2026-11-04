package states

import (
	"fmt"
	"sort"
	"strings"
)

// Carrier is where a field lives remotely.
type Carrier string

const (
	// CarrierField is a ClickUp custom field.
	CarrierField Carrier = "FIELD"
	// CarrierBlock is the managed block in the task description.
	CarrierBlock Carrier = "BLOCK"
)

// CarrierMode is the operator's carrier policy.
type CarrierMode string

const (
	// ModeAuto uses a custom field where the list exposes a matching one, else the block.
	ModeAuto CarrierMode = "auto"
	// ModeBlock always uses the description block and ignores any custom field.
	ModeBlock CarrierMode = "block"
	// ModeField requires a custom field for every key and fails closed otherwise.
	ModeField CarrierMode = "field"
)

// ParseMode validates a --carrier value.
func ParseMode(v string) (CarrierMode, error) {
	switch CarrierMode(v) {
	case ModeAuto, ModeBlock, ModeField:
		return CarrierMode(v), nil
	}
	return "", fmt.Errorf("states: carrier mode %q (want auto|block|field)", v)
}

// OptionDef is one drop-down option of a custom field.
type OptionDef struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	OrderIndex int    `json:"orderindex"`
}

// FieldDef is a custom field a list exposes (GET /list/{id}/field).
type FieldDef struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Type    string      `json:"type"`
	Options []OptionDef `json:"options,omitempty"`
}

// ResStatus is the outcome of resolving a key against the list's custom fields.
type ResStatus string

const (
	ResOK           ResStatus = "OK"
	ResMissing      ResStatus = "MISSING"
	ResTypeMismatch ResStatus = "TYPE_MISMATCH"
	ResOptionsDrift ResStatus = "OPTIONS_DRIFT"
	ResAmbiguous    ResStatus = "AMBIGUOUS"
)

// Resolution is the per-key carrier decision.
type Resolution struct {
	Key          string    `json:"key"`
	Status       ResStatus `json:"status"`
	Carrier      Carrier   `json:"carrier"`
	Def          FieldDef  `json:"def,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	ExtraOptions []string  `json:"extra_options,omitempty"`
}

// CarrierPlan is the per-list result of ResolveCarriers.
type CarrierPlan struct {
	Mode CarrierMode  `json:"mode"`
	Res  []Resolution `json:"resolutions"`
}

// NormalizeName is how a custom field name is matched to a key: lower-cased,
// spaces and hyphens folded to underscores, runs collapsed.
func NormalizeName(n string) string {
	n = strings.ToLower(strings.TrimSpace(n))
	var b strings.Builder
	prev := false
	for _, r := range n {
		if r == ' ' || r == '-' || r == '_' || r == '\t' {
			if !prev {
				b.WriteByte('_')
			}
			prev = true
			continue
		}
		prev = false
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), "_")
}

// ResolveCarriers decides, per key, FIELD or BLOCK. In ModeField any key that
// cannot use a FIELD is an error (fail closed, before any write).
func (s *Spec) ResolveCarriers(defs []FieldDef, mode CarrierMode) (CarrierPlan, error) {
	plan := CarrierPlan{Mode: mode}
	byName := map[string][]FieldDef{}
	for _, d := range defs {
		n := NormalizeName(d.Name)
		byName[n] = append(byName[n], d)
	}
	var unresolved []string
	for _, f := range s.Fields {
		r := Resolution{Key: f.Key, Carrier: CarrierBlock}
		if mode == ModeBlock {
			r.Status = ResMissing
			r.Reason = "carrier mode block: custom fields ignored"
			plan.Res = append(plan.Res, r)
			continue
		}
		cands := byName[NormalizeName(f.Key)]
		switch {
		case len(cands) == 0:
			r.Status = ResMissing
			r.Reason = "no custom field named " + f.Key
		case len(cands) > 1:
			r.Status = ResAmbiguous
			r.Reason = fmt.Sprintf("%d custom fields normalise to %s", len(cands), f.Key)
		default:
			d := cands[0]
			r.Def = d
			if err := checkDef(f, d, &r); err != "" {
				r.Reason = err
			} else {
				r.Status = ResOK
				r.Carrier = CarrierField
			}
		}
		if r.Carrier != CarrierField {
			unresolved = append(unresolved, f.Key+"="+string(r.Status))
		}
		plan.Res = append(plan.Res, r)
	}
	if mode == ModeField && len(unresolved) > 0 {
		return plan, fmt.Errorf("states: carrier mode field but unresolved: %s", strings.Join(unresolved, ", "))
	}
	return plan, nil
}

// checkDef validates def against f and fills r.Status on mismatch. It returns a
// non-empty reason on mismatch.
func checkDef(f FieldSpec, d FieldDef, r *Resolution) string {
	switch f.Kind {
	case KindEnum:
		if d.Type != "drop_down" {
			r.Status = ResTypeMismatch
			return fmt.Sprintf("type %q, want drop_down", d.Type)
		}
		have := map[string]bool{}
		for _, o := range d.Options {
			have[strings.TrimSpace(o.Name)] = true
		}
		var missing []string
		for _, v := range f.NonUnsetValues() {
			if !have[v] {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			r.Status = ResOptionsDrift
			return "missing options: " + strings.Join(missing, ", ")
		}
		allowed := map[string]bool{}
		for _, v := range f.Values {
			allowed[v] = true
		}
		for _, o := range d.Options {
			if !allowed[strings.TrimSpace(o.Name)] {
				r.ExtraOptions = append(r.ExtraOptions, strings.TrimSpace(o.Name))
			}
		}
		sort.Strings(r.ExtraOptions)
	case KindText:
		if d.Type != "text" && d.Type != "short_text" {
			r.Status = ResTypeMismatch
			return fmt.Sprintf("type %q, want text or short_text", d.Type)
		}
	}
	return ""
}

// Of returns the carrier of key (BLOCK for an unknown key).
func (p CarrierPlan) Of(key string) Carrier {
	for _, r := range p.Res {
		if r.Key == key {
			return r.Carrier
		}
	}
	return CarrierBlock
}

// Def returns the custom-field definition behind a FIELD key.
func (p CarrierPlan) Def(key string) (FieldDef, bool) {
	for _, r := range p.Res {
		if r.Key == key && r.Carrier == CarrierField {
			return r.Def, true
		}
	}
	return FieldDef{}, false
}

// Keys returns the keys carried by c, in spec order.
func (p CarrierPlan) Keys(c Carrier) []string {
	var out []string
	for _, r := range p.Res {
		if r.Carrier == c {
			out = append(out, r.Key)
		}
	}
	return out
}

// OptionID returns the option id of an enum value in def (exact trimmed name).
func OptionID(d FieldDef, value string) (string, bool) {
	for _, o := range d.Options {
		if strings.TrimSpace(o.Name) == value {
			return o.ID, true
		}
	}
	return "", false
}

// Provision describes a custom field an operator (or a guarded create) must
// provision for a key to use the FIELD carrier.
type Provision struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"`
	Reason  string   `json:"reason"`
}

// Provisions lists, in spec order, every key whose resolution is not OK, with
// the exact name, ClickUp type and drop-down options to provision.
func (s *Spec) Provisions(p CarrierPlan) []Provision {
	var out []Provision
	for _, r := range p.Res {
		if r.Status == ResOK || p.Mode == ModeBlock {
			continue
		}
		f, _ := s.Field(r.Key)
		pr := Provision{Key: r.Key, Reason: string(r.Status) + ": " + r.Reason}
		if f.Kind == KindEnum {
			pr.Type = "drop_down"
			pr.Options = f.NonUnsetValues()
		} else {
			pr.Type = "text"
		}
		out = append(out, pr)
	}
	return out
}
