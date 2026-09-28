---
status: completed
execution_id: attention-controller-board-e2e-exec-004-board-failure-e2e
dark-factory-version: dev
created: "2026-09-27T22:13:17Z"
queued: "2026-09-27T22:13:17Z"
started: "2026-09-27T22:14:06Z"
completed: "2026-09-27T22:27:38Z"
---

# Prove the board's failure behaviour in a real browser

<summary>
- A malformed message from the live stream is caught in a real browser, not just in the served source
- A dead stream shows the board's not-tracking state, and a restored stream clears it
- A failure note is still on the card after the stream replaces that card
- A retry that succeeds clears the note, so a stale failure cannot outlive its cause
- These run against a real binary in a real browser, so rendering and the DOM are actually exercised
- No new dependency — the browser harness already exists
- The existing cases keep passing and none is weakened
</summary>

<objective>
Close the gap between "the marker is in the served source" and "the behaviour works in a browser". The board's error surface has been verified so far by asserting substrings in the served HTML, which cannot exercise rendering, the DOM or the live stream — and every defect this feature fixed lived in exactly those places. This adds browser-driven cases that prove the four behaviours end to end, and that fail against the revision before the change.
</objective>

<context>
Read these before writing anything:

- `e2e/board_test.go` — the existing browser harness and the file you are extending. Read it in full before writing a line.
- `e2e/e2e_suite_test.go` — the suite entry point and, in its package comment, the reason this package is separate: Ginkgo permits exactly one `RunSpecs` per package, and `pkg/handler/handler_suite_test.go` already calls it. **The suite is deliberately NOT part of `make precommit` and never runs automatically** — it is triggered by `make e2e`.
- `Makefile` — the `e2e` target: installs chromium, then runs `go test -mod=mod -tags e2e -count=1 -timeout 15m ./e2e/`.
- `scenarios/001-board-browser-cases.md` — the scenario that owns the browser run. It hard-codes the current case count and the case names, so adding four cases makes it stale: update the count and add the new names there in the same change, so the scenario and the suite agree rather than drifting apart.
- `pkg/handler/attention-page.go` — the board itself, and the source of every behaviour you are asserting.

**The harness already gives you what you need — use it, do not rebuild it.** `BeforeSuite` starts a real binary and a real browser against it; `AfterSuite` tears them down. The helpers are `post(path, body)`, `push(payload, dedupKey) string`, `answer(itemID)`, `rowCount(page, itemID) int` and `newPage(path) playwright.Page`. Do **not** start a second server, a second browser, or your own fixture store.

**The technique you need is already demonstrated in this file.** The case *"removes a row from the DOM when the item is answered over the SSE stream"* drives the **store** and lets the page learn of it over the stream, with the comment that this is *"what makes this a test of the stream rather than of a local click handler."* Several of your cases need exactly that: change the store, then assert what the page does about it. A click-based test would prove nothing about the stream.

Two details that will save you time:

- The board takes a `hide` query parameter. With the answered rows **shown** rather than hidden, answering an item replaces its row **in place** with a dimmed version instead of removing it — which is a row swap, and a row swap is what destroys a note. `rowCount` and the existing hide=none case show the parameter in use.
- Playwright can intercept a request (`page.Route`, and `page.Unroute` to remove it — both exist in the pinned `playwright-go`). Aborting the stream request is the most direct way to make the stream fail on purpose, and removing the interception is the most direct way to let it recover. ⚠️ **Register the route before the page navigates.** `newPage` navigates immediately, so a route registered afterwards misses the stream request the page has already made — register it first and reload, or build the page yourself for that case.

Conventions that bind this change, inlined deliberately — this worktree has no `CLAUDE.md` (the repo root's `CLAUDE.md` is gitignored via the `.gitignore` entry `/CLAUDE.md`, so it is absent from every branch and the container cannot read it):

- Ginkgo/Gomega, `package e2e`, build tag `e2e` on the file. Match the existing cases' shape.
- ⚠️ **The container cannot run a browser.** It compiles this package and runs `make precommit`; it cannot execute `make e2e`. Write the cases as though they will be run for the first time on a machine that can — the operator runs `make e2e` afterwards. Do not claim in a comment or a report that the cases were observed passing; you cannot observe that here, and saying so would be false.
</context>

<requirements>
1. **Add four cases to `e2e/board_test.go`**, inside the existing `Describe`, following the existing cases' shape and using the existing helpers. Each must assert a **behaviour in the browser**, not a substring in a response body:

   a. **A malformed stream frame is caught, not thrown.** A frame the page cannot parse must be logged and skipped rather than throwing uncaught and aborting that event's row swap. If you cannot drive a malformed frame through the real stream with the harness as it stands, record the limitation as a comment in `e2e/board_test.go` and say so plainly in your report, and cover the behaviour as closely as the harness genuinely allows — **do not** fake it with a substring assertion and present it as the browser case. A reported limitation is worth more than a test that proves nothing.

   b. **A dead stream shows the not-tracking state, and a restored stream clears it.** Make the stream fail, assert the board's not-tracking state is visible, let the stream recover, assert the state is gone. The state must be found by its rendered presence, not by grepping the page source.

   c. **A failure note survives a stream event that replaces its row.** Produce a failure note on a card, then cause a real stream event for that same item so its row is replaced, then assert the note is **still rendered**. This is the case the whole change exists for; it must go through the stream, not a local click.

   d. **A retry that succeeds clears the note.** After a failure note is rendered, let that action succeed and assert the note is gone — a stale failure must not outlive its cause.

2. **Do not weaken, reword or delete any existing case.** The five existing cases stay byte-identical. If one of them genuinely must change to accommodate yours, say so explicitly in your report and explain why, rather than editing it quietly.

3. **Keep the suite out of `make precommit`.** The package comment states that exclusion is deliberate; do not add the `e2e` target to `precommit`, and do not add the build tag to any file that `precommit` compiles.

4. **Add no dependency.** `github.com/mxschmitt/playwright-go` is already required; nothing else is needed.

5. **Self-check before finishing.** Re-run the `<verification>` command and confirm it passes. Then walk requirements 1, 2 and 3 individually against what you wrote: confirm all four cases exist and each asserts a rendered behaviour rather than a substring; confirm every pre-existing case is unchanged; and confirm `make precommit` does not run the e2e package. Report any requirement you could not satisfy as a blocker rather than working around it — in particular, if a case cannot be driven through the harness, report that instead of weakening it.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT add a `## Unreleased` entry to `CHANGELOG.md` — the changelog bullet is authored once at the shipping step.
- Do NOT bump any version string, and do NOT create a git tag.
- Do NOT change `.maintainer.yaml`, `.dark-factory.yaml`, or the `precommit` target of the Makefile.
- Do NOT start a second server, browser or fixture store — use `BeforeSuite`'s.
- Do NOT add a dependency.
- Do NOT weaken, reword or delete an existing case.
- Do NOT introduce the literal `attention board: ` anywhere in `pkg/handler/attention-page.go` — an existing test (`pkg/handler/attention-page_test.go`) asserts it appears exactly nine times.
- Do NOT write a case that passes by grepping the served HTML. The whole point of this change is to stop asserting on strings.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must pass. This is the repo's validation command and runs the Ginkgo suite. It deliberately does **not** include the e2e package.

Then confirm the e2e package still **compiles**, since a browser cannot be launched here. This compiles every test binary in the package and runs no specs:

```
if go test -mod=mod -tags e2e -run='^$' -count=1 ./e2e/ >/dev/null 2>&1; then echo "OK: e2e package compiles"; else echo "FAIL: e2e package does not compile"; fi
```

This must print `OK`. ⚠️ **It is a weak proxy and it already passes on the current base**, so it carries no change signal at all — it proves the package still compiles, nothing about the new cases. The four marker greps below are the only checks here that move; the operator's `make e2e` on a machine with a browser is the evidence.

Then confirm the four cases actually landed, so a case that never landed cannot satisfy this (it cannot tell a case from a comment — that is what the operator's run is for):

```
for marker in "not-tracking" "survives" "clears the note" "malformed"; do
  n=$(grep -ci "$marker" e2e/board_test.go)
  if [ "$n" -ge 1 ]; then echo "OK: $marker referenced"; else echo "FAIL: no reference to $marker"; fi
done
```

This must print four `OK` lines. It is a tripwire for presence, not for correctness — the operator's `make e2e` run is what proves the behaviour.
</verification>
