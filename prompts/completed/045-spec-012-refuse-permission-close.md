---
status: completed
spec: [012-bug-permission-close-without-decision]
summary: Added ErrPermissionCloseWithoutDecision and an in-transaction guard refusing an arm-caused close of an open permission item, with four specs covering the refusal and its three positive controls.
execution_id: attention-controller-refuse-decisionless-close-exec-045-spec-012-refuse-permission-close
dark-factory-version: v0.198.7
created: "2026-10-16T09:37:00Z"
queued: "2026-10-16T09:59:47Z"
started: "2026-10-16T09:59:49Z"
completed: "2026-10-16T10:05:48Z"
branch: dark-factory/bug-permission-close-without-decision
---

# Refuse an arm-caused close of an open permission item

<summary>
- A permission gate can no longer be ended by a close that names an arm and records no decision
- Such a close is refused with its own error, distinguishable from an illegal transition
- The refused close writes nothing: the item stays open with no closed timestamp and no arm recorded
- The gate stays answerable — answering it with a decision still succeeds afterwards, which is what separates a refusal from a consumption
- A producer withdrawing its own item still closes it, because that close names no arm
- Closing a non-permission item with an arm is unchanged, and still records the arm
- The refusal is unconditional — no configuration switch, no opt-out, no allowlist
- `make precommit` passes
</summary>

<objective>
A `permission` item is a gate on an action: a parked session waits on it and the operator is the only arm that may answer it. The close route's arm-caused write records no decision at all, so applying it to an open gate consumes the gate's only answer path and strands the asking session. Refuse that write inside the store's close transaction — before anything is persisted — with a new exported sentinel, and pin the three legitimate paths with positive controls so the refusal cannot over-reach.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md` for the definition of done.

Read `pkg/attention-store-impl.go` — the `attentionStore.Close` method. It opens one `a.db.Update` transaction, loads the item with `a.store.Get`, takes `from := item.State`, calls `ValidateTransition(ctx, from, ClosedState)`, then writes `item.State = ClosedState` and `item.ClosedAt`, and inside `if from == OpenState { ... }` writes `item.AnsweredBy = answeredBy` and conditionally `item.AnsweredClient = answeredClient`, before `a.putItem(ctx, tx, *item)`. The transaction already loads the item and already branches on `from == OpenState`, so the guard needs no new read. The error is wrapped once more at the method's end with `errors.Wrap(ctx, err, "close failed")`, which is why `errors.Is` on the returned error still reaches a sentinel wrapped inside the transaction — see the sibling guard at `attentionStore.Escalate`, which returns `errors.Wrapf(ctx, ErrItemNotOpen, ...)` from inside its own transaction and is asserted with `errors.Is` from the tests.

Read `pkg/errors.go` — the `var (...)` block of exported sentinels. Every entry is a `stderrors.New(...)` with a doc comment explaining what it means and, where it matters, what it is deliberately NOT. `ErrIllegalTransition` and `ErrItemNotOpen` are the two closest siblings; match their shape and comment style.

Read `pkg/answer-mechanism.go` — `PermissionAnswerMechanism` (`AnswerMechanism = "permission"`) and `MessageAnswerMechanism` / `AckAnswerMechanism`. Read `pkg/attention-item.go` — the `Item` struct's `AnswerMechanism AnswerMechanism \`json:"answer_mechanism"\`` field, plus `ClosedAt`, `AnsweredAt`, `AnsweredBy`, `AnsweredClient`.

Read `pkg/attention-store_test.go` — the outer `Describe("AttentionStore", ...)` sets up `db` (a real `libboltkv.OpenTemp`), `store` (`pkg.NewAttentionStore`) and the `pushRequest` closure that builds a `message` declaration. The nested `Describe("Close", ...)` block holds the existing close specs. The permission mechanism is pushed the same way `pushRequest` builds a message item, with `request.AnswerMechanism = pkg.PermissionAnswerMechanism` — see the spec "returns every pushed item while every producer is live" for that exact construction.

Read `pkg/attention-store-impl.go` — `attentionStore.Answer`'s `OpenState` arm, which is the path the positive control for answering exercises: from `open` it writes `State = AnsweredState`, `AnsweredAt`, `AnsweredBy`, `ResolvedBy` and `Decision`.

Coding docs (read before writing): `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` (the `github.com/bborbe/errors` API, never `fmt.Errorf`), `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo/Gomega, external test package, counterfeiter), `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`.
</context>

<requirements>
1. In `pkg/errors.go`, add one exported sentinel to the existing `var (...)` block, alongside the siblings — do not create a new block and do not reorder the existing entries:

   ```go
   // ErrPermissionCloseWithoutDecision is returned when an arm-caused close is
   // attempted on an open `permission` item.
   //
   // A `permission` item is a gate the operator alone may answer, and the close
   // route's arm-caused write records no decision — it is the acknowledgement of
   // an `ack` item, which routes nothing back. Applying that write to a gate
   // does not clear it, it consumes it: `closed` is terminal, so the write takes
   // the gate's only answer path and the asking session stays parked.
   //
   // ⚠️ It is deliberately NOT ErrIllegalTransition. `open -> closed` is a row
   // of the schema's table, so a caller told "illegal transition" would be told
   // the wrong thing — and could not tell "this transition does not exist" from
   // "this close is not an acceptable way to end a gate".
   ErrPermissionCloseWithoutDecision = stderrors.New("permission close without decision")
   ```

   The message string must be exactly `permission close without decision`. `stderrors` is the existing alias for the standard library `errors` package already imported in this file.

2. In `pkg/attention-store-impl.go`, add the guard inside `attentionStore.Close`, as the FIRST statement inside the existing `if from == OpenState { ... }` block — before `item.AnsweredBy = answeredBy` and before the `answeredClient` write:

   ```go
   if from == OpenState {
       // A `permission` item leaves `open` only through a write that records a
       // decision. This route's arm-caused body carries no decision field at
       // all, so an arm-caused close of an open gate is not an answer to it —
       // and `closed` is terminal, so accepting it would consume the gate.
       if item.AnswerMechanism == PermissionAnswerMechanism && answeredBy != "" {
           return errors.Wrapf(
               ctx,
               ErrPermissionCloseWithoutDecision,
               "item %s is a permission gate and a close naming arm %q records no decision",
               itemID,
               answeredBy,
           )
       }
       item.AnsweredBy = answeredBy
       ...
   }
   ```

   Requirements on this guard, each load-bearing:
   - It lives INSIDE the transaction, before `a.putItem`, so the transition is refused before it is persisted. Returning the error aborts the `libkv` update, so nothing is written. Do not implement this as a post-write check on `result`.
   - It is scoped to `from == OpenState`. The `answered -> closed` row is untouched: closing an already-answered `permission` item with an arm stays legal.
   - It is keyed on the STORED item's `AnswerMechanism` and on `answeredBy != ""` only. Do not add any check on `answeredClient`, on any request field, or on the caller's identity.
   - `ValidateTransition` keeps running before this branch, so closing an already-`closed` item still returns `ErrIllegalTransition` — the new refusal and the illegal-transition refusal stay distinguishable.
   - Add no configuration switch, opt-out flag, allowlist or tunable threshold. The refusal is unconditional.
   - Do not change the transition table (`closed` stays terminal) and do not change the close route's request shape.

3. In `pkg/attention-store_test.go`, inside the nested `Describe("Close", ...)` block, add a `pushPermission` closure and four specs. Use the spec names verbatim — they are the evidence the spec's acceptance criteria are keyed on, and `go test -v -args -ginkgo.v` prints them (⚠️ `go test -v` alone does **not**: `-v` surfaces the test binary's stdout, while the spec names come from Ginkgo's own reporter, which needs `-ginkgo.v`. Measured on this suite — the same command without `-ginkgo.v` yields `grep -c` = 0):

   ```go
   // pushPermission pushes a `permission` item: a gate the operator alone may
   // answer. It is a question-shaped declaration with the permission mechanism,
   // exactly as the "returns every pushed item while every producer is live"
   // spec builds one.
   pushPermission := func(dedupKey pkg.DedupKey) *pkg.Item {
       request := pushRequest("session-a", dedupKey)
       request.AnswerMechanism = pkg.PermissionAnswerMechanism
       item, err := store.Push(ctx, request)
       Expect(err).To(BeNil())
       return item
   }
   ```

   1. `It("refuses an arm-caused close and leaves the gate open", ...)` — push a permission item, then `store.Close(ctx, item.ItemID, "attention-board", nil)`. Assert:
      - `errors.Is(err, pkg.ErrPermissionCloseWithoutDecision)` is true
      - `errors.Is(err, pkg.ErrIllegalTransition)` is false (the two refusals are distinguishable)
      - read the item back with `store.Get` and assert `State == pkg.OpenState`, `ClosedAt` is nil, `AnsweredAt` is nil, `AnsweredBy` is empty, and `AnsweredClient` is nil. The read-back is the point: a guard that returned an error while the write still landed would be strictly worse than none.
   2. `It("still answers a permission item that carries a decision", ...)` — push a permission item, attempt the same refused close (`"attention-board"`, nil), expect the refusal, then answer the SAME item: `store.Answer(ctx, item.ItemID, "operator", "", pkg.DenyDecision, nil, nil, nil)`. Assert the answer succeeds (`err` is nil), `answered.State == pkg.AnsweredState`, and `answered.Decision == pkg.DenyDecision`. This is the observable that separates a refusal from a consumption — the gate is still answerable after the refused close.
   3. `It("still lets a producer withdraw its own item, which names no arm", ...)` — push a permission item, then `store.Close(ctx, item.ItemID, "", nil)`. Assert `err` is nil, `closed.State == pkg.ClosedState`, `closed.ClosedAt` is not nil, and `closed.AnsweredBy` is empty.
   4. `It("leaves the arm-caused close of a non-permission item untouched", ...)` — push an `ack` item (build it with `pushRequest` and set `request.AnswerMechanism = pkg.AckAnswerMechanism`), then `store.Close(ctx, item.ItemID, "attention-board", nil)`. Assert `err` is nil, `closed.State == pkg.ClosedState`, `closed.AnsweredBy == "attention-board"`, and `closed.AnsweredAt` is nil.

   These four specs are one group: the refusal plus the three positive controls that stop it over-reaching. Do not split them across files.

4. Before finishing, re-run `<verification>` and confirm it passes, then walk each acceptance criterion in `specs/in-progress/012-bug-permission-close-without-decision.md` against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT edit `CHANGELOG.md` — the changelog entry for this change is written by the final prompt (`3-spec-012-federation-roundtrip.md`).
- Do NOT edit `pkg/handler/attention-close.go` or `pkg/attention-remote.go` — the two mapping branches are the next prompts' work. This prompt lands the store-level refusal and its sentinel only.
- The rule is evaluated inside the store's close path, so the transition is refused before it is persisted — a guard that rejected the returned value while the write landed would be strictly worse than none.
- The sentinel is a new exported value alongside the existing ones, and callers distinguish it with `errors.Is`.
- The close route's request shape is unchanged; this change adds no field to it.
- `closed` remains terminal and the transition table is unchanged.
- No configuration switch, opt-out or per-caller allowlist. The refusal is unconditional.
- Tests follow the repo's conventions: Ginkgo/Gomega, a real in-memory `libkv` database (`libboltkv.OpenTemp`) rather than a mocked one.
- Existing tests must still pass.
- Error handling follows `github.com/bborbe/errors` patterns — `errors.Wrapf` / `errors.Wrap`, never `fmt.Errorf`, never `context.Background()` inside `pkg/`.
- No absolute or home-relative paths in code.
</constraints>

<verification>
- `go test -v -mod=mod ./pkg/... -args -ginkgo.v` — must exit 0, and its output must contain each of these four spec names verbatim:
  - `refuses an arm-caused close and leaves the gate open`
  - `still answers a permission item that carries a decision`
  - `still lets a producer withdraw its own item, which names no arm`
  - `leaves the arm-caused close of a non-permission item untouched`

  Confirm each one with `set -o pipefail; go test -v -mod=mod ./pkg/... -args -ginkgo.v 2>&1 | grep -c -F "<spec name>"` — must print a number ≥ 1, once per name above, substituting the name verbatim between the **double** quotes. Use double quotes, never single: these names contain apostrophes, and a single-quoted pattern closes early and never runs — a check that cannot fail, which the daemon would report as a pass. (A count of 0 makes `grep` exit 1, so a missing spec is visible rather than silently passing. The `pipefail` keeps a failing `go test` from being masked by `grep`'s status.)
- `grep -c 'ErrPermissionCloseWithoutDecision' pkg/errors.go` — must print a number ≥ 1.
- `grep -c 'ErrPermissionCloseWithoutDecision' pkg/attention-store-impl.go` — must print a number ≥ 1.
- `ROOTDIR=/workspace make precommit` — must exit 0. (`hideGit: true` is in effect, so the container has no `.git`; pass `ROOTDIR=/workspace` on every `make` call or `make` dies resolving `ROOTDIR` from `git rev-parse`. Never run a bare `git` command — it will fail with `fatal: not a git repository` and the daemon does not check verification exit codes, so a git-based check silently passes.)
</verification>
