---
spec: ["022-permission-answer-hook-streams"]
status: draft
created: "2026-11-19T10:10:00Z"
---

# Permission answer hook: install step, stream doc, changelog and guard evidence

<summary>
- Installing the service also installs the hook, so the second binary is never a hand-copied leftover on one machine.
- The board's change stream gains a written contract outside any spec, so the next consumer cites one page instead of restating it.
- The baseline gap and the "re-read by id" rule are written down, because a consumer that subscribes and waits will silently miss an answer.
- The changelog records the change under an unreleased heading, since this repo does not cut releases automatically.
- Every guard is proven by deleting it and watching its own test fail, with the failing line quoted.
- The tree is restored and the full gate re-run after the last deletion, so the evidence cannot be produced by a broken working copy.
</summary>

<objective>
Finish the change: add the hook's own install step to the existing install recipe, give the stream's event contract a documentation home, add the unreleased changelog entry, and produce the per-guard mutation evidence that proves each guard is actually covered.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs listed below and read `docs/dod.md`.

Read the approved spec first: `specs/in-progress/022-permission-answer-hook-streams.md`. § Verification (all three rungs), § Constraints and § Suggested Decomposition row 5 are the contract for this prompt. **Implement it; do not redefine it.** If this prompt and the spec disagree, the spec wins and the disagreement is a finding to report.

Read before editing:

- `Makefile` — `INSTALL_BIN`, `CODESIGN_IDENTITY`, `LAUNCHD_LABEL`, `LAUNCHD_PLIST`, and the `install-bin` recipe. That recipe builds, signs, verifies and swaps the **service** binary with a per-shell unique stage path (`$$$$`) and a `.prev` retention; `install: install-bin` then runs `launchd-baseline` and `launchd-reload`. § Constraints: "install-bin builds only the main binary today, so the hook needs its own install step added to the same recipe — never a second hand-run copy."
- `docs/attention-item-timeline.md` and `docs/attention-item-rating.md` — the house shape for a contract doc in this repo: a short `#` heading, then the contract, the fields it reads, and why each rule exists.
- `pkg/handler/attention-stream.go` — the authoritative behaviour to document: `attentionStreamEvent` is `{type, item_id, html}`; `type` is `upsert` or `remove`; `html` is present on an upsert only; the baseline is the shared board taken at `:360` with the headers flushed at `:369-370`; `push` skips a row whose rendered HTML is unchanged (`:426-433`), so a change landing between the resolve and the subscribe emits no event.
- `CHANGELOG.md` — the top of the file. Its first section is `## v0.99.6`; there is **no** `## Unreleased` heading today, so this prompt creates one.
- `.dark-factory.yaml` — `autoRelease: false`, which is why the entry goes under `## Unreleased`.
- `cmd/permission-answer-hook/main.go` and `main_test.go` — the guards whose evidence you will produce.
- `specs/in-progress/020-answered-signal-folded-into-delivery-arm.md` and `specs/in-progress/021-manager-wake-in-process-job.md` — the two sibling consumers the doc exists for. Cite them by slug.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-makefile-commands.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
### A. The install step — `Makefile`

1. Declare the hook's install path beside `INSTALL_BIN`:

   ```make
   # The permission-answer hook is a second binary installed from this repo. It is
   # not the launchd service, so it carries no .prev rollback: a failed hook start
   # cannot take the board down.
   INSTALL_HOOK_BIN ?= $(HOME)/.local/bin/attention-controller-permission-answer-hook
   ```

   ⚠️ The file name is `attention-controller-permission-answer-hook` — the spec's operator-executable rung runs `~/.local/bin/attention-controller-permission-answer-hook`.

2. Extend the existing `install-bin` recipe with a **second** `@set -e; \` block, after the service binary's block and before the next target. Mirror the existing sequence exactly in shape, with its own per-shell unique stage path:

   ```make
   @set -e; \
       stage="$(INSTALL_HOOK_BIN).new.$$$$"; \
       trap 'rm -f "$$stage"' EXIT INT TERM; \
       go build -o "$$stage" ./cmd/permission-answer-hook; \
       codesign -f -s "$(CODESIGN_IDENTITY)" "$$stage"; \
       codesign --verify --strict "$$stage"; \
       mv -f "$$stage" "$(INSTALL_HOOK_BIN)"
   ```

   ⚠️ Keep the `$$$$`-unique staging path. The existing comment above `install-bin` records why: two concurrent installs sharing one staging name each `mv` the other's file and the loser installs nothing. The same hazard applies to the second binary.

   Do **not** add a `.prev` copy for the hook, do **not** touch `install`'s dependencies, and do **not** change the service binary's block.

3. Do not make the hook's build conditional on the service binary's success beyond the recipe's own `set -e` ordering: if the service build fails, `install-bin` fails and the hook is not installed, which is correct.

### B. The stream contract — `docs/attention-stream.md`

4. Create `docs/attention-stream.md` following the house doc shape. It must state, in this order:

   - **What the route is.** `GET /api/1.0/attention/stream`, a `text/event-stream` response on the board router, server-to-client only. Nothing is ever read from the connection.
   - **The event shape.** `{type, item_id, html}` — `type` is `upsert` or `remove`, `item_id` is the row the event is about, `html` is the rendered row and is present on an `upsert` only. Each event is framed as `data: <json>` followed by a blank line.
   - **It is a change signal, not the item.** A consumer that needs the item reads it back by id. The payload carries rendered HTML, not the record.
   - **The baseline.** The server takes a baseline snapshot of the shared board before it flushes the response headers, so a client that has received the headers is baselined. A change landing between the client's own read and the subscription is **still missed**: the diff emits only rows whose rendered HTML changed, so an unchanged row produces no event.
   - **What a consumer must therefore do.** Subscribe, then read the item once by id immediately after the subscription is established, and decide from that read. A consumer that only waits for an event silently misses an answer that landed in the gap.
   - **What the stream does not promise.** No keep-alive frames on an idle channel; no reconnection protocol; a dropped stream is the consumer's to handle, and a re-subscribe loop is a poll in disguise.
   - **The consumers.** Cite `020-answered-signal-folded-into-delivery-arm` and `021-manager-wake-in-process-job` by slug. ⚠️ **Do not assign this change a spec number in the doc** — the page documents the contract, not the change that wrote it down.

5. The doc must contain these literal strings, which a verification step greps for: `item_id`, `type`, `html`, `baseline`. Use `item_id` (the wire name) rather than a Go field name when describing the event.

### C. The changelog — `CHANGELOG.md`

6. Add a `## Unreleased` section at the top of the file, immediately after the preamble and above `## v0.99.6`, with one bullet in the repo's existing style (`- feat: **<headline>.** <body>`):

   - the headline names the outcome: the parked-permission answer is delivered by a stream subscription instead of a 2 s poll;
   - the body states: the hook is a Go binary in this repo at `cmd/permission-answer-hook/` replacing `permission-answer-poll.py` — ⚠️ **the literal `permission-answer-hook` must appear in the bullet**, because the verification `awk` keys on it; a bullet that says only "the hook is a Go binary" makes the check find no match and the correct implementation reads as a failure; it resolves its session's card in a bounded window, opens exactly one `/api/1.0/attention/stream` subscription, reads the item once after the baseline and once per matching event, and never reads the list again; all six guards from the task's guard table survive plus the four selection guards (permission-class only, ambiguity refusal, the `expect` payload prefix and `not_before`); an in-pane answer is recognised from the producer's state file and the hook exits without racing it; the delivery-attempt record the tab path's `received_at` reads is written after the decision; the stdout contract is byte-identical and proven by golden fixtures;
   - it names the cross-repo dependency: the `pgrep … permission-answer-poll.py` count reaches zero only once `bborbe/claude-supervisor` repoints its `PermissionRequest` hook at the new binary.

   ⚠️ Keep it one bullet. Do not reformat neighbouring sections, do not add a version heading, and do not touch any released section.

### D. The guard-mutation evidence

7. For **each** of the eighteen guards below, run the procedure and record the result:

   ```
   TestHookPayloadReadFromStdin
   TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy
   TestDenyNeverInferred
   TestKillSwitchSupervisorPermissionPollOff
   TestHeadlessWorkerSkipped
   TestPermissionClassOnly
   TestTwoPermissionCardsAreAmbiguous
   TestStaleCardForAnotherGateIsRefused
   TestCardOlderThanTheHookIsRefused
   TestOneSubscriptionNoListRepoll
   TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap
   TestResolveWindowExpiresQuietly
   TestFirstAnswerWinsOverInPane
   TestUntrackedGateAbandonedAfterGrace
   TestLocalCloseReadsProducerStateFile
   TestFailOpenOnErrorMalformedDeadline
   TestStdoutContractGolden
   TestDeliveryAttemptRecordedAfterEmit
   ```

   Procedure, per guard:
   1. Copy `cmd/permission-answer-hook/main.go` to `/tmp/guard-backup.go`.
   2. Delete the branch that implements the guard (the condition, the filter, the suppression, the record call, the write — whichever line the guard exists to prove).
   3. Run the guard's own evidence command, in the same form the acceptance criteria give it: `go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus='<Guard>' -ginkgo.fail-on-empty`.
   4. **Quote the failing line** it prints.
   5. Restore with `cp /tmp/guard-backup.go cmd/permission-answer-hook/main.go`.

   ⚠️ **A deletion that leaves the suite green is not covered.** If the guard's command still exits 0 with the branch deleted, the test does not prove the guard: strengthen the test, then re-run the deletion. Report every such case.

   ⚠️ `-ginkgo.fail-on-empty` is what makes a focus that stopped matching fail instead of passing on nothing. Never drop it from a mutation run.

   Do not use `git` for the mutation — `workflow: worktree` masks `.git` in this container. Copy and restore the file, and after the last guard confirm the restored file is byte-identical to the backup (`diff /tmp/guard-backup.go cmd/permission-answer-hook/main.go` prints nothing).

### E. Report

8. In the completion report, include a table: guard name, the file and symbol whose branch you deleted, the failing line quoted, and `restored`. Name any guard whose deletion did not fail its test, with what you changed to make it fail.

9. Before you finish, re-run the `<verification>` block and confirm every command passes, then walk each requirement above against the change and report any you could not satisfy.
</requirements>

<constraints>
- The binary lives in `cmd/permission-answer-hook/` beside `deploy`, `import-spawn-ledger` and `attention-log-event`, and is installed by the same `install-bin` recipe — never a second hand-run copy.
- `docs/attention-stream.md` documents the `{type, item_id, html}` event contract and the baseline-gap behaviour, so the sibling specs cite one page instead of restating it. Do not cite a spec number for this change in the doc.
- The change carries a `## Unreleased` bullet in `CHANGELOG.md`; `.dark-factory.yaml` sets `autoRelease: false`, so no version heading is added.
- Every guard in the spec's Acceptance Criteria must be executed by name, and a guard whose deletion does not fail its test is not covered.
- No `time.Sleep`, no `time.NewTicker`, no `DEFAULT_INTERVAL` anywhere in `cmd/permission-answer-hook/`.
- Do not change the hook's behaviour in this prompt. If a mutation reveals a missing guard, strengthen the test; do not weaken the behaviour.
- Do not change the item model: no new field on `pkg.Item`, no new store record, no new bucket.
- Do not change the service binary's install block, `install`'s dependencies, or the launchd targets.
- Do not add a scenario file.
- New files carry the repo's license header.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. ⚠️ `workflow: worktree` masks `.git`, so no `git` command may appear here and every `make` call needs an explicit `ROOTDIR`.

- `grep -q 'INSTALL_HOOK_BIN' Makefile` — exits 0.
- `grep -q 'attention-controller-permission-answer-hook' Makefile` — exits 0.
- `grep -q './cmd/permission-answer-hook' Makefile` — exits 0 (the hook is built by the recipe, not copied by hand).
- `test -f docs/attention-stream.md` — the contract has its doc home.
- `grep -q 'item_id' docs/attention-stream.md` — exits 0.
- `grep -q 'baseline' docs/attention-stream.md` — exits 0.
- `grep -q 'html' docs/attention-stream.md` — exits 0.
- `! grep -q '022-permission-answer-hook' docs/attention-stream.md` — exits 0 (the page does not cite this change's spec number).
- `[ "$(awk '/^## /{sec=$0} /permission-answer-hook/{print sec; exit}' CHANGELOG.md)" = "## Unreleased" ]` — exits 0 **only** when the hook bullet sits under `## Unreleased`. (Section-walking `awk`, never a line-scoped `grep -A` window, which swallows neighbouring sections and reports a correct file as wrong. ⚠️ And a **string comparison**, never a pipe to `head -1`: `head` exits 0 on empty input, so the piped form passes with the bullet absent entirely — the exact false positive this check exists to prevent.)
- `set -e; for g in TestHookPayloadReadFromStdin TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy TestDenyNeverInferred TestKillSwitchSupervisorPermissionPollOff TestHeadlessWorkerSkipped TestPermissionClassOnly TestTwoPermissionCardsAreAmbiguous TestStaleCardForAnotherGateIsRefused TestCardOlderThanTheHookIsRefused TestOneSubscriptionNoListRepoll TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap TestResolveWindowExpiresQuietly TestFirstAnswerWinsOverInPane TestUntrackedGateAbandonedAfterGrace TestLocalCloseReadsProducerStateFile TestFailOpenOnErrorMalformedDeadline TestStdoutContractGolden TestDeliveryAttemptRecordedAfterEmit; do go test ./cmd/permission-answer-hook/... -count=1 -run TestSuite -ginkgo.focus="$g" -ginkgo.fail-on-empty; done` — exits 0, every guard present and matching.
- `diff /tmp/guard-backup.go cmd/permission-answer-hook/main.go` — prints nothing (the tree is restored after the mutation runs).
- `! grep -rqE 'time\.Sleep|NewTicker|DEFAULT_INTERVAL' cmd/permission-answer-hook/` — exits 0.
- `ROOTDIR=/workspace make test` — the whole suite passes.
- `ROOTDIR=/workspace make precommit` — must exit 0.

## Open questions

- **The codesign identity for the hook is a judgement call.** The service binary is signed because macOS privacy control keys the vault grant to its designated requirement; the hook reads no vault and needs no grant. This prompt signs it with the same identity for consistency with the recipe. If the reviewer prefers an ad-hoc signature or none, the recipe line changes and nothing else does.
- **`make install` runs `launchd-reload`, which restarts the service.** The hook's install step adds no restart of its own — a running hook is a short-lived process the harness re-invokes. If the operator expects the hook's install to take effect immediately, note that it already does: the next `PermissionRequest` invokes the newly installed path.
</verification>
