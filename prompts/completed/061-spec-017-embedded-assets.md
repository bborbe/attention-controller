---
status: completed
spec: [019-who-needs-me-data-endpoint]
summary: Confirmed the embedded board assets, assembly, and pinned-digest test are already present and correct; no changes needed, and make precommit passed.
execution_id: a7357e99-78ca-5254-8a83-b7f4601a0ca4-exec-061-spec-017-embedded-assets
dark-factory-version: dev
created: "2026-11-18T07:27:08Z"
queued: "2026-11-18T07:41:01Z"
started: "2026-11-18T07:41:03Z"
completed: "2026-11-18T07:44:30Z"
branch: dark-factory/who-needs-me-data-endpoint
---
<!--
OPEN QUESTIONS FOR THE AUDITOR (2026-11-18):

1. The spec's § Acceptance Criteria pins the assembled document at 121,395 bytes /
   sha256 4812c549…, but the base this prompt runs on (origin/master, v0.96.0)
   pins 146,374 bytes / sha256 0ab27da0… in
   pkg/handler/attention-page-template_test.go. The spec's numbers are stale: the
   board grew after the spec was re-pinned. This prompt therefore does NOT assert
   the spec's literal digest; it requires the existing pinned test to pass
   unchanged (or, when the test is absent, pins the digest of the document the
   extraction actually produced). Confirm the intended baseline before approving.

2. The extraction already appears to be present on origin/master (commit
   955067a "Move the board's document and script into embedded assets", merged
   into v0.96.0). The spec's § Verification compares against an origin/master
   that still carries the raw-string literal, which is no longer true. This prompt
   is therefore state-aware: it performs the extraction only when the literal is
   still present, and otherwise verifies the end state and changes nothing.
-->

---
spec: ["017-embedded-board-assets"]
status: draft
created: "2026-11-18T08:00:00Z"
---

# Move the board's document and script into embedded assets

<summary>
- The board's HTML document and its inline JavaScript live in their own asset files instead of a Go raw-string literal.
- Both assets are compiled into the binary, so deployment is unchanged: one artefact, no mount, no runtime file read, no new flag.
- The served document is assembled from the two assets plus the existing stylesheet constant before the template engine parses it.
- The served bytes are unchanged; a pinned digest and a before/after reconstruction prove it.
- The handler file is a handler again: no served bytes, and well under the lint limit.
- A missing or duplicated placeholder stops the process at startup with a message naming the placeholder, instead of serving an empty stylesheet or script.
- No markup byte, script byte, template action or browser case changes.
- If the split is already present on the base, the prompt verifies it and makes no change.
</summary>

<objective>
Ensure the attention board's document and inline script are separate embedded assets assembled with the existing stylesheet constant at package init, before `html/template` parses the result, so the served page is byte-identical while `pkg/handler/attention-page.go` carries no served bytes.
</objective>

<context>
There is no root `CLAUDE.md` in this repo. `docs/dod.md` is the Definition of Done this prompt is validated against.

Read these before changing anything:

- `specs/in-progress/017-embedded-board-assets.md` — the spec. Read § Goal, § Non-goals, § Acceptance Criteria, § Desired Behavior, § Constraints and § Failure Modes. The byte-identity rule and the two frozen placeholders are the whole contract.
- `pkg/handler/attention-page.go` — `NewAttentionPageHandler`. On a pre-extraction base this file also holds `const attentionPageTemplate` (its doc comment starts `// attentionPageTemplate is the whole page:`), the `<style>` seam and the `<script>` … `</script>` block. On the current base the literal is already gone and the file is 182 lines.
- `pkg/handler/attention-page-helpers.go` — `const attentionPageStyles` (the stylesheet). It stays in Go; its comment must not be edited.
- `pkg/handler/attention-page-template.go` — `newAttentionPageTemplate`, `attentionPageTemplate`, `buildAttentionPage`, `replaceAttentionPagePlaceholder`, and the two placeholder constants. On the current base all of these exist.
- `pkg/handler/static_embed.go` — `//go:embed static`, `attentionPageStatic`, `mustReadAttentionPageAsset`.
- `pkg/handler/static/attention-page.html` and `pkg/handler/static/attention-page.js` — the two assets on the current base.
- `pkg/handler/attention-page-template_test.go` — the assembly specs and the pinned digest on the current base.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-package-layout-guide.md` — package/file layout conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — doc-comment placement for the moved Go comment.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — the Ginkgo/Gomega conventions this package uses.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — what `make precommit` runs.

⚠️ The runner is started with `.git` masked (`workflow: worktree`). Do NOT run any `git` command in this container: a bare `git` dies with `fatal: not a git repository`, and the daemon does not check verification exit codes, so a git-based check ships as a silent false positive. The spec's comparison against `origin/master` is operator-side and is replaced here by a before/after reconstruction on the working tree, which proves the same thing about the assembled bytes.
</context>

<requirements>
1. **Determine the base state before editing.** Run:

   ```bash
   grep -c 'const attentionPageTemplate = `' pkg/handler/attention-page.go
   ```

   - If it prints `1`, the raw-string literal is still present: follow **Path A** (requirements 2–5).
   - If it prints `0` (or exits 1 because the count is zero), the split is already present: follow **Path B** (requirement 6).
   - Do not mix the two paths.

2. **Path A — snapshot the pre-change served document, then extract the two assets.** Run this from the repository root while the literal still exists. It reconstructs the exact document the board served before the change and writes it to `/tmp/attention-page-pre.txt`:

   ```bash
   python3 - <<'PY'
   src = open('pkg/handler/attention-page.go', encoding='utf-8').read()
   helpers = open('pkg/handler/attention-page-helpers.go', encoding='utf-8').read()

   TEMPLATE_CONST = "const attentionPageTemplate = `"
   STYLES_JOIN = "` + attentionPageStyles + `"
   STYLES_CONST = "const attentionPageStyles = `"

   start = src.index(TEMPLATE_CONST) + len(TEMPLATE_CONST)
   head_end = src.index("`", start)
   head = src[start:head_end]
   assert src[head_end:].startswith(STYLES_JOIN), "template is not head + styles + tail"
   tail_start = head_end + len(STYLES_JOIN)
   tail = src[tail_start:src.index("`", tail_start)]

   sstart = helpers.index(STYLES_CONST) + len(STYLES_CONST)
   styles = helpers[sstart:helpers.index("`", sstart)]

   doc = head + styles + tail
   open('/tmp/attention-page-pre.txt', 'w', encoding='utf-8').write(doc)
   print('pre-change bytes:', len(doc.encode('utf-8')))
   PY
   ```

   Then extract the two assets byte-for-byte. Do not hand-edit the output:

   ```bash
   python3 - <<'PY'
   import os

   src = open('pkg/handler/attention-page.go', 'rb').read().decode('utf-8')
   helpers = open('pkg/handler/attention-page-helpers.go', 'rb').read().decode('utf-8')

   TEMPLATE_CONST = "const attentionPageTemplate = `"
   STYLES_JOIN = "` + attentionPageStyles + `"
   STYLES_MARKER = "/*__ATTENTION_PAGE_STYLES__*/"
   SCRIPT_MARKER = "/*__ATTENTION_PAGE_SCRIPT__*/"

   start = src.index(TEMPLATE_CONST) + len(TEMPLATE_CONST)
   head_end = src.index("`", start)
   head = src[start:head_end]
   assert src[head_end:].startswith(STYLES_JOIN), "template is not head + styles + tail"
   tail_start = head_end + len(STYLES_JOIN)
   tail = src[tail_start:src.index("`", tail_start)]

   script_start = tail.index("<script>") + len("<script>")
   script_end = tail.index("</script>")
   script = tail[script_start:script_end]

   html = head + STYLES_MARKER + tail[:script_start] + SCRIPT_MARKER + tail[script_end:]

   os.makedirs('pkg/handler/static', exist_ok=True)
   open('pkg/handler/static/attention-page.html', 'wb').write(html.encode('utf-8'))
   open('pkg/handler/static/attention-page.js', 'wb').write(script.encode('utf-8'))
   print('html bytes:', len(html.encode('utf-8')))
   print('js bytes:  ', len(script.encode('utf-8')))
   PY
   ```

   The HTML asset is the whole document with exactly two placeholders: `/*__ATTENTION_PAGE_STYLES__*/` inside `<style>` and `/*__ATTENTION_PAGE_SCRIPT__*/` inside `<script>`. The JS asset is the exact text between `<script>` and `</script>`. Both placeholders are comments in their own language, so the asset is parseable while unfilled.

3. **Path A — add the embed shim and the assembly.** Create `pkg/handler/static_embed.go` with the standard BSD licence header used by the other files, `package handler`, and exactly this shim. It is the only `//go:embed` directive in the package, and it names the directory rather than a file glob:

   ```go
   package handler

   import (
       "embed"
       "fmt"
       "io/fs"
   )

   // attentionPageStatic carries the board's HTML document and its inline script
   // into the binary. The directive names the directory, so the whole directory is
   // compiled in at build time: there is no runtime path, mount or flag, and no
   // file is read from disk when the process serves a page.
   //
   //go:embed static
   var attentionPageStatic embed.FS

   // mustReadAttentionPageAsset reads one embedded asset by its file name inside
   // static/. A file missing from the directory still builds — the directive embeds
   // whatever is there — so the failure is a startup panic that names the asset
   // rather than a request that serves an empty document.
   func mustReadAttentionPageAsset(name string) string {
       content, err := fs.ReadFile(attentionPageStatic, "static/"+name)
       if err != nil {
           panic(fmt.Sprintf("attention page: read embedded asset %q: %v", name, err))
       }
       return string(content)
   }
   ```

   In `pkg/handler/attention-page-template.go`, keep `newAttentionPageTemplate` and its `template.FuncMap` exactly as they are. Add the two frozen placeholder constants, the package-level assembled value, the assembly function and its placeholder helper:

   ```go
   const (
       attentionPageStylesPlaceholder = "/*__ATTENTION_PAGE_STYLES__*/"
       attentionPageScriptPlaceholder = "/*__ATTENTION_PAGE_SCRIPT__*/"
   )

   var attentionPageTemplate = buildAttentionPage(
       mustReadAttentionPageAsset("attention-page.html"),
       mustReadAttentionPageAsset("attention-page.js"),
   )

   func buildAttentionPage(html, script string) string {
       page := replaceAttentionPagePlaceholder(
           html, attentionPageStylesPlaceholder, attentionPageStyles, "stylesheet",
       )
       return replaceAttentionPagePlaceholder(
           page, attentionPageScriptPlaceholder, script, "script",
       )
   }

   func replaceAttentionPagePlaceholder(document, placeholder, value, name string) string {
       count := strings.Count(document, placeholder)
       switch count {
       case 1:
           return strings.Replace(document, placeholder, value, 1)
       case 0:
           panic(fmt.Sprintf(
               "attention page: %s placeholder %q is absent", name, placeholder,
           ))
       default:
           panic(fmt.Sprintf(
               "attention page: %s placeholder %q occurs %d times", name, placeholder, count,
           ))
       }
   }
   ```

   Add the imports this needs (`fmt`, `strings`); keep `html/template`. The substitution must be plain string replacement, never a template action: an action would put the script through `html/template`'s JS escaper and change every byte.

4. **Path A — move the document's Go doc comment and delete the literal.** The block immediately above `const attentionPageTemplate` (it starts `// attentionPageTemplate is the whole page:`) is a Go comment, not document bytes. Move it above the new `var attentionPageTemplate` and change only its first paragraph so it no longer claims the document is a string constant in `attention-page.go`. Suggested first paragraph:

   ```go
   // attentionPageTemplate is the whole page: one document, inline styles, no
   // external stylesheet, no framework, no build step. It is assembled at package
   // init from the embedded HTML asset, the embedded script asset and
   // attentionPageStyles, so the page still ships inside the binary — with no
   // runtime file read and no new deployment artefact.
   ```

   Leave every other sentence of that comment intact. Then delete the entire `attentionPageTemplate` raw-string constant from `pkg/handler/attention-page.go` (from the `const` keyword through its closing backtick), keeping the package clause, imports and `NewAttentionPageHandler`. Leave exactly one blank line between the import block and the `NewAttentionPageHandler` doc comment.

   Correct the now-false rationale comment on `newAttentionPageTemplate` (the paragraph beginning `⚠️ This lives in its own file because it owns the assembly as well as the parse:` is already the corrected form on the current base; if your base still says the file exists to stay under revive's limit, replace that paragraph with the corrected one). Do not put any Go comment into either asset.

5. **Path A — add the assembly specs and prove byte identity.**
   a. Create `pkg/handler/attention-page-template_test.go` as `package handler` (internal, because `buildAttentionPage` and the placeholder constants are unexported). Follow the conventions in `pkg/handler/handler_suite_test.go`. The file must contain these specs, written out with the placeholder strings literal (pinning them is the contract):
      - substitutes both placeholders and leaves neither behind;
      - inserts the script as literal text, tags and all;
      - panics naming the stylesheet placeholder when it is absent;
      - panics naming the stylesheet placeholder when it is duplicated (`occurs 2 times`);
      - panics naming the script placeholder when it is absent;
      - panics naming the script placeholder when it is duplicated (`occurs 2 times`).
   b. Add one spec that pins what the assembly produced:

      ```go
      sum := sha256.Sum256([]byte(attentionPageTemplate))

      Expect(len(attentionPageTemplate)).To(Equal(<LEN>))
      Expect(hex.EncodeToString(sum[:])).To(Equal(
          "<SHA256>",
      ))
      ```

      Compute `<LEN>` and `<SHA256>` from the assembled document after the extraction, using the same reconstruction as requirement 5c. Regenerate this digest only when the served page is meant to change.
   c. Prove byte identity on the working tree (no `git`): assemble from the two assets plus the stylesheet constant, strip `{{/* … */}}` template comments from both sides, and assert the result equals `/tmp/attention-page-pre.txt`:

      ```bash
      python3 - <<'PY'
      import hashlib, re

      pre = open('/tmp/attention-page-pre.txt', encoding='utf-8').read()
      html = open('pkg/handler/static/attention-page.html', encoding='utf-8').read()
      js = open('pkg/handler/static/attention-page.js', encoding='utf-8').read()
      helpers = open('pkg/handler/attention-page-helpers.go', encoding='utf-8').read()

      styles = helpers.split("const attentionPageStyles = `", 1)[1].split("`", 1)[0]
      assert html.count('/*__ATTENTION_PAGE_STYLES__*/') == 1, "stylesheet placeholder count"
      assert html.count('/*__ATTENTION_PAGE_SCRIPT__*/') == 1, "script placeholder count"

      post = html.replace('/*__ATTENTION_PAGE_STYLES__*/', styles, 1)
      post = post.replace('/*__ATTENTION_PAGE_SCRIPT__*/', js, 1)

      COMMENT = re.compile(r"\{\{/\*.*?\*/\}\}", re.S)
      assert COMMENT.sub('', pre) == COMMENT.sub('', post), "DIFFERENT: assembled document is not master's"
      print('identical:', len(post.encode('utf-8')), 'bytes served')
      print('len:', len(post.encode('utf-8')))
      print('sha256:', hashlib.sha256(post.encode('utf-8')).hexdigest())
      PY
      ```

      The check is falsifiable: a one-character edit to either asset makes it print a mismatch and exit non-zero. Confirm that once before trusting a pass, then revert the edit.

6. **Path B — verify the end state and change nothing if it is complete.** Confirm all of the following. If any element is missing or malformed, restore it to the exact shape in requirements 2–5; if everything is present, make no change to the assets, the assembly or the test.

   - `pkg/handler/static/attention-page.html` exists and contains exactly one `/*__ATTENTION_PAGE_STYLES__*/` and exactly one `/*__ATTENTION_PAGE_SCRIPT__*/`.
   - `pkg/handler/static/attention-page.js` exists and is the script text (it contains `addEventListener`).
   - `pkg/handler/static_embed.go` exists, carries `//go:embed static`, and is the only file in `pkg/handler/` with an embed directive.
   - `pkg/handler/attention-page-template.go` defines `attentionPageTemplate` as a package-level `var`, `buildAttentionPage`, and `replaceAttentionPagePlaceholder`, and the assembly panics naming the placeholder when a placeholder is absent or duplicated.
   - `pkg/handler/attention-page.go` has no `<!DOCTYPE html>`, no `addEventListener`, no `attentionPageTemplate`, and is at most 300 lines.
   - `pkg/handler/attention-page-template_test.go` contains the six assembly specs and the pinned digest, and `go test -mod=mod ./pkg/handler/` passes.
   - `attentionPageStyles` remains a constant in `pkg/handler/attention-page-helpers.go`; do not move it and do not edit its comment.

7. **Do not touch anything else.** No change to any template action, any markup, any script byte, `attention-stream.go`, `attention-page-data.go`, `attention-page-helpers.go`, `e2e/`, `scenarios/`, or `CHANGELOG.md` (the changelog is prompt 2). No reformatting, reindenting, re-wrapping or comment rewrite inside either asset.

8. Run the gates in `<verification>`.
</requirements>

<constraints>
- **The served bytes are the contract.** No reformatting, no reindenting, no re-wrapping, no comment rewrites inside the moved bytes, no added or removed template action, and no whitespace change at either seam. A single changed byte fails the reconstruction check.
- **The two placeholders are frozen** at `/*__ATTENTION_PAGE_STYLES__*/` (inside `<style>`) and `/*__ATTENTION_PAGE_SCRIPT__*/` (inside `<script>`). Do not rename them; the reconstruction substitutes by these exact strings.
- **The stylesheet stays in Go.** `attentionPageStyles` remains a constant in `attention-page-helpers.go`; do not move it into an asset and do not edit its comment.
- **Assembly must not go through the template engine.** Substitute the script by plain string replacement, never by a `{{...}}` action; an action would put it through the JS escaper and change every byte.
- **No behaviour change of any kind.** No markup, no script, no template action, no HTTP response, no route and no e2e case changes.
- **No new runtime surface.** No runtime file read, no new mount, no new flag, no new config key, no new HTTP response, and no second URL for the script. The assets are compile-time constants in the binary.
- **The failure modes are implemented, not deferred:** a wrong embed path fails `go build`; a file missing from `static/` panics at startup from `fs.ReadFile` naming the asset; a deleted or duplicated placeholder panics naming the placeholder and its count.
- **No `e2e/` or `scenarios/` edit.** `scenarios/001` and `scenarios/002` assert hard package totals that must not move.
- **No `//nolint`.** New Go files carry the standard BSD licence header.
- **No `git` commands in this container** (`.git` is masked). Do not commit — dark-factory handles git.
- Existing tests must still pass unchanged.
</constraints>

<verification>
Run from the repository root. `.git` is masked: no `git` command appears here and none may be added. Pass `ROOTDIR=/workspace` on every `make` call.

- `test "$(wc -l < pkg/handler/attention-page.go)" -le 300` — succeeds (from 1,995 on a pre-extraction base).
- `! grep -q '<!DOCTYPE html>' pkg/handler/attention-page.go` — succeeds only when the handler carries no document.
- `! grep -q 'addEventListener' pkg/handler/attention-page.go` — succeeds only when the handler carries no script.
- `! grep -q 'attentionPageTemplate' pkg/handler/attention-page.go` — succeeds only when the handler no longer declares the template.
- `grep -q 'addEventListener' pkg/handler/static/attention-page.js` — succeeds only when the script asset exists with script content.
- `test "$(grep -c '/\*__ATTENTION_PAGE_STYLES__\*/' pkg/handler/static/attention-page.html)" = 1` — exactly one stylesheet placeholder.
- `test "$(grep -c '/\*__ATTENTION_PAGE_SCRIPT__\*/' pkg/handler/static/attention-page.html)" = 1` — exactly one script placeholder.
- `test "$(grep -c '^//go:embed static$' pkg/handler/static_embed.go)" = 1` — the embed directive names the directory.
- `test "$(grep -rl '^//go:embed' pkg/handler/*.go)" = "pkg/handler/static_embed.go"` — the only embed directive in the package.
- `grep -q 'func buildAttentionPage' pkg/handler/attention-page-template.go` — succeeds.
- `grep -q 'func replaceAttentionPagePlaceholder' pkg/handler/attention-page-template.go` — succeeds.
- `grep -q 'sha256.Sum256' pkg/handler/attention-page-template_test.go` — succeeds (the pinned-digest spec exists).
- Reconstruct the assembled document from the assets and assert it matches the pin in `pkg/handler/attention-page-template_test.go`:

  ```bash
  python3 - <<'PY'
  import hashlib, re
  html = open('pkg/handler/static/attention-page.html', encoding='utf-8').read()
  js = open('pkg/handler/static/attention-page.js', encoding='utf-8').read()
  helpers = open('pkg/handler/attention-page-helpers.go', encoding='utf-8').read()
  styles = helpers.split("const attentionPageStyles = `", 1)[1].split("`", 1)[0]
  assert html.count('/*__ATTENTION_PAGE_STYLES__*/') == 1
  assert html.count('/*__ATTENTION_PAGE_SCRIPT__*/') == 1
  doc = html.replace('/*__ATTENTION_PAGE_STYLES__*/', styles, 1)
  doc = doc.replace('/*__ATTENTION_PAGE_SCRIPT__*/', js, 1)
  test = open('pkg/handler/attention-page-template_test.go', encoding='utf-8').read()
  pin = re.search(r'[0-9a-f]{64}', test).group(0)
  n = re.search(r'len\(attentionPageTemplate\)\)\.To\(Equal\((\d+)\)\)', test).group(1)
  got = hashlib.sha256(doc.encode('utf-8')).hexdigest()
  assert len(doc.encode('utf-8')) == int(n), (len(doc.encode('utf-8')), n)
  assert got == pin, (got, pin)
  print('assembled', len(doc.encode('utf-8')), 'bytes, sha256', pin)
  PY
  ```

- `go build -mod=mod ./...` — must exit 0.
- `go test -mod=mod ./pkg/handler/` — must exit 0.
- `ROOTDIR=/workspace make precommit` — **must exit 0**.

The spec's comparison against `origin/master` and the `Ran N of N Specs` browser comparison are operator-side (the container has no `.git` and no browser) and are not part of this prompt's gate.
</verification>
