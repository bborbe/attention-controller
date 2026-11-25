# The operator tier for message cards

The attention store refuses to let a lower arm settle an **operator-tier** item. This page is
the doctrine for which message cards are operator-tier and which arms may settle them. Both
tables below are mirrored from `pkg/operator-tier.go` — `TestOperatorTierDocMatchesCode` parses
them and compares row for row, so a missing, extra or reordered row fails the build.

The `permission` class is **not** governed here: `permission-tier.py` tiers permission gates, and
a manager-tier permission answer must keep landing. The `ack` class is not governed either — it
is a separate operator-tier class with its own producer path.

## Which cards are operator-tier

An item is operator-tier when its `answer_mechanism` is **not** `permission` and one of these
holds:

- its `interrupt_class` is `approve` (or its `decision` alias), or
- its `interrupt_class` is `pick` and its producer kind is `manager`, or
- its `interrupt_class` is `pick`, its producer kind is `session`, and its payload matches one of
  the operator words below.

A `pick` from a session whose payload matches none of the words is manager-tier and unchanged.
Any other producer kind is unrated.

### The operator words

| # | Word |
|---|---|
| 1 | prod |
| 2 | production |
| 3 | deploy |
| 4 | make apply |
| 5 | make buca |
| 6 | merge |
| 7 | release |
| 8 | delete |
| 9 | force |
| 10 | secret |
| 11 | credential |
| 12 | token |
| 13 | live trade |
| 14 | TDR |
| 15 | approve: |

Matching is case-insensitive and word-bounded, exactly as the probe's `re.I` + `\b`.

## Which arms may settle one

| # | Match | Value |
|---|---|---|
| 1 | equals | operator |
| 2 | contains | attention-board |
| 3 | prefix | supervisor:attention-next |

The **empty arm** is allowed on the **close** route — a producer withdrawing its own card names no
actor and answers nothing — and refused on the **answer** route, where an armless answer names no
operator.

⚠️ This is a transition constraint, not authentication. `answered_by` is caller-declared (see
`docs/answer-attribution.md`), so a caller that declares `attention-board` bypasses the refusal.
The control raises the cost of an accidental lower-tier settle; it does not defeat a deliberate
misdeclaration.

## The probe it mirrors

The placement control that measures this population is `attention-placement.py`, a script in the
operator's private vault (not vendored in this repo). It is pinned here by commit and content
hash so a drift is auditable:

- script: `attention-placement.py` (operator's private vault, not in this repo)
- commit: `67925e3e3b6eced83ed8e969838830ca41e5a8ef` (2026-11-20)
- sha256: `250688b15d6e6cb56846d6fb5a5e737742ab76437f38fe0ef44008855ccfb899`
- functions: `expected_tier` (the word list and the `producer_kind` branch), `answer_tier` (the
  operator set)

⚠️ Two deliberate divergences, both in the store's favour:

- **The empty arm on the answer route.** The store refuses an empty `answered_by` on an
  operator-tier item, while the probe treats an armless answer with no `answered_at` as unrated
  (`None`, not a breach). The store is stricter than the probe here.
- **`approve:` on its canonical payload.** The probe's word list is one `\b`-bounded alternation,
  so its trailing `\b` after the `:` in `approve:` fails on the canonical `approve: repair` form
  (the colon is followed by a space, not a word character) and the token never matches. The store
  bounds `approve:` on the left only, so it matches `approve: repair` — the form the cards
  actually carry.
