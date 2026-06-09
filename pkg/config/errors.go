package config

import "errors"

// Injection-contract errors. These name the MISSING consumer-supplied field
// without ever referencing the field's value (§11.4.10 — no credential leak).
var (
	ErrMissingToken   = errors.New("task_bridge/config: APIToken not injected by consumer")
	ErrMissingBoard   = errors.New("task_bridge/config: neither ListID nor BoardURL injected by consumer")
	ErrMissingDB      = errors.New("task_bridge/config: DBPath not injected by consumer")
	ErrMissingItemKey = errors.New("task_bridge/config: ItemKeyCustomField not injected by consumer")
)
