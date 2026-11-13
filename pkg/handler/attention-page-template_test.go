// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is an internal test package rather than the external `handler_test`
// the rest of the directory uses, and deliberately so: buildAttentionPage and
// the two placeholder constants are unexported, and the document they assemble
// is not reachable through the served page. The specs register into the same
// ginkgo suite handler_suite_test.go runs.
package handler

import (
	"crypto/sha256"
	"encoding/hex"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("buildAttentionPage", func() {
	// The placeholders are written out literally rather than referenced through
	// the production constants, because pinning them IS the contract: the assets
	// on disk carry these exact strings, and the byte-identity check outside the
	// container substitutes them by name. A test that reused the constants would
	// still pass after a rename that silently orphaned every asset.
	const (
		html   = `<style>/*__ATTENTION_PAGE_STYLES__*/</style><script>/*__ATTENTION_PAGE_SCRIPT__*/</script>`
		script = "var a = 1;"
	)

	It("substitutes both placeholders and leaves neither behind", func() {
		page := buildAttentionPage(html, script)

		Expect(page).To(ContainSubstring(attentionPageStyles))
		Expect(page).To(ContainSubstring(script))
		Expect(page).NotTo(ContainSubstring(attentionPageStylesPlaceholder))
		Expect(page).NotTo(ContainSubstring(attentionPageScriptPlaceholder))
	})

	It("inserts the script as literal text, tags and all", func() {
		// The claim is that the substitution is textual, so the assertion is that
		// the bytes land unchanged between their tags. That the RESULT survives
		// html/template unescaped is a property of the engine, covered by the
		// document-level digest below rather than by this function.
		Expect(buildAttentionPage(html, script)).To(ContainSubstring("<script>var a = 1;</script>"))
	})

	It("panics naming the stylesheet placeholder when it is absent", func() {
		Expect(func() {
			buildAttentionPage(
				`<style></style><script>/*__ATTENTION_PAGE_SCRIPT__*/</script>`,
				script,
			)
		}).To(PanicWith(ContainSubstring(attentionPageStylesPlaceholder)))
	})

	It("panics naming the stylesheet placeholder when it is duplicated", func() {
		Expect(func() {
			buildAttentionPage(
				`<style>/*__ATTENTION_PAGE_STYLES__*/</style>`+
					`<style>/*__ATTENTION_PAGE_STYLES__*/</style>`+
					`<script>/*__ATTENTION_PAGE_SCRIPT__*/</script>`,
				script,
			)
		}).To(PanicWith(ContainSubstring("occurs 2 times")))
	})

	It("panics naming the script placeholder when it is absent", func() {
		Expect(func() {
			buildAttentionPage(
				`<style>/*__ATTENTION_PAGE_STYLES__*/</style><script></script>`,
				script,
			)
		}).To(PanicWith(ContainSubstring(attentionPageScriptPlaceholder)))
	})

	It("panics naming the script placeholder when it is duplicated", func() {
		Expect(func() {
			buildAttentionPage(
				`<style>/*__ATTENTION_PAGE_STYLES__*/</style>`+
					`<script>/*__ATTENTION_PAGE_SCRIPT__*/</script>`+
					`<script>/*__ATTENTION_PAGE_SCRIPT__*/</script>`,
				script,
			)
		}).To(PanicWith(ContainSubstring("occurs 2 times")))
	})

	It("assembles the document the board served before the extraction", func() {
		// ⚠️ This pins what buildAttentionPage PRODUCED, not what the asset files
		// hold. The out-of-container byte-identity check reads the two files and
		// never runs the assembly, so an assembly that trimmed, re-encoded or
		// dropped a trailing newline from an asset would pass every file-level
		// criterion while serving something master never served. Regenerate this
		// digest only when the served page is meant to change.
		sum := sha256.Sum256([]byte(attentionPageTemplate))

		Expect(len(attentionPageTemplate)).To(Equal(131964))
		Expect(hex.EncodeToString(sum[:])).To(Equal(
			"4812c5491c68b48dbe4b47a2426833e5bd4e45ae16c05d487b701d1fa91fb731",
		))
	})
})
