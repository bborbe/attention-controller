---
status: approved
spec: [022-permission-answer-hook-streams]
created: "2026-11-19T09:25:00Z"
queued: "2026-11-19T00:39:59Z"
---

# Permission answer hook: the four selection guards

<summary>
- The hook picks the one card its own session posted, and refuses to pick when the choice would be a guess.
- A question card is never treated as a permission answer, however closely it matches.
- Two permission cards for the same session settle nothing: the hook reports the ambiguity instead of answering one of them.
- A card left open by an earlier gate is rejected by the text the card carries, so a stale card cannot release this gate.
- A card created before the hook started is rejected by its own timestamp, with a five-second tolerance for clock skew.
- Each refusal has a positive control beside it, so no filter can quietly become a refuse-everything rule.
</summary>

<objective>
Add the item-selection function to `cmd/permission-answer-hook` with all four of the script's guards — permission-class only, refusal on more than one candidate, the `expect` payload prefix and `not_before` — so the hook resolves its own session's card and settles nothing when it would have to guess.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs listed below and read `docs/dod.md`.

Read the approved spec first: `specs/in-progress/022-permission-answer-hook-streams.md`. § Desired Behavior 4 and § Constraints ("Selection keeps all four of the script's guards") are the contract for this prompt. **Implement it; do not redefine it.** If this prompt and the spec disagree, the spec wins and the disagreement is a finding to report.

This prompt extends the package created by the previous prompt. Read it first, then read:

- `cmd/permission-answer-hook/main.go` — the constants, `hookInput`, `parseHookInput`, `storeClient`, `permissionVerdict` and `run` you are extending.
- `cmd/permission-answer-hook/main_test.go` — the `recordingClient`, the fake clock and the `hookOptions` helper you extend.
- `pkg/attention-item.go` — `Item.AnswerMechanism`, `Item.ProducerID`, `Item.Payload`, `Item.CreatedAt` (`libtime.DateTime`, whose `.Time()` returns a `time.Time`), and `Item.ItemID`.
- `pkg/answer-mechanism.go` — `PermissionAnswerMechanism` (`"permission"`), `MessageAnswerMechanism` (`"message"`), `AckAnswerMechanism` (`"ack"`).
- `pkg/attention-store.go` — `type Items []Item`.
- `pkg/attention-asked-at_test.go` — the house shape for building `pkg.Item` fixtures in a spec, including `libtime.DateTime(now)` conversions.
- `specs/in-progress/022-permission-answer-hook-streams.md` § Desired Behavior 4, whose ⚠️ records why the ambiguity refusal exists: measured 2026-09-27, an in-pane approval left its store card `open` for 9+ minutes, and releasing THIS gate under another card's subject is the failure that rule prevents.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-filter-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. The selector

1. Declare the selection function in `cmd/permission-answer-hook/main.go`:

   ```go
   // selectItem returns the one card this hook invocation answers, or a zero item
   // and a reason naming why no single card could be chosen.
   //
   // notBefore is when this hook invocation started; a card created before
   // notBefore minus notBeforeTolerance is from an earlier gate and is refused.
   // expect is the "<tool>: <detail>" prefix from the stdin payload, or "" when
   // the invocation carried none.
   func selectItem(
       items pkg.Items,
       producerID string,
       expect string,
       notBefore time.Time,
   ) (pkg.Item, string)
   ```

2. Apply the four filters **in this order**, keeping every candidate rather than the newest:

   | # | filter | refuses when |
   |---|---|---|
   | 1 | `item.AnswerMechanism == pkg.PermissionAnswerMechanism` | the item is a `message` or `ack` card |
   | 2 | `item.ProducerID == pkg.ProducerID(producerID)` | the item belongs to another session |
   | 3 | `expect == ""` **or** `strings.HasPrefix(string(item.Payload), expect)` | the card's payload does not start with the expected text |
   | 4 | `!item.CreatedAt.Time().Before(notBefore.Add(-notBeforeTolerance))` | the card predates the hook, past the tolerance |

   ⚠️ Filter 3 is inactive when `expect` is empty — that is the `--session-id` re-run path, which carries no prefix. It must not become a refuse-all.

3. Return values:
   - exactly one survivor → that item, empty reason;
   - zero survivors → zero `pkg.Item`, a reason naming the producer and stating no matching permission item was found;
   - more than one survivor → zero `pkg.Item`, a reason **containing the word `ambiguous`** and the candidate count. ⚠️ The hook must not settle one of them: an in-pane approval that left a store card open must never release a different gate.

4. Do not relax any guard to "newest wins". The newest card is not evidence that it is the right card.

### B. Wire it into `run`

5. Extend `run`'s sequence, after the `parseHookInput` step, to: build the not-before instant from `opts.Clock.Now()`, call `opts.Client.List(ctx)`, call `selectItem`, and on a non-empty reason write `permission-answer-hook: <reason>` to `opts.Stderr` and return nil. On a store error, write the wrapped error to stderr and return nil (fail-open — § Failure Modes row "The store is unreachable").

   This is a **single** list read. The bounded retry around it is a later prompt's subject; do not add a loop or a timer here.

6. Do not emit a decision yet and do not write to `opts.Stdout` — the byte-exact stdout contract is a later prompt's subject. `run` may pass the selected item no further than a variable for now.

### C. The specs — `cmd/permission-answer-hook/main_test.go`

7. Add four specs to the existing test file. ⚠️ **Every spec's `Describe` text must contain the guard token verbatim**, because the evidence command focuses on that token and `-ginkgo.fail-on-empty` is what turns a non-matching focus into a failure.

   Build fixtures with a helper that takes the fields a case cares about and fills the rest with values that satisfy `pkg.Item.Validate`'s shape (`ItemID`, `ProducerID`, `ProducerKind`, `LivenessRef`, `DedupKey`, `InterruptClass`, `Payload`, `AnswerMechanism`, `State`, `CreatedAt`) — follow `pkg/attention-asked-at_test.go`'s fixture style. Use a fixed `notBefore` instant and fixed `CreatedAt` instants, never a live clock.

8. **`TestPermissionClassOnly`** — a `pkg.MessageAnswerMechanism` item for the session is never selected: with only that item present, `selectItem` returns a zero item and a reason. **Positive control:** the same item with `AnswerMechanism: pkg.PermissionAnswerMechanism` is selected. Assert the control explicitly, so the filter cannot become a refuse-all.

9. **`TestTwoPermissionCardsAreAmbiguous`** — two permission-class items for the same producer, both matching `expect` and both created after `notBefore`, return a zero item and a reason containing `ambiguous`. Assert the returned `ItemID` is empty, so a failure reports that a card was settled rather than merely that the reason was wrong. **Positive control:** drop one of the two and the survivor is selected.

10. **`TestStaleCardForAnotherGateIsRefused`** — with `expect == "Write: /tmp/w.txt"`, a permission card whose `Payload` is `"Bash: rm -rf /tmp/x"` is refused (zero item, non-empty reason). **Positive control:** the same card with `Payload: "Write: /tmp/w.txt — create the file"` is selected. Add a third case: `expect == ""` selects the card regardless of payload, so the filter is provably inactive on the argv path.

11. **`TestCardOlderThanTheHookIsRefused`** — with `notBefore = T`, a card whose `CreatedAt` is `T - 30s` is refused. **Boundary cases:** a card at exactly `T - notBeforeTolerance` (5 s before) is selected, and a card at `T - notBeforeTolerance - 1s` is refused. Assert the tolerance boundary on both sides, because an off-by-one here silently drops every legitimate card or admits every stale one.

12. Before you finish, re-run the `<verification>` block and confirm every command passes, then walk each requirement above against the change and report any you could not satisfy.
</requirements>

<constraints>
- Selection keeps all four of the script's guards: permission-class only, refusal on more than one candidate, the `expect` payload prefix, and `not_before`. None is relaxed to "newest wins".
- The `expect` prefix is derived from the stdin body and nowhere else; an invocation with no stdin payload carries no prefix and the prefix filter is inactive.
- `notBeforeTolerance` is `5 * time.Second`, declared beside the hook.
- More than one candidate settles **nothing** — no decision, exit 0, and the reason names the count and contains `ambiguous`.
- The list read is a single call in this prompt; no retry loop, no timer, no `time.Sleep`.
- No `time.Sleep`, no `time.NewTicker`, no `DEFAULT_INTERVAL` anywhere in `cmd/permission-answer-hook/` — including the test file.
- Do not change the item model: no new field on `pkg.Item`, no new store record, no new bucket.
- Do not write to stdout and do not post an attempt record — later prompts own both.
- Do **not** touch the repo-root `main.go` (the board server), `Makefile`, `CHANGELOG.md` or `docs/` — a later prompt owns those. You **do** edit `cmd/permission-answer-hook/main.go`, which is this prompt's target.
- Do not add a scenario file; the package's own specs cover this with injected seams.
- New files carry the repo's license header.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. ⚠️ The package is Ginkgo-only: `-run TestSuite` selects the single suite, `-ginkgo.focus` narrows it, and `-ginkgo.fail-on-empty` makes a focus that matches nothing fail instead of exiting 0.

- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestPermissionClassOnly' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestTwoPermissionCardsAreAmbiguous' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestStaleCardForAnotherGateIsRefused' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestCardOlderThanTheHookIsRefused' -ginkgo.fail-on-empty`
- `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='TestKillSwitchSupervisorPermissionPollOff' -ginkgo.fail-on-empty` — the previous prompt's guard still passes.
- `! grep -rqE 'time\.Sleep|NewTicker|DEFAULT_INTERVAL' cmd/permission-answer-hook/` — exits 0.
- `grep -q 'ambiguous' cmd/permission-answer-hook/main.go` — exits 0 (the refusal reason is in the selector, not only in a spec).
- `ROOTDIR=/workspace make test` — the whole suite passes. The explicit `ROOTDIR` is required: `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve `ROOTDIR`.
- `ROOTDIR=/workspace make precommit` — must exit 0.
</verification>
