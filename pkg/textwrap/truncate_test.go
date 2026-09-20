// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package textwrap_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/textwrap"
)

var _ = Describe("Truncate", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("returns text that already fits unchanged", func() {
		result, err := textwrap.Truncate(ctx, "short", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("short"))
	})

	It("returns text of exactly the width unchanged", func() {
		result, err := textwrap.Truncate(ctx, "exact", 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("exact"))
	})

	It("shortens text and appends the ellipsis", func() {
		result, err := textwrap.Truncate(ctx, "abcdefghij", 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("abcd…"))
	})

	It("counts the ellipsis toward the width", func() {
		result, err := textwrap.Truncate(ctx, "abcdefghij", 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(len([]rune(result))).To(Equal(5))
	})

	It("returns only the ellipsis when width equals its length", func() {
		result, err := textwrap.Truncate(ctx, "abcdefghij", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("…"))
	})

	It("counts runes instead of bytes when shortening", func() {
		result, err := textwrap.Truncate(ctx, "äöüäöü", 4)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("äöü…"))
	})

	It("never returns more runes than the width", func() {
		for width := 1; width <= 12; width++ {
			result, err := textwrap.Truncate(ctx, "lorem ipsum dolor", width)
			Expect(err).NotTo(HaveOccurred())
			Expect(len([]rune(result))).To(BeNumerically("<=", width))
		}
	})

	It("returns an error when width is zero", func() {
		result, err := textwrap.Truncate(ctx, "text", 0)
		Expect(err).To(HaveOccurred())
		Expect(result).To(BeEmpty())
	})

	It("returns an error when width is negative", func() {
		result, err := textwrap.Truncate(ctx, "text", -1)
		Expect(err).To(HaveOccurred())
		Expect(result).To(BeEmpty())
	})

	Describe("TruncateLines", func() {
		It("truncates every line", func() {
			result, err := textwrap.TruncateLines(
				ctx,
				[]string{"abcdefghij", "short", "klmnopqrst"},
				5,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal([]string{"abcd…", "short", "klmn…"}))
		})

		It("returns an empty slice for empty input", func() {
			result, err := textwrap.TruncateLines(ctx, []string{}, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeEmpty())
		})

		It("does not modify the input slice", func() {
			input := []string{"abcdefghij"}
			_, err := textwrap.TruncateLines(ctx, input, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(input).To(Equal([]string{"abcdefghij"}))
		})

		It("propagates the width error", func() {
			result, err := textwrap.TruncateLines(ctx, []string{"text"}, 0)
			Expect(err).To(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})
})
