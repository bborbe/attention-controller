// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"html/template"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
)

// attentionPageTemplate is the whole page: one document, inline styles, no
// external stylesheet, no framework, no build step. It lives here as a string
// constant so the page ships inside the binary rather than as an asset.
//
// The document is deliberately inert. There is no <form>, no submitting
// <button>, no <script> and no fetch/XHR: the page only reads, so an answer or
// close control would be a defect rather than a missing feature.
//
// Rendering goes through html/template, which escapes every interpolated value
// for the context it lands in. ProducerID is a producer-supplied free string
// (validated only by NotEmptyString), so that escaping is what keeps a producer
// from injecting markup into the reader's browser.
const attentionPageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Attention</title>
<style>
/* Dark theme, matching the tts-mcp page this store's UI is modelled on — same
   token names and values, so the two surfaces read as one family. The
   color-scheme property is set so the scrollbar and any native control render
   dark too; without it the page is dark but the chrome around it stays light. */
:root {
  color-scheme: dark;
  --bg: #111418;
  --panel: #1a1f26;
  --border: #2a3038;
  --text: #e8edf2;
  --muted: #8b95a3;
}
* { box-sizing: border-box; }
body {
  margin: 0 auto;
  max-width: 760px;
  padding: 24px;
  background: var(--bg);
  color: var(--text);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}
h1 { font-size: 20px; margin: 0 0 16px; }
ul.items { list-style: none; padding: 0; margin: 0; }
li.item {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 16px;
  margin-bottom: 12px;
}
.producer {
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  letter-spacing: 0.04em;
}
.payload { font-size: 15px; line-height: 1.45; margin: 6px 0 8px; white-space: pre-wrap; }
.meta { color: var(--muted); font-size: 12px; }
.empty { color: var(--muted); font-size: 14px; }
</style>
</head>
<body>
<h1>Attention</h1>
{{if .Items}}<ul class="items">
{{range .Items}}<li class="item" data-item-id="{{ .ItemID }}">
<div class="producer">{{ .ProducerID }} ({{ .ProducerKind }})</div>
<div class="payload">{{ .Payload }}</div>
<div class="meta">{{ .State }} - {{ .CreatedAt }}</div>
</li>
{{end}}</ul>
{{else}}<p class="empty">Nothing needs attention.</p>
{{end}}</body>
</html>
`

// attentionPageData is what the template renders: the items the store's read
// path returned, in the order it returned them. It carries no ordering of its
// own — the store never ranks, so the page must not either.
type attentionPageData struct {
	Items pkg.Items
}

// NewAttentionPageHandler creates the read-only HTML page a human opens to see
// what currently needs attention.
//
// It is the store's first consumer outside Claude Code, so it renders what the
// store returns and nothing else: no session-name resolution, no ranking by
// InterruptClass, no state-changing control. ProducerID is shown exactly as the
// producer supplied it — it is a session id, and the session registry deletes
// its entry when the session exits, so a render-time lookup would find nothing
// and would present an unresolvable value as resolved.
func NewAttentionPageHandler(store pkg.AttentionStore) http.Handler {
	// Parsed once at construction rather than per request: the template is a
	// compile-time constant, so a parse failure is a programming error, and
	// template.Must makes it a startup failure rather than a per-request one.
	page := template.Must(template.New("attention-page").Parse(attentionPageTemplate))
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				items, err := store.Read(ctx)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "read failed"),
						libhttp.ErrorCodeInternal,
						http.StatusInternalServerError,
					)
				}
				// Rendered into a buffer first so a render failure can still
				// produce the standard JSON error body. Writing straight to the
				// response would commit a 200 and a partial document before the
				// error handler had a chance to report anything.
				var body bytes.Buffer
				if err := page.Execute(&body, attentionPageData{Items: items}); err != nil {
					return errors.Wrap(ctx, err, "render page failed")
				}
				resp.Header().Set(
					libhttp.ContentTypeHeaderName,
					libhttp.TextHTML+"; charset=utf-8",
				)
				if _, err := resp.Write(body.Bytes()); err != nil {
					return errors.Wrap(ctx, err, "write response failed")
				}
				return nil
			},
		),
	)
}
