// Command task_bridge is the CLI entrypoint for the generic task/board sync
// engine. P1 SCAFFOLD: argument parsing + subcommand dispatch stubs only — NO
// sync logic, NO live ClickUp calls. Real subcommand bodies land P2–P10.
//
// Subcommands (contract fixed in P1; bodies stubbed):
//
//	reconcile   one authoritative pass (cron backstop, default --dry-run)   [P5/P6.3]
//	push        local -> remote                                              [P5.2]
//	pull        remote -> local                                             [P5.1]
//	resolve     URL -> folder/list IDs (API probe, no guessing)             [P2.2]
//	status      print sync state (token-efficient structured output)        [P7.1]
//	conflicts   list same-field conflicts for the operator                  [P7.1]
//	init        dry-run-then-real init sync (statuses/types/tags)           [P10]
//
// DECOUPLING (§11.4.28): this binary reads its config from the ENVIRONMENT and
// flags supplied by the CONSUMER (e.g. CLICKUP_API_KEY / CLICKUP_FOLDER /
// CLICKUP_BOARD / DB path). It hardcodes nothing project-specific and never
// prints the token value (§11.4.10).
package main

import (
	"fmt"
	"os"
)

const usage = `task_bridge - generic bidirectional task/board sync engine (P1 scaffold)

usage: task_bridge <command> [flags]

commands:
  reconcile   one authoritative sync pass (default --dry-run)
  push        push local items to the remote board
  pull        pull remote tasks into the local SSoT
  resolve     resolve folder/board URLs to IDs (live API probe)
  status      print sync state
  conflicts   list same-field conflicts awaiting operator decision
  init        dry-run-then-real init sync (statuses/types/version-tags)

All project specifics (token, board/folder URLs, DB path) are injected via the
environment by the consuming project. The token value is never printed.

NOTE: P1 scaffold — commands are not yet implemented; each prints its phase.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
	case "reconcile":
		notImplemented(cmd, "P5/P6.3")
	case "push":
		notImplemented(cmd, "P5.2")
	case "pull":
		notImplemented(cmd, "P5.1")
	case "resolve":
		notImplemented(cmd, "P2.2")
	case "status":
		notImplemented(cmd, "P7.1")
	case "conflicts":
		notImplemented(cmd, "P7.1")
	case "init":
		notImplemented(cmd, "P10")
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "task_bridge: unknown command %q\n\n%s\n", cmd, usage)
		os.Exit(2)
	}
}

// notImplemented keeps the scaffold honest (§11.4.27): it states the phase that
// will implement the command and exits non-zero, never faking success.
func notImplemented(cmd, phase string) {
	fmt.Fprintf(os.Stderr, "task_bridge: %q not implemented in P1 scaffold (lands in %s)\n", cmd, phase)
	os.Exit(3)
}
