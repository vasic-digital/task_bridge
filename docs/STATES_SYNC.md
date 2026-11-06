# task_bridge - state-field sync

**Revision:** 1
**Last modified:** 2026-11-03
**Scope:** how a consumer's extra tracker fields (closed-vocabulary states and free-text details) are carried from the local items table to a remote board. Generic and project-agnostic (§11.4.28): the engine ships no field names and no vocabulary; the consumer supplies a JSON spec. The consumer's own runbook lives in the consumer repository.

## Spec

```json
{ "version": 1, "block_id": "TB-STATES",
  "fields": [
    {"key": "reproduction_state", "kind": "enum", "values": ["UNSET", "..."], "unset": "UNSET"},
    {"key": "reproduction_env",   "kind": "text", "max_runes": 1000} ] }
```

- `key` is the items-table column and the canonical remote name (`^[a-z][a-z0-9_]*$`).
- `enum`: a closed vocabulary, `unset` names the member that means "no state". Members match `^[A-Za-z0-9][A-Za-z0-9._-]*$`.
- `text`: free text, trimmed, CR/CRLF folded to LF, control characters other than LF and TAB refused, truncated to `max_runes` (default 8000, reported as `CLAMPED:<key>`).
- Unknown JSON members, duplicate keys, a version other than 1 and every structural error are refused (`ErrSpec`).

An empty or NULL enum is the `unset` member. Any other token outside the vocabulary is refused, never mapped to the nearest member or to `unset`.

## Carriers

| Carrier | Chosen when | Form |
|---|---|---|
| FIELD | the list exposes a custom field whose normalised name equals the key (lower case, spaces and hyphens folded to `_`), exactly one such field, the right type (`drop_down` carrying every non-unset member as an option for an enum; `text` or `short_text` for text) | drop-down set by option id; text as a string; unset / empty = value cleared |
| BLOCK | otherwise, or `--carrier block` | managed block in the description |

`--carrier auto` (default) decides per field; `--carrier field` fails closed (exit 2, before any write) if any field cannot use a custom field. A field with a wrong type, missing options or a duplicate name is reported (`TYPE_MISMATCH`, `OPTIONS_DRIFT`, `AMBIGUOUS`) and never re-created; its key stays on the block.

### Description block

```
<base description>

[TB-STATES:BEGIN v1] managed by task_bridge from the tracker database; edit it there, not here
reproduction_state: reproduced-live
reproduction_red_cmd: bash a.sh\nsecond line
[TB-STATES:END]
```

One line per non-empty field, in spec order. In a value `\` is `\\`, LF is `\n`, TAB is `\t`; nothing else is escaped, so a value can never form a marker line. The parser is tolerant of a Markdown round trip (a backslash before ASCII punctuation, escaped markers and keys, CRLF, NBSP, trailing blanks).

- The base description is never edited. The block is appended after one blank line, replaced in place, or removed when every field is unset.
- Equality is semantic (parsed values), never byte equality, so a service that re-serialises the block does not cause a perpetual rewrite. Duplicate regions, foreign lines, unknown keys and lines of a key that moved to a custom field make the block rewrite (collapsed to one).
- A BEGIN without END, a nested BEGIN or a stray END is `BLOCK_MALFORMED`: the description is not touched (exit 4).
- A task whose Markdown source was not returned (only a rendered plain description) is `DESC_SOURCE_UNAVAILABLE`: not touched (exit 4). A task with no description at all is writable.
- `--states-materialize-unset` also writes `key: UNSET` / `key:` lines (default off, so a board with 1500 untouched items stays untouched).

## reconcile flags and exit codes

`--states-spec`, `--carrier`, `--create-missing-fields`, `--max-field-creates`, `--states-only`, `--states-strict`, `--states-materialize-unset`, `--no-readback`, `--states-report-max` (and the env names in `.env.example`). Without a spec the run is the original one.

Scoping and duplicate flags (independent of the states feature; all off by default, so a run without them is the original one):

- `--keys ATM-1,ATM-2` (env `TASK_BRIDGE_KEYS`): only these items are created or updated, whatever else the DB holds or the board shows. It applies after `--prefix`. A key that is not in scope (absent from the DB, outside `--prefix`) or not of the shape `PREFIX-NNN` is a usage error (exit 2) before any remote call, never silently dropped.
- `--desc-marker REGEXP` (env `TASK_BRIDGE_DESC_MARKER`): a regular expression with exactly one capture group, the consumer's description version marker (for example a content-hash line). When the local description carries a marker and the remote description lacks it or carries another value, the task is `description-drift`: `--apply` rewrites name and description (never status or labels; the managed state block is re-added in the same pass; `--states-only` never rewrites the description). Idempotent: once the marker matches the plan is in sync.
- DUPLICATE: when two or more remote tasks carry the same `[KEY]` of a local item, all of them are listed under `DUPLICATE` (plan artifact member `duplicate`), none is updated, no further task is created for the key, nothing is deleted, and the run exits 6. Which task stays is an operator decision. Keys that no local item owns stay `INVESTIGATE`.

| Exit | Meaning |
|---|---|
| 0 | done |
| 1 | fetch failure or a per-item write / read-back failure |
| 2 | usage or configuration (bad spec, `--carrier field` unmet, `--states-strict` with columns absent) |
| 4 | a state part was refused: value outside its vocabulary, malformed remote block, or no Markdown source |
| 5 | the remote rejected the credentials (HTTP 401/403); the batch stops at once |
| 6 | two or more remote tasks carry the same item key (`DUPLICATE`); nothing is written for those keys |

A DB without the spec's columns prints `STATES DISABLED` and syncs without states (it is never read as "all unset", which would wipe existing remote state).

## Safety properties (each has a named test and a mutant)

- Dry-run default; every write sits behind `--apply`.
- Custom-field creation needs `--create-missing-fields` AND `--apply`, is capped per list, re-lists before each create, never re-creates a mismatched or duplicate-named field, and ends on a 404/405/501.
- No task is deleted; the only DELETE calls clear a custom-field value or a tag.
- Idempotence: apply twice = zero writes (unit, property and end-to-end).
- Every state write is read back; a diff that does not converge is an error (`READBACK_MISMATCH`).
- `--states-only --apply` writes the state part of existing tasks and nothing else (no create, status, title or label update).
- The token is passed to the transport only; it never reaches stdout, stderr, the apply log or an error string (asserted in the end-to-end suite).

## Tests

| Layer | Where |
|---|---|
| unit, golden, property (random strings through identity / Markdown / sloppy channels; apply twice = zero diff) | `go test ./...` (`pkg/states`, `pkg/syncengine`, `pkg/client`, `pkg/localstore`, `cmd/task_bridge`) |
| offline mock ClickUp server with fault injection (401, 429, 500, partial pages, lossy Markdown, ignored writes, field-create 404/400) | `scripts/mock_clickup_server.py` (self-test `scripts/test_mock_clickup_server.py`) |
| end-to-end through the real binary and a real SQLite DB | `python3 scripts/e2e_states.py` |
| paired mutation proof (about 100 mutants, each killed by a named test or scenario) | `python3 scripts/mutate_states.py` |

## Not confirmed against the live service

The `markdown_description` round trip, `include_markdown_description`, the `last_page` member, whether a drop-down value is returned as orderindex or option id (both handled), the custom-field text length limits, and the existence of a create-field endpoint. Each is isolated in `pkg/client/states_http.go`. Run the first live apply as `--states-only --apply --limit 1` and inspect the ticket.
