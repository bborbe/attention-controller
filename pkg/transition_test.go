// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("AvailableTransitions", func() {
	// The table is the schema's transcription and the answer fix deliberately
	// does not touch it. `closed` is terminal: admitting `closed -> answered`
	// would let an arm answer an item that has already left the queue, which is
	// the opposite of what naming that failure is for.
	//
	// The row count is asserted rather than the single absent row alone, so a row
	// added anywhere is a failure too — the table is a transcription of the
	// schema page, and any drift belongs in a review rather than in a diff.
	It("holds exactly the schema's three rows, with closed terminal", func() {
		Expect(pkg.AvailableTransitions).To(HaveLen(3))

		_, found := pkg.AvailableTransitions.Find(pkg.ClosedState, pkg.AnsweredState)
		Expect(found).To(BeFalse())

		// And the refusal is the schema's own sentinel rather than a new error:
		// a caller that reads the failure as ErrIllegalTransition must keep
		// finding it, because that is the value the handler classifies on.
		err := pkg.ValidateTransition(context.Background(), pkg.ClosedState, pkg.AnsweredState)
		Expect(err).NotTo(BeNil())
		Expect(errors.Is(err, pkg.ErrIllegalTransition)).To(BeTrue())
	})
})
