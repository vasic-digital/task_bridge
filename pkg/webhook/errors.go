package webhook

import "errors"

// ErrNotImplemented marks a P1-scaffold stub; HMAC-SHA256 verify + targeted
// reconcile land in P6.
var ErrNotImplemented = errors.New("task_bridge/webhook: not implemented in P1 scaffold")
