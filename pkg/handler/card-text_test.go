// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is an internal test package rather than the external `handler_test`
// the rest of the directory uses, and deliberately so: renderCardText is
// unexported, and the escaping boundary it owns is worth asserting on the
// function itself rather than through a served page — a page-level assertion
// cannot distinguish "the body was rendered as an anchor" from "some other row
// on the page carried an anchor". The specs register into the same ginkgo suite
// handler_suite_test.go runs.
package handler

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("renderCardText", func() {
	// ⚠️ Every expected value below is a hand-written literal, never one built
	// with the same helper the code uses. A literal built by sharing the
	// production escaper would agree with itself whatever that escaper produced,
	// which is the failure this whole file exists to catch.
	DescribeTable("renders the body",
		func(input string, expected string) {
			Expect(string(renderCardText(input))).To(Equal(expected))
		},
		Entry("a markdown link, as an anchor carrying only the label",
			`[pane 87](http://127.0.0.1:1337/jump?pane=87&t=x)`,
			`<a href="http://127.0.0.1:1337/jump?pane=87&amp;t=x">pane 87</a>`),
		Entry("a bare https url, autolinked with the url as its own text",
			`see https://example.com/x`,
			`see <a href="https://example.com/x">https://example.com/x</a>`),
		Entry("a bare http url, autolinked",
			`go to http://127.0.0.1:18080/ now`,
			`go to <a href="http://127.0.0.1:18080/">http://127.0.0.1:18080/</a> now`),
		Entry("an obsidian link, which the board's own navigation needs",
			`[task](obsidian://open?vault=Personal&file=25%20Tasks%2FFoo)`,
			`<a href="obsidian://open?vault=Personal&amp;file=25%20Tasks%2FFoo">task</a>`),
		Entry("a javascript link, refused by the scheme allowlist and left as text",
			`[x](javascript:alert(1))`,
			`[x](javascript:alert(1))`),
		Entry("a data link, refused by the scheme allowlist and left as text",
			`[y](data:text/html,z)`,
			`[y](data:text/html,z)`),
		Entry("a mixed-case scheme, folded before the allowlist check",
			`[x](JaVaScRiPt:alert(1))`,
			`[x](JaVaScRiPt:alert(1))`),
		Entry("html in the body, escaped with no live tag",
			`<script>alert(1)</script>`,
			`&lt;script&gt;alert(1)&lt;/script&gt;`),
		Entry("a payload cut mid-link: no anchor and no autolinked fragment",
			`[pane 88](http://127.0.0.1:1337/`,
			`[pane 88](http://127.0.0.1:1337/`),
		Entry("html inside a label, escaped",
			`[<b>x</b>](https://example.com)`,
			`<a href="https://example.com">&lt;b&gt;x&lt;/b&gt;</a>`),
		Entry("a quote in a url, escaped so it cannot leave the attribute",
			`[x](https://example.com/"onmouseover=evil)`,
			`<a href="https://example.com/&#34;onmouseover=evil">x</a>`),
		Entry("an empty body",
			``,
			``),
		Entry("plain text with no link at all",
			`review: check the board`,
			`review: check the board`),
	)

	It("accepts the named body types the template actually passes", func() {
		// ⚠️ The regression this guards: a func declared to take `string` is
		// refused by html/template when handed `pkg.Payload`, because the field's
		// own type is passed through — `wrong type for value; expected string;
		// got pkg.Payload`, raised at render time, which turns every board page
		// into a 500 while every unit test on a plain string still passes.
		Expect(string(renderCardText(pkg.Payload(`[x](https://example.com)`)))).
			To(Equal(`<a href="https://example.com">x</a>`))
		Expect(string(renderCardText(pkg.ItemContext(`plain context`)))).
			To(Equal(`plain context`))
	})

	It("keeps a multi-byte rune whole", func() {
		Expect(string(renderCardText("café — naïve"))).To(Equal("café — naïve"))
	})

	It("autolinks the link that survived a cut but not the one the cut broke", func() {
		// The live reproduction: the producer stores exactly 200 characters, so
		// the first link is whole and the second is cut mid-URL. The whole link
		// must become an anchor and the broken one must stay text — this is the
		// case the operator's screenshot shows, and the case a renderer that
		// autolinked every bare URL would get half wrong.
		body := `review: Start Day in [pane 87](http://127.0.0.1:1337/jump?pane=87&t=abc), Check Prometheus Alerts in [pane 88](http://127.0.0.1:1337/`
		got := string(renderCardText(body))

		Expect(got).To(ContainSubstring(
			`<a href="http://127.0.0.1:1337/jump?pane=87&amp;t=abc">pane 87</a>`))
		Expect(got).To(ContainSubstring(`[pane 88](http://127.0.0.1:1337/`))
		Expect(strings.Count(got, "<a ")).To(Equal(1),
			"only the whole link may become an anchor")
	})

	It("never emits a scheme outside the allowlist as an href", func() {
		for _, body := range []string{
			`[x](javascript:alert(1))`,
			`[x](data:text/html,z)`,
			`[x](vbscript:msgbox)`,
			`[x](file:///etc/passwd)`,
			`[x](//example.com)`,
		} {
			Expect(string(renderCardText(body))).NotTo(ContainSubstring("<a "),
				"body %q must not become an anchor", body)
		}
	})
})
