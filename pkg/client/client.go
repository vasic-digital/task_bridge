// Package client is a thin, generic wrapper over the MIT-licensed
// raksul/go-clickup transport (https://github.com/raksul/go-clickup, MIT).
//
// P1 SCAFFOLD: this is an interface + stub only. The real HTTP wiring (tasks,
// custom-fields, folders, lists, tags, members, webhooks), the runtime
// X-RateLimit token-bucket throttle (P2.3 / DZ-5), and the incremental
// date_updated_gt pull (P5.1) land in later phases. No live ClickUp call is
// made by this scaffold.
//
// DECOUPLING (§11.4.28): the client takes its token + IDs from a value passed
// in by the engine (sourced from config.Config), never from a hardcoded or
// project-specific constant.
package client

import "context"

// Task is the engine-internal, transport-neutral view of a remote task. The
// mapper (pkg/mapper) translates between this and the local workable-items
// model. Kept minimal in the scaffold; expanded in P3.
type Task struct {
	ID            string
	Name          string
	Description   string
	Status        string
	Tags          []string
	CustomID      string
	DateUpdatedMS int64             // ClickUp date_updated (Unix ms) — freshness signal
	CustomFields  map[string]string // including the ItemKeyCustomField value
}

// Client is the transport contract the sync engine depends on. Keeping it an
// interface lets tests inject a mock ClickUp (DZ-11 dry-run / DZ-2 conflict
// scenarios) with zero real API calls.
type Client interface {
	// ListTasks returns up to one page of tasks for the configured list.
	// sinceMS implements the incremental date_updated_gt pull (0 = full).
	ListTasks(ctx context.Context, listID string, page int, sinceMS int64) ([]Task, bool, error)
	// GetTask fetches a single task by id.
	GetTask(ctx context.Context, taskID string) (Task, error)
	// CreateTask creates a task in the list (gated by dry-run upstream).
	CreateTask(ctx context.Context, listID string, t Task) (Task, error)
	// UpdateTask updates name/desc/status/tags (NOT custom fields — those use
	// SetCustomField, per the ClickUp API; see P0 §1.5).
	UpdateTask(ctx context.Context, t Task) error
	// SetCustomField sets one custom-field value (the two-call write, P0 §1.5).
	SetCustomField(ctx context.Context, taskID, fieldID, value string) error
	// AddTag attaches a tag (label) to a task via ClickUp's dedicated add-tag
	// endpoint (POST /task/{id}/tag/{name}). This is the RELIABLE way to set a
	// tag on an EXISTING task — ClickUp's update-task body IGNORES the `tags`
	// field (verified live 2026-07-27: a status column update landed but the
	// body-supplied tags did not), so the `status:<word>` label MUST go through
	// this endpoint on the update path. Adding an already-present tag is a no-op.
	AddTag(ctx context.Context, taskID, tag string) error
	// RemoveTag detaches a tag from a task (DELETE /task/{id}/tag/{name}). Used
	// only to remove a SUPERSEDED status:<word> label when the exact status
	// changed within the same grouped column. It NEVER removes a non-status tag
	// and never deletes the task itself (§9/§11.4.122).
	RemoveTag(ctx context.Context, taskID, tag string) error
	// DeleteTask deletes a task. Only ever called under AllowRemoteDelete.
	DeleteTask(ctx context.Context, taskID string) error
}

// stubClient is the P1 placeholder. It satisfies the interface so the module
// compiles; every method returns ErrNotImplemented until the phase that wires
// raksul/go-clickup. This is honest scaffolding, not a fake (§11.4.27 — fakes
// live only in unit tests; this stub returns an explicit not-implemented error
// rather than pretending to succeed).
type stubClient struct{}

// New returns the P1 stub. Replaced by the real raksul/go-clickup-backed
// implementation in P5; signature stays stable.
func New(token string) Client { _ = token; return stubClient{} }

func (stubClient) ListTasks(context.Context, string, int, int64) ([]Task, bool, error) {
	return nil, false, ErrNotImplemented
}
func (stubClient) GetTask(context.Context, string) (Task, error) {
	return Task{}, ErrNotImplemented
}
func (stubClient) CreateTask(context.Context, string, Task) (Task, error) {
	return Task{}, ErrNotImplemented
}
func (stubClient) UpdateTask(context.Context, Task) error { return ErrNotImplemented }
func (stubClient) SetCustomField(context.Context, string, string, string) error {
	return ErrNotImplemented
}
func (stubClient) AddTag(context.Context, string, string) error    { return ErrNotImplemented }
func (stubClient) RemoveTag(context.Context, string, string) error { return ErrNotImplemented }
func (stubClient) DeleteTask(context.Context, string) error        { return ErrNotImplemented }
