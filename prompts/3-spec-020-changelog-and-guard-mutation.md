---
spec: ["020-answered-signal-folded-into-delivery-arm"]
status: draft
created: "2026-11-18T07:36:43Z"
---

# Record the change and prove every guard fails when deleted

<summary>
- The change is recorded in the changelog under the unreleased section, as the repo's bookkeeping requires.
- Every guard this change introduces is proven by deleting it and watching a named spec fail.
- The evidence for each guard is a quoted failing line, not an assertion that the test "looks right".
- The tree is left exactly as it was found: each mutation is reverted and the suite runs green afterwards.
- The attribution contract, the ladder and the delivery wiring are unchanged by this prompt.
</summary>

<objective>
Close out the answered-signal change by recording it in the changelog and by proving, guard by guard, that the new specs actually fail when the guard they cover is deleted. A guard whose deletion leaves the suite green is not covered, and this prompt is where that is discovered rather than after the merge.
</objective>

<context>
**Dependency: `prompts/2-spec-020-signal-fold-into-arms.md` must already be complete.** This prompt assumes `pkg/answered-signal.go` carries the classifier, the rendered vocabulary, the recorder and the signaller, that the four arms call it, and that all seven named specs pass. If any of that is absent, STOP and report `status: failed` naming the missing prompt.

There is no `CLAUDE.md` in this repo — follow the coding plugin docs listed below, and read `docs/dod.md`.

Read before starting:

- `specs/in-progress/020-answered-signal-folded-into-delivery-arm.md` — § Verification → Guard mutation is the procedure this prompt executes; § Constraints requires the changelog bullet.
- `CHANGELOG.md` — its head only. Read it before editing: there is **no** `## Unreleased` section at the head of this tree today (the newest heading is `## v0.95.0`), so this prompt creates it. Never create a second one.
- `pkg/answered-signal.go` — the file every mutation in requirement 4 edits, and the one you must restore byte-for-byte.
- `docs/answer-attribution.md` — the contract the changelog bullet points at. Do not edit it here.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. The changelog bullet

1. Add one bullet to `CHANGELOG.md`, following `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`. Since no `## Unreleased` heading exists, create it immediately below the intro block (the paragraph and the three Semantic Versioning bullets that end with the PATCH line) and immediately above `## v0.95.0`:

   ```markdown
   ## Unreleased

   - feat: The answered-card signal is now produced by this service's own delivery arms instead of a per-session Python watcher. An operator answer emits exactly one `ANSWERED <item-id>` line for the session that asked, from whichever arm carried it — pane, headless, menu or routine-gate settle — and the line rides the existing two-second delivery tick, so no second timer is introduced. The signal is read from the card's own state and `producer_id` and gated by the ported attribution ladder: a scripted click reads `AUTOMATED`, a close with no answer reads `UNATTRIBUTED`, a decline reads `DECLINED` and a hidden answer reads `HIDDEN`, and none of them emits. One answered card therefore produces one wake where two channels previously notified the same session. Contract: `docs/answer-attribution.md`.
   ```

   Do not create a second `## Unreleased` heading. Do not add a version bump. Do not reorder or edit any released section. The bullet's prefix is `feat:` and its opening words are exactly `The answered-card signal`, because a verification step locates the bullet by that phrase.

### B. Guard-mutation evidence

2. Before the first mutation, take one backup of the file you will mutate:

   ```bash
   cp pkg/answered-signal.go /tmp/answered-signal.go.bak
   ```

   Every mutation is applied with a targeted edit to `pkg/answered-signal.go` (or, for the two guards that live elsewhere, the file named in the table), the focused spec is run, the failing line is captured, and the file is restored with `cp /tmp/answered-signal.go.bak pkg/answered-signal.go` — or, for the two guards outside that file, with a backup taken the same way before that mutation.

3. For each guard in the table below: apply the mutation, run the focused spec, quote **the failing line verbatim** in your completion report, then restore the file before moving to the next guard. A guard whose deletion leaves the suite green is **not covered**: report it as a finding naming the guard and the spec, and do not paper over it by editing the spec.

   | # | guard | file | mutation | spec that must fail |
   |---|---|---|---|---|
   | 1 | kind before client | `pkg/answered-signal.go` | move the `skip` and `hidden` checks to *after* the automation check | `AnsweredVersusNotOperatorAnsweredDeclinedHidden` |
   | 2 | a close is a clear | `pkg/answered-signal.go` | delete the `ClosedState`-without-`AnsweredAt` branch | `AnsweredVersusNotOperatorAnsweredDeclinedHidden` |
   | 3 | `resolved_by` is not required | `pkg/answered-signal.go` | add `if item.ResolvedBy == "" { return AttributionVerdictUnattributed }` immediately before the final attributed return | `AnsweredVersusNotOperatorAnsweredDeclinedHidden` |
   | 4 | a scripted click is never attributed | `pkg/answered-signal.go` | delete the `Automation` branch | `AnsweredVersusNotOperatorAnsweredDeclinedHidden` |
   | 5 | producer scoping | `pkg/answered-signal.go` | delete the `ProducerID != sessionID` decline in `Signal` | `AnsweredReadFromItemStateNotOpenList` |
   | 6 | only `ATTRIBUTED` emits | `pkg/answered-signal.go` | remove the `verdict != AttributionVerdictAttributed` decline in `Signal` | `AnsweredReadFromItemStateNotOpenList` |
   | 7 | at most one signal per item | `pkg/answered-signal.go` | delete the already-signalled check under the mutex | `AnsweredSignalsOnceAcrossTicks` |
   | 8 | fail-open on a store read error | `pkg/attention-watcher-job.go` | make `readBoard` swallow the `ReadBoard` error and return an empty board with no error | `FailOpenOnErrorMalformedDeadline` |
   | 9 | one definition site for the vocabulary | `pkg/answered-signal.go` | copy the `RenderedAnswerSignal` switch into `pkg/answer.go` | `EveryDeliveryArmCallsSameHelper` |
   | 10 | the pane arm still skips menu cards | `pkg/attention-watcher-job.go` | delete the `MenuQuestionsOf` branch in `deliverable` | `EveryDeliveryArmSignals` |

   For guard 8, take the backup of `pkg/attention-watcher-job.go` before the mutation and restore it from that backup. For guard 9, the mutation is an addition to a second file: delete the copied switch again after the run, and confirm `grep -rl 'AUTOMATED' pkg/*.go | grep -v '_test.go' | wc -l` prints `1` again.

4. After the last restore, prove the tree is unchanged and green:

   - `diff /tmp/answered-signal.go.bak pkg/answered-signal.go` prints nothing;
   - `grep -rl 'AUTOMATED' pkg/*.go | grep -v '_test.go' | wc -l` prints `1`;
   - `ROOTDIR=/workspace make test` passes.

5. Before you finish: re-run the `<verification>` block and confirm every command passes, then confirm each of the ten guards above has a quoted failing line in your report and none is reported as uncovered.
</requirements>

<constraints>
- The tree must be left exactly as found: every mutation reverted, no backup file inside the repo (use `/tmp`), and the full suite green at the end.
- A guard whose deletion leaves the suite green is a **finding to report**, never something to fix by weakening the spec.
- Do not change the ladder, the rendered vocabulary, the recorder, the signaller, the arm wiring or `docs/answer-attribution.md` in this prompt — only `CHANGELOG.md` is edited for real, and the source edits are temporary mutations.
- The changelog bullet's prefix is `feat:`; dark-factory reads the prefix to choose the version bump.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root.

- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`. A second heading means the section was duplicated.
- `awk '/^## /{sec=$0} /^- feat: The answered-card signal/{print "sits under: " sec}' CHANGELOG.md` — prints exactly one line, `sits under: ## Unreleased`. ⚠️ `grep -n 'The answered-card signal' CHANGELOG.md` alone cannot see placement: a bullet appended under `## v0.95.0` passes it.
- `diff /tmp/answered-signal.go.bak pkg/answered-signal.go` — prints nothing; the file was restored byte-for-byte after the last mutation.
- `grep -rl 'AUTOMATED' pkg/*.go | grep -v '_test.go' | wc -l` — prints `1`; the vocabulary was not duplicated into a second file by the guard-9 mutation.
- `ROOTDIR=/workspace make test` — the whole suite passes after every mutation was reverted. The explicit `ROOTDIR` is required in this container: `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve `ROOTDIR` and every `make` target dies before it runs.
- `ROOTDIR=/workspace make precommit` — must exit 0.

Operator-side verification is NOT part of this prompt: the merged-sha check, the `pgrep -fl 'answered-watch\.py'` count and the end-to-end wake count live in the spec's § Verification → Operator-executable ladder.
</verification>
