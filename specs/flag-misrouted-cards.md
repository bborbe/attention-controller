---
status: draft
created: 2026-10-28
---

## Summary

- The operator can mark an attention card as *misrouted* — "this should never have reached me" — with a written reason.
- A flag is **not an answer**: the item stays `open`, `answered_at` stays absent, and the gate remains answerable.
- A flagged card leaves the board's default view and stays reachable at `?hide=none` and behind an explicit flag toggle.
- The reason and timestamp are retained on the item, attributable to the producer that raised it.
- Nothing reroutes automatically off a flag; flags are evidence for a routing fix, never a trigger.

## Problem

The only way to dispose of a misrouted card today is to answer it. Answering clears the board but leaves the routing defect in place, so the same card type returns next week, and the act of answering destroys the only signal that it was misrouted. There is no way for the operator to say "this should not have reached me" without also pretending to answer it.

## Goal

An attention item carries a flag — a reason and a timestamp — that the operator writes from the board. A flagged item is unchanged in every lifecycle respect: it is still `open`, still unanswered, still answerable. The board hides flagged cards from the view the operator actually sees and keeps them one click away. The record survives so a per-producer count of misrouted cards can be built from it later.

The authoritative contract is **`docs/attention-item-flagging.md`** in this repo, which is a copy of the vault page `Attention Item Schema` § Flagging and its `flag_reason` / `flagged_at` field rows. **Implement that contract; do not redefine it.** If this spec and that doc disagree, the doc wins and the disagreement is a finding to report, not a choice to make in code.

## Non-goals

- The weekly aggregation and reporting of flags per producer and reason. Separate work.
- Any automatic routing change driven by a flag. Flags are evidence, not triggers.
- Deciding which producer is "wrong". That is the routing fix's job.
- Flagging as a substitute for answering, closing, dismissing or hiding. A flag adds an axis; it removes none.
- **Flagging an item that is no longer open.** A flag is a field write on an `open` item; an item already answered or closed is refused (see Failure Modes). Recovering the misroute signal from an already-answered card is deliberately out of scope.

## Assumptions

- `docs/attention-item-flagging.md` is **committed with this work** and is authoritative. Prompt 1 commits it before writing any Go.
- The board is the only arm that renders the flag control; no other arm needs to change.
- The item record is the only storage surface — no new store, no new collection.

## Alternatives

- **Reuse `answer.kind: hidden`.** Rejected: `hidden` makes `open` → `answered` and tells the producer the question was handled elsewhere. A flag asserts the opposite and must leave the item answerable. Reusing the answer path sets `answered_at` and destroys the distinction the feature exists to create.
- **Add a session-ledger field instead of an item field.** Rejected: the flag is a property of the card, and the weekly count reads it per producer from the item. AC8 proves no session-record field was added.
- **Filter flagged rows server-side.** Rejected: the board does not filter server-side for anything. It renders every row and parks the hidden ones client-side (`applyFilter`, `data-item-id`), which is what keeps "the hidden rows are still served, still parked client-side and still one toggle away" true. A second, server-side mechanism for flags alone would invert that invariant and would be undone by the SSE stream, which inserts rendered rows directly. See Desired Behavior 5.
- **Do nothing.** See Do-Nothing Option.

## Acceptance Criteria

- [ ] **AC1 — a flag is recorded without touching the answer or the lifecycle.** After a `POST /api/1.0/attention/{itemID}/flag`, a store read returns `state: open`, `answered_at` absent, and a non-empty `flag_reason` with a `flagged_at` timestamp. **Negative half:** `git diff origin/master -- pkg/transition.go` is empty. Evidence: a Ginkgo spec asserting all four stored values from a real in-memory libkv store, plus the empty diff.
- [ ] **AC2 — an empty reason is refused and nothing is written.** A flag POST with `{"reason": ""}` and one with a whitespace-only reason both return **HTTP 400** with wire code `FLAG_REASON_REQUIRED`, and a subsequent store read shows `flag_reason` still empty and `flagged_at` still absent. Evidence: handler spec asserting status, code and the unchanged record.
- [ ] **AC3 — the control renders on every card and is wired to the endpoint.** Each rendered card's row subtree contains a `data-flag-control` form carrying the page's own JS-submit shape — the class the delegated submit handler matches, exactly as `form.answer` is matched today — plus a `name="reason"` input and a submit control, so the click posts JSON to `/api/1.0/attention/{itemID}/flag`. It must **not** carry `action`/`method`: the page submits every control through one delegated handler that calls `preventDefault` and `fetch`es JSON, so a native-submitting form would navigate the operator off the board into a JSON body and its urlencoded payload would never be parsed. Evidence: a served-row assertion for the form, its class and its `name="reason"` input; a submit-path spec asserting the handler posts JSON to the flag endpoint and the item's `flag_reason` is set; and the operator round-trip. Assert per card with the existing `rowBlock` helper rather than a global substring count, and use `ContainSubstring` + `data-item-id`; do not add `goquery`.
- [ ] **AC4 — a flagged card is served, marked, and parked by the default view.** The board renders every row and filters client-side, so the evidence is the mark and the filter state, not body absence:
  - the served body contains the flagged card's row carrying `data-flagged="true"`;
  - the board's switches render **checked when their filter is active** — `aria-checked` mirrors the existing "Hide answered" switch — so the bare URL renders the flag filter **on**, `?flags=none` renders it **off** with the hide-answered filter still on, and `?hide=none` renders **both** off;
  - the client filter predicate reads `data-flagged`, so the same rule parks a row that arrives later.
  Evidence: board-page specs asserting the attribute and each switch's rendered `aria-checked` for all three URLs, plus a served-script assertion that the predicate references `data-flagged`.
- [ ] **AC5 — the live stream cannot re-insert a parked card unmarked.** A row delivered by `GET /api/1.0/attention/stream` carries the same `data-flagged` attribute and goes through the same client predicate, so a flagged card that changes while the page is open stays parked rather than reappearing unfiltered. Evidence: a stream spec asserting the rendered event payload carries `data-flagged`, plus a served-script assertion that the insert path calls the filter.
- [ ] **AC6 — unflagging clears the flag and the card returns.** A `POST /api/1.0/attention/{itemID}/unflag` against a flagged item leaves `flag_reason` empty and `flagged_at` absent, and the item renders in the default view again. Evidence: store spec read-back plus a board page spec.
- [ ] **AC7 — the reason is escaped wherever it renders.** A flag whose reason is `<script>alert(1)</script>` renders escaped: the served body contains `&lt;script&gt;alert(1)` and `ContainSubstring("<script>alert(1)")` is **false** — asserted at `?flags=none` and at `?hide=none`. Evidence: board-page specs asserting both the positive escaped literal and the negative raw one on both views.
- [ ] **AC8 — the flag follows the item's owner.** Flagging a peer-owned item proxies to the peer: the local store's record is unchanged and the peer's record carries the reason. A peer outage returns **HTTP 503** `PEER_UNAVAILABLE`; a peer that answers **409 `ITEM_NOT_OPEN`** or **404** maps to the same status and code it returned, rather than being collapsed into a local write or a generic conflict. Evidence: federating-store spec with a counterfeiter peer, plus remote-client specs for each mapping.
- [ ] **AC9 — no session-record field is added.** `git diff origin/master -- pkg/session-ledger.go pkg/session-ledger-patch.go` is empty; the change is confined to the attention item, its store, its handlers and the board.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, lint, test and security checks exit 0
- `make test` — the Ginkgo suite passes, including the new flag specs
- `ls docs/attention-item-flagging.md` — succeeds
- `grep -n 'flag_reason' pkg/attention-item.go` — returns ≥1 line
- `grep -n 'ErrFlagReasonRequired' pkg/errors.go` — returns ≥1 line
- `grep -n 'FLAG_REASON_REQUIRED' pkg/attention-error-codes.go` — returns ≥1 line
- `grep -n '/flag' main.go` — returns ≥1 line (route registration lives in `registerAttentionAPIRoutes`, and a forgotten registration is a 404 on a working endpoint, not a compile error)
- `grep -n '/unflag' main.go` — returns ≥1 line (`"/flag"` is not a substring of `"/unflag"`, so the first grep cannot cover it)
- `grep -n 'data-flagged' pkg/handler/attention-page.go` — returns ≥1 line

### Operator-executable (runs on the host after PR merge)

- `make install` — builds, codesigns as `de.bborbe.attention-controller`, reloads the `com.bborbe.attention-controller` launchd job
- Round-trip against the running board page (not the JSON read handler, which does not honour view parameters): open `http://127.0.0.1:18080/` and confirm the card is listed; flag it; confirm it leaves the listing and that `http://127.0.0.1:18080/?flags=none` lists it again with its reason visible; unflag; confirm it returns to the bare URL.

## Desired Behavior

1. **A flag is a field write, not a transition.** Writing a flag leaves `state` and every answer field untouched, and the item stays answerable afterwards. No row is added to the transitions table.
2. **A reason is required.** An empty or whitespace-only reason is refused with HTTP 400 and wire code `FLAG_REASON_REQUIRED` before any write is attempted; the record is left exactly as it was.
3. **The timestamp is the store's, not the caller's.** `flagged_at` is stamped from the store's own clock inside the same compare-and-set that writes `flag_reason`, so the pair is always present together or absent together.
4. **The control sits on the card and writes the flag.** Every rendered card carries, inside its own row, a flag form with a reason input and a submit control — beside the existing answer controls (`✓ Submit answer`, `✕ Dismiss`, `Hide`), not in a page-level toolbar. A control that renders without a working endpoint is a false affordance, which is the failure class the read-aloud toggle already guards against.
5. **The default view parks flagged cards, client-side.** The board renders every row and parks the ones the current view hides — the mechanism it already uses for answered records. A flagged row is served carrying `data-flagged="true"` and the filter predicate parks it. The flag filter is a second switch marked `data-flag-filter` and labelled "Hide flagged", whose checked state mirrors the existing "Hide answered" switch: checked means the filter is active. The parameter is `flags`, and it names the set that is *hidden*, matching the board's existing convention — so its default value is `flagged`, an absent parameter and `flags=flagged` render identically, and `flags=none` disables the filter. `?hide=none` disables **both** filters (the union view); `?flags=none` disables **only** the flag filter, so revealing a misroute never also floods the board with answered history.
6. **The stream obeys the same predicate.** Rows arriving over the SSE stream carry the same `data-flagged` attribute and go through the same filter, so a card that changes while the page is open cannot reappear un-parked.
7. **A flag is reversible.** Unflagging clears both fields and the card returns to the default view. Unlike `closed`, a flag is not terminal.
8. **The flag follows the item's owner.** For a peer-owned item the flag is proxied to the owning store and applied there, exactly as escalate and close are.

## Constraints

- **Contract discipline.** Every field, state and transition comes from `docs/attention-item-flagging.md`. That doc already defines `flag_reason` and `flagged_at` and states that a flag is neither a transition nor an answer. Do not add a field the doc does not define; if it is silent, report it rather than filling it silently.
- **`flag_reason` and `flagged_at` are store-written.** The caller supplies only the reason text. The timestamp never comes from the caller, exactly as `escalated_at` does not.
- **The compare-and-set lives in one place.** Read, compare and write happen inside a single `db.Update` transaction, as `Escalate` and `Close` do. A separate `View` then `Update` is the bug this pattern exists to prevent.
- **Routes.** `POST /api/1.0/attention/{itemID}/flag` and `POST /api/1.0/attention/{itemID}/unflag`, registered in `registerAttentionAPIRoutes` in `main.go` beside `/escalate`, and mirrored in the remote client and the federating wrapper exactly as `/escalate` is.
- **New sentinels and codes.** `ErrFlagReasonRequired` goes in `pkg/errors.go`; wire code `FLAG_REASON_REQUIRED` goes in `pkg/attention-error-codes.go`, following the existing `Err`-prefixed sentinel and SCREAMING_CASE code conventions.
- **Write verbs stay POST.** This surface has no `DELETE` route and gains none. A `DELETE` route would need a second registration shape the federating client does not model.
- **The filter is client-side, like the existing one.** Do not add server-side row exclusion for flags: it would be a second filtering mechanism, it would break the "hidden rows are still served" invariant, and the SSE insert path would defeat it. The row carries the mark; the script decides.
- **`?hide=none` widens, deliberately.** That URL already meant "show answered records too"; it now also reveals flagged cards, because it disables every filter. The widening is accepted: `hide=none` is the union view by its own definition, and `flags=none` exists for the narrower reveal. A bookmark to `?hide=none` therefore shows strictly more than it did before — no card that was visible disappears.
- **The `data-` attribute names do not prefix each other.** Use `data-flag-control` (the form), `data-flag-filter` (the switch) and `data-flagged` (the row mark), so a substring count on one cannot match another.
- **Error handling** follows `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- **Tests** use Ginkgo/Gomega against a real in-memory libkv DB, never a mocked `libkv.DB`. Counterfeiter mocks live in `mocks/`. Assert rendered HTML with `ContainSubstring` and `data-item-id` markers, the convention the existing board specs use.
- **The existing answer path must not change.** `hidden`, `skip`, `dismiss` and the corner-X behaviour are untouched; a flag is a new axis beside them.
- **No new session field.** The flag lives on the attention item only.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Flag POST with an empty or whitespace-only reason | HTTP 400, code `FLAG_REASON_REQUIRED`, no write | Re-POST with a non-empty reason returns 200 and `flag_reason` is set |
| Flag POST against an unknown item id | HTTP 404, no write | Correct the id; a read of a real item returns 200 |
| Flag POST against an already-answered or closed item | HTTP 409, code `ITEM_NOT_OPEN`, no write — the flag is a field write on an `open` item | None; the misroute signal for an already-answered card is out of scope |
| Two flags race on the same item | Last writer wins; both are valid flags, the later reason replaces the earlier | Re-flag with the intended reason; the store read shows it |
| Crash mid-transaction | No partial flag — `flag_reason` and `flagged_at` are written in one `db.Update`, so either both land or neither does | Retry the POST |
| Flag POST while the peer owns the item | Proxied to the peer; a peer failure returns 503 `PEER_UNAVAILABLE` and writes nothing locally | Retry once the peer is reachable; a local read still shows no flag |
| Peer owns the item and refuses with 409 or 404 | The peer's status and code are surfaced unchanged; nothing is written locally | The operator sees the peer's own verdict rather than a rewritten one |
| Unflag against an item that is not flagged | HTTP 200, no-op success — the caller's intent already holds | None needed |
| Flagged item later answered | Both records coexist: the item is answered *and* carries its flag, so the misroute evidence survives the answer | None; this is the intended behaviour |
| Flagged row arrives over the stream while the page is open | The inserted row carries `data-flagged` and the client predicate parks it | None; this is the intended behaviour |

## Security / Abuse

- The reason is operator-supplied free text rendered on the board — **twice**, in the card row and in the revealed view. It must be escaped on render exactly as every other card field is; the existing card-text escaping path is the one to use, not a new one. AC7 proves it with positive and negative evidence on both views.
- **The flag endpoint's auth matches its siblings, not a stronger claim.** `registerAttentionAPIRoutes` is mounted on both listeners; the bearer middleware applies only to the cluster listener. On the Mac-local board the write endpoints are unauthenticated, by operator ruling — which is the surface the board's own form posts to. The flag endpoint therefore adds no privilege beyond what `/answer` and `/close` already carry: it neither answers a gate nor closes an item.
- No reason length cap is introduced. If one is added later it must be declared in the contract doc first. Agent decides the exact wording of the 400 body.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Domain and store: commit `docs/attention-item-flagging.md`, item fields, `Flag`/`Unflag`, sentinel, wire code, store specs | 1, 2, 3, 7 | AC1 (store half), AC2, AC6 (store half), AC9 | — |
| 2 | HTTP layer: handlers, factory, route registration in `main.go`, remote client, notifying and federating wrappers | 2, 8 | AC2 (HTTP half), AC8 | prompt 1 |
| 3 | Board render: per-card form, `data-flagged`, `flags` filter, `hide=none` union, stream predicate, escaping | 4, 5, 6 | AC3, AC4, AC5, AC6 (render half), AC7 | prompts 1, 2 |
| 4 | Integration and CHANGELOG: end-to-end round-trip across store and served page, `## Unreleased` bullet | — | re-verifies AC1–AC7 | prompts 1–3 |

Rationale: prompt 1 establishes the store contract every later layer calls. Prompt 2 adds the endpoints prompt 3's form posts to — the render prompt cannot be verified without a live route, so it now depends on 2 as well as 1. Prompt 4 is the only one that needs all three.

## Do-Nothing Option

The operator keeps answering misrouted cards. Each answer clears the board and destroys the only evidence that the card was misrouted, so the routing defect is never counted, never attributable to a producer, and never fixed. The cost is not the click — it is that the same card returns every week and nothing in the system can say why.
