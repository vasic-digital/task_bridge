package client

import (
	clickup "github.com/raksul/go-clickup/clickup"
)

// newUpstream constructs the MIT raksul/go-clickup transport client with the
// injected personal token. P1 SCAFFOLD: this wires the dependency for real (so
// the module genuinely depends on go-clickup, not a dangling require) but the
// returned upstream client is not yet driven — the Client interface methods are
// implemented against it in P5. It is referenced by New() below to keep the
// import live and the dependency direct.
//
// DECOUPLING (§11.4.28) + CREDENTIALS (§11.4.10): the token is passed in by the
// engine (from config.Config, sourced from the consumer's .env) and is never
// logged here.
func newUpstream(token string) *clickup.Client {
	return clickup.NewClient(nil, token)
}

// ensure the upstream constructor is referenced so go-clickup is a direct,
// used dependency from P1 onward (the real transport wiring lands in P5).
var _ = newUpstream
