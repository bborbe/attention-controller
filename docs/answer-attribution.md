# Answer attribution

This page is the in-repo copy of the answer-attribution ladder. Its source is
`bborbe/claude-supervisor`'s `scripts/answered-attribution.py` at commit
`2fdfc1a394089b5b36f17b54b1cec0178f03d728`, whose `classify(item)`
returns `(verdict, reason)`. ⚠️ **The commit is pinned deliberately.** The source is not
vendored, so without a pin a future upstream change would drift this page and the Go port
silently, with nothing in this repo able to detect it. Re-derive the pin (and re-check the
ladder row for row) whenever the source moves; if the two disagree, the source wins and the
disagreement is a finding to report. The Go port lives in `pkg/answered-signal.go` and is
**implemented from this contract; do not redefine it here.** If this page and the
source disagree, the source wins and the disagreement is a finding to report.

The ladder exists because a consumer that reads only `answered` treats a scripted
click as the operator's answer. It is the one guard between a scripted click and a
wake the operator never gave.

## The ladder, in order

The conditions are checked in this exact order and the first match wins. The order
is the contract, not an optimisation: rows 3 and 4 are read before row 6 so that a
decline or a hidden answer is classified by what the operator said, not by who
clicked.

| # | Condition | Verdict |
|---|---|---|
| 1 | not `was_acted_on(item)` | `NOTHING` |
| 2 | `state == "closed"` **and** no `answered_at` | `UNATTRIBUTED` — a close is a clear, not an answer |
| 3 | `answer.kind == "skip"` | `DECLINED` |
| 4 | `answer.kind == "hidden"` | `HIDDEN` |
| 5 | `answered_client` absent | `UNATTRIBUTED` |
| 6 | `answered_client.automation` is `true` | `AUTOMATED` |
| 7 | otherwise | `ATTRIBUTED` |

`was_acted_on(item)` is true when `answered_at` is set, **or** when `answered_by`
is set and `state` is `answered` or `closed`. Nothing else makes a card acted on:
an `answered_by` on an open card, for example, is `NOTHING`.

## The fields it reads

| Field | What the ladder concludes from it |
|---|---|
| `state` | `closed` with no `answered_at` is a clear, not an answer (row 2). `answered` or `closed` is what lets a set `answered_by` make a card acted on at all. |
| `answered_at` | Its presence alone makes a card acted on; its absence on a closed card is the row 2 boundary. |
| `answered_by` | With `state` in `{answered, closed}`, makes a card acted on even when `answered_at` is absent. It is a caller declaration, so it never distinguishes a human from a script. |
| `answered_client` | Absent means the answer is `UNATTRIBUTED` (row 5): nothing recorded the client, so nothing can attribute the answer. |
| `answered_client.automation` | `true` means a scripted click (row 6). Absent and `false` are treated alike: only a positive `true` is automated, so an item answered before the field existed is not read as a claim that it was human. |
| `resolved_by` | **Never read as a condition.** Present or absent changes nothing; row 7 does not require it. |
| `answer.kind` | `skip` declines (row 3) and `hidden` hides (row 4), both before the client is consulted. Every other kind — `option`, `text`, empty — falls through to the client checks. |

## Rendered vocabulary

The rendered vocabulary is derived from the verdict and never stored beside it.

| Verdict | Rendered |
|---|---|
| `ATTRIBUTED` | `ANSWERED` |
| `DECLINED` | `DECLINED` |
| `HIDDEN` | `HIDDEN` |
| `NOTHING`, `UNATTRIBUTED`, `AUTOMATED` (and any unknown value) | `NOT_OPERATOR_ANSWERED` |

## The three documented regressions

**The kind before client rule (2026-10-20).** A decline is a decline whether a
person or a script clicked it. Asking "was this flagged automated?" before reading
the answer kind answered `ATTRIBUTED` for a card the operator had explicitly
dismissed, so rows 3 and 4 now precede row 6.

**A close is a clear, not an answer (2026-09-27).** The board's Acknowledge closes
a card with `answered_at` unset and nothing routed back. Releasing on it would open
a gate nobody opened, so row 2 classifies it `UNATTRIBUTED` rather than as an
answer.

**`resolved_by` is NOT a requirement (2026-09-27).** The requirement was withdrawn
hours after it shipped because the board's JavaScript never sends `resolved_by`, so
it refused the operator's own board answer — the card read `answered` while the
asking session stayed frozen. The `resolved_by` requirement in
`permission-answer-poll.py` is a different script's rule and does not apply here.
Row 7 therefore reads a recorded client with no automation flag as the operator's
answer, whether or not a `resolved_by` is present.

## Known gap: multi-question items

A multi-question item carries its kinds in `answers` and leaves `answer` nil — the
two fields are mutually exclusive. The ported ladder reads `answer.kind` only,
exactly as the source does, so a multi-question card is classified on its
`answered_client` alone: one whose answers are all `hidden` or all `skip` classifies
`ATTRIBUTED` rather than `HIDDEN` or `DECLINED`. This is the faithful port;
changing the definition is out of scope for this spec.
