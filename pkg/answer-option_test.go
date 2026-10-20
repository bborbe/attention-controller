// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// `exclusive` marks the do-nothing escape of a combinable menu — the option the
// board clears every other pick for, and clears in turn when any other is
// picked. These specs cover the rule the schema states for it: at most one
// option may carry it, and the flag is accepted on a single-pick unit rather
// than rejected, because a rejected item is dropped by the pusher and a
// redundant flag must not delete a card.
var _ = Describe("AnswerOption exclusive", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("accepts one exclusive option on a multiple-pick unit", func() {
		options := pkg.AnswerOptions{
			{Label: "/vault-cli:reflect"},
			{Label: "/coding:self-improve"},
			{Label: "nothing — close", Exclusive: true},
		}
		Expect(options.ValidateForCardinality(ctx, pkg.MultipleAnswerCardinality)).To(Succeed())
	})

	It("rejects two exclusive options, which describe two escapes", func() {
		options := pkg.AnswerOptions{
			{Label: "nothing — close", Exclusive: true},
			{Label: "keep the session open", Exclusive: true},
		}
		Expect(options.ValidateForCardinality(ctx, pkg.MultipleAnswerCardinality)).ToNot(Succeed())
	})

	It("accepts an exclusive option on a single-pick unit, where it is redundant", func() {
		options := pkg.AnswerOptions{
			{Label: "/vault-cli:reflect"},
			{Label: "nothing — close", Exclusive: true},
		}
		Expect(options.ValidateForCardinality(ctx, pkg.SingleAnswerCardinality)).To(Succeed())
	})

	It("accepts a list carrying no exclusive option", func() {
		options := pkg.AnswerOptions{
			{Label: "/vault-cli:reflect"},
			{Label: "/coding:self-improve"},
		}
		Expect(options.ValidateForCardinality(ctx, pkg.MultipleAnswerCardinality)).To(Succeed())
	})
})
