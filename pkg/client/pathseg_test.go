package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// wireLog is a local fake that records the RAW request target (RequestURI: the
// bytes on the wire before any server-side decoding) of every request.
type wireLog struct {
	mu   sync.Mutex
	got  []string // "<METHOD> <request-target>"
	srv  *httptest.Server
	base string
}

func newWireLog(t *testing.T) *wireLog {
	w := &wireLog{}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.got = append(w.got, r.Method+" "+r.RequestURI)
		w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"id":"t1","name":"[ATM-1] x","status":{"status":"to do"}}`))
	}))
	t.Cleanup(w.srv.Close)
	w.base = w.srv.URL + "/api/v2"
	return w
}

func (w *wireLog) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.got
	w.got = nil
	return out
}

func (w *wireLog) cl() Client {
	return NewClickUpWithOptions(testToken, Options{BaseURL: w.base, MinInterval: time.Millisecond, Sleep: func(time.Duration) {}})
}

// TestPathSegmentsOfRemoteValuesAreEscaped is the regression guard of the
// request-forgery finding: the task id and the tag name come from the ClickUp
// responses (anybody who can tag a task in the synced list controls the tag), the
// upstream library concatenates them into the URL without escaping, so a "/",
// "?", "#" or "../" in them selected another endpoint. Each case asserts the
// exact raw request target: one request, on the intended resource, no query.
func TestPathSegmentsOfRemoteValuesAreEscaped(t *testing.T) {
	w := newWireLog(t)
	cl := w.cl()
	ctx := context.Background()
	type call struct {
		name string
		do   func() error
		want string
	}
	cases := []call{
		// control: legitimate values keep EXACTLY the wire form they had before the fix
		{"add legit", func() error { return cl.AddTag(ctx, "86abc12de", "status:In progress") }, "POST /api/v2/task/86abc12de/tag/status:In%20progress"},
		{"remove legit", func() error { return cl.RemoveTag(ctx, "86abc12de", "status:in progress") }, "DELETE /api/v2/task/86abc12de/tag/status:in%20progress"},
		{"get legit", func() error { _, err := cl.GetTask(ctx, "t1"); return err }, "GET /api/v2/task/t1/"},
		{"update legit", func() error { return cl.UpdateTask(ctx, Task{ID: "t1", Name: "n"}) }, "PUT /api/v2/task/t1/"},
		{"custom id legit", func() error { return cl.RemoveTag(ctx, "ATM-1", "status:queued") }, "DELETE /api/v2/task/ATM-1/tag/status:queued"},
		// hostile tag
		{"tag traversal to a list", func() error { return cl.RemoveTag(ctx, "t1", "status:/../../../../list/victim") },
			"DELETE /api/v2/task/t1/tag/status:%2F..%2F..%2F..%2F..%2Flist%2Fvictim"},
		{"tag slash", func() error { return cl.RemoveTag(ctx, "t1", "status:a/b") }, "DELETE /api/v2/task/t1/tag/status:a%2Fb"},
		{"tag query", func() error { return cl.RemoveTag(ctx, "t1", "status:a?evil=1") }, "DELETE /api/v2/task/t1/tag/status:a%3Fevil=1"},
		{"tag fragment", func() error { return cl.RemoveTag(ctx, "t1", "status:a#frag") }, "DELETE /api/v2/task/t1/tag/status:a%23frag"},
		{"add tag traversal", func() error { return cl.AddTag(ctx, "t1", "../x") }, "POST /api/v2/task/t1/tag/..%2Fx"},
		// hostile task id
		{"id traversal out of the api prefix", func() error { return cl.RemoveTag(ctx, "x/../../../list/victim", "status:queued") },
			"DELETE /api/v2/task/x%2F..%2F..%2F..%2Flist%2Fvictim/tag/status:queued"},
		{"id query", func() error { return cl.AddTag(ctx, "t1?evil=1", "status:Queued") }, "POST /api/v2/task/t1%3Fevil=1/tag/status:Queued"},
		{"update task id slash", func() error { return cl.UpdateTask(ctx, Task{ID: "a/b", Name: "n"}) }, "PUT /api/v2/task/a%2Fb/"},
		{"get task id slash", func() error { _, err := cl.GetTask(ctx, "a/b"); return err }, "GET /api/v2/task/a%2Fb/"},
	}
	for _, c := range cases {
		if err := c.do(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := w.take()
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: wire = %q, want exactly [%q]", c.name, got, c.want)
		}
	}
}

// TestDotSegmentsAndEmptyAreRefusedNotSent: "." and ".." survive any escaping and
// would be resolved as dot segments, "" collapses the path; they are refused
// before any request is built.
func TestDotSegmentsAndEmptyAreRefusedNotSent(t *testing.T) {
	w := newWireLog(t)
	cl := w.cl()
	ctx := context.Background()
	for _, v := range []string{"", ".", ".."} {
		errs := map[string]error{
			"AddTag id":     cl.AddTag(ctx, v, "status:Queued"),
			"AddTag tag":    cl.AddTag(ctx, "t1", v),
			"RemoveTag id":  cl.RemoveTag(ctx, v, "status:queued"),
			"RemoveTag tag": cl.RemoveTag(ctx, "t1", v),
			"UpdateTask id": cl.UpdateTask(ctx, Task{ID: v, Name: "n"}),
		}
		_, errGet := cl.GetTask(ctx, v)
		errs["GetTask id"] = errGet
		for name, err := range errs {
			if err == nil || !strings.Contains(err.Error(), "refusing to build a ClickUp request path") {
				t.Errorf("%s with %q: err = %v, want a refusal", name, v, err)
			}
		}
	}
	if got := w.take(); len(got) != 0 {
		t.Fatalf("a refused value must produce no request, server saw %q", got)
	}
}
