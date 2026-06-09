package client

import "errors"

// ErrNotImplemented marks a P1-scaffold stub method whose real transport wiring
// (raksul/go-clickup) lands in a later phase. Returning an explicit error keeps
// the scaffold honest (§11.4.27): it never pretends a remote op succeeded.
var ErrNotImplemented = errors.New("task_bridge/client: not implemented in P1 scaffold")
