// Command task_bridge is the CLI entrypoint for the generic task/board sync
// engine.
//
// The `reconcile` subcommand is IMPLEMENTED for real (title-prefix mode): it
// reads the consumer's §11.4.93 items DB, lists the live ClickUp board, and
// produces the DIFF (UPDATE/CREATE/INVESTIGATE/DELETE/UNKEYED buckets). Dry-run
// is the DEFAULT (zero writes); `--apply` gates the real push. The other
// subcommands remain honest stubs until their phase.
//
// DECOUPLING (§11.4.28): all project specifics (token, list id, DB path) are
// injected via the ENVIRONMENT / flags by the CONSUMER. Nothing project-specific
// is hardcoded. The token value is NEVER printed (§11.4.10).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/localstore"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
	"github.com/vasic-digital/task_bridge/pkg/syncengine"
)

const usage = `task_bridge - generic bidirectional task/board sync engine

usage: task_bridge <command> [flags]

commands:
  reconcile   DIFF the local items DB against the live board (dry-run default)
  push        push local items to the remote board                    [stub]
  pull        pull remote tasks into the local SSoT                   [stub]
  resolve     resolve folder/board URLs to IDs (live API probe)       [stub]
  status      print sync state                                        [stub]
  conflicts   list same-field conflicts awaiting operator decision    [stub]
  init        dry-run-then-real init sync                             [stub]

reconcile flags:
  --list <id>    ClickUp list id      (or env CLICKUP_LIST_ID)
  --db <path>    workable-items DB     (or env TASK_BRIDGE_DB)
  --out <path>   write the JSON diff artifact to this path
  --prefix <csv> comma-separated key prefixes to include, e.g. "ATM" or
                 "MVR,SPK" (or env TASK_BRIDGE_PREFIX). Empty = no filtering
                 (every local item is in scope for this list). REQUIRED
                 whenever more than one board/list is wired against the SAME
                 local items DB — without it every board receives a copy of
                 EVERY item regardless of context (multi-board fan-out must be
                 scoped per consumer, never assumed by this generic engine).
  --apply        GATED real push (create + status-update). Default: dry-run.

env (injected by the consumer; token value is never printed):
  CLICKUP_API_KEY    personal token (required)
  CLICKUP_LIST_ID    target list id (or --list)
  TASK_BRIDGE_DB     path to the items DB (or --db)
  TASK_BRIDGE_PREFIX comma-separated key-prefix filter (or --prefix)`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "reconcile":
		os.Exit(runReconcile(os.Args[2:]))
	case "push":
		notImplemented("push", "P5.2")
	case "pull":
		notImplemented("pull", "P5.1")
	case "resolve":
		notImplemented("resolve", "P2.2")
	case "status":
		notImplemented("status", "P7.1")
	case "conflicts":
		notImplemented("conflicts", "P7.1")
	case "init":
		notImplemented("init", "P10")
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "task_bridge: unknown command %q\n\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
}

func runReconcile(args []string) int {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	listID := fs.String("list", os.Getenv("CLICKUP_LIST_ID"), "ClickUp list id")
	dbPath := fs.String("db", os.Getenv("TASK_BRIDGE_DB"), "workable-items DB path")
	outPath := fs.String("out", "", "write the JSON diff artifact here")
	prefixCSV := fs.String("prefix", os.Getenv("TASK_BRIDGE_PREFIX"), "comma-separated key-prefix filter (e.g. ATM or MVR,SPK); empty = no filtering")
	apply := fs.Bool("apply", false, "GATED real push (default: dry-run)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	token := os.Getenv("CLICKUP_API_KEY") // NEVER printed (§11.4.10)
	if token == "" {
		fmt.Fprintln(os.Stderr, "reconcile: CLICKUP_API_KEY not set (injected by consumer, never printed)")
		return 2
	}
	if *listID == "" {
		fmt.Fprintln(os.Stderr, "reconcile: no list id (--list or CLICKUP_LIST_ID)")
		return 2
	}
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "reconcile: no DB path (--db or TASK_BRIDGE_DB)")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	local, err := localstore.LoadItems(ctx, *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: load local items: %v\n", err)
		return 1
	}
	allLocalCount := len(local)
	local = filterByPrefix(local, *prefixCSV)
	if *prefixCSV != "" {
		fmt.Printf("prefix filter %q: %d of %d local items in scope for this list\n", *prefixCSV, len(local), allLocalCount)
	}
	cl := client.NewClickUp(token)
	remote, err := syncengine.FetchAllRemote(ctx, cl, *listID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: fetch remote tasks: %v\n", err)
		return 1
	}

	plan := syncengine.PlanReconcile(local, remote)
	printReport(*listID, len(local), len(remote), plan, *apply)

	if *outPath != "" {
		if err := writeArtifact(*outPath, *listID, len(local), len(remote), plan); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: write artifact: %v\n", err)
			return 1
		}
		fmt.Printf("\ndiff artifact: %s\n", *outPath)
	}

	if *apply {
		fmt.Println("\n--apply: performing GATED push (create + status-update; never delete)…")
		res, err := syncengine.Apply(ctx, cl, mapper.New(), *listID, plan, local)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: apply: %v\n", err)
			return 1
		}
		fmt.Printf("apply: created=%d updated=%d errors=%d\n", res.Created, res.Updated, len(res.Errors))
		for _, e := range res.Errors {
			fmt.Fprintf(os.Stderr, "  apply-error: %s\n", e)
		}
		// N2 / §11.4.1: per-item push failures MUST surface as a non-zero exit —
		// Apply collects them WITHOUT returning a top-level error, so the CLI is
		// the layer that must NOT report silent partial success.
		return exitForApply(res)
	}
	return 0
}

// exitForApply maps a gated-push result to the process exit code: any collected
// per-item error (Apply returns them without a top-level error) makes the exit
// NON-ZERO, so a partial push failure can never be mistaken for success
// (N2 / §11.4.1). Pure + side-effect-free so it is unit-testable.
func exitForApply(res syncengine.ApplyResult) int {
	if len(res.Errors) > 0 {
		return 1
	}
	return 0
}

func printReport(listID string, localN, remoteN int, plan syncengine.Plan, apply bool) {
	mode := "DRY-RUN (no writes)"
	if apply {
		mode = "APPLY (gated push)"
	}
	c := plan.Counts()
	fmt.Printf("task_bridge reconcile — %s\n", mode)
	fmt.Printf("list=%s  local_items=%d  remote_tasks=%d\n", listID, localN, remoteN)
	fmt.Println("buckets:")
	fmt.Printf("  UPDATE      %5d  (local item matched a remote task)\n", c["UPDATE"])
	fmt.Printf("  CREATE      %5d  (local item, no remote task)\n", c["CREATE"])
	fmt.Printf("  INVESTIGATE %5d  (keyed remote task, no local item — never delete)\n", c["INVESTIGATE"])
	fmt.Printf("  DELETE      %5d  (always 0 — never-auto-delete-remote)\n", c["DELETE"])
	fmt.Printf("  UNKEYED     %5d  (remote task with no [XXX-NNN] key — operator decision)\n", c["UNKEYED"])

	// INVESTIGATE is the small keyed-orphan bucket — list them all.
	if len(plan.Investigate) > 0 {
		fmt.Println("\nINVESTIGATE (keyed remote orphans):")
		for _, e := range plan.Investigate {
			fmt.Printf("  %-12s task=%s  %q\n", e.Key, e.TaskID, e.RemoteName)
		}
	}
	if len(plan.Unkeyed) > 0 {
		fmt.Printf("\nUNKEYED (%d remote tasks with no recognized key) — first 10:\n", len(plan.Unkeyed))
		for i, e := range plan.Unkeyed {
			if i >= 10 {
				fmt.Printf("  … and %d more\n", len(plan.Unkeyed)-10)
				break
			}
			fmt.Printf("  task=%s  %q\n", e.TaskID, e.RemoteName)
		}
	}
	// Show a small UPDATE status-drift sample (real field-level diff).
	if drift := driftSample(plan.Update, 8); len(drift) > 0 {
		fmt.Println("\nUPDATE status-drift sample:")
		for _, e := range drift {
			fmt.Printf("  %-12s %s\n", e.Key, e.Detail)
		}
	}
}

func driftSample(update []syncengine.PlanEntry, n int) []syncengine.PlanEntry {
	var out []syncengine.PlanEntry
	for _, e := range update {
		if !e.InSync {
			out = append(out, e)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

type artifact struct {
	GeneratedAt string          `json:"generated_at"`
	ListID      string          `json:"list_id"`
	LocalItems  int             `json:"local_items"`
	RemoteTasks int             `json:"remote_tasks"`
	Counts      map[string]int  `json:"counts"`
	Plan        syncengine.Plan `json:"plan"`
	Note        string          `json:"note"`
}

func writeArtifact(path, listID string, localN, remoteN int, plan syncengine.Plan) error {
	a := artifact{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		ListID:      listID,
		LocalItems:  localN,
		RemoteTasks: remoteN,
		Counts:      plan.Counts(),
		Plan:        plan,
		Note:        "title-prefix reconcile; DELETE always 0 (never-auto-delete-remote); INVESTIGATE + UNKEYED are operator decisions",
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// filterByPrefix restricts items to those whose Key starts with one of the
// prefixes in prefixCSV (case-insensitive, whitespace-tolerant, comma-
// separated). An empty prefixCSV performs NO filtering (returns items
// unchanged) — the caller decides whether filtering applies; this function
// never invents a default scope (§11.4.6). A non-empty prefixCSV with zero
// matches returns an EMPTY slice, never falls back to "all items" — silently
// pushing every item because a typo'd prefix matched nothing would be exactly
// the cross-board duplication this filter exists to prevent.
//
// This is the mechanism that makes MULTIPLE per-context boards safe against
// the SAME local items DB (operator mandate, §11.4.101): without it, every
// configured board would receive a create-push of every local item regardless
// of context. The engine stays fully generic (§11.4.28) — it knows nothing
// about which prefixes exist or what they mean; the consumer supplies the set.
func filterByPrefix(items []mapper.LocalItem, prefixCSV string) []mapper.LocalItem {
	prefixCSV = strings.TrimSpace(prefixCSV)
	if prefixCSV == "" {
		return items
	}
	// Genuine key shape only (PREFIX-NNN, anchored both ends) — mirrors
	// pkg/mapper's keyInTitle discriminator so e.g. "ATM-DERIVED-9" (a
	// non-digit follows the dash) is never mistaken for a real ATM item.
	var patterns []*regexp.Regexp
	for _, p := range strings.Split(prefixCSV, ",") {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p != "" {
			patterns = append(patterns, regexp.MustCompile(`^`+regexp.QuoteMeta(p)+`-\d+$`))
		}
	}
	out := make([]mapper.LocalItem, 0, len(items))
	for _, it := range items {
		key := strings.ToUpper(it.Key)
		for _, re := range patterns {
			if re.MatchString(key) {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

func notImplemented(cmd, phase string) {
	fmt.Fprintf(os.Stderr, "task_bridge: %q not implemented (lands in %s)\n", cmd, phase)
	os.Exit(3)
}
