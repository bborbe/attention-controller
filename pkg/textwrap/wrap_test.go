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

var _ = Describe("Wrap", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("wraps text at the given width", func() {
		lines, err := textwrap.Wrap(ctx, "the quick brown fox jumps over the lazy dog", 15)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"the quick brown",
			"fox jumps over",
			"the lazy dog",
		}))
	})

	It("keeps text on a single line when it already fits", func() {
		lines, err := textwrap.Wrap(ctx, "short enough", 40)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{"short enough"}))
	})

	It("fills every line up to the width before breaking", func() {
		lines, err := textwrap.Wrap(ctx, "aa bb cc dd", 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"aa bb",
			"cc dd",
		}))
	})

	It("collapses runs of whitespace to a single space", func() {
		lines, err := textwrap.Wrap(ctx, "aa   bb\t\tcc", 40)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{"aa bb cc"}))
	})

	It("trims leading and trailing whitespace", func() {
		lines, err := textwrap.Wrap(ctx, "  padded  ", 40)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{"padded"}))
	})

	It("starts a new line at every existing line break", func() {
		lines, err := textwrap.Wrap(ctx, "first\nsecond", 40)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"first",
			"second",
		}))
	})

	It("keeps blank lines so paragraph spacing survives", func() {
		lines, err := textwrap.Wrap(ctx, "first\n\nsecond", 40)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"first",
			"",
			"second",
		}))
	})

	It("returns a single empty line for empty input", func() {
		lines, err := textwrap.Wrap(ctx, "", 40)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{""}))
	})

	It("hard-splits a word longer than the width", func() {
		lines, err := textwrap.Wrap(ctx, "abcdefghij", 4)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"abcd",
			"efgh",
			"ij",
		}))
	})

	It("splits a long word without losing the surrounding words", func() {
		lines, err := textwrap.Wrap(ctx, "aa abcdefghij bb", 4)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"aa",
			"abcd",
			"efgh",
			"ij",
			"bb",
		}))
	})

	It("counts runes instead of bytes", func() {
		lines, err := textwrap.Wrap(ctx, "äöü äöü", 7)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"äöü äöü",
		}))
	})

	It("wraps a rune-heavy text at the rune width", func() {
		lines, err := textwrap.Wrap(ctx, "ää ää ää", 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"ää ää",
			"ää",
		}))
	})

	It("never returns a line longer than the width", func() {
		lines, err := textwrap.Wrap(
			ctx,
			"lorem ipsum dolor sit amet consetetur sadipscing elitr sed diam nonumy",
			11,
		)
		Expect(err).NotTo(HaveOccurred())
		for _, line := range lines {
			Expect(len([]rune(line))).To(BeNumerically("<=", 11))
		}
	})

	It("returns an error when width is zero", func() {
		lines, err := textwrap.Wrap(ctx, "text", 0)
		Expect(err).To(HaveOccurred())
		Expect(lines).To(BeNil())
	})

	It("returns an error when width is negative", func() {
		lines, err := textwrap.Wrap(ctx, "text", -3)
		Expect(err).To(HaveOccurred())
		Expect(lines).To(BeNil())
	})

	It("wraps at width one", func() {
		lines, err := textwrap.Wrap(ctx, "ab cd", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"a",
			"b",
			"c",
			"d",
		}))
	})

	DescribeTable("wraps at the requested width",
		func(text string, width int, expected []string) {
			lines, err := textwrap.Wrap(ctx, text, width)
			Expect(err).NotTo(HaveOccurred())
			Expect(lines).To(Equal(expected))
		},
		Entry("width 10", "aa bb cc dd ee", 10, []string{"aa bb cc", "dd ee"}),
		Entry("width 6", "aa bb cc", 6, []string{"aa bb", "cc"}),
		Entry("width 3", "aa bb", 3, []string{"aa", "bb"}),
	)

	It("returns a single empty line for whitespace-only input", func() {
		lines, err := textwrap.Wrap(ctx, "   \t ", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{""}))
	})

	It("wraps every paragraph of a multi-paragraph text", func() {
		lines, err := textwrap.Wrap(ctx, "aa bb\ncc dd", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{
			"aa",
			"bb",
			"cc",
			"dd",
		}))
	})

	It("keeps a word of exactly the width on one line", func() {
		lines, err := textwrap.Wrap(ctx, "abcde", 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{"abcde"}))
	})

	It("does not split a word that exactly fills the line", func() {
		lines, err := textwrap.Wrap(ctx, "aa bbbbb", 8)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{"aa bbbbb"}))
	})

	Describe("WrapString", func() {
		It("joins the wrapped lines with newlines", func() {
			result, err := textwrap.WrapString(ctx, "the quick brown fox", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("the quick\nbrown fox"))
		})

		It("returns the original text when it already fits", func() {
			result, err := textwrap.WrapString(ctx, "short", 40)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("short"))
		})

		It("propagates the width error", func() {
			result, err := textwrap.WrapString(ctx, "text", 0)
			Expect(err).To(HaveOccurred())
			Expect(result).To(BeEmpty())
		})

		It("round-trips text through Wrap and back", func() {
			input := "aa bb cc dd ee ff"
			wrapped, err := textwrap.WrapString(ctx, input, 5)
			Expect(err).NotTo(HaveOccurred())
			lines, err := textwrap.Wrap(ctx, wrapped, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(lines).To(Equal([]string{
				"aa bb",
				"cc dd",
				"ee ff",
			}))
		})
	})
})
