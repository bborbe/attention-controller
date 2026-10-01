---
status: approved
spec: [004-decide-headless-only]
created: "2026-10-01T20:27:52Z"
queued: "2026-10-01T20:36:35Z"
branch: dark-factory/decide-headless-only
---

<summary>
- The project changelog records the headless-gate change under a new `## Unreleased` section.
- The changelog's first section is currently a released version; the new section is created above it.
- The entry is a `feat:` bullet naming what changed, in the changelog's required format.
- The full test and precommit gates are re-run as the final validation for the whole feature.
</summary>

<objective>
Record the change under a new `## Unreleased` section in `CHANGELOG.md`, and run the feature's final build and test gate. This is the release-hygiene tail after the resolver, the row gate and the e2e harness have landed.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions (absent in-repo; follow the coding plugin docs below).

Read these files before changing anything:
- `CHANGELOG.md` — its top: the intro paragraphs and the first section, which is currently `## v0.32.2` (there is **no** `## Unreleased` section today).
- `prompts/1-spec-004-resolver-headless.md`, `prompts/2-spec-004-row-gate.md`, `prompts/3-spec-004-e2e-ledger.md` — the three preceding prompts, whose changes this entry describes.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, verb style, anti-patterns, `## Unreleased` rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/git-workflow.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
1. **Create a `## Unreleased` section in `CHANGELOG.md`** immediately after the intro paragraphs and above the current first version section (`## v0.32.2`). Insert a blank line around it so the file keeps its existing formatting.

2. **Add exactly one `feat:` bullet** under `## Unreleased`, in the changelog's required format `- <prefix>: <what> [context]`. The bullet must be specific — name the control, the source and the fail-closed direction — and must contain the word `headless`. Use a bullet equivalent to:

   ```markdown
   - feat: render the board's Allow / Deny pair on a `permission` card only when the item's session is recorded as a `headless` worker in the supervisor's spawn ledger, so a tab worker's gate no longer gains a second, invisible answering surface on the board. The headless/tab determination is read from `<spawn-state-dir>/<session>.json`'s `mode` field at page-render time (once per page load, not once per card) through the new `SpawnStateDir` / `-spawn-state-dir` config, whose default resolves in-process to `~/.local/state/claude-supervisor/sessions` with no launchd plist change. The gate is fail-closed: an absent ledger directory, a missing record, an unparseable record or a `mode` outside `{headless, interactive}` all render no control while the page still returns 200. Nothing else on a permission card changes — the jump corner, the corner X, the state line and the provenance line are unchanged on both the headless and the tab row. The e2e harness isolates its own spawn ledger and seeds the fixture session as `headless`, keeping the permission answer-shape cases green.
   ```

   One bullet per logical change — this is one change, so one bullet. Do not use the prompt filename as the entry and do not describe what you verified.

3. **Run the feature's final gate.** Run `make test` and then `make precommit` once, after the changelog edit.
4. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes — in particular that the placement check exits 0 and names `## Unreleased`, not a released heading. Walk spec AC5 and AC7 against the change. Do not report success on a command you did not run.
</requirements>

<constraints>
- ⚠️ **Two independent release settings, and they point opposite ways.** `.dark-factory.yaml`'s `autoRelease: false` means dark-factory itself does not bump `## Unreleased` → `## vX.Y.Z` or tag — it commits only. But `.maintainer.yaml`'s `release.autoRelease: true` means the github-releaser agent **will** rename `## Unreleased` → `## vX.Y.Z`, commit, tag and push after this PR merges. Create the section as written and add **no** version heading yourself; the rename is the releaser's job, not yours.
- ⚠️ If a `## Unreleased` section already exists (it does not today), append to it rather than creating a second one.
- Do NOT copy bash comments from any `<verification>` section into the changelog, and do NOT add a `+## vX.Y.Z` line.
- Do NOT commit — dark-factory handles git. The worktree's `.git` is masked; never run a `git` command.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the repo root:

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- `awk '/^## /{sec=$0} /headless/{print "sits under: " sec}' CHANGELOG.md` — **must exit 0** — a bullet placed under a released heading (e.g. `## v0.32.2`) makes it exit non-zero instead of merely printing a readable line the agent may not read. Also: `grep -m1 '^## ' CHANGELOG.md` prints `## Unreleased` (the section sits above `## v0.32.2`), and `! grep -q '^## v0\.32\.3' CHANGELOG.md` must exit 0 (this change introduces no version heading).
- `grep -c '^## Unreleased' CHANGELOG.md` — returns exactly 1.
</verification>
