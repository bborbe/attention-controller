# The synthetic-probe tag

A **synthetic probe** is a card posted to exercise the attention stack — the
push path, the answer path, a counter — and never a question for the operator.
It must never render on the board.

The tag is a **payload prefix**:

```
SC2 burst probe
```

## The convention

A probe's `payload` **opens** with `SC2 burst probe`, then any discriminator the
probe needs:

```
SC2 burst probe #218: synthetic dismissal
```

`pkg/probe-tag.go` carries the constant (`ProbeTagPrefix`) and the predicate
(`IsSyntheticProbePayload`). The exclusion lives in `classifyForRead`
(`pkg/attention-store-impl.go`): a tagged item is omitted from `ReadBoard`'s
result and **not** removed from the store.

## Why a payload prefix and not a field

[[Attention Item Schema]] carries no tag field, and this repo's own rule is
that a field the schema does not define is a finding to report on the schema
page, never a gap to fill silently in code. A prefix is a convention rather than
a schema change, which is the same class the production-touching marker
(`docs/production-touching-marker.md`) already occupies.

## The properties that matter

⚠️ **The match is a PREFIX, never a substring.** A real card may legitimately
discuss the probes — a task card reading *"the `SC2 burst probe` burst filled
the board"* is an operator question and must render. Only a payload that OPENS
with the tag is a probe. This is the same discipline
`docs/production-touching-marker.md` records: *"The match is list-item form
only, never a bare substring."*

⚠️ **The comparison is case-sensitive.** `sc2 burst probe` is **not** tagged and
**will** reach the board. Write it exactly as shown. A silent case-fold would
hide that mistake rather than report it.

⚠️ **The key is the tag, not the producer.** A filter keyed on a producer
session id would fail the moment a probe is posted from a session that did not
exist when the filter shipped — and the 2026-11-17 burst already used 220
distinct producer ids. `IsSyntheticProbePayload` reads `payload` alone.

⚠️ **It is a read filter, not a delete.** The disposition is neither `keep` nor
`remove`, so a probe stays in the store. The two tasks that produced the
2026-11-17 burst require that burst to remain permanent store history, and a
census can still find it by name.

## Scope: the board only

The exclusion applies to `ReadBoard` — the read the board JSON
(`GET /api/1.0/attention/board`, `pkg/handler/attention-read.go`) and the board's live stream
(`pkg/handler/attention-stream.go`) both consume. `Read` is unchanged, so the
JSON read API and every arm that acts on what it returns see exactly what they
saw before.

The **history** endpoint is deliberately untouched: it is the census surface,
and the producing tasks' own requirement is that a census *exclude the tag by
name*, not that the history read hide it.

⚠️ **`ReadBoard` has in-process consumers besides the two board surfaces** — the
watcher job, the menu-answer job, the context-crossing sink, the compact trio and
the event-ingest sink. They inherit the exclusion, which is the intended
direction: a synthetic probe must not raise an invariant card or drive a manager
wake. The one consequence worth knowing is `StoreEventSink.Close`, which resolves
an open item by dedup key from `ReadBoard`: a probe closed through that path
no-ops and stays open until `expires_at`. Probes are posted through
`attention-ask.py`, not through event ingest, so this does not arise in practice;
if a probe ever is produced by a hook, close it by item id instead.

## For a probe author

1. Open the payload with `SC2 burst probe`.
2. Post it the way you would any card.
3. Expect it to be absent from `GET /` and present in
   `GET /api/1.0/attention/history`.
4. Do not rely on the board to answer it — answer it from your own script, as
   the 2026-11-17 burst did.

## Measured history

The 2026-11-17 burst: **220** cards, created `19:05:12Z..19:05:20Z`, **all 220**
rendered by the board (`displayed_at` 16–243 ms after creation), all answered
5.5–8.9 s later by `Python-urllib/3.14` from `127.0.0.1` (the probe's own
script, not the operator), all closed 8 s after that. The defect this tag fixes
is the render, not the answers.
