---
status: completed
summary: Kept failure notes alive across the board's row swaps via an item-keyed map replayed through the original note helpers, and added a visible not-tracking state to the board's control row driven by source.onerror.
execution_id: attention-controller-board-visibility-exec-003-board-failure-stays-visible
dark-factory-version: dev
created: "2026-09-27T21:21:15Z"
queued: "2026-09-27T21:21:15Z"
started: "2026-09-27T21:21:59Z"
completed: "2026-09-27T21:32:36Z"
---

# Keep a failed action visible, and show when the live stream has stopped

<summary>
- A failure note stays on the card instead of disappearing when the card refreshes itself
- The note the operator was reading is still there after a live update replaces the card
- The board says plainly when it has stopped tracking the store
- The "not tracking" state appears without opening devtools, and clears by itself on reconnect
- A board that is merely quiet still looks quiet — the state appears only after the stream errors, and clears on the next message, so a brief store restart shows it only for the gap
- Nothing else the operator sees changes
- No new dependency, no build step, no change to how a card is rendered
</summary>

<objective>
Make a board failure stay visible. Two holes remain in the error surface: a failure note is destroyed the moment the live stream replaces that card's node, so an operator who was reading it loses it mid-sentence; and a stream that has died permanently leaves the board frozen at its load-time state with no signal at all, so the operator reads a stale board as a current one. Both are the same defect — a failure the operator can no longer see — and both are fixed without changing what a healthy board looks like.
</objective>

<context>
Read these before writing anything — the change is confined to one inline script and must read as though the same author wrote it:

- `pkg/handler/attention-page.go` — the whole board: the HTML template, the inline `<style>` block and the inline `<script>`. All board JavaScript lives here. There is no `.js` asset anywhere in the repo and this change adds none.
- `pkg/handler/attention-page_test.go` — the file that asserts the inline script's own source, and the home for the new cases.

**The precedent to follow for the note-survives problem already exists in this file.** `upsertRow(itemID, html)` re-applies `speakButtonState(speakControl(itemID), true)` after it replaces a row's node, driven by a `speaking` map keyed on the item id — and its comment states the reason: *"The row's node was just replaced, so the toggle's repainted state went with it. Re-applied from the map rather than re-rendered by the server, because the map lives in this page: the server keeps no record of what this page is playing."* A failure note dies to exactly that mechanism. Read `upsertRow`, the `speaking` map and `speakButtonState` before designing anything — the new code should be recognisably the same idea.

The existing error surface this change extends and must not alter:

- **Three note helpers** — `showNote(form, message, isError)`, `showJumpNote(row, message, isError)` and `showCloseNote(row, message, isError)` — each appending a `<div>`/`<span>` carrying class `note failed` beside its control. Their text, placement and class stay exactly as they are.
- **Two node-replacing paths** destroy a note. `upsertRow` writes `row.outerHTML = html` for a visible row and `parked[hidden].html = html` for a row the filter has hidden. Both lose whatever was appended inside.
- **The live stream** — `source = new EventSource('/api/1.0/attention/stream')`, with `source.onmessage` and **no `onerror` handler**. The comment above it records why that absence is deliberate: *"No reconnect handler: EventSource reconnects on its own, which is the property that lets the board survive a restart of the store."* This prompt adds a **visible state**, not reconnect logic — `EventSource` still owns reconnection.
- **The board's control row** — `<div class="board-controls">` holds the **Hide answered** filter switch, and the `<style>` block already defines a `--warn` colour used by `.note.failed`. That row is where a stream-health indicator belongs, so it sits with the other thing that describes the board rather than the items.

Conventions that bind this change, inlined deliberately — this worktree has no `CLAUDE.md` (the repo root's `CLAUDE.md` is gitignored via the `.gitignore` entry `/CLAUDE.md`, so it is absent from every branch and the container cannot read it):

- The page carries no framework, no bundler and no build step. Plain DOM work inside a Go string constant is the house style; do not introduce a library.
- The inline script is written in ES5 style (`var`, `function`). Match it. It also lives inside a backtick-delimited Go raw string, so no backtick may appear anywhere in the script — not in a template literal, not in a comment.
- The container cannot load the page in a browser. Browser-side verification is operator-side and out of scope here; what the container verifies is that the markers are present in the served source and that the suite passes.

⚠️ **One constraint comes from the previous change and is not negotiable.** The literal `attention board: ` is **reserved** for the nine console lines that change added — `pkg/handler/attention-page_test.go` asserts `strings.Count(body, "attention board: ")` is **exactly 9**. Do not introduce that literal anywhere new, not even in a comment: a tenth occurrence fails an existing spec on otherwise-correct code. Your own console lines, if any, must use a different prefix.
</context>

<requirements>
1. **Make a failure note survive the row swap.** A note rendered by `showNote`, `showJumpNote` or `showCloseNote` is destroyed when `upsertRow` replaces the row's node. Keep the most recent failure per item id in a map — mirroring how `speaking` is keyed — and re-render that note after `upsertRow` has written the new node, on **both** paths: the visible row (`row.outerHTML = html`) and the parked row (`parked[hidden].html = html`).

   Record the note when a helper renders it, and clear the entry when the action next succeeds — a stale failure note that outlives its cause is a worse defect than the one being fixed, because it reports a failure that is no longer true. A success note is not a failure: do not persist it.

   The re-rendered note must be identical in text, placement and class to the one the helper produced. Prefer re-using the existing helpers' own rendering over writing a second renderer that could drift from them.

2. **Say when the stream has stopped.** Add a `source.onerror` handler that renders a visible state on the board, and clear it on the next `source.onmessage`. Today a permanently dead stream (a 404 after a redeploy, a persistent 500) leaves the board frozen at its load-time state with no signal.

   - Place it in the existing `<div class="board-controls">` row, beside the **Hide answered** filter switch, so it reads as a property of the board rather than of an item. Do **not** change the filter switch, its markup, its `aria-checked` handling or its behaviour.
   - Give it its own class and style it with the `--warn` colour the `<style>` block already defines. Do not add a new colour, a popup, a modal or an overlay.
   - The text must say what is wrong in the operator's terms — that the board is no longer tracking the store and is showing its last known state — not merely that an error occurred.
   - It must be readable **without devtools**, which is the whole point: this is the state that currently has no signal at all.

3. **Do not reconnect, and do not suppress the browser's own behaviour.** `EventSource` reconnects on its own and that stays true — `source.onerror` fires on each failed attempt, so the handler renders the state and returns. Do not call `source.close()`, do not build a retry loop, and do not remove or reword the existing comment explaining that the absence of a reconnect handler is deliberate; extend it instead to record what the new handler does and does not do.

4. **A quiet board still looks quiet.** The not-tracking state appears only after `onerror` fires. It must not render on a healthy load, and it must not be triggered by a `remove` event, an empty board, or the `Nothing needs attention.` empty state.

5. **Do not change anything else the operator sees.** The note helpers' text, placement and classes stay as they are; the success paths stay as they are; the row rendering, the filter and the empty state are unchanged. A restyle or a new visual language is a defect here.

6. **Do not add a dependency, a build step, or a `.js` asset.** `EventSource`, `console` and `JSON` are language built-ins; nothing else is needed.

7. **Add test cases to `pkg/handler/attention-page_test.go`.** Follow that file's existing substring-assertion shape, and note that `pkg/handler/attention-board-page_test.go` asserts rendered row markup rather than the script, so it is not the home for these. At minimum assert:
   - the served body contains `source.onerror`,
   - the served body contains the class name you chose for the not-tracking state,
   - `strings.Count(body, "attention board: ")` is **still exactly 9** — assert it here as well as in the existing spec, so a later change that introduces the reserved prefix fails loudly rather than silently,
   - the served body contains a marker for the note-survives mechanism (the map you add), so a regression that drops the re-render is caught.

   These assert presence in the served source, which is the boundary this change crosses. Do not describe them in a comment as verifying browser behaviour — they do not. The operator's own click-through covers that, and it happens outside this container.

8. **Self-check before finishing.** Re-run the `<verification>` command and confirm it passes. Then walk requirements 1, 2 and 4 individually against the code you wrote: confirm the note map is re-rendered on **both** `upsertRow` paths and cleared on success; confirm `source.onerror` exists, renders into the control row, and is cleared in `source.onmessage`; confirm no healthy-load path can render the not-tracking state; and confirm `attention board: ` still appears exactly nine times. Report any requirement you could not satisfy as a blocker rather than working around it.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT add a `## Unreleased` entry to `CHANGELOG.md` — the changelog bullet is authored once for this feature at its shipping step, not per prompt.
- Do NOT bump any version string, and do NOT create a git tag.
- Do NOT change `.maintainer.yaml`, `.dark-factory.yaml`, or any Makefile.
- Do NOT touch `pkg/handler/attention-page.go`'s Go code beyond the inline template string, and do not add a new handler file.
- Do NOT introduce the literal `attention board: ` anywhere — an existing spec asserts it appears exactly nine times.
- Do NOT add a browser test harness, a Playwright/CDP dependency, or a JS runtime to the Go tests. That is a separate task.
- Do NOT add a retry loop, a reconnect handler, or a `source.close()` call — `EventSource` owns reconnection.
- Do NOT log on the success paths.
- Existing tests must still pass.
- Do NOT add a dependency.
</constraints>

<verification>
Run `make precommit` — must pass. This is the repo's validation command and runs the full Ginkgo suite including the new cases.

Then confirm the reserved prefix was not widened — this is the check that protects the previous change's spec, and it must fail loudly if a tenth occurrence appears:

```
n=$(grep -o "attention board: " pkg/handler/attention-page.go | wc -l | tr -d ' ')
if [ "$n" -eq 9 ]; then echo "OK: reserved prefix still 9"; else echo "FAIL: reserved prefix is $n, must stay 9"; fi
```

This must print `OK`.

Then confirm the not-tracking state actually landed rather than being described in a comment — count the occurrences, so a marker mentioned only in prose cannot satisfy it:

```
n=$(grep -o "source.onerror" pkg/handler/attention-page.go | wc -l | tr -d ' ')
if [ "$n" -ge 1 ]; then echo "OK: $n source.onerror references"; else echo "FAIL: no source.onerror reference"; fi
```

This must print `OK`. It is a weak proxy — a reference proves the handler is written, not that it renders anything — so requirement 7's class-name case and the operator's click-through are the real guards, and this line is a cheap tripwire.
</verification>
