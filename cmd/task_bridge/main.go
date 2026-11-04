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
	"github.com/vasic-digital/task_bridge/pkg/states"
	"github.com/vasic-digital/task_bridge/pkg/syncengine"
)

const usage = `task_bridge - generic bidirectional task/board sync engine

usage: task_bridge <command> [flags]

commands:
  reconcile   DIFF the local items DB against the live board (dry-run default)
  push        (placeholder; use "reconcile --apply" for the real push)  [stub]
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
  --apply        GATED real push (create + status/label/title update; never
                 delete). Default: dry-run. This is the REAL push path; the
                 separate "push" subcommand below is a placeholder only.
  --limit <n>    with --apply: touch at most n actionable items (CREATE +
                 drifted UPDATE) this run; the rest stay for the next batch.
  --newest-first with --apply: order actionable items by descending key number.
  --stop-on-error with --apply: halt after the first failing item (exit 1).
  --apply-log <path>  with --apply: append one JSON line per item outcome
                 (op, key, task_id, ok, err) — never contains credentials.
  --min-interval-ms <n>  minimum spacing between ClickUp requests (default 700;
                 HTTP 429 is retried honouring Retry-After).

state-field flags (the consumer-defined tracker states; off unless --states-spec):
  --states-spec <path>  JSON spec of the state fields (keys, closed vocabularies);
                 or env TASK_BRIDGE_STATES_SPEC. The values are read from the
                 same-named columns of the items table.
  --carrier <m>  auto (default: a custom field where the list has one, else a
                 managed description block) | block | field (fail closed if any
                 field is missing). Or env TASK_BRIDGE_STATES_CARRIER.
  --create-missing-fields  with --apply: try to create the missing custom fields
                 (guarded, capped, listed; falls back to the block when the remote
                 offers no such endpoint). Or env TASK_BRIDGE_CREATE_FIELDS=1.
  --max-field-creates <n>  cap on creations per list (default: the spec size).
  --states-only  with --apply: write ONLY state fields of existing tasks (no
                 create, no status/title/label update) - the staged roll-out switch.
  --states-strict  exit 2 when a spec column is absent from the DB (default: the
                 state part is disabled with a loud notice).
  --states-materialize-unset  also write UNSET / empty lines into the block.
  --no-readback  skip the verifying re-read after a state write.
  --states-report-max <n>  per-task state diff lines printed (default 200).

exit codes: 0 ok | 1 fetch/apply failure | 2 usage or configuration | 4 a local
state value was refused (outside its closed vocabulary) or a remote block could
not be rewritten safely | 5 the remote rejected the credentials.

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
	limit := fs.Int("limit", 0, "with --apply: max actionable items this run (0 = unlimited)")
	newestFirst := fs.Bool("newest-first", false, "with --apply: order by descending key number")
	stopOnErr := fs.Bool("stop-on-error", false, "with --apply: halt after the first failing item")
	applyLog := fs.String("apply-log", "", "with --apply: append per-item JSONL outcomes here")
	minIntervalMS := fs.Int("min-interval-ms", int(client.DefaultMinInterval/time.Millisecond), "minimum ms between ClickUp requests")
	specPath := fs.String("states-spec", os.Getenv("TASK_BRIDGE_STATES_SPEC"), "JSON spec of the state fields")
	carrierFlag := fs.String("carrier", envOr("TASK_BRIDGE_STATES_CARRIER", "auto"), "state carrier: auto|block|field")
	createFields := fs.Bool("create-missing-fields", os.Getenv("TASK_BRIDGE_CREATE_FIELDS") == "1", "with --apply: create missing custom fields (guarded)")
	maxCreates := fs.Int("max-field-creates", 0, "cap on custom-field creations per list (0 = spec size)")
	statesOnly := fs.Bool("states-only", false, "with --apply: write only state fields of existing tasks")
	statesStrict := fs.Bool("states-strict", false, "exit 2 when a spec column is absent from the DB")
	materialize := fs.Bool("states-materialize-unset", false, "also write UNSET / empty lines into the block")
	noReadback := fs.Bool("no-readback", false, "skip the verifying re-read after a state write")
	reportMax := fs.Int("states-report-max", 200, "per-task state diff lines printed")
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

	var spec *states.Spec
	var extraCols []string
	if *specPath != "" {
		raw, err := os.ReadFile(*specPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: read states spec: %v\n", err)
			return 2
		}
		if spec, err = states.LoadSpec(raw); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
			return 2
		}
		extraCols = spec.Keys()
	} else if *statesOnly || *createFields {
		fmt.Fprintln(os.Stderr, "reconcile: --states-only / --create-missing-fields need --states-spec")
		return 2
	}
	mode, err := states.ParseMode(*carrierFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		return 2
	}
	local, absent, err := localstore.LoadItemsExtra(ctx, *dbPath, extraCols)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: load local items: %v\n", err)
		return 1
	}
	if spec != nil && len(absent) > 0 {
		fmt.Printf("STATES DISABLED: the items table has no column(s) %s (the state columns have not landed in this DB); the run continues WITHOUT state fields\n", strings.Join(absent, ","))
		if *statesStrict || *statesOnly {
			fmt.Fprintln(os.Stderr, "reconcile: state columns absent and --states-strict / --states-only given")
			return 2
		}
		spec = nil
	}
	allLocalCount := len(local)
	local = filterByPrefix(local, *prefixCSV)
	if *prefixCSV != "" {
		fmt.Printf("prefix filter %q: %d of %d local items in scope for this list\n", *prefixCSV, len(local), allLocalCount)
	}
	cl := client.NewClickUpWithOptions(token, client.Options{
		BaseURL:     os.Getenv("TASK_BRIDGE_BASE_URL"), // tests/proxies only; empty = production
		MinInterval: time.Duration(*minIntervalMS) * time.Millisecond,
		StatesMode:  spec != nil,
	})
	var sc *syncengine.StatesContext
	var prov syncengine.ProvisionReport
	if spec != nil {
		scl, ok := cl.(client.StatesClient)
		if !ok {
			fmt.Fprintln(os.Stderr, "reconcile: the transport does not support state fields")
			return 2
		}
		sc, prov, err = syncengine.PrepareStates(ctx, scl, *listID, spec, syncengine.PrepareOptions{
			Mode: mode, CreateMissing: *createFields, Apply: *apply, MaxCreates: *maxCreates, MaterializeUnset: *materialize,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: prepare state fields: %v\n", err)
			if client.IsAuth(err) {
				return exitAuth
			}
			printProvision(prov)
			return 2
		}
	}
	remote, err := syncengine.FetchAllRemote(ctx, cl, *listID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: fetch remote tasks: %v\n", err)
		if client.IsAuth(err) {
			return exitAuth
		}
		return 1
	}

	plan := syncengine.PlanReconcileWith(local, remote, syncengine.PlanOptions{States: sc})
	printReport(*listID, len(local), len(remote), plan, *apply)
	if sc != nil {
		printProvision(prov)
		printStates(plan, *reportMax)
	}

	if *outPath != "" {
		if err := writeArtifact(*outPath, *listID, len(local), len(remote), plan, sc, prov); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: write artifact: %v\n", err)
			return 1
		}
		fmt.Printf("\ndiff artifact: %s\n", *outPath)
	}

	if *apply {
		fmt.Println("\n--apply: performing GATED push (create + status-update; never delete)…")
		opts := syncengine.ApplyOptions{Limit: *limit, StopOnError: *stopOnErr, NewestFirst: *newestFirst,
			States: sc, StatesOnly: *statesOnly, VerifyReadback: sc != nil && !*noReadback}
		var logf *os.File
		if *applyLog != "" {
			f, err := os.OpenFile(*applyLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "reconcile: open apply-log: %v\n", err)
				return 1
			}
			defer f.Close()
			logf = f
			opts.OnEvent = func(ev syncengine.ApplyEvent) {
				b, _ := json.Marshal(struct {
					TS     string `json:"ts"`
					ListID string `json:"list_id"`
					syncengine.ApplyEvent
				}{time.Now().UTC().Format(time.RFC3339), *listID, ev})
				_, _ = logf.Write(append(b, '\n'))
			}
		}
		res, err := syncengine.ApplyWith(ctx, cl, mapper.New(), *listID, plan, local, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: apply: %v\n", err)
			return 1
		}
		fmt.Printf("apply: created=%d updated=%d errors=%d attempted=%d remaining=%d stopped=%v\n",
			res.Created, res.Updated, len(res.Errors), res.Attempted, res.Remaining, res.Stopped)
		for _, e := range res.Errors {
			fmt.Fprintf(os.Stderr, "  apply-error: %s\n", e)
		}
		// N2 / §11.4.1: per-item push failures MUST surface as a non-zero exit —
		// Apply collects them WITHOUT returning a top-level error, so the CLI is
		// the layer that must NOT report silent partial success.
		if res.AuthRejected {
			fmt.Fprintln(os.Stderr, "reconcile: the remote rejected the credentials (HTTP 401/403); batch stopped")
			return exitAuth
		}
		if rc := exitForApply(res); rc != 0 {
			return rc
		}
	}
	return exitForStates(plan)
}

// exitAuth is the exit code of a credentials rejection; exitStatesRefused the
// code of a refused state value or an unsafe remote block. They are distinct so
// a wrapper can tell "fix the token" from "fix the data" from "retry".
const (
	exitAuth          = 5
	exitStatesRefused = 4
)

// exitForStates is non-zero when the plan holds a refused state part: a local
// value outside its closed vocabulary, a malformed remote block, or a task that
// returned only a plain description. Pure so it is unit-testable.
func exitForStates(plan syncengine.Plan) int {
	if plan.StateCounts().Refused > 0 {
		return exitStatesRefused
	}
	return 0
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
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
	States      *statesArtifact `json:"states,omitempty"`
}

type statesArtifact struct {
	Provision syncengine.ProvisionReport `json:"provision"`
	Counts    syncengine.StateCounts     `json:"counts"`
}

func writeArtifact(path, listID string, localN, remoteN int, plan syncengine.Plan, sc *syncengine.StatesContext, prov syncengine.ProvisionReport) error {
	a := artifact{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		ListID:      listID,
		LocalItems:  localN,
		RemoteTasks: remoteN,
		Counts:      plan.Counts(),
		Plan:        plan,
		Note:        "title-prefix reconcile; DELETE always 0 (never-auto-delete-remote); INVESTIGATE + UNKEYED are operator decisions",
	}
	if sc != nil {
		a.States = &statesArtifact{Provision: prov, Counts: plan.StateCounts()}
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

// printProvision reports how each state field is carried on this list and which
// custom fields an operator would have to provision (or a guarded create made).
func printProvision(p syncengine.ProvisionReport) {
	fmt.Printf("\nstate fields - carrier mode %s:\n", p.Mode)
	for _, r := range p.Resolutions {
		extra := ""
		if r.Reason != "" {
			extra = "  (" + r.Reason + ")"
		}
		fmt.Printf("  %-26s %-5s %s%s\n", r.Key, r.Carrier, r.Status, extra)
	}
	if len(p.WouldCreate) > 0 {
		fmt.Printf("  DRY-RUN: --create-missing-fields with --apply WOULD try to create: %s\n", strings.Join(p.WouldCreate, ", "))
	}
	if len(p.Created) > 0 {
		fmt.Printf("  CREATED custom fields: %s\n", strings.Join(p.Created, ", "))
	}
	if p.CreateUnsupported {
		fmt.Println("  FIELD_CREATE_UNSUPPORTED: the remote offers no custom-field creation; the missing keys use the description block")
	}
	for _, e := range p.CreateErrors {
		fmt.Printf("  FIELD_CREATE_ERROR: %s\n", e)
	}
	if len(p.ToProvision) > 0 {
		fmt.Println("  to use the FIELD carrier an operator provisions these custom fields on the list (exact name, type, options):")
		for _, pr := range p.ToProvision {
			opt := ""
			if len(pr.Options) > 0 {
				opt = "  options: " + strings.Join(pr.Options, ", ")
			}
			fmt.Printf("    %-26s %-9s%s\n", pr.Key, pr.Type, opt)
		}
	}
}

// printStates prints the per-task state diff (dry-run default) and the totals.
func printStates(plan syncengine.Plan, max int) {
	c := plan.StateCounts()
	fmt.Printf("\nstate-field diff: tasks_with_drift=%d create_rows_with_state=%d field_ops=%d block_ops=%d refused=%d (invalid_value=%d malformed_block=%d no_markdown_source=%d)\n",
		c.Drift, c.OnCreate, c.FieldOps, c.BlockOps, c.Refused, c.Invalid, c.Malformed, c.NoSource)
	lines, omitted := plan.StateReportLines(max)
	for _, l := range lines {
		fmt.Println(l)
	}
	if omitted > 0 {
		fmt.Printf("  ... and %d more task(s) (the full list is in the --out artifact)\n", omitted)
	}
}
