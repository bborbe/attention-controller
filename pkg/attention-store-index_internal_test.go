// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// This file pins the dedup index key's contract against the built-marker.
//
// It lives in `package pkg` rather than `package pkg_test` because
// `dedupIndexKeyOf` is unexported — the same reason `expiry_internal_test.go`
// lives here. Both packages compile into the same test binary, so these specs
// register with the same suite the rest of the package runs under.
//
// ⚠️ The invariant is load-bearing rather than decorative: `dedupIndexEntryCount`
// skips the `"!"` built-marker BY NAME, so if `ProducerID`/`DedupKey` ever
// stopped being `validation.NotEmptyString`, a real key could equal `"!"` and be
// silently under-counted.
var _ = Describe("dedup index key", func() {
	It("cannot produce a real key equal to the built-marker", func() {
		Expect(dedupIndexKeyOf(ProducerID("a"), DedupKey("b"))).NotTo(Equal("!"))
	})
})
