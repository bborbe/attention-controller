---
status: completed
summary: Added a nine-line console trail to the attention board's inline script (five fetch-failure branches plus four catches), top-level window.onerror and unhandledrejection handlers, and a try/catch guard around the live stream's JSON.parse, with a spec asserting each marker in the served source.
execution_id: attention-controller-error-trace-exec-002-board-error-console-trace
dark-factory-version: dev
created: "2026-09-27T19:24:09Z"
queued: "2026-09-27T19:24:09Z"
started: "2026-09-27T19:37:19Z"
completed: "2026-09-27T19:43:02Z"
---

# Log every attention-board failure to the browser console

<summary>
- A failed action on the attention board writes a line to the browser's console
- The line names which action failed and what the server answered
- A JavaScript error that nothing currently catches also reaches the console
- A malformed message from the live-update stream is logged and skipped instead of thrown
- Nothing the operator sees changes: the note rendered beside the control stays exactly as it is
- No new dependency, no build step, and no change to the served markup or styles
</summary>

<objective>
Give the attention board a console trail so a failed action is debuggable from devtools. The board renders each failure as a transient inline note beside the control and writes nothing to the browser console, so a card that misbehaves leaves no evidence once the note scrolls out of view or the row is replaced by the stream. This adds the missing half of the error surface — one console line per failure, plus handlers for the throws nothing catches today — and changes nothing the operator sees.
</objective>

<context>
Read these before writing anything — the change is confined to one inline script and must read as though the same author wrote it:

- `pkg/handler/attention-page.go` — the whole board: the HTML template, the inline `<style>` block and the inline `<script>`. All board JavaScript lives here. There is no `.js` asset anywhere in the repo and this change adds none.
- `pkg/handler/attention-page_test.go` — the file that asserts the inline script's own source, and the home for the new cases (see requirement 6).
- `pkg/handler/attention-board-page_test.go` — asserts rendered row markup, not the script.
- `pkg/handler/healthz_test.go` — the canonical handler-test shape: `package handler_test`, `httptest`, `Expect(...).To(...)`.

The existing error surface this change extends and must not alter:

- **Four `fetch` call sites, each already handled** — the answer form's submit listener (`sendAnswer`), the read-aloud control (`button[data-speak]`), the jump control (`button[data-jump]`), and the close control (the corner X and the acknowledge button). Each checks `response.ok` and reads the body with `response.text()`; each also carries a `.catch` rendering `"<Action> failed - <error>"`.
- **The failure render differs per site, and the difference is load-bearing for the count in requirement 1.** The answer path renders a failure note on **two** branches — the `failure.leftQueue` branch renders `failure.message`, and the fallthrough renders `"Answer failed - HTTP <status> - <body>"`. The read-aloud control renders its failure through a **ternary** inside a single `showNote(...)` call rather than a branch. Jump and close each render `"<Action> failed - HTTP <status> - <body>"` on one branch.
- **Three note helpers** — `showNote(form, message, isError)`, `showJumpNote(row, message, isError)` and `showCloseNote(row, message, isError)` — each appending a `<div>`/`<span>` carrying class `note failed` beside its control. These are the operator's surface and stay byte-identical in behaviour: same text, same placement, same class, same success path.
- **`answerFailure(...)`** — reads the store's `{error: {code, message, details}}` envelope so an item that left the queue is reported as such rather than as a validation error. Leave its logic alone.
- **The close control's verb is read off the row** — `Acknowledge` or `Clear`, whichever the row carries. Requirement 1 explains why the console line does not follow that.

Conventions that bind this change, inlined deliberately — this worktree has no `CLAUDE.md` (the repo root's `CLAUDE.md` is gitignored via the `.gitignore` entry `/CLAUDE.md`, so it is absent from every branch and the container cannot read it):

- The page carries no framework, no bundler and no build step. Plain DOM work inside a Go string constant is the house style; do not introduce a library.
- The inline script is written in ES5 style (`var`, `function`). Match it. It also lives inside a backtick-delimited Go raw string, so no backtick may appear anywhere in the script — not in a template literal, not in a comment. The file's own comments already carry this warning.
- The served page is asserted by substring in tests, which is the boundary this change crosses.
- The container cannot load the page in a browser. Browser-side verification is operator-side and out of scope here; what the container verifies is that the markers are present in the served source and that the suite passes.
</context>

<requirements>
1. **Add a console line to every fetch-failure branch that renders a failure note.** In `pkg/handler/attention-page.go`, at each of the four `fetch` call sites, add a `console.error(...)` on every branch that currently renders a fetch-failure note through `showNote`, `showJumpNote` or `showCloseNote`. The answer form's own local-validation note — `showNote(form, 'Pick an option or write an answer first.', true)` — is not a fetch failure and gets no console line; it is the one failure note in this file that sits outside the four call sites. Each line names the action and the HTTP status, in this exact form so a test can assert each one:

   ```js
   console.error('attention board: <action> failed - HTTP ' + response.status, body);
   ```

   `<action>` is one of four **fixed** control names — `answer`, `read aloud`, `jump`, `close`. Use those fixed words rather than the note's own verb: the close control's note verb is read off the row (`Acknowledge` or `Clear`), and a stable action word is what makes a console line greppable and assertable. The note keeps whatever verb it renders today — the two surfaces are allowed to differ, and the note is the one that must not change.

   **Count the branches, not the call sites — there are five, not four:**
   - the answer path renders a failure note on **two** branches: the `failure.leftQueue` branch (`showNote(form, failure.message, true)`) and the raw-body fallthrough (`showNote(form, 'Answer failed - HTTP ' + ...)`),
   - the read-aloud control renders its failure through a **ternary** inside a single `showNote(...)` call — turn that ternary into an `if`/`else` so there is a branch to log. The note's rendered text and its `isError` value must come out identical to today's.
   - jump and close each render on one branch.

   Add a second `console.error(...)` inside each of the four `.catch` handlers, so a network-level rejection is logged too, in the matching form so all nine lines share one greppable prefix:

   ```js
   console.error('attention board: <action> failed - ' + String(error));
   ```

   That is **nine `console.error` calls** in total: five failure branches plus four catches. The `attention board: ` prefix is **reserved** for these nine lines — the global handlers in requirement 2 and the parse guard in requirement 3 log without it, so the count in requirement 6 stays unambiguous. Do not write that literal anywhere else in the file, not even in a comment: requirement 6 counts it in the served body and `<verification>` counts it in the source, so a comment carrying it fails both on otherwise-correct code.

2. **Log what nothing currently catches.** Register a `window.onerror` handler and a `window.addEventListener('unhandledrejection', ...)` handler that write to the console, so a throw outside the four handled paths is no longer silent. Both are registered once at the top level of the inline script — not inside a handler that may never run.

   `window.onerror` receives `(message, source, lineno, colno, error)`; prefer `error` when present and fall back to `message`. An `unhandledrejection` event carries its reason on `event.reason`.

   Every promise chain in this script already carries its own `.catch`, so the `unhandledrejection` handler guards code added later rather than a path that can escape today. Register it for parity with `window.onerror`, not because a rejection currently escapes.

   Both handlers write to the console **only**. Do not surface them on the page: an uncaught error has no form and no row to host a note, so a page surface here would be a new visible element on a path that renders none today — which this prompt's objective forbids.

3. **Guard the stream's parse.** In the live-update handler — `source.onmessage = function (event) { var change = JSON.parse(event.data); ... }` — wrap the `JSON.parse` in a `try`/`catch`. On a parse failure log it with `console.error` and `return` from the handler, so the rest of that handler does not run on a frame it cannot read. Today an unparseable frame throws uncaught and aborts that event's row swap silently. Leave the stream's own reconnect behaviour untouched — there is deliberately no `onerror` handler today and this prompt does not add one.

4. **Do not change what the operator sees.** The note helpers' text, placement and classes stay exactly as they are; the success paths stay exactly as they are; the served markup, the `<style>` block and the template's row rendering are unchanged. A restyle, a colour change or a new popup is a defect here.

5. **Do not add a dependency, a build step, or a `.js` asset.** `console` and `JSON` are language built-ins; nothing else is needed.

6. **Add test cases to `pkg/handler/attention-page_test.go`** — the file that already asserts the inline script's own source, as its `ContainSubstring("failure.code !== 'ITEM_CLOSED'")` and `ContainSubstring("showNote(form, failure.message, true)")` cases show. Follow that file's existing substring-assertion shape. At minimum assert:
   - the served body contains `console.error` at least nine times — count with `strings.Count`, since Gomega has no count matcher. This is a floor, not an equality: the global handlers from requirement 2 and the parse guard from requirement 3 also log, so the true total is higher and an equality here would fail on a correct implementation,
   - the served body contains `window.onerror`,
   - the served body contains `unhandledrejection`,
   - the served body contains each of the four distinct console prefixes — `attention board: answer failed - HTTP `, `attention board: read aloud failed - HTTP `, `attention board: jump failed - HTTP `, `attention board: close failed - HTTP ` — so a line that exists but sits on the wrong action is caught,
   - `strings.Count(body, "attention board: ")` is exactly `9` — the nine action lines from requirement 1, and only those. That prefix is **reserved** for them: the global handlers and the parse guard must not use it, which is what makes this count unambiguous where a bare `console.error` count is not,
   - `strings.Count(body, "attention board: answer failed - HTTP ")` is exactly `2` — the answer path renders **two** failure branches and each logs, so a branch that was logged and a branch that was forgotten are distinguishable rather than both satisfying a bare count. The trailing `HTTP ` matters: it excludes the catch line, which carries no status.

   These assert presence in the served source, which is the boundary this change crosses. Do not describe them in a comment as verifying browser behaviour — they do not. The operator's own click-through covers that, and it happens outside this container.

7. **Self-check before finishing.** Re-run the `<verification>` command and confirm it passes. Then walk requirements 1, 2, 3 and 4 individually against the code you wrote: confirm a `console.error` on every fetch-failure branch that renders a failure note — five of them, including both on the answer path — plus one in each of the four `.catch` handlers, nine in total, and none on the local-validation note; that both global handlers are registered at the top level; that the `JSON.parse` in `source.onmessage` sits inside a `try`/`catch` that returns; and that `showNote`, `showJumpNote` and `showCloseNote` are behaviourally unchanged, including the read-aloud note whose ternary you restructured. Report any requirement you could not satisfy as a blocker rather than working around it.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT add a `## Unreleased` entry to `CHANGELOG.md` — the changelog bullet is authored once for this feature at its shipping step, not per prompt.
- Do NOT bump any version string, and do NOT create a git tag.
- Do NOT change `.maintainer.yaml`, `.dark-factory.yaml`, or any Makefile.
- Do NOT touch `pkg/handler/attention-page.go`'s Go code beyond the inline template string, and do not add a new handler file.
- Do NOT add a browser test harness, a Playwright/CDP dependency, or a JS runtime to the Go tests. That is a separate task.
- Do NOT log on the success paths — only failures are logged. A `console.log` on a happy path is noise and a defect here.
- Do NOT add an `onerror` handler to the live-update stream. The absence is deliberate and documented in the file.
- Existing tests must still pass.
- Do NOT add a dependency.
</constraints>

<verification>
Run `make precommit` — must pass. This is the repo's configured `validationCommand` and runs the full Ginkgo suite including the new cases.

Then confirm the markers are present in the served page source, because the executor does not treat a non-zero exit here as failure:

```
for marker in "console.error" "window.onerror" "unhandledrejection"; do
  if grep -q "$marker" pkg/handler/attention-page.go; then echo "OK: $marker present"; else echo "FAIL: $marker missing"; fi
done
```

This must print three `OK` lines.

Then confirm the count is not a single token addition, by counting occurrences rather than lines:

```
n=$(grep -o "console\.error" pkg/handler/attention-page.go | wc -l | tr -d ' ')
if [ "$n" -ge 9 ]; then echo "OK: $n console.error calls"; else echo "FAIL: only $n console.error calls, expected at least 9"; fi
```

This must print `OK`. It is a weak proxy — a count proves calls exist, not that they sit on the right branch — so requirement 6's four-prefix cases and the operator's click-through are the real guards, and this line is a cheap tripwire.

Then confirm each of the four actions has its own console line, so a line that exists on the wrong action is caught:

```
for action in "answer" "read aloud" "jump" "close"; do
  if grep -q "attention board: $action failed - HTTP " pkg/handler/attention-page.go; then echo "OK: $action prefix present"; else echo "FAIL: $action prefix missing"; fi
done
```

This must print four `OK` lines.

Then confirm all nine lines share the one prefix, so a catch line left in a different shape is caught:

```
n=$(grep -o "attention board: " pkg/handler/attention-page.go | wc -l | tr -d ' ')
if [ "$n" -eq 9 ]; then echo "OK: $n lines carry the shared prefix"; else echo "FAIL: $n lines carry the shared prefix, expected 9"; fi
```

This must print `OK`.
</verification>
