// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("managerIsAsk verb scoping", func() {
	It("reads exactly the four ask verbs as an allow-list", func() {
		Expect(managerAskVerbs).To(ConsistOf("pick", "approve:", "review:", "you run:"))
		Expect(len(managerAskVerbs)).To(Equal(4))
	})

	DescribeTable("the four ask verbs read as an ask",
		func(body string) {
			Expect(managerIsAsk(body)).To(BeTrue())
		},
		Entry("pick", "pick — 1. X · 2. Y"),
		Entry("approve:", "approve: run it"),
		Entry("review:", "review: the diff"),
		Entry("you run:", "you run: make deploy"),
	)

	// ⚠️ `foo —`, `bar:` and `baz —` are invented verbs that appear in no
	// allow-list: a single-prefix denylist would still gate them, so these
	// entries are what make "every other verb reads as no ask" discriminating.
	// `you decide` is the closer contract's category label, not an ask verb.
	DescribeTable("invented verbs and non-ask panels read as no ask",
		func(body string) {
			Expect(managerIsAsk(body)).To(BeFalse())
		},
		Entry("foo", "foo — do something"),
		Entry("bar", "bar: do something"),
		Entry("baz", "baz — do something"),
		Entry("you decide is a category label, not an ask verb", "you decide"),
		Entry("nothing", "nothing"),
		Entry("later (on …)", "later (on the deploy): nothing to do"),
	)
})
