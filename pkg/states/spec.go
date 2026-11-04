// Package states carries a consumer-defined set of extra tracker fields
// (closed-vocabulary enum fields such as a reproduction / investigation state,
// and free-text detail fields) from the local workable-items database to a
// remote board, deterministically and idempotently.
//
// DECOUPLING (§11.4.28): this package knows no project vocabulary. The field
// keys, the enum vocabularies and the marker id come from a JSON Spec the
// CONSUMER supplies at runtime (see LoadSpec). The package is PURE: no I/O, no
// network, no clock, so every function is unit-, golden- and property-testable.
//
// Two remote carriers exist per field and are chosen per board list:
//
//	FIELD  a ClickUp custom field (drop_down for enum, text for text) that the
//	       list already exposes under the field's canonical name; and
//	BLOCK  a managed block inside the task description, delimited by
//	       [<ID>:BEGIN] / [<ID>:END] marker lines, rewritten idempotently.
//
// ClickUp's public API has no documented endpoint to create a custom field
// (docs/research/clickup_integration/P2_schema_field_mapping_design.md), so BLOCK
// is the carrier that works with zero provisioning and FIELD is used where the
// operator (or a guarded create) has provisioned the field.
package states

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Kind is the value shape of a field.
type Kind string

const (
	// KindEnum is a closed vocabulary; the remote carrier is a drop-down.
	KindEnum Kind = "enum"
	// KindText is free text; the remote carrier is a text custom field.
	KindText Kind = "text"
)

// DefaultBlockID is the marker id used when the Spec does not set one.
const DefaultBlockID = "TB-STATES"

// DefaultMaxRunes is the clamp applied to a text field with no max_runes.
const DefaultMaxRunes = 8000

// SpecVersion is the only Spec schema version this package understands.
const SpecVersion = 1

var (
	keyRe     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	enumValRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	blockIDRe = regexp.MustCompile(`^[A-Z][A-Z0-9-]{2,30}$`)
)

// FieldSpec describes one tracked field.
type FieldSpec struct {
	// Key is the local DB column AND the canonical remote name (a custom field is
	// matched by this name, case-/space-/hyphen-insensitively).
	Key  string `json:"key"`
	Kind Kind   `json:"kind"`
	// Label is the human label (optional; informational, not used for matching).
	Label string `json:"label,omitempty"`
	// Values is the closed vocabulary of an enum field, including Unset.
	Values []string `json:"values,omitempty"`
	// Unset names the vocabulary value that means "no state recorded". The
	// remote carries it as an ABSENT value (no block line / cleared field).
	Unset string `json:"unset,omitempty"`
	// MaxRunes clamps a text value (deterministic truncation, reported).
	MaxRunes int `json:"max_runes,omitempty"`
}

// Spec is the consumer-supplied field set.
type Spec struct {
	Version int         `json:"version"`
	BlockID string      `json:"block_id,omitempty"`
	Fields  []FieldSpec `json:"fields"`
}

// ErrSpec is returned for any structurally invalid Spec.
type ErrSpec struct{ Msg string }

func (e *ErrSpec) Error() string { return "states: invalid spec: " + e.Msg }

// LoadSpec decodes and validates a Spec. Unknown JSON members are refused so a
// typo in the consumer's spec can never silently weaken a vocabulary.
func LoadSpec(data []byte) (*Spec, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return nil, &ErrSpec{Msg: err.Error()}
	}
	if dec.More() {
		return nil, &ErrSpec{Msg: "trailing data after the spec object"}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// ID returns the marker id (DefaultBlockID when unset).
func (s *Spec) ID() string {
	if s.BlockID == "" {
		return DefaultBlockID
	}
	return s.BlockID
}

// Validate checks the Spec; it never mutates it.
func (s *Spec) Validate() error {
	if s.Version != SpecVersion {
		return &ErrSpec{Msg: fmt.Sprintf("version %d unsupported (want %d)", s.Version, SpecVersion)}
	}
	if s.BlockID != "" && !blockIDRe.MatchString(s.BlockID) {
		return &ErrSpec{Msg: fmt.Sprintf("block_id %q must match %s", s.BlockID, blockIDRe)}
	}
	if len(s.Fields) == 0 {
		return &ErrSpec{Msg: "no fields"}
	}
	seen := map[string]bool{}
	for i, f := range s.Fields {
		if !keyRe.MatchString(f.Key) {
			return &ErrSpec{Msg: fmt.Sprintf("field %d key %q must match %s", i, f.Key, keyRe)}
		}
		if seen[f.Key] {
			return &ErrSpec{Msg: fmt.Sprintf("duplicate key %q", f.Key)}
		}
		seen[f.Key] = true
		switch f.Kind {
		case KindEnum:
			if len(f.Values) < 2 {
				return &ErrSpec{Msg: fmt.Sprintf("enum %q needs at least 2 values", f.Key)}
			}
			vs := map[string]bool{}
			for _, v := range f.Values {
				if !enumValRe.MatchString(v) {
					return &ErrSpec{Msg: fmt.Sprintf("enum %q value %q must match %s", f.Key, v, enumValRe)}
				}
				if vs[v] {
					return &ErrSpec{Msg: fmt.Sprintf("enum %q duplicate value %q", f.Key, v)}
				}
				vs[v] = true
			}
			if f.Unset == "" || !vs[f.Unset] {
				return &ErrSpec{Msg: fmt.Sprintf("enum %q unset %q is not one of its values", f.Key, f.Unset)}
			}
			if f.MaxRunes != 0 {
				return &ErrSpec{Msg: fmt.Sprintf("enum %q must not set max_runes", f.Key)}
			}
		case KindText:
			if len(f.Values) != 0 || f.Unset != "" {
				return &ErrSpec{Msg: fmt.Sprintf("text %q must not set values/unset", f.Key)}
			}
			if f.MaxRunes < 0 {
				return &ErrSpec{Msg: fmt.Sprintf("text %q max_runes negative", f.Key)}
			}
		default:
			return &ErrSpec{Msg: fmt.Sprintf("field %q kind %q unknown", f.Key, f.Kind)}
		}
	}
	return nil
}

// Keys returns the field keys in spec order.
func (s *Spec) Keys() []string {
	out := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		out = append(out, f.Key)
	}
	return out
}

// Field returns the FieldSpec for key.
func (s *Spec) Field(key string) (FieldSpec, bool) {
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return FieldSpec{}, false
}

// RunesLimit returns the effective clamp of a text field.
func (f FieldSpec) RunesLimit() int {
	if f.MaxRunes > 0 {
		return f.MaxRunes
	}
	return DefaultMaxRunes
}

// NonUnsetValues returns an enum's vocabulary without Unset, sorted.
func (f FieldSpec) NonUnsetValues() []string {
	var out []string
	for _, v := range f.Values {
		if v != f.Unset {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
