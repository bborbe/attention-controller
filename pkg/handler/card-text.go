// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"html"
	"html/template"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// cardAnchorAttrs is what every anchor this file authors carries after its href.
//
// ⚠️ `target="_blank"` on its own is a tabnabbing hole: the opened page gets a
// live `window.opener` and can navigate the board out from under the operator —
// which is the exact harm the new tab exists to prevent. `rel="noopener
// noreferrer"` is what closes it, and it is not decoration. `noreferrer` also
// keeps the board's URL out of the target's referrer.
const cardAnchorAttrs = ` target="_blank" rel="noopener noreferrer"`

// cardLinkSchemes is the closed set of URL schemes a card body may link to.
//
// ⚠️ This is an allowlist, not a denylist, and it is the whole security
// argument for renderCardText. A card body is authored by a *session*, not by a
// trusted template, so the value reaching the anchor is producer input. A
// denylist would have to enumerate every dangerous scheme and would be wrong the
// first time one was missed; an allowlist fails closed.
//
// `obsidian://` is admitted because it is the board's own navigation scheme —
// the provenance line already links into the vault with it. `javascript:` and
// `data:` are the two the task names as must-stay-inert, and neither is here.
var cardLinkSchemes = map[string]bool{
	"http":     true,
	"https":    true,
	"obsidian": true,
}

var (
	// cardMarkdownLink matches `[label](url)`. The label may not contain `]`
	// and the url may not contain whitespace or a paren, so a url that itself
	// carries parens (`javascript:alert(1)`) does not match — which is the safe
	// direction: an unmatched candidate stays literal text.
	//
	// ⚠️ The `^` is load-bearing for cost, not for meaning. renderCardText tests
	// this at every position of the body, and an unanchored pattern makes each
	// test scan the whole remaining string to find a match it will then reject —
	// O(n²) over a link-free body, on a path that re-renders the entire board on
	// every change. Anchored, the engine fails on the first byte.
	cardMarkdownLink = regexp.MustCompile(`^\[([^\]]*)\]\(([^()\s]*)\)`)

	// cardBareURL matches a bare http(s) URL. Deliberately excludes the
	// characters that end a sentence around a URL — `<`, `>`, quotes, and the
	// parens — so a URL wrapped in prose is autolinked without swallowing the
	// prose. ⚠️ `)` is in that set and is the one that matters: without it
	// `see (https://example.com)` links to `https://example.com)` and the
	// anchor points at a URL that does not exist. The cost is a URL whose own
	// path carries a paren is cut at it, which is the rarer case on this board
	// and the safer failure. `^` is load-bearing for the same cost reason as
	// cardMarkdownLink's.
	cardBareURL = regexp.MustCompile(`^https?://[^\s<>"'()]+`)
)

// cardScheme returns the scheme of a URL, lowercased, or "" when the value
// carries none.
//
// A scheme is the run before the first `:` — but only when that `:` precedes any
// `/`, `?` or `#`, so `example.com/a:b` reports no scheme rather than inventing
// one from a colon inside a path. Case is folded because `JavaScript:` and
// `javascript:` are the same scheme to a browser, and an allowlist that
// compared raw would admit the first.
func cardScheme(rawURL string) string {
	for i := 0; i < len(rawURL); i++ {
		switch rawURL[i] {
		case ':':
			return strings.ToLower(rawURL[:i])
		case '/', '?', '#':
			return ""
		}
	}
	return ""
}

// cardTextString returns a card body field's text.
//
// The body fields are named string types rather than `string`, so a func the
// template calls has to widen them itself. The test is on reflect.Kind rather
// than on a list of the two types that exist today: a new body field of any
// string-kinded type is then carried without a change here, which is the
// difference between a field that renders and one that 500s the whole page.
//
// ⚠️ Anything not string-kinded returns empty rather than a rendering of the
// value. A non-string-kinded body field is a programming error, and the obvious
// fallback — fmt.Sprint — would put a struct's own fields on the operator's
// card. This is a trust boundary, so it fails closed.
func cardTextString(value any) string {
	if value == nil {
		return ""
	}
	if rv := reflect.ValueOf(value); rv.Kind() == reflect.String {
		return rv.String()
	}
	return ""
}

// renderCardText renders a card's body text as HTML: markdown links and bare
// http(s) URLs become anchors, and everything else is escaped.
//
// ⚠️ **The return type is the risk.** `template.HTML` is trusted unescaped by
// html/template — that is the only way to emit an anchor at all, since the
// template otherwise escapes the whole body. So this function, not the
// template, is the escaping boundary, and its contract is: every byte of the
// input reaches the output either HTML-escaped, or inside an attribute or text
// node whose *scheme was checked by name* against cardLinkSchemes first. There
// is no path where an unchecked value becomes markup.
//
// Three rules, each with a case that fails without it:
//
//   - A markdown link whose scheme is not allowlisted renders as its own
//     escaped text — no anchor. The scheme is checked, never inherited.
//   - A bare URL is autolinked only when the two bytes before it are not `](`.
//     That lookbehind is what stops a payload the producer cut mid-link
//     (`[pane 88](http://127.0.0.1:1337/`) from rendering a clickable
//     half-link: the URL is real and the link syntax is broken, so linking it
//     would point the operator at a 404 while looking correct. ⚠️ It is a fixed
//     two-byte test, not a parse — `[label] (url)`, with a space between them,
//     still autolinks its URL. That is the right call, since that form is not
//     valid link syntax and the URL is genuinely bare, and it does not weaken
//     the case the rule exists for: the producer cuts at end-of-string, so the
//     two bytes before a cut URL are always `](`.
//   - Nothing else is markup. A body carrying `<script>` renders the literal
//     text, escaped.
//
// ⚠️ **The parameter is `any`, not `string`, and that is forced by the call
// site.** The body fields are distinct named string types — `pkg.Payload` and
// `pkg.ItemContext` — so html/template passes them through by their own type and
// refuses a call to a func declared to take `string`: `wrong type for value;
// expected string; got pkg.Payload`, raised at render time, which turns every
// board page into a 500. cardTextString widens them back; the reflection there
// is on Kind, so a future body field of another string-kinded type works without
// touching this function.
func renderCardText(value any) template.HTML {
	text := cardTextString(value)

	var out strings.Builder
	out.Grow(len(text))

	for i := 0; i < len(text); {
		if markup, consumed, ok := cardLinkAt(text, i); ok {
			out.WriteString(markup)
			i += consumed
			continue
		}
		if markup, consumed, ok := cardBareURLAt(text, i); ok {
			out.WriteString(markup)
			i += consumed
			continue
		}
		// Advance one whole rune, so a multi-byte character is never split.
		_, width := utf8.DecodeRuneInString(text[i:])
		out.WriteString(html.EscapeString(text[i : i+width]))
		i += width
	}

	// #nosec G203 -- the reported risk is "use of unescaped data in an HTML
	// template", and the conversion is the point: html/template escapes a plain
	// string, so a card body carrying a link cannot be rendered as an anchor
	// any other way. Every byte written above is either html.EscapeString'd or
	// is markup this function authored, and the one value that reaches an
	// attribute — the href — is escaped *and* had its scheme checked against
	// cardLinkSchemes by name. This records the boundary; it does not waive a
	// risk.
	return template.HTML(out.String())
}

// cardLinkAt returns the markup for a markdown link starting at text[i] and the
// number of bytes it consumes. ok is false when no link starts there, which is
// the common case and costs one byte comparison.
//
// ⚠️ The first-byte guard is what keeps renderCardText's loop linear. The
// pattern is `^`-anchored, so each call is cheap on its own — but invoking the
// regexp engine at every position of a body that carries no link at all is work
// with no possible answer, and this runs for every row of a board that
// re-renders on every change. The guard cannot change what matches: the pattern
// begins with `[`.
func cardLinkAt(text string, i int) (string, int, bool) {
	if text[i] != '[' {
		return "", 0, false
	}
	loc := cardMarkdownLink.FindStringSubmatchIndex(text[i:])
	if loc == nil {
		return "", 0, false
	}
	label := text[i+loc[2] : i+loc[3]]
	rawURL := text[i+loc[4] : i+loc[5]]
	if !cardLinkSchemes[cardScheme(rawURL)] {
		// Scheme refused: the whole candidate stays visible as text, so the
		// operator can still read what the card tried to do.
		return html.EscapeString(text[i+loc[0] : i+loc[1]]), loc[1], true
	}
	return `<a href="` + html.EscapeString(rawURL) + `"` + cardAnchorAttrs + `>` +
		html.EscapeString(label) + `</a>`, loc[1], true
}

// cardBareURLAt returns the markup for a bare http(s) URL starting at text[i]
// and the number of bytes it consumes. ok is false when none starts there.
//
// ⚠️ A URL whose two immediately preceding bytes are `](` is emitted as text,
// never as an anchor. That lookbehind is what stops a payload the producer cut
// mid-link (`[pane 88](http://127.0.0.1:1337/`) from rendering a clickable
// half-link: the URL is real and the link syntax is broken, so linking it would
// point the operator at a 404 while looking correct. It is a fixed two-byte
// test rather than a parse, so `[label] (url)` — a space between them — still
// autolinks its URL; see renderCardText for why that is correct.
//
// The first-byte guard keeps the loop linear, for the reason given on cardLinkAt.
func cardBareURLAt(text string, i int) (string, int, bool) {
	if text[i] != 'h' {
		return "", 0, false
	}
	loc := cardBareURL.FindStringIndex(text[i:])
	if loc == nil {
		return "", 0, false
	}
	rawURL := text[i : i+loc[1]]
	if i >= 2 && text[i-2:i] == "](" {
		return html.EscapeString(rawURL), loc[1], true
	}
	return `<a href="` + html.EscapeString(rawURL) + `"` + cardAnchorAttrs + `>` +
		html.EscapeString(rawURL) + `</a>`, loc[1], true
}
