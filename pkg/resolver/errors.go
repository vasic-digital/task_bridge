package resolver

import "errors"

// ErrNotImplemented marks the P1-scaffold stub; the API-probe resolution
// (P2.2, §11.4.6 probe-not-guess) lands in a later phase.
var ErrNotImplemented = errors.New("task_bridge/resolver: not implemented in P1 scaffold")
