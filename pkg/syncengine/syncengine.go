// Package syncengine is the deterministic last-edit-wins reconciler (P0 §5.1).
//
// P1 SCAFFOLD: the Engine type + Reconcile/Push/Pull stubs + the resolution
// outcome enum. The real per-item 2-phase idempotent commit (DZ-1/DZ-4), the
// same-field-conflict STOP (DZ-2), the clock-skew-immune delta tie-break
// (DZ-7), and the dry-run gate (DZ-11, default ON) land in P5.
//
// CONFLICT MODEL (P0 §5.1), encoded here as the resolution enum so the contract
// is fixed in P1: per item, compute local_changed (hash != last_synced_hash) and
// clickup_changed (date_updated > last_synced). Disjoint changes auto-resolve by
// direction; same-field collisions STOP and surface to the operator — NEVER a
// silent merge.
package syncengine

import (
	"context"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/config"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
)

// Outcome is the deterministic per-item resolution (P0 §5.1 table).
type Outcome int

const (
	OutcomeInSync   Outcome = iota // no-op (early cutoff)
	OutcomePushed                  // local -> remote
	OutcomePulled                  // remote -> local
	OutcomeConflict                // same-field collision: STOP, surface to operator
	OutcomeDeleted                 // tombstoned (never resurrected, DZ-3)
)

// Engine reconciles the local SSoT with the remote board. It holds ONLY
// injected collaborators (no project-specific state) — the decoupling boundary.
type Engine struct {
	cfg config.Config
	cl  client.Client
	mp  mapper.Mapper
}

// New constructs the engine from the consumer-injected config + collaborators.
func New(cfg config.Config, cl client.Client, mp mapper.Mapper) *Engine {
	return &Engine{cfg: cfg, cl: cl, mp: mp}
}

// Reconcile runs one authoritative pass (the cron backstop, P0 §6). DryRun
// (default) performs zero remote writes and logs the would-be calls (DZ-11).
func (e *Engine) Reconcile(ctx context.Context) error {
	_ = ctx
	return ErrNotImplemented
}

// Resolve computes the deterministic outcome for one item given the freshness
// signals. Pure + side-effect-free by design so it is trivially unit-testable
// (DZ-2/DZ-7) without any network. Wired in P5.3.
func (e *Engine) Resolve(localChanged, remoteChanged, sameFieldCollision bool) Outcome {
	_ = localChanged
	_ = remoteChanged
	_ = sameFieldCollision
	// P1 stub: returns InSync; the real truth-table lands in P5.3 with its
	// paired §1.1 mutation test.
	return OutcomeInSync
}
