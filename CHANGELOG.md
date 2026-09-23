# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## Unreleased

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
