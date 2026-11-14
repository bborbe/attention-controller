// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// fixedLiveness reports a scripted liveness verdict.
type fixedLiveness struct {
	live bool
}

func (l *fixedLiveness) IsLive(_ context.Context, _ string) bool {
	return l.live
}

var _ = Describe("ReleaseExpiredHoldsJob", func() {
	var ctx context.Context
	var book pkg.ManagerHoldBook
	var sink *recordingSink
	var now stdtime.Time

	hold := func(id string, heldAt stdtime.Time) pkg.ManagerHold {
		return pkg.ManagerHold{
			Record:  pkg.EventIngestRecord{ItemID: id, SessionID: "sid-1", Kind: "permission"},
			Manager: "mgr-1",
			HeldAt:  heldAt,
		}
	}

	job := func(live bool) pkg.ReleaseExpiredHoldsJob {
		clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
		return pkg.NewReleaseExpiredHoldsJob(
			book,
			sink,
			&fixedLiveness{live: live},
			5*stdtime.Minute,
			stdtime.Hour,
			clock,
		)
	}

	BeforeEach(func() {
		ctx = context.Background()
		book = pkg.NewManagerHoldBook()
		sink = &recordingSink{}
		now = stdtime.Now()
	})

	It("posts a hold whose window expired while its session is live", func() {
		book.Hold("a", hold("a", now.Add(-10*stdtime.Minute)))

		Expect(job(true).ReleaseOnce(ctx)).To(Succeed())

		Expect(sink.opened).To(Equal([]string{"a"}))
		Expect(book.Len()).To(Equal(0))
	})

	It("drops a hold whose session exited before the window ran out", func() {
		book.Hold("a", hold("a", now.Add(-10*stdtime.Minute)))

		Expect(job(false).ReleaseOnce(ctx)).To(Succeed())

		Expect(sink.opened).To(BeEmpty())
		Expect(book.Len()).To(Equal(0))
	})

	It("leaves a hold alone until its window expires", func() {
		book.Hold("a", hold("a", now))

		Expect(job(true).ReleaseOnce(ctx)).To(Succeed())

		Expect(sink.opened).To(BeEmpty())
		Expect(book.Len()).To(Equal(1))
	})

	It("keeps the hold when the store fails, so the next pass retries it", func() {
		book.Hold("a", hold("a", now.Add(-10*stdtime.Minute)))
		sink.err = fmt.Errorf("store down")

		Expect(job(true).ReleaseOnce(ctx)).NotTo(Succeed())
		Expect(book.Len()).To(Equal(1))

		sink.err = nil
		Expect(job(true).ReleaseOnce(ctx)).To(Succeed())
		Expect(sink.opened).To(Equal([]string{"a"}))
		Expect(book.Len()).To(Equal(0))
	})
})
