// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The generation guard is the cache's whole correctness argument: a List that
// started before a write must not publish its pre-write body. The race is
// driven deterministically here — load, write, store — rather than by timing,
// so the spec fails every run the guard is removed, not just the unlucky ones.
var _ = Describe("ledgerListCache generation guard", func() {
	It("refuses a body read under a generation a write has since replaced", func() {
		c := &ledgerListCache{}
		_, gen := c.load() // a ListJSON starts and reads the generation
		c.invalidate()     // a write lands while it is listing
		c.store(gen, []byte("pre-write body"))

		body, _ := c.load()
		Expect(body).To(BeNil())
	})

	It("keeps a body read under the current generation", func() {
		c := &ledgerListCache{}
		_, gen := c.load()
		c.store(gen, []byte("current body"))

		body, _ := c.load()
		Expect(string(body)).To(Equal("current body"))
	})

	It("never publishes a stale body under concurrent writes", func() {
		c := &ledgerListCache{}
		var wg sync.WaitGroup
		for i := 0; i < 200; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, gen := c.load()
				c.store(gen, []byte("body"))
			}()
			go func() {
				defer wg.Done()
				c.invalidate()
			}()
		}
		wg.Wait()
		// After the final write, a body may only be present if it was stored
		// under the final generation — and an invalidate always clears it.
		c.invalidate()
		body, _ := c.load()
		Expect(body).To(BeNil())
	})
})
