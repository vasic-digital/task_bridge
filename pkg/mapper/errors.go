package mapper

import "errors"

// ErrNotImplemented marks the P1-scaffold stub; the §5.3 field mapping + two-call
// custom-field write land in P3.
var ErrNotImplemented = errors.New("task_bridge/mapper: not implemented in P1 scaffold")
