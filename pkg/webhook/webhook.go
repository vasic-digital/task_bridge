// Package webhook receives live ClickUp change events (P0 §2 / §6).
//
// P1 SCAFFOLD: the VerifySignature contract + a stub HTTP handler. The real
// X-Signature HMAC-SHA256-hex-of-raw-body verification (P0 §2, the receiver MUST
// verify before acting) and the targeted single-task reconcile enqueue land in
// P6. Webhooks are a latency optimisation only; the cron reconcile is the
// authoritative safety net (DZ-6), so webhook loss/duplication is non-fatal.
package webhook

import "context"

// VerifySignature checks the ClickUp X-Signature header (HMAC-SHA256 hex of the
// raw request body, keyed by the webhook secret). P1 stub returns
// ErrNotImplemented; the real constant-time verify lands in P6.1 with its
// signature unit test.
func VerifySignature(secret string, rawBody []byte, sigHeader string) error {
	_ = secret
	_ = rawBody
	_ = sigHeader
	return ErrNotImplemented
}

// Handler is the receiver contract: verify, then enqueue a targeted reconcile.
type Handler interface {
	Handle(ctx context.Context, rawBody []byte, sigHeader string) error
}

type stubHandler struct{}

// New returns the P1 stub handler.
func New() Handler { return stubHandler{} }

func (stubHandler) Handle(context.Context, []byte, string) error { return ErrNotImplemented }
