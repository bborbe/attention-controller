# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## Unreleased

- fix: render an acknowledge control on the board's `ack` card, so a report-only item can be cleared from the only surface that shows it. An `ack` item is a condition report — it asks nothing and routes nothing back — and the board derived its affordance from a single boolean with no `ack` branch anywhere, so an `ack` row fell through the same path as a `permission` row and rendered its payload and a state line and nothing else: no input, no button, not even a Skip. Measured before the change, 8 of the 15 cards on the board were `compact:<sid>` reports rendering exactly that. The control is a single **Acknowledge** rather than a Dismiss, because Dismiss on a `message` card means the `skip` answer — a meaningful thing to say about a question — and an `ack` item has no question to skip
- fix: record which arm caused a close, so a board acknowledgement is distinguishable from a producer withdrawing its own item or from the store's producer-exit sweep. `Close` gains an optional `answered_by`, written **only on the `open` → `closed` row**: on the `answered` → `closed` row the field already names the arm that *answered*, and overwriting it there would replace that with the arm that merely closed. `POST /close` accepts the value in an optional body, so an empty body — the shape every existing caller sends — is unchanged. The schema is amended first, per this repo's § Schema discipline: § Lifecycle's `open` → `closed` row named no arm as a causer at all, so the board rendering such a control would have been a causer invented in code

## v0.11.0

## v0.10.0

- feat: add an optional `description` to each `options` entry in the push request and the item, so the board renders a muted cost/risk line beneath an option's label — the label named *what* a choice was and nothing about *what it costs*, which left the cost invisible at the moment of choosing. It is per-option rather than per-item, so it could not ride `payload` or `context`, whose contract is plain operator-facing sentences carrying no machine handles; an option carrying none renders its label alone
- feat: add producer-declared `answer_cardinality` (`single`|`multiple`) to the push request and the item, so the board knows whether a question takes one pick or many and renders a radio button or a checkbox accordingly. It is declared and never derived, because the option count does not carry it — a one-option question and a many-option single-pick question are different shapes with the same list — and an absent value reads as `single`, which is what every item pushed before this field existed reads as. `message`-only, rejected with HTTP 400 elsewhere exactly as `options` is
- feat: add producer-declared `questions` (`{tab, payload, cardinality, options}`) and store-written `answers` (`{question, kind, value, values}`) to the push request and the item, so one `message` item can carry several questions and the board can render a tab per question with its own options, its own control and its own answer. `questions` is `message`-only and supersedes the top-level `payload`, `options` and `answer_cardinality` as the question units when present, leaving `payload` the card's title; two questions may not share a tab, since an answer names the question it answers by tab. `answers` is deliberately distinct from `answer` rather than a widening of it, so a single-question item's wire shape is unchanged — the two are mutually exclusive and the store rejects a call carrying both, as it does an answer naming a question the item does not ask
- feat: carry a `multiple` question's picks in a `values` list rather than joining them into one string. The carrier is fixed by the question's declared cardinality, not chosen by the caller: a `single` question's answer carries `value`, a `multiple` question's carries `values`, and an entry carrying both — or the wrong one for its question — is rejected. Joining the labels would have been lossy, since an option label containing `", "` is then indistinguishable from two picks, so a two-pick answer and a one-pick answer whose label contains the separator would read back identically. `values` is carried by the answer shape itself rather than by an `answers` entry alone, so a single-question item declared `multiple` — a card rendering checkboxes and no tabs — has somewhere to put its picks. The `answer`/`answers` exclusion runs in both directions too: an item carrying `questions` is answered through `answers`, and the single field is rejected there rather than silently storing content with no tab attached
- feat: restyle the board's `message` card to the reference question-card design — a bold question line carrying its cardinality hint, one row per option with a radio button or a checkbox per the declared cardinality, `(Recommended)` on the recommended one and the option's `description` as a muted cost/risk line beneath its label, a full-width `Other...` field, and `Dismiss`/`Next` actions; an item carrying `questions` renders a tab per question, each with its own payload, options, control and answer. A `permission` item is untouched and still renders zero answer controls. The page also now validates its answer fields inside the answer transaction, which it previously did not — it mutates a stored item and writes it back without calling `Item.Validate`, so an answer naming a question the item does not carry was accepted and stored
- feat: stamp `escalated_at` on the escalation path — written from the store's own clock inside the same compare-and-set that writes `escalated_by`, never supplied by the caller, so the two are always present together. The operator rung previously had a count and no latency: the time from escalation to answer had no starting stamp, and a reader that borrowed `closed_at` would have measured how long the item sat instead. Also reject an `escalated_by` that is not a well-formed session id rather than storing it — a placeholder such as `session-a` has no UUID to normalize to, so "repair" could only mean inventing an identity, and the live store was carrying three such values against the schema's own rule

## v0.9.0

- feat: add a Jump button to every board item whose session resolves to a live WezTerm pane, rendered beside the existing copyable `/supervisor:jump <pane>` command rather than replacing it, and extend the handover to `message` items, which previously rendered answer controls and no route back to their session. The button calls a board-side `GET /jump/{itemID}` **in the background** — the board re-resolves the pane and performs the jump **server-side**, answering `204` — so the browser stays on the board and only WezTerm switches, and the fleet-jump server's shared token never reaches the browser in a page, a link or a response. That route is gated on `Sec-Fetch-Site` because routing the jump through this origin re-opens the cross-origin vector the token exists to defeat. Both the button and the route degrade to the existing command alone when the token file at `JUMP_TOKEN_PATH` is unreadable, and the pane is re-resolved per request rather than carried in the request, since a pane id is recycled across tab moves and restarts

## v0.8.0

- feat: add a Jump button to every board item whose session resolves to a live WezTerm pane, rendered beside the existing copyable `/supervisor:jump <pane>` command rather than replacing it, and extend the handover to `message` items, which previously rendered answer controls and no route back to their session. The button links to a new board-side `GET /jump/{itemID}` that re-resolves the pane and redirects to the fleet-jump server with its shared token appended server-side, so the token never reaches the browser; that route is gated on `Sec-Fetch-Site` because routing the jump through this origin re-opens the cross-origin vector the token exists to defeat. Both the button and the redirect degrade to the existing command alone when the token file at `JUMP_TOKEN_PATH` is unreadable, and the pane is re-resolved per request rather than carried in the link, since a pane id is recycled across tab moves and restarts

## v0.7.0

- feat: add producer-declared `options` (`{label, recommended}`) and `context` to the push request and the item, and store the operator's `answer` (`{kind: option|skip|text, value}`) on the existing `open` → `answered` transition, so a `message` item is answerable on a surface other than the asker's own tab; `options` is rejected with HTTP 400 on a `permission` or `ack` item, and at most one option may carry `recommended`
- feat: render answer controls on the board for `message` items — option buttons with the recommended one marked, a skip control and a free-text field, posting to the answer endpoint as `answered_by: attention-board`; a `permission` item renders a copyable `/supervisor:jump <pane>` command and **zero** controls, because only the operator may answer a gate and only in the session that raised it. This reverses the page's recorded inert-page decision for `message` items only; the board still does not sort, since it is served by the store and the no-ranking boundary applies to it
- feat: add `POST /api/1.0/attention/{itemID}/speak`, a server-side proxy that reads an item aloud through the tts server at `TTS_URL` and returns its `message_id`, plus the board's read-aloud control. It is a proxy rather than a direct browser call because the tts server has no CORS middleware; the route and the control are both gated on `TTS_URL` being set, so a host without a tts server renders no control rather than one that always fails

## v0.6.0

- feat: add caller-supplied `decision` (allow/deny) to the answer body and the item, stamped on the answer path beside `answered_by` and `resolved_by`, so an item answered allow is distinguishable from one answered deny — one arm supplies both, so the arm alone could not say what was decided

## v0.5.0

- feat: add caller-supplied `resolved_by` session id, stamped on the answer path beside `answered_by`, so a manager-resolved item is distinguishable from an untouched one and from an operator-escalated one; add `GET /api/1.0/attention/history`, a closed-inclusive read that never prunes, so the resolved-versus-escalated split is countable

## v0.4.0

- feat: add producer-declared provenance_class field (hook/explicit/third_party) to attention items

## v0.3.1

- fix: Render no pane claim when the WezTerm listing cannot be read, instead of marking every row `unroutable` — an unreadable listing proves nothing about a pane, so the row now renders as it does when a value is absent, while an empty-but-read listing still proves a recorded pane is gone; the WezTerm CLI is also resolved through PATH with a fallback to the macOS app bundle, because that directory is not on the launchd job's PATH and a PATH-only lookup failed on every request in the deployed configuration

## v0.3.0

- feat: Render each item's provenance on the read-only page — host, cwd, tool and pane — joined from the producer's own event log by the item's dedup key, with a pane shown only when it validates against the session's current name and the row marked `unroutable` otherwise

## v0.2.0

- feat: Add the `escalated_by` field and a `POST /api/1.0/attention/{itemID}/escalate` route, recording which session is carrying an item to the operator as an atomic compare-and-set — first to stamp wins, a manager is never blocked by its own stamp, and escalation deliberately leaves the item's state untouched because it is not a transition

## v0.1.0

- Initial commit
- feat: Add the attention item store, push entry point and read path
- feat: Add single-item read, close and answer HTTP endpoints
- fix: Validate PushRequest as a request, route Close, and start make run without a Sentry DSN
