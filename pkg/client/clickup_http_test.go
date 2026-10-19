package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorded is one request the fake ClickUp server saw.
type recorded struct {
	Method, Path, Auth string
	Body               map[string]any
}

// fakeClickUp is a LOCAL fake HTTP server (fakes are permitted only in unit
// tests, §11.4.27). script(n, r) decides the response for the n-th request.
type fakeClickUp struct {
	mu   sync.Mutex
	reqs []recorded
	srv  *httptest.Server
}

func newFake(t *testing.T, script func(n int, w http.ResponseWriter, r *http.Request)) *fakeClickUp {
	f := &fakeClickUp{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
		n := len(f.reqs)
		f.mu.Unlock()
		script(n, w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeClickUp) all() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func okTask(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"` + id + `","name":"[ATM-1] x","status":{"status":"to do"}}`))
}

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(d time.Duration) { s.mu.Lock(); s.d = append(s.d, d); s.mu.Unlock() }
func (s *sleeps) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

const testToken = "TESTTOKEN-not-a-real-credential"

// TestCreateTaskIsRealPostWith429Retry proves the REAL create path: POST
// /list/{id}/task with the injected Authorization header, JSON name/status/tags
// body; a first 429 with Retry-After: 2 is honoured (sleep 2s) and the SAME body
// is replayed on the retry; the created id is returned.
func TestCreateTaskIsRealPostWith429Retry(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"err":"Rate limit reached","ECODE":"APP_002"}`))
			return
		}
		okTask(w, "abc123")
	})
	var sl sleeps
	cl := NewClickUpWithOptions(testToken, Options{BaseURL: f.srv.URL, MinInterval: time.Millisecond, Sleep: sl.sleep})
	got, err := cl.CreateTask(context.Background(), "L1", Task{Name: "[ATM-1] x", Status: "to do", Tags: []string{"Bug", "status:Queued"}})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if got.ID != "abc123" {
		t.Fatalf("created id = %q, want abc123", got.ID)
	}
	reqs := f.all()
	if len(reqs) != 2 {
		t.Fatalf("server saw %d requests, want 2 (429 then retry)", len(reqs))
	}
	for i, r := range reqs {
		if r.Method != http.MethodPost || r.Path != "/list/L1/task" {
			t.Fatalf("req %d = %s %s, want POST /list/L1/task", i, r.Method, r.Path)
		}
		if r.Auth != testToken {
			t.Fatalf("req %d Authorization header not forwarded", i)
		}
		if r.Body["name"] != "[ATM-1] x" || r.Body["status"] != "to do" {
			t.Fatalf("req %d body = %v: body not replayed intact", i, r.Body)
		}
	}
	found2 := false
	for _, d := range sl.got() {
		if d == 2*time.Second {
			found2 = true
		}
	}
	if !found2 {
		t.Fatalf("sleeps = %v, want one 2s wait honouring Retry-After", sl.got())
	}
}

// TestUpdateTaskNameOnlyBodyOmitsOtherFields proves the title-only update sends
// ONLY the name (PUT /task/{id}); description/status/tags must not be present so
// a title repair can never overwrite them.
func TestUpdateTaskNameOnlyBodyOmitsOtherFields(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) { okTask(w, "t9") })
	cl := NewClickUpWithOptions(testToken, Options{BaseURL: f.srv.URL, MinInterval: time.Millisecond, Sleep: func(time.Duration) {}})
	if err := cl.UpdateTask(context.Background(), Task{ID: "t9", Name: "[ATM-9] new title"}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	reqs := f.all()
	if len(reqs) != 1 || reqs[0].Method != http.MethodPut || strings.TrimSuffix(reqs[0].Path, "/") != "/task/t9" {
		t.Fatalf("requests = %+v, want exactly PUT /task/t9", reqs)
	}
	if reqs[0].Body["name"] != "[ATM-9] new title" {
		t.Fatalf("name missing from body %v", reqs[0].Body)
	}
	for _, k := range []string{"description", "status", "tags"} {
		if _, present := reqs[0].Body[k]; present {
			t.Fatalf("body carries %q — a title-only update must not overwrite it (%v)", k, reqs[0].Body)
		}
	}
}

// TestRetryAfterBeyondCapStopsInsteadOfStalling proves a >120s server demand is
// NOT slept on: the 429 is surfaced as an error after a single request.
func TestRetryAfterBeyondCapStopsInsteadOfStalling(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"err":"Rate limit reached"}`))
	})
	var sl sleeps
	cl := NewClickUpWithOptions(testToken, Options{BaseURL: f.srv.URL, MinInterval: time.Millisecond, Sleep: sl.sleep})
	if _, err := cl.CreateTask(context.Background(), "L1", Task{Name: "[ATM-1] x", Status: "to do"}); err == nil {
		t.Fatal("expected an error when the server demands a 600s wait")
	}
	if n := len(f.all()); n != 1 {
		t.Fatalf("server saw %d requests, want 1 (no retry past the cap)", n)
	}
	for _, d := range sl.got() {
		if d >= 120*time.Second {
			t.Fatalf("slept %v — must not honour a wait beyond the cap", d)
		}
	}
}

// TestRetriesAreBounded proves a permanently-429 server is retried exactly
// Max429Retries times then fails (no infinite loop).
func TestRetriesAreBounded(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"err":"x"}`))
	})
	cl := NewClickUpWithOptions(testToken, Options{BaseURL: f.srv.URL, MinInterval: time.Millisecond, Max429Retries: 3, Sleep: func(time.Duration) {}})
	if _, err := cl.CreateTask(context.Background(), "L1", Task{Name: "[ATM-1] x", Status: "to do"}); err == nil {
		t.Fatal("expected error from a permanently rate-limited server")
	}
	if n := len(f.all()); n != 4 {
		t.Fatalf("server saw %d requests, want 4 (1 + 3 retries)", n)
	}
}

// TestMinIntervalPacesRequests proves consecutive requests are spaced by the
// configured interval (the sleep requested equals the unspent remainder).
func TestMinIntervalPacesRequests(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) { okTask(w, "t1") })
	var sl sleeps
	fake := time.Unix(1_700_000_000, 0)
	cl := NewClickUpWithOptions(testToken, Options{
		BaseURL: f.srv.URL, MinInterval: 700 * time.Millisecond, Sleep: sl.sleep,
		Now: func() time.Time { return fake }, // clock frozen: remainder is the full interval
	})
	for i := 0; i < 3; i++ {
		if err := cl.UpdateTask(context.Background(), Task{ID: "t1", Name: "[ATM-1] x"}); err != nil {
			t.Fatal(err)
		}
	}
	paced := 0
	for _, d := range sl.got() {
		if d == 700*time.Millisecond {
			paced++
		}
	}
	if paced != 2 {
		t.Fatalf("pacing sleeps = %v, want two 700ms waits (before request 2 and 3)", sl.got())
	}
}

// TestTokenNeverAppearsInErrors proves a failing call's error text does not
// contain the token (§11.4.10).
func TestTokenNeverAppearsInErrors(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"err":"Token invalid","ECODE":"OAUTH_025"}`))
	})
	cl := NewClickUpWithOptions(testToken, Options{BaseURL: f.srv.URL, MinInterval: time.Millisecond, Sleep: func(time.Duration) {}})
	_, err := cl.CreateTask(context.Background(), "L1", Task{Name: "[ATM-1] x", Status: "to do"})
	if err == nil {
		t.Fatal("expected 401 error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error text leaks the token: %v", err)
	}
}
