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

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
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
	// remote status already equals the local status. Apply and the CLI report key
	// their skip/push decision off THIS field, never off the human-readable
	// Detail string (N3 — no fragile string coupling). Only ever set on UPDATE
	// rows; false (and omitted) for every other bucket.
	InSync bool   `json:"in_sync,omitempty"`
	Detail string `json:"detail,omitempty"` // human reason (display only)
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

// PlanReconcile computes the DIFF (no writes). Matching is by the `[XXX-NNN]`
// title-prefix key. Deterministic: every bucket is sorted (§11.4.50).
func PlanReconcile(local []mapper.LocalItem, remote []client.Task) Plan {
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
		}
		if mapper.RemoteStatusMatchesLocal(t.Status, li.Status) {
			e.InSync = true
			e.Detail = "status in-sync"
		} else if want, okm := mapper.StatusToRemote(li.Status); okm {
			e.Detail = fmt.Sprintf("status-drift: remote=%q want=%q", t.Status, want)
		} else {
			e.Detail = fmt.Sprintf("local status %q not in vocab", li.Status)
		}
		plan.Update = append(plan.Update, e)
	}

	// Local items with no matching remote task -> CREATE.
	for _, li := range byKey {
		if matched[li.Key] {
			continue
		}
		plan.Create = append(plan.Create, PlanEntry{
			Kind:        ActionCreate,
			Key:         li.Key,
			LocalStatus: li.Status,
			Detail:      "local item with no remote task — push would create",
		})
	}

	sortEntries(plan.Update)
	sortEntries(plan.Create)
	sortEntries(plan.Investigate)
	sortEntries(plan.Unkeyed)
	return plan
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
}

// Apply performs the GATED push. It is reached ONLY under an explicit apply
// request (never in a dry-run). It creates missing tasks and updates
// status-drifted tasks. It NEVER touches INVESTIGATE / UNKEYED / DELETE rows —
// those are operator decisions and no remote data is ever destroyed
// (§9/§11.4.122). listID is where new tasks are created.
func Apply(ctx context.Context, cl client.Client, mp mapper.Mapper, listID string, plan Plan, local []mapper.LocalItem) (ApplyResult, error) {
	byKey := indexLocal(local)
	var res ApplyResult

	for _, e := range plan.Create {
		li, ok := byKey[e.Key]
		if !ok {
			res.Errors = append(res.Errors, fmt.Sprintf("CREATE %s: local item vanished", e.Key))
			continue
		}
		rt, err := mp.ToRemote(li)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("CREATE %s: map: %v", e.Key, err))
			continue
		}
		if _, err := cl.CreateTask(ctx, listID, client.Task{
			Name: rt.Name, Description: rt.Description, Status: rt.Status, Tags: rt.Tags,
		}); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("CREATE %s: %v", e.Key, err))
			continue
		}
		res.Created++
	}

	for _, e := range plan.Update {
		// Only push when the row is actually drifted. Keyed off the TYPED InSync
		// flag, never the human-readable Detail string (N3).
		if e.InSync {
			continue
		}
		li, ok := byKey[e.Key]
		if !ok {
			continue
		}
		rt, err := mp.ToRemote(li)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("UPDATE %s: map: %v", e.Key, err))
			continue
		}
		if err := cl.UpdateTask(ctx, client.Task{
			ID: e.TaskID, Name: rt.Name, Description: rt.Description, Status: rt.Status, Tags: rt.Tags,
		}); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("UPDATE %s: %v", e.Key, err))
			continue
		}
		res.Updated++
	}
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
