---
status: approved
approved: "2026-11-12T22:52:23Z"
branch: dark-factory/embedded-board-assets
---

## Summary

- `pkg/handler/attention-page.go` carries the board's entire HTML document as a Go raw-string literal: 1,728 of the file's 1,995 lines, 103,042 bytes of document, of which 83,224 bytes is one inline `<script>`.
- The file sits five lines under revive's 2,000-line `file-length-limit`, so the document has nowhere to grow, and the script exists only inside a Go source file that no test can read as a script.
- Move the document into an embedded HTML asset and the script into an embedded JS asset, both compiled into the binary and assembled into one document before `html/template` parses it.
- The served page is byte-identical to today's. This is a pure move: no behaviour change of any kind.
- The shape is copied from `bborbe/vault-ui`'s `src/vault_ui/static/` + `static_embed.go`, so this repo adopts a shape a sibling already runs rather than inventing one.

## Problem

`pkg/handler/attention-page.go` is 1,995 lines because the whole board document is a single Go literal inside it. The stylesheet was already cut out into `attention-page-helpers.go` for exactly this reason, and the comment recording that decision says the file "sits at revive's 2000-line file-length limit" — five lines of headroom. Every future change to the board's markup or script therefore starts by finding somewhere else to put it, which is how a second, divergent renderer gets created. The script is also the only part of the board with no unit-test seam: it is 1,550 lines of JavaScript that exists solely as bytes inside a Go file, so the class of defect where a control's position in the DOM silently decides what a keystroke does can be caught only by a browser case written against rendered markup.

## Goal

The board's document and script each live in their own embedded asset file, compiled into the binary and assembled into one document before the template engine parses it, so the served page is byte-for-byte what it is today, `pkg/handler/attention-page.go` is a handler again rather than a container for 83 KB of JavaScript, and an editor, a linter or a test can open the script as a script.

## Non-goals

- Changing anything the board renders, sends, or does. A behaviour change bundled into this makes the "nothing regressed" assertion unverifiable, which is the whole point of the move.
- Moving the stylesheet out of Go. `attentionPageStyles` stays a constant in `attention-page-helpers.go`; whether it should become an asset is a separate decision with its own reasoning already recorded beside it.
- Serving the script as a second HTTP response. It stays inline in the document: a separate URL would add a request, a cache header and a content-type decision, none of which this change is allowed to touch.
- Adding, removing or editing any e2e case. `scenarios/001-board-browser-cases.md` and `scenarios/002-board-answer-shapes.md` each assert a hard package total, so an added or removed case moves both — and this change must not move them.
- Reformatting, reindenting, re-wrapping or "tidying" the moved bytes, and changing any template action. The document moves as bytes.
- Making the script's new file status an invitation to modernise it: the removal of the raw-string restriction is a side effect of the move, not a licence to use it. The script stays byte-identical, backtick-free though it now may use them.

## Assumptions

- The document's only two non-literal parts are the stylesheet, which is already a separate constant, and the script, which is the block between `<script>` and `</script>`. Nothing else in the literal is assembled from anywhere.
- `html/template` parses the document after assembly, so its behaviour is a function of the assembled string and not of how the string was built. This is why byte identity is a sufficient proof of unchanged behaviour at the document level.
- `//go:embed` of a directory compiles the whole directory into the binary at build time, so no runtime path, mount or flag is needed.
- The one red e2e spec was corrected on `origin/master` in `v0.72.6` (PR #196) while this change was in flight, so the browser comparison now runs against a green baseline at `57 of 57` rather than against a single known failure.
- `origin/master` is reachable from the container's clone. Where it is not, the byte-identity check fetches it first rather than skipping.

## Acceptance Criteria

- [ ] `make precommit` exits 0.
- [ ] **The served document is byte-identical to `origin/master`'s, and the old file no longer carries it** — evidence: the check in § Verification reconstructs master's document from `git show origin/master:pkg/handler/attention-page.go` plus the stylesheet constant in `attention-page-helpers.go`, reconstructs the branch's document from the two asset files with their placeholders substituted, and compares them; it prints `identical: 121395 bytes served` and exits 0. The byte arithmetic must account for every byte: master's 111,034-byte file holds a 131,964-byte document (20,175-byte HTML asset + 84,504-byte script asset + two 29-byte placeholders − 58) plus the 27,343-byte stylesheet, which stays in Go. Alongside it, `wc -l pkg/handler/attention-page.go` is **≤ 300** (from 1,995), `grep -c '<!DOCTYPE html>' pkg/handler/attention-page.go` is 0, and `grep -c 'addEventListener' pkg/handler/attention-page.go` is 0 while the same grep against `pkg/handler/static/attention-page.js` is non-zero. ⚠️ `{{/* … */}}` template comments are stripped from both sides before the comparison, because they never reach the browser; every byte that does reach the browser must match exactly.
- [ ] **Go's assembled output is the pinned document** — evidence: a spec asserts `hex.EncodeToString(sha256.Sum256([]byte(attentionPageTemplate))[:])` equals `4812c5491c68b48dbe4b47a2426833e5bd4e45ae16c05d487b701d1fa91fb731`, the 131,964-byte unstripped document. ⚠️ **This is the assertion the file-level check cannot make.** That check reads the asset files on disk and never runs the Go assembly, so an assembly that trimmed, re-encoded or dropped a trailing newline from an embedded asset would pass every other criterion while serving something master never served. The digest pins what `buildAttentionPage` actually produced.
- [ ] **Both parts are separate embedded assets in the vault-ui shape** — evidence: `pkg/handler/static/attention-page.html` and `pkg/handler/static/attention-page.js` exist, `pkg/handler/static_embed.go` carries `//go:embed static` and no other embed directive exists in the package, and `go build ./...` succeeds with no asset read at runtime.
- [ ] **The assembly cannot silently serve an empty stylesheet or an empty script** — evidence: a spec asserts `buildAttentionPage` substitutes both placeholders, panics with a message naming the placeholder when it is absent, and panics when it is duplicated; the doctored inputs are the asset strings with one placeholder removed and one duplicated.
- [ ] **The browser suite shows no new failure against pristine `origin/master`** — evidence: `go test -mod=mod -tags e2e -count=1 -v ./e2e/` run on the branch and run again in a detached `origin/master` worktree produce the **same** `Ran N of N Specs` line and the **same** set of failing spec names. ⚠️ **The baseline moved while this change was in flight.** `origin/master` is now `v0.72.6`, and PR #196 corrected the stale expectation this criterion was written around — the case is renamed `keeps an agent-kind card pushed while the page is open off the managers-only view` and the suite is **green at `57 of 57`** on master. The criterion is unchanged in kind and now reads as "both sides green, same count": it is still a comparison rather than an assertion that the suite passes, so a red suite on either side fails it, and the branch must not trade one failure for another. ⚠️ Read the count with that command, never with `make e2e`, which omits `-v` and discards Ginkgo's summary.
- [ ] **`CHANGELOG.md` carries an `## Unreleased` bullet** describing the move for a reader, since `.maintainer.yaml` sets `release.autoRelease: true` and a merge without one cuts no tag — evidence: `awk '/^## /{s=$0} /embedded asset|inline HTML|attention-page/{print s}' CHANGELOG.md` prints `## Unreleased`, and `grep -c '^## Unreleased$' CHANGELOG.md` prints `1` (not two headings).

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, lint, unit tests, licence and security checks clean
- `go test -mod=mod ./pkg/handler/` — the handler suite, including the new assembly specs, passes
- `go build -mod=mod ./...` — the embedded assets compile into the binary
- The pinned-digest assertion, which reads Go's assembled output rather than the files:

```bash
go test -mod=mod ./pkg/handler/ 2>&1 | tail -3
# the spec asserts sha256(attentionPageTemplate) ==
# 4812c5491c68b48dbe4b47a2426833e5bd4e45ae16c05d487b701d1fa91fb731
```
- The byte-identity check, from the repository root:

```bash
git show origin/master:pkg/handler/attention-page.go > /tmp/master-page.go
git show origin/master:pkg/handler/attention-page-helpers.go > /tmp/master-helpers.go
python3 - /tmp/master-page.go /tmp/master-helpers.go <<'PY'
import re, sys
TEMPLATE_CONST = "const attentionPageTemplate = `"
STYLES_JOIN = "` + attentionPageStyles + `"
STYLES_CONST = "const attentionPageStyles = `"
STYLES_MARKER = "/*__ATTENTION_PAGE_STYLES__*/"
SCRIPT_MARKER = "/*__ATTENTION_PAGE_SCRIPT__*/"
TEMPLATE_COMMENT = re.compile(r"\{\{/\*.*?\*/\}\}", re.S)

def raw_const(text, opener):
    start = text.index(opener) + len(opener)
    return text[start:text.index("`", start)]

def master_document(go_path, helpers_path):
    go_src = open(go_path, encoding="utf-8").read()
    start = go_src.index(TEMPLATE_CONST) + len(TEMPLATE_CONST)
    head_end = go_src.index("`", start)
    head = go_src[start:head_end]
    assert go_src[head_end:].startswith(STYLES_JOIN), "master is not head + styles + tail"
    tail_start = head_end + len(STYLES_JOIN)
    tail = go_src[tail_start:go_src.index("`", tail_start)]
    return head + raw_const(open(helpers_path, encoding="utf-8").read(), STYLES_CONST) + tail

def branch_document(html_path, js_path, helpers_path):
    styles = raw_const(open(helpers_path, encoding="utf-8").read(), STYLES_CONST)
    page = open(html_path, encoding="utf-8").read()
    for marker, value, name in ((STYLES_MARKER, styles, "stylesheet"),
                                (SCRIPT_MARKER, open(js_path, encoding="utf-8").read(), "script")):
        assert page.count(marker) == 1, f"{name} placeholder occurs {page.count(marker)} times"
        page = page.replace(marker, value, 1)
    return page

master = TEMPLATE_COMMENT.sub("", master_document(sys.argv[1], sys.argv[2]))
branch = TEMPLATE_COMMENT.sub("", branch_document(
    "pkg/handler/static/attention-page.html",
    "pkg/handler/static/attention-page.js",
    "pkg/handler/attention-page-helpers.go"))
if master != branch:
    import difflib
    sys.stdout.writelines(list(difflib.unified_diff(
        master.splitlines(True), branch.splitlines(True), "origin/master", "branch", n=2))[:80])
    sys.exit(f"DIFFERENT: master {len(master)} bytes, branch {len(branch)} bytes")
print(f"identical: {len(master)} bytes served")
PY
```

  ⚠️ The comparison is only meaningful against a fetched `origin/master`; run `git fetch origin` first if the ref is absent. ⚠️ The check is falsifiable: a one-character edit to the script asset prints a unified diff and exits non-zero — confirm that once before trusting a pass.

### Operator-executable (the pull-request gate, not runnable in the container)

`e2e/` is build-tagged and excluded from `make precommit`, and the YOLO container has no browser, so the suite is run where Chromium is installed:

```bash
# branch
go test -mod=mod -tags e2e -count=1 -v ./e2e/
# pristine master, in a detached worktree
git worktree add /tmp/ac-master origin/master
cd /tmp/ac-master && go test -mod=mod -tags e2e -count=1 -v ./e2e/
```

Observable: both runs print the same `Ran N of N Specs` line, and the failing spec names are identical, with `keeps a session-kind card pushed while the page is open off the managers-only view` named in the pull request body as pre-existing on master.

## Desired Behavior

1. The board's document is produced by one assembly step from two embedded files plus the existing stylesheet constant, and the bytes it produces are exactly the bytes the single-literal form produced.
2. The HTML asset is the whole document — head, body, both sub-templates and every template action — and it carries exactly two placeholders: one inside `<style>` where the stylesheet goes, one inside `<script>` where the script goes.
3. The script asset is the whole inline script as a standalone `.js` file, byte-identical to the text that sits between `<script>` and `</script>` today.
4. Assembly happens before `html/template` parses the document, so the engine still sees one document: CSS comments are still stripped in the style context, template actions are still escaped in their own contexts, and the script is not handed to the JS escaper. The placeholders are inert either way — neither `/*__ATTENTION_PAGE_STYLES__*/` nor `/*__ATTENTION_PAGE_SCRIPT__*/` contains `{{`, so the parser would read both as ordinary text if a substitution were ever skipped; the panic is what stops that from being served.
5. A missing or duplicated placeholder stops the process at startup with a message naming the placeholder, rather than serving a page whose stylesheet or script came out empty.
6. Both assets are compiled into the binary. The deployment is unchanged: one artefact, no new mount, no file read at runtime, no new flag.
7. `pkg/handler/attention-page.go` becomes a handler again — its remaining content is the handler, its documentation and its data types, with no served bytes in it.

## Constraints

- **The served bytes are the contract.** No reformatting, no reindenting, no re-wrapping, no comment rewrites inside the moved bytes, no added or removed template action, no whitespace change at either seam. A single changed byte fails the byte-identity check, which is what makes "nothing regressed" verifiable at all.
- **The two placeholders are frozen** at `/*__ATTENTION_PAGE_STYLES__*/` (inside `<style>`) and `/*__ATTENTION_PAGE_SCRIPT__*/` (inside `<script>`). Each is a comment in the language of its context, so the asset stays parseable while unfilled, and each name is what the verification command substitutes by. Renaming them breaks the check, so they are part of the contract rather than an internal detail.
- **The vault-ui shape is matched, not approximated:** `//go:embed static` — the directive names a directory, not a file glob — with plainly-named lowercase assets inside it, and the shim in its own file beside the directory. `bborbe/vault-ui`'s reference is `src/vault_ui/static_embed.go` + `src/vault_ui/static/{index.html,app.js,style.css}`. The one divergence, stated in the pull request body: vault-ui's shim is a separate package (`staticui`) because a second package (`pkg/board/jsasset`) consumes it, while here the only consumer is `handler`, so the shim and its `embed.FS` live in `handler` and stay unexported.
- **The stylesheet stays in Go.** `attentionPageStyles` remains a constant in `attention-page-helpers.go`; the HTML asset holds a placeholder for it rather than the CSS itself. Moving it is a separate decision.
- **Assembly must not go through the template engine.** Substituting the script by way of a template action would put it through html/template's JS escaper and change every byte of it.
- **`pkg/handler/attention-page-template.go`'s own comment is corrected in this change**, because it explains that it lives in its own file to keep `attention-page.go` under revive's limit — a reason that stops being true here. It is a Go comment outside the moved bytes, so correcting it cannot affect the served document. The same latitude does not extend into the assets: a comment inside the moved bytes stays as it is.
- **The handler suite must keep passing unchanged.** The existing specs render pages and assert their content; they are the behavioural lock the byte-identity check cannot provide, since byte identity is about the document's template, not about what a render produces from it.
- **No e2e case is added, removed or edited**, so the package total in `scenarios/001` and `scenarios/002` does not move.
- `make precommit` is the gate; a bare `go build ./...` is not evidence.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A placeholder is deleted or duplicated while editing an asset | `buildAttentionPage` panics at startup with a message naming the placeholder and its count | Restore the placeholder; the panic names it, so the fix is one edit |
| An asset path in the embed directive is wrong | `go build` fails with an embed error rather than the binary starting without an asset | Fix the path; the embed error names the missing file |
| A file is missing *inside* `static/` while the directory itself exists | The build succeeds — `//go:embed static` embeds whatever is there — and the process panics at startup from `fs.ReadFile` with the asset name | Add the file back; the panic names it, and `go test ./pkg/handler/` fails first |
| The e2e failing-spec set differs between the two runs because of flakiness rather than a change | The comparison cannot distinguish a regression from a flake | Re-run both sides once; if the sets still differ, name the spec that differs rather than reporting a match |
| A byte of the moved document changes during the move | The byte-identity check prints a unified diff and exits non-zero | Diff the two sides and restore the byte; do not "fix" the check |
| Someone reformats the script while moving it | Same as above — the check fails on the first changed byte | Revert the reformatting; a formatting change is a separate change |
| The handler suite fails after the move | The assembly produced a different document than the engine was parsing before | Compare the assembled document against master's with the check before reading any test failure as a behaviour change |
| The e2e total moves | A behaviour or a case changed, either of which is out of scope | Revert the cause; the total must match `origin/master`'s |
| The `origin/master` ref is absent in the container | The byte-identity check cannot run | `git fetch origin` first; a check that cannot reach master proves nothing |

## Security / Abuse Cases

The change adds no input surface: no HTTP parameter, no request-derived path and no runtime file read. Both assets are compile-time constants embedded in the binary, so there is nothing to write to, and the embed directive names one directory in the repository. The document still passes through `html/template`, and the escaping that protects card text — `renderCardText`'s scheme check and the engine's contextual escaping — is unchanged, because the assembly happens before the parse rather than after the render. The one property worth stating: the script is inserted verbatim into the `<script>` context, which is exactly what happens today, so no new script-injection path is created; the script's own content is repository source, not operator or producer data.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Split the document into `static/attention-page.html` + `static/attention-page.js`, add the `//go:embed static` shim and the assembly with its placeholder panics, wire `attention-page-template.go` to the assembled value, and correct that file's now-false rationale comment | 1, 2, 3, 4, 5, 6, 7 | 1, 2, 4 | — |
| 2 | The assembly specs: both placeholders substituted, panic on absent, panic on duplicated, embedded script served verbatim, and the pinned SHA-256 of the assembled document | 5 | 3, 5 | prompt 1 |
| 3 | `CHANGELOG.md` `## Unreleased` bullet | — | 7 | prompt 1 |

Rationale: prompt 1 is the move and must land as one commit so the byte-identity check has a single tree to run against; prompt 2 depends on the assembly existing and locks the failure the panic exists for; prompt 3 is the release-hygiene tail. The e2e comparison (AC6) is the pull-request gate and is owned by no prompt, because the container cannot run a browser suite.

## Do-Nothing Option

`pkg/handler/attention-page.go` stays at 1,995 lines against revive's 2,000-line limit, so the document cannot grow by even a few lines without another extraction being invented on the spot — which is how the stylesheet ended up in a second file with a comment explaining that it had nowhere else to go. The script stays unreadable as a script: 1,550 lines that only exist as bytes inside a Go file, so no test can open it, and every defect in it is reachable only through a browser case written against rendered markup. The cost is paid on every subsequent change to the board, and it is paid in exactly the currency that produced the last one.

## Related

- Task file: `Extract the Attention Board's Inline HTML and JS into Embedded Assets` (vault `private-personal`)
- Goal: `Every Attention Gate Reaches the Board Within Seconds and Every Answer Is Delivered`
- Shape reference: `bborbe/vault-ui` — `src/vault_ui/static_embed.go`, `src/vault_ui/static/{index.html,app.js,style.css}`, and `pkg/board/jsasset/jsasset.go`, which reads the embedded tree through `fs.Sub` + `fs.ReadFile`
- Browser baseline: `origin/master` `v0.72.6`, whose PR #196 renamed `keeps a session-kind card pushed while the page is open off the managers-only view` to `keeps an agent-kind card pushed …` and made the suite green at `57 of 57`
- Release path and the red-suite rule: `docs/release-process.md`
