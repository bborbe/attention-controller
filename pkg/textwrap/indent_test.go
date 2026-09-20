// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package textwrap_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/textwrap"
)

var _ = Describe("Indent", func() {
	It("prefixes every non-empty line", func() {
		result := textwrap.Indent("first\nsecond", "> ")
		Expect(result).To(Equal("> first\n> second"))
	})

	It("leaves blank lines untouched", func() {
		result := textwrap.Indent("first\n\nsecond", "> ")
		Expect(result).To(Equal("> first\n\n> second"))
	})

	It("leaves whitespace-only lines untouched", func() {
		result := textwrap.Indent("first\n   \nsecond", "> ")
		Expect(result).To(Equal("> first\n   \n> second"))
	})

	It("indents a single line", func() {
		result := textwrap.Indent("only", "    ")
		Expect(result).To(Equal("    only"))
	})

	It("returns an empty string for empty input", func() {
		result := textwrap.Indent("", "> ")
		Expect(result).To(Equal(""))
	})
})

var _ = Describe("IndentLines", func() {
	It("prefixes every non-empty entry", func() {
		result := textwrap.IndentLines([]string{"a", "b"}, "- ")
		Expect(result).To(Equal([]string{"- a", "- b"}))
	})

	It("leaves blank entries untouched", func() {
		result := textwrap.IndentLines([]string{"a", "", "b"}, "- ")
		Expect(result).To(Equal([]string{"- a", "", "- b"}))
	})

	It("does not modify the input slice", func() {
		input := []string{"a", "b"}
		_ = textwrap.IndentLines(input, "- ")
		Expect(input).To(Equal([]string{"a", "b"}))
	})

	It("returns an empty slice for empty input", func() {
		result := textwrap.IndentLines([]string{}, "- ")
		Expect(result).To(BeEmpty())
	})

	It("supports an empty prefix", func() {
		result := textwrap.IndentLines([]string{"a", "b"}, "")
		Expect(result).To(Equal([]string{"a", "b"}))
	})
})

var _ = Describe("Dedent", func() {
	It("removes the common leading whitespace", func() {
		result := textwrap.Dedent("    first\n    second")
		Expect(result).To(Equal("first\nsecond"))
	})

	It("removes only the shared part of the indentation", func() {
		result := textwrap.Dedent("    first\n        second")
		Expect(result).To(Equal("first\n    second"))
	})

	It("ignores blank lines when computing the common prefix", func() {
		result := textwrap.Dedent("    first\n\n    second")
		Expect(result).To(Equal("first\n\nsecond"))
	})

	It("returns the text unchanged when a line has no indentation", func() {
		input := "first\n    second"
		result := textwrap.Dedent(input)
		Expect(result).To(Equal(input))
	})

	It("removes tab indentation", func() {
		result := textwrap.Dedent("\tfirst\n\tsecond")
		Expect(result).To(Equal("first\nsecond"))
	})

	It("removes the shared part of mixed indentation", func() {
		result := textwrap.Dedent("\t\tfirst\n\t\t\tsecond")
		Expect(result).To(Equal("first\n\tsecond"))
	})

	It("dedents a single indented line", func() {
		result := textwrap.Dedent("    only")
		Expect(result).To(Equal("only"))
	})

	It("returns an empty string for empty input", func() {
		result := textwrap.Dedent("")
		Expect(result).To(Equal(""))
	})

	It("returns a blank-only text unchanged", func() {
		input := "   \n\t"
		result := textwrap.Dedent(input)
		Expect(result).To(Equal(input))
	})

	It("removes a mixed tab and space prefix", func() {
		result := textwrap.Dedent(" \tfirst\n \tsecond")
		Expect(result).To(Equal("first\nsecond"))
	})

	It("keeps the indentation part that is not shared", func() {
		result := textwrap.Dedent("\t first\n\t  second")
		Expect(result).To(Equal("first\n second"))
	})

	It("is idempotent", func() {
		once := textwrap.Dedent("    first\n    second")
		Expect(textwrap.Dedent(once)).To(Equal(once))
	})

	It("round-trips text through Indent and back", func() {
		input := "first\n\nsecond"
		Expect(textwrap.Dedent(textwrap.Indent(input, "    "))).To(Equal(input))
	})

	It("round-trips wrapped lines through IndentLines and back", func() {
		input := []string{"first", "second"}
		indented := strings.Join(textwrap.IndentLines(input, "\t"), "\n")
		Expect(textwrap.Dedent(indented)).To(Equal("first\nsecond"))
	})
})
