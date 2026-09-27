---
status: approved
created: "2026-09-27T22:34:47Z"
queued: "2026-09-27T22:34:47Z"
---

# Name the task a card belongs to, as a link

<summary>
- A card says where an item came from and never what it is about
- It names the session, host, working directory, pane and a timestamp, so judging a card means leaving the board to open the session
- The card gains the task its session is anchored to, drawn as a link that opens that task
- An item whose session resolves to no task renders no link at all, never an empty or dangling one
- The link is navigation, so it adds no control and changes no card's affordances
- A card whose provenance is only a task name still renders, which today it would not
- Nothing else about the card changes
</summary>

<objective>
Draw the task name the resolver now carries, as a link that reaches the task, so the operator can judge a card without leaving the board. The name is the one fact the card has always lacked, and it belongs on the line that already reports where the item came from.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree.)

Read `pkg/handler/attention-page.go` — find the page template's provenance line, the `{{if .Provenance.Resolved}}` div, and the `attentionPageRow` struct. Read the struct's existing field comments: this file records *why* each affordance exists, and the new one must match that standard.

Read `pkg/provenance.go` — find the `Provenance` struct and its `Resolved` method. The two fields this prompt draws (`TaskName`, `TaskPath`) were added by the previous prompt in this set and are already populated.

⚠️ **The gate is the trap.** The template renders the provenance line only `{{if .Provenance.Resolved}}`, and `Resolved` today tests `Host`, `Cwd`, `Tool` and `Pane` only. A `Provenance` carrying **nothing but** a task name therefore renders **no line at all** — the task name would be resolved correctly, drawn correctly, and never appear. Extending `Resolved` is part of this prompt, not an afterthought.
</context>

<requirements>
1. In `pkg/provenance.go`, extend `Resolved` so a `Provenance` whose only resolved value is `TaskName` reports true. Keep its existing doc comment's meaning — it answers "could anything about this item's origin be told" — and say in the comment that a task name is such a fact.
2. Add a `TaskURL template.URL` field to `attentionPageRow` in `pkg/handler/attention-page.go`, with a doc comment in the style of the fields around it. It is the link's destination and is empty when no task resolved. ⚠️ It must be `template.URL`, **not** `string`: html/template's URL filter admits only `http`, `https`, `mailto` and relative URLs, so a plain-string `href="{{ .TaskURL }}"` renders `href="#ZgotmplZ"` and the link is dead while every test that asserts on the row field still passes. Record that reason in the field's doc comment.
3. Add a helper in `pkg/handler/attention-page.go` that builds the link from the vault's name and the task's path as a `template.URL`, returning the empty `template.URL` when either is empty. ⚠️ The `template.URL(...)` conversion trips gosec **G203**, which `make precommit` runs (it excludes only G104), so it carries `// #nosec G203 -- <reason>` in the style of the existing precedent in `pkg/pane-lister.go`: the URL is built from the operator-configured vault name and a filesystem-derived task path, never from producer input, and `template.URL` is the only way to emit an `obsidian://` href at all. The form is `obsidian://open?vault=<vault>&file=<path>`. **Both** values are URL-encoded — spaces become `%20` and slashes `%2F`, and a trailing `.md` extension is dropped **if present** — strip it idempotently rather than assuming the path carries one, since the index's own field is not specified to include it. Escape the path with `url.PathEscape` — ⚠️ **not** `url.QueryEscape` and **not** `url.Values.Encode()`, both of which render a space as `+` and would silently break the `%20` contract above — and escape the **vault name the same way**, because it is the configured directory's base name and nothing guarantees it is free of a space. ⚠️ `template.URL` bypasses html/template's escaping by construction, so an unescaped name would emit a raw space or `&` straight into the href. Record the format in the helper's doc comment.
4. Give `newAttentionPageRow` a `vaultName string` parameter and set `TaskURL` from it plus `provenance.TaskPath`. ⚠️ **This prompt depends on the previous one having landed** — `a.VaultDir`, `TaskName` and `TaskPath` are all added by it, and nothing here compiles without them. The `3-` ordering prefix is what sequences the set; do not approve this one ahead of `2-`. ⚠️ **Neither handler receives the vault directory today**, so it must be plumbed in — this is the bulk of the change, not a detail: add a `vaultDir string` parameter to `NewAttentionPageHandler` (this file) and `NewAttentionStreamHandler` (`pkg/handler/attention-stream.go`), thread `a.VaultDir` (the field added by the previous prompt in this set) through `pkg/factory`'s page and stream factories from `main.go`, and update the **eight** test call sites (`attention-page_test.go`, `attention-board-page_test.go`, `attention-jump_test.go`, `attention-stream_test.go` — two each). Derive the name **once per request** as `filepath.Base(vaultDir)`, never per row. ⚠️ `newAttentionPageRow` itself has exactly two callers — the page handler here and the stream handler — and both must pass the **same** derived name, or a row arriving over the live stream differs from the same row on a fresh load, which is the invariant the stream handler exists to preserve.
5. In the provenance line, draw the link when `TaskURL` is set: an `<a>` whose href is `TaskURL` and whose text is the task name. ⚠️ When `TaskURL` is empty, render **nothing** — no empty anchor, no placeholder, no dash — matching the rule the rest of that line already follows for an unresolved host, cwd, tool or pane.
6. Render the link as the **first** child of the provenance div, wrapped in its own `<span class="task">`, so the task name leads the line. ⚠️ The wrapper span is required, not cosmetic: the line's separators come from a CSS rule matching only **adjacent** `<span>`s (`.provenance span + span::before { content: " · "; }`). A bare `<a>` inserted among the spans suppresses the separator beside it, and the line renders as `Task Namehost · cwd`. Do not add a CSS rule and do not restyle the anchor — requirement 7 still holds.
7. ⚠️ Do **not** alter any other control, any other row field, or any other part of the template. This prompt adds a name, not an affordance — the link is navigation and must not change what any card offers.
8. In `pkg/handler/attention-page_test.go` (`package handler_test`), add specs driving the page handler: an item whose provenance carries a task renders an anchor whose href is the expected `obsidian://` URL and whose text is the task name; an item whose session resolves to no task renders **no** anchor (assert on the absence, using `rowOf` to scope the assertion to that item's row — a page-wide check cannot fail, because other rows legitimately carry one); and an item whose provenance is **only** a task name still renders its provenance line, which is the `Resolved` change and fails without it. ⚠️ Write the expected href as a **hand-written literal** (`obsidian://open?vault=Personal&amp;file=25%20Tasks%2F…`), never one built with the same helper the code uses — a shared helper would agree with itself whatever it produced. ⚠️ html/template HTML-escapes the `&` in the attribute, so the **served** markup reads `…vault=…&amp;file=…` — write the literal as it appears in the served HTML, since the assertion is a raw-string `ContainSubstring` over the `rowOf` slice. ⚠️ The no-task fixture must carry at least one **other** resolved provenance value (a host, say), or the provenance div never renders at all and the absence assertion passes vacuously. Add a case in `pkg/handler/attention-stream_test.go` asserting the row the stream sends for a task-carrying item carries the same anchor, since a stream handler wired with an empty vault name compiles and passes every check above while rendering a link-less row.
9. Extend `pkg/provenance_test.go` with a case asserting `Resolved` is true for a `Provenance` holding only `TaskName`, and false for the zero value.
10. Self-check before finishing: re-run `<verification>` and confirm it passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Errors use `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Tests use Ginkgo/Gomega, and the suite runs against a real in-memory libkv DB, never a mocked `libkv.DB`.
- No `//nolint` without an explanation.
- Repo-relative paths only — no absolute or home-relative paths.
- ⚠️ The page template is a Go **raw string literal**, so a backtick anywhere inside its CSS, JS or HTML terminates the string and surfaces as a parse error on a line nowhere near the cause. Do not introduce one.
</constraints>

<verification>
Run `make precommit` -- must pass.

Then confirm the four parts actually landed, because the page renders nothing without the first:
- `grep -A6 'func (p Provenance) Resolved' pkg/provenance.go | grep -q 'TaskName'` -- the gate must admit a task-only provenance.
- `grep -q 'TaskURL template.URL' pkg/handler/attention-page.go` -- the row must carry the link as a `template.URL`; a plain `string` field satisfies a bare `grep -q 'TaskURL'` while the href renders `#ZgotmplZ`.
- `grep -q 'href="{{ .TaskURL }}"' pkg/handler/attention-page.go` -- the template, not only the row struct, must interpolate it.
- `grep -q 'url.PathEscape(' pkg/handler/attention-page.go` -- the escaper that yields `%20`/`%2F` must be the one used. ⚠️ The trailing paren is load-bearing: without it the check is satisfied by a doc comment that merely names the function.

⚠️ Do **not** grep for `obsidian://` alone: requirement 3 also puts that literal in the helper's doc comment, so the check passes on a helper that returns `""`.

⚠️ Do not assert `grep -q 'TaskName' pkg/handler/attention-page.go` alone: the row draws `.Provenance.TaskName`, so that string is present even if `Resolved` was never extended — which is exactly the failure this prompt exists to prevent.
</verification>
