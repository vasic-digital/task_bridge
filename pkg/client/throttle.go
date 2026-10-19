// throttle.go — a rate-limit-friendly http.RoundTripper for the live ClickUp
// transport (ATM-1123). ClickUp v2 allows ~100 requests/minute per token and
// answers 429 with Retry-After / X-RateLimit-Reset when exceeded. This wrapper
//
//	(1) spaces every request by at least MinInterval, and
//	(2) on HTTP 429 sleeps for the server-supplied delay and retries the SAME
//	    request (body replayed via GetBody) up to Max429Retries times.
//
// It never logs, stores or rewrites credentials: the Authorization header is
// set by the upstream client and passed through untouched (§11.4.10).
package client

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxRetryAfter caps how long a single 429 wait may be. A longer server demand
// is returned to the caller as the 429 response itself so a bounded batch stops
// instead of stalling for minutes.
const maxRetryAfter = 120 * time.Second

// defaultRetryAfter is used when a 429 carries neither Retry-After nor a usable
// X-RateLimit-Reset.
const defaultRetryAfter = 5 * time.Second

// ThrottleConfig configures the transport. Sleep and Now are injectable so unit
// tests run without real waiting.
type ThrottleConfig struct {
	MinInterval   time.Duration
	Max429Retries int
	Sleep         func(time.Duration)
	Now           func() time.Time
}

type throttleTransport struct {
	base http.RoundTripper
	cfg  ThrottleConfig
	mu   sync.Mutex
	last time.Time
}

func newThrottle(base http.RoundTripper, cfg ThrottleConfig) *throttleTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &throttleTransport{base: base, cfg: cfg}
}

// pace blocks until MinInterval has elapsed since the previous request started.
func (t *throttleTransport) pace() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cfg.MinInterval > 0 && !t.last.IsZero() {
		if wait := t.cfg.MinInterval - t.cfg.Now().Sub(t.last); wait > 0 {
			t.cfg.Sleep(wait)
		}
	}
	t.last = t.cfg.Now()
}

// retryDelay derives the wait from a 429 response.
func retryDelay(resp *http.Response, now time.Time) time.Duration {
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
			return time.Duration(secs * float64(time.Second))
		}
		if at, err := http.ParseTime(v); err == nil {
			if d := at.Sub(now); d > 0 {
				return d
			}
			return 0
		}
	}
	if v := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Unix(unix, 0).Sub(now); d > 0 {
				return d + time.Second
			}
			return time.Second
		}
	}
	return defaultRetryAfter
}

func (t *throttleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		t.pace()
		r := req
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r = req.Clone(req.Context())
			r.Body = body
		}
		resp, err := t.base.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= t.cfg.Max429Retries {
			return resp, nil
		}
		// A body that cannot be replayed cannot be retried safely.
		if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
			return resp, nil
		}
		d := retryDelay(resp, t.cfg.Now())
		if d > maxRetryAfter {
			return resp, nil // let the caller stop the batch
		}
		_ = resp.Body.Close()
		t.cfg.Sleep(d)
	}
}
