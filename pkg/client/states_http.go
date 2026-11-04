// states_http.go — the ClickUp transport for the consumer-defined state fields
// (pkg/states): the explicit task listing, custom-field discovery / guarded
// creation / set / clear, and the Markdown description write.
//
// Everything goes through the SAME http.Client as the rest of the transport, so
// the throttle (min request spacing, Retry-After aware 429 retry) and the
// credential handling (Authorization header set by the upstream client, never
// logged) apply unchanged (ATM-1123, §11.4.10).
//
// UNCONFIRMED against the live service (no token was available when this was
// written): the exact JSON shapes of the custom-field endpoints beyond what the
// public documentation states, the `last_page` member of the list response, and
// the create-field endpoint (which the public documentation does not list). Each
// is isolated in one function below and is covered by the offline mock server
// (scripts/mock_clickup_server.py) only.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	clickup "github.com/raksul/go-clickup/clickup"

	"github.com/vasic-digital/task_bridge/pkg/states"
)

// Compile-time proof that the live transport satisfies the optional extension.
var _ StatesClient = (*clickupClient)(nil)

type listTasksResponse struct {
	Tasks    []clickup.Task `json:"tasks"`
	LastPage *bool          `json:"last_page"`
}

// listTasksStates is ListTasks for StatesMode. It asks for the Markdown source
// of the description and uses the API's last_page flag when present: a SHORT page
// with last_page=false is a partial page and paging continues; a full page with
// last_page=true ends it. Without the flag it falls back to the full-page rule.
// An empty page always ends paging.
func (c *clickupClient) listTasksStates(ctx context.Context, listID string, page int, sinceMS int64) ([]Task, bool, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("include_closed", "true")
	q.Set("subtasks", "true")
	q.Set("include_markdown_description", "true")
	if sinceMS > 0 {
		q.Set("date_updated_gt", strconv.FormatInt(sinceMS, 10))
	}
	req, err := c.up.NewRequest(http.MethodGet, "list/"+url.PathEscape(listID)+"/task?"+q.Encode(), nil)
	if err != nil {
		return nil, false, err
	}
	var resp listTasksResponse
	if _, err := c.up.Do(ctx, req, &resp); err != nil {
		return nil, false, classify(err)
	}
	out := make([]Task, 0, len(resp.Tasks))
	for i := range resp.Tasks {
		out = append(out, fromUpstream(&resp.Tasks[i]))
	}
	hasMore := len(resp.Tasks) == clickPageSize
	if resp.LastPage != nil {
		hasMore = !*resp.LastPage
	}
	if len(resp.Tasks) == 0 {
		hasMore = false
	}
	return out, hasMore, nil
}

// classify maps an upstream error to ErrUnauthorized when the status says so.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var er *clickup.ErrorResponse
	if errors.As(err, &er) && er.Response != nil && IsAuth(err) {
		return fmt.Errorf("%w: %s %d %s", ErrUnauthorized, er.Response.Request.Method, er.Response.StatusCode, er.ECode)
	}
	return err
}

// getTaskStates is GetTask for StatesMode: it asks for the Markdown source so
// a read-back after a description write compares like with like.
func (c *clickupClient) getTaskStates(ctx context.Context, taskID string) (Task, error) {
	req, err := c.up.NewRequest(http.MethodGet, "task/"+url.PathEscape(taskID)+"?include_markdown_description=true", nil)
	if err != nil {
		return Task{}, err
	}
	var t clickup.Task
	if _, err := c.up.Do(ctx, req, &t); err != nil {
		return Task{}, classify(err)
	}
	return fromUpstream(&t), nil
}

// IsAuth reports whether err is a credentials rejection (HTTP 401/403), whether
// it was already classified or is a raw upstream error response.
func IsAuth(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnauthorized) {
		return true
	}
	var er *clickup.ErrorResponse
	return errors.As(err, &er) && er.Response != nil &&
		(er.Response.StatusCode == http.StatusUnauthorized || er.Response.StatusCode == http.StatusForbidden)
}

type fieldsResponse struct {
	Fields []struct {
		ID         string                 `json:"id"`
		Name       string                 `json:"name"`
		Type       string                 `json:"type"`
		TypeConfig map[string]interface{} `json:"type_config"`
	} `json:"fields"`
}

// ListFields returns the custom fields a list exposes.
func (c *clickupClient) ListFields(ctx context.Context, listID string) ([]states.FieldDef, error) {
	req, err := c.up.NewRequest(http.MethodGet, "list/"+url.PathEscape(listID)+"/field", nil)
	if err != nil {
		return nil, err
	}
	var resp fieldsResponse
	if _, err := c.up.Do(ctx, req, &resp); err != nil {
		return nil, classify(err)
	}
	out := make([]states.FieldDef, 0, len(resp.Fields))
	for _, f := range resp.Fields {
		d := states.FieldDef{ID: f.ID, Name: f.Name, Type: f.Type}
		d.Options = decodeOptions(f.TypeConfig)
		out = append(out, d)
	}
	return out, nil
}

// CreateField asks the remote to create a custom field. ClickUp documents no
// such endpoint (UNCONFIRMED whether one exists); a 404/405/501 is reported as
// ErrFieldCreateUnsupported so the caller falls back to the description block.
func (c *clickupClient) CreateField(ctx context.Context, listID string, def states.FieldDef) (states.FieldDef, error) {
	body := map[string]interface{}{"name": def.Name, "type": def.Type}
	if def.Type == "drop_down" {
		opts := make([]map[string]interface{}, 0, len(def.Options))
		for i, o := range def.Options {
			opts = append(opts, map[string]interface{}{"name": o.Name, "orderindex": i})
		}
		body["type_config"] = map[string]interface{}{"options": opts}
	}
	req, err := c.up.NewRequest(http.MethodPost, "list/"+url.PathEscape(listID)+"/field", body)
	if err != nil {
		return states.FieldDef{}, err
	}
	var resp struct {
		Field struct {
			ID         string                 `json:"id"`
			Name       string                 `json:"name"`
			Type       string                 `json:"type"`
			TypeConfig map[string]interface{} `json:"type_config"`
		} `json:"field"`
	}
	r, err := c.up.Do(ctx, req, &resp)
	if err != nil {
		if r != nil && r.Response != nil {
			switch r.Response.StatusCode {
			case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
				return states.FieldDef{}, ErrFieldCreateUnsupported
			}
		}
		return states.FieldDef{}, classify(err)
	}
	if resp.Field.ID == "" {
		return states.FieldDef{}, fmt.Errorf("create field %q: response carried no field id", def.Name)
	}
	return states.FieldDef{ID: resp.Field.ID, Name: resp.Field.Name, Type: resp.Field.Type, Options: decodeOptions(resp.Field.TypeConfig)}, nil
}

// SetFieldValue sets one custom-field value on a task.
func (c *clickupClient) SetFieldValue(ctx context.Context, taskID, fieldID string, value interface{}) error {
	req, err := c.up.NewRequest(http.MethodPost, "task/"+url.PathEscape(taskID)+"/field/"+url.PathEscape(fieldID), map[string]interface{}{"value": value})
	if err != nil {
		return err
	}
	_, err = c.up.Do(ctx, req, nil)
	return classify(err)
}

// ClearFieldValue removes one custom-field value from a task.
func (c *clickupClient) ClearFieldValue(ctx context.Context, taskID, fieldID string) error {
	req, err := c.up.NewRequest(http.MethodDelete, "task/"+url.PathEscape(taskID)+"/field/"+url.PathEscape(fieldID), nil)
	if err != nil {
		return err
	}
	_, err = c.up.Do(ctx, req, nil)
	return classify(err)
}

// SetDescription replaces the Markdown description of a task and nothing else.
func (c *clickupClient) SetDescription(ctx context.Context, taskID, markdown string) error {
	req, err := c.up.NewRequest(http.MethodPut, "task/"+url.PathEscape(taskID), map[string]interface{}{"markdown_description": markdown})
	if err != nil {
		return err
	}
	_, err = c.up.Do(ctx, req, nil)
	return classify(err)
}

// decodeOptions reads type_config.options of a drop_down field.
func decodeOptions(tc map[string]interface{}) []states.OptionDef {
	raw, _ := tc["options"].([]interface{})
	out := make([]states.OptionDef, 0, len(raw))
	for _, o := range raw {
		m, ok := o.(map[string]interface{})
		if !ok {
			continue
		}
		od := states.OptionDef{}
		od.ID, _ = m["id"].(string)
		od.Name, _ = m["name"].(string)
		if od.Name == "" {
			od.Name, _ = m["label"].(string)
		}
		switch oi := m["orderindex"].(type) {
		case float64:
			od.OrderIndex = int(oi)
		case string:
			od.OrderIndex, _ = strconv.Atoi(oi)
		}
		out = append(out, od)
	}
	return out
}

// decodeFields converts the upstream custom_fields array to RemoteFields keyed by
// custom-field id. A drop_down value arrives as the option's orderindex (number)
// or id (string); both are resolved to the option NAME. UNCONFIRMED which of the
// two the live API sends; both are handled.
func decodeFields(cfs []clickup.CustomField) map[string]states.RemoteField {
	if len(cfs) == 0 {
		return nil
	}
	out := make(map[string]states.RemoteField, len(cfs))
	for _, cf := range cfs {
		rf := states.RemoteField{ID: cf.ID, Name: cf.Name, Type: cf.Type}
		switch cf.Type {
		case "drop_down":
			tc, _ := cf.TypeConfig.(map[string]interface{})
			opts := decodeOptions(tc)
			switch v := cf.Value.(type) {
			case nil:
			case float64:
				rf.Set = true
				for _, o := range opts {
					if o.OrderIndex == int(v) {
						rf.OptionName = o.Name
					}
				}
			case string:
				rf.Set = true
				for _, o := range opts {
					if o.ID == v {
						rf.OptionName = o.Name
					}
				}
			}
		case "text", "short_text":
			if v, ok := cf.Value.(string); ok && v != "" {
				rf.Set = true
				rf.Text = v
			}
		default:
			if cf.Value != nil {
				rf.Set = true
				rf.Text = fmt.Sprint(cf.Value)
			}
		}
		out[cf.ID] = rf
	}
	return out
}
