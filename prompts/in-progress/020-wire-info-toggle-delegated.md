---
status: approved
spec: [005-card-metadata-behind-info]
created: "2026-10-02T08:03:00Z"
queued: "2026-10-02T08:22:35Z"
---

# Wire the card's info affordance with a document-delegated listener

<summary>
- Activating the card's `i` control reveals that card's information panel and sets `aria-expanded="true"`; activating it again hides the panel and sets `aria-expanded="false"`.
- The control is a real button, so keyboard activation behaves exactly like a pointer click with no extra code.
- The reveal survives the board's live stream replacing a row's markup — the control is never rendered and inert.
- The listener is bound once on the document, so a row the stream re-renders keeps a working control.
- A row that carries no panel is a no-op rather than a throw.
- The board's existing stream, note, filter and answer behaviour is untouched.
</summary>

<objective>
Make the `i` affordance added by the sibling markup prompt actually reveal the card's information panel, and keep it working across the board's live stream row-swap. The board's stream replaces a row's whole `outerHTML` on every event, so a per-button listener dies with the node it was bound to and leaves the control rendered and dead — the defect this board already shipped at v0.19.0 for the read-aloud control and fixed for the jump control. This prompt ships the interaction only.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

⚠️ **This prompt depends on the sibling markup prompt (`1-restructure-attention-row-behind-info.md`) having landed.** That prompt adds the `info-toggle` button (`class="info-toggle"`, `data-info-toggle`, `aria-expanded="false"`) and the `info-panel` div (`class="info-panel"`, `data-info-panel`, `hidden`) to the `attention-row` sub-template, and it adds the `.info-toggle[aria-expanded="true"]` style rule. Nothing here is observable without them. If `grep -c 'class="info-panel"' pkg/handler/attention-page.go` prints `0`, stop and report `status: failed` with the message `"the info panel is not yet deployed (prompt 1)"` — do not add the markup yourself.

Read `pkg/handler/attention-page.go` — the whole `<script>` inside `attentionPageTemplate`, and in particular:
- the read-aloud control's document-level listener, whose body starts `var button = event.target.closest('button[data-speak]');`;
- the jump control's document-level listener, whose body starts `var button = event.target.closest('button[data-jump]');`;
- `upsertRow` and `preparedHTML`, which replace a row's whole `outerHTML` on every stream event.

**The jump control's document-level listener is the pattern to mirror — it is the converted form of a per-button binding, and the comment above it records why:**

```js
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-jump]');
  if (!button) { return; }
  var row = button.closest('li.item');
  fetch(button.getAttribute('data-jump'), { method: 'GET' })
    ...
});
```

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for the Ginkgo/Gomega conventions, and `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` for the coverage rules.

**Acceptance-criteria ownership for this set.** This prompt owns **no** acceptance criterion — it is the enabler for the browser cases in the e2e prompt. The behavioural proof is that prompt's Playwright case; the spec below is a cheap source-presence guard.
</context>

<requirements>
1. **Add a document-delegated click listener for `button[data-info-toggle]`** to the `<script>` inside `attentionPageTemplate` in `pkg/handler/attention-page.go`. Place it immediately **after** the jump control's document-level listener (the block whose body begins `var button = event.target.closest('button[data-jump]');`) and immediately **before** `function showJumpNote(row, message, isError) {`.

   ```js
   /* The card's machine identity, behind an `i` at the card's top right. The
      panel ships hidden in the served markup and this only flips that state, so a
      row the stream re-renders comes back closed with the control still
      operable — never open-by-default, and never rendered-and-dead.

      ⚠️ Delegated on the document, NOT bound per button — for the same reason the
      Jump and read-aloud controls are, and the reason is a defect rather than a
      preference. The stream replaces a row's whole outerHTML on every event
      (upsertRow), and a listener attached to the old node dies with it: the
      button is there, the click does nothing, and no panel opens. That is the
      failure this repo already shipped once at v0.19.0.

      ⚠️ The state is read from the button's own aria-expanded attribute rather
      than from a page-local map, and that is deliberate rather than incidental:
      the attribute is server-rendered on every row the stream sends, so it is the
      one place the open/closed state survives a swap without a second store to
      keep in step. */
   document.addEventListener('click', function (event) {
     if (!event.target || !event.target.closest) { return; }
     var button = event.target.closest('button[data-info-toggle]');
     if (!button) { return; }
     var row = button.closest('li.item');
     if (!row) { return; }
     var panel = row.querySelector('[data-info-panel]');
     if (!panel) { return; }
     if (button.getAttribute('aria-expanded') === 'true') {
       button.setAttribute('aria-expanded', 'false');
       panel.setAttribute('hidden', '');
       return;
     }
     button.setAttribute('aria-expanded', 'true');
     panel.removeAttribute('hidden');
   });
   ```

   The `if (!row) { return; }` and `if (!panel) { return; }` guards are required rather than defensive: a throw inside a delegated document listener escapes to `window.onerror` and is exactly the class of failure the page's own top-level handlers exist to make visible. A row with no panel is a no-op.

2. **Do not add a `console.error('attention board: …')` line.** That prefix is reserved for the board's nine ACTION lines and an existing spec pins its count at nine. This handler has no error path, so it logs nothing.

3. **Do not change `upsertRow`, `preparedHTML`, or the order of `var prepared = preparedHTML(html, itemID)` and `row.outerHTML = prepared`.** Existing specs assert those strings and that order by position. The reveal's survival across a swap is a consequence of the delegation plus the server-rendered `hidden` panel — no map, no re-apply step and no new branch in the update path.

4. **Add a source-presence spec** in `pkg/handler/attention-page_test.go`, inside the top-level `Describe("AttentionPageHandler", …)` block, beside `It("re-renders a failure note after the stream replaces its row", …)`. Name it `It("ships the card info affordance and its delegated listener", …)`. Render the page once with `get("GET")` and assert on the served body:
   - `Expect(body).To(ContainSubstring("button[data-info-toggle]"))` — the delegated selector is served;
   - `Expect(body).To(ContainSubstring("[data-info-panel]"))` — the panel lookup is served;
   - `Expect(body).To(ContainSubstring("button.setAttribute('aria-expanded', 'true')"))` and `Expect(body).To(ContainSubstring("button.setAttribute('aria-expanded', 'false')"))` — both transitions are served;
   - ⚠️ **Do not re-assert the ACTION-line count here.** `pkg/handler/attention-page_test.go` already carries `It("keeps the reserved console prefix at exactly nine lines", …)`, which asserts `strings.Count(body, "attention board: ")` equals `9`. Adding a second copy would be one assertion in two places. Reference that case in a comment instead, and if it reddens, your listener added a log line — remove it rather than changing the count.
   - Write a comment in the spec stating plainly that this is a **source-presence guard and not browser behaviour** — the inline script has no unit harness, and the behavioural proof is the Playwright case in the e2e prompt.

5. **Add nothing to `CHANGELOG.md`.** The `feat:` bullet for this feature lands in the sibling markup prompt and covers the affordance and its reveal. Adding a second bullet here would describe one logical change twice.

6. Self-check before finishing: re-run `<verification>` and confirm every line passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. The only test file this prompt touches is `pkg/handler/attention-page_test.go`, and only by adding one `It`.
- ⚠️ **The control's listener must be delegated on the document, not bound per button.** The stream replaces a row's whole `outerHTML` on every event, so a per-button binding leaves every re-rendered card's control rendered and inert — the defect this board shipped at v0.19.0 for the read-aloud control and fixed for the jump control.
- ⚠️ **The control's meaning must not live in a hover-only `title` or an `aria-label` alone.** The visible `i` glyph is the meaning; the `aria-label` is the accessible name. Do not replace the glyph with an SVG or move the label into a `title`.
- **The control is a real button**, so keyboard activation (Enter / Space) is inherited rather than re-implemented. Do not add a keydown handler.
- **Not the `Jumped.` note.** It is transient client-side state appended into the `.jump` container by `showJumpNote`, not a card metadata row; it is out of scope.
- **Not a board-wide filter, collapse-all, or persisted open/closed state.** The affordance is per card and per page load; two cards on one page may be open at once and neither closes the other.
- **No new network surface.** The panel is inert markup plus a client-side toggle; it issues no request.
- **The board's existing behaviour must not regress:** the corner X's `(not .Dimmed)` gate, the read-aloud toggle's `and .Speak (not .Dimmed)` gate, the jump control's two arms, the `Hide answered` switch, the stream-stale notice, the failure-note replay, and the dimmed record card's rendering.
- **`pkg/handler/attention-page.go` is against revive's 2,000-line `file-length-limit`.** The template and its script deliberately stay in that file; if this change pushes it over the limit, report `status: failed` rather than moving the script.
- Test types follow the repo's guide: Ginkgo only, no stdlib table tests, no direct `testing.T`, a real in-memory libkv DB rather than a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

- `grep -c 'data-info-toggle' pkg/handler/attention-page.go` — must print a value of at least 2 (the button's own attribute and the delegated selector).
- `grep -c 'data-info-panel' pkg/handler/attention-page.go` — must print a value of at least 2 (the panel's own attribute and the lookup selector).
- `grep -c 'aria-expanded' pkg/handler/attention-page.go` — must print a value of at least 3 (the button's server-rendered attribute and the two assignments in the listener).
- `grep -c "console.error('attention board: " pkg/handler/attention-page.go` — must print `9`.
- `grep -c "var prepared = preparedHTML(html, itemID)" pkg/handler/attention-page.go` — must print `1`.
- `grep -c 'button\[data-info-toggle\]' pkg/handler/attention-page.go` — must print `1`.
- `grep -c 'ships the card info affordance and its delegated listener' pkg/handler/attention-page_test.go` — must print `1`.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
