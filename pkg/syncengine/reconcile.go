// reconcile.go — the minimum-viable, title-prefix DIFF reconciler (P5).
//
// PlanReconcile is a PURE function (no I/O, no writes) that matches local
// §11.4.93 items to remote ClickUp tasks by the `[XXX-NNN]` title-prefix key
// (mapper.ParseKey) and buckets the result. Apply is the GATED push: it is the
// ONLY path that writes to the remote, and it NEVER deletes (§9/§11.4.122 —
// ClickUp-only tasks are INVESTIGATE, never DELETE).
package syncengine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
	"github.com/vasic-digital/task_bridge/pkg/states"
)

// ActionKind is the reconcile bucket a row lands in.
type ActionKind string

const (
	// ActionUpdate: a local item matched to a remote task (both exist).
	ActionUpdate ActionKind = "UPDATE"
	// ActionCreate: a local item with no remote task (push would create it).
	ActionCreate ActionKind = "CREATE"
	// ActionInvestigate: a remote task WITH a recognized `[XXX-NNN]` key that
	// matches NO local item (a keyed remote orphan). NEVER deleted — the
	// operator decides (§9/§11.4.122).
	ActionInvestigate ActionKind = "INVESTIGATE"
	// ActionDelete: reserved. In DIFF mode + the never-auto-delete-remote
	// default this bucket is ALWAYS empty — the engine issues no remote DELETE.
	ActionDelete ActionKind = "DELETE"
	// ActionUnkeyed: a remote task whose title carries NO recognized key (e.g.
	// an `[ATM-DERIVED-*]` task). Distinct from INVESTIGATE: it has no key to
	// match at all. Needs a separate operator decision; never deleted.
	ActionUnkeyed ActionKind = "UNKEYED"
)

// PlanEntry is one row of the reconcile plan (clean, JSON-serialisable — the
// captured-evidence artefact, §11.4.69).
type PlanEntry struct {
	Kind         ActionKind `json:"kind"`
	Key          string     `json:"key,omitempty"`           // item key ("" for UNKEYED)
	TaskID       string     `json:"task_id,omitempty"`       // remote id ("" for CREATE)
	LocalStatus  string     `json:"local_status,omitempty"`  // DB status
	RemoteStatus string     `json:"remote_status,omitempty"` // ClickUp status name
	RemoteName   string     `json:"remote_name,omitempty"`   // ClickUp task title
	// InSync is the TYPED, load-bearing drift flag for UPDATE rows: true when the
	// remote already matches the local BOTH in grouped column AND in the exact
	// status:<word> label. Apply and the CLI report key their skip/push decision
	// off THIS field, never off the human-readable Detail string (N3 — no fragile
	// string coupling). Only ever set on UPDATE rows; false (and omitted) for
	// every other bucket.
	InSync bool `json:"in_sync,omitempty"`
	// TitleDrift is true on an UPDATE row when the remote task name differs from
	// the name the mapper would push for the local item (mapper.RemoteTitle).
	// A title-only drift is repaired with a NAME-ONLY update (no description,
	// status or tag overwrite). ATM-1123.
	TitleDrift bool `json:"title_drift,omitempty"`
	// RemoteTags is the matched remote task's current tag set (UPDATE rows only).
	// Apply uses it to reconcile the status:<word> label without an extra GET:
	// add the wanted label if absent, remove a SUPERSEDED status label if present.
	// Type/other tags are never touched (mapper.IsStatusLabel gates removal).
	RemoteTags []string `json:"remote_tags,omitempty"`
	Detail     string   `json:"detail,omitempty"` // human reason (display only)
	// StateDiff is the difference of the consumer-defined state fields (nil when
	// the feature is off). StateDrift is the typed flag Apply keys off: something
	// must be written. A StateDiff with Refusals is reported and makes the run
	// exit non-zero, but writes nothing for the task's state part.
	StateDiff  *states.Diff `json:"state_diff,omitempty"`
	StateDrift bool         `json:"state_drift,omitempty"`
	// RemoteDescription is the matched task's description text (Markdown source
	// when RemoteDescSource says so). It is held in memory for the block rewrite
	// and never serialised into the artifact.
	RemoteDescription string                        `json:"-"`
	RemoteFields      map[string]states.RemoteField `json:"-"`
}

// Plan is the bucketed reconcile diff.
type Plan struct {
	Update      []PlanEntry `json:"update"`
	Create      []PlanEntry `json:"create"`
	Investigate []PlanEntry `json:"investigate"`
	Delete      []PlanEntry `json:"delete"`
	Unkeyed     []PlanEntry `json:"unkeyed"`
}

// Counts returns the per-bucket sizes (the load-bearing dry-run summary).
func (p Plan) Counts() map[string]int {
	return map[string]int{
		string(ActionUpdate):      len(p.Update),
		string(ActionCreate):      len(p.Create),
		string(ActionInvestigate): len(p.Investigate),
		string(ActionDelete):      len(p.Delete),
		string(ActionUnkeyed):     len(p.Unkeyed),
	}
}

// indexLocal builds a key -> LocalItem map. If the same key appears twice (a
// tracker item legitimately present in BOTH Issues and Fixed, §11.4.93), the
// most-recently-modified row wins (no silent drop). On an EXACT last_modified
// tie the winner is chosen by a deterministic secondary key (itemTieKey) so the
// result is identical regardless of SQL row / slice order (N5 / §11.4.50).
func indexLocal(local []mapper.LocalItem) map[string]mapper.LocalItem {
	m := make(map[string]mapper.LocalItem, len(local))
	for _, li := range local {
		if li.Key == "" {
			continue
		}
		if prev, ok := m[li.Key]; ok {
			// Primary: keep the most-recently-modified row.
			if prev.LastModified > li.LastModified {
				continue
			}
			// Secondary deterministic tie-break on an EXACT last_modified equality
			// (N5 / §11.4.50): keep the row with the lexicographically-greater
			// canonical tuple, so the winner never depends on input row order.
			if prev.LastModified == li.LastModified && itemTieKey(prev) >= itemTieKey(li) {
				continue
			}
		}
		m[li.Key] = li
	}
	return m
}

// itemTieKey is the deterministic, order-independent secondary sort key used to
// break an EXACT last_modified tie in indexLocal. It is a canonical join of the
// row's stable fields (NUL-separated so field boundaries can't collide). Two
// rows with the same key AND same last_modified AND same tuple are genuinely
// identical, so which one "wins" is immaterial — the point is that the choice is
// deterministic across runs and across SQL row orderings.
func itemTieKey(li mapper.LocalItem) string {
	return li.Location + "\x00" + li.Status + "\x00" + li.Title + "\x00" + li.Description
}

// PlanOptions carries the optional state-field context of a plan.
type PlanOptions struct {
	// States, when non-nil, adds the state-field diff to every matched and every
	// new task. Nil = the original plan, byte for byte.
	States *StatesContext
}

// PlanReconcile computes the DIFF (no writes). Matching is by the `[XXX-NNN]`
// title-prefix key. Deterministic: every bucket is sorted (§11.4.50).
func PlanReconcile(local []mapper.LocalItem, remote []client.Task) Plan {
	return PlanReconcileWith(local, remote, PlanOptions{})
}

// PlanReconcileWith is PlanReconcile plus the optional state-field diff.
func PlanReconcileWith(local []mapper.LocalItem, remote []client.Task, po PlanOptions) Plan {
	byKey := indexLocal(local)
	matched := make(map[string]bool, len(byKey))
	var plan Plan

	for _, t := range remote {
		key, ok := mapper.ParseKey(t.Name)
		if !ok {
			plan.Unkeyed = append(plan.Unkeyed, PlanEntry{
				Kind:         ActionUnkeyed,
				TaskID:       t.ID,
				RemoteStatus: t.Status,
				RemoteName:   t.Name,
				Detail:       "remote task has no [XXX-NNN] key — operator decision",
			})
			continue
		}
		li, found := byKey[key]
		if !found {
			plan.Investigate = append(plan.Investigate, PlanEntry{
				Kind:         ActionInvestigate,
				Key:          key,
				TaskID:       t.ID,
				RemoteStatus: t.Status,
				RemoteName:   t.Name,
				Detail:       "keyed remote task with no local item — never deleted, operator decision",
			})
			continue
		}
		matched[key] = true
		e := PlanEntry{
			Kind:         ActionUpdate,
			Key:          key,
			TaskID:       t.ID,
			LocalStatus:  li.Status,
			RemoteStatus: t.Status,
			RemoteName:   t.Name,
			RemoteTags:   t.Tags,
		}
		colMatch := mapper.RemoteStatusMatchesLocal(t.Status, li.Status)
		wantLbl, hasLbl := mapper.StatusLabel(li.Status)
		lblMatch := !hasLbl || remoteHasTag(t.Tags, wantLbl)
		wantTitle := mapper.RemoteTitle(li)
		e.TitleDrift = strings.TrimSpace(t.Name) != strings.TrimSpace(wantTitle)
		switch {
		case colMatch && lblMatch && !e.TitleDrift:
			e.InSync = true
			e.Detail = "status in-sync (column + label)"
		case colMatch && lblMatch && e.TitleDrift:
			e.Detail = fmt.Sprintf("title-drift: remote=%q want=%q", t.Name, wantTitle)
		case !colMatch:
			if want, okm := mapper.StatusColumn(li.Status); okm {
				e.Detail = fmt.Sprintf("status-drift: remote=%q want-column=%q label=%q", t.Status, want, wantLbl)
			} else {
				e.Detail = fmt.Sprintf("local status %q not in vocab", li.Status)
			}
		default: // column matches but the status label is missing/stale
			e.Detail = fmt.Sprintf("label-drift: column ok, missing status label %q", wantLbl)
		}
		if e.TitleDrift && !e.InSync && !(colMatch && lblMatch) {
			e.Detail += "; title-drift"
		}
		if po.States != nil {
			d, _ := po.States.stateDiffFor(li, remoteStateOf(t))
			e.StateDiff = &d
			e.StateDrift = d.Drift()
			e.RemoteDescription = t.Description
			e.RemoteFields = t.Fields
			if e.StateDrift {
				if e.InSync {
					e.InSync = false
					e.Detail = "state-drift: " + d.DiffSummary()
				} else {
					e.Detail += "; state-drift: " + d.DiffSummary()
				}
			} else if d.Refused() {
				e.Detail += "; state-refused: " + d.DiffSummary()
			}
		}
		plan.Update = append(plan.Update, e)
	}

	// Local items with no matching remote task -> CREATE.
	for _, li := range byKey {
		if matched[li.Key] {
			continue
		}
		ce := PlanEntry{
			Kind:        ActionCreate,
			Key:         li.Key,
			LocalStatus: li.Status,
			Detail:      "local item with no remote task — push would create",
		}
		if po.States != nil {
			d, _ := po.States.stateDiffFor(li, states.RemoteState{DescSource: states.DescEmpty})
			ce.StateDiff = &d
			ce.StateDrift = d.Drift()
			if ce.StateDrift {
				ce.Detail += "; state: " + d.DiffSummary()
			}
		}
		plan.Create = append(plan.Create, ce)
	}

	sortEntries(plan.Update)
	sortEntries(plan.Create)
	sortEntries(plan.Investigate)
	sortEntries(plan.Unkeyed)
	return plan
}

// remoteHasTag reports whether tags contains want (case-insensitive; ClickUp
// lowercases tag names, so a case-blind compare is required).
func remoteHasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func sortEntries(es []PlanEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Key != es[j].Key {
			return es[i].Key < es[j].Key
		}
		return es[i].TaskID < es[j].TaskID
	})
}

// ApplyResult reports what a gated push actually did.
type ApplyResult struct {
	Created int
	Updated int
	Errors  []string
	// CreatedIDs / UpdatedIDs map item key -> ClickUp task id for every write
	// that succeeded (the audit trail of a bounded batch, ATM-1123).
	CreatedIDs map[string]string
	UpdatedIDs map[string]string
	// Attempted is the number of actionable items processed in this call;
	// Remaining the number that were left for a later batch (Limit / StopOnError).
	Attempted int
	Remaining int
	// Stopped is true when StopOnError halted the batch on the first failing item.
	Stopped bool
	// AuthRejected is true when the batch stopped because the remote rejected the
	// credentials (HTTP 401/403).
	AuthRejected bool
}

// ApplyOptions bounds a gated push. The zero value reproduces the original
// Apply behaviour (unbounded, collect-and-continue, legacy order).
type ApplyOptions struct {
	// Limit caps the number of ACTIONABLE items (CREATE + drifted UPDATE) touched
	// in one call; 0 = unlimited. The rest stay in Remaining for the next batch.
	Limit int
	// StopOnError halts the batch after the first item that produced an error.
	StopOnError bool
	// NewestFirst orders actionable items by descending numeric key suffix
	// (ATM-1325 before ATM-1244) so a bounded batch handles the newest first.
	NewestFirst bool
	// OnEvent, when set, is called once per item with its outcome (audit log).
	OnEvent func(ApplyEvent)
	// States, when non-nil, applies the state-field diff of every item. The
	// Client must implement client.StatesClient or ApplyWith refuses to start.
	States *StatesContext
	// StatesOnly restricts a gated push to the state fields of EXISTING tasks:
	// no CREATE, and no status / title / label / name update. It is the staged
	// roll-out switch.
	StatesOnly bool
	// VerifyReadback re-reads each task after a state write and fails the item
	// when the diff does not converge to empty.
	VerifyReadback bool
}

// ApplyEvent is one per-item outcome, handed to ApplyOptions.OnEvent. It never
// carries credentials.
type ApplyEvent struct {
	Op     string `json:"op"` // CREATE | UPDATE
	Key    string `json:"key"`
	TaskID string `json:"task_id,omitempty"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Err    string `json:"err,omitempty"`
}

// Apply performs the GATED push with the original unbounded semantics. See
// ApplyWith for the bounded variant.
func Apply(ctx context.Context, cl client.Client, mp mapper.Mapper, listID string, plan Plan, local []mapper.LocalItem) (ApplyResult, error) {
	return ApplyWith(ctx, cl, mp, listID, plan, local, ApplyOptions{})
}

type applyWork struct {
	kind  ActionKind
	entry PlanEntry
}

// keyNum returns the numeric suffix of a PREFIX-NNN key (0 when absent).
func keyNum(key string) int {
	i := strings.LastIndex(key, "-")
	if i < 0 {
		return 0
	}
	n := 0
	for _, r := range key[i+1:] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// ApplyWith performs the GATED push. It is reached ONLY under an explicit apply
// request (never in a dry-run). It creates missing tasks and updates drifted
// tasks (status column, status label, title). It NEVER touches INVESTIGATE /
// UNKEYED / DELETE rows — those are operator decisions and no remote data is
// ever destroyed (§9/§11.4.122). listID is where new tasks are created. opts
// bounds the batch (Limit / StopOnError / NewestFirst) and exposes an audit hook.
func ApplyWith(ctx context.Context, cl client.Client, mp mapper.Mapper, listID string, plan Plan, local []mapper.LocalItem, opts ApplyOptions) (ApplyResult, error) {
	byKey := indexLocal(local)
	res := ApplyResult{CreatedIDs: map[string]string{}, UpdatedIDs: map[string]string{}}

	var scl client.StatesClient
	if opts.States != nil {
		var ok bool
		if scl, ok = cl.(client.StatesClient); !ok {
			return res, fmt.Errorf("apply: states configured but the client does not implement client.StatesClient")
		}
	}
	if opts.StatesOnly && opts.States == nil {
		return res, fmt.Errorf("apply: StatesOnly without a states context")
	}
	var work []applyWork
	if !opts.StatesOnly {
		for _, e := range plan.Create {
			work = append(work, applyWork{ActionCreate, e})
		}
	}
	for _, e := range plan.Update {
		// Skip only when status column, status label AND title already match
		// (InSync). Keyed off the TYPED InSync flag, never the human-readable
		// Detail string (N3). Under StatesOnly only a state drift is actionable.
		if opts.StatesOnly {
			if !e.StateDrift {
				continue
			}
		} else if e.InSync {
			continue
		}
		work = append(work, applyWork{ActionUpdate, e})
	}
	if opts.NewestFirst {
		sort.SliceStable(work, func(i, j int) bool {
			ni, nj := keyNum(work[i].entry.Key), keyNum(work[j].entry.Key)
			if ni != nj {
				return ni > nj
			}
			return work[i].entry.Key > work[j].entry.Key
		})
	}
	total := len(work)

	emit := func(ev ApplyEvent) {
		if opts.OnEvent != nil {
			opts.OnEvent(ev)
		}
	}

	for idx, w := range work {
		if opts.Limit > 0 && res.Attempted >= opts.Limit {
			break
		}
		res.Attempted++
		e := w.entry
		li, ok := byKey[e.Key]
		var errs []string
		authStop := false
		switch w.kind {
		case ActionCreate:
			if !ok {
				errs = append(errs, fmt.Sprintf("CREATE %s: local item vanished", e.Key))
				break
			}
			rt, err := mp.ToRemote(li)
			if err != nil {
				errs = append(errs, fmt.Sprintf("CREATE %s: map: %v", e.Key, err))
				break
			}
			createDesc := rt.Description
			var cd states.Diff
			var cvals states.Values
			if opts.States != nil {
				var probs []states.Problem
				cd, probs = opts.States.stateDiffFor(li, states.RemoteState{DescSource: states.DescEmpty})
				if len(probs) == 0 {
					cvals = opts.States.Spec.Normalize(li.States).Values
					if cd.BlockOp == states.BlockAdd {
						createDesc = states.Compose(rt.Description, opts.States.Spec.RenderBlock(cd.BlockKeys, cvals, opts.States.Options.MaterializeUnset))
					}
				}
			}
			created, err := cl.CreateTask(ctx, listID, client.Task{
				Name: rt.Name, Description: createDesc, Status: rt.Status, Tags: rt.Tags,
			})
			if err != nil {
				errs = append(errs, fmt.Sprintf("CREATE %s: %v", e.Key, err))
				if client.IsAuth(err) {
					authStop = true
				}
				break
			}
			res.Created++
			res.CreatedIDs[e.Key] = created.ID
			emit(ApplyEvent{Op: "CREATE", Key: e.Key, TaskID: created.ID, OK: true, Detail: "status=" + rt.Status})
			if opts.States != nil && cd.Drift() && !cd.Refused() {
				_, serrs, sauth := applyStateWrites(ctx, scl, opts.States, e.Key, created.ID, cd, cvals, "", false)
				errs = append(errs, serrs...)
				authStop = authStop || sauth
				if len(serrs) == 0 && opts.VerifyReadback {
					verrs, vauth := verifyStates(ctx, cl, opts.States, li, e.Key, created.ID, "create")
					errs = append(errs, verrs...)
					authStop = authStop || vauth
				}
			}
		case ActionUpdate:
			if !ok {
				break
			}
			rt, err := mp.ToRemote(li)
			if err != nil && !opts.StatesOnly {
				errs = append(errs, fmt.Sprintf("UPDATE %s: map: %v", e.Key, err))
				break
			}
			touched := false
			// descBase is set when the full update below overwrote the remote
			// description with the local one: the managed block is then gone and
			// must be re-added on top of exactly that text.
			var descBase *string
			colDrift := !mapper.RemoteStatusMatchesLocal(e.RemoteStatus, li.Status) && !opts.StatesOnly
			// (1) column: push only when the grouped column drifted (the update
			// body DOES honour `status`, verified live 2026-07-27). This full
			// update also carries the corrected name.
			if colDrift {
				if err := cl.UpdateTask(ctx, client.Task{
					ID: e.TaskID, Name: rt.Name, Description: rt.Description, Status: rt.Status, Tags: rt.Tags,
				}); err != nil {
					errs = append(errs, fmt.Sprintf("UPDATE %s: %v", e.Key, err))
					authStop = authStop || client.IsAuth(err)
					break
				}
				touched = true
				if rt.Description != "" {
					base := rt.Description
					descBase = &base
				}
			} else if e.TitleDrift && !opts.StatesOnly {
				// (1b) title-only drift: NAME-ONLY update (empty description /
				// status / tags are omitted from the request body).
				if err := cl.UpdateTask(ctx, client.Task{ID: e.TaskID, Name: mapper.RemoteTitle(li)}); err != nil {
					errs = append(errs, fmt.Sprintf("UPDATE %s: title: %v", e.Key, err))
					authStop = authStop || client.IsAuth(err)
					break
				}
				touched = true
			}
			// (2) status label: ClickUp's update body IGNORES tags (verified live
			// 2026-07-27), so the exact status:<word> label MUST be reconciled
			// through the dedicated add/remove-tag endpoints. Remove any
			// SUPERSEDED status label, then add the wanted one if absent.
			// Type/other tags are never touched (mapper.IsStatusLabel gates
			// removal).
			if wantLbl, hasLbl := mapper.StatusLabel(li.Status); hasLbl && !opts.StatesOnly {
				failed := false
				for _, tg := range e.RemoteTags {
					if mapper.IsStatusLabel(tg) && !strings.EqualFold(strings.TrimSpace(tg), wantLbl) {
						if err := cl.RemoveTag(ctx, e.TaskID, tg); err != nil {
							errs = append(errs, fmt.Sprintf("UPDATE %s: remove stale label %q: %v", e.Key, tg, err))
							authStop = authStop || client.IsAuth(err)
							failed = true
							continue
						}
						touched = true
					}
				}
				if !failed && !remoteHasTag(e.RemoteTags, wantLbl) {
					if err := cl.AddTag(ctx, e.TaskID, wantLbl); err != nil {
						errs = append(errs, fmt.Sprintf("UPDATE %s: add label %q: %v", e.Key, wantLbl, err))
						authStop = authStop || client.IsAuth(err)
					} else {
						touched = true
					}
				}
			}
			// (3) state fields: custom-field sets/clears and the managed description
			// block, written AFTER the steps above so a description overwrite in (1)
			// can be repaired in the same pass.
			if opts.States != nil && e.StateDiff != nil && !authStop {
				sd := *e.StateDiff
				base := states.Compose(opts.States.Spec.ParseBlock(e.RemoteDescription).Base, "")
				if descBase != nil {
					base = states.Compose(*descBase, "")
					src := states.DescMarkdown
					if *descBase == "" {
						src = states.DescEmpty
					}
					sd, _ = opts.States.stateDiffFor(li, states.RemoteState{Description: *descBase, DescSource: src, Fields: e.RemoteFields})
				}
				if sd.Drift() {
					vals := opts.States.Spec.Normalize(li.States).Values
					wrote, serrs, sauth := applyStateWrites(ctx, scl, opts.States, e.Key, e.TaskID, sd, vals, base, true)
					errs = append(errs, serrs...)
					authStop = authStop || sauth
					touched = touched || wrote
					if wrote && len(serrs) == 0 && opts.VerifyReadback {
						verrs, vauth := verifyStates(ctx, cl, opts.States, li, e.Key, e.TaskID, "update")
						errs = append(errs, verrs...)
						authStop = authStop || vauth
					}
				}
			}
			if touched {
				res.Updated++
				res.UpdatedIDs[e.Key] = e.TaskID
				emit(ApplyEvent{Op: "UPDATE", Key: e.Key, TaskID: e.TaskID, OK: len(errs) == 0, Detail: e.Detail})
			}
		}
		for _, m := range errs {
			res.Errors = append(res.Errors, m)
			emit(ApplyEvent{Op: string(w.kind), Key: e.Key, TaskID: e.TaskID, OK: false, Err: m})
		}
		if authStop {
			res.AuthRejected = true
		}
		if len(errs) > 0 && (opts.StopOnError || authStop) {
			// A credentials rejection (401/403) stops the batch even without
			// StopOnError: no later item can succeed, and retrying each one only
			// burns the rate limit.
			res.Stopped = true
			res.Remaining = total - (idx + 1)
			return res, nil
		}
	}
	res.Remaining = total - res.Attempted
	return res, nil
}

// FetchAllRemote pages through the entire list via the injected client and
// returns every task (full pull, sinceMS=0). It caps pages defensively so a
// mis-behaving API can never spin forever.
func FetchAllRemote(ctx context.Context, cl client.Client, listID string) ([]client.Task, error) {
	const maxPages = 1000
	var all []client.Task
	for page := 0; page < maxPages; page++ {
		tasks, hasMore, err := cl.ListTasks(ctx, listID, page, 0)
		if err != nil {
			return all, fmt.Errorf("list page %d: %w", page, err)
		}
		all = append(all, tasks...)
		if !hasMore {
			return all, nil
		}
	}
	return all, fmt.Errorf("exceeded %d pages — aborting", maxPages)
}
