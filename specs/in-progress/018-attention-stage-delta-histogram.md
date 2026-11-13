---
status: approved
approved: "2026-11-13T09:54:11Z"
branch: dark-factory/attention-stage-delta-histogram
---

## Summary

- The controller exposes how long each leg of a card's journey took, as a histogram, so a dashboard can chart the pipeline as percentiles over time.
- There are exactly four legs, and they are the four the timeline contract already names.
- Each leg is identified by its two stage names, so a reader can find both terms in the contract's own table.
- A leg whose stages both carry a timestamp is measured; a leg touching a missing stage is not, while a leg that does not touch it still is.
- A clock skew that makes a leg negative is reported as measured, never hidden, clamped or absolute-valued.
- The population is re-measured on a bounded schedule rather than accumulated, so the reading describes today's cards and not the process's uptime.
- The bucket edges resolve the range these legs actually span — seconds to hours — so the percentiles mean something.
- Nothing about how a card is written, rendered or answered changes.

## Problem

The Grafana stage-duration dashboard cannot be built, because the number it charts does not exist anywhere a metrics system can read.

Today the four deltas between consecutive stages are computed only at render time, in `pkg/handler/attention-page-timeline.go`, as a string like `"3s"` placed on a card's info panel. Percentiles require a histogram, and a rendered string cannot produce one. Verified on 2026-11-13: `git grep 'attention_stage_delta' origin/master` in this repo returns nothing, and the live `http://localhost:18080/metrics` carries only the three board counters and `attention_watcher_cycle_seconds`. The timestamps themselves all exist — `asked_at` since v0.73.0, `displayed_at` since v0.76.0, `created_at` and `answered_at` always, and the delivery attempt as a sibling record — so this is a missing read surface, not missing data.

The authoritative contract is **`docs/attention-item-timeline.md`** in this repo, § "The delta vocabulary" and § "Seven stages, and where each comes from". **Implement that contract; do not redefine it.** If this spec and that doc disagree, the doc wins and the disagreement is a finding to report, not a choice to make in code.

## Goal

`attention_stage_delta_seconds` exists as a histogram with `from` and `to` labels over the four contract deltas, served from the controller's existing `/metrics` endpoint, so `histogram_quantile` can answer "what was the p50 and p90 of each pipeline leg" for any time window the metrics store retains.

## Non-goals

- **The Grafana dashboard itself.** Four panels and their `GrafanaDashboard` CR live in `bborbe/nuke` and belong to the vault task *Grafana Shows the Attention Pipeline's Stage Durations*. This spec produces the series those panels read; it does not add, apply or verify a panel.
- **The lost-answer counter.** A different family (`attention_lost_answers_total`), deferred in that same row for its own reason.
- **The card's info-panel timeline.** `pkg/handler/attention-page-timeline.go` already renders the same four deltas as text. It is a different surface with a different contract (human-readable, floored to whole seconds, `—` for absent) and this spec does not touch it.
- **Changing what the store writes.** No new field, no new write path, no change to `Push`, `MarkSeen`, `Answer`, `RecordAttempt`, `Close` or `Escalate`.
- **Fixing the `markEverySeen()` per-row POST on every board load.** A known concern recorded in the Grafana row. The design here reads the resulting `displayed_at` value rather than instrumenting the seen endpoint's write path, so it neither depends on nor inflates that behaviour.
- **Backfill.** Items that predate a field simply contribute no observation for the deltas touching it.

## Assumptions

- The store's `History` read is the population source. It never prunes, never filters on liveness and never filters on state; the unbounded form is `limit <= 0`. It does drop expired **open** items on read and serves a warm page cache, so the population is "every item except expired open ones", read from cache when warm.
- The metrics pusher `com.bborbe.attention-metrics-push` scrapes `http://localhost:18080/metrics` on `StartInterval 300` and forwards only `attention_*` families, so a new family under that prefix is picked up with no pusher change.
- The controller process registers its collectors once at startup on `prometheus.DefaultRegisterer`, which is what `promhttp` serves.
- A delivery attempt is keyed by the item id as a string, so a bulk read joins to `History` on `ItemID` with no second key space.
- Producer and store share a clock in the normal case; a producer clock ahead of the store's is a real, observable condition rather than an impossibility.

## Alternatives

- **Observe at write time — one hook in each of `Push`, `MarkSeen`, `Answer` and `RecordAttempt`.** Rejected. It couples a metrics dependency into four write paths, re-observes on a re-attempt (which overwrites rather than appends), and cannot produce a reading for the existing backlog — so it would need a live card driven through the pipeline before any criterion could pass. It also makes the missing-stage rule four independent predicates instead of one per item.
- **A `HistogramVec` with `Reset()` before each refresh.** Rejected: a scrape landing mid-`Reset` would observe a partially populated histogram, and the reading would be silently wrong rather than absent.
- **A single `stage` label carrying `asked→created`.** Rejected by the contract's own sentence: "Labels use these stage names verbatim". A delta is not a stage name, and a reader looking for `asked` in the label set would not find it.
- **Read through `Read` or `ReadBoard`.** Rejected: both remove dead items from the store as a side effect, and a metrics scrape must not mutate the population it measures.
- **Call `Delivery` once per item.** Rejected on cost: it opens one read transaction per item, and the population is in the tens of thousands. The store's own comments record that this shape was already removed once for exactly this reason.
- **Clamp a negative delta to zero.** Rejected: a delta computed from a producer clock ahead of the store's is skew, and skew is a measurement. Clamping asserts the leg took no time, which is the opposite of what a negative says.
- **Do nothing.** See Do-Nothing Option.

## Acceptance Criteria

- [ ] **AC1 — the family exists with exactly two labels, only the four contract deltas, and all four always exposed.** A gather of the registry exposes `attention_stage_delta_seconds` as a histogram whose label names, **excluding the implicit `le` carried by every `_bucket` series**, are exactly `from` and `to`; whose sorted `from`/`to` value set over its `_count` series is exactly the four pairs `asked→created`, `created→displayed`, `displayed→answered`, `answered→delivered`; and which exposes those four pairs even over an empty population, each with `_count` `0`. **Negative half:** `delivered→released`, any `→closed` pair and the `shown` spelling are absent from the gathered label set. Evidence: a Ginkgo spec gathering a private registry over both a populated and an empty source, asserting the label names and the sorted pair set, and asserting `_count` is `0` for all four over the empty source.
- [ ] **AC2 — the bucket edges resolve seconds to hours, and are frozen.** The `_bucket` series' `le` values are exactly `0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 21600` plus `+Inf`. ⚠️ This criterion exists because `prometheus.DefBuckets` — the laziest registration — tops out at 10 s, which passes every counting criterion while making `histogram_quantile` return the largest bound for every leg of a pipeline whose median forwarding lag is measured in minutes. Evidence: a Ginkgo spec gathering the family and asserting the sorted finite `le` set equals that list exactly, and that the largest finite bound is `21600`.
- [ ] **AC3 — a stage with no timestamp suppresses only the deltas that touch it.** For an item carrying `created_at`, `displayed_at`, `answered_at` and a delivered attempt but **no** `asked_at`, the population yields observations for `created→displayed`, `displayed→answered` and `answered→delivered`, and **no** observation for `asked→created`. ⚠️ Both halves are asserted in one case: a collector that skips the whole item satisfies the absence half and fails the presence half, which is the defect this criterion exists to catch. Evidence: a Ginkgo spec over a synthetic item, asserting the three pairs' `_count` each include it and `asked→created`'s `_count` does not.
- [ ] **AC4 — a negative delta is observed unclamped.** An item whose `asked_at` is later than its `created_at` produces one `asked→created` observation whose `_sum` is negative and whose `_count` includes it. **Negative half:** the value is neither rewritten to `0` nor absolute-valued — asserted on the sign of `_sum`, not merely on the series existing. Evidence: a Ginkgo spec asserting `_sum < 0` and `_count` incremented by exactly 1.
- [ ] **AC5 — only a `delivered` attempt produces `answered→delivered`.** Three answered items with attempts of `delivered`, `failed` and `held` respectively produce exactly one `answered→delivered` observation — the `delivered` one. ⚠️ A `failed` or `held` attempt carries an `AttemptedAt` of its own, but the contract says the release *is* the delivery; counting either would report a delivery time for an answer that never arrived. Evidence: a Ginkgo spec asserting `_count` for that pair is exactly 1 over the three-item population.
- [ ] **AC6 — the reading is a snapshot of the population, not an accumulation.** Two collects over an unchanged population report the same `_count`; a third collect, taken after the movable clock advances past the refresh bound and one more item is added, reports `_count` + 1. **Negative half:** the count does not double between collects, which is what a collector calling `Observe` on every scrape would produce. Evidence: a Ginkgo spec performing three collects against a fake source with a movable clock, asserting equality then a single increment.
- [ ] **AC7 — the refresh is bounded, and concurrent scrapes share one read.** Three collects inside the refresh window cause exactly one population read; a collect after the clock advances past the bound causes a second. Evidence: a Ginkgo spec with a call-counting fake asserting the read count is `1` across three collects, then `2` after the clock advances; and a concurrency case built on a fake whose read blocks on a channel until the second collect has entered, then releases — comparing the two gathered families for equality, never a `time.Sleep`-based race.
- [ ] **AC8 — the read is read-only, and is exactly two reads.** The collector reaches the store through `History` and the bulk delivery-attempt read only; `Read`, `ReadBoard`, `SweepAnswered` and every write method are never called. The federating wrapper's `DeliveryAttempts` returns local attempts only, never a peer's. Evidence: `git grep` over the collector's source for `ReadBoard`, `SweepAnswered` and `.Read(` returning 0 matches; a Ginkgo spec whose fake records its calls and asserts exactly those two; and a spec asserting the federating store's `DeliveryAttempts` returns nothing for an item only the peer holds.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — the repo's gate; exits 0
- `grep -c 'attention_stage_delta_seconds' pkg/boardmetrics/stage-delta.go` — prints at least `1`
- `grep -c 'DeliveryAttempts' pkg/attention-store.go pkg/attention-store-impl.go pkg/attention-store-notifying.go pkg/attention-store-federating.go` — non-zero for each of the four files
- `! grep -q '"shown"' pkg/boardmetrics/stage-delta.go` — succeeds only when no renamed stage leaked in
- `! grep -q 'ReadBoard\|SweepAnswered\|\.Read(' pkg/boardmetrics/stage-delta.go` — succeeds only when no pruning read was introduced

### Operator-executable (runs on the host after PR merge)

The live instance is the **local launchd controller**, not the dev or prod cluster: both clusters' attention-controller carry zero traffic (`attention_board_renders_total 0`), while `http://localhost:18080/metrics` serves the families the pusher forwards. So the deployed check reads `localhost:18080` after the operator has run `make install` and `launchctl kickstart -k gui/$UID/com.bborbe.attention-controller`.

- `curl -s http://localhost:18080/metrics | grep -c '^attention_stage_delta_seconds_count'` — prints `4`, one per contract pair
- `curl -s http://localhost:18080/metrics | grep '^attention_stage_delta_seconds_count{from="answered",to="delivered"}'` — prints a value `> 0`
- `curl -s http://localhost:18080/metrics | grep '^attention_stage_delta_seconds_sum{from="answered",to="delivered"}'` — prints a value `> 0`
- `curl -s http://localhost:18080/metrics | grep -c 'attention_stage_delta_seconds.*released'` — prints `0`

## Desired Behavior

1. **The controller exposes `attention_stage_delta_seconds` as a histogram** with exactly two labels, `from` and `to`, whose values are the stage names the contract uses verbatim: `asked`, `created`, `displayed`, `answered`, `delivered`. The family name is frozen with `Namespace` and `Subsystem` empty, following the existing counters in `pkg/boardmetrics/metrics.go`. The four pairs are `asked→created`, `created→displayed`, `displayed→answered`, `answered→delivered`, and all four are exposed on every scrape regardless of whether any item contributed to them.
2. **Each pair is measured independently per item, from the timestamps that pair names.** `asked→created` needs `AskedAt`; `created→displayed` needs `DisplayedAt`; `displayed→answered` needs `DisplayedAt` and `AnsweredAt`; `answered→delivered` needs `AnsweredAt` and a delivery attempt whose outcome is `delivered`. A pair whose inputs are both present is measured even when a stage another pair needs is absent.
3. **An absent stage contributes no observation, and never a zero.** No fallback to another field, no substitution, no "treat missing as the other end".
4. **A negative delta is observed as measured.** The signed value reaches `_sum` unchanged and the observation counts in `_count`.
5. **The observation set is recomputed, not accumulated.** A second scrape over an unchanged population reports the same counts rather than double them.
6. **The refresh is bounded.** The population is read at most once per 300 seconds, with the cached reading served in between, and concurrent scrapes serialise onto one read rather than racing.
7. **The read is read-only.** The population comes from `History`, which never prunes, and from a new bulk delivery-attempt read. No method that mutates the store is called from the collector.
8. **The bucket edges cover the range these legs span.** Seconds to hours, in the same spirit as the existing `attention_watcher_cycle_seconds` histogram — whose own comment argues for edges chosen around what the measured thing actually costs — but wider, because a forwarding lag and a delivery wait span minutes to hours where a watcher cycle spans a fraction of a second to a few minutes.

## Constraints

- The contract doc `docs/attention-item-timeline.md` is authoritative for the delta vocabulary and the stage names. Do not redefine it and do not add a delta it does not name.
- Exactly two labels, `from` and `to`. Never a single `stage` label carrying `asked→created`.
- The metric name is frozen: `attention_stage_delta_seconds`, with `Namespace` and `Subsystem` empty.
- The bucket edges are frozen at `0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 21600`.
- The collector lives in `pkg/boardmetrics/stage-delta.go`, extending that package rather than adding a second metrics package, so the repo keeps one metrics home.
- The new store method is named `DeliveryAttempts`, returns `map[ItemID]DeliveryAttempt`, and is added to the `AttentionStore` interface and all three implementations (the store, the notifying wrapper, the federating wrapper).
- The existing `pkg.Metrics` port and its four families are unchanged in name, type, label set and meaning. This adds a collector beside them.
- `pkg/handler/attention-page-timeline.go` is not modified. The text renderer and the histogram are different surfaces over the same contract, and unifying them is out of scope.
- No store write path changes. The only store addition is one bulk read.
- A federated card contributes no attempt-derived observation on this instance: the federating wrapper delegates the new read to its local store only, matching the contract's rule that a peer's store-written stages read as absent here.
- Use the injected `libtime.CurrentDateTimeGetter`, never `time.Now()`.
- The pusher forwards `attention_*` families by prefix, so no pusher change is required and none is to be made.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md` naming the family and its two labels. ⚠️ The repo currently has **no** `## Unreleased` section — `v0.76.1` is the top heading — so the section must be created above it, and `grep -c '^## Unreleased$' CHANGELOG.md` must print exactly `1` afterwards. `.maintainer.yaml` sets `release.autoRelease: true`, so this bullet is what the releaser turns into the tag that makes the family consumable.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| The store read fails during a refresh | The collector keeps serving the last good snapshot; it does not emit a zeroed histogram, which would read as "no cards have any delta". A refresh failure is logged at warning level naming the failing read. | Next refresh retries. Detection: `_count` stops moving while cards continue to be answered, and the controller log carries the failing read's name. |
| The process starts with an empty or freshly reset store | All four pairs exposed with `_count` 0. | None needed. Detection: `_count{from="asked",to="created"} 0` with a non-empty board — the two disagree only until the first item lands. |
| A producer clock runs ahead of the store's | A negative `asked→created` observation; `_sum` goes down and the observation lands in the lowest bucket, exactly as `Histogram.Observe` places a negative. | None — this is the measurement. Detection: `_sum{from="asked",to="created"}` decreasing while `_count` rises. |
| Two scrapes arrive concurrently | The second waits on the first; both read the same snapshot. | None needed. Detection: the two responses are identical for that scrape. |
| The store grows, so each refresh decodes more rows | The 300 s bound caps the cost at one decode per window; the read runs in a read transaction and never holds the writer lock. | If the decode time approaches the push interval, raise the bound rather than removing the cache. Detection: refresh duration visible in the controller log; the board's own request latency unaffected. |
| The metrics store is unreachable from the pusher | The controller's local families are unaffected; the push job logs its own failure. | Out of scope — the pusher owns it. Detection: `attention_stage_delta_seconds` present on `localhost:18080` but absent at the datasource. |
| A card is answered and delivered between two refreshes | The next refresh includes it. | None — bounded staleness is the design, and the push cadence is 300 s. Detection: the value appears within one push cycle. |

## Security / Abuse

The family is served on the same `/metrics` listener as the existing `attention_*` families, which is the controller's existing exposure surface; this spec adds no route and widens no listener. The labels carry only fixed stage names — no item id, session id, producer id, carrier name or any other per-item value — so the series count is bounded at four and no card's content or identity is disclosed through the metrics endpoint. The collector reads the store in the same process that already holds it; it adds no credential, no network call and no file read.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Bulk delivery-attempt read on the store, threaded through both wrappers, plus its own spec | 7 | AC8's interface half — `DeliveryAttempts` present on the interface and all three implementations | — |
| 2 | The stage-delta collector, its four pairs, its frozen buckets, its bounded refresh, its registration in `main.go`, and the specs | 1-6, 8 | AC1-AC7, and AC8's collector half | prompt 1 |

Rationale: prompt 1 is a plain read addition with a mock regeneration; prompt 2 is the feature and needs prompt 1's method to compile. Splitting them keeps the interface change reviewable on its own, and prompt 2's specs can then use a fake source that implements both methods. The spec's surface is wide because the metric contract itself is wide; the two-prompt split is where that width is paid down, not a reason to shrink the contract.

## Do-Nothing Option

The dashboard stays empty. The vault task *Grafana Shows the Attention Pipeline's Stage Durations* can provision its four panels and they will read `No data` forever, because the series they query has no producer. Goal SC4 — "a Grafana dashboard shows every stage duration as percentiles over time" — cannot close, and the two facts it exists to expose (the forwarding lag between ask and store receipt, and whether an answer reached its session) stay readable only by an operator who happens to open one card's info panel.

The cost of not doing it is not zero and not deferred: the Grafana row is already `in_progress` with panels written, so the gap is currently a dashboard that looks provisioned and measures nothing.
