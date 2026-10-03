// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is an internal test package rather than the external `handler_test`
// the rest of the directory uses, and deliberately so: writeLegacyJumpPage is
// unexported, and the handler never reaches it with an escapable pane — the
// strconv.Atoi gate admits only digits, which escape to themselves — so the
// single-escape property can only be pinned by calling the writer directly. The
// specs register into the same ginkgo suite handler_suite_test.go runs.
package handler

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("writeLegacyJumpPage", func() {
	It("escapes the pane exactly once", func() {
		resp := httptest.NewRecorder()

		writeLegacyJumpPage(resp, http.StatusOK, toneOK, "h", "d", "<b>&</b>")

		body := resp.Body.String()
		Expect(body).To(ContainSubstring(`<p class="value mono">&lt;b&gt;&amp;&lt;/b&gt;</p>`))
		Expect(body).NotTo(ContainSubstring("&amp;lt;"))
		Expect(body).NotTo(ContainSubstring("<b>&</b>"))
	})
})
