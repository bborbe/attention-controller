// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The ingest tails every event log every two seconds. On 2026-11-16 that was
// 6676 files and 162 MB, and reading each in full to discover that nothing had
// been appended was 55% of the process's allocation and 17% of its CPU. These
// specs pin the property that made the read cheap: a file already at its cursor
// is not read at all, and a file that grew is read from the cursor rather than
// from the beginning.
var _ = Describe("readRootFileFrom", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	write := func(name string, body string) {
		Expect(os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)).To(Succeed())
	}

	read := func(name string, offset int64) ([]byte, int64, error) {
		root, err := os.OpenRoot(dir)
		Expect(err).To(BeNil())
		defer func() { Expect(root.Close()).To(Succeed()) }()
		return readRootFileFrom(root, name, offset)
	}

	It("reads nothing from a file already at its cursor", func() {
		write("a.events.jsonl", "one\ntwo\n")
		content, start, err := read("a.events.jsonl", int64(len("one\ntwo\n")))
		Expect(err).To(BeNil())
		// The whole point: no content, and the cursor unmoved. A read that
		// returned the file again would satisfy the "no new records" assertion
		// below while still paying for the bytes.
		Expect(content).To(BeEmpty())
		Expect(start).To(Equal(int64(len("one\ntwo\n"))))
	})

	It("reads only the bytes appended past the cursor", func() {
		write("a.events.jsonl", "one\ntwo\n")
		content, start, err := read("a.events.jsonl", int64(len("one\n")))
		Expect(err).To(BeNil())
		Expect(string(content)).To(Equal("two\n"))
		// start is what the caller measures record offsets from, so it must be
		// the cursor, not zero.
		Expect(start).To(Equal(int64(len("one\n"))))
	})

	It("re-reads from the beginning when the file is shorter than the cursor", func() {
		write("a.events.jsonl", "one\n")
		content, start, err := read("a.events.jsonl", 4096)
		Expect(err).To(BeNil())
		// A truncated or replaced log: start 0 is how the caller learns it
		// rewound, and applies the settled-ts mark to what it re-reads.
		Expect(start).To(Equal(int64(0)))
		Expect(string(content)).To(Equal("one\n"))
	})

	It("reads the whole file when the cursor is zero", func() {
		write("a.events.jsonl", "one\ntwo\n")
		content, start, err := read("a.events.jsonl", 0)
		Expect(err).To(BeNil())
		Expect(start).To(Equal(int64(0)))
		Expect(string(content)).To(Equal("one\ntwo\n"))
	})
})
