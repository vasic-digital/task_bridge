#!/usr/bin/env bash
# task_bridge_init.sh — init-sync (dry-run THEN real) wrapper (P0 §10).
#
# Purpose:     map column names / statuses / types / version-tag labels with the
#              SAME ordering/attributes to the ClickUp board, validate vs the
#              local System, then (only on --apply) do the full bidirectional
#              data sync. Dry-run by default (never pollute the board, DZ-11).
# Usage:       task_bridge_init.sh [--apply]
# Inputs:      consumer-injected CLICKUP_API_KEY / CLICKUP_FOLDER / CLICKUP_BOARD
#              / TASK_BRIDGE_DB (same contract as task_bridge_sync.sh).
# Outputs:     init diff report on stdout.
# Side-effects: NONE by default; with --apply: creates status/type/tag scaffold
#              + full data sync (gated).
# Dependencies: the task_bridge binary.
# Decoupling:  ZERO project specifics (§11.4.28).
#
# P1 SCAFFOLD: forwards to the (stubbed) binary; real init lands in P10.
set -euo pipefail

APPLY=""
[ "${1:-}" = "--apply" ] && APPLY="--apply"

exec task_bridge init ${APPLY}
