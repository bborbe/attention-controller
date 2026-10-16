---
status: completed
spec: [012-bug-permission-close-without-decision]
summary: Mapped ErrPermissionCloseWithoutDecision to its own HTTP 409 wire code on the close route, with a handler spec asserting the code and the untouched item.
execution_id: attention-controller-refuse-decisionless-close-exec-046-spec-012-close-route-code
dark-factory-version: v0.198.7
created: "2026-10-16T09:37:00Z"
queued: "2026-10-16T09:59:47Z"
started: "2026-10-16T10:05:50Z"
completed: "2026-10-16T10:09:49Z"
branch: dark-factory/bug-permission-close-without-decision
---

# Answer the close route's own code for a decision-less permission close

<summary>
- A refused decision-less close of a permission gate reaches the caller as its own error code
- The code is distinct from the one an illegal transition answers, so a caller can tell the two apart
- The response stays HTTP 409, matching the close route's other refusals
- The refused request still writes nothing through the route — the item is left open
- Every other close-route failure keeps its current code and status
- `make precommit` passes
</summary>

<objective>
The store now refuses an arm-caused close of an open `permission` item with its own sentinel, but the close route's error mapping has no branch for it and would answer the generic validation code — the same code an illegal transition gets, which erases the distinction the refusal exists to make. Map the new sentinel to its own wire code on the close route so a caller can tell "this close is not an acceptable way to end a gate" from "this transition does not exist".
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md` for the definition of done.

Read `pkg/attention-error-codes.go` — the single `const (...)` block of wire error codes. Every entry is an UPPER_SNAKE string with a doc comment that says what the caller receives and, where it matters, what it is deliberately NOT. The header comment explains why these codes live in `pkg` rather than `pkg/handler`: the federating client has to turn a peer's response back into the sentinel, and it cannot import `pkg/handler` (that would be a cycle). Match the existing shape and comment style exactly.

Read `pkg/handler/attention-close.go` — `NewAttentionCloseHandler` calls `wrapItemError(ctx, err, itemID, "close failed")` on any store error. `wrapItemError` is a `switch` with three arms: `errors.Is(err, pkg.ErrItemNotFound)` → 404 + `libhttp.ErrorCodeNotFound`; `errors.Is(err, pkg.ErrPeerUnavailable)` → 503 + `pkg.ErrorCodePeerUnavailable`; and a `default:` → 409 + `libhttp.ErrorCodeValidation`. The ⚠️ comment on the `ErrPeerUnavailable` arm explains why it must be checked before the catch-all: without it, that failure reads as a malformed request. The same reasoning applies to the new arm.

Read `pkg/errors.go` — `ErrPermissionCloseWithoutDecision`, added by the previous prompt (`1-spec-012-refuse-permission-close.md`). It is returned by `attentionStore.Close` when an arm-caused close targets an open `permission` item, and is wrapped once more with `"close failed"` before it reaches the handler, so `errors.Is` still reaches it.

Read `pkg/handler/attention-close_test.go` — the suite wiring: a real `libboltkv.OpenTemp` DB and a real `pkg.NewAttentionStore`; the `pushAck` and `pushQuestion` closures; and the `closeItem(itemID, body)` helper, which posts a body to `/api/1.0/attention/{id}/close` with `mux.SetURLVars`. Specs assert `resp.Code` and then read the item back with `store.Get` to prove what the store recorded.

Read `pkg/handler/attention-peer-unavailable_test.go` — for the assertion shape this repo uses when a spec must pin the wire CODE and not just the status: `Expect(resp.Body.String()).To(ContainSubstring(pkg.ErrorCodeX))` together with `Expect(resp.Body.String()).NotTo(ContainSubstring("VALIDATION_ERROR"))`, on the grounds that a correct status still carrying the generic code would be the same defect wearing the right number.

Coding docs (read before writing): `/home/node/.claude/plugins/marketplaces/coding/docs/go-json-error-handler-guide.md` (JSON error responses, error codes, `WrapWithDetails`), `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`.
</context>

<requirements>
1. In `pkg/attention-error-codes.go`, add one constant to the existing `const (...)` block — do not create a new block and do not reorder the existing entries:

   ```go
   // ErrorCodePermissionCloseWithoutDecision is the code a caller receives when
   // it closes an open `permission` item naming an arm and carrying no decision.
   //
   // It is deliberately not ErrorCodeValidation, which is what it used to
   // arrive as. The request was well formed and named a real item, so reporting
   // it as a malformed request asks the caller to correct something that is not
   // wrong — and the caller cannot tell "this transition does not exist" from
   // "this close is not an acceptable way to end a gate".
   //
   // It is deliberately not the illegal-transition reading either: `open ->
   // closed` IS a row of the schema's table, so the transition exists and is
   // merely not one that records a decision. The refusal is keyed on the
   // stored item's mechanism, never on anything the caller supplied.
   ErrorCodePermissionCloseWithoutDecision = "PERMISSION_CLOSE_WITHOUT_DECISION"
   ```

   The value string must be exactly `PERMISSION_CLOSE_WITHOUT_DECISION`.

2. In `pkg/handler/attention-close.go`, add one arm to `wrapItemError`, placed BEFORE the `default:` arm (after the existing `pkg.ErrPeerUnavailable` arm):

   ```go
   case errors.Is(err, pkg.ErrPermissionCloseWithoutDecision):
       return libhttp.WrapWithDetails(
           errors.Wrap(ctx, err, what),
           pkg.ErrorCodePermissionCloseWithoutDecision,
           http.StatusConflict,
           map[string]any{"item_id": itemID.String()},
       )
   ```

   Requirements on this arm, each load-bearing:
   - It must come before the `default:`. A Go `switch` takes the first matching arm, and the `default:` is the catch-all that answers `libhttp.ErrorCodeValidation` — an arm placed after it is dead code and the caller keeps seeing the generic code.
   - Status is `http.StatusConflict` (409), matching the route's other refusals. The refusal is not a malformed request and not an unavailable peer.
   - The detail map is `map[string]any{"item_id": itemID.String()}`, matching every sibling arm in this function.
   - Do not touch the `ErrItemNotFound` or `ErrPeerUnavailable` arms, the `default:` arm, the request body decoding, or the route's request shape. This change adds no field to the close body.
   - `wrapItemError` is shared with the GET routes (`attention-get.go`, `attention-jump.go`); it must stay a generic mapper. Do not special-case the close route inside it beyond adding this one sentinel arm.

3. In `pkg/handler/attention-close_test.go`, add a `pushPermission` closure and one spec. Use the spec name verbatim — it is the evidence the spec's acceptance criterion is keyed on, and `go test -v` prints it:

   ```go
   // pushPermission pushes a `permission` item: a gate the operator alone may
   // answer, so the close route's arm-caused write is not a way to end it.
   pushPermission := func() *pkg.Item {
       item, err := store.Push(ctx, pkg.PushRequest{
           ProducerID:      "session-c",
           ProducerKind:    pkg.SessionProducerKind,
           LivenessRef:     pkg.LivenessRef("session:session-c"),
           DedupKey:        "gate-permission-1",
           InterruptClass:  "approve",
           Payload:         "deploy prod?",
           AnswerMechanism: pkg.PermissionAnswerMechanism,
       })
       Expect(err).To(BeNil())
       return item
   }
   ```

   `It("answers the close route's own code for a decision-less permission close", ...)` — push a permission item, then `closeItem(item.ItemID, \`{"answered_by":"attention-board"}\`)`. Assert:
   - `resp.Code` equals `http.StatusConflict`
   - `resp.Body.String()` contains `pkg.ErrorCodePermissionCloseWithoutDecision`
   - `resp.Body.String()` does NOT contain `"VALIDATION_ERROR"` — the status alone is not the assertion; a correct 409 still carrying the generic code would be the same defect
   - read the item back with `store.Get` and assert `State == pkg.OpenState` and `ClosedAt` is nil — the refusal wrote nothing through the route either

4. Before finishing, re-run `<verification>` and confirm it passes, then walk each acceptance criterion in `specs/in-progress/012-bug-permission-close-without-decision.md` against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT edit `CHANGELOG.md` — the changelog entry for this change is written by the final prompt (`3-spec-012-federation-roundtrip.md`).
- Do NOT edit `pkg/attention-remote.go` — the federating client's matching branch is the next prompt's work.
- The close route's error mapping gains a branch for the new sentinel; without it the refusal reaches the caller as `VALIDATION_ERROR`, indistinguishable from an illegal transition.
- The close route's request shape is unchanged; this change adds no field to it.
- Existing tests must still pass.
- Error handling follows `github.com/bborbe/errors` patterns — `errors.Wrap` / `errors.Wrapf`, never `fmt.Errorf`, never `context.Background()` inside `pkg/`.
- No absolute or home-relative paths in code.
- No configuration switch, opt-out or per-caller allowlist. The refusal is unconditional.
</constraints>

<verification>
- `go test -v -mod=mod ./pkg/... -args -ginkgo.v` — must exit 0, and its output must contain the spec name `answers the close route's own code for a decision-less permission close` verbatim. Confirm with `set -o pipefail; go test -v -mod=mod ./pkg/... -args -ginkgo.v 2>&1 | grep -c -F "answers the close route's own code for a decision-less permission close"` — must print a number ≥ 1. (A count of 0 makes `grep` exit 1, so a missing spec is visible rather than silently passing. The `pipefail` keeps a failing `go test` from being masked by `grep`'s status.)
- `grep -c 'ErrorCodePermissionCloseWithoutDecision' pkg/attention-error-codes.go` — must print a number ≥ 1.
- `grep -c 'ErrPermissionCloseWithoutDecision' pkg/handler/attention-close.go` — must print a number ≥ 1.
- `ROOTDIR=/workspace make precommit` — must exit 0. (`hideGit: true` is in effect, so the container has no `.git`; pass `ROOTDIR=/workspace` on every `make` call. Never run a bare `git` command — it fails with `fatal: not a git repository` and the daemon does not check verification exit codes, so a git-based check silently passes.)
</verification>
