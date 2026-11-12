---
status: completed
spec: [017-embedded-board-assets]
summary: Moved the board's HTML document and inline script into embedded assets (pkg/handler/static/attention-page.html and .js) with a //go:embed shim and init-time assembly, keeping the served page byte-identical (pinned sha256 e919452e…a86a) and shrinking attention-page.go from 1,995 to 177 lines.
execution_id: 3598a026-320d-54d6-9763-7f081bbe3f48-exec-054-spec-017-extract-board-assets
dark-factory-version: dev
created: "2026-11-12T23:04:36Z"
queued: "2026-11-12T23:19:16Z"
started: "2026-11-12T23:19:19Z"
completed: "2026-11-12T23:34:43Z"
branch: dark-factory/embedded-board-assets
---

# Move the board's document and script into embedded assets

<summary>
- The board's HTML document and its inline JavaScript move out of `pkg/handler/attention-page.go` and into two asset files, `pkg/handler/static/attention-page.html` and `pkg/handler/static/attention-page.js`.
- Both assets are compiled into the binary with `//go:embed`, so deployment is unchanged: one artefact, no new mount, no runtime file read, no new flag.
- The served document is assembled from the two assets plus the existing stylesheet constant before `html/template` parses it, so the engine still sees one document.
- The served page is byte-for-byte what it is today; a pinned SHA-256 and a reconstruction check prove it.
- A missing or duplicated placeholder stops the process at startup with a message naming the placeholder, instead of serving a page with an empty stylesheet or script.
- The handler file drops from 1,995 lines to under 300 and carries no served bytes.
- The stylesheet stays a Go constant in `attention-page-helpers.go`; only the document and the script move.
- The comment that explained why the template lived in its own file is corrected, since its stated reason stops being true.
- No template action, no markup byte, no script byte and no e2e case changes.
</summary>

<objective>
Move the attention board's document and inline script into two embedded asset files, assemble them with the existing stylesheet constant at package init before `html/template` parses the result, and leave the served page byte-identical while `pkg/handler/attention-page.go` becomes a handler again.
</objective>

<context>
There is no root `CLAUDE.md` in this repo. `docs/dod.md` is the Definition of Done this prompt is validated against.

Read these files before changing anything:

- `specs/in-progress/017-embedded-board-assets.md` — the spec. Read § Goal, § Non-goals, § Acceptance Criteria, § Desired Behavior, § Constraints and § Failure Modes. The byte-identity rule and the frozen placeholders are the whole contract.
- `pkg/handler/attention-page.go` — the `const attentionPageTemplate` literal (its doc comment starts `// attentionPageTemplate is the whole page:`), the `<style>` seam and the `<script>` … `</script>` block inside it. This is the source of the bytes; read it before running the extraction.
- `pkg/handler/attention-page-helpers.go` — `const attentionPageStyles`. It stays in Go and is not moved. The comment above it explaining why the stylesheet is a const rather than an embed remains true and must not be edited.
- `pkg/handler/attention-page-template.go` — `newAttentionPageTemplate`. It parses `attentionPageTemplate`; its rationale comment (the paragraph beginning `⚠️ This lives in its own file`) is now false and is corrected by this prompt.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-package-layout-guide.md` — package/file layout conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — doc-comment placement for the moved Go comment.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — what `make precommit` runs.

⚠️ The runner is started with `.git` masked (`workflow: worktree`). Do NOT run any `git` command in this container; a bare `git` dies with `fatal: not a git repository`, and the daemon does not check verification exit codes, so a git-based check ships as a silent false positive. The spec's comparison against `origin/master` is operator-side and is replaced in this prompt by the pinned-digest reconstruction check below, which proves the same thing about the assembled bytes.
</context>

<requirements>
1. **Extract the two assets first, while the template literal still exists.** Run this from the repository root. It reads `pkg/handler/attention-page.go` and `pkg/handler/attention-page-helpers.go` as raw UTF-8, splits the literal at the two seams, and writes the two assets byte-for-byte. Do not hand-edit the output; this script is the byte-identity guarantee.

   ```bash
   python3 - <<'PY'
   import os

   src = open('pkg/handler/attention-page.go', 'rb').read().decode('utf-8')
   helpers = open('pkg/handler/attention-page-helpers.go', 'rb').read().decode('utf-8')

   TEMPLATE_CONST = "const attentionPageTemplate = `"
   STYLES_JOIN = "` + attentionPageStyles + `"
   STYLES_CONST = "const attentionPageStyles = `"
   STYLES_MARKER = "/*__ATTENTION_PAGE_STYLES__*/"
   SCRIPT_MARKER = "/*__ATTENTION_PAGE_SCRIPT__*/"

   start = src.index(TEMPLATE_CONST) + len(TEMPLATE_CONST)
   head_end = src.index("`", start)
   head = src[start:head_end]
   assert src[head_end:].startswith(STYLES_JOIN), "template is not head + styles + tail"
   tail_start = head_end + len(STYLES_JOIN)
   tail = src[tail_start:src.index("`", tail_start)]

   styles_start = helpers.index(STYLES_CONST) + len(STYLES_CONST)
   styles = helpers[styles_start:helpers.index("`", styles_start)]

   script_start = tail.index("<script>") + len("<script>")
   script_end = tail.index("</script>")
   script = tail[script_start:script_end]

   html = head + STYLES_MARKER + tail[:script_start] + SCRIPT_MARKER + tail[script_end:]

   os.makedirs('pkg/handler/static', exist_ok=True)
   open('pkg/handler/static/attention-page.html', 'wb').write(html.encode('utf-8'))
   open('pkg/handler/static/attention-page.js', 'wb').write(script.encode('utf-8'))

   print('html bytes:', len(html.encode('utf-8')))
   print('js bytes:  ', len(script.encode('utf-8')))
   print('styles chars:', len(styles))
   PY
   ```

   Expected output: `html bytes: 19876`, `js bytes:   83224`, `styles chars: 27125`. Confirm with `wc -c`:
   - `pkg/handler/static/attention-page.html` is exactly **19876** bytes
   - `pkg/handler/static/attention-page.js` is exactly **83224** bytes

   The HTML asset is the whole document with exactly two placeholders: `/*__ATTENTION_PAGE_STYLES__*/` immediately after `<style>`, and `/*__ATTENTION_PAGE_SCRIPT__*/` immediately after `<script>`. The JS asset is the exact text that sat between `<script>` and `</script>` (it begins with a newline and ends with a newline). The assets are valid to parse unfilled because each placeholder is a comment in its own language.

2. **Create `pkg/handler/static_embed.go`** with the standard BSD licence header used by the other files, `package handler`, and the embed shim. This is the only `//go:embed` directive in the package. It names the directory, not a file glob, matching the vault-ui shape named in the spec:

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

   The directive line must be exactly `//go:embed static` at column 0, immediately followed (no blank line) by `var attentionPageStatic embed.FS` in the same comment group. Do not embed `static/*.html` or `static/*.js`; do not add a second embed directive anywhere in `pkg/handler/`.

3. **Add the assembly and the assembled value to `pkg/handler/attention-page-template.go`.** Keep `newAttentionPageTemplate` and its `template.FuncMap` exactly as they are; it continues to parse the identifier `attentionPageTemplate`, which is now a package-level `var` rather than a `const`. Add:

   ```go
   const (
       attentionPageStylesPlaceholder = "/*__ATTENTION_PAGE_STYLES__*/"
       attentionPageScriptPlaceholder = "/*__ATTENTION_PAGE_SCRIPT__*/"
   )

   var attentionPageTemplate = buildAttentionPage(
       mustReadAttentionPageAsset("attention-page.html"),
       mustReadAttentionPageAsset("attention-page.js"),
   )

   // buildAttentionPage assembles the served document from the embedded HTML
   // asset, the embedded script asset and the stylesheet constant. It runs at
   // package init, before html/template parses the result, so the engine still
   // sees one document: CSS comments are still stripped in the style context, and
   // the script is still inserted verbatim because it is literal text rather than
   // a template action.
   //
   // ⚠️ A missing or duplicated placeholder panics rather than serving a page
   // whose stylesheet or script came out empty. The panic happens at startup, so
   // the process never serves the broken document.
   func buildAttentionPage(html, script string) string {
       page := replaceAttentionPagePlaceholder(
           html, attentionPageStylesPlaceholder, attentionPageStyles, "stylesheet",
       )
       return replaceAttentionPagePlaceholder(
           page, attentionPageScriptPlaceholder, script, "script",
       )
   }

   // replaceAttentionPagePlaceholder substitutes value for the one occurrence of
   // placeholder in document. It panics when the placeholder is absent or appears
   // more than once, naming the placeholder and its count, because either state
   // means the assembled document would differ from the one master served.
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

   Add the imports this needs (`fmt`, `strings`); keep `html/template`. The substitution must be a plain string replacement, never a template action — a template action would put the script through `html/template`'s JS escaper and change every byte of it.

   Then **correct the now-false rationale comment** on `newAttentionPageTemplate` (the paragraph beginning `⚠️ This lives in its own file rather than beside the template constant because`). Replace it with a true statement, for example:

   ```go
   // ⚠️ This lives in its own file because it owns the assembly as well as the
   // parse: attentionPageTemplate is built here from the embedded assets, so
   // attention-page.go carries no served bytes and stays a handler.
   ```

4. **Move the document's Go doc comment and delete the literal from `pkg/handler/attention-page.go`.** The block currently immediately above `const attentionPageTemplate` (it starts `// attentionPageTemplate is the whole page:`) is a Go comment, not document bytes. Move that whole block to sit immediately above the new `var attentionPageTemplate` in `attention-page-template.go`, and change only its first paragraph so it stops claiming the document lives in `attention-page.go` as a string constant. Suggested replacement for the first paragraph:

   ```go
   // attentionPageTemplate is the whole page: one document, inline styles, no
   // external stylesheet, no framework, no build step. It is assembled at package
   // init from the embedded HTML asset, the embedded script asset and
   // attentionPageStyles, so the page still ships inside the binary — with no
   // runtime file read and no new deployment artefact.
   ```

   Leave every other sentence of that comment intact. Do **not** put any of this Go comment into the HTML asset.

   Then delete the entire `const attentionPageTemplate = ` … closing backtick from `pkg/handler/attention-page.go` (the block between the moved doc comment and the blank line above `// NewAttentionPageHandler`). Keep the package clause, the imports and the handler. All imports remain used. Leave exactly one blank line between the import block and the `NewAttentionPageHandler` doc comment. The result must be under 300 lines and carry no served bytes.

5. **Do not move or edit the stylesheet.** `attentionPageStyles` stays a constant in `pkg/handler/attention-page-helpers.go`, including the comment above it. The HTML asset holds a placeholder for it; the CSS itself never enters the asset.

6. **Do not touch anything else.** No change to any template action, any markup, any script byte, `attention-stream.go`, `attention-page-data.go`, `attention-page-helpers.go`, `e2e/`, `scenarios/`, or `CHANGELOG.md` (the changelog is a separate prompt). No reformatting, reindenting, re-wrapping or comment rewrite inside either asset.

7. **Prove the assembled bytes are master's bytes** with this non-git reconstruction, run from the repo root after the edits. It reads the two asset files on disk, substitutes the stylesheet constant from `attention-page-helpers.go`, and asserts the pinned SHA-256:

   ```bash
   python3 - <<'PY'
   import hashlib

   html = open('pkg/handler/static/attention-page.html', 'rb').read().decode('utf-8')
   js = open('pkg/handler/static/attention-page.js', 'rb').read().decode('utf-8')
   helpers = open('pkg/handler/attention-page-helpers.go', 'rb').read().decode('utf-8')

   STYLES_CONST = "const attentionPageStyles = `"
   STYLES_MARKER = "/*__ATTENTION_PAGE_STYLES__*/"
   SCRIPT_MARKER = "/*__ATTENTION_PAGE_SCRIPT__*/"

   start = helpers.index(STYLES_CONST) + len(STYLES_CONST)
   styles = helpers[start:helpers.index("`", start)]

   assert html.count(STYLES_MARKER) == 1, "stylesheet placeholder must occur once"
   assert html.count(SCRIPT_MARKER) == 1, "script placeholder must occur once"

   doc = html.replace(STYLES_MARKER, styles, 1).replace(SCRIPT_MARKER, js, 1)
   digest = hashlib.sha256(doc.encode('utf-8')).hexdigest()
   print('assembled bytes:', len(doc.encode('utf-8')), 'sha256:', digest)
   assert digest == 'e919452e21188873e422ca8323a8a2c3c2380043f3c9bc07e58410073257a86a', digest
   PY
   ```

   Expected: `assembled bytes: 130273 sha256: e919452e21188873e422ca8323a8a2c3c2380043f3c9bc07e58410073257a86a`. (The spec's prose byte counts are UTF-8 byte counts: 19,876 + 83,224 assets and a 130,273-byte unstripped document. The pinned digest is the authority.)

8. Run `ROOTDIR="$(pwd)" make precommit`, `go build -mod=mod ./...` and `go test -mod=mod ./pkg/handler/`.
</requirements>

<constraints>
- **The served bytes are the contract.** No reformatting, no reindenting, no re-wrapping, no comment rewrites inside the moved bytes, no added or removed template action, and no whitespace change at either seam. A single changed byte fails the reconstruction digest.
- **The two placeholders are frozen** at `/*__ATTENTION_PAGE_STYLES__*/` and `/*__ATTENTION_PAGE_SCRIPT__*/`. Do not rename them; the verification substitutes by these exact strings.
- **The stylesheet stays in Go.** `attentionPageStyles` remains a constant in `attention-page-helpers.go`; do not move it into an asset and do not edit its comment.
- **Assembly must not go through the template engine.** Substitute the script by plain string replacement, never by a `{{...}}` action; an action would put it through the JS escaper and change every byte.
- **No behaviour change of any kind.** No markup, no script, no template action, no HTTP response, no route and no e2e case changes.
- **No new runtime surface.** No runtime file read, no new mount, no new flag, no new config key, no new HTTP response, and no second URL for the script. The assets are compile-time constants in the binary.
- **`attention-page-helpers.go` is not modified.** Its comment explaining why the stylesheet is a const rather than an embed stays true and stays as written.
- **No `e2e/` or `scenarios/` edit.** `scenarios/001` and `scenarios/002` assert hard package totals that must not move.
- **The failure modes are implemented, not deferred:** a wrong embed path fails `go build`; a file missing from `static/` panics at startup from `fs.ReadFile` naming the asset; a deleted or duplicated placeholder panics naming the placeholder and its count.
- **No `//nolint`.** New Go files carry the standard BSD licence header.
- **No `git` commands in this container** (`.git` is masked). Do not commit — dark-factory handles git.
- Existing tests must still pass unchanged. The handler suite is the behavioural lock; this prompt does not edit it.
</constraints>

<verification>
Run from the repository root. `.git` is masked: no `git` command appears here and none may be added.

- `wc -c pkg/handler/static/attention-page.html` — prints `19876`.
- `wc -c pkg/handler/static/attention-page.js` — prints `83224`.
- `grep -F -c '/*__ATTENTION_PAGE_STYLES__*/' pkg/handler/static/attention-page.html` — prints `1`.
- `grep -F -c '/*__ATTENTION_PAGE_SCRIPT__*/' pkg/handler/static/attention-page.html` — prints `1`.
- `grep -c '^//go:embed static$' pkg/handler/static_embed.go` — prints `1`.
- `grep -rl '^//go:embed' pkg/handler/*.go` — prints only `pkg/handler/static_embed.go`.
- `wc -l pkg/handler/attention-page.go` — prints a number **≤ 300** (from 1,995).
- `grep -c '<!DOCTYPE html>' pkg/handler/attention-page.go` — prints `0`.
- `grep -c 'addEventListener' pkg/handler/attention-page.go` — prints `0`.
- `grep -c 'addEventListener' pkg/handler/static/attention-page.js` — prints a number **> 0**.
- `grep -c 'attentionPageTemplate' pkg/handler/attention-page.go` — prints `0`.
- `grep -c 'func buildAttentionPage' pkg/handler/attention-page-template.go` — prints `1`.
- `grep -c 'func replaceAttentionPagePlaceholder' pkg/handler/attention-page-template.go` — prints `1`.
- The reconstruction check in requirement 7 — prints `assembled bytes: 130273 sha256: e919452e21188873e422ca8323a8a2c3c2380043f3c9bc07e58410073257a86a` and exits 0.
- `go build -mod=mod ./...` — must exit 0.
- `go test -mod=mod ./pkg/handler/` — must exit 0.
- `ROOTDIR="$(pwd)" make precommit` — **must exit 0**.

The spec's comparison against `origin/master` and the `Ran N of N Specs` browser comparison are operator-side (the container has no `.git` and no browser) and are not part of this prompt's gate.
</verification>
