# Release Process

How a change reaches a `vX.Y.Z` tag in this repo, and the gate that stands between a pull request and that tag.

## The release is a side effect of the merge

`.maintainer.yaml` sets `release.autoRelease: true`. When a commit lands on `master` carrying a non-empty `## Unreleased` section in `CHANGELOG.md`, the `github-release` watcher emits a release task and `github-releaser-agent` rewrites `## Unreleased` → `## vX.Y.Z`, commits, tags and pushes. There is no manual release step and no human in the loop after the merge button.

That is the whole reason the gate sits at the pull request: **the merge is the last moment a person is involved.** Anything not checked before the merge is not checked at all.

Do not rename `## Unreleased` → `## vX.Y.Z`, do not create a local tag, and do not push a release commit — the bot owns the release commit, and a local copy of it races the bot for the same version number.

## The release gate: run `make e2e` before you merge

`make precommit` is necessary but not sufficient. It runs format, lint, unit tests and the licence check; it does **not** run the browser suite, and it cannot catch the class of defect this repo keeps producing — post-load behaviour in a real browser, where a toggle disappears, hide defaults the wrong way, or a submit button sends the wrong answer shape.

The rule: **run `make e2e` and get a green suite before you open a pull request for merge.** The suite is the only thing between your branch and a tag.

```bash
make e2e
```

`make e2e` installs the Playwright Chromium build and runs `go test -tags e2e ./e2e/`. It builds its own binary, serves it on a random port against a temp `DATADIR` and a temp session registry, and stubs the tts endpoint. It never contacts the launchd service on `:18080` — see [[Never Drive a Verification Browser Against the Live Board]] for why that matters, and for the fixture rules (temp datadir, a port other than `18080`, `-sessions-dir`) to follow if you run the suite by hand.

The scenarios in `scenarios/` describe the cases the suite asserts:

- `scenarios/001-board-browser-cases.md` — the board's post-load behaviour
- `scenarios/002-board-answer-shapes.md` — what each answer shape stores

## When the suite is red

**A red suite stops the merge.** Fix the regression, rerun, and merge only when the suite is green. "I will fix it in a follow-up" is not a merge reason: the merge cuts the tag, and the tag is what ships.

If a spec was **already red on `master` before your change**, name it in the pull request body — the spec name and the evidence that it fails without your diff. A pre-existing failure you name is a known defect; one you stay quiet about is a green suite that isn't.

As of 2026-11-12 `master` carries one such failure: `keeps a session-kind card pushed while the page is open off the managers-only view`. Name it, do not silently absorb it, and do not use it as cover for a second failure.

## Reading the spec count

`make e2e` prints no Ginkgo summary — the `e2e` target runs without `-v`, and `go test` discards a passing package's stdout. So a count assertion is read from the verbose command instead:

```bash
go test -mod=mod -tags e2e -count=1 -v ./e2e/
```

That command's `Ran N of N Specs` line is the number to compare against `scenarios/001` and `scenarios/002`. Do not read the count off `make e2e`, and do not increment a total by hand — re-derive it from the suite's own line.

## Shipping

1. Run `make e2e` — green.
2. Add a `## Unreleased` bullet to `CHANGELOG.md` describing the change for a reader, not the commit log. Without the bullet the watcher emits no release task and no tag is cut.
3. Open the pull request, get bot review, merge on green.
4. Confirm the tag contains the merge: `git fetch --tags && git tag --contains <merge-sha>`.
5. Confirm it is on `master`, not only on your branch: `git describe --tags --abbrev=0`.

## See also

- `docs/dod.md` — the general definition of done
- `scenarios/001-board-browser-cases.md`, `scenarios/002-board-answer-shapes.md` — the cases the gate runs
- `.maintainer.yaml` — `release.autoRelease`, which makes the merge the release
