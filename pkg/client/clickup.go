// Real ClickUp transport, backed by the MIT raksul/go-clickup library.
//
// DECOUPLING (§11.4.28) + CREDENTIALS (§11.4.10): the token is passed in by the
// consumer (from config.Config, sourced from the consumer's .env) and is NEVER
// logged here. This file makes LIVE ClickUp v2 calls when driven by the engine;
// the read path (ListTasks) + the gated write path (CreateTask/UpdateTask) are
// implemented for real. SetCustomField/DeleteTask are intentionally NOT wired
// (the minimum-viable title-prefix mode uses no custom fields and never deletes
// remote data, §9/§11.4.122) — they return an explicit not-implemented error
// rather than faking success (§11.4.27).
package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	clickup "github.com/raksul/go-clickup/clickup"
)

// DefaultMinInterval spaces live requests so a sustained batch stays under
// ClickUp's ~100 requests/minute per-token limit (ATM-1123).
const DefaultMinInterval = 700 * time.Millisecond

// DefaultMax429Retries is how many times one request is retried after HTTP 429.
const DefaultMax429Retries = 5

// Options tunes the live transport. The zero value means: production base URL,
// DefaultMinInterval pacing, DefaultMax429Retries, real clock.
type Options struct {
	BaseURL       string // override (tests / proxies); must be http(s); trailing slash added
	MinInterval   time.Duration
	Max429Retries int
	Sleep         func(time.Duration)
	Now           func() time.Time
}

// clickPageSize is ClickUp's fixed v2 page size for list/{id}/task. A page that
// returns exactly this many tasks means "there may be more" (the library does
// not surface the API's last_page flag), so pagination continues until a short
// page is seen.
const clickPageSize = 100

// clickupClient is the live transport. It holds ONLY the injected upstream
// client — no project-specific state (the decoupling boundary).
type clickupClient struct {
	up *clickup.Client
}

// NewClickUp constructs the live ClickUp-backed Client from the injected token.
// The token is handed straight to the transport and never retained or logged
// here (§11.4.10). Returns the Client interface so callers stay transport-blind.
func NewClickUp(token string) Client {
	return NewClickUpWithOptions(token, Options{})
}

// NewClickUpWithOptions is NewClickUp with an explicit transport policy. Every
// request goes through the throttling/429-aware RoundTripper (throttle.go).
func NewClickUpWithOptions(token string, o Options) Client {
	if o.MinInterval == 0 {
		o.MinInterval = DefaultMinInterval
	}
	if o.Max429Retries == 0 {
		o.Max429Retries = DefaultMax429Retries
	}
	hc := &http.Client{
		Timeout: 60 * time.Second,
		Transport: newThrottle(nil, ThrottleConfig{
			MinInterval: o.MinInterval, Max429Retries: o.Max429Retries, Sleep: o.Sleep, Now: o.Now,
		}),
	}
	up := clickup.NewClient(hc, token)
	if o.BaseURL != "" {
		if u, err := url.Parse(o.BaseURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			if !strings.HasSuffix(u.Path, "/") {
				u.Path += "/"
			}
			up.BaseURL = u
		}
	}
	return &clickupClient{up: up}
}

// ListTasks returns one page of tasks for the list. sinceMS>0 restricts to
// tasks updated after that instant (incremental pull); sinceMS==0 is a full
// pull. Closed tasks + subtasks are included so the full board is reconciled.
// hasMore is true when the page was full (clickPageSize).
func (c *clickupClient) ListTasks(ctx context.Context, listID string, page int, sinceMS int64) ([]Task, bool, error) {
	opts := &clickup.GetTasksOptions{
		Page:          page,
		IncludeClosed: true,
		Subtasks:      true,
	}
	if sinceMS > 0 {
		opts.DateUpdatedGt = clickup.NewDateWithUnixTime(sinceMS)
	}
	up, _, err := c.up.Tasks.GetTasks(ctx, listID, opts)
	if err != nil {
		return nil, false, err
	}
	out := make([]Task, 0, len(up))
	for i := range up {
		out = append(out, fromUpstream(&up[i]))
	}
	return out, len(up) == clickPageSize, nil
}

// GetTask fetches a single task by id.
func (c *clickupClient) GetTask(ctx context.Context, taskID string) (Task, error) {
	up, _, err := c.up.Tasks.GetTask(ctx, taskID, nil)
	if err != nil {
		return Task{}, err
	}
	return fromUpstream(up), nil
}

// CreateTask creates a task in the list. Only ever reached under --apply
// (dry-run suppresses it upstream in the engine).
func (c *clickupClient) CreateTask(ctx context.Context, listID string, t Task) (Task, error) {
	req := &clickup.TaskRequest{
		Name:                t.Name,
		MarkdownDescription: t.Description,
		Status:              t.Status,
		Tags:                t.Tags,
	}
	up, _, err := c.up.Tasks.CreateTask(ctx, listID, req)
	if err != nil {
		return Task{}, err
	}
	return fromUpstream(up), nil
}

// UpdateTask updates name/description/status/tags on an existing task. Only ever
// reached under --apply.
func (c *clickupClient) UpdateTask(ctx context.Context, t Task) error {
	req := &clickup.TaskUpdateRequest{
		Name:        t.Name,
		Description: t.Description,
		Status:      t.Status,
		Tags:        t.Tags,
	}
	_, _, err := c.up.Tasks.UpdateTask(ctx, t.ID, nil, req)
	return err
}

// SetCustomField is not used in title-prefix mode (the board has no custom
// fields). It returns an explicit error rather than faking a write.
func (c *clickupClient) SetCustomField(context.Context, string, string, string) error {
	return ErrNotImplemented
}

// AddTag attaches a label to an existing task via ClickUp's dedicated add-tag
// endpoint (the update-task body ignores tags — verified live 2026-07-27). Only
// ever reached under --apply.
func (c *clickupClient) AddTag(ctx context.Context, taskID, tag string) error {
	_, err := c.up.Tags.AddTagToTask(ctx, taskID, tag, nil)
	return err
}

// RemoveTag detaches a (superseded status:<word>) label from a task. Only ever
// reached under --apply; never deletes the task (§9/§11.4.122).
func (c *clickupClient) RemoveTag(ctx context.Context, taskID, tag string) error {
	_, err := c.up.Tags.RemoveTagToTask(ctx, taskID, tag, nil)
	return err
}

// DeleteTask is never called under the never-auto-delete-remote default
// (§9/§11.4.122); it returns an explicit error to keep the engine honest.
func (c *clickupClient) DeleteTask(context.Context, string) error {
	return ErrNotImplemented
}

// fromUpstream converts a go-clickup Task into the transport-neutral Task.
func fromUpstream(t *clickup.Task) Task {
	tags := make([]string, 0, len(t.Tags))
	for _, tg := range t.Tags {
		tags = append(tags, tg.Name)
	}
	desc := t.MarkdownDescription
	if desc == "" {
		desc = t.Description
	}
	var updatedMS int64
	if v, err := strconv.ParseInt(t.DateUpdated, 10, 64); err == nil {
		updatedMS = v
	}
	return Task{
		ID:            t.ID,
		Name:          t.Name,
		Description:   desc,
		Status:        t.Status.Status,
		Tags:          tags,
		CustomID:      t.CustomID,
		DateUpdatedMS: updatedMS,
	}
}
