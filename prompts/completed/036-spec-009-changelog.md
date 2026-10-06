---
status: completed
spec: [009-incremental-provenance-reads]
summary: 'Added an ## Unreleased section to CHANGELOG.md with two perf: bullets recording the incremental event-log byte-offset tail and the fsnotify-backed task-index watcher, and make precommit passed'
execution_id: attention-controller-incremental-reads-exec-036-spec-009-changelog
dark-factory-version: v0.196.0
created: "2026-10-06T07:55:00Z"
queued: "2026-10-06T08:57:54Z"
started: "2026-10-06T08:57:56Z"
completed: "2026-10-06T09:01:38Z"
branch: dark-factory/incremental-provenance-reads
---

# Changelog entry for the incremental provenance reads

<summary>
- The changelog records that a board render no longer re-scans every producer's event log
- The changelog records that the vault task index is kept current by a watcher instead of a per-lookup timer
- The entry names the new dependency and the new mechanism, so a reader can tell what changed without reading the diff
- There is exactly one `## Unreleased` section and it sits above the newest released version
- `make precommit` passes
</summary>

<objective>
Record this spec's change in `CHANGELOG.md` so the release notes name what changed: the per-producer event-log read became an incremental byte-offset tail, and the vault task index stopped being re-read on a two-second timer in favour of a filesystem watcher with a minutes-scale backstop. This is the last prompt of the spec — the three implementation prompts deliberately add no changelog entry.
</objective>

<context>
Read `README.md` for the project's structure (this repo has no `CLAUDE.md`).

Read `CHANGELOG.md` — the preamble, and the newest section, which is currently `## v0.42.0`. There is NO `## Unreleased` section today, so this prompt creates it.

Read the coding-plugin guide `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` for the entry format and the prefix rules.

Read `pkg/event-log-reader.go`, `pkg/provenance.go` (`NewProvenanceResolver`, `readEvents`, the `taskIndex` rebuild and backstop), `pkg/task-index-watcher.go` and `main.go` — the change this entry describes. Describe what was IMPLEMENTED, not what was verified.

Read `prompts/completed/029-spec-007-changelog.md` and the `v0.39.3` / `v0.40.1` entries in `CHANGELOG.md` — the house style for a change of this shape.
</context>

<requirements>
1. In `CHANGELOG.md`, insert a `## Unreleased` section immediately after the preamble's last line (`* PATCH version when you make backwards-compatible bug fixes.`) and directly above `## v0.42.0`. ⚠️ Never move, delete or insert anything above or inside the preamble — the `# Changelog` title, the "All notable changes…" line and the MAJOR/MINOR/PATCH bullets are frozen. There must be exactly ONE `## Unreleased` section; if one already exists, append to it rather than adding a second.

2. Add one `- perf:` bullet per logical change — two bullets:
   - The per-producer event-log read: it now remembers a byte offset per producer and reads only the bytes appended since it, so a render over an unchanged log performs no read at all; a just-appended event still resolves on the first render after it lands because the offset advances by reading rather than by a notification; a torn final line is left unconsumed and resolves exactly once when it completes; and a log that shrank below the remembered offset is re-read from the start. Name the injectable seam the read sits behind (`pkg/event-log-reader.go`) and the fact that the resolver no longer holds the state directory itself.
   - The vault task index: it is kept current by a filesystem watcher on the vault's task directory (`pkg/task-index-watcher.go`, new direct dependency `github.com/fsnotify/fsnotify`) that rebuilds on change, with the two-second per-lookup window replaced by a five-minute safety-net rescan so a missed watcher event self-heals. Note that the watcher is started and stopped with the service, that a watcher that cannot be established logs a warning and leaves the service serving, and that the index's soft-failure rules are unchanged.

3. Keep the house style: one bullet per logical change (not per file), a recognised conventional prefix (`perf:`), specific names of types, packages and mechanisms, and no date suffix. Do NOT copy any comment from a prompt's `<verification>` block, do NOT use a prompt filename as an entry, and do NOT describe what was verified — describe what was implemented.

4. Do NOT edit any Go file. This prompt changes `CHANGELOG.md` only.

5. Before finishing, re-run `<verification>` and confirm it passes, then walk each requirement above against the change and confirm it holds.
</requirements>

<constraints>
- The changelog preamble is frozen: nothing is inserted above or inside the `# Changelog` title and its SemVer bullets.
- Newest first: `## Unreleased` goes directly above the highest `## vX.Y.Z` section.
- Flat list under the section — no `### Added` / `### Fixed` categories.
- Never a second `## Unreleased`.
- Do NOT commit — dark-factory handles git.
- Do NOT edit any Go file; this prompt is the changelog only.
</constraints>

<verification>
`make precommit` — must pass.

`test "$(grep -c '^## Unreleased' CHANGELOG.md)" -eq 1` — exactly one `## Unreleased` section.

`awk '/^## /{sec=$0} sec=="## Unreleased" && /^- / && !/^- (feat|fix|refactor|test|docs|chore|perf): /{print "MISSING PREFIX: " $0; bad=1} END{print "ok"; exit bad}' CHANGELOG.md` — must print `ok` and no `MISSING PREFIX:` line.

`awk '/^## /{print; exit}' CHANGELOG.md` — must print `## Unreleased`, i.e. the section sits above the newest released version. ⚠️ `.maintainer.yaml` sets `release.autoRelease: true`, so once this branch merges the releaser renames `## Unreleased` → `## vX.Y.Z`; if this check prints a `## vX.Y.Z` heading it means the release already fired, not that the edit was misplaced.
</verification>
