package syncengine

import "errors"

// ErrNotImplemented marks a P1-scaffold stub; the §5.1 reconciler + 2-phase
// idempotent commit land in P5.
var ErrNotImplemented = errors.New("task_bridge/syncengine: not implemented in P1 scaffold")
