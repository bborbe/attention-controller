---
status: approved
approved: "2026-11-16T22:54:16Z"
branch: dark-factory/who-needs-me-data-endpoint
---

## Summary

- `attention-controller` exposes who-needs-me's answer as **joined JSON data**, so a manager reads it from the long-running service instead of spawning `who-needs-me.py` on every tick.
- The service already holds every input — the store, the pane lister, the registry reader, the provenance resolver, the transcript reader — so this adds one read route over them and no new client.
- The answer is **data, not a rendering**: per-item structured fields, the registry's sessions with ages, and the live pane listing. Each caller formats its own rows.
- A stopped store does not blind a manager: the route folds the hook-written `<sid>.events.jsonl` files, names that source on every row, and reports a per-source status so "unreadable" never reads as "empty".
- Parity is evidenced against seeded stores and generated exact-value fixtures, so an endpoint that echoes a stub cannot pass.

## Problem

`who-needs-me.py` is the single largest CPU consumer on this machine — re-measured 2026-11-16 at **2.70 cores average over ten samples**, two instances live — and it is spawned afresh on every manager tick. A bare `python3 -c pass` costs 0.57 s, while one `who-needs-me.py --section needs-you` did not finish within 90 s on three consecutive attempts, so the cost is not interpreter start-up: it is the per-invocation work of re-reading the store, the pane list, the registry and the event logs from scratch. The same work is already done once, and cached, inside `attention-controller`, which owns all five inputs.

The vault task *Who-Needs-Me Stops Spawning a Python Interpreter Every Tick* records the measurements, the operator ruling (board card `4ded38ffed73c7b918793882cb12e101`, option 1: **data endpoint, caller formats**) and the caller inventory. This spec is the `attention-controller` half; the `bborbe/claude-supervisor` call-site migration is a separate change in that repo.

The operator ruling fixes the fork that made the task unbuildable: the store may not render, and the callers may not stop needing the answer. So the endpoint returns the joined **fields** and the rendering stays in each caller.

## Goal

`GET /api/1.0/who-needs-me` returns, as `application/json`, the open attention items joined to their provenance, the session registry with transcript ages, the live pane listing, and a per-source status block — the data the five in-scope `who-needs-me.py` sub-commands (`--section` / `--all` / `--stuck-min`, `--ages`, `--pane-for`) derive — so a caller answers the same question without spawning the script.

## Non-goals

- **The `bborbe/claude-supervisor` call sites.** The seven literal and six runtime-built spawn sites in the task's SC4 are a separate PR in that repo. This spec makes the endpoint exist; it does not migrate a caller.
- **`--jump`.** Activating a WezTerm pane is an arm, and this repo's charter forbids arms. Its home stays `scripts/jump.py`.
- **`--resolve-owners`.** It writes `causes.json` from a `vault-cli task search` — a state-writing maintenance job, not a store read.
- **Re-deciding what the answer is.** Which gates count, how they rank and what a section means are the reader's existing policy. This moves the read; it does not change the classification.
- **Retiring `who-needs-me.py`.** It stays until no caller outside the managers needs it; the retirement is a follow-up.
- **The nuke-prod store instance.** This is the local Mac board only.

## Assumptions

- `store.Read(ctx)` is the population source: it drops expired items and resolves producer liveness server-side as a side effect of the read.
- `ProvenanceResolver.Resolve(ctx, items)` is the one join from an item to its event log, pane ownership, registry name, task/goal/topic, headless mode and worker coverage.
- `readSessionNames(ctx, dir)` is the one session-registry reader; `TranscriptReader.Path(sid)` locates a transcript whose mtime is the session's age.
- The existing event-log decoder (`decodeEventRecord` / the `EventIngestRecord` shape, `pkg/event-ingest.go`) is the one parser of a `<sid>.events.jsonl` line; the fallback reuses it rather than adding a second.
- The board router is the only listener that serves this route: the cluster-reachable listener has no WezTerm, no registry and no vault.
- The managers read `http://localhost:18080` — the local board — not the cluster.

## Alternatives

- **A Go CLI that re-reads everything per tick.** Rejected: it inherits the cost profile the measurement falsified ("skip the spawn" is not the win; "do the work once in a process that holds the inputs" is).
- **Extend `GET /api/1.0/attention`.** Rejected: that route returns the raw `Item` shape many arms already parse; a new route is a projection, not a mutation of the store's read.
- **Return a rendered feed string in a JSON envelope.** Rejected by the ruling: `{"text": "<the whole rendered feed>"}` satisfies a content-type check while being exactly the renderer the charter forbids.
- **Serve the route on the cluster listener too.** Rejected: the cluster store backend has none of the host inputs, so every pane, name and task would render absent — a confidently empty answer from a host where the question was never answerable.
- **Do nothing.** See Do-Nothing Option.

## Acceptance Criteria

- [ ] **AC1 — the route answers joined JSON with structured fields, 200, `application/json`.** `GET /api/1.0/who-needs-me` returns a JSON object with a top-level `sources` object (keys `store`, `registry`, `panes`, each `ok` | `failed`), a `source` string (`store` | `event-log`), and `items`, `sessions`, `panes` arrays; the response `Content-Type` is `application/json`. Each item object carries at least: `item_id` (the **store** id, unchanged from `/api/1.0/attention`), `dedup_key`, `session_id` (derived from `liveness_ref`), `kind`, `text`, `state`, `created_at`, `age_seconds`, `pane`, `pane_listing_unreadable`, `recorded_pane`, `routable`, `session_name`, `host`, `cwd`, `tool`, `task`, `goal`, `topic`, `headless`, `source`, `replayed`. Each session object carries `session_id`, `name`, `age_state` (`ok` | `absent`), `age_seconds`, `pane`. ⚠️ No field may hold a multi-line preformatted feed: every string field contains no `\n`. Evidence: a Ginkgo spec serving a synthetic item and asserting the field set, the content type via `libhttp.ApplicationJSONContentType`, and the no-newline rule.
- [ ] **AC2 — a real join resolves, so a stub cannot pass.** A fixture whose registry fake returns a **name generated inside the spec** (a unique token, not a constant in the implementation), whose pane fake returns a pane id and a title equal to that name, and whose task index returns a **generated** task title, yields a row carrying those exact generated values in `session_name`, `pane`, and `task`; and the `sessions` array carries the generated name for that id. **Negative half:** the same fixture with the pane listing injected as failing yields `pane == ""` and `pane_listing_unreadable == true` — not a fabricated pane. ⚠️ The values are generated so no hardcoded literal in the implementation can satisfy the assertion; this is the criterion that kills a handler emitting the field set with every provenance field blank. Evidence: a Ginkgo spec configuring the fakes with generated values, asserting exact equality, then asserting the failing-listing shape.
- [ ] **AC3 — the answer changes with the store's contents.** Two seeded stores (temp BoltDB, `Push`-seeded) — one holding at least one open item, one empty — produce two different `items` arrays; the ≥1-open store's item id and payload appear in its answer and the empty store's answer has `items: []`. **Positive control:** the ≥1-open answer carries an item. Evidence: a Ginkgo spec seeding each store and asserting the difference and the positive control.
- [ ] **AC4 — the store-down fallback answers from the event log and names its source.** With a store client injected to fail and a fixed, checked-in events dir holding **two sessions in differing states**, the handler answers `source: "event-log"` and `sources.store == "failed"`, with one `items` entry per record the existing decoder folds as open (last record per dedup key with `Open: true`), each carrying `replayed: true` and `item_id == dedup_key` (the event log's id — there is no store id in replay mode, so the two keys are documented equal). Evidence: a Ginkgo spec injecting a failing store over the two-session events fixture, asserting source, `replayed`, the per-session counts, `sources.store`, and the `item_id == dedup_key` rule.
- [ ] **AC5 — "unreadable" is distinguishable from "empty".** A failed registry read yields `sources.registry == "failed"` with `sessions: []`, never a silent empty list; a failed pane listing yields `sources.panes == "failed"` and `pane_listing_unreadable == true` on any row whose event recorded a pane. ⚠️ The registry status is producible only by extending `readSessionNames` to return `(map, error)` — see Constraints — because the current reader fails open and cannot report failure; the spec drives the failure by pointing `SessionsDir` at a regular file (an `ENOTDIR` read). Evidence: a Ginkgo spec pointing `SessionsDir` at a regular file and injecting a failing pane lister, asserting both status blocks and the row flag.
- [ ] **AC6 — the store stays a store.** The **new** files' added lines contain no `text/template`, `html/template`, `lipgloss`, `termenv`, `fmt.Sprintf("<`, `strings.Builder` or `tabwriter` construct, and every new handler sets `Content-Type: application/json`. The boundary sentence — "no arms or renderers, no answer-return routing to `producer_id`" — is landed in the **tracked** `docs/dod.md` in the same change, because `CLAUDE.md:11` is untracked. Evidence: `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter|fmt\.Sprintf\("<' pkg/who-needs-me*.go pkg/handler/who-needs-me*.go | wc -l` prints `0`; a spec asserting the content type; `test -f docs/dod.md` and `grep -q` for the sentence there.
- [ ] **AC7 — the read is read-only and adds no client.** The handler reaches the store through `Read` only; no write method is called. The join uses the injected `ProvenanceResolver`, `PaneLister`, registry reader, `TranscriptReader` and the existing event decoder; no new HTTP client and no second liveness model. Evidence: `grep -rnE 'http\.Get|http\.Client|\.Push\(|\.Answer\(|\.Close\(|\.Flag\(|\.Rate\(|\.Seen\(|\.Escalate\(|\.RecordAttempt\(' pkg/who-needs-me*.go pkg/handler/who-needs-me*.go | wc -l` prints `0`; a Ginkgo spec whose fake store records calls and asserts only `Read`.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `test -f pkg/who-needs-me.go && test -f pkg/handler/who-needs-me.go` — both new files exist
- `grep -c 'who-needs-me' main.go` — prints at least `1` (the route is registered)
- `grep -rnE 'text/template|html/template|lipgloss|termenv|strings\.Builder|tabwriter|fmt\.Sprintf\("<' pkg/who-needs-me*.go pkg/handler/who-needs-me*.go | wc -l` — prints `0`
- `grep -rnE 'http\.Get|http\.Client' pkg/who-needs-me*.go pkg/handler/who-needs-me*.go | wc -l` — prints `0`

### Operator-executable (runs on the host after PR merge and `make install`)

- `make e2e` — exits 0 (the `docs/dod.md` gate, before the PR opens)
- `curl -s -o /dev/null -w '%{http_code} %{content_type}\n' http://localhost:18080/api/1.0/who-needs-me` — prints `200 application/json`
- `curl -s http://localhost:18080/api/1.0/who-needs-me | jq -r '.source, .sources.store'` — prints `store` then `ok`
- `curl -s http://localhost:18080/api/1.0/who-needs-me | jq '.items | length'` — prints a number; with an open item present it is `> 0`
- `curl -s http://localhost:18080/api/1.0/who-needs-me | jq -r '.items[0] | keys | join(",")'` — names the structured fields, none of them a rendered feed

## Desired Behavior

1. **One new read route, `GET /api/1.0/who-needs-me`, on the board router only.** Registered in `main.go` beside the board's other board-only surfaces, not inside `registerAttentionAPIRoutes` (which the cluster listener shares).
2. **The answer is joined data.** For each open item the route emits the item's own fields (`item_id` = the store id, unchanged from `/api/1.0/attention`; `dedup_key` = the event-log key; `session_id` derived from `liveness_ref`; `kind` mapped from `answer_mechanism`; `text` = payload; `state`; `created_at`; `age_seconds`) joined to `ProvenanceResolver.Resolve`'s output (`pane`, `pane_listing_unreadable`, `recorded_pane`, `routable`, `session_name`, `host`, `cwd`, `tool`, `task`, `goal`, `topic`, `headless`). `ack` items are omitted: an acknowledgement is not a gate.
3. **The answer carries the host snapshot.** `sessions` lists the registry with each session's transcript state (`ok` | `absent`), its age and its pane — the listing entry whose glyph-stripped `title` equals the session's glyph-stripped `name`, else `""` (the script's `pane_for` rule); `panes` lists the live pane listing. A `sources` block names whether each read succeeded (`store`, `registry`, `panes`), so a caller can distinguish "no sessions" from "registry down".
4. **A store outage falls back to the event log.** When `store.Read` fails, the route folds every `<sid>.events.jsonl` under the attention state dir through the existing event decoder into currently-open records, answers `source: "event-log"` and `sources.store: "failed"`, and stamps every row `replayed: true` with `item_id == dedup_key`. It never fails on a store outage.
5. **No field is a rendering.** Every string field is a single-line value the caller formats; the route assembles no template, no table and no preformatted feed.
6. **The inputs are the service's own.** The route reuses `store.Read`, `ProvenanceResolver`, `PaneLister`, the extended `readSessionNames`, `TranscriptReader` and the existing event decoder. It opens no HTTP client, reads no second liveness model and mutates nothing.

## Constraints

- The route is `GET /api/1.0/who-needs-me` on the board router only. Never inside `registerAttentionAPIRoutes`.
- `item_id` keeps its existing meaning (the store id); the event-log key is `dedup_key`. In replay mode (no store id) `item_id == dedup_key`, documented in DB4 and AC4.
- `readSessionNames` is extended to return `(map[string]sessionName, error)` so `sources.registry` can report a failed read. Its existing call sites — `pkg/provenance.go` (the resolver), `pkg/worker-coverage-parity_internal_test.go:64` (which currently assigns one return value and must be updated to compile), and the new reader — are updated to accept the error and keep their current fail-open rendering (a failed registry still resolves no name). The change is tested. No second registry reader is added.
- The answer is JSON data. No template, no lipgloss/termenv, no `strings.Builder`, no `tabwriter`, no `fmt.Sprintf("<`, and no `{"text": …}` envelope.
- Every handler sets `Content-Type: application/json`.
- The store's `Read` is the only store method the route calls. No write method, no second client.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- The boundary sentence is landed in the tracked `docs/dod.md`, because `CLAUDE.md:11` is untracked.
- The fallback reuses the existing event decoder (`decodeEventRecord` / `EventIngestRecord`), not a second parser; "currently open" means the last record per dedup key has `Open: true`.
- Tests use the repo's idiom: a temp BoltDB seeded via `Push`, with injected `PaneLister` / registry / `TranscriptReader` fakes. A checked-in binary Bolt directory is not used.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`. `.maintainer.yaml` sets `release.autoRelease: true`, so the bullet is what the releaser turns into the tag.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| The store is unreachable | Answer `source: "event-log"`, `sources.store: "failed"`, every row `replayed: true`. | The store recovers; the next read answers `source: "store"`. | `.source` flips to `event-log` while the board is down. |
| The pane listing fails | No pane is claimed; `sources.panes: "failed"`; rows with a recorded pane carry `pane_listing_unreadable: true` and `recorded_pane` — never `unroutable`. | The listing recovers; the next read resolves panes. | `sources.panes` is `failed` while `wezterm cli list` answers by hand. |
| The registry is unreadable | `sources.registry: "failed"`; `sessions: []`; rows make no name claim. | The registry recovers; the next read resolves names. | `sources.registry` is `failed` while `~/.claude/sessions` holds entries. |
| An item carries an `ack` mechanism | Omitted, not rendered as a gate. | None needed. | An `ack` item is absent from `.items`. |
| A slow or contended resolver read | The route returns its `sources` block rather than stalling the board; the board's own latency is unaffected. | The resolver's cache window refreshes on the next read. | The route's request latency, read beside the board's. |
| A caller asks for `--jump` / `--resolve-owners` | Out of scope: the route serves data only, with no arm and no cause write. | Use `scripts/jump.py` / the script. | n/a — documented boundary. |

## Security / Abuse

The route is read-only and serves on the same loopback board listener the operator already uses; it widens no listener. It returns item payloads, session ids, pane ids, cwds and task titles — the same values the board already renders to the same operator — so it discloses nothing the board does not. It adds no credential, no network call and no file read beyond the state dir it already reads. The pane listing carries `tty_name` in the raw type but this route emits only `pane_id` and `title`, never the device path.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Extend `readSessionNames` to return an error; update its call sites; spec | 3 | AC5's registry half | — |
| 2 | The reader: item + provenance join, host snapshot, `sources` block, in `pkg/who-needs-me.go`, with its specs | 2, 3, 6 | AC1, AC2, AC3, AC5, AC7 | prompt 1 |
| 3 | The event-log fallback reusing the existing decoder, with its spec | 4 | AC4 | prompt 2 |
| 4 | The handler + route registration + the tracked `docs/dod.md` boundary sentence, with its spec | 1, 5 | AC1, AC6, AC7 | prompt 2 |

## Do-Nothing Option

The machine keeps paying ~1.3–2.7 cores to `who-needs-me.py` on every manager tick, and the task's DoD — `pgrep -f 'who-needs-me\.py' | wc -l` returning 0 with positive tick evidence — cannot close because there is nothing to migrate the callers onto. The parent goal's SC3 ("ported into that service") stays met only through its "its own task filed" branch, and the cost the whole task exists to remove stays exactly where it is.
