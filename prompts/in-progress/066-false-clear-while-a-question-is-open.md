---
status: approved
created: "2026-11-26T23:05:00Z"
queued: "2026-11-26T20:21:59Z"
---

# Stop reporting a session cleared while it holds an open store item

<summary>
- A manager can be told a session's gate cleared while that session still holds an unanswered card in the attention store.
- The verdict that produces the "cleared" line reads only the session registry and the transcript's closer text; it never reads the store.
- Measured 2026-11-26 19:2x: `attention cleared: 8e25e9d1` was typed into the manager's pane while question `0aacef0cfcd0f05ad05f13285506a678` was open (age 20 s) and the session was live (heartbeat 11 s).
- After this change, a session holding any open store item is reported gated, not cleared, and a manager that trusts the line no longer drops a real park.
- No new item field, state or transition, and no change to how a missing card is re-posted.
</summary>

<objective>
Make "this session holds an open item in the attention store" outrank the two no-ask rows of the gate verdict, so a session parked on an unanswered card is never reported cleared.
</objective>

<context>
There is no `CLAUDE.md` in this repo — read `docs/dod.md` for the definition of done.

Read before writing anything:

- `pkg/manager-gate.go` — `managerGateInput` (`:67`), `managerGateVerdict` (`:84`), the ten ordered rows (row 5 `headless:no-ask` at `:107`, row 9 `idle:no-ask` at `:130`), and `ManagerGateRows` (`:148`), the table in evaluation order.
- `pkg/manager-transcript-source.go` — `GateStates` (`:70`), `gateState` (`:88`), and the `managerGateInput` literal at `:104`. This source has no store access today.
- `pkg/manager-wake-job.go` — `p.wakeTranscript` calls `source.GateStates(p.ctx, p.trackedFor(manager))` at `:708`; the pass holds the store's open items in `p.items`. `newPass` (`:340-346`) aborts the whole pass when the store read fails, so `wakeTranscript` never runs on a failed read.
- `pkg/provenance.go` — `sessionIDFromItem` (`:786`), which resolves an item to its session across all three liveness models (`session:`, `owner:`, `heartbeat:`) and a `session:`-prefixed producer id.
- `pkg/manager-gate_internal_test.go` — the verdict specs and `managerGateTestCases` (`:71`, "covers every row of the ten-row union"), the helpers `managerGateTestState` (`:23`) and `managerGateTestRecordsOfKind` (`:49`), and `expectGateTableMirrorsRows` (`:179`).
- `pkg/manager-gate-doc_internal_test.go` — `TestDocTableMatchesCode` (`:71`), which parses `docs/manager-gate-semantics.md` and compares it row for row against `ManagerGateRows`.
- `pkg/manager-gate-park.go` — `ManagerParkedReasons` (`:20`), the closed set that decides which reasons age.

Two facts that shape the design:

- The gate table has **three** mirrors that must stay byte-identical: `docs/manager-gate-semantics.md`, `ManagerGateRows` (`pkg/manager-gate.go:148`), and the literal slice inside `expectGateTableMirrorsRows` (`pkg/manager-gate_internal_test.go:180`). `TestDocTableMatchesCode` compares the doc against the Go table; `expectGateTableMirrorsRows` compares the test literal against it. Editing one fails the other two.
- `pkg/manager-gate.go:32` defines the cleared decision as "the session settled with no live ask". An open store item is a live ask; this change makes that definition true rather than widening it.
</context>

<requirements>
### A. Thread the open set into the verdict

1. Add two fields to `managerGateInput` (`pkg/manager-gate.go:67`): `SessionID string` and `OpenItems map[string]struct{}` — the set of session ids that hold at least one open store item.
2. Extend the `ManagerTranscriptSource` interface's `GateStates` (`pkg/manager-transcript-source.go:33`) and its implementation (`:70`) to accept that set and pass it through `gateState` into the `managerGateInput` literal at `:104`, alongside the session id. Document on the interface that a nil set means "the store was not consulted" and must reproduce today's rows exactly.
   Update the three existing calls in `pkg/manager-transcript-source_internal_test.go` (`:78`, `:90`, `:98`) to pass `nil`.
3. In `p.wakeTranscript` (`pkg/manager-wake-job.go:708`), build the set from `p.items` using `sessionIDFromItem`, skipping any item that resolves to `""`, and pass it to `GateStates`. `p.items` is never nil at this point — a failed read aborts the pass in `newPass` — so this is not a runtime nil path; the nil set is requirement 2's unit-level guard.

### B. The two rows lose to an open item

4. In `managerGateVerdict` (`pkg/manager-gate.go:84`), rows 5 and 9 return `ManagerGateGated` with reason `store:open-item` when the session holds an open item. Put the test in one package-level helper — `func managerGateOpenItem(in managerGateInput) bool { _, ok := in.OpenItems[in.SessionID]; return ok }` — and call it from both rows, so one mutation of the helper body (requirement 11) proves both rows at once. The call sits **after** each row's existing `managerIsAsk(in.CloserBody)` check (a closer ask keeps its own reason) and **before** its `ManagerGateCleared` return.
5. Do not add a row and do not reorder. Rows 1–4 and 6–10 are unchanged, including `unregistered`, `stuck:`, `closer-unknown`, `registry:waiting`, `busy+parked-modal` and `held:`.
6. The reason is `store:open-item`, not `store:open-question`: the gate reads a bare set of session ids, so it has no kind to name — `store:open-question` would claim a discriminator the gate never reads.

### C. All three mirrors, in one edit

7. Update `docs/manager-gate-semantics.md` rows 5 (`:24`) and 9 (`:28`), `ManagerGateRows` (`pkg/manager-gate.go:148`), and the literal slice in `expectGateTableMirrorsRows` (`pkg/manager-gate_internal_test.go:180`) with the same cells, row for row, adding nothing and reordering nothing. Use exactly:
   - row 5 → Condition `status is None and closer is not an ask`, Verdict `gated / cleared`, Reason `store:open-item / headless:no-ask`;
   - row 9 → Condition `status == "idle"`, Verdict `gated / cleared`, Reason `idle+closer / store:open-item / idle:no-ask`.

### D. Specs

8. In `pkg/manager-gate_internal_test.go`, add a `Describe("OpenItemOutranksNoAsk")` — that exact string is the `<verification>` focus — with these specs:
   - a `headless:no-ask` state (no registry status, heartbeat live, closer known and not an ask) whose session is in `OpenItems` returns `ManagerGateGated` with reason `store:open-item`;
   - the same for `idle:no-ask` (registry status `idle`, closer known and not an ask);
   - both rows still return `ManagerGateCleared` when `OpenItems` is nil — the guard that a caller which has not read the store cannot turn "unknown" into a gate;
   - a `registry:waiting` state whose session is in `OpenItems` still returns `ManagerGateGated` with reason `registry:waiting`, not `store:open-item`;
   - a headless session whose closer body IS an ask and whose session is in `OpenItems` returns `headless+closer`, and the idle equivalent returns `idle+closer` — the closer ask keeps its own reason, which is the precedence requirement 4 asserts.
   Add the two gated-variant cases for rows 5 and 9 (session in `OpenItems` → gated / `store:open-item`) beside the existing cleared cases in `managerGateTestCases` (`:71`).
9. Add one threading spec in `pkg/manager-transcript-source_internal_test.go`: call `GateStates` with a non-nil set containing the session id and assert the returned reason is `store:open-item`. Without it a verdict unit test can pass while `GateStates` drops the set.
   Also add one spec in `pkg/manager-wake-job_test.go` (it is `package pkg_test` and reaches only exported symbols): inject a `pkg.ManagerTranscriptSource` fake that records the `OpenItems` set it receives, seed the injected store so the pass holds an open item whose `sessionIDFromItem` resolves to that manager's tracked session plus one item that resolves to `""`, and drive one pass with `job.WakeOnce(ctx)` — `wakeTranscript`, `wake` and `p.items` are unexported and unreachable from `pkg_test`. Assert the recorded set contains the tracked session id and not `""`. Without this, a `wakeTranscript` that builds the map wrongly still passes every other spec.

### E. Changelog

10. `CHANGELOG.md` has **no** `## Unreleased` heading today — the newest is `## v0.111.2` at line 11. Create `## Unreleased` immediately below the intro block (the paragraph plus the three Semantic Versioning bullets, ending with the PATCH line) and immediately above `## v0.111.2`, with exactly one bullet under it describing the fix for a reader, not the commit log. Do not create a second `## Unreleased`, do not edit `## v0.111.2` or any released section, and do not bump a version. Format per `docs/release-process.md` § Shipping step 2 and the coding plugin's `changelog-guide.md`.

### F. Mutation evidence

11. Add one row to the committed `hack/guard-mutation.sh` table for the new guard, anchored on the helper **body** from requirement 4 — the two call sites are textually identical, so `mutate.py` rejects a call-site anchor with `mutation anchor occurs 2 times`:

    ```
    OpenItemOutranksNoAsk|pkg/manager-gate.go|_, ok := in.OpenItems[in.SessionID]|_, ok := in.OpenItems[in.SessionID]; ok = false
    ```

    The guard name is the `<verification>` focus, so the harness's focus column matches the `Describe("OpenItemOutranksNoAsk")` specs. Without this row the new guard is the only one in `pkg/manager-gate.go` the harness does not prove.
</requirements>

<constraints>
- No new item field, state or transition row.
- `store:open-item` is **not** added to `ManagerParkedReasons` (`pkg/manager-gate-park.go:20`): it is a gate reason, not a park, so it never ages and `expectAgedOnlyForParkedReasons` keeps its closed four. If ageing is wanted, say so and update that test in the same change.
- No change to `pkg/manager-gate-repost.go`, `managerOpenView`, or the re-post inputs.
- `OpenItems == nil` must reproduce today's rows exactly.
- The row order and the three-mirror equality are contracts; a reordered or single-mirror edit fails the existing tests.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root. `pkg/` is Ginkgo-only: `-run TestSuite` selects the single suite and `-ginkgo.focus` narrows it. Use `-ginkgo.fail-on-empty` so a focus that matches nothing fails instead of exiting 0.

- `go test ./pkg -count=1 -run TestSuite -ginkgo.focus='OpenItemOutranksNoAsk' -ginkgo.fail-on-empty -v` — exits 0 and prints a `Ran N of M Specs` line with `N >= 1`.
- `grep -q 'store:open-item' pkg/manager-gate.go` — succeeds.
- `grep -q 'store:open-item' docs/manager-gate-semantics.md` — succeeds.
- `grep -q 'store:open-item' pkg/manager-gate_internal_test.go` — succeeds (the mirror literal).
- `grep -q 'managerGateOpenItem' pkg/manager-gate.go` — succeeds (the actual wiring, not a comment).
- `grep -q 'OpenItems' pkg/manager-transcript-source.go` — succeeds.
- `grep -q 'OpenItems' pkg/manager-wake-job.go` — succeeds.
- `ROOTDIR=/workspace make test` — the whole suite passes, including `TestDocTableMatchesCode` and `expectGateTableMirrorsRows`. The explicit `ROOTDIR` is required in this container: `workflow: worktree` masks `.git`, so `Makefile.variables` cannot resolve it.
- `ROOTDIR=/workspace make precommit` — must exit 0.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`; `grep -n '^## ' CHANGELOG.md | head -1` names `## Unreleased`.
- `bash hack/guard-mutation.sh` — exits 0 with `failures=0` and `guards=45` (44 today plus this one). The harness's git half is skipped when `.git` is masked, so this runs in the container.

The deployed-board check (a session holding an open card is not reported cleared) is operator-executable and is not part of this prompt.
</verification>
