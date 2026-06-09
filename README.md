# task_bridge

**Revision:** 1
**Last modified:** 2026-06-09T00:00:00Z

A **generic, decoupled, bidirectional task/board sync engine** (Go). It keeps a
consuming project's **workable-items SQLite SSoT** ↔ its **tracker docs** ↔ a
remote **board** (first member: **ClickUp**; Jira/Linear are future members)
impeccably in sync — deterministic last-edit-wins, dry-run-first, never out of
sync, never corrupting or losing data.

This repository is a **submodule** consumed by other projects. It is
**project-agnostic** (constitution §11.4.28): it ships **zero** project-specific
values. Every credential, board/folder ID, item-key field, and DB path is
**injected by the consumer at runtime** through `pkg/config.Config`.

## Status — P1 scaffold

This is the **P1 scaffold**: module layout, package interfaces, CLI/daemon
entrypoints, the decoupling boundary (`pkg/config`), and tests are in place.
**No sync logic and no live ClickUp call are implemented yet** — every stub
returns an explicit not-implemented error (honest scaffolding per §11.4.27, not
a fake). Architecture + the full phased plan live in the consumer's
`docs/research/clickup_integration/P0_research_and_plan.md`.

## Layout

| Path | Purpose |
|---|---|
| `cmd/task_bridge/` | CLI entrypoint (`reconcile`/`push`/`pull`/`resolve`/`status`/`conflicts`/`init`) |
| `cmd/task_bridged/` | Long-running daemon (webhook receiver + cron reconcile) |
| `pkg/config/` | **Decoupling boundary** — the runtime injection contract |
| `pkg/client/` | Thin wrapper over MIT `raksul/go-clickup` (transport interface + stub) |
| `pkg/resolver/` | Board/folder URL → ID (live API probe, no URL-grammar guessing) |
| `pkg/mapper/` | Local workable-item ↔ remote task field mapping |
| `pkg/syncengine/` | Deterministic last-edit-wins reconciler (conflict outcomes) |
| `pkg/webhook/` | Live-event receiver (`X-Signature` HMAC-SHA256 verify) |
| `scripts/` | `task_bridge_sync.sh`, `task_bridge_init.sh` — consumer-facing wrappers |
| `docs/` | Architecture + API reference skeleton |

## Decoupling contract (§11.4.28)

`task_bridge` knows nothing about any specific project. A consumer wires it by
building a `config.Config` from its **own** environment and handing it to the
engine:

```go
cfg := config.Defaults()        // safe generic defaults (never-auto-delete, 10m, dry-run)
cfg.APIToken           = os.Getenv("CLICKUP_API_KEY")  // never logged (§11.4.10)
cfg.FolderURL          = os.Getenv("CLICKUP_FOLDER")
cfg.BoardURL           = os.Getenv("CLICKUP_BOARD")
cfg.DBPath             = os.Getenv("TASK_BRIDGE_DB")
cfg.ItemKeyCustomField = os.Getenv("TASK_BRIDGE_ITEM_KEY")  // e.g. "ATM_ID"
// ... construct client + mapper, then run the engine.
```

Copy `.env.example` to a **gitignored `.env` in the consuming project** (never in
this submodule) and fill in real values.

## Operator-decided defaults (encoded in `config.Defaults()`)

| Decision | Value |
|---|---|
| Submodule name | `task_bridge` (provider-generic) |
| Delete behaviour | **never auto-delete remote** — mark `Deleted` (§9 data-safety) |
| Item key | consumer-supplied custom field (e.g. `ATM_ID`) |
| Reconcile cadence | **10 min** cron behind live webhooks |
| Dry-run | **ON by default** (never pollute the board) |

## Build

```sh
go build ./...
go test  ./...
```

## Transport

ClickUp API v2 via the MIT-licensed
[`raksul/go-clickup`](https://github.com/raksul/go-clickup) library
(tasks, custom-fields, folders, lists, tags, members, webhooks).

## Governance

This submodule inherits the Helix Constitution (see `CLAUDE.md` / `AGENTS.md` /
`QWEN.md`). It is committed + pushed to **GitHub** and **GitLab** under
`vasic-digital`. License: MIT.
