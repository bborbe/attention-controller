# Answer attribution

This page is the in-repo home of the answer-attribution contract: how one
answered card is classified as the operator's answer, a decline, a hidden
answer, a scripted click, an unattributed record, or a card nothing acted on.
It is a faithful port of `answered-attribution.py`'s `classify()` from
`bborbe/claude-supervisor`, inlined in spec `020`. **Implement this contract;
do not redefine it.** If this page and the source ladder disagree, the source
wins and the disagreement is a finding to report, not a choice to make in code.

The Go identifiers are `pkg.ClassifyAnswerAttribution`,
`pkg.AttributionVerdict` and `pkg.RenderedAnswerSignal`, all in
`pkg/answered-signal.go`. The predicate below keeps the source's name,
`was_acted_on`, because that is the shared vocabulary between the two repos.

## The seven-row ladder

The rows are evaluated in this order and the first match wins. The order is the
contract, not an optimisation.

| # | condition | verdict |
|---|---|---|
| 1 | not `was_acted_on` | `NOTHING` |
| 2 | `state == "closed"` **and** no `answered_at` | `UNATTRIBUTED` — a close is a clear, not an answer |
| 3 | `answer.kind == "skip"` | `DECLINED` |
| 4 | `answer.kind == "hidden"` | `HIDDEN` |
| 5 | `answered_client` absent | `UNATTRIBUTED` |
| 6 | `answered_client.automation is true` | `AUTOMATED` |
| 7 | otherwise | `ATTRIBUTED` |

`was_acted_on` is true when `answered_at` is set, **or** when `answered_by` is
set and `state` is `answered` or `closed`. Nothing else makes a card acted on:
an open card carrying an `answered_by` that never advanced its state is
`NOTHING`.

Every verdict carries a reason naming the deciding row. Row 6's reason names
the automation flag, so a scripted click is diagnosable from the log alone.

## The fields it reads

| Field | What the ladder concludes from it |
|---|---|
| `state` | `closed` with no `answered_at` is `UNATTRIBUTED` (row 2). `answered` or `closed` plus an `answered_by` satisfies `was_acted_on`. |
| `answered_at` | Its presence alone satisfies `was_acted_on` (row 1), and its absence on a closed card is what makes a close a clear rather than an answer (row 2). |
| `answered_by` | With a terminal `state`, its presence satisfies `was_acted_on`. It is a caller declaration and does **not** distinguish a human from a script. |
| `answered_client` | Absent means the answer cannot be attributed (row 5). Present means the store recorded a client. |
| `answered_client.automation` | `true` is `AUTOMATED` (row 6). Absent and `false` are treated alike — only a positive flag makes a card automated. |
| `resolved_by` | **Not read.** It is never a condition and its presence or absence changes nothing (row 7). See § The three regressions. |
| `answer.kind` | `skip` is `DECLINED` (row 3); `hidden` is `HIDDEN` (row 4). These are evaluated before the client check. |

## The rendered vocabulary

The rendered word is derived from the verdict and never stored beside it.

| Verdict | Rendered word |
|---|---|
| `ATTRIBUTED` | `ANSWERED` |
| `DECLINED` | `DECLINED` |
| `HIDDEN` | `HIDDEN` |
| `NOTHING` | `NOT_OPERATOR_ANSWERED` |
| `UNATTRIBUTED` | `NOT_OPERATOR_ANSWERED` |
| `AUTOMATED` | `NOT_OPERATOR_ANSWERED` |

Every other value, including an unknown one, renders `NOT_OPERATOR_ANSWERED`.

## The three regressions

- **kind before client** (2026-10-20). Rows 3–4 are evaluated before row 6. A
  decline is a decline whether a person or a script clicked it. Asking "was this
  flagged automated?" first answered `ATTRIBUTED` for a card the operator had
  explicitly dismissed.
- **A close is a clear, not an answer** (2026-09-27). The board's Acknowledge
  closes a card with `answered_at` unset and nothing routed back, so releasing
  on it would open a gate nobody opened. Row 2 returns `UNATTRIBUTED`.
- **`resolved_by` is NOT a requirement** (2026-09-27). The requirement was
  withdrawn hours after it shipped, because the board's JavaScript never sends
  it and so refused the operator's own board answer — the card read `answered`
  while the asking session stayed frozen. The `resolved_by` requirement in
  `permission-answer-poll.py` is a different script's rule and does not apply
  here. No row reads it.

## The multi-question gap

A multi-question item carries its per-question kinds in `answers` and leaves
`answer` nil — the two are mutually exclusive. The ported ladder reads
`answer.kind`, exactly as the source does, so a multi-question card whose
answers are all `hidden` or all `skip` is classified on its `answered_client`
alone rather than on those kinds. That is the faithful port; widening the
definition to inspect `answers` is out of scope for this spec and would change
what counts as an operator answer.
