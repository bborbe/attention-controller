// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	stdtime "time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("ManagerHoldBook", func() {
	var book pkg.ManagerHoldBook
	var now stdtime.Time

	hold := func(id string, heldAt stdtime.Time) pkg.ManagerHold {
		return pkg.ManagerHold{
			Record:  pkg.EventIngestRecord{ItemID: id, SessionID: "sid-1"},
			Manager: "mgr-1",
			Reason:  "goal manager",
			HeldAt:  heldAt,
		}
	}

	BeforeEach(func() {
		book = pkg.NewManagerHoldBook()
		now = stdtime.Now()
	})

	It("holds and reports one gate", func() {
		book.Hold("a", hold("a", now))

		Expect(book.Len()).To(Equal(1))
	})

	It("keeps the first hold's clock on a repeat", func() {
		book.Hold("a", hold("a", now.Add(-10*stdtime.Minute)))
		book.Hold("a", hold("a", now))

		expired := book.Expired(now, 5*stdtime.Minute)
		Expect(expired).To(HaveLen(1))
		Expect(expired[0].HeldAt).To(BeTemporally("==", now.Add(-10*stdtime.Minute)))
	})

	It("takes a hold out of the book", func() {
		book.Hold("a", hold("a", now))

		taken, ok := book.Take("a")
		Expect(ok).To(BeTrue())
		Expect(taken.Record.ItemID).To(Equal("a"))
		Expect(book.Len()).To(Equal(0))

		_, ok = book.Take("a")
		Expect(ok).To(BeFalse())
	})

	It("returns only the holds past their window, oldest first", func() {
		book.Hold("old", hold("old", now.Add(-10*stdtime.Minute)))
		book.Hold("older", hold("older", now.Add(-20*stdtime.Minute)))
		book.Hold("fresh", hold("fresh", now))

		expired := book.Expired(now, 5*stdtime.Minute)

		Expect(expired).To(HaveLen(2))
		Expect(expired[0].Record.ItemID).To(Equal("older"))
		Expect(expired[1].Record.ItemID).To(Equal("old"))
	})

	It("leaves the expired holds in the book for the caller to take", func() {
		book.Hold("a", hold("a", now.Add(-10*stdtime.Minute)))

		Expect(book.Expired(now, 5*stdtime.Minute)).To(HaveLen(1))
		Expect(book.Len()).To(Equal(1))
	})
})
