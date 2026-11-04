package states

import (
	"math/rand"
	"strings"
	"testing"
)

func blockPlan(t *testing.T, s *Spec) CarrierPlan {
	p, err := s.ResolveCarriers(nil, ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func want(t *testing.T, s *Spec, kv map[string]string) Values {
	nr := s.Normalize(kv)
	if len(nr.Problems) != 0 {
		t.Fatal(nr.Problems)
	}
	return nr.Values
}

func TestComputeBlockMatrix(t *testing.T) {
	s := mustSpec(t)
	p := blockPlan(t, s)
	set := want(t, s, map[string]string{"reproduction_state": "reproduced-live", "reproduction_evidence": "e1"})
	blk := s.RenderBlock(p.Keys(CarrierBlock), set, false)
	empty := want(t, s, nil)

	cases := []struct {
		name   string
		w      Values
		desc   string
		src    DescSource
		op     BlockOp
		refuse string
		notes  string
	}{
		{"absent->add", set, "human text", DescMarkdown, BlockAdd, "", ""},
		{"empty-desc->add", set, "", DescEmpty, BlockAdd, "", ""},
		{"in-sync", set, Compose("human text", blk), DescMarkdown, BlockNone, "", ""},
		{"in-sync-block-mid-text", set, "a\n\n" + blk + "\n\nb", DescMarkdown, BlockNone, "", ""},
		{"value-differs->rewrite", want(t, s, map[string]string{"reproduction_state": "UNREPRODUCED", "reproduction_evidence": "e1"}), Compose("h", blk), DescMarkdown, BlockRewrite, "", ""},
		{"extra-line-differs->rewrite", want(t, s, map[string]string{"reproduction_state": "reproduced-live"}), Compose("h", blk), DescMarkdown, BlockRewrite, "", ""},
		{"all-unset+block->remove", empty, Compose("h", blk), DescMarkdown, BlockRemove, "", ""},
		{"all-unset+no-block->none", empty, "h", DescMarkdown, BlockNone, "", ""},
		{"all-unset+only-unset-lines->none", empty, "h\n\n[TB-STATES:BEGIN v1]\nreproduction_state: UNSET\n[TB-STATES:END]", DescMarkdown, BlockNone, "", ""},
		{"duplicate->rewrite", set, Compose("h", blk) + "\n\n" + blk, DescMarkdown, BlockRewrite, "", "DUPLICATE_BLOCK"},
		{"foreign-lines->rewrite", set, "h\n\n[TB-STATES:BEGIN v1]\nreproduction_state: reproduced-live\nreproduction_evidence: e1\nstray prose\n[TB-STATES:END]", DescMarkdown, BlockRewrite, "", "FOREIGN_LINES"},
		{"unknown-key->rewrite", set, "h\n\n[TB-STATES:BEGIN v1]\nreproduction_state: reproduced-live\nreproduction_evidence: e1\nmystery: 1\n[TB-STATES:END]", DescMarkdown, BlockRewrite, "", "UNKNOWN_KEY:mystery"},
		{"malformed->refuse", set, "h\n[TB-STATES:BEGIN v1]\nreproduction_state: reproduced-live", DescMarkdown, BlockNone, "BLOCK_MALFORMED", ""},
		{"plain-source+change->refuse", set, "h", DescPlain, BlockNone, "DESC_SOURCE_UNAVAILABLE", ""},
		{"plain-source+in-sync->none", set, Compose("h", blk), DescPlain, BlockNone, "", ""},
		{"remote-bogus-value->rewrite", set, "h\n\n[TB-STATES:BEGIN v1]\nreproduction_state: bogus\nreproduction_evidence: e1\n[TB-STATES:END]", DescMarkdown, BlockRewrite, "", ""},
	}
	for _, c := range cases {
		d := s.Compute(p, c.w, RemoteState{Description: c.desc, DescSource: c.src}, Options{})
		if d.BlockOp != c.op {
			t.Errorf("%s: BlockOp = %q, want %q (%s)", c.name, d.BlockOp, c.op, d.DiffSummary())
		}
		if c.refuse != "" && (len(d.Refusals) != 1 || d.Refusals[0] != c.refuse) {
			t.Errorf("%s: refusals = %v, want [%s]", c.name, d.Refusals, c.refuse)
		}
		if c.refuse == "" && len(d.Refusals) != 0 {
			t.Errorf("%s: unexpected refusals %v", c.name, d.Refusals)
		}
		if c.notes != "" && !strings.Contains(strings.Join(d.Notes, ","), c.notes) {
			t.Errorf("%s: notes = %v, want %s", c.name, d.Notes, c.notes)
		}
		if c.op == BlockNone && c.refuse == "" && d.Drift() {
			t.Errorf("%s: Drift() true for an in-sync task: %s", c.name, d.DiffSummary())
		}
	}
}

func TestComputeMaterializeUnset(t *testing.T) {
	s := mustSpec(t)
	p := blockPlan(t, s)
	empty := want(t, s, nil)
	d := s.Compute(p, empty, RemoteState{Description: "h", DescSource: DescMarkdown}, Options{MaterializeUnset: true})
	if d.BlockOp != BlockAdd {
		t.Fatalf("materialize on an unblocked task: %q", d.BlockOp)
	}
	blk := s.RenderBlock(p.Keys(CarrierBlock), empty, true)
	d = s.Compute(p, empty, RemoteState{Description: Compose("h", blk), DescSource: DescMarkdown}, Options{MaterializeUnset: true})
	if d.Drift() {
		t.Fatalf("materialized block is in sync: %s", d.DiffSummary())
	}
}

func TestComputeStaleKeyAfterBlockToFieldMigration(t *testing.T) {
	s := mustSpec(t)
	// Only reproduction_state is a FIELD now; the old block still has it.
	defs := []FieldDef{ddDef("cf-rs", "reproduction_state", s.Fields[0].NonUnsetValues()...)}
	p, err := s.ResolveCarriers(defs, ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	old := "h\n\n[TB-STATES:BEGIN v1]\nreproduction_state: reproduced-live\n[TB-STATES:END]"
	w := want(t, s, map[string]string{"reproduction_state": "reproduced-live"})
	rs := RemoteState{Description: old, DescSource: DescMarkdown, Fields: map[string]RemoteField{"cf-rs": {ID: "cf-rs", Set: true, OptionName: "reproduced-live"}}}
	d := s.Compute(p, w, rs, Options{})
	if d.BlockOp != BlockRemove || !strings.Contains(strings.Join(d.Notes, ","), "STALE_KEY:reproduction_state") {
		t.Fatalf("stale block lines not removed: %s notes=%v", d.DiffSummary(), d.Notes)
	}
}

func TestComputeFieldMatrix(t *testing.T) {
	s := mustSpec(t)
	p, _ := s.ResolveCarriers(fullDefs(s), ModeAuto)
	w := want(t, s, map[string]string{"reproduction_state": "reproduced-live", "reproduction_env": "bench"})
	rs := RemoteState{DescSource: DescEmpty, Fields: map[string]RemoteField{}}
	d := s.Compute(p, w, rs, Options{})
	if len(d.Changes) != 2 || d.BlockOp != BlockNone {
		t.Fatalf("fresh task: %s", d.DiffSummary())
	}
	if d.Changes[0].Key != "reproduction_state" || d.Changes[0].Op != OpSet || d.Changes[0].FieldID != "cf-reproduction_state" || d.Changes[0].Want != "reproduced-live" {
		t.Fatalf("change 0 = %+v", d.Changes[0])
	}
	rs.Fields["cf-reproduction_state"] = RemoteField{ID: "cf-reproduction_state", Set: true, OptionName: "reproduced-live"}
	rs.Fields["cf-reproduction_env"] = RemoteField{ID: "cf-reproduction_env", Set: true, Text: "bench\r\n"}
	if d = s.Compute(p, w, rs, Options{}); d.Drift() {
		t.Fatalf("in-sync fields drift: %s", d.DiffSummary())
	}
	// Wrong value -> SET; cleared want -> CLEAR; option named UNSET equals unset; stale text -> CLEAR.
	rs.Fields["cf-reproduction_state"] = RemoteField{ID: "cf-reproduction_state", Set: true, OptionName: "UNREPRODUCED"}
	d = s.Compute(p, w, rs, Options{})
	if len(d.Changes) != 1 || d.Changes[0].Op != OpSet || d.Changes[0].Have != "UNREPRODUCED" {
		t.Fatalf("wrong value: %s", d.DiffSummary())
	}
	w2 := want(t, s, nil)
	d = s.Compute(p, w2, rs, Options{})
	if len(d.Changes) != 2 || d.Changes[0].Op != OpClear || d.Changes[1].Op != OpClear {
		t.Fatalf("clear: %s", d.DiffSummary())
	}
	rs.Fields["cf-reproduction_state"] = RemoteField{ID: "cf-reproduction_state", Set: true, OptionName: "UNSET"}
	rs.Fields["cf-reproduction_env"] = RemoteField{ID: "cf-reproduction_env", Set: false}
	if d = s.Compute(p, w2, rs, Options{}); d.Drift() {
		t.Fatalf("UNSET option / unset text must equal unset: %s", d.DiffSummary())
	}
	rs.Fields["cf-reproduction_state"] = RemoteField{ID: "cf-reproduction_state", Set: true, OptionName: ""}
	d = s.Compute(p, w2, rs, Options{})
	if len(d.Changes) != 1 || d.Changes[0].Have != "?unresolved" || d.Changes[0].Op != OpClear {
		t.Fatalf("unresolved option must be cleared: %s", d.DiffSummary())
	}
}

// simulateApply performs the diff on a RemoteState exactly as the engine would
// (set/clear fields, compose the block) so the property tests can assert
// "apply twice = zero diff" without any I/O.
func simulateApply(s *Spec, p CarrierPlan, w Values, rs RemoteState, d Diff, opt Options, ch func(string) string) RemoteState {
	out := RemoteState{Description: rs.Description, DescSource: rs.DescSource, Fields: map[string]RemoteField{}}
	for k, v := range rs.Fields {
		out.Fields[k] = v
	}
	for _, c := range d.Changes {
		if c.Carrier != CarrierField {
			continue
		}
		if c.Op == OpClear {
			out.Fields[c.FieldID] = RemoteField{ID: c.FieldID}
			continue
		}
		f, _ := s.Field(c.Key)
		rf := RemoteField{ID: c.FieldID, Set: true}
		if f.Kind == KindEnum {
			rf.OptionName = c.Want
		} else {
			rf.Text = c.Want
		}
		out.Fields[c.FieldID] = rf
	}
	if d.BlockOp != BlockNone {
		pb := s.ParseBlock(rs.Description)
		blk := ""
		if d.BlockOp != BlockRemove {
			blk = s.RenderBlock(d.BlockKeys, w, opt.MaterializeUnset)
		}
		out.Description = ch(Compose(pb.Base, blk))
		if out.Description == "" {
			out.DescSource = DescEmpty
		} else {
			out.DescSource = DescMarkdown
		}
	}
	return out
}

// TestPropApplyTwiceIsZeroDiff: for random wanted values, random pre-existing
// remote state, every carrier mix and every channel, one application brings the
// task to a state with an EMPTY diff, and a second application changes nothing.
func TestPropApplyTwiceIsZeroDiff(t *testing.T) {
	s := mustSpec(t)
	r := rand.New(rand.NewSource(4242))
	chans := []func(string) string{func(x string) string { return x }, mdChannel, sloppyChannel}
	plans := map[string]CarrierPlan{}
	plans["block"] = blockPlan(t, s)
	plans["field"], _ = s.ResolveCarriers(fullDefs(s), ModeAuto)
	mixed := fullDefs(s)[:3]
	plans["mixed"], _ = s.ResolveCarriers(mixed, ModeAuto)
	rsVals := []string{"UNSET", "UNREPRODUCED", "reproduced-live", "reproduced-host-fixture", "not-reproducible-with-evidence", "blocked-no-device"}
	isVals := []string{"UNSET", "not-started", "in-progress", "root-cause-found-with-evidence", "root-cause-unconfirmed", "closed-no-defect"}
	for name, p := range plans {
		for i := 0; i < 600; i++ {
			ch := chans[r.Intn(len(chans))]
			opt := Options{MaterializeUnset: r.Intn(4) == 0}
			raw := map[string]string{
				"reproduction_state": rsVals[r.Intn(6)], "investigation_state": isVals[r.Intn(6)],
				"reproduction_evidence": randString(r, 25), "reproduction_red_cmd": randString(r, 25),
				"reproduction_green_cmd": randString(r, 25), "reproduction_env": randString(r, 15),
				"investigation_root_cause": randString(r, 25), "investigation_evidence": randString(r, 25),
			}
			nr := s.Normalize(raw)
			if len(nr.Problems) != 0 {
				continue
			}
			base := strings.ReplaceAll(randString(r, 40), "[", "(")
			rs := RemoteState{Description: base, DescSource: DescMarkdown, Fields: map[string]RemoteField{}}
			if base == "" {
				rs.DescSource = DescEmpty
			}
			// Pre-existing stale remote state half of the time.
			if r.Intn(2) == 0 {
				old := want(t, s, map[string]string{"reproduction_state": rsVals[r.Intn(6)], "reproduction_evidence": randString(r, 10)})
				stale := s.Compute(p, old, rs, Options{})
				rs = simulateApply(s, p, old, rs, stale, Options{}, ch)
			}
			d1 := s.Compute(p, nr.Values, rs, opt)
			if d1.Refused() {
				t.Fatalf("%s: unexpected refusal %v", name, d1.Refusals)
			}
			rs1 := simulateApply(s, p, nr.Values, rs, d1, opt, ch)
			d2 := s.Compute(p, nr.Values, rs1, opt)
			if d2.Drift() {
				t.Fatalf("%s: apply once left a diff: %s\ndesc=%q", name, d2.DiffSummary(), rs1.Description)
			}
			rs2 := simulateApply(s, p, nr.Values, rs1, d2, opt, ch)
			if rs2.Description != rs1.Description || len(rs2.Fields) != len(rs1.Fields) {
				t.Fatalf("%s: apply twice changed the remote state", name)
			}
		}
	}
}

func TestDiffSummaryShortensLongValues(t *testing.T) {
	d := Diff{Changes: []Change{{Key: "reproduction_evidence", Carrier: CarrierBlock, Op: OpSet, Have: "", Want: strings.Repeat("x", 500)}}}
	got := d.DiffSummary()
	if len(got) > 200 || !strings.Contains(got, "...") {
		t.Fatalf("summary not shortened (%d bytes): %s", len(got), got)
	}
	if full := d.Changes[0].Want; len(full) != 500 {
		t.Fatal("the change itself must keep the full value")
	}
}
