package client

import "errors"

// ErrNotImplemented marks a P1-scaffold stub method whose real transport wiring
// (raksul/go-clickup) lands in a later phase. Returning an explicit error keeps
// the scaffold honest (§11.4.27): it never pretends a remote op succeeded.
var ErrNotImplemented = errors.New("task_bridge/client: not implemented in P1 scaffold")

// ErrUnauthorized marks an HTTP 401/403 from the remote. The batch cannot make
// progress after it, so the engine stops instead of retrying every item.
var ErrUnauthorized = errors.New("task_bridge/client: remote rejected the credentials (HTTP 401/403)")

// ErrFieldCreateUnsupported marks a remote that does not offer custom-field
// creation (HTTP 404/405/501 on the create endpoint).
var ErrFieldCreateUnsupported = errors.New("task_bridge/client: remote does not support creating custom fields")
