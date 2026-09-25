# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## Unreleased

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
