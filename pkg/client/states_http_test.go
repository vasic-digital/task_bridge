package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vasic-digital/task_bridge/pkg/states"
)

func statesClient(t *testing.T, f *fakeClickUp, statesMode bool) Client {
	t.Helper()
	return NewClickUpWithOptions(testToken, Options{BaseURL: f.srv.URL, MinInterval: time.Millisecond, Sleep: func(time.Duration) {}, StatesMode: statesMode})
}

func jsonOK(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

const taskWithFields = `{"id":"t1","name":"[ATM-1] x","status":{"status":"to do"},"markdown_description":"md","description":"pl",
 "custom_fields":[
  {"id":"f1","name":"reproduction_state","type":"drop_down","type_config":{"options":[{"id":"o0","name":"A","orderindex":0},{"id":"o1","name":"B","orderindex":1}]},"value":1},
  {"id":"f2","name":"x","type":"drop_down","type_config":{"options":[{"id":"p0","name":"A","orderindex":0},{"id":"p1","name":"B","orderindex":1}]},"value":"p0"},
  {"id":"f3","name":"y","type":"drop_down","type_config":{"options":[{"id":"q0","name":"A","orderindex":0}]}},
  {"id":"f4","name":"env","type":"text","value":"bench"},
  {"id":"f5","name":"e2","type":"short_text","value":""}]}`

func TestListTasksStatesModeQueryAndDecoding(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		jsonOK(w, `{"tasks":[`+taskWithFields+`,{"id":"t2","name":"n2","status":{"status":"to do"},"description":"only plain"},{"id":"t3","name":"n3","status":{"status":"to do"}}],"last_page":false}`)
	})
	cl := statesClient(t, f, true)
	tasks, more, err := cl.ListTasks(context.Background(), "L1", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !more {
		t.Fatal("last_page=false with a SHORT page must mean there is more")
	}
	rq := f.all()[0]
	if rq.Method != "GET" || rq.Path != "/list/L1/task" {
		t.Fatalf("request = %s %s", rq.Method, rq.Path)
	}
	if rq.Auth != testToken {
		t.Fatal("Authorization not forwarded")
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks = %d", len(tasks))
	}
	t1 := tasks[0]
	if t1.Description != "md" || t1.DescriptionSource != "markdown" {
		t.Fatalf("t1 desc = %q source %q", t1.Description, t1.DescriptionSource)
	}
	if tasks[1].DescriptionSource != "plain" || tasks[1].Description != "only plain" {
		t.Fatalf("t2 = %+v", tasks[1])
	}
	if tasks[2].DescriptionSource != "empty" {
		t.Fatalf("t3 source = %q", tasks[2].DescriptionSource)
	}
	if got := t1.Fields["f1"]; !got.Set || got.OptionName != "B" {
		t.Fatalf("drop-down by orderindex: %+v", got)
	}
	if got := t1.Fields["f2"]; !got.Set || got.OptionName != "A" {
		t.Fatalf("drop-down by id: %+v", got)
	}
	if got := t1.Fields["f3"]; got.Set {
		t.Fatalf("unset drop-down reported set: %+v", got)
	}
	if got := t1.Fields["f4"]; !got.Set || got.Text != "bench" {
		t.Fatalf("text: %+v", got)
	}
	if got := t1.Fields["f5"]; got.Set {
		t.Fatalf("empty text must be unset: %+v", got)
	}
}

func TestListTasksStatesModeQueryParameters(t *testing.T) {
	var q map[string][]string
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		jsonOK(w, `{"tasks":[]}`)
	})
	cl := statesClient(t, f, true)
	if _, _, err := cl.ListTasks(context.Background(), "L1", 2, 1700000000000); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"page": "2", "include_closed": "true", "subtasks": "true", "include_markdown_description": "true", "date_updated_gt": "1700000000000"} {
		if len(q[k]) != 1 || q[k][0] != want {
			t.Errorf("query %s = %v, want %s", k, q[k], want)
		}
	}
}

func TestListTasksOriginalModeDoesNotAskForMarkdown(t *testing.T) {
	var raw string
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		jsonOK(w, `{"tasks":[]}`)
	})
	cl := statesClient(t, f, false)
	if _, _, err := cl.ListTasks(context.Background(), "L1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "include_markdown_description") {
		t.Fatalf("the original path changed: %q", raw)
	}
}

func TestPagingRules(t *testing.T) {
	mk := func(n int) string {
		var b []string
		for i := 0; i < n; i++ {
			b = append(b, `{"id":"i`+string(rune('a'+i%26))+`","name":"n","status":{"status":"to do"}}`)
		}
		return strings.Join(b, ",")
	}
	cases := []struct {
		name string
		body string
		more bool
	}{
		{"full-page-no-flag", `{"tasks":[` + mk(100) + `]}`, true},
		{"short-page-no-flag", `{"tasks":[` + mk(60) + `]}`, false},
		{"full-page-last-true", `{"tasks":[` + mk(100) + `],"last_page":true}`, false},
		{"short-page-last-false", `{"tasks":[` + mk(60) + `],"last_page":false}`, true},
		{"empty-page-last-false", `{"tasks":[],"last_page":false}`, false},
	}
	for _, c := range cases {
		body := c.body
		f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) { jsonOK(w, body) })
		_, more, err := statesClient(t, f, true).ListTasks(context.Background(), "L", 0, 0)
		if err != nil || more != c.more {
			t.Errorf("%s: more=%v err=%v, want %v", c.name, more, err, c.more)
		}
	}
}

func asStates(t *testing.T, c Client) StatesClient {
	sc, ok := c.(StatesClient)
	if !ok {
		t.Fatal("live client does not implement StatesClient")
	}
	return sc
}

func TestListFields(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		jsonOK(w, `{"fields":[{"id":"a","name":"reproduction_state","type":"drop_down","type_config":{"options":[{"id":"o","name":"X","orderindex":"2"}]}},{"id":"b","name":"env","type":"text","type_config":{}}]}`)
	})
	defs, err := asStates(t, statesClient(t, f, true)).ListFields(context.Background(), "L1")
	if err != nil {
		t.Fatal(err)
	}
	rq := f.all()[0]
	if rq.Method != "GET" || rq.Path != "/list/L1/field" {
		t.Fatalf("request = %s %s", rq.Method, rq.Path)
	}
	if len(defs) != 2 || defs[0].ID != "a" || defs[0].Options[0].OrderIndex != 2 || defs[0].Options[0].Name != "X" || defs[1].Type != "text" {
		t.Fatalf("defs = %+v", defs)
	}
}

func TestSetClearAndDescriptionRequests(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) { jsonOK(w, `{}`) })
	sc := asStates(t, statesClient(t, f, true))
	ctx := context.Background()
	if err := sc.SetFieldValue(ctx, "T1", "F1", "optid"); err != nil {
		t.Fatal(err)
	}
	if err := sc.ClearFieldValue(ctx, "T1", "F1"); err != nil {
		t.Fatal(err)
	}
	if err := sc.SetDescription(ctx, "T1", "## md"); err != nil {
		t.Fatal(err)
	}
	rs := f.all()
	if rs[0].Method != "POST" || rs[0].Path != "/task/T1/field/F1" || rs[0].Body["value"] != "optid" || len(rs[0].Body) != 1 {
		t.Fatalf("set = %+v", rs[0])
	}
	if rs[1].Method != "DELETE" || rs[1].Path != "/task/T1/field/F1" {
		t.Fatalf("clear = %+v", rs[1])
	}
	if rs[2].Method != "PUT" || len(rs[2].Body) != 1 || rs[2].Body["markdown_description"] != "## md" {
		t.Fatalf("description write must carry ONLY markdown_description: %+v", rs[2].Body)
	}
	for _, r := range rs {
		if r.Auth != testToken {
			t.Fatal("token not forwarded")
		}
	}
}

func TestCreateFieldBodyAndUnsupportedMapping(t *testing.T) {
	status := http.StatusOK
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			jsonOK(w, `{"err":"nope","ECODE":"X_1"}`)
			return
		}
		jsonOK(w, `{"field":{"id":"nf","name":"reproduction_state","type":"drop_down","type_config":{"options":[{"id":"o0","name":"A","orderindex":0}]}}}`)
	})
	sc := asStates(t, statesClient(t, f, true))
	def := states.FieldDef{Name: "reproduction_state", Type: "drop_down", Options: []states.OptionDef{{Name: "A"}, {Name: "B"}}}
	got, err := sc.CreateField(context.Background(), "L1", def)
	if err != nil || got.ID != "nf" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	b := f.all()[0].Body
	opts := b["type_config"].(map[string]interface{})["options"].([]interface{})
	if b["name"] != "reproduction_state" || b["type"] != "drop_down" || len(opts) != 2 || opts[1].(map[string]interface{})["orderindex"].(float64) != 1 {
		t.Fatalf("body = %v", b)
	}
	for _, s := range []int{404, 405, 501} {
		status = s
		if _, err := sc.CreateField(context.Background(), "L1", def); !errors.Is(err, ErrFieldCreateUnsupported) {
			t.Errorf("status %d -> %v, want ErrFieldCreateUnsupported", s, err)
		}
	}
	status = 400
	if _, err := sc.CreateField(context.Background(), "L1", def); err == nil || errors.Is(err, ErrFieldCreateUnsupported) || IsAuth(err) {
		t.Errorf("400 -> %v", err)
	}
	status = 401
	if _, err := sc.CreateField(context.Background(), "L1", def); !IsAuth(err) {
		t.Errorf("401 -> %v, want auth", err)
	}
}

func TestAuthClassificationOnEveryNewCall(t *testing.T) {
	for _, code := range []int{401, 403} {
		c := code
		f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c)
			jsonOK(w, `{"err":"Token invalid","ECODE":"OAUTH_025"}`)
		})
		cl := statesClient(t, f, true)
		sc := asStates(t, cl)
		ctx := context.Background()
		_, _, e1 := cl.ListTasks(ctx, "L", 0, 0)
		_, e2 := sc.ListFields(ctx, "L")
		e3 := sc.SetFieldValue(ctx, "T", "F", "v")
		e4 := sc.ClearFieldValue(ctx, "T", "F")
		e5 := sc.SetDescription(ctx, "T", "x")
		_, e6 := cl.GetTask(ctx, "T")
		for i, e := range []error{e1, e2, e3, e4, e5, e6} {
			if !IsAuth(e) {
				t.Errorf("status %d call %d: %v is not classified as auth", c, i, e)
			}
			if e != nil && strings.Contains(e.Error(), testToken) {
				t.Errorf("token leaked into %v", e)
			}
		}
	}
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); jsonOK(w, `{"err":"boom"}`) })
	if err := asStates(t, statesClient(t, f, true)).SetDescription(context.Background(), "T", "x"); err == nil || IsAuth(err) {
		t.Fatalf("500 must be a plain error: %v", err)
	}
}

func TestIsAuthNilAndPlain(t *testing.T) {
	if IsAuth(nil) || IsAuth(errors.New("401 in text only")) {
		t.Fatal("IsAuth must not guess from text")
	}
	if !IsAuth(ErrUnauthorized) {
		t.Fatal("ErrUnauthorized must be auth")
	}
}

func TestStateWritesAre429RetriedWithTheSameBody(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			jsonOK(w, `{"err":"Rate limit reached","ECODE":"APP_002"}`)
			return
		}
		jsonOK(w, `{}`)
	})
	var sl sleeps
	cl := NewClickUpWithOptions(testToken, Options{BaseURL: f.srv.URL, MinInterval: time.Millisecond, Sleep: sl.sleep, StatesMode: true})
	if err := asStates(t, cl).SetFieldValue(context.Background(), "T", "F", "optid"); err != nil {
		t.Fatal(err)
	}
	rs := f.all()
	if len(rs) != 2 || rs[0].Body["value"] != "optid" || rs[1].Body["value"] != "optid" {
		t.Fatalf("requests = %+v", rs)
	}
	var waited bool
	for _, d := range sl.got() {
		if d == time.Second {
			waited = true
		}
	}
	if !waited {
		t.Fatalf("Retry-After not honoured: %v", sl.got())
	}
}

func TestGetTaskStatesModeAsksForMarkdown(t *testing.T) {
	var raw string
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		jsonOK(w, taskWithFields)
	})
	got, err := statesClient(t, f, true).GetTask(context.Background(), "t1")
	if err != nil || raw != "include_markdown_description=true" || got.Description != "md" {
		t.Fatalf("raw=%q got=%+v err=%v", raw, got, err)
	}
}
