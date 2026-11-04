// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// heapInUse forces two GC cycles and returns the live heap, so a value that
// still pins its source file shows up as retained bytes.
func heapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

// retainedSince returns how far the live heap grew above before, or 0.
func retainedSince(before uint64) uint64 {
	after := heapInUse()
	if after < before {
		return 0
	}
	return after - before
}

// allocationDuring returns how many bytes were allocated while f ran.
func allocationDuring(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

var _ = Describe("frontmatter reads copy the block, never the file", func() {
	// A task file averages ~12 KB and the startup pass parses 10,132 of them.
	// Reading the whole file per frontmatter key is what put the index pass at
	// ~730 MB of allocation and the process footprint at 605 MB sixteen seconds
	// after start, so the bound is pinned against a body far larger than any real
	// file: 32 MB of body may cost nothing beyond the block.
	const bodySize = 32 << 20
	const copyBound = 1 << 20

	content := func(newline string) []byte {
		return []byte("---" + newline + "status: in_progress" + newline +
			"goals:" + newline + "    - '[[Some Goal]]'" + newline + "---" + newline +
			strings.Repeat("x", bodySize))
	}

	It("allocates for the block only, not the body", func() {
		body := content("\n")
		allocated := allocationDuring(func() {
			frontmatterValue(body, "status")
			frontmatterList(body, "goals")
		})
		Expect(allocated).To(BeNumerically("<", copyBound),
			"a frontmatter read must not copy the file body")
	})

	It("reads the same values from a CRLF file", func() {
		body := content("\r\n")
		Expect(frontmatterValue(body, "status")).To(Equal("in_progress"))
		Expect(frontmatterList(body, "goals")).To(Equal([]string{"- '[[Some Goal]]'"}))
	})

	It("reports nothing for a file with no frontmatter", func() {
		body := []byte("no frontmatter here\n")
		Expect(frontmatterValue(body, "status")).To(BeEmpty())
		Expect(frontmatterList(body, "goals")).To(BeEmpty())
	})

	It("reports nothing for an unterminated block", func() {
		body := []byte("---\nstatus: in_progress\n")
		Expect(frontmatterValue(body, "status")).To(BeEmpty())
	})
})

var _ = Describe("frontmatter values do not pin their source file", func() {
	// 32 MB of body below the frontmatter block: a value that pins the file
	// retains at least this much, one that does not retains a few bytes.
	const bodySize = 32 << 20
	const pinBound = 4 << 20

	content := func() []byte {
		return []byte("---\nstatus: in_progress\ngoals:\n    - '[[Some Goal]]'\n---\n" +
			strings.Repeat("x", bodySize))
	}

	It("frontmatterValue retains only the value", func() {
		before := heapInUse()
		value := frontmatterValue(content(), "status")
		retained := retainedSince(before)
		Expect(value).To(Equal("in_progress"))
		Expect(retained).To(BeNumerically("<", pinBound))
		runtime.KeepAlive(value)
	})

	It("frontmatterList retains only the entries", func() {
		before := heapInUse()
		entries := frontmatterList(content(), "goals")
		retained := retainedSince(before)
		Expect(entries).To(Equal([]string{"- '[[Some Goal]]'"}))
		Expect(retained).To(BeNumerically("<", pinBound))
		runtime.KeepAlive(entries)
	})
})
