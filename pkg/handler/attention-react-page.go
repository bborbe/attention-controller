// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"html"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// buildIdentityMarker is the element the built index.html carries where the
// serving build's identity belongs. The handler replaces it per request, because
// the identity is a fact about the running binary and cannot be baked into the
// static build. It is a real element rather than an HTML comment so Vite's build
// preserves it.
const buildIdentityMarker = `<div id="build-identity"></div>`

// NewAttentionReactPageHandler serves the embedded React frontend as the board
// page. It replaces the server-rendered vanilla page: the document is the Vite
// build, the client fetches the JSON API and subscribes to the SSE stream.
//
// dist is the frontend/dist sub-tree, opened by main with fs.Sub. `/` (and
// `/index.html`) serve the document; a path that names a real file is served as
// an immutable, content-hashed asset; any other path is a 404. The board has no
// client-side routes, so falling back to index.html would only answer a missing
// asset with an HTML 200 — a browser then fails to parse it as a script instead
// of reporting the missing file. A missing index.html — a binary built without
// `make frontend` — is a 503 rather than an empty 200.
//
// ⚠️ Assets are read with fs.ReadFile + http.ServeContent rather than
// http.FileServer(http.FS(dist)). FileServer maps req.URL.Path into the FS with a
// leading slash, which fs.Sub rejects, so every asset 404s under the FileServer
// form while index.html still serves — the exact "page loads, JS does not"
// failure. Reading the bytes and serving them by name keeps the Content-Type
// (from the extension), Range and HEAD handling without the path mismatch.
//
// identity is the running binary's provenance. It is injected into the served
// index.html's footer so the deploy gate's `commit <span class="bi-value">`
// probe keeps working; the footer is the same markup the vanilla page rendered.
func NewAttentionReactPageHandler(
	dist fs.FS,
	identity buildidentity.Identity,
	metrics pkg.Metrics,
) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		resp.Header().Set("X-Content-Type-Options", "nosniff")
		path := strings.TrimPrefix(req.URL.Path, "/")
		if path != "" && path != "index.html" {
			data, err := fs.ReadFile(dist, path)
			if err != nil {
				http.NotFound(resp, req)
				return
			}
			// Vite emits content-hashed asset names, so an asset's bytes never
			// change under a given URL.
			resp.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeContent(resp, req, path, time.Time{}, bytes.NewReader(data))
			return
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(resp, "frontend not built", http.StatusServiceUnavailable)
			return
		}
		// Counted per document, not per asset: one page load is one board request,
		// matching the counter's meaning under the vanilla page.
		metrics.BoardPageRequestsTotalCounterInc()
		resp.Header().Set("Content-Type", "text/html; charset=utf-8")
		resp.Header().Set("Cache-Control", "no-store")
		if req.Method == http.MethodHead {
			return
		}
		_, _ = resp.Write([]byte(injectBuildIdentity(string(index), identity)))
	})
}

// injectBuildIdentity replaces the marker with the footer markup the vanilla
// page rendered. The deploy gate reads the SERVING process's commit out of that
// footer with `commit <span class="bi-value">([0-9a-f]+)`, so the markup is kept
// byte-compatible rather than restyled.
//
// An unstamped build renders the same "no build identity" sentence the vanilla
// page did: the prober then reports "no commit" and refuses the deploy, which is
// the honest outcome for a binary that cannot name its source.
func injectBuildIdentity(document string, identity buildidentity.Identity) string {
	var footer string
	if identity.Known() {
		footer = `<footer class="build-identity">version <span class="bi-value">` +
			html.EscapeString(identity.Version) + `</span> · commit <span class="bi-value">` +
			html.EscapeString(identity.Commit) + `</span> · committed <span class="bi-value">` +
			html.EscapeString(identity.CommitTime) + `</span></footer>`
	} else {
		footer = `<footer class="build-identity"><span class="bi-absent">No build identity: ` +
			`this binary carries no VCS stamp, so it cannot say which source it was built from.</span></footer>`
	}
	return strings.Replace(document, buildIdentityMarker, footer, 1)
}
