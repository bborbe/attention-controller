---
status: completed
spec: [012-render-decide-pair-on-every-permission-card]
summary: 'Retired both exclusions from docs/production-touching-marker.md, keeping the marker convention documented, and verified the existing ## Unreleased changelog entry without editing it'
execution_id: attention-controller-corner-x-all-classes-exec-046-spec-012-retire-exclusions-and-changelog
dark-factory-version: v0.198.7
created: "2026-10-16T16:30:00Z"
queued: "2026-10-16T16:40:28Z"
started: "2026-10-16T16:40:29Z"
completed: "2026-10-16T16:42:42Z"
---

<summary>
- The in-tree page that documented the withheld Allow / Deny pair stops describing an exclusion the board no longer applies.
- The page still documents the production-touching marker convention itself — the marker's form, the pattern that recognises it, and the traps inside that pattern — because the convention outlives the board's read of it.
- Every statement that the board withholds a control, that either gate fails open or closed, or that a marker suppresses the pair is gone from the page.
- The page states plainly what replaced the two fail directions: nothing, because the render path consults no provenance fact any more.
- The changelog **already carries** an `## Unreleased` section with this change's bullet — dark-factory's changelog suffix wrote it while the preceding prompt ran — so this prompt verifies that entry rather than creating one.
- No released changelog section is touched, and no version heading is added by hand — the releaser owns that rename.
- No Go file, test, scenario, spec or vault file changes; the two now-unread provenance facts stay exactly where they are.
- The repo's test and precommit gates are re-run as the final validation.
</summary>

<objective>
Retire both exclusions from the in-tree marker page `docs/production-touching-marker.md` so it describes the board as it now ships, and verify the change's existing entry under the `## Unreleased` heading in `CHANGELOG.md`. Doc and release hygiene only — this prompt changes no Go, no test, no scenario and no spec.
</objective>

<context>
This repo carries no in-repo root `CLAUDE.md`; follow the coding plugin docs below for conventions.

The behavioral change this prompt documents has already landed (its preceding prompt dropped the two provenance terms from the `Decide` derivation, reconciled the twelve served-page cases and extended the e2e answer-shape case). This prompt writes down what shipped, and nothing else.

Read these files in full before changing anything:
- `docs/production-touching-marker.md` — the page being retired. It is a short page (55 lines) organised end to end around the exclusion. ⚠️ **Read every line**: the spec names four sites, but the page asserts the exclusion in more sentences than those four, and a sentence left standing teaches behaviour the shipped code no longer has.
- `CHANGELOG.md` — its top. Read the preamble block (the `# Changelog` title, the `All notable changes…` line, the SemVer link and the three `* MAJOR / MINOR / PATCH` bullets) and the topmost section. ⚠️ **`## Unreleased` ALREADY EXISTS and already carries this change's bullet** — dark-factory's changelog suffix wrote both while the preceding prompt ran, so this prompt verifies that entry and creates nothing. ⚠️ **Re-read the top when you start:** `.maintainer.yaml` carries `release.autoRelease: true`, so a releaser can rename `## Unreleased` to `## vX.Y.Z` under you (it did exactly that while an earlier draft of this prompt was being written, moving the top from `## v0.54.4` to `## v0.55.0`). If the heading has become a released section carrying this change's bullet, the releaser got there first — that is correct, not a failure; never hardcode a version number.
- `prompts/1-spec-012-decide-derivation-comments-and-test-reconciliation.md` — the vocabulary this spec uses for the retired class split, and the wording of the change being recorded.
- `specs/in-progress/012-render-decide-pair-on-every-permission-card.md` — the `## Constraints` row on `docs/production-touching-marker.md`, AC9, and the `## Verification` probes. This prompt covers the spec's row-3 focus (AC9) and is the second of two prompt files: the spec's rows 1 and 2 were merged into `prompts/1-spec-012-decide-derivation-comments-and-test-reconciliation.md`, because `validationCommand: make precommit` runs `test`, so a code-only prompt would be marked FAILED and no later prompt would run.
- `.maintainer.yaml` — read the `release.autoRelease: true` comment. `.dark-factory.yaml` — read `autoRelease: false`. The two point opposite ways and both matter to the heading placement.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — the preamble-frozen rule, `## Unreleased` placement, the conventional-prefix rule, entry style and anti-patterns.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — § 4, why `docs/` exists: it is context memory for an assistant resuming work months later, so it must describe the **current** state, not a superseded one.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — the DoD this prompt's final gate satisfies.
</context>

<requirements>
1. **Retire the exclusion framing in `docs/production-touching-marker.md`.** The page currently states that the board withholds the Allow / Deny pair, that the two gates have opposite fail directions, and that a marker suppresses the pair. None of that is true of the shipped code any more. Replace the file's body with the page given in requirement 2.

   ⚠️ **The spec names four sites; the page carries more.** All of the following must go, and every one of them is asserted by the absence probe in `<verification>`:

   - **Line 3's headline** — *"The board withholds the Allow / Deny pair from a headless worker's `permission` park when the item's own task file declares a production-touching step. This page records the convention that declaration uses, because the board now depends on it…"*. Both clauses are false: the headless clause and the marker clause.
   - **§ Fail direction** (the whole section, including the two-row table) — *"Absence of the marker renders the pair — the exclusion is fail-open, and this is the opposite polarity to the headless gate (which is fail-closed)"*, the `Headless` and `ProductionTouching` table rows, and *"Neither polarity may be flipped…"*.
   - **§ What this withholds, and what it does not** (the whole section) — *"The exclusion is render-level. It withholds a control…"*, *"Read the feature as 'the board does not offer a one-click approval here'…"*, and *"The withheld pair renders as nothing, with no affordance naming the reason."* ⚠️ **This is the page's own statement of the exclusion's contract and the section a later reader would quote — do not leave it standing.**
   - **Four further sentences the spec's four-site list does not name**, each of which asserts the exclusion just as directly: line 25 (*"…a one-click Allow on a destructive action is the harm the exclusion exists to prevent"*), line 29 (*"…a substring match fires on all of it, removing a control the operator needs"*), line 31 (*"Two limits the pattern encodes silently, and both fail open — a marker that misses them renders the pair with no indication why"*) and line 36 (*"Any marker in the item's task suppresses the pair for every park that task raises… The fail-safe direction is chosen over the precise one."*).

   ⚠️ **The § Fail direction heading and the § What this withholds heading must both be replaced by headings that do not assert an active exclusion.** The absence probe keys on `What this withholds`; a page that keeps that heading has not retired the section.

   ⚠️ **Any reference to the retired exclusion must be past tense and dated (2026-10-16), never stated as current behaviour.** A page that still teaches the withheld behaviour contradicts the shipped code; a page that records *when and why* the framing changed is correct and useful.

2. **Use this page as the target.** Replace the file's body with the following (the file's first line is `# The production-touching marker`; keep it). ⚠️ **Equivalent wording is acceptable provided the absence probe in `<verification>` returns 0 and every survivor probe returns its stated count** — but do not paraphrase the marker's pattern literal, the glyph-group warning or the checkbox-class warning, which are the convention's substance.

````markdown
# The production-touching marker

⚠️ **The board renders the Allow / Deny pair on every `permission` card, whichever class it belongs to, as of 2026-10-16.** This page records the production-touching marker convention — the operator's authored declaration that a step must not be approved with one click — which the board **no longer consults**. The marker is still parsed and still resolved on every page load (`pkg/provenance.go`), but nothing consumes the result. The convention outlives the board's read of it, so it is documented here rather than deleted.

## The marker

A task's subtask line carries it:

```markdown
- [ ] ⚠️ production-touching — `make install`; …
```

The detected form is a task checkbox, an optional warning glyph, the phrase `production-touching`, and an em-dash separator:

```
(?m)^\s*-\s*\[[ x/]\]\s*(?:⚠️?)?\s*production-touching\s*—
```

⚠️ **The leading `(?m)` is required, not decoration.** Without it `^` anchors only at the start of the whole text, so the pattern matches a marker on the **first line** of a task file and silently misses every one below it. The implementation carries it (`pkg/provenance.go`).

⚠️ **The glyph group is `(?:⚠️?)?`, and the outer group is load-bearing.** `⚠️` is two code points — U+26A0 plus the variation selector U+FE0F — so a bare `⚠️?` makes only the *selector* optional and still **requires** the base glyph. That silently fails every marker written without it, which is the opposite of what "optional glyph" means. The outer non-capturing group is what makes the whole glyph optional.

## Why the marker and not the command

The parked command is available — the event log records it — but it cannot be classified soundly. A command obfuscates, aliases, wraps in a script, or splits across lines, so any classifier over it fails open on exactly the inputs that matter, and a one-click Allow on a destructive action is the harm the convention exists to name. The marker is **authored**: the operator declares which steps are production-touching, and nothing is guessed.

⚠️ **The board no longer acts on the declaration.** Until 2026-10-16 the board withheld the pair from a `permission` park whose task carried the marker; that exclusion was overruled, and the pair now renders on every `permission` card. The convention is kept because it is the operator's own record of which steps are irreversible, and because the parse remains in `pkg/provenance.go`.

## The two properties that matter

**The match is list-item form only, never a bare substring.** Task files discuss the phrase in ordinary prose — an `# Impact`, a `# Success Criteria`, a `# Progress` entry — and the pattern is deliberately narrow so that prose is never mistaken for a declaration. Only the checkbox marker counts.

⚠️ **Two limits the pattern encodes silently — a marker that misses them is not recognised at all:**

- **The checkbox class is exactly `[ x/]`.** A capital `X` — `- [X] ⚠️ production-touching — …` — does **not** match. The vault writes lowercase for a checked box; if you ever write the capital, the marker is inert.
- **The phrase match is case-sensitive.** `Production-Touching`, `PRODUCTION-TOUCHING` and any other casing do **not** match. Write it lowercase, exactly as shown above.

**The reading was task-level, not command-level.** A marker in the item's task once applied to *every* park that task raised, benign ones included. The board no longer reads the marker, so nothing is withheld on that or on any other basis.

## Fail direction: none remains

⚠️ **There is no fail direction left to describe, and that is the point of the change.** Until 2026-10-16 two provenance facts gated the pair and pulled in opposite directions — the session-mode read failed **closed** (an unresolvable session rendered no control) while the marker read failed **open** (an undetected marker rendered the pair). Both gates are gone from the render path: the pair renders on every `permission` item, including one whose provenance cannot be resolved at all. No uncertain input is left to resolve, so there is no polarity to state and none to flip.

## Nothing is withheld at the render level

⚠️ **Nothing is withheld at the render level any more: the pair renders on every `permission` card.** One property of the repository is unchanged, and it never depended on either exclusion: the answer endpoint does not police provenance. `handleAttentionAnswer` accepts a `{"decision":"allow"}` POST against any item id and applies no provenance check at all — so a board control is an *offering*, never an *authorisation*, and an answer posted directly has always been accepted. That is the repo's standing design.
````

3. **Keep the convention documented — do not delete the page.** The marker convention itself is unchanged (it is still authored by hand on the task's own subtask line, and `pkg/provenance.go` still parses it), so the page survives with its title, the marker example, the pattern literal, the `(?m)` warning, the glyph-group warning, the checkbox-class warning and the case-sensitivity warning intact. A deleted page loses the only in-tree record of the convention.

4. ⚠️ **`CHANGELOG.md` IS ALREADY DONE — VERIFY IT, DO NOT REPEAT IT.**

   **This requirement was rewritten after the preceding prompt ran, and the reason matters.** dark-factory appends a changelog suffix to every prompt when `CHANGELOG.md` exists, and that suffix already inserted `## Unreleased` and wrote a full bullet for this change. So `grep -c '^## Unreleased' CHANGELOG.md` reads **`1` before you touch anything**, and the heading sits between the preamble and the topmost released section.

   ⚠️ **Do NOT create the heading. Do NOT add a second bullet. Do NOT edit the existing one.** Two bullets for one logical change is the failure this rewrite exists to prevent, and it is an easy mistake to make: the previous wording said "insert the heading", and an agent told to insert a heading does not check whether one is already there.

   **Verify instead, and report each number:**
   - `grep -c '^## Unreleased' CHANGELOG.md` — must read **`1`**. A `2` means you duplicated the heading; revert that before finishing.
   - `grep -m1 '^## ' CHANGELOG.md` — must print `## Unreleased`, i.e. it sits above the topmost released section.
   - `grep -c 'Allow / Deny pair now renders on every' CHANGELOG.md` — must read **`1`**. That phrase occurs in the suffix's bullet and nowhere else in the file, so it is the discriminating anchor. ⚠️ **Do NOT probe the bare `Allow / Deny pair`**: it occurs **8** times, in released sections as well, so it cannot tell this change's bullet from any older one. ⚠️ If the count is `0`, the suffix did not run: add exactly ONE bullet in the changelog guide's shape, say so explicitly in your completion report, and still do not add a second.
   - The preamble is intact — the `* MAJOR version when you make incompatible API changes,` bullet still sits above `## Unreleased`, and nothing was inserted above or inside the preamble block (the changelog guide's `preamble-frozen` MUST rule).

   ⚠️ **Your actual work in this prompt is requirement 1, the doc page.** If `CHANGELOG.md` needs no edit, say exactly that in the report rather than inventing one.

5. **Touch nothing else.** No released `## vX.Y.Z` section is edited, reordered or removed; no version heading is added by hand; no Go file, no test file, no `e2e/`, no `scenarios/`, no `specs/` file and no vault task file changes. `specs/in-progress/007-production-touching-exclusion.md` stays exactly where it is — its disposition is not this prompt's work.

6. **Self-check before finishing:** re-run every command in `<verification>` and confirm each prints what it claims. Do not report success on a command you did not run, and record the pre-edit baseline numbers the block asks for.
</requirements>

<constraints>
- ⚠️ **This prompt changes NO Go.** `pkg/handler/attention-page-helpers.go`, `pkg/handler/attention-page.go`, `pkg/provenance.go` and every test file are already in their final state. Do not open them for editing; read them only if you need to confirm a claim you are about to write down.
- ⚠️ **The two provenance facts are NOT deleted and NOT added to `Resolved()`.** `provenance.Headless` and `provenance.ProductionTouching` lose their only reader, but the session-mode read and the marker read stay exactly as they are, and both stay out of `Provenance.Resolved()`. Deleting them removes a shipped spec's behaviour and its tests; adding them to `Resolved()` puts non-drawing booleans in the provenance line's gate, which specs 004 and 007 both forbid. Write them down as dead reads, not as dead facts.
- ⚠️ **The marker convention itself is unchanged.** `docs/production-touching-marker.md` documents the operator's authored marker and is not deleted; the vault's task files are not edited. Only the board's *consultation* of the marker ends.
- ⚠️ **No confirmation step, no second click, no warning interstitial, and no control between the operator and the verdict.** The exclusions were overruled by the operator with the hazard quoted on the card that carried the ruling; this change implements the ruling and does not re-argue it. Do not add such a caveat to the page or the changelog.
- ⚠️ **Do NOT edit `specs/in-progress/007-production-touching-exclusion.md` or any other spec file.** Spec 007 is left in place at `verifying` with ACs this change deliberately falsifies; its disposition is recorded on the carrying vault task, not resolved here.
- ⚠️ **Do NOT touch `scenarios/001-board-browser-cases.md` or `scenarios/002-board-answer-shapes.md`.** Both assert the e2e package total as `36 of 36`, and the preceding prompt extends an existing case rather than adding one precisely so that total does not move.
- ⚠️ **Add no version heading and do not restructure the released sections.** The rename `## Unreleased` -> `## vX.Y.Z` is the releaser's job, not yours.
- ⚠️ **Two release settings point opposite ways, and both are load-bearing.** `.dark-factory.yaml`'s `autoRelease: false` means dark-factory commits only. `.maintainer.yaml`'s `release.autoRelease: true` means the github-releaser **will** rename `## Unreleased` -> `## vX.Y.Z`, commit, tag and push once master moves with a non-empty `## Unreleased`. ⚠️ **Read the placement probe's output before the releaser fires**, or it will print a released heading and appear to fail while being correct; if the `## Unreleased` count is 0 and a fresh `## vX.Y.Z` carries your bullet, the releaser simply got there first.
- ⚠️ **No Prometheus metric, no config field, no new file.** The spec asks for none, and this prompt adds none.
- ⚠️ **Do NOT commit — dark-factory handles git.** ⚠️ **The container's `.git` is masked, so never run any `git` command** — it dies with `fatal: not a git repository`, and the daemon's executor does not check verification exit codes, so a failed `git` step ships as a silent pass. Every probe in `<verification>` is therefore a filesystem check, and AC9's diff-against-`origin/master` half is replaced there by the non-git equivalent the changelog prompt for spec 007 already used.
- ⚠️ **Do NOT run `make e2e` or `go test -tags e2e ./e2e/`.** That package is build-tagged, is invisible to `make test` and `make precommit`, and needs a Chromium download the YOLO container does not have. A doc and changelog edit cannot affect it.
- ⚠️ **`docs/` is context memory for an assistant resuming work months from now.** A page that still describes the withheld pair is worse than no page: it is a confident wrong answer. Correct the framing; do not merely append a note beside it.
- Errors, if any arise, wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`. (This prompt should produce no Go change at all.)
- Existing tests must still pass, and no existing test is weakened.

<!--
OPEN QUESTIONS FOR THE REVIEWER (non-interactive generation; resolved best-effort from the spec):

1. The spec names FOUR sites in `docs/production-touching-marker.md` (line 3, § Fail direction's lines 40 and 44, and § What this withholds at 49-55). Reading the whole page, the exclusion is asserted on THIRTEEN content lines — 3, 25, 29, 31, 36, 40, 44, 45, 47, 49, 51, 53, 55 — plus the `## Fail direction` heading at line 38, i.e. fourteen sites by this prompt's own granularity. The spec's list omits lines 25, 29, 31 and 36 outright and groups § What this withholds' four lines and § Fail direction's four lines as two sections — and two of them (29 and 31) are the *rationale* for the pattern's narrowness, which the change makes historical. The spec's own instruction is to "enumerate every sentence that asserts either exclusion", so requirement 1 covers all fourteen sites — by whole-section replacement for § Fail direction and § What this withholds, and by named sentence for the rest — and the absence probe keys on 14 substrings across 12 lines. ⚠️ Two residual gaps: the probe does not key line 45's text nor line 25's second clause; both are removed by the whole-section replacement, so the page still scores 0, but the probe is not a complete oracle for requirement 1's claim. The four-site list reads as an incomplete enumeration rather than a deliberate limit.

2. The spec's AC9 says "`v0.54.4` is the top section". That was true when the spec was written and is **false now twice over**: a releaser cut `## v0.55.0` while this prompt was being generated, and then dark-factory's changelog suffix created `## Unreleased` and wrote this change's bullet while the PRECEDING prompt ran. So both halves of AC9's premise are stale — `grep -c '^## Unreleased'` now returns **`1`**, not 0. Requirement 4 was rewritten from "create the heading and add the bullet" to "verify the heading and the bullet", because the old wording would have produced a duplicate heading and a second bullet for one logical change.

3. The spec's AC9 evidence is `awk` (kept verbatim) plus a diff against `origin/master` showing no `+## vX.Y.Z` line. The second half cannot run in the container (`.git` masked), so `<verification>` uses the non-git equivalent already established by this repo's spec-007 changelog prompt: the `^## v` heading count must be unchanged from the pre-edit baseline, recorded before the edit.

4. The sibling prompt in this spec carries a `branch:` frontmatter field. `docs/rules/prompt-writing.md` sanctions only `spec`, `status`, `created` and `issue` ("dark-factory adds the rest"), so this prompt omits it and quotes the spec slug in the canonical array form. The sibling's `branch:` is a rule deviation worth correcting there, not a convention to propagate.

5. `make precommit` is included as the final gate. It is the repo's DoD target and the spec's `## Verification` labels it container-executable, and this repo's spec-007 changelog prompt ran it successfully in the same container. If the container lacks network for its `vulncheck` / `osv-scanner` / `trivy` sub-steps, that is an environment failure to report honestly, not a defect of the doc edit — the doc and changelog probes above are this prompt's own evidence and do not depend on it.
-->

<verification>
Run inside the repo root. ⚠️ **No `git` command** (the container's `.git` is masked) and **no `make e2e`** (see `<constraints>`).

**Step 0 — baseline, BEFORE making any edit. Record both numbers:**

- ⚠️ `grep -c 'provenance.Headless\|provenance.ProductionTouching' pkg/handler/attention-page-helpers.go` — must print **`0`**. This is the guard that the preceding prompt's code change actually landed. ⚠️ **If it prints non-zero, STOP and report it** — it means this prompt would document a change that has not shipped, and every other probe here would pass identically on the un-changed tree, so nothing else in this prompt would catch it.

- `grep -cE 'The board withholds the Allow / Deny pair|the exclusion is fail-open|the opposite polarity to the headless gate|a headless card loses a control it should have|Neither polarity may be flipped|suppresses the pair for|the harm the exclusion exists to prevent|removing a control the operator needs|renders the pair with no indication why|The exclusion is render-level|the board does not offer a one-click approval here|The withheld pair renders as nothing|The fail-safe direction is chosen over the precise one|What this withholds' docs/production-touching-marker.md` — prints **12** before your edits. ⚠️ **Run this first and confirm 12** — a pattern that reads 0 on both trees enforces nothing. Each alternative is a verbatim substring of one line, so `grep -c` (which counts lines) is the right form; line 36 and line 40 each carry two of the alternatives.
- `grep -c '^## v' CHANGELOG.md` — record it (**103** at the time of writing). The container's `.git` is masked, so AC9's diff-against-`origin/master` check cannot run here; this count is its non-git equivalent, and it must not change.

**Doc absence probe (the discriminating half):**

- `grep -cE 'The board withholds the Allow / Deny pair|the exclusion is fail-open|the opposite polarity to the headless gate|a headless card loses a control it should have|Neither polarity may be flipped|suppresses the pair for|the harm the exclusion exists to prevent|removing a control the operator needs|renders the pair with no indication why|The exclusion is render-level|the board does not offer a one-click approval here|The withheld pair renders as nothing|The fail-safe direction is chosen over the precise one|What this withholds' docs/production-touching-marker.md` — prints **0**. ⚠️ `grep -c` prints `0` but exits 1; read the printed number. Do NOT use `! grep -qE` as an exit-code form: it exits 0 on a MISSING file, so it would pass a deleted page. Deletion is caught only by the survivor probe `grep -c '^# The production-touching marker'` = 1 below — run both.

**Doc survivor probes (the convention must still be documented):**

- `grep -c -F '(?m)^\s*-\s*\[[ x/]\]' docs/production-touching-marker.md` — prints `1` (the pattern literal survived).
- `grep -c -F 'U+FE0F' docs/production-touching-marker.md` — prints `1` (the glyph-group warning survived).
- `grep -c -F 'PRODUCTION-TOUCHING' docs/production-touching-marker.md` — prints `1` (the case-sensitivity warning survived).
- `grep -c -F 'pkg/provenance.go' docs/production-touching-marker.md` — prints at least `1` (the implementation reference survived).
- `grep -c '^# The production-touching marker' docs/production-touching-marker.md` — prints `1` (the page was not deleted or retitled).

**Changelog probes:**

- `grep -c '^## Unreleased' CHANGELOG.md` — prints exactly `1`.
- `grep -m1 '^## ' CHANGELOG.md` — prints `## Unreleased`. ⚠️ `-m1` is load-bearing: it reads the **first** section heading, so a bullet placed under a released section cannot satisfy this together with the probe above.
- `awk '/^## /{sec=$0} /Allow \/ Deny pair now renders on every/{print "sits under: " sec}' CHANGELOG.md` — prints **exactly one** line, and it names `## Unreleased`. ⚠️ **The phrase is the discriminating anchor, and the pattern was verified against the real file:** `grep -c 'Allow / Deny pair now renders on every'` reads **`1`**, while the bare `Allow / Deny pair` reads **`8`** because released sections carry it too — so a looser pattern could not tell this change's bullet from an older one. ⚠️ An earlier draft keyed on `Allow / Deny pair renders on every`, which reads **`0`** against the bullet dark-factory actually wrote — it says the pair **now** renders on every card.
- `grep -c -F 'Allow / Deny pair now renders on every' CHANGELOG.md` — prints `1` (the bullet carries the exact wording the probe above keys on).
- `grep -c '^## v' CHANGELOG.md` — **unchanged from the Step 0 baseline** (no version heading was added). ⚠️ If a releaser fires between your edit and this read, the count rises by one and `^## Unreleased` drops to 0 — that is the releaser being correct, not your edit being wrong; confirm by reading the new `## vX.Y.Z` section for your bullet.
- `grep -c '^\* MAJOR version when you make incompatible API changes,' CHANGELOG.md` — prints `1`, and `grep -c '^# Changelog\|^All notable changes to this project' CHANGELOG.md` prints `2` (the whole preamble, not one line, was left intact above the new heading).

**Gates:**

- `make test` — exits 0.
- `make precommit` — exits 0. This is the repo's DoD target and re-asserts that the preceding prompt left the tree green.
</verification>
