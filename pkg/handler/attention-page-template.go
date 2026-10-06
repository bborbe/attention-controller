// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "html/template"

// newAttentionPageTemplate parses the board template together with the
// functions it calls.
//
// ⚠️ **Both the page handler and the stream handler parse this one template, and
// they must parse it identically.** `attention-row` renders from one definition,
// so a function registered on one path and not the other leaves the stream
// raising `function "cardtext" not defined` at parse time while the page renders
// fine — and because the stream is the live-update path, that failure is
// invisible on a fresh page load and appears only when a row arrives over an
// open stream. Building the template in one place is what keeps the two in step;
// the stream handler's own comment had claimed they were in step since before
// there was any function to get wrong, and the claim was false the moment there
// was one.
//
// ⚠️ A parse failure here is a programming error, so `template.Must` is
// deliberate: it makes the failure a startup crash rather than a per-request
// 500.
//
// ⚠️ This lives in its own file rather than beside the template constant because
// `attention-page.go` sits within a few lines of revive's 2000-line
// `file-length-limit`; adding here costs that file nothing.
func newAttentionPageTemplate() *template.Template {
	return template.Must(template.New("attention-page").Funcs(template.FuncMap{
		// cardtext is the escaping boundary for a card's body text: the template
		// escapes a plain string, so a payload carrying a markdown link could
		// never render as an anchor without it. See renderCardText — the scheme
		// check lives there, not here.
		"cardtext": renderCardText,
	}).Parse(attentionPageTemplate))
}
