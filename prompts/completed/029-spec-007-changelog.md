---
status: completed
spec: [007-production-touching-exclusion]
summary: 'Appended the production-touching exclusion''s feat bullet under the existing ## Unreleased section in CHANGELOG.md and passed make test plus make precommit'
execution_id: attention-controller-production-touching-exec-029-spec-007-changelog
dark-factory-version: v0.196.0
created: "2026-10-03T23:52:00Z"
queued: "2026-10-03T22:02:33Z"
started: "2026-10-03T22:17:18Z"
completed: "2026-10-03T22:21:03Z"
branch: dark-factory/production-touching-exclusion
---

<summary>
- The project changelog records the production-touching exclusion as a feature entry.
- The entry sits under the existing unreleased section, above the released versions.
- No version heading is added by hand — the released section stays the releaser's job.
- The whole feature's test and precommit gates are re-run as the final validation.
- The entry uses the `feat:` prefix, so the release's version bump is detected automatically.
</summary>

<objective>
Record the production-touching exclusion under `## Unreleased` in `CHANGELOG.md`, and run the feature's final build and test gate after the resolver change and the row gate have landed.
</objective>

<context>
Follow the coding plugin docs below for project conventions (this repo carries no in-repo root `CLAUDE.md`).

Read these files before changing anything:
- `CHANGELOG.md` — its top. ⚠️ A `## Unreleased` section **already exists** (created with this spec, holding a `docs:` bullet that records `docs/production-touching-marker.md`). The released section below it is `## v0.37.0`. Do not create a second unreleased section.
- `prompts/1-spec-007-marker-detection.md` and `prompts/2-spec-007-row-gate.md` — the two preceding prompts, whose changes this entry describes.
- `.maintainer.yaml` — read the `release.autoRelease: true` comment.
- `.dark-factory.yaml` — read `autoRelease: false`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, prefix rules, verb style, anti-patterns, `## Unreleased` rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/git-workflow.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>
1. **Append exactly one `feat:` bullet to the existing `## Unreleased` section** in `CHANGELOG.md`, after the existing `docs:` bullet and before the `## v0.37.0` heading. Keep the section's blank-line formatting. Use a bullet equivalent to:

   ```markdown
   - feat: withhold the board's Allow / Deny pair from a headless worker's `permission` park when the item's own task file declares a production-touching step — the operator's authored `- [ ] ⚠️ production-touching — <command>` marker on one of the task's subtask lines. The marker is matched in list-item form only (`^\s*-\s*\[[ x/]\]\s*(?:⚠️?)?\s*production-touching\s*—`), so the phrase in ordinary prose — an Impact, Success Criteria or Progress entry, or a non-checkbox bullet — never removes a control. It is read from the same `25 Tasks/*.md` file the resolver already opens whole at index-build time and carried as `Provenance.ProductionTouching`, a control gate deliberately excluded from `Resolved()` exactly as `Headless` is; no new file read, no new directory scan, nothing stored on the item. The exclusion is fail-OPEN — an absent, unreadable or unparsable task file, an unmatched session and an absent marker all render the pair — the opposite polarity to the fail-closed headless gate. The reading is task-level, so every park a marked task raises loses the pair, benign ones included. `docs/production-touching-marker.md` records the convention.
   ```

   One bullet per logical change — this is one change, so one bullet. Do not use the prompt filename as the entry, do not describe what you verified, and do not copy bash comments out of any `<verification>` section.

2. **Run the feature's final gate.** After the changelog edit, run `make test` and then `make precommit` once.
3. **Self-check before finishing:** re-run every `<verification>` command and confirm each passes — in particular that the placement check prints a line for the **`- feat:`** bullet naming `## Unreleased`, and not a released heading. Do not report success on a command you did not run.
</requirements>

<constraints>
- ⚠️ **Two independent release settings, and they point opposite ways.** `.dark-factory.yaml`'s `autoRelease: false` means dark-factory itself does not bump `## Unreleased` → `## vX.Y.Z` or tag — it commits only. But `.maintainer.yaml`'s `release.autoRelease: true` means the github-releaser agent **will** rename `## Unreleased` → `## vX.Y.Z`, commit, tag and push once master moves with a non-empty `## Unreleased`. ⚠️ Read this prompt's placement check **before the releaser fires**, or it will print `## vX.Y.Z` and appear to fail while being correct. Add **no** version heading yourself — the rename is the releaser's job.
- ⚠️ Append to the existing `## Unreleased` section; do NOT create a second one.
- Do NOT add a `+## vX.Y.Z` line, and do NOT restructure or reorder the released sections.
- Do NOT commit — dark-factory handles git. The container's `.git` is masked; never run a `git` command.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the repo root (`/workspace`):

- `make test` — must exit 0.
- `make precommit` — must exit 0.
- `awk '/^## /{sec=$0} /^- feat: .*production-touching/{print "sits under: " sec}' CHANGELOG.md` — prints **at least one** line, and every printed line names `## Unreleased` (never `## v0.37.0` or any other released heading). ⚠️ The pattern keys on the `- feat:` prefix, not the bare phrase: `CHANGELOG.md` already carries a `- docs:` bullet containing `production-touching` under `## Unreleased`, so a phrase-only pattern prints a line on the **un-edited** file and cannot prove this prompt's bullet landed — a no-op run would pass the whole verification block.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints exactly `1`.
- `grep -m1 '^## ' CHANGELOG.md` — prints `## Unreleased` (the section sits above `## v0.37.0`).
- `grep -c '^## v' CHANGELOG.md` — the count is the same as it was before the edit; record both numbers. ⚠️ The spec's own check for this is a diff against origin/master showing no `+## vX.Y.Z` line, but the container's `.git` is masked so that check cannot run here — the unchanged `^## v` count is the non-git equivalent.
</verification>
