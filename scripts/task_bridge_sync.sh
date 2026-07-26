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

# Resolve the task_bridge binary WITHOUT requiring a system-PATH install
# (decoupled, §11.4.28): TASK_BRIDGE_BIN override -> the repo-local build next
# to this script (tools/task_bridge/bin/) -> a PATH install.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [ -n "${TASK_BRIDGE_BIN:-}" ] && [ -x "${TASK_BRIDGE_BIN}" ]; then
	TB="${TASK_BRIDGE_BIN}"
elif [ -x "${SCRIPT_DIR}/../bin/task_bridge" ]; then
	TB="${SCRIPT_DIR}/../bin/task_bridge"
elif command -v task_bridge >/dev/null 2>&1; then
	TB="task_bridge"
else
	echo "task_bridge_sync.sh: task_bridge binary not found (build: go build -o tools/task_bridge/bin/task_bridge ./cmd/task_bridge)" >&2
	exit 3
fi

# The binary reads CLICKUP_API_KEY / CLICKUP_LIST_ID / TASK_BRIDGE_DB from the
# environment (consumer-injected). It never prints the token value (§11.4.10).
exec "$TB" reconcile ${APPLY}
