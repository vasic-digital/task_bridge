# task_bridge — Architecture

**Revision:** 1
**Last modified:** 2026-06-09T00:00:00Z
**Status:** P1 scaffold skeleton. The authoritative architecture + danger-zone
analysis + phased plan live in the consumer's
`docs/research/clickup_integration/P0_research_and_plan.md`; this file mirrors
the engine-side design and is fleshed out per phase.

## 1. Goal

Generic bidirectional sync: **SQLite workable-items SSoT ↔ tracker docs ↔ remote
board (ClickUp)**. Deterministic, dry-run-first, never out of sync, never
corrupting/losing data, fully decoupled from any consumer (§11.4.28).

## 2. Components

```
consumer .env (CLICKUP_API_KEY/FOLDER/BOARD, DB path)   ClickUp Board (Folder→List→Tasks)
        │ injected at runtime, token never printed              ▲   │ webhooks (X-Signature HMAC)
        ▼                                                       │   ▼
 ┌───────────────────────────┐   push/pull          ┌────────────────────────┐
 │ task_bridge (Go)          │◀───────────────────▶ │ raksul/go-clickup (MIT) │
 │  pkg/config  (injection)  │   HTTP v2            └────────────────────────┘
 │  pkg/resolver(URL→ID probe)│
 │  pkg/mapper  (§5.3)        │   reads/writes
 │  pkg/syncengine (last-edit)│────────────────────▶ workable_items.db (consumer SSoT)
 │  pkg/webhook (HMAC verify) │                              │ md↔db
 └───────────────────────────┘                              ▼
        │ docs_chain exec transform              Issues/Fixed/Summary/**Deleted** docs
        ▼                                                    │ §11.4.65
   docs_chain (DAG, atomic, conflict-aware) ─────────────────┘ → HTML+PDF+DOCX
```

## 3. Packages (P1 contracts; bodies per phase)

- `pkg/config` — the decoupling boundary; the runtime injection struct + safe
  generic defaults (never-auto-delete, 10m cadence, dry-run ON).
- `pkg/client` — transport interface over MIT `raksul/go-clickup`; stubbed in P1.
- `pkg/resolver` — board/folder URL → ID via live API probe (no URL-grammar
  guessing, §11.4.6); stubbed in P1, wired P2.2.
- `pkg/mapper` — local item ↔ remote task (§5.3), two-call custom-field write; P3.
- `pkg/syncengine` — deterministic last-edit-wins outcomes (in-sync / push /
  pull / conflict / deleted), 2-phase idempotent commit; P5.
- `pkg/webhook` — `X-Signature` HMAC-SHA256-hex-of-raw-body verify + targeted
  reconcile; P6.

## 4. Conflict model (P0 §5.1)

Per item: `local_changed = hash != last_synced_hash`,
`remote_changed = date_updated > last_synced`. Disjoint changes auto-resolve by
direction; same-field collisions **STOP** and surface to the operator — never a
silent merge. Clock-skew-immune (per-source monotonic deltas vs each side's own
last-synced baseline, never a cross-host wall-clock compare).

## 5. Phasing

P1 (this) scaffold → P2 URL→ID + config → P3 schema mapping → P4 docs_chain
extension (Deleted docs) → P5 bidirectional sync + conflict + dry-run → P6
webhooks + cron → P7 MCP/Skill/Plugin → P8 tests (all types + danger-zones) →
P9 docs → P10 init sync (dry→real) + validation.
