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
