---
status: prompted
approved: "2026-10-16T09:32:49Z"
generating: "2026-10-16T09:32:50Z"
prompted: "2026-10-16T09:49:41Z"
branch: dark-factory/bug-permission-close-without-decision
---

## Summary

- A `permission` item is a gate on an action: a parked session waits on it, and the operator is the only arm that may answer it.
- `Close` accepts a `permission` item from `open`, writing `state: closed` with `answered_at`, `decision` and `resolved_by` all unset, and routes nothing back to the producer.
- `closed` is terminal, so that write consumes the gate's only answer path: the asking session stays frozen, and the card leaves the board — the only surface showing the gate.
- The close route's body carries `answered_by` and `automation` and no decision field at all, so an arm-caused close of an open `permission` item can never be a legitimate answer to it.
- Today that close is accepted, reports the same code an illegal transition does, and across the federation is reported as a peer failure — so this spec refuses it and makes the refusal distinguishable on both the owning and the non-owning store.

## Problem

A `permission` item exists so that exactly one thing happens — the operator decides. Every other lifecycle move on it either records a decision or is a producer withdrawing its own item. The close route is the one path that can end a gate's life while recording neither, and today it is accepted. The failure is silent in the worst way: the caller receives a success, the record looks properly closed, and the session that asked is still parked with no way left to answer it. Because `closed` is terminal and reopening is a new item with a new id, there is no recovery for that gate — only a new gate.

## Reproduction

Store: the `attention-controller` HTTP API. Item observed 2026-10-16: `dc5c4202f311df8056115342df9cd944` (nuke dev cluster store). `dark-factory --version` → `v0.198.7`.

1. Park a `permission` item — any producer works, provided the item has `answer_mechanism: permission` and `state: open`.

2. Close it naming an arm and carrying no decision:

```bash
curl -sS -X POST "$STORE/api/1.0/attention/$ITEM/close" \
  -H 'Content-Type: application/json' \
  -d '{"answered_by":"attention-board","automation":false}'
```

3. Read it back — `curl -sS "$STORE/api/1.0/attention/$ITEM"`. Observed, verbatim:

```json
{"item_id":"dc5c4202f311df8056115342df9cd944","answer_mechanism":"permission",
 "state":"closed","answered_by":"attention-board",
 "answered_client":{"user_agent":"Go-http-client/2.0","remote_addr":"10.42.0.16:45802","automation":false},
 "closed_at":"2026-10-16T06:56:20.952213845Z"}
```

`answered_at`, `decision` and `resolved_by` are all absent from the record.

4. Answer it — the only action that would release the gate:

```bash
curl -sS -X POST "$STORE/api/1.0/attention/$ITEM/answer" \
  -H 'Content-Type: application/json' \
  -d '{"answered_by":"operator","decision":"deny"}'
```

Observed, verbatim from the store's log:

```
I1016 07:05:16.429034 http_json-error-handler.go:71] handle POST request to
/api/1.0/attention/dc5c4202f311df8056115342df9cd944/answer failed with status 409 and code ITEM_CLOSED:
item dc5c4202f311df8056115342df9cd944 was already closed at 2026-10-16T06:56:20.952213845Z
and can no longer be answered: transition closed -> answered is not in the schema's table: illegal transition
```

## Expected vs Actual

**Expected.** A `permission` item leaves `open` only through a write that records a decision. A close that names an arm and carries none is refused with a code that distinguishes it from an illegal transition — on the store that owns the item and on a store that does not — and the item remains `open` and answerable.

**Actual.** The close is accepted, the item becomes `closed`, and the gate is consumed with nothing routed. The asking session stays parked and its answer path is gone. A caller that did receive an error could not tell this refusal from an illegal transition anyway, because the close route maps both to `VALIDATION_ERROR`; and a caller in front of a non-owning store is told the peer is unavailable.

## Why this is a bug

`[[Attention Item Schema]]` makes `permission` the mechanism whose answer the operator owns, and it routes the close route's arm to the acknowledgement of an `ack` item — a clear that routes nothing, written with `answered_at` unset *because* nothing is routed back. Applying that same write to a gate does not clear it; it destroys it.

The store's own contract already separates the two. `Close`'s documented rule is that an arm-caused `open -> closed` is the `ack` acknowledgement, and that the only other causer — a producer withdrawing its own item — names no arm. Nothing in that contract describes an arm-caused close of a `permission` item, so the accepted write is not a supported transition that merely lacks a guard; it is a transition the contract never contemplated.

## Goal

A `permission` item cannot leave `open` except through a write that records a decision. An arm-caused close of an open `permission` item is refused, the refusal is distinguishable to the caller whether or not the store answering owns the item, the item is left untouched, and every legitimate path — answering with a decision, a producer withdrawing its own item, and every close of a non-`permission` item — behaves exactly as it does today.

## Non-goals

- Not changing the close route's request shape. This adds no field, and the refusal is keyed on stored item state rather than on anything the caller supplies.
- Not changing the transition table. `closed` stays terminal; reopening remains a new item with a new id.
- Not changing the board's own `permission` clear path. That is a separate change with its own spec, and the sequencing that holds this refusal behind it is recorded on the task that filed this spec.
- Not adding a configuration switch, an opt-out, or a per-caller allowlist. The refusal is unconditional.

## Assumptions

- The guard reads `AnswerMechanism` from the item inside the existing `Close` transaction, before `putItem`, so the refusal precedes the write rather than unwinding it. The transaction already loads the item (`pkg/attention-store-impl.go:1438`, wrapped at `:1443`) and already branches on `from == OpenState` before writing (`:1464`), so no new read is required.
- A store that owns the item inherits the guard, because `federatingAttentionStore.Close` delegates to `f.local.Close` for a locally-owned item. A store that does **not** own it proxies, and the peer applies the guard on its own store — so the refusal is correct on both sides without either changing its routing.
- The refusal's *code* does not yet survive that hop: `remoteResponseError` has no case for it and falls to a `default` that wraps `ErrPeerUnavailable`, so a non-owning store would report a deterministic refusal as a peer outage. Closing that is part of this spec, not a follow-up.
- A caller that can distinguish the new code will act on it — correct the client — rather than retry. Retrying an identical body cannot succeed, so the distinction is for diagnosis, not for backoff.

## Acceptance Criteria

- [ ] An arm-caused close of an `open` `permission` item returns an error satisfying `errors.Is(err, ErrPermissionCloseWithoutDecision)`. Evidence: `go test -v -mod=mod ./pkg/... -args -ginkgo.v` exits 0 and its output names the spec `refuses an arm-caused close and leaves the gate open`.
- [ ] The refused close leaves the item untouched — `state` is `open`, and `closed_at`, `answered_at` and `answered_by` are each unset. Evidence: AC1's spec `refuses an arm-caused close and leaves the gate open` asserts all four fields, and `go test -v -mod=mod ./pkg/... -args -ginkgo.v` exits 0.
- [ ] The refusal reaches the caller as HTTP 409 with a code distinct from the illegal-transition code. Evidence: `go test -v -mod=mod ./pkg/... -args -ginkgo.v` exits 0, naming the handler spec `answers the close route's own code for a decision-less permission close`, which asserts the response code equals `ErrorCodePermissionCloseWithoutDecision`.
- [ ] A peer's refusal survives the federation hop as the same sentinel rather than a peer failure. Evidence: `go test -v -mod=mod ./pkg/... -args -ginkgo.v` exits 0, naming the spec `maps the peer's decision-less permission refusal back to its own sentinel`, which asserts `errors.Is(err, ErrPermissionCloseWithoutDecision)` and **not** `errors.Is(err, ErrPeerUnavailable)`.
- [ ] Positive control — answering that same item, the one the refused close touched, with `decision: deny` succeeds and sets `state: answered` and `decision: deny`. Evidence: `go test -v -mod=mod ./pkg/... -args -ginkgo.v` exits 0, naming `still answers a permission item that carries a decision`.
- [ ] Positive control — a close naming no arm still closes an `open` `permission` item, leaving `answered_by` empty. Evidence: `go test -v -mod=mod ./pkg/... -args -ginkgo.v` exits 0, naming `still lets a producer withdraw its own item, which names no arm`.
- [ ] Positive control — an arm-caused close of a non-`permission` item is unchanged: an `ack` item closes and records `answered_by`. Evidence: `go test -v -mod=mod ./pkg/... -args -ginkgo.v` exits 0, naming `leaves the arm-caused close of a non-permission item untouched`.
- [ ] Negative — no other behavior moved. **Operator-executable** (a host-side `git diff`; the container has no `.git`). Evidence: `git diff --stat "$(git merge-base HEAD origin/master)"` lists only `pkg/attention-store-impl.go`, `pkg/errors.go`, `pkg/attention-error-codes.go`, `pkg/handler/attention-close.go`, `pkg/attention-remote.go`, their test files and `CHANGELOG.md`.

## Verification

### Container-executable

⚠️ `hideGit: true` is in effect (mandatory in a worktree), so the container has no `.git` and any command deriving a ref from it returns empty. The `go test` form is what carries the spec names, because `make test` passes no `-v`.

- `ROOTDIR=/workspace make precommit` — exits 0 (format, lint, tests, security)
- `go test -v -mod=mod ./pkg/... -args -ginkgo.v` — exits 0, and its output contains the six spec names quoted in the Acceptance Criteria (AC1, AC3, AC4, AC5, AC6, AC7; AC2 shares AC1's spec). ⚠️ `-ginkgo.v` is load-bearing: `go test -v` surfaces the test binary's stdout, but the spec names come from Ginkgo's own reporter, so without it `grep -c` returns 0 on a suite that passed — a check that cannot fire.
- `grep -c 'ErrPermissionCloseWithoutDecision' pkg/errors.go` — returns ≥1
- `grep -c 'ErrPermissionCloseWithoutDecision' pkg/attention-store-impl.go` — returns ≥1
- `grep -c 'ErrorCodePermissionCloseWithoutDecision' pkg/attention-error-codes.go` — returns ≥1
- `grep -c 'ErrPermissionCloseWithoutDecision' pkg/handler/attention-close.go` — returns ≥1
- `grep -c 'ErrorCodePermissionCloseWithoutDecision' pkg/attention-remote.go` — returns ≥1

### Operator-executable

- `git diff --stat "$(git merge-base HEAD origin/master)"` — the changed-file list matches the negative AC
- Replay `## Reproduction` step 2 against the deployed store — the `POST /close` returns 409 with the new code, and step 3 reads back `state: open` with `closed_at` unset. This is the bug-specific check: tests passing is not the bug being gone.
- `gh pr view <n> --json reviewDecision,mergeStateStatus` — `APPROVED` and not `BLOCKED`
- The cluster deploy ladder is owned by the task that filed this spec; the peer store must carry the change before the round-trip can be observed there

## Desired Behavior

1. A close request against an `open` `permission` item that carries a non-empty `answered_by` is refused, and the response carries a code distinguishable from the schema's illegal-transition refusal — a caller must be able to tell "this transition does not exist" from "this close is not an acceptable way to end a gate".
2. The refused request writes nothing: the item stays `open`, and `closed_at`, `answered_at`, `answered_by` and `answered_client` are unchanged.
3. The item remains answerable — an answer carrying a decision against that same item succeeds afterwards, which is the observable that separates a refusal from a consumption.
4. A close against an `open` `permission` item carrying an empty `answered_by` still succeeds and closes it, because that is the producer withdrawing its own item and it names no arm.
5. A close against an `open` item whose `answer_mechanism` is not `permission` still succeeds, including the arm-caused case that records `answered_by`.
6. The refusal applies on the federated path, and it stays *legible* there: a store that does not own the item receives the peer's refusal and re-raises it as the same sentinel, so the caller is not told the peer was unavailable for a deterministic refusal. The rule itself keys on the item's mechanism and the absence of a decision, never on any property of the calling client.

## Constraints

- The rule is evaluated inside the store's close path, so the transition is refused before it is persisted — a guard that rejected the returned value while the write landed would be strictly worse than none.
- The sentinel is a new exported value alongside the existing ones, and callers distinguish it with `errors.Is`.
- The close route's error mapping gains a branch for the new sentinel; without it the refusal reaches the caller as `VALIDATION_ERROR`, indistinguishable from an illegal transition.
- The federating client's response mapping gains the matching branch; without it the refusal reaches a caller in front of a non-owning store as `ErrPeerUnavailable`, which reads as a retryable outage. Every existing error code round-trips this way — the codes live in `pkg` precisely so the federating client can turn a peer's response back into the sentinel — and the new one must not be the exception.
- The close route's request shape is unchanged; this spec adds no field to it.
- `closed` remains terminal and the transition table is unchanged.
- Tests follow the repo's conventions: Ginkgo/Gomega, a real in-memory `libkv` database rather than a mocked one, and the existing peer harness for the federated case.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A caller that previously closed a `permission` item now receives the refusal | The refusal carries its own code, so the caller can be corrected rather than retried | The client is corrected to answer with a decision; the sequencing that holds this refusal behind that correction is recorded on the filing task |
| A producer withdrawing its own item starts being refused | Not reachable — the producer-withdrawal positive control fails | Re-run `go test -v -mod=mod ./pkg/... -args -ginkgo.v`; the withdrawal spec fails |
| The error mapping is added for the store path but not for the handler, so the caller still sees `VALIDATION_ERROR` | The distinguishable-code AC fails | Add the `wrapItemError` branch; re-run the handler spec |
| The mapping is added for the handler but not for the federating client, so a non-owning store answers 503 | The round-trip AC fails | Add the `remoteResponseError` branch; re-run the federated spec |
| A peer still running the previous version answers the new close without the new code | The owning store refuses locally; a proxied call is reported as a peer failure until the peer is upgraded | Deploy the owning store first; the cluster ladder is owned by the filing task |
| Two writers race a close and an answer on the same item | The store's compare-and-set decides one winner; the loser reads back and reports, and never routes its own answer | Existing behavior, unchanged by this spec |

## Security / Abuse Cases

The change narrows the surface rather than adding one. It introduces no new route, no new input field, no credential and no file write; the close route's body is unchanged and the decision is keyed on stored item state, never on a caller-supplied value. The abuse it closes is the one that matters here: any client able to reach the close route could end a gate without recording a decision, and after this change it cannot. The refusal is unconditional, so there is no allowlist to misconfigure and no flag to flip.

## Suggested Decomposition

Three prompts, in order. The change spans five code layers — the store's close path, the error sentinel, the error-code constant, the close handler's error mapping, and the federating client's response mapping — and prompts 2 and 3 each depend on prompt 1.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Refuse an arm-caused close of an open `permission` item, with the four regression specs | 1-5 | 1, 2, 5, 6, 7 | — |
| 2 | Map the new sentinel to its own HTTP code on the close route | 1 | 3 | prompt 1 (uses the sentinel) |
| 3 | Round-trip the new code back to its sentinel across the federation | 6 | 4, 8 | prompts 1 and 2 (uses the code) |

Rationale: prompt 1 lands the guard and its positive controls together — the controls (DB 4, 5) are what stop the guard over-reaching, so separating them would leave a window in which the refusal exists unproven against the paths it must not break. Prompts 2 and 3 are separable because the store-level refusal is correct and testable before either mapping learns to name it; only the caller-visible distinction depends on them, and prompt 3 additionally needs the code prompt 2 introduces. Prompt 3 is last because the negative AC spans all three. The daemon executes in filename order, so approve them in this order.

## Do-Nothing Option

Not fixing this leaves the only path that silently destroys a gate in place, and the cost recurs on every occurrence rather than being paid once. Each occurrence is unrecoverable for that gate: the session that asked stays parked, the operator's answer is refused as `ITEM_CLOSED`, and the board — the only surface showing the gate — no longer lists it. Nothing reports the session as broken, so the failure surfaces only as a session that has quietly stopped. Against that, the fix is one guard, one sentinel, one error-code branch, two mapping branches and six specs.
