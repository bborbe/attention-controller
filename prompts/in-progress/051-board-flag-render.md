---
status: approved
spec: [014-flag-misrouted-cards]
created: "2026-10-28T13:53:31Z"
queued: "2026-10-28T14:15:11Z"
branch: dark-factory/flag-misrouted-cards
---

# Render the flag control and the two-filter board view

<summary>
- Every open card carries its own flag form with a reason input and a submit control.
- The flag form posts to `/flag` through its own delegated listener and never to `/answer`.
- The flag form carries no `action` and no `method`, so it cannot navigate the board away.
- The flag filter is a second switch, "Hide flagged", whose checked state mirrors the URL.
- A flagged row is served marked, so the client filter can park it without a second request.
- The two filters compose per row: turning one off never restores rows the other still hides.
- `?hide=none` reveals both kinds of hidden row, and the flag switch writes the narrower URL back.
- Rows arriving over the live stream carry the same mark and obey the same predicate.
- A flag reason is escaped wherever it renders, on both views.
- The browser scenario gains three cases and its spec count is updated.
- `make precommit` passes.
</summary>

<objective>
Put the flag on the board: a per-card form wired to the flag endpoint, a `data-flagged` mark on the row, and a second client-side filter composed with the existing answered filter exactly as `docs/attention-item-flagging.md` § Two filters defines. Then add the browser cases that prove the park/restore path, which only a real browser can execute.
</objective>

<context>
**Dependency: prompts 1 and 2 must already be complete.** This prompt assumes `Item.FlagReason`/`Item.FlaggedAt` exist, and that `POST /api/1.0/attention/{itemID}/flag` and `/unflag` are routed and working. If a prerequisite is absent, STOP and report `status: failed` with the message naming the missing prompt (1 or 2) — do not implement the endpoint or the fields here.

There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md`. Read `docs/attention-item-flagging.md` § Two filters in full: this prompt implements it verbatim and must not restate or reinterpret it.

Read `pkg/handler/attention-page.go` in chunks (it is ~1,800 lines):
- the template head and `.board-controls` row (around the `Hide answered` switch);
- the top-level submit listener for `form.answer` (the `document.addEventListener('submit', ...)` that resolves `event.target.closest('form.answer')`);
- `showNote` / `rememberFailure` / `failures` (the note helpers);
- the `attention-row` sub-template (the `<li ... data-item-id=... data-risk=...>` opening tag, the `{{if .Dimmed}}...{{else if .Message}}<form class="answer">...</form>{{end}}` block, the `.actions` rows, and the info panel);
- the `(function () { ... })()` block that owns the view filter (`button`, `stale`, `HIDE_PARAM`, `on`, `parked`, `findRow`, `ensureList`, `nextRowID`, `park`, `unpark`, `applyFilter`, `forget`, the switch click listener, and the load-time `if (on) { applyFilter(); }`);
- `upsertRow` (including its parked-entry branch) and `preparedHTML`;
- `attentionPageRow`, `newAttentionPageRow`, `attentionPageData`, the `boardHideParam` consts, and `NewAttentionPageHandler` (the per-request `hideAnswered := ...` line and the `page.Execute` call).

Read `pkg/handler/attention-page-helpers.go` — `newAttentionPageRow`, and `attentionPageStyles` (append styles at its end; do not reorder existing rules — `attention-page-helpers_test.go` asserts the relative order of `.actions .next` and `.actions button[data-decision]`).

Read `pkg/handler/attention-page-template.go` — `newAttentionPageTemplate`, and note the template body is a Go raw-string literal: **no backticks may appear anywhere in the template**.

Read `pkg/handler/attention-board-page_test.go` — the `filterSwitchOn`/`filterSwitchOff` constants (assert the FULL `data-board-filter role="switch" aria-checked="..."` sequence, never a bare `aria-checked`, because the stylesheet carries matching selectors), `renderAt`, `dimmedRow`, `rowBlock`, and the `Describe("the answerable filter", ...)` block where the new flag-filter specs belong. Read `pkg/handler/attention-page_test.go` — `rowOf`, the `messageRequest` closure and the escaping spec (`&lt;script&gt;` positive, `<script>alert(1)` negative). Read `pkg/handler/attention-stream_test.go` — how a rendered row event payload is captured.

Read `e2e/board_test.go` — `push`, `pushQuestions`, `answer`, `post`, `rowCount`, `openItemIDs`, `newPage`, `rowSelector`, `speakPattern`, and the `It(...)` case style. Read `scenarios/001-board-browser-cases.md` in full (49 lines).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
</context>

<requirements>
### A. Row mark and row field

1. In `pkg/handler/attention-page-helpers.go` → `newAttentionPageRow`, add:

   ```go
   // Flagged reports whether this row is parked by the flag filter. It is
   // derived from the stored flag alone — the reason's presence — because the
   // pair is written together and cleared together, so the timestamp cannot
   // disagree with it.
   row.Flagged = item.FlagReason != ""
   ```

   Place it beside the `row.Message, row.Ack = affordance(...)` assignment. Add the corresponding field to `attentionPageRow` in `pkg/handler/attention-page.go`:

   ```go
   // Flagged reports whether this row carries a flag and is therefore parked by
   // the flag filter's default position. The template reads it for the row's
   // data-flagged mark and for the flag-reason line; the page's script reads the
   // rendered mark, never this field.
   Flagged bool
   ```

2. In the `attention-row` template, add the mark to the opening `<li>` tag, after `data-risk`:

   ```
   data-flagged="{{if .Flagged}}true{{else}}false{{end}}"
   ```

   Render it on every row, `true` or `false`, so the client predicate reads one attribute with one spelling and a spec can assert the unflagged negative.

### B. The flag form

3. In the `attention-row` template, add the flag form as a **direct child of `<li class="item">`**, immediately after the `{{if .Dimmed}}...{{else if .Message}}<form class="answer">...</form>{{end}}` block and before the undeliverable marker:

   ```
   {{if not .Dimmed}}<form class="flag" data-flag-control><input class="flag-reason-input" type="text" name="reason" placeholder="Why should this not have reached you?" aria-label="Reason this card was misrouted"><button type="submit" class="flag-submit">⚑ Flag as misrouted</button></form>
   {{end}}{{if .Flagged}}<div class="flag-reason-shown">flagged: {{ cardtext .Item.FlagReason }}</div>
   {{end}}
   ```

   Load-bearing constraints on this markup:
   - **Never nest it inside `form.answer`.** Nested forms are invalid HTML and the browser drops the inner `<form>` element, which would leave the flag input and submit button owned by the answer form — the exact failure AC3 forbids.
   - **No `action` and no `method`.** The page submits through delegated listeners; a native-submitting form would navigate the operator off the board into a JSON body.
   - The form's class is `flag`, never `answer`. `row.querySelector('form.answer')` must keep resolving the answer form only.
   - The `data-flag-control` attribute is the selector the listener matches. It must not be a prefix of, or prefixed by, `data-flag-filter` or `data-flagged`.
   - The form renders on every non-dimmed row — `message`, `permission` and `ack` alike. ⚠️ It is withheld from the dimmed record card because a flag is a field write on an `open` item and the endpoint answers 409 for an answered one; a control that always fails is the false affordance this board has removed before. <!-- Reviewer note: AC3 says "each rendered card"; this prompt reads "card" as "open card", matching every other control's `(not .Dimmed)` gate. -->
   - No backticks anywhere (the template is a raw string literal).

### C. The second switch

4. In the `.board-controls` div, add a second switch after the `Hide answered` button and before the `[data-stream-stale]` span, mirroring the first switch's markup:

   ```
   <button type="button" class="board-filter flag-filter" data-flag-filter role="switch" aria-checked="{{if .FlagFilter}}true{{else}}false{{end}}"><svg class="switch" viewBox="0 0 44 24" aria-hidden="true" focusable="false"><rect class="switch-track" x="1" y="1" width="42" height="22" rx="11"/><circle class="switch-knob" cx="12" cy="12" r="8"/></svg><span class="switch-label">Hide flagged</span></button>
   ```

5. In `attentionPageData`, add:

   ```go
   // FlagFilter is the flag filter's initial position, read from the request's
   // `flags` parameter and composed with `hide` exactly as § Two filters
   // defines: the flag filter can only be on when the answered filter is on,
   // because `hide=none` is the union view. It is a rendering input, never a
   // store input — the page still renders every row it was given.
   FlagFilter bool
   ```

6. Extend the existing `boardHideParam` const block at `pkg/handler/attention-page.go:1640` — do **not** add a second `const` block, which would redeclare `boardHideParam`. The three existing constants are shown only to fix the placement:

   ```go
   const (
       boardHideParam    = "hide"
       boardHideAnswered = "answered"
       boardHideNone     = "none"

       // boardFlagsParam carries the flag filter's view state, on the same
       // convention as boardHideParam: the value names the SET that is hidden,
       // so `none` is the only value that turns it off. boardFlagsFlagged is the
       // default spelling the script writes; it is declared here so the Go and
       // script copies are greppable together.
       boardFlagsParam   = "flags"
       boardFlagsFlagged = "flagged"
       boardFlagsNone    = "none"
   )
   ```

7. In `NewAttentionPageHandler`, read the second parameter and pass it:

   ```go
   flagsParam := req.URL.Query().Get(boardFlagsParam)
   hideAnswered := req.URL.Query().Get(boardHideParam) != boardHideNone
   flagFilter := hideAnswered && flagsParam != boardFlagsNone
   ```

   and set `FlagFilter: flagFilter` in the `attentionPageData` literal. The three rendered states must be: bare URL → `HideAnswered` true, `FlagFilter` true; `?flags=none` → `HideAnswered` true, `FlagFilter` false; `?hide=none` → both false.

### D. The client predicate and the two switches

8. Replace the filter portion of the IIFE in `pkg/handler/attention-page.go` (from `var button = document.querySelector('button[data-board-filter]');` through the load-time `if (on) { applyFilter(); }`) with a two-filter implementation. Keep the existing `findRow`, `ensureList` and `nextRowID` helpers unchanged, keep the "a filtered row is REMOVED, not hidden" rationale comment (amended to name both filters), and use exactly this shape:

   ```js
   var button = document.querySelector('button[data-board-filter]');
   var flagButton = document.querySelector('button[data-flag-filter]');
   var stale = document.querySelector('[data-stream-stale]');
   /* Mirrors boardHideParam / boardFlagsParam and their values in Go — the one
      thing in this file that exists in two languages. */
   var HIDE_PARAM = 'hide';
   var HIDE_ANSWERED = 'answered';
   var HIDE_NONE = 'none';
   var FLAGS_PARAM = 'flags';
   var FLAGS_FLAGGED = 'flagged';
   var FLAGS_NONE = 'none';
   var params = new URLSearchParams(window.location.search);
   var hideOn = params.get(HIDE_PARAM) !== HIDE_NONE;
   var flagsOn = hideOn && params.get(FLAGS_PARAM) !== FLAGS_NONE;
   var parked = [];

   function findRow(itemID) { /* unchanged */ }
   function ensureList() { /* unchanged */ }
   function nextRowID(row) { /* unchanged */ }

   function isAnswered(row) { return row.classList.contains('dimmed'); }
   function isFlagged(row) { return row.getAttribute('data-flagged') === 'true'; }
   /* The contract's predicate, verbatim:
        visible(row) = (hideOff || !row.answered) && (flagsOff || !row.flagged)
        hideOff      = (hide == "none")
        flagsOff     = (flags == "none") || hideOff */
   function visible(answered, flagged) {
     var hideOff = !hideOn;
     var flagsOff = !flagsOn || hideOff;
     return (hideOff || !answered) && (flagsOff || !flagged);
   }
   function park(row) {
     var id = row.getAttribute('data-item-id');
     parked = parked.filter(function (entry) { return entry.id !== id; });
     parked.push({
       id: id,
       html: row.outerHTML,
       next: nextRowID(row),
       answered: isAnswered(row),
       flagged: isFlagged(row)
     });
     row.remove();
   }
   function unparkEntry(entry) {
     var list = ensureList();
     if (!list) { return; }
     var anchor = entry.next ? findRow(entry.next) : null;
     if (anchor) { anchor.insertAdjacentHTML('beforebegin', entry.html); return; }
     list.insertAdjacentHTML('beforeend', entry.html);
   }
   /* Re-evaluated on EVERY change and PER ROW. Restoring is never a global
      unpark: a row hidden by one filter stays hidden while the other is still
      on, which is the flood flags=none exists to prevent. */
   function applyFilter() {
     var keep = [];
     for (var i = parked.length - 1; i >= 0; i--) {
       var entry = parked[i];
       if (visible(entry.answered, entry.flagged)) { unparkEntry(entry); } else { keep.unshift(entry); }
     }
     parked = keep;
     var rows = document.querySelectorAll('li.item');
     for (var j = 0; j < rows.length; j++) {
       if (!visible(isAnswered(rows[j]), isFlagged(rows[j]))) { park(rows[j]); }
     }
     showEmptyStatement();
   }
   function forget(itemID) { /* unchanged */ }
   ```

   Keep `collapseIfEmpty` and `showEmptyStatement` unchanged.

9. Replace the single switch's click listener with two listeners that each mutate the URL and then re-derive their state from it:

   ```js
   function syncFromURL(url) {
     hideOn = url.searchParams.get(HIDE_PARAM) !== HIDE_NONE;
     flagsOn = hideOn && url.searchParams.get(FLAGS_PARAM) !== FLAGS_NONE;
   }
   function renderSwitchState() {
     if (button) { button.setAttribute('aria-checked', hideOn ? 'true' : 'false'); }
     if (flagButton) { flagButton.setAttribute('aria-checked', flagsOn ? 'true' : 'false'); }
   }
   if (button) {
     button.addEventListener('click', function () {
       var url = new URL(window.location.href);
       if (url.searchParams.get(HIDE_PARAM) === HIDE_NONE) {
         /* Turning the answered filter back on: absence is the default spelling. */
         url.searchParams.delete(HIDE_PARAM);
       } else {
         /* hide=none is the union view — it disables both filters — so the flag
            parameter has no meaning there and is dropped rather than left
            describing a set the URL does not hide. */
         url.searchParams.set(HIDE_PARAM, HIDE_NONE);
         url.searchParams.delete(FLAGS_PARAM);
       }
       window.history.replaceState(null, '', url);
       syncFromURL(url);
       renderSwitchState();
       applyFilter();
     });
   }
   if (flagButton) {
     flagButton.addEventListener('click', function () {
       var url = new URL(window.location.href);
       var hideOff = url.searchParams.get(HIDE_PARAM) === HIDE_NONE;
       var flagsOff = url.searchParams.get(FLAGS_PARAM) === FLAGS_NONE || hideOff;
       if (flagsOff) {
         /* Turning the flag filter ON. The union view and the flag filter cannot
            both be active, so hide=none re-enables the answered filter. The
            switch is never rendered disabled — a disabled control with no
            explanation reads as broken. */
         url.searchParams.set(HIDE_PARAM, HIDE_ANSWERED);
         url.searchParams.set(FLAGS_PARAM, FLAGS_FLAGGED);
       } else {
         url.searchParams.set(FLAGS_PARAM, FLAGS_NONE);
       }
       window.history.replaceState(null, '', url);
       syncFromURL(url);
       renderSwitchState();
       applyFilter();
     });
   }
   ```

   ⚠️ These URL outcomes are required and are what prompt 3's specs assert: bare default → no `hide` and no `flags`; answered filter off → `hide=none` and no `flags`; flag filter off with answered on → `flags=none`; flag filter turned on at `hide=none` → `hide=answered` **and** `flags=flagged`. Use `replaceState`, never `pushState`, on the existing rationale.

10. Replace the load-time line with an unconditional `applyFilter();`, so a page whose URL starts filtered parks its rows before the first paint and a page that starts unfiltered still establishes the predicate for rows that arrive later.

11. In `upsertRow`'s parked branch, update the entry's `answered`/`flagged` flags from the prepared markup and re-run the filter, so a parked card that is unflagged while the page is open returns to the board:

    ```js
    parked[hidden].html = prepared;
    var detached = document.createElement('div');
    detached.innerHTML = prepared;
    var node = detached.firstElementChild;
    if (node) {
      parked[hidden].answered = isAnswered(node);
      parked[hidden].flagged = isFlagged(node);
    }
    applyFilter();
    return;
    ```

    The non-parked branch keeps its existing `applyFilter()` call.

### E. The flag submit listener

12. Add a **second, top-level** delegated submit listener beside the existing `form.answer` listener (NOT inside the filter IIFE — the IIFE returns early when `EventSource` is undefined, and the flag form is plain DOM work):

    ```js
    /* The flag control is its OWN form with its OWN listener. ⚠️ Load bearing:
       the answer listener above resolves form.answer and would preventDefault,
       find no answer entries, and dispatch a `send` answer — answering the card
       instead of flagging it. A flag form is not an answer form, and this
       listener is the only one that handles it. */
    document.addEventListener('submit', function (event) {
      var form = event.target && event.target.closest ? event.target.closest('form[data-flag-control]') : null;
      if (!form) { return; }
      event.preventDefault();
      var row = form.closest('li.item');
      if (!row) { return; }
      var field = form.querySelector('input[name=reason]');
      var reason = field ? field.value.trim() : '';
      if (!reason) {
        showNote(form, 'Say why this card should not have reached you.', true);
        return;
      }
      var itemID = row.getAttribute('data-item-id');
      fetch('/api/1.0/attention/' + encodeURIComponent(itemID) + '/flag', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reason: reason })
      }).then(function (response) {
        if (!response.ok) { throw new Error('flag failed: ' + response.status); }
        return response.json();
      }).then(function () {
        if (field) { field.value = ''; }
      }).catch(function (err) {
        showNote(form, 'Could not flag this card: ' + err.message, true);
      });
    });
    ```

    It must make **no** request to `/answer`. The row's new state arrives over the SSE stream, exactly as an answer's does.

### F. Styles

13. Append to `attentionPageStyles` in `pkg/handler/attention-page-helpers.go` (at the end; do not reorder existing rules):

    ```css
    .flag { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin: 8px 0 0; }
    .flag input { flex: 1 1 220px; }
    .flag-reason-shown { margin: 8px 0 0; color: var(--muted); }
    ```

    Match the file's existing property ordering and use only the variables it already defines. Do not restyle `.actions` or `form.answer`.

### G. Handler and render specs

14. In `pkg/handler/attention-page_test.go` (or a new `attention-flag-page_test.go` in `package handler_test` — prefer a new file to keep the existing file's length bounded), add specs for the control's markup. Scope each assertion to one row with `rowOf`:

    a. `renders a flag form on an open card, separate from the answer form` — the row block contains `<form class="flag" data-flag-control>`, `name="reason"`, and a submit control; the row block also contains `<form class="answer"` for a `message` item.
    b. `renders the flag form on a permission card and an ack card` — one case each, asserting the form is present and no `form.answer` is.
    c. `withholds the flag form from the dimmed record card` — push, answer, render; the dimmed row block contains no `data-flag-control`.
    d. `carries no action and no method on the flag form` — extract the substring from `<form class="flag"` to the matching `</form>` and assert it contains neither `action=` nor `method=`.
    e. `serves the second delegated listener and its selector` — the page body contains `form[data-flag-control]` and `var FLAGS_PARAM = 'flags';`. ⚠️ `data-flag-control` also appears in the row markup, so assert the selector literal `form[data-flag-control]` (with the `form` prefix), which only the script carries.
    f. `marks a flagged row and not an unflagged one` — flag one item, leave another; the flagged row block contains `data-flagged="true"`, the unflagged contains `data-flagged="false"`.

15. In `pkg/handler/attention-board-page_test.go`, add a `Describe("the flag filter", ...)` inside the existing filter block, using `renderAt`:

    a. `renders the flag switch on by default and off at flags=none` — assert the full sequences: bare `/` → `data-flag-filter role="switch" aria-checked="true"`; `/?flags=none` → `data-flag-filter role="switch" aria-checked="false"`; and in the same request the hide switch is still on (`filterSwitchOn`).
    b. `renders both switches off at hide=none` — `/?hide=none` → `filterSwitchOff` and `data-flag-filter role="switch" aria-checked="false"`.
    c. `still serves the flagged row under every view` — a flagged item's row is present in the body at `/`, `/?flags=none` and `/?hide=none`; the filter is client-side, so body absence is never the evidence.
    d. `escapes the flag reason on both views` — flag an item with reason `<script>alert(1)</script>`; for both `/?flags=none` and `/?hide=none`, the body contains `&lt;script&gt;alert(1)` and does **not** contain `<script>alert(1)` (AC7, both views).
    e. `pins the script's copy of the flag parameter` — the body contains `var FLAGS_PARAM = 'flags';`, `var FLAGS_FLAGGED = 'flagged';`, `var FLAGS_NONE = 'none';`, mirroring the existing `HIDE_*` spec.
    f. `pins the per-row predicate` — the body contains `!flagsOn || hideOff` (the `flagsOff` composition) and `hideOff || !answered` (the per-row half), so a later rewrite that restores globally or drops the coupling fails here.

16. In `pkg/handler/attention-stream_test.go`, add a spec `carries the flag mark in a streamed row` — flag an item, capture the rendered event payload for its row, and assert it contains `data-flagged="true"`. Use the file's existing capture helper; do not start a second renderer.

### H. Browser cases and the scenario count

17. Add exactly **three** `It(...)` cases to `e2e/board_test.go`, beside the existing board cases:

    a. `flags a misrouted card without answering it, so the item stays open` —
       push a message card, `newPage("/")`, fill `form[data-flag-control] input[name=reason]` with `"asked the wrong team"`, click the form's submit button. Register a route/counter for `**/api/1.0/attention/*/answer` (mirroring how `speakPattern` is used) and assert it received **zero** requests. Then `Eventually` read the item through `GET api/1.0/attention/{itemID}` (add a small `getItem(itemID)` helper decoding into `pkg.Item`) and assert `state == "open"`, `flag_reason == "asked the wrong team"`, and `answered_at` is absent. Also assert the row leaves the default view (`rowCount(page, id)` reaches 0).

    b. `restores only the flagged card when the flag filter is turned off, leaving answered rows parked` —
       seed one open card and one answered card, load `/` (the open one visible, the answered one parked), then flag **both** through the API (a `post("api/1.0/attention/"+id+"/flag", map[string]any{"reason": "misrouted"})` helper, so the case isolates the click). The answered card is now both flagged and answered — AC4's both-marks row. Click `[data-flag-filter]`; assert the open flagged card is present (`rowCount` 1) and the answered-and-flagged card is still absent (`rowCount` 0), which is `visible(false, true) === true` beside `visible(true, true) === false`. This is the per-row restore: a global unpark would show the answered card too.

       Third phase in the same case: click `[data-flag-filter]` again so the flag filter is back on (`flags=flagged`), then `post("api/1.0/attention/"+id+"/unflag", map[string]any{})`; `Eventually` the open card returns (`rowCount` 1), proving the parked entry's own `flagged` flag is re-derived and the filter re-run rather than the row staying parked at its stale mark (AC6's render half, and the only test of requirement 11's parked branch).

    c. `writes hide=answered when the flag switch is turned on at hide=none` —
       seed one open card and one answered card, `newPage("/?hide=none")` (both visible), click `[data-flag-filter]`; assert the page URL contains `hide=answered` and `flags=flagged`, the answered card is parked (`rowCount` 0) and the **open** card is still visible (`rowCount` 1), and the switch is not disabled (`Locator("[data-flag-filter]").IsDisabled()` is false).

    Use `Eventually`/`Consistently` rather than sleeps, as the existing cases do. Keep every case's own preconditions explicit — the suite shares one store across cases.

18. Update `scenarios/001-board-browser-cases.md`:
    - add three bullets to the **Expected** list naming the three cases above, in the file's existing voice (one line each, stating the discriminating half);
    - change the `36 of 36 Specs` total to `39 of 39 Specs` (the case-list bullet);
    - re-read the intro prose ("Validates that the attention board's twenty post-load JS behaviours still work") and increment that numeric count by the three cases if it is a literal count of behaviours; leave it alone if it is not. State in the bullet which count you changed.
    - After the edit, `grep -c '36 of 36' scenarios/001-board-browser-cases.md` must return 0.

    ⚠️ The e2e suite is operator-only: this container cannot run the browser. The count is an arithmetic statement about the three cases you added, and the operator's `go test -mod=mod -tags e2e -count=1 -v ./e2e/` run is what proves it.
</requirements>

<constraints>
- Dependency ordering: prompts 1 and 2 must already have shipped. Absent prerequisites are a `status: failed` report naming the missing prompt, never `needs_input`.
- The filter is client-side, like the existing one. Do not add server-side row exclusion for flags, and do not stop serving a flagged row.
- The `data-` attribute names must not prefix each other: `data-flag-control` (the form), `data-flag-filter` (the switch), `data-flagged` (the row mark).
- `?hide=none` widens deliberately: it disables both filters. Do not "fix" that.
- The flag switch is never rendered `disabled`.
- The existing answer path must not change: `hidden`, `skip`, `dismiss`, the corner X and `form.answer` behaviour are untouched. The flag form is a new axis beside them.
- The flag form must never be captured by the `form.answer` listener, and must make no `/answer` request.
- No backticks anywhere in the Go raw-string template or in the stylesheet.
- Escaping goes through the existing `cardtext` func, never a new escaping path.
- Tests assert rendered HTML with `ContainSubstring` and `data-item-id` markers, the existing board-spec convention. No new test framework.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Do not touch `pkg/attention-item.go`, the store, the handlers or the routes — prompts 1 and 2 own them. If a spec needs a store change, stop and report.
- Do not touch `CHANGELOG.md`; prompt 4 owns it.
- End with the completion report dark-factory appends, followed by a `## Improvements` section (categorise each suggestion PROMPT / GUIDE / GLOBAL).
</constraints>

<verification>
Run in the repo root:

- `make test` — the suite passes, including the new page, board-filter and stream specs.
- `grep -n 'data-flagged' pkg/handler/attention-page.go` — at least one line.
- `grep -n 'data-flag-control' pkg/handler/attention-page.go` — at least two lines (the form and the script's selector).
- `grep -n 'data-flag-filter' pkg/handler/attention-page.go` — at least two lines (the switch and the script's lookup).
- `grep -n 'FlagFilter' pkg/handler/attention-page.go` — at least two lines (the field and the handler's assignment).
- `grep -n 'boardFlagsParam' pkg/handler/attention-page.go` — at least one line.
- `grep -c '36 of 36' scenarios/001-board-browser-cases.md` — prints `0`.
- `grep -n '39 of 39 Specs' scenarios/001-board-browser-cases.md` — at least one line.
- `go vet -mod=mod -tags e2e ./e2e/` — type-checks the three new browser cases without launching a browser.
- `grep -c 'FlagFilter\|data-flag-control' pkg/attention-store.go pkg/attention-store-impl.go` — prints `0` for each file (this prompt is render-only; no store change leaked in).
  ⚠️ The authoritative `git diff origin/master -- pkg/attention-item.go pkg/attention-store.go pkg/attention-store-impl.go` empty-diff check is OPERATOR-SIDE: this container's `.git` is masked, so a git command here fails rather than proving an empty diff. Run it on the host.
- `make precommit` — must exit 0.

Operator-side verification is NOT part of this prompt and is not repeated here: the browser suite (`go test -mod=mod -tags e2e -count=1 -v ./e2e/`) and the `make install` round-trip live in the spec's § Verification → Operator-executable ladder. This container cannot run a browser or reload a launchd job, so neither is a gate on this prompt.
</verification>
