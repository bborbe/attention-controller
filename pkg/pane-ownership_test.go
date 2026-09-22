// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("StripStatusGlyph", func() {
	It("strips the status glyph Claude Code prefixes onto a pane title", func() {
		// Measured live: this is the real pane title, whose registry name is
		// `⚙ Let the Login Middleware …`. Both must strip to the same string or
		// a genuinely correct pane reads as unroutable.
		Expect(
			pkg.StripStatusGlyph("◐ ⚙ Let the Login Middleware Offer Alternative Authenticators"),
		).
			To(Equal("Let the Login Middleware Offer Alternative Authenticators"))
	})

	It("strips a leading glyph from a session's own name", func() {
		Expect(pkg.StripStatusGlyph("⚙ BRO-21473 Fix MDM Duplicate Merger Silent Failures")).
			To(Equal("BRO-21473 Fix MDM Duplicate Merger Silent Failures"))
	})

	It("leaves a name that starts with a word character untouched", func() {
		Expect(pkg.StripStatusGlyph("MDM Bugs")).To(Equal("MDM Bugs"))
	})

	It("keeps a leading slash, so a path-shaped name is not eaten", func() {
		Expect(pkg.StripStatusGlyph("/tmp/scratch")).To(Equal("/tmp/scratch"))
	})

	It("returns empty for empty", func() {
		Expect(pkg.StripStatusGlyph("")).To(Equal(""))
		Expect(pkg.StripStatusGlyph("   ")).To(Equal(""))
	})
})

var _ = Describe("OwnsPane", func() {
	var panes map[int]pkg.Pane

	BeforeEach(func() {
		panes = map[int]pkg.Pane{
			1140: {
				PaneID: 1140,
				Title:  "◐ ⚙ Let the Login Middleware Offer Alternative Authenticators",
			},
			493: {PaneID: 493, Title: "✳ MDM Bugs"},
			0:   {PaneID: 0, Title: "◑ Some Other Session"},
		}
	})

	It("rejects a pane that does not exist", func() {
		Expect(pkg.OwnsPane(panes, 9999, "MDM Bugs")).To(BeFalse())
	})

	It("accepts a pane whose glyph-stripped title is the session's current name", func() {
		Expect(
			pkg.OwnsPane(
				panes,
				1140,
				"⚙ Let the Login Middleware Offer Alternative Authenticators",
			),
		).
			To(BeTrue())
	})

	It("accepts pane 0, which is a real pane id rather than a sentinel", func() {
		// Measured live: `wezterm cli list` returns pane 0, so treating it as
		// "no pane" would silently drop a legitimate row.
		Expect(pkg.OwnsPane(panes, 0, "Some Other Session")).To(BeTrue())
	})

	It("rejects a pane that exists but belongs to another session", func() {
		// § Silence 7's case: the id resolves, so a naive existence check passes,
		// but it is another session's pane. This is the one check that separates a
		// correct implementation from a plausible one.
		Expect(
			pkg.OwnsPane(panes, 493, "Let the Login Middleware Offer Alternative Authenticators"),
		).
			To(BeFalse())
	})

	It("treats an unprovable pane as routable rather than unroutable", func() {
		// A mismatch is required, never a mere absence. Marking these unroutable
		// would strip provenance from every session the registry cannot name.
		Expect(pkg.OwnsPane(panes, 493, "")).To(BeTrue())

		panes[493] = pkg.Pane{PaneID: 493, Title: ""}
		Expect(pkg.OwnsPane(panes, 493, "MDM Bugs")).To(BeTrue())
	})
})
