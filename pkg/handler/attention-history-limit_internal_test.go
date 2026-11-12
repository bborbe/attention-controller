// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The cap is what stops one request from materialising an unbounded page, which
// is what set the process footprint: a 50,000-row read allocates roughly 200 MB
// before it is encoded, and two overlapping reads produced a 386 MB peak against
// a 23 MB live heap (measured 2026-11-04).
var _ = Describe("clampHistoryLimit", func() {
	DescribeTable("bounds one page",
		func(requested, want int) {
			Expect(clampHistoryLimit(requested)).To(Equal(want))
		},
		Entry("keeps a page under the cap", 250, 250),
		Entry("keeps the default", historyDefaultLimit, historyDefaultLimit),
		Entry("keeps the cap itself", historyMaxLimit, historyMaxLimit),
		Entry("clamps a page over the cap", 50000, historyMaxLimit),
		Entry("clamps the whole-store request", 0, historyMaxLimit),
		Entry("clamps a negative request", -1, historyMaxLimit),
	)
})
