// states.go — the state-field leg of the reconciler: carrier preparation (custom
// field discovery + guarded creation), the per-task state diff for the plan, and
// the gated write of that diff. Everything here is inert unless a *StatesContext
// is supplied; without one the engine behaves exactly as before.
package syncengine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
	"github.com/vasic-digital/task_bridge/pkg/states"
)

// StatesContext is the per-list state-sync configuration produced by
// PrepareStates.
type StatesContext struct {
	Spec    *states.Spec
	Plan    states.CarrierPlan
	Options states.Options
}

// PrepareOptions bounds PrepareStates.
type PrepareOptions struct {
	// Mode is the carrier policy (auto|block|field).
	Mode states.CarrierMode
	// CreateMissing asks for custom-field creation. It only ever acts together
	// with Apply; a dry-run lists what WOULD be created and calls nothing.
	CreateMissing bool
	// Apply is true for a gated write run.
	Apply bool
	// MaxCreates caps field creations per list (0 = the number of spec fields).
	MaxCreates int
	// MaterializeUnset also renders UNSET / empty lines in the description block.
	MaterializeUnset bool
}

// ProvisionReport records what PrepareStates found and did.
type ProvisionReport struct {
	Mode              states.CarrierMode  `json:"mode"`
	Resolutions       []states.Resolution `json:"resolutions"`
	ToProvision       []states.Provision  `json:"to_provision,omitempty"`
	WouldCreate       []string            `json:"would_create,omitempty"`
	Created           []string            `json:"created,omitempty"`
	CreateUnsupported bool                `json:"create_unsupported,omitempty"`
	CreateErrors      []string            `json:"create_errors,omitempty"`
}

// PrepareStates discovers the list's custom fields, resolves each spec key to a
// carrier, and (only with CreateMissing AND Apply) tries to create the missing
// fields. Creation is guarded three ways: both flags, a per-list cap, and a fresh
// field listing immediately before each create so a concurrent operator action
// can never produce a duplicate-named field. A remote that offers no create
// endpoint is not an error: the missing keys simply stay on the block carrier and
// the report lists the exact fields to provision by hand.
func PrepareStates(ctx context.Context, sc client.StatesClient, listID string, spec *states.Spec, po PrepareOptions) (*StatesContext, ProvisionReport, error) {
	rep := ProvisionReport{Mode: po.Mode}
	var defs []states.FieldDef
	if po.Mode != states.ModeBlock {
		var err error
		defs, err = sc.ListFields(ctx, listID)
		if err != nil {
			return nil, rep, fmt.Errorf("list custom fields of %s: %w", listID, err)
		}
	}
	plan, err := spec.ResolveCarriers(defs, po.Mode)
	if po.CreateMissing && po.Mode != states.ModeBlock {
		var missing []states.Provision
		for _, p := range spec.Provisions(plan) {
			if strings.HasPrefix(p.Reason, string(states.ResMissing)+":") {
				missing = append(missing, p)
			}
		}
		limit := po.MaxCreates
		if limit <= 0 {
			limit = len(spec.Fields)
		}
		if len(missing) > limit {
			missing = missing[:limit]
		}
		if !po.Apply {
			for _, m := range missing {
				rep.WouldCreate = append(rep.WouldCreate, m.Key)
			}
		} else {
			for _, m := range missing {
				fresh, ferr := sc.ListFields(ctx, listID)
				if ferr != nil {
					rep.CreateErrors = append(rep.CreateErrors, m.Key+": relist: "+ferr.Error())
					break
				}
				if hasName(fresh, m.Key) {
					continue // appeared meanwhile: never create a second one
				}
				def := states.FieldDef{Name: m.Key, Type: m.Type}
				for i, o := range m.Options {
					def.Options = append(def.Options, states.OptionDef{Name: o, OrderIndex: i})
				}
				if _, cerr := sc.CreateField(ctx, listID, def); cerr != nil {
					if errors.Is(cerr, client.ErrFieldCreateUnsupported) {
						rep.CreateUnsupported = true
						break
					}
					rep.CreateErrors = append(rep.CreateErrors, m.Key+": "+cerr.Error())
					if client.IsAuth(cerr) {
						break
					}
					continue
				}
				rep.Created = append(rep.Created, m.Key)
			}
			if len(rep.Created) > 0 {
				defs, err = sc.ListFields(ctx, listID)
				if err != nil {
					return nil, rep, fmt.Errorf("re-list custom fields of %s: %w", listID, err)
				}
				plan, err = spec.ResolveCarriers(defs, po.Mode)
			}
		}
	}
	rep.Resolutions = plan.Res
	rep.ToProvision = spec.Provisions(plan)
	if err != nil {
		return nil, rep, err
	}
	return &StatesContext{Spec: spec, Plan: plan, Options: states.Options{MaterializeUnset: po.MaterializeUnset}}, rep, nil
}

func hasName(defs []states.FieldDef, key string) bool {
	n := states.NormalizeName(key)
	for _, d := range defs {
		if states.NormalizeName(d.Name) == n {
			return true
		}
	}
	return false
}

// stateDiffFor computes the state diff of one matched or new task. A local value
// outside the closed vocabulary (or a malformed text value) refuses the task's
// state part: nothing is written for it and the run exits non-zero.
func (sc *StatesContext) stateDiffFor(li mapper.LocalItem, rs states.RemoteState) (d states.Diff, problems []states.Problem) {
	nr := sc.Spec.Normalize(li.States)
	if len(nr.Problems) > 0 {
		for _, p := range nr.Problems {
			d.Refusals = append(d.Refusals, "STATE_INVALID:"+p.Key)
		}
		return d, nr.Problems
	}
	d = sc.Spec.Compute(sc.Plan, nr.Values, rs, sc.Options)
	for _, k := range nr.Clamped {
		d.Notes = append(d.Notes, "CLAMPED:"+k)
	}
	return d, nil
}

// remoteStateOf builds the states view of a remote task.
func remoteStateOf(t client.Task) states.RemoteState {
	return states.RemoteState{Description: t.Description, DescSource: states.DescSource(t.DescriptionSource), Fields: t.Fields}
}

// StateCounts summarises the state part of a plan.
type StateCounts struct {
	Drift     int `json:"drift"`     // tasks with something to write
	Refused   int `json:"refused"`   // tasks whose state part cannot be written
	FieldOps  int `json:"field_ops"` // custom-field set/clear operations
	BlockOps  int `json:"block_ops"` // description block add/rewrite/remove operations
	OnCreate  int `json:"on_create"` // CREATE rows that carry state
	Invalid   int `json:"invalid"`   // tasks refused for an out-of-vocabulary local value
	Malformed int `json:"malformed"` // tasks refused for a malformed remote block
	NoSource  int `json:"no_source"` // tasks refused because only a plain description was returned
}

// StateCounts tallies the plan.
func (p Plan) StateCounts() StateCounts {
	var c StateCounts
	add := func(e PlanEntry, create bool) {
		if e.StateDiff == nil {
			return
		}
		if e.StateDrift {
			if create {
				c.OnCreate++
			} else {
				c.Drift++
			}
		}
		for _, ch := range e.StateDiff.Changes {
			if ch.Carrier == states.CarrierField {
				c.FieldOps++
			}
		}
		if e.StateDiff.BlockOp != states.BlockNone {
			c.BlockOps++
		}
		if e.StateDiff.Refused() {
			c.Refused++
			for _, r := range e.StateDiff.Refusals {
				switch {
				case strings.HasPrefix(r, "STATE_INVALID"):
					c.Invalid++
				case r == "BLOCK_MALFORMED":
					c.Malformed++
				case r == "DESC_SOURCE_UNAVAILABLE":
					c.NoSource++
				}
			}
		}
	}
	for _, e := range p.Update {
		add(e, false)
	}
	for _, e := range p.Create {
		add(e, true)
	}
	return c
}

// StateReportLines renders the per-task state diff for the dry-run report, in
// plan order. At most max lines are returned (0 = all); the second result is the
// number of tasks omitted.
func (p Plan) StateReportLines(max int) ([]string, int) {
	var lines []string
	omitted := 0
	emit := func(prefix string, e PlanEntry) {
		if e.StateDiff == nil || (!e.StateDrift && !e.StateDiff.Refused()) {
			return
		}
		if max > 0 && len(lines) >= max {
			omitted++
			return
		}
		lines = append(lines, fmt.Sprintf("  %-12s %-6s %s", e.Key, prefix, e.StateDiff.DiffSummary()))
	}
	for _, e := range p.Update {
		emit("UPDATE", e)
	}
	for _, e := range p.Create {
		emit("CREATE", e)
	}
	return lines, omitted
}

// applyStateWrites performs the writes of one task's state diff: the custom-field
// sets/clears and, when writeBlock is set, the description block. base is the
// description text outside the block that the rewrite must preserve. It returns
// whether anything was written, the per-operation errors, and whether the remote
// rejected the credentials (the caller then stops the whole batch).
func applyStateWrites(ctx context.Context, scl client.StatesClient, sc *StatesContext, key, taskID string, d states.Diff, vals states.Values, base string, writeBlock bool) (touched bool, errs []string, auth bool) {
	fail := func(what string, err error) {
		errs = append(errs, fmt.Sprintf("STATE %s: %s: %v", key, what, err))
		if client.IsAuth(err) {
			auth = true
		}
	}
	for _, ch := range d.Changes {
		if ch.Carrier != states.CarrierField {
			continue
		}
		if ch.Op == states.OpClear {
			if err := scl.ClearFieldValue(ctx, taskID, ch.FieldID); err != nil {
				fail("clear "+ch.Key, err)
				if auth {
					return touched, errs, auth
				}
				continue
			}
			touched = true
			continue
		}
		f, _ := sc.Spec.Field(ch.Key)
		var value interface{} = ch.Want
		if f.Kind == states.KindEnum {
			def, _ := sc.Plan.Def(ch.Key)
			id, found := states.OptionID(def, ch.Want)
			if !found {
				errs = append(errs, fmt.Sprintf("STATE %s: set %s: no option %q in field %s", key, ch.Key, ch.Want, def.ID))
				continue
			}
			value = id
		}
		if err := scl.SetFieldValue(ctx, taskID, ch.FieldID, value); err != nil {
			fail("set "+ch.Key, err)
			if auth {
				return touched, errs, auth
			}
			continue
		}
		touched = true
	}
	if writeBlock && d.BlockOp != states.BlockNone && !d.Refused() {
		block := ""
		if d.BlockOp != states.BlockRemove {
			block = sc.Spec.RenderBlock(d.BlockKeys, vals, sc.Options.MaterializeUnset)
		}
		if err := scl.SetDescription(ctx, taskID, states.Compose(base, block)); err != nil {
			fail("description block "+string(d.BlockOp), err)
		} else {
			touched = true
		}
	}
	return touched, errs, auth
}

// verifyStates re-reads the task and reports a mismatch when the state diff does
// not converge to empty after a write (the live service rewrote the block, or
// ignored the value). A non-converging write is an ERROR, never silent success.
func verifyStates(ctx context.Context, cl client.Client, sc *StatesContext, li mapper.LocalItem, key, taskID, when string) (errs []string, auth bool) {
	t, err := cl.GetTask(ctx, taskID)
	if err != nil {
		return []string{fmt.Sprintf("STATE %s: read-back: %v", key, err)}, client.IsAuth(err)
	}
	if again, _ := sc.stateDiffFor(li, remoteStateOf(t)); again.Drift() || again.Refused() {
		return []string{fmt.Sprintf("STATE %s: READBACK_MISMATCH after %s: %s", key, when, again.DiffSummary())}, false
	}
	return nil, false
}
