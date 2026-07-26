package mapper

import "errors"

// Mapper errors. These are real, explicit failures — the mapper NEVER guesses a
// value it cannot map (§11.4.6).
var (
	// ErrMissingKey: a LocalItem has no key to embed as the title prefix.
	ErrMissingKey = errors.New("task_bridge/mapper: local item has no key")
	// ErrNoKeyInTitle: a remote task title carries no `[XXX-NNN]` key.
	ErrNoKeyInTitle = errors.New("task_bridge/mapper: remote task title has no [KEY] prefix")
	// ErrUnmappedStatus: a status value is outside the closed-set vocabulary
	// (P2 §3) — surfaced, never coerced to a guessed value.
	ErrUnmappedStatus = errors.New("task_bridge/mapper: status not in the closed-set vocabulary")
)
