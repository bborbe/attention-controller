---
status: completed
spec: [020-answered-signal-folded-into-delivery-arm]
summary: Ported answered-attribution.py's seven-row ladder to pkg/answered-signal.go with a ten-row committed vector fixture, six Ginkgo specs, and docs/answer-attribution.md.
execution_id: 320aab09-d524-5370-b349-d02209b39707-exec-061-spec-020-attribution-port
dark-factory-version: dev
created: "2026-11-18T10:54:16Z"
queued: "2026-11-18T11:10:42Z"
started: "2026-11-18T11:10:49Z"
completed: "2026-11-18T11:15:16Z"
branch: dark-factory/answered-signal-folded-into-delivery-arm
---

# Port the answer-attribution ladder to Go with its vectors and contract doc

<summary>
- An answered card can be classified into exactly one of six outcomes: a real operator answer, a decline, a hidden answer, a scripted click, an unattributed record, or a card nothing acted on.
- The classification reproduces the supervisor's documented ladder row for row, in the same order.
- The three historical boundary rules are preserved: a decline outranks a scripted client, a close is a clear rather than an answer, and a resolved-by stamp is not required.
- A checked-in vector table covers every outcome and every boundary row, so deleting any one guard fails the suite.
- A scripted click can never read as the operator's answer.
- The rendered vocabulary (answered, not-operator-answered, declined, hidden) is derived from the classification rather than stored beside it.
- The attribution contract gains a written home outside the spec, so the spec is not the only record.
- No delivery arm changes behaviour in this prompt.
</summary>

<objective>
Port `answered-attribution.py`'s decision ladder into this repo as a pure Go function over the existing item model, commit the vector table that proves all seven rows, and give the contract a documentation home. It is ported rather than reused because the source lives in `bborbe/claude-supervisor`, which a binary in this repo cannot call, and the ladder is the only thing standing between a scripted click and a wake the operator never gave.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs listed below, and read `docs/dod.md` for the repo's definition of done.

Read the approved spec first: `specs/in-progress/020-answered-signal-folded-into-delivery-arm.md`. § The ladder, inlined is the authoritative contract for this prompt, including its three regression notes. **Implement it; do not redefine it.** If this prompt and the spec's ladder disagree, the spec wins and the disagreement is a finding to report.

Read before writing anything:

- `pkg/attention-item.go` — the `Item` struct. The fields this prompt reads are `State`, `AnsweredAt`, `AnsweredBy`, `AnsweredClient`, `ResolvedBy`, `Answer` and `Answers`. Note the doc comment on `AnsweredClient` (store-written, derived from the request) and on `ResolvedBy` (a caller declaration, not a proof).
- `pkg/answered-client.go` — `AnsweredClient` and its `Automation *bool`. It is a pointer so that absent is distinguishable from false; the ladder must treat absent and false alike.
- `pkg/answer.go` and `pkg/answer-kind.go` — `Answer.Kind` and the four `AnswerKind` values (`option`, `skip`, `text`, `hidden`).
- `pkg/attention-state.go` — `OpenState`, `AnsweredState`, `ClosedState`.
- `pkg/producer-kind.go` — `SessionProducerKind` is `"session"`, the value the vector fixture uses.
- `pkg/pkg_suite_test.go` — the suite is Ginkgo-only: exactly one `func TestSuite` (line 17), every spec a `var _ = Describe(...)`. A `go test -run Test<Name>` selects nothing and exits 0, which is why every evidence command below focuses the suite and asserts a non-zero spec count.
- `pkg/attention-watcher-job_test.go` — the house spec style: `package pkg_test`, dot-imported Ginkgo/Gomega, local fakes declared beside the specs (`recordingCarrier`), `mocks.AttentionStore` for the store.
- `pkg/completed-task-source_test.go` — the house pattern for reading a committed fixture out of `pkg/testdata/`.
- `docs/attention-item-timeline.md` and `docs/attention-item-rating.md` — the house shape for a contract doc in this repo (a short H1, then the contract, the fields it reads and why each rule exists).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-licensing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. The classifier — `pkg/answered-signal.go`

1. Create `pkg/answered-signal.go` in `package pkg` with the repo's BSD license header, copied verbatim from the top of `pkg/answer.go` (the header `addlicense` enforces is `// Copyright (c) 2026 Benjamin Borbe All rights reserved.` plus the two following lines).

   ⚠️ **The classifier and the rendered-vocabulary mapping live in this one file and nowhere else.** A later acceptance criterion counts the non-test `pkg/*.go` files containing the string `AUTOMATED` and requires exactly `1`, so a second file that re-states the ladder or its vocabulary fails the spec.

2. Declare the verdict type and its six values, with the string values spelled exactly as below (they are the vocabulary the supervisor's guard table names):

   ```go
   // AttributionVerdict is what one card's answer can be attributed to.
   type AttributionVerdict string

   const (
       AttributionVerdictNothing      AttributionVerdict = "NOTHING"
       AttributionVerdictUnattributed AttributionVerdict = "UNATTRIBUTED"
       AttributionVerdictDeclined     AttributionVerdict = "DECLINED"
       AttributionVerdictHidden       AttributionVerdict = "HIDDEN"
       AttributionVerdictAutomated    AttributionVerdict = "AUTOMATED"
       AttributionVerdictAttributed   AttributionVerdict = "ATTRIBUTED"
   )
   ```

3. Declare an unexported predicate `wasActedOn(item Item) bool`, true when `item.AnsweredAt != nil`, **or** when `item.AnsweredBy != ""` and `item.State` is `AnsweredState` or `ClosedState`. Nothing else makes a card acted on.

4. Declare the classifier:

   ```go
   // ClassifyAnswerAttribution returns the verdict and a human-readable reason.
   func ClassifyAnswerAttribution(ctx context.Context, item Item) (AttributionVerdict, string)
   ```

   It evaluates these seven conditions **in this order** and returns at the first match. The order is the contract, not an optimisation:

   | # | condition | verdict |
   |---|---|---|
   | 1 | `!wasActedOn(item)` | `AttributionVerdictNothing` |
   | 2 | `item.State == ClosedState` **and** `item.AnsweredAt == nil` | `AttributionVerdictUnattributed` |
   | 3 | `item.Answer != nil` **and** `item.Answer.Kind == SkipAnswerKind` | `AttributionVerdictDeclined` |
   | 4 | `item.Answer != nil` **and** `item.Answer.Kind == HiddenAnswerKind` | `AttributionVerdictHidden` |
   | 5 | `item.AnsweredClient == nil` | `AttributionVerdictUnattributed` |
   | 6 | `item.AnsweredClient.Automation != nil` **and** `*item.AnsweredClient.Automation` | `AttributionVerdictAutomated` |
   | 7 | otherwise | `AttributionVerdictAttributed` |

   Every verdict, including `AttributionVerdictAttributed`, returns a non-empty reason naming the row that decided it. Row 6's reason names the automation flag, so a scripted click is diagnosable from the log alone. Row 7's reason states that a client was recorded with no automation flag; `item.ResolvedBy` is never read as a condition, and its presence or absence changes nothing.

   `ctx` is taken for consistency with the package's function shape and is not otherwise used (`revive`'s `unused-parameter` rule is excluded repo-wide in `.golangci.yml`).

5. Declare the rendered-vocabulary mapping, derived from the verdict and never stored:

   ```go
   // RenderedAnswerSignal returns the word the guard table renders for a verdict.
   func RenderedAnswerSignal(verdict AttributionVerdict) string
   ```

   `AttributionVerdictAttributed` → `ANSWERED`; `AttributionVerdictDeclined` → `DECLINED`; `AttributionVerdictHidden` → `HIDDEN`; every other value, including an unknown one, → `NOT_OPERATOR_ANSWERED`.

6. The doc comments carry the three regressions, because they are the reason the order is what it is. Put one short paragraph on the ordering (rows 3–4 before row 6), one on row 2, and one on row 7:
   - **Kind before client** (2026-10-20): a decline is a decline whether a person or a script clicked it. Asking "was this flagged automated?" first answered `ATTRIBUTED` for a card the operator explicitly dismissed.
   - **A close is a clear, not an answer** (2026-09-27): the board's Acknowledge closes a card with `answered_at` unset and nothing routed back, so releasing on it opens a gate nobody opened.
   - ⚠️ **`resolved_by` is NOT a requirement.** It was withdrawn 2026-09-27, hours after it shipped, because the board's JavaScript never sends it and so refused the operator's own board answer (the card read `answered` while the asking session stayed frozen). The `resolved_by` requirement in `permission-answer-poll.py` is a different script's rule and does not apply here.

<!-- OPEN QUESTION (for the operator, not a blocker): the spec's Assumptions say the vector fixture is "committed before any prompt is generated", but `pkg/testdata/answer-attribution-vectors.json` does not exist in this tree. This prompt therefore creates it from the spec's inlined ladder. If the fixture was meant to come from an authoritative external source, that source must be supplied before this prompt runs. -->

<!-- OPEN QUESTION (for the operator, not a blocker): the ported ladder reads `item.Answer.Kind` only, exactly as `answered-attribution.py` reads `answer.kind`. A multi-question item carries its kinds in `item.Answers` and `item.Answer` is nil (the two are mutually exclusive — see `Item.validateAnswers`), so a multi-question card whose answers are all `hidden` or all `skip` classifies ATTRIBUTED here. That is the faithful port and changing the definition is a stated non-goal, so this prompt does not widen it; the gap is recorded in the contract doc (requirement 12) instead. -->

### B. The vector fixture — `pkg/testdata/answer-attribution-vectors.json`

7. Create the fixture as a JSON array of objects with three keys each: `name` (stable, kebab-case), `verdict` (the expected `AttributionVerdict` string), and `item` (an `Item` in the struct's own JSON shape, so the spec can unmarshal it straight into `pkg.Item`). Timestamps are fixed RFC3339 instants, never relative forms, so the fixture is deterministic.

   It carries **ten** rows: one per verdict, plus the boundary rows. Write exactly these:

   | name | state | answered_at | answered_by | answered_client | answer | resolved_by | expected verdict |
   |---|---|---|---|---|---|---|---|
   | `nothing-open-card` | `open` | absent | absent | absent | absent | absent | `NOTHING` |
   | `nothing-answered-by-on-open` | `open` | absent | `board` | absent | absent | absent | `NOTHING` |
   | `unattributed-close-without-answered-at` | `closed` | absent | `board` | absent | absent | absent | `UNATTRIBUTED` |
   | `unattributed-no-client` | `answered` | present | `board` | absent | `{"kind":"text","value":"yes"}` | absent | `UNATTRIBUTED` |
   | `declined-skip` | `answered` | present | `board` | `{"automation":false}` | `{"kind":"skip"}` | absent | `DECLINED` |
   | `declined-skip-by-automation` | `answered` | present | `board` | `{"automation":true}` | `{"kind":"skip"}` | absent | `DECLINED` |
   | `hidden` | `answered` | present | `board` | `{"automation":false}` | `{"kind":"hidden"}` | absent | `HIDDEN` |
   | `automated-scripted-click` | `answered` | present | `board` | `{"automation":true}` | `{"kind":"option","value":"yes"}` | absent | `AUTOMATED` |
   | `attributed-board-click` | `answered` | present | `board` | `{"user_agent":"Mozilla/5.0","remote_addr":"127.0.0.1"}` | `{"kind":"option","value":"yes"}` | absent | `ATTRIBUTED` |
   | `attributed-with-resolved-by` | `answered` | present | `board` | `{"user_agent":"Mozilla/5.0","remote_addr":"127.0.0.1"}` | `{"kind":"option","value":"yes"}` | a session id | `ATTRIBUTED` |

   Every row also carries the fields `Item` needs to read as a real record: `item_id` (unique per row), `producer_id` (a UUID), `producer_kind` `"session"`, `liveness_ref` `"session:<same uuid>"`, `dedup_key`, `interrupt_class` `"pick"`, `payload` `"vector"`, `answer_mechanism` `"message"`, and `created_at` an RFC3339 instant earlier than that row's `answered_at`. The three boundary rows are `unattributed-close-without-answered-at` (a close is not an answer), `declined-skip-by-automation` (kind wins over the automation flag) and `attributed-with-resolved-by` (a present `resolved_by` changes nothing).

### C. The spec — `pkg/answered-signal_test.go`

8. Create `pkg/answered-signal_test.go` in `package pkg_test`, following `pkg/attention-watcher-job_test.go`'s style. Its `Describe` text **must contain** the token `AnsweredVersusNotOperatorAnsweredDeclinedHidden` — the evidence command focuses on that string, and a spec whose text lacks it runs zero specs.

   Add a local struct and loader:

   ```go
   type attributionVector struct {
       Name    string   `json:"name"`
       Verdict string   `json:"verdict"`
       Item    pkg.Item `json:"item"`
   }
   ```

   Read `testdata/answer-attribution-vectors.json` with `os.ReadFile` and `json.Unmarshal` in a `BeforeEach`, the way `pkg/completed-task-source_test.go` reads its fixture. `pkg.Item`'s `AnsweredAt` is a `*libtime.DateTime`, which implements `UnmarshalJSON`, so no manual timestamp parsing is needed.

9. The specs:

   a. **Every row classifies as declared, with a non-empty reason.** Iterate the loaded rows and assert `ClassifyAnswerAttribution` returns `pkg.AttributionVerdict(row.Verdict)` and a non-empty reason, naming the row in the failure message so a regression reports which vector broke.
   b. **Every verdict is covered.** Assert the set of verdicts across the rows contains all six `AttributionVerdict` values, so deleting a row cannot silently shrink coverage.
   c. **The three boundary rows are present by name.** Assert the loaded names contain `unattributed-close-without-answered-at`, `declined-skip-by-automation` and `attributed-with-resolved-by`. This is what makes a deleted boundary row fail rather than merely reduce a count.
   d. **A scripted click is never the operator's answer.** Select the row named `automated-scripted-click` and assert its verdict is `AUTOMATED` and is not `ATTRIBUTED`. Assert the reason mentions automation, so the failure is diagnosable.
   e. **The rendered vocabulary is derived.** Assert `RenderedAnswerSignal` maps `ATTRIBUTED` → `ANSWERED`, `DECLINED` → `DECLINED`, `HIDDEN` → `HIDDEN`, and each of `NOTHING`, `UNATTRIBUTED`, `AUTOMATED` → `NOT_OPERATOR_ANSWERED`.
   f. **`resolved_by` is not required.** Assert `attributed-board-click` (no `resolved_by`) and `attributed-with-resolved-by` (a present one) both classify `ATTRIBUTED`, so adding a `resolved_by` requirement fails here.

### D. The contract doc — `docs/answer-attribution.md`

10. Create `docs/answer-attribution.md` with an H1 and the repo's doc shape (`docs/attention-item-timeline.md` is the exemplar). It states:
    - the seven-row ladder, as an ordered table, with the six verdicts;
    - the fields it reads, each named: `state`, `answered_at`, `answered_by`, `answered_client` (and its `automation` member), `resolved_by`, `answer.kind` — and, for each, what the ladder concludes from it;
    - the rendered vocabulary and its mapping onto the verdicts;
    - the three documented regressions, with their dates.

11. The doc must contain these four literal strings, which a verification step greps for: `was_acted_on`, `answered_client`, `resolved_by`, `kind before client`. Use `was_acted_on` as the name of the predicate when describing the ladder (it is the source function's name and the shared vocabulary between the two repos), even though the Go identifier is `wasActedOn`.

12. Record the multi-question gap in the doc in one short paragraph: a multi-question item carries its kinds in `answers` and leaves `answer` nil, so the ported ladder — which reads `answer.kind`, exactly as the source does — classifies such a card on its `answered_client` alone. State that changing the definition is out of scope for this spec.

13. Before you finish: re-run the `<verification>` block and confirm every command passes, then walk each requirement above against the change and report any you could not satisfy.
</requirements>

<constraints>
- The ladder is a **port** of `answered-attribution.py`, inlined in the spec. Changing what counts as an operator answer is out of scope; if a row looks wrong, report it, do not adjust it.
- The ladder is a decision function over the item model already in the tree. **Add no data model**: no new field on `Item`, no new store record, no new bucket.
- The classifier and the rendered-vocabulary mapping live in exactly one non-test file.
- `resolved_by` is never a requirement, and no branch may read it.
- `answered_client.automation` absent and `false` are treated alike; only `true` is automated.
- The vector fixture is committed with the change; the spec reads it from disk rather than building items inline.
- Reuse the injected clock pattern only where time is read — this prompt reads no clock at all. Never call `time.Now()`.
- New files carry the repo's license header.
- Do **not** touch `pkg/attention-watcher-job.go`, `pkg/headless-answer.go`, `pkg/menu-answer.go`, `pkg/routine-gate-settle-job.go`, `main.go` or `CHANGELOG.md` — the next prompt owns the wiring and the changelog.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. ⚠️ `pkg/` is Ginkgo-only: `-run TestSuite` selects the single suite and `-ginkgo.focus` narrows it. A focus that matches nothing still exits 0, so **every evidence command asserts a non-zero spec count** — the `Ran [1-9]` grep is the assertion, not decoration.

- `go test ./pkg -count=1 -run TestSuite -ginkgo.focus='AnsweredVersusNotOperatorAnsweredDeclinedHidden' -v 2>&1 | grep -E 'Ran [1-9][0-9]* of [0-9]+ Specs'` — prints a `Ran N of M Specs` line with `N >= 1`.
- `test -f pkg/testdata/answer-attribution-vectors.json` — the fixture exists.
- `grep -c '"name"' pkg/testdata/answer-attribution-vectors.json` — prints `10`, one per row.
- `test -f docs/answer-attribution.md` — the contract has its doc home.
- `grep -q 'was_acted_on' docs/answer-attribution.md` — succeeds.
- `grep -q 'answered_client' docs/answer-attribution.md` — succeeds.
- `grep -q 'resolved_by' docs/answer-attribution.md` — succeeds.
- `grep -q 'kind before client' docs/answer-attribution.md` — succeeds.
- `grep -rl 'AUTOMATED' pkg/*.go | grep -v '_test.go' | wc -l` — prints `1`. That one file is `pkg/answered-signal.go`; a second means the vocabulary was duplicated.
- `! grep -q 'AUTOMATED' pkg/answer.go pkg/attention-item.go pkg/answered-client.go` — succeeds; the ladder does not leak into the data-model files.
- `ROOTDIR=/workspace make test` — the whole suite passes. `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve `ROOTDIR`; the explicit value is required or every `make` target dies before it runs.
- `ROOTDIR=/workspace make precommit` — must exit 0.
</verification>
