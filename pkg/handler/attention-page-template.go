// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"fmt"
	"html/template"
	"strings"
)

const (
	attentionPageStylesPlaceholder = "/*__ATTENTION_PAGE_STYLES__*/"
	attentionPageScriptPlaceholder = "/*__ATTENTION_PAGE_SCRIPT__*/"
)

// attentionPageTemplate is the whole page: one document, inline styles, no
// external stylesheet, no framework, no build step. It is assembled at package
// init from the embedded HTML asset, the embedded script asset and
// attentionPageStyles, so the page still ships inside the binary — with no
// runtime file read and no new deployment artefact.
//
// The document renders answer controls for `message` items, and a jump handover
// for every item whose session resolves to a pane. ⚠️ Both reverse the decision
// this page was built with, which was that the document is inert — "no <form>,
// no submitting <button>, no <script> and no fetch/XHR: the page only reads, so
// an answer or close control would be a defect rather than a missing feature".
//
// The reversal is bounded by the schema's who-answers-what ruling rather than
// by taste. A `message` item is `pick`-shaped, so a control that gives a `pick`
// answer is a legal way to answer it. A `permission` item is `approve`-shaped
// and **only the operator may answer it, in the session that raised it** — an
// answer control there would be the permission laundering the schema forbids —
// so a `permission` item renders no answer controls at all. The schema's
// § Answer routing records the board as a second arm
// (`answered_by: attention-board`) and states this reversal; silence 12 is the
// field change that made it possible.
//
// The card is shaped by the declared cardinality rather than by taste: a
// question that allows several picks renders checkboxes and one that allows a
// single pick renders radio buttons, both read from the producer's
// AnswerCardinality and never inferred from the option count — a one-option
// question and a many-option single-pick question carry lists of different
// lengths and ask for different things, so a board that read the length would
// render a checkbox for a question admitting one answer. An item carrying
// `questions` renders one tab per question, each with its own payload, options
// and control; a single-question item renders one card with no tab strip.
//
// ⚠️ The jump handover is a button as well as a copyable command, and that
// reverses a second decision recorded here. It was a command only, on the
// reasoning that "a browser cannot activate a WezTerm tab, so an anchor here
// would present a value as resolved that is not". That premise is false against
// the fleet-jump server, which activates a pane from a plain HTTP GET — proven
// end-to-end on 2026-09-24 — so a link does resolve. The command stays, because
// an operator may still want to paste it, and the button is added beside it.
//
// The button's target is a path on *this* board, never the fleet-jump URL: that
// URL carries the shared token as a query parameter, and the token must not
// reach the browser. The path is called in the background, and the board
// performs the jump server-side, so the token stays on the server and out of
// the served document — and the click leaves the page where it was, which is
// the behaviour the operator asked for.
//
// Rendering goes through html/template, which escapes every interpolated value
// for the context it lands in. ProducerID is a producer-supplied free string
// (validated only by NotEmptyString) and the provenance values are read from
// the producer's own event log, so that escaping is what keeps a producer from
// injecting markup into the reader's browser.
//
// The provenance line renders one span per resolved value and omits the rest.
// The task, goal and topic names, the host, cwd, tool and pane the event log
// recorded, and the session name the registry resolved are each drawn as their
// own span when resolved. ⚠️ An unresolved value is *omitted*, never filled with
// a placeholder — a stand-in like `unknown` or `n/a` would be an unresolvable
// value presented as resolved, which [[Attention Item Schema]] § Silence 7
// forbids. A row with no resolvable provenance at all renders no provenance
// line, which is exactly what this page rendered before the change.
//
// ⚠️ The task name leads the line, drawn as a link that opens the task in
// Obsidian — the one fact the card had always lacked, since the host, cwd, tool
// and pane all say *where* a session ran and none says *what* it was working on.
// It is wrapped in its own span rather than inserted as a bare anchor, and the
// wrapper is required rather than cosmetic: the line's separators come from a
// rule matching only *adjacent* spans (`.provenance span + span::before`), so a
// bare `<a>` among the spans would suppress the separator beside it and the line
// would render as `Fix the boardburn · /w/x`. It is navigation, so it adds no
// control and changes nothing any card offers. ⚠️ The session name closes the
// line, drawn last as its own span, and it is the one value on the line that
// comes from the session registry rather than from the producer's event log or
// the vault — so a card whose session resolves a name and nothing else renders a
// line carrying only that span. The line leads the card, above the ask, per
// silence 26 as amended 2026-10-03, and each distinct navigation value renders
// once: a session name equal to the resolved task title is not drawn, because
// the task link already carries it.
//
// ⚠️ The goal and the topic follow the task as two more spans of the same shape,
// each gated on its own resolved link: the goal this item's task names first, and
// the topic page that lists that goal under its `## Goals` heading. They are
// gated independently rather than by one condition over both, because a task
// carrying a goal no topic lists is the dominant live case — a single gate would
// drop that goal link along with the absent topic. An unresolved one renders
// absent rather than as a placeholder, the rule the rest of the line already
// follows: a goal no topic lists draws its goal span and no topic span, and a
// task naming no goal draws neither. They are navigation too, adding no control
// and changing nothing any card offers. The session name is a fourth span of the
// same shape, gated on its own resolved value and independent of the other
// three — any of the four may render alone or in any combination.
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
// ⚠️ This lives in its own file because it owns the assembly as well as the
// parse: attentionPageTemplate is built here from the embedded assets, so
// attention-page.go carries no served bytes and stays a handler.
func newAttentionPageTemplate() *template.Template {
	return template.Must(template.New("attention-page").Funcs(template.FuncMap{
		// cardtext is the escaping boundary for a card's body text: the template
		// escapes a plain string, so a payload carrying a markdown link could
		// never render as an anchor without it. See renderCardText — the scheme
		// check lives there, not here.
		"cardtext": renderCardText,
	}).Parse(attentionPageTemplate))
}
