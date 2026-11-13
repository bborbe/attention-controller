---
status: completed
spec: [018-attention-stage-delta-histogram]
summary: Added the bulk DeliveryAttempts read to the AttentionStore interface and its store, notifying, and federating implementations, regenerated the mock, added specs, and updated the changelog.
execution_id: 265d36f2-c6f7-5a45-9e02-d0cbf5080223-exec-058-spec-018-delivery-attempts-read
dark-factory-version: dev
created: "2026-11-13T10:30:00Z"
queued: "2026-11-13T10:36:24Z"
started: "2026-11-13T10:36:26Z"
completed: "2026-11-13T10:45:55Z"
branch: dark-factory/attention-stage-delta-histogram
---

# Add the bulk delivery-attempt read to the attention store

<summary>
- The attention store gains one new read that returns every delivery-attempt record at once.
- It exists so a metrics collector can join attempts to items without opening one read transaction per item.
- The read is added to the store interface and to all three implementations that carry that interface.
- The notifying wrapper forwards it without signalling, because it is a read.
- The federating wrapper reads only this instance's attempts, never a peer's, matching the delivery trail's local-only rule.
- The generated mock is regenerated rather than hand-edited.
- Tests prove the real store returns attempts keyed by item id, the notifying wrapper delegates without signalling, and the federating wrapper never asks the peer.
- Nothing about how an attempt is written changes.
</summary>

<objective>
Add `DeliveryAttempts(ctx context.Context) (map[ItemID]DeliveryAttempt, error)` to the `AttentionStore` interface and all three implementations (the libkv-backed store, the notifying wrapper, the federating wrapper), so the stage-delta collector in the follow-up prompt can read the whole attempt population in one read transaction. This is a read-only addition: no write path, no stored field, and no peer call changes.
</objective>

<context>
Read these files fully before changing anything:

- `pkg/attention-store.go` — the `AttentionStore` interface and its `//counterfeiter:generate` directive. `Delivery` is the last method; the new method goes beside it.
- `pkg/attention-store-impl.go` — the `attentionStore` struct, its `attempts libkv.StoreTx[string, DeliveryAttempt]` field, and the existing `History` read (which shows the `a.db.View` + `StoreTx` shape to follow). ⚠️ The spec's container-executable verification greps this exact file for `DeliveryAttempts`, so implement the method here rather than in a sibling file.
- `pkg/attention-store-attempt.go` — `RecordAttempt` (the write) and `Delivery` (the single-item read). The new method is the bulk sibling of `Delivery`.
- `pkg/attention-store-notifying.go` — the notifying decorator. `Delivery` and `MarkSeen` show how a read delegates without signalling.
- `pkg/attention-store-federating.go` — the federating decorator. `Delivery` and `RecordAttempt` show the local-only reasoning the new read must copy.
- `pkg/attention-remote.go` — `RemoteAttentionStore`. ⚠️ Do **not** add the method here: the interface's own doc explains why delivery bookkeeping is local-only.
- `pkg/delivery-attempt.go` — the `DeliveryAttempt` record (`ItemID`, `Carrier`, `Outcome`, `AttemptedAt`).
- `mocks/attention-store.go` — the counterfeiter fake. It is generated; do not hand-edit.
- `pkg/pkg_suite_test.go` — the `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` directive that regenerates every fake in the package.
- `pkg/delivery-attempt_test.go` — the existing attempt specs and their movable-clock store helper.
- `pkg/attention-store-federating_test.go` — the faked-local/faked-peer setup and the `mutableClock` helper (defined in `pkg/session-liveness-cache_test.go`).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md` — counterfeiter generation, never hand-edit.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega shape, real in-memory `libboltkv` stores.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc on the new interface method.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` wrapping, no `fmt.Errorf`.
</context>

<requirements>
1. **Add the method to the `AttentionStore` interface** in `pkg/attention-store.go`, immediately after the existing `Delivery(...)` method and before the closing brace of the interface:

   ```go
   // DeliveryAttempts returns every delivery-attempt record the store holds,
   // keyed by the item id it belongs to. It is the bulk read a population-wide
   // reader uses: one read transaction for the whole store, never one Delivery
   // call per item.
   //
   // It is read-only. It never prunes, never mutates and never consults a peer.
   DeliveryAttempts(ctx context.Context) (map[ItemID]DeliveryAttempt, error)
   ```

   The GoDoc comment is required by the repo's Definition of Done.

2. **Implement it on the libkv-backed store** in `pkg/attention-store-impl.go` (place it near `History`). Use a single read-only `View` over the `attempts` bucket and key the result by the record's own `ItemID`:

   ```go
   func (a *attentionStore) DeliveryAttempts(
   	ctx context.Context,
   ) (map[ItemID]DeliveryAttempt, error) {
   	result := make(map[ItemID]DeliveryAttempt)
   	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
   		return a.attempts.Map(
   			ctx,
   			tx,
   			func(ctx context.Context, _ string, attempt DeliveryAttempt) error {
   				result[attempt.ItemID] = attempt
   				return nil
   			},
   		)
   	})
   	if err != nil {
   		return nil, errors.Wrap(ctx, err, "delivery attempts failed")
   	}
   	return result, nil
   }
   ```

   - `libkv.StoreTx.Map` already treats a missing bucket as an empty bucket and returns `nil` (verified against `github.com/bborbe/kv@v1.21.29`), so a freshly created store returns an empty map with no error.
   - `libkv` and `github.com/bborbe/errors` are already imported by `attention-store-impl.go`.
   - It must be a `View`, never an `Update`: the collector must not take the writer lock or mutate the store.

3. **Implement it on the notifying wrapper** in `pkg/attention-store-notifying.go`, beside `Delivery`:

   ```go
   // DeliveryAttempts delegates and never signals: it is a read.
   func (n *notifyingAttentionStore) DeliveryAttempts(
   	ctx context.Context,
   ) (map[ItemID]DeliveryAttempt, error) {
   	return n.store.DeliveryAttempts(ctx)
   }
   ```

4. **Implement it on the federating wrapper** in `pkg/attention-store-federating.go`, beside `Delivery`:

   ```go
   // DeliveryAttempts delegates to the local store and is never proxied. The
   // delivery trail is the LOCAL arm's bookkeeping about a delivery this
   // process performed; a peer holds no record of it, so a federated card
   // contributes no attempt-derived observation here.
   func (f *federatingAttentionStore) DeliveryAttempts(
   	ctx context.Context,
   ) (map[ItemID]DeliveryAttempt, error) {
   	return f.local.DeliveryAttempts(ctx)
   }
   ```

   Do **not** add the method to `RemoteAttentionStore` in `pkg/attention-remote.go`. Its absence there is deliberate and documented.

5. **Regenerate the mock.** Run `go generate -mod=mod ./pkg` from the repo root (this executes the counterfeiter directive in `pkg/pkg_suite_test.go` and rewrites every fake in `mocks/`). Do not hand-edit `mocks/attention-store.go`. Confirm the regenerated file now carries `DeliveryAttemptsStub`, `DeliveryAttemptsReturns`, `DeliveryAttemptsCalls` and `DeliveryAttemptsCallCount`.

6. **Add the store-level specs** in a new file `pkg/attention-store-delivery-attempts_test.go` (`package pkg_test`, so it joins the existing Ginkgo suite). Build a real store the same way `pkg/delivery-attempt_test.go` does: `libboltkv.OpenTemp(ctx)`, a `mocks.SessionLivenessChecker` whose `IsLiveReturns(true)`, `pkg.NewItemIDGenerator()`, and a fixed `libtime.CurrentDateTimeGetterFunc`. Cover:

   - **Empty store:** `DeliveryAttempts` returns a non-nil empty map and no error.
   - **One recorded attempt:** push an item (reuse the `push` helper shape from `pkg/delivery-attempt_test.go`), call `RecordAttempt(ctx, item.ItemID, "carrier", pkg.DeliveredOutcome)`, then assert `DeliveryAttempts` returns a map of length 1 whose value is keyed by `item.ItemID`, has `Outcome == pkg.DeliveredOutcome`, `Carrier == "carrier"`, and a non-zero `AttemptedAt`.

7. **Add the federating-wrapper spec** in the same file (or `pkg/attention-store-federating_test.go`). Follow that file's faked-local/faked-peer setup:

   - `local := &mocks.AttentionStore{}`, `remote := &mocks.RemoteAttentionStore{}`, `store := pkg.NewFederatingAttentionStoreWithClock(local, remote, newMutableClock())`.
   - `local.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{}, nil)`.
   - Call `store.DeliveryAttempts(ctx)` and assert the result is empty, `local.DeliveryAttemptsCallCount() == 1`, and an item only the peer holds does not appear (set `remote.ReadReturns(pkg.Items{{ItemID: "peer-only"}}, nil)` and assert `"peer-only"` is absent from the result). This is the spec's AC8 evidence that the federating wrapper never asks the peer for attempts.

8. **Add the notifying-wrapper spec** in the same file: wrap `mocks.AttentionStore` in `pkg.NewNotifyingAttentionStore`, stub the inner `DeliveryAttemptsReturns`, call through the wrapper, and assert the map is returned unchanged and `notifier.NotifyCallCount() == 0` (a read must not wake subscribers).

9. **Self-check before finishing:** re-run every `<verification>` command. Confirm the four production files each mention `DeliveryAttempts`, the regenerated mock does, `pkg/attention-remote.go` still does not, and `make test` passes.
</requirements>

<constraints>
- The new store method is named `DeliveryAttempts`, returns `map[ItemID]DeliveryAttempt`, and is added to the `AttentionStore` interface and all three implementations (the store, the notifying wrapper, the federating wrapper).
- The federating wrapper delegates the new read to its local store only, never to the peer.
- No store write path changes: `Push`, `MarkSeen`, `Answer`, `RecordAttempt`, `Close`, `Escalate`, `SweepAnswered` are untouched. The only store addition is this one bulk read.
- Do not add `DeliveryAttempts` to `RemoteAttentionStore`.
- Do not hand-edit `mocks/attention-store.go`; regenerate it with `go generate -mod=mod ./pkg`.
- Errors follow `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Tests use Ginkgo/Gomega against a real in-memory `libboltkv` store (or the generated mocks for the decorators), never a mocked `libkv.DB`.
- The delivery trail is read-only: `DeliveryAttempts` must use `db.View`, never `db.Update`.
- Existing tests must still pass.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run inside the repo root:

- `make test` — the Ginkgo suite passes, including the new specs.
- `make precommit` — exits 0 (its `generate` target regenerates the mock).
- `grep -c 'DeliveryAttempts' pkg/attention-store.go pkg/attention-store-impl.go pkg/attention-store-notifying.go pkg/attention-store-federating.go` — prints a non-zero count for **each** of the four files.
- `grep -c 'DeliveryAttempts' mocks/attention-store.go` — prints a non-zero count (the mock was regenerated).
- `! grep -q 'DeliveryAttempts' pkg/attention-remote.go` — succeeds only when the peer interface was left unchanged.
- `grep -A8 'func (a \*attentionStore) DeliveryAttempts' pkg/attention-store-impl.go | grep -q 'db.View'` — the bulk read runs in a read-only transaction.
- `! grep -q 'func (a \*attentionStore) DeliveryAttempts' pkg/attention-store-attempt.go` — the method lives in `attention-store-impl.go`, where the verification greps for it.
</verification>
