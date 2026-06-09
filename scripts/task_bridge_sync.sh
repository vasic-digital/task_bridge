#!/usr/bin/env bash
# task_bridge_sync.sh — generic wrapper that runs one authoritative reconcile
# pass via the task_bridge binary.
#
# Purpose:     run `task_bridge reconcile` (the cron backstop, P0 §6) for the
#              consuming project, dry-run by default.
# Usage:       task_bridge_sync.sh [--apply]
# Inputs:      environment injected by the CONSUMER (NOT this engine):
#                CLICKUP_API_KEY   - personal token (never printed)        [§11.4.10]
#                CLICKUP_FOLDER    - folder URL (resolved to ID at runtime) [§1.6]
#                CLICKUP_BOARD     - board/list URL (resolved to ID)        [§1.6]
#                TASK_BRIDGE_DB    - path to the workable-items SQLite DB
# Outputs:     reconcile report on stdout; non-zero exit on transport/conflict.
# Side-effects: NONE by default (dry-run). With --apply: remote writes (gated).
# Dependencies: the task_bridge binary (go build ./cmd/task_bridge).
# Decoupling:  this script hardcodes ZERO project specifics (§11.4.28); it only
#              forwards consumer-injected env to the binary.
#
# P1 SCAFFOLD: forwards to the binary, which is itself stubbed until P5/P6.
set -euo pipefail

APPLY=""
[ "${1:-}" = "--apply" ] && APPLY="--apply"

# The binary reads CLICKUP_* + TASK_BRIDGE_DB from the environment.
# It never prints the token value (§11.4.10).
exec task_bridge reconcile ${APPLY}
