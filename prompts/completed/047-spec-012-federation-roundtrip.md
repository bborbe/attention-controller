---
status: completed
spec: [012-bug-permission-close-without-decision]
summary: Taught the federating client's remoteResponseError to map the peer's PERMISSION_CLOSE_WITHOUT_DECISION code back to ErrPermissionCloseWithoutDecision, added the round-trip spec and the CHANGELOG entry
execution_id: attention-controller-refuse-decisionless-close-exec-047-spec-012-federation-roundtrip
dark-factory-version: v0.198.7
created: "2026-10-16T09:37:00Z"
queued: "2026-10-16T09:59:47Z"
started: "2026-10-16T10:09:51Z"
completed: "2026-10-16T10:14:49Z"
branch: dark-factory/bug-permission-close-without-decision
---

# Round-trip the decision-less permission refusal across the federation

<summary>
- A refusal raised by the store that owns a permission gate survives the federation hop as the same error
- A caller in front of a store that does not own the item is no longer told the peer was unavailable
- A deterministic refusal is no longer reported as a retryable outage
- Every other peer failure code keeps its current mapping
- The change is recorded in the changelog
- `make precommit` passes
</summary>

<objective>
The owning store now refuses an arm-caused close of an open `permission` item with its own sentinel, and the close route answers its own code — but a store that does NOT own the item proxies the write to the peer, and the federating client's response mapping has no case for the new code, so it falls to a `default:` that wraps `ErrPeerUnavailable`. A deterministic refusal therefore reaches the caller as a retryable peer outage. Teach the client to turn the peer's refusal back into this store's own sentinel, so the refusal is as legible in front of a non-owning store as in front of the owner.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md` for the definition of done.

Read `pkg/attention-remote.go` — `remoteResponseError(ctx, resp)`. It decodes the peer's `libhttp.ErrorResponse` body and switches on `errorResponse.Error.Code`, reading the CODE rather than only the status, because two different failures share HTTP 409. The existing arms map `ErrorCodeAlreadyAnswered` → `ErrAlreadyAnswered`, `ErrorCodeItemClosed` → `ErrIllegalTransition`, `ErrorCodeAlreadyEscalated` → `ErrAlreadyEscalated`, `ErrorCodeItemNotOpen` → `ErrItemNotOpen`, and a not-found code or status → `ErrItemNotFound`. The `default:` arm wraps `ErrPeerUnavailable`, and its ⚠️ comment explains why: without a sentinel every handler mapping falls through to its own `default:` and answers 409 `VALIDATION_ERROR`. The function's own doc comment states the rule this prompt completes — a code and its sentinel are paired so the client can round-trip a peer's error faithfully.

Read `pkg/attention-remote.go` — `httpRemoteAttentionStore.Close` (which posts to `remoteItemPath(itemID)+"/close"` with a `remoteCloseRequest`) and `do` (which routes any non-2xx response to `remoteResponseError`). That is the path the federating store's `Close` reaches when the local store does not hold the item: see `pkg/attention-store-federating.go`, `federatingAttentionStore.Close`, which calls `f.remote.Close` when `isRemote` is true.

Read `pkg/attention-error-codes.go` — `ErrorCodePermissionCloseWithoutDecision` (`"PERMISSION_CLOSE_WITHOUT_DECISION"`), added by the previous prompt. Read `pkg/errors.go` — `ErrPermissionCloseWithoutDecision`. The two are a pair: the code is the wire name of the sentinel, and this mapping is what turns the one back into the other.

Read `pkg/attention-remote_test.go` — the suite drives a real `httptest.Server` peer through the real `pkg.NewHTTPRemoteAttentionStore` client. The `respondWith(status int, body string)` closure starts a peer that answers every request with the given status and JSON body; the specs live under `Context("mapping the peer's failure back to a sentinel", ...)` and assert with `errors.Is(err, pkg.ErrX)` — for example "maps ALREADY_ANSWERED to ErrAlreadyAnswered" and "maps ITEM_CLOSED to ErrIllegalTransition, not to ErrAlreadyAnswered". Note the import block does not currently include `fmt`.

Read `pkg/handler/attention-peer-unavailable_test.go` — the harness that drives the whole chain (a real boltkv store, the real federating decorator and a real HTTP peer) through the handler routes. It is reference for how the federation is exercised end to end; this prompt's spec belongs at the client layer above, where the assertion is on the sentinel the client returns.

Read `CHANGELOG.md` — its top. It currently has no `## Unreleased` section: the preamble is followed directly by `## v0.54.3`.

Coding docs (read before writing): `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` (entry format, the `## Unreleased` rules, the frozen preamble).
</context>

<requirements>
1. In `pkg/attention-remote.go`, add one arm to `remoteResponseError`'s `switch`, placed with the other `pkg` code comparisons (after the `code == ErrorCodeItemNotOpen` arm) and BEFORE the `code == libhttp.ErrorCodeNotFound || resp.StatusCode == http.StatusNotFound` arm and the `default:`:

   ```go
   case code == ErrorCodePermissionCloseWithoutDecision:
       return errors.Wrapf(
           ctx,
           ErrPermissionCloseWithoutDecision,
           "peer attention store: permission close without decision (%s)",
           errorResponse.Error.Message,
       )
   ```

   Requirements on this arm, each load-bearing:
   - It must come before the `default:`. A Go `switch` takes the first matching arm, so an arm placed after the `default:` is dead code and the peer's refusal keeps arriving as `ErrPeerUnavailable`.
   - It matches on the body's CODE (`errorResponse.Error.Code`), exactly as the sibling arms do — never on the HTTP status alone. The status is the fallback for a peer that answers with a body this shape cannot read, and it cannot separate this refusal from the illegal-transition refusal.
   - It wraps `ErrPermissionCloseWithoutDecision`, not `ErrPeerUnavailable` and not `ErrIllegalTransition`. The refusal is deterministic: the same body can never succeed, so reporting it as a retryable outage sends the caller down the wrong path.
   - Do not touch the other arms, the not-found arm or the `default:` arm. Every existing code keeps its current mapping.

2. In `pkg/attention-remote_test.go`, add one spec inside the existing `Context("mapping the peer's failure back to a sentinel", ...)`, using the existing `respondWith` closure. Add `"fmt"` to the import block if it is not already there. Use the spec name verbatim — it is the evidence the spec's acceptance criterion is keyed on, and `go test -v` prints it:

   ```go
   It("maps the peer's decision-less permission refusal back to its own sentinel", func() {
       // ⚠️ The body is built from the constant rather than typed as a literal, so
       // the code this client reads and the code the peer writes cannot drift: the
       // wire string is the contract the mapping round-trips.
       respondWith(
           http.StatusConflict,
           fmt.Sprintf(
               `{"error":{"code":%q,"message":"records no decision"}}`,
               pkg.ErrorCodePermissionCloseWithoutDecision,
           ),
       )

       _, err := remote.Close(ctx, "peer-1", "attention-board", nil)

       Expect(errors.Is(err, pkg.ErrPermissionCloseWithoutDecision)).Should(BeTrue())
       // ⚠️ Not ErrPeerUnavailable: a deterministic refusal is not a retryable
       // outage, and reporting it as one is the one instruction that is wrong here.
       Expect(errors.Is(err, pkg.ErrPeerUnavailable)).Should(BeFalse())
   })
   ```

   The spec exercises the real client over a real HTTP round trip, so it traverses the JSON boundary the new code string crosses — a shape-only assertion on the constant would not.

3. In `CHANGELOG.md`, record the change under a `## Unreleased` section:
   - If a `## Unreleased` section already exists, append the bullet to it.
   - Otherwise create it immediately after the SemVer preamble block (the three MAJOR/MINOR/PATCH bullets) and above the current first version heading `## v0.54.3`, with a blank line around it so the file keeps its formatting. Never insert content above the `# Changelog` title or inside the preamble block.
   - Add exactly one `fix:` bullet for this change, specific enough to name the code, the sentinel and the two mappings. It describes what was implemented, not what was verified:

     ```markdown
     - fix: **an arm-caused close of an open `permission` item no longer consumes the gate — it answers `409` with the new `PERMISSION_CLOSE_WITHOUT_DECISION` code.** The close route's arm-caused body carries `answered_by` and `automation` and no decision field at all, so applying it to a gate recorded neither an answer nor a withdrawal: the item became `closed`, the asking session stayed parked, and `closed` being terminal left that gate with no answer path — a silent and unrecoverable failure, because the board (the only surface showing the gate) stops listing it and nothing reports the session as broken. The guard now lives inside `Close`'s own write transaction and is keyed on the STORED item's `answer_mechanism` together with a non-empty `answered_by`, so the refusal precedes the write rather than unwinding it, and it is deliberately NOT `ErrIllegalTransition`: `open -> closed` IS a row of the schema's table, so the caller must be able to tell "this transition does not exist" from "this close is not an acceptable way to end a gate". ⚠️ The distinction is legible on both stores: `wrapItemError` gains the branch that answers the new code on the owning store (without it the refusal reached the caller as `VALIDATION_ERROR`), and `remoteResponseError` gains the matching branch that turns a peer's refusal back into `ErrPermissionCloseWithoutDecision` rather than falling to its `default:` and reporting a deterministic refusal as `ErrPeerUnavailable` — a retryable outage, which is the one instruction that is wrong here. Every legitimate path is unchanged: answering a permission item with a decision, a producer withdrawing its own item (which names no arm), and every close of a non-`permission` item.
     ```

4. Keep every edit inside the files this spec names — `pkg/attention-store-impl.go`, `pkg/errors.go`, `pkg/attention-error-codes.go`, `pkg/handler/attention-close.go`, `pkg/attention-remote.go`, their test files and `CHANGELOG.md`. The negative acceptance criterion (no other behavior moved) is verified on the HOST with `git diff --stat "$(git merge-base HEAD origin/master)"`, which must list only those files. `hideGit: true` is in effect, so the container has no `.git` and MUST NOT attempt that command — a `git` command here fails with `fatal: not a git repository` and, because the daemon does not check verification exit codes, would silently "pass".

5. Before finishing, re-run `<verification>` and confirm it passes, then walk each acceptance criterion in `specs/in-progress/012-bug-permission-close-without-decision.md` against the change and confirm it holds.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt is the last of three; the changelog entry for this change is written here and nowhere else. Do not add a second bullet for the store guard or the handler mapping — they are one logical change.
- The federating client's response mapping gains the matching branch; without it the refusal reaches a caller in front of a non-owning store as `ErrPeerUnavailable`, which reads as a retryable outage. Every existing error code round-trips this way — the codes live in `pkg` precisely so the federating client can turn a peer's response back into the sentinel — and the new one must not be the exception.
- Do not change the close route's request shape; this change adds no field to it.
- `closed` remains terminal and the transition table is unchanged.
- No configuration switch, opt-out or per-caller allowlist. The refusal is unconditional.
- Tests follow the repo's conventions: Ginkgo/Gomega and the real HTTP peer harness rather than a mocked client.
- Existing tests must still pass.
- Error handling follows `github.com/bborbe/errors` patterns — `errors.Wrapf` / `errors.Wrap`, never `fmt.Errorf`, never `context.Background()` inside `pkg/`.
- No absolute or home-relative paths in code.
- The negative acceptance criterion is operator-verified (a host-side `git diff`); it is not run in the container and no `git` command belongs in this prompt's `<verification>`.
</constraints>

<verification>
- `go test -v -mod=mod ./pkg/... -args -ginkgo.v` — must exit 0, and its output must contain the spec name `maps the peer's decision-less permission refusal back to its own sentinel` verbatim. Confirm with `set -o pipefail; go test -v -mod=mod ./pkg/... -args -ginkgo.v 2>&1 | grep -c -F "maps the peer's decision-less permission refusal back to its own sentinel"` — must print a number ≥ 1.
- `grep -c 'ErrorCodePermissionCloseWithoutDecision' pkg/attention-remote.go` — must print a number ≥ 1.
- `grep -c '^## Unreleased' CHANGELOG.md` — must print exactly `1`, and `grep -m1 '^## ' CHANGELOG.md` must print `## Unreleased` (the section sits above `## v0.54.3`).
- `awk '/^## /{sec=$0} /no longer consumes the gate/{print "sits under: " sec}' CHANGELOG.md` — must print `sits under: ## Unreleased`; a bullet placed under a released heading prints the wrong section instead of merely being invisible.
- `ROOTDIR=/workspace make precommit` — must exit 0. (`hideGit: true` is in effect, so the container has no `.git`; pass `ROOTDIR=/workspace` on every `make` call or `make` dies resolving `ROOTDIR` from `git rev-parse`. Never run a bare `git` command — see requirement 4.)
</verification>
