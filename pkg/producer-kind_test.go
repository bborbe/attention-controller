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

var _ = Describe("AvailableProducerKinds", func() {
	// The kind list has no HTTP surface: a rejected push names only the
	// *rejected* value and never the legal set, and AvailableProducerKinds is
	// a package var rather than anything a caller can read. This test is
	// therefore the only place the list is checkable.
	//
	// The row count is asserted rather than the new member alone, so a kind
	// added anywhere is a failure too — the collection is the schema's
	// transcription, and drift belongs in a review rather than in a diff.
	It("holds exactly the schema's five kinds, including manager", func() {
		Expect(pkg.AvailableProducerKinds).To(HaveLen(5))
		Expect(pkg.AvailableProducerKinds).To(ContainElement(pkg.ManagerProducerKind))

		Expect(pkg.ManagerProducerKind.Validate(context.Background())).To(BeNil())
	})

	// The negative control, and it is load-bearing: without it the assertions
	// above would also pass on a Validate that accepts every string, which is
	// the failure the AvailableXs + Contains pattern exists to prevent. A
	// constant added without collection membership fails here and nowhere
	// else.
	It("rejects a kind outside the collection", func() {
		err := pkg.ProducerKind("not-a-kind").Validate(context.Background())
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("unknown producerKind"))
	})
})
