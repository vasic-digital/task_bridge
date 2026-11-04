package states

import (
	"fmt"
	"strings"
)

// DescSource says what the remote description text is.
type DescSource string

const (
	// DescMarkdown: the text is the Markdown SOURCE returned by the API.
	DescMarkdown DescSource = "markdown"
	// DescEmpty: the task has no description at all.
	DescEmpty DescSource = "empty"
	// DescPlain: only a rendered plain-text description was returned, so the
	// Markdown source is unknown and the description must not be rewritten.
	DescPlain DescSource = "plain"
)

// RemoteField is a task's current value of one custom field.
type RemoteField struct {
	ID         string
	Name       string
	Type       string
	Set        bool
	Text       string // text / short_text value
	OptionName string // drop_down: the selected option's name ("" if unresolved)
}

// RemoteState is what the board currently holds for one task.
type RemoteState struct {
	Description string
	DescSource  DescSource
	Fields      map[string]RemoteField // by custom-field id
}

// Op is one state operation.
type Op string

const (
	OpSet   Op = "SET"
	OpClear Op = "CLEAR"
)

// Change is one custom-field operation or one per-key line of a block change.
type Change struct {
	Key     string  `json:"key"`
	Carrier Carrier `json:"carrier"`
	Op      Op      `json:"op"`
	FieldID string  `json:"field_id,omitempty"`
	Have    string  `json:"have"`
	Want    string  `json:"want"`
}

// BlockOp is the operation on the description block.
type BlockOp string

const (
	BlockNone    BlockOp = ""
	BlockAdd     BlockOp = "ADD"
	BlockRewrite BlockOp = "REWRITE"
	BlockRemove  BlockOp = "REMOVE"
)

// Diff is the per-task state difference.
type Diff struct {
	// Changes lists every drifted key (FIELD ops and the per-key lines of a block op).
	Changes []Change `json:"changes,omitempty"`
	// BlockOp is the single description operation (none when the block is in sync).
	BlockOp BlockOp `json:"block_op,omitempty"`
	// BlockKeys are the keys the block carries (needed to render it).
	BlockKeys []string `json:"block_keys,omitempty"`
	// Notes are non-blocking observations (DUPLICATE_BLOCK, FOREIGN_LINES, UNKNOWN_KEY:x, CLAMPED:k).
	Notes []string `json:"notes,omitempty"`
	// Refusals are reasons the state part of this task cannot be written; the
	// task's state part is then skipped and the run exits non-zero.
	Refusals []string `json:"refusals,omitempty"`
}

// Drift is true when something must be written.
func (d Diff) Drift() bool { return len(d.Changes) > 0 || d.BlockOp != BlockNone }

// Refused is true when the state part cannot be written.
func (d Diff) Refused() bool { return len(d.Refusals) > 0 }

// Options tunes Compute.
type Options struct {
	// MaterializeUnset also renders Unset / empty lines in the block.
	MaterializeUnset bool
}

// Compute is the PURE per-task difference between the wanted Values and the
// remote state under a CarrierPlan. want must come from Normalize (callers
// refuse a task whose Normalize returned Problems before reaching here).
func (s *Spec) Compute(plan CarrierPlan, want Values, rs RemoteState, opt Options) Diff {
	var d Diff
	// FIELD carriers.
	for _, key := range plan.Keys(CarrierField) {
		f, _ := s.Field(key)
		def, _ := plan.Def(key)
		rf := rs.Fields[def.ID]
		w := want[key]
		have := haveFieldValue(f, rf)
		if have == w {
			continue
		}
		op := OpSet
		if s.IsUnset(key, w) {
			op = OpClear
		}
		d.Changes = append(d.Changes, Change{Key: key, Carrier: CarrierField, Op: op, FieldID: def.ID, Have: have, Want: w})
	}
	// BLOCK carrier.
	bkeys := plan.Keys(CarrierBlock)
	d.BlockKeys = bkeys
	if len(bkeys) == 0 {
		return d
	}
	pb := s.ParseBlock(rs.Description)
	if pb.Count > 1 {
		d.Notes = append(d.Notes, "DUPLICATE_BLOCK")
	}
	if pb.Foreign > 0 {
		d.Notes = append(d.Notes, "FOREIGN_LINES")
	}
	for _, u := range pb.Unknown {
		d.Notes = append(d.Notes, "UNKNOWN_KEY:"+u)
	}
	var lineChanges []Change
	wantNonEmpty := false
	for _, key := range bkeys {
		f, _ := s.Field(key)
		w := want[key]
		if !s.IsUnset(key, w) || opt.MaterializeUnset {
			wantNonEmpty = true
		}
		have, present := pb.Values[key]
		if !present {
			if f.Kind == KindEnum {
				have = f.Unset
			} else {
				have = ""
			}
		}
		if f.Kind == KindText {
			if nh, err := NormalizeText(have); err == nil {
				have = nh
			}
		}
		if have == w {
			continue
		}
		op := OpSet
		if s.IsUnset(key, w) {
			op = OpClear
		}
		lineChanges = append(lineChanges, Change{Key: key, Carrier: CarrierBlock, Op: op, Have: have, Want: w})
	}
	inBlock := map[string]bool{}
	for _, k := range bkeys {
		inBlock[k] = true
	}
	stale := false
	for _, f := range s.Fields {
		if _, present := pb.Values[f.Key]; present && !inBlock[f.Key] {
			d.Notes = append(d.Notes, "STALE_KEY:"+f.Key)
			stale = true
		}
	}
	structural := pb.Count > 1 || pb.Foreign > 0 || len(pb.Unknown) > 0 || stale
	if len(lineChanges) == 0 && !structural {
		// A materialising run must also add the block when it is absent.
		if !(opt.MaterializeUnset && pb.Count == 0) {
			return d
		}
	}
	switch {
	case pb.Malformed:
		d.Refusals = append(d.Refusals, "BLOCK_MALFORMED")
		return d
	case rs.DescSource == DescPlain:
		d.Refusals = append(d.Refusals, "DESC_SOURCE_UNAVAILABLE")
		return d
	}
	d.Changes = append(d.Changes, lineChanges...)
	switch {
	case !wantNonEmpty && pb.Count > 0:
		d.BlockOp = BlockRemove
	case !wantNonEmpty:
		d.BlockOp = BlockNone
		d.Changes = nil
	case pb.Count == 0:
		d.BlockOp = BlockAdd
	default:
		d.BlockOp = BlockRewrite
	}
	return d
}

func haveFieldValue(f FieldSpec, rf RemoteField) string {
	if !rf.Set {
		if f.Kind == KindEnum {
			return f.Unset
		}
		return ""
	}
	if f.Kind == KindEnum {
		if rf.OptionName == "" {
			return "?unresolved"
		}
		return strings.TrimSpace(rf.OptionName)
	}
	t, err := NormalizeText(rf.Text)
	if err != nil {
		return rf.Text
	}
	return t
}

// maxShown is the longest value DiffSummary prints; the JSON artifact keeps the
// full value, the human report stays readable.
const maxShown = 60

func shorten(v string) string {
	r := []rune(v)
	if len(r) <= maxShown {
		return v
	}
	return string(r[:maxShown]) + "..."
}

// DiffSummary renders a one-line summary for reports.
func (d Diff) DiffSummary() string {
	var parts []string
	for _, c := range d.Changes {
		parts = append(parts, fmt.Sprintf("%s[%s]: %q -> %q", c.Key, c.Carrier, shorten(c.Have), shorten(c.Want)))
	}
	if d.BlockOp != BlockNone {
		parts = append(parts, "block:"+string(d.BlockOp))
	}
	for _, r := range d.Refusals {
		parts = append(parts, "REFUSED:"+r)
	}
	return strings.Join(parts, "; ")
}
