// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The weekly misroute count is the reader a flag exists for, so the specs assert
// the two properties that make it falsifiable rather than merely present: a
// repeated (producer, reason) pair must read as a count above one, and a card
// flagged outside the requested week must be absent. A response that emitted one
// row per flagged card, or that ignored the window, would satisfy a spec that
// only asserted the rows it expected to see.
var _ = Describe("Attention misroutes over HTTP", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var currentDateTime libtime.CurrentDateTime
	var handlerUnderTest http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		// The clock is held rather than inlined: Flag stamps FlaggedAt from the
		// store's own clock, so a spec that could not move that clock could not
		// place a card in a week.
		currentDateTime = libtime.NewCurrentDateTime()
		currentDateTime.SetNow(libtime.DateTime(at(27, 10)))

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			currentDateTime,
			libtime.Duration(15*60*1e9),
		)
		handlerUnderTest = handler.NewAttentionMisroutesHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func(producerID string, dedupKey string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID(producerID),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "pick",
			Payload:         "Which surface should the answer land on?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	flagAt := func(item *pkg.Item, reason string, when stdtime.Time) {
		currentDateTime.SetNow(libtime.DateTime(when))
		_, err := store.Flag(ctx, item.ItemID, reason)
		Expect(err).To(BeNil())
	}

	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodGet,
			"/api/1.0/attention/misroutes"+query,
			nil,
		)
		rec := httptest.NewRecorder()
		handlerUnderTest.ServeHTTP(rec, req)
		return rec
	}

	countOf := func(rec *httptest.ResponseRecorder) pkg.MisrouteCount {
		Expect(rec.Code).To(Equal(http.StatusOK))
		var count pkg.MisrouteCount
		Expect(json.Unmarshal(rec.Body.Bytes(), &count)).To(BeNil())
		return count
	}

	It("counts a repeated (producer, reason) pair as one row of 2", func() {
		first := push("producer-a", "gate-1")
		second := push("producer-b", "gate-2")
		third := push("producer-a", "gate-3")

		flagAt(first, "routing", at(27, 10))
		flagAt(third, "routing", at(28, 10))
		flagAt(second, "noise", at(27, 11))

		count := countOf(get("?week=2026W44"))
		Expect(count.Week).To(Equal("2026W44"))
		Expect(count.Rows).To(HaveLen(2))
		Expect(count.Rows[0].ProducerID).To(Equal(pkg.ProducerID("producer-a")))
		Expect(count.Rows[0].Reason).To(Equal("routing"))
		Expect(count.Rows[0].Count).To(Equal(2))
		Expect(count.Rows[1].ProducerID).To(Equal(pkg.ProducerID("producer-b")))
		Expect(count.Rows[1].Count).To(Equal(1))
	})

	It("states the window it covers as calendar dates", func() {
		push("producer-a", "gate-1")

		count := countOf(get("?week=2026W44"))
		Expect(count.Start.String()).To(Equal("2026-10-26"))
		Expect(count.End.String()).To(Equal("2026-11-01"))
	})

	// ⚠️ Two different weeks in the same run is what defeats a response
	// hardcoded for the one week a fixture happens to flag: it cannot satisfy
	// both date ranges and both row sets.
	It("answers a second week with that week's own rows and dates", func() {
		first := push("producer-a", "gate-1")
		outside := push("producer-c", "gate-2")

		flagAt(first, "routing", at(27, 10))
		flagAt(outside, "routing", at(20, 10))

		inWeek := countOf(get("?week=2026W44"))
		Expect(inWeek.Start.String()).To(Equal("2026-10-26"))
		Expect(inWeek.End.String()).To(Equal("2026-11-01"))
		Expect(inWeek.Rows).To(HaveLen(1))
		Expect(inWeek.Rows[0].ProducerID).To(Equal(pkg.ProducerID("producer-a")))

		previousWeek := countOf(get("?week=2026W43"))
		Expect(previousWeek.Start.String()).To(Equal("2026-10-19"))
		Expect(previousWeek.End.String()).To(Equal("2026-10-25"))
		Expect(previousWeek.Rows).To(HaveLen(1))
		Expect(previousWeek.Rows[0].ProducerID).To(Equal(pkg.ProducerID("producer-c")))
	})

	// ⚠️ An empty week answers with an empty array rather than null, so the
	// quiet-week reading is distinguishable from a failed one.
	It("answers a card-free week with an empty row list", func() {
		push("producer-a", "gate-1")

		rec := get("?week=2026W20")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(`"rows":[]`))
	})

	It("rejects a request with no week", func() {
		Expect(get("").Code).To(Equal(http.StatusBadRequest))
	})

	DescribeTable("rejects a malformed week",
		func(query string) {
			Expect(get(query).Code).To(Equal(http.StatusBadRequest))
		},
		Entry("dashes", "?week=2026-44"),
		Entry("short", "?week=2026W4"),
		Entry("week zero", "?week=2026W00"),
		Entry("week the year does not have", "?week=2025W53"),
	)
})

// at builds an instant inside the ISO week 44 of 2026 (2026-10-26 .. 2026-11-01)
// for the days the specs use, in the local zone the store stamps in.
func at(day int, hour int) stdtime.Time {
	return stdtime.Date(2026, stdtime.October, day, hour, 0, 0, 0, stdtime.Local)
}
