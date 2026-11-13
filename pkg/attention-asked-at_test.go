// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// asked_at is the producer's own instant, and the store's only job is to keep
// it exactly as pushed. Its absence is equally load bearing: an item pushed
// without an ask time must read back with the field absent, never with a zero
// and never with the store's created_at — a fabricated instant hides the
// forwarding lag the field exists to expose.
var _ = Describe("asked_at", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore

	// now is the store's clock, fixed so every instant in this spec is one the
	// spec chose. The pushed ask times sit before it, so a store that re-stamped
	// the value would be caught rather than accidentally matching.
	now := stdtime.Date(2026, 11, 11, 10, 0, 1, 0, stdtime.UTC)

	pushRequest := func(producerID pkg.ProducerID, dedupKey pkg.DedupKey) pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			}),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("stores the pushed ask time unchanged and reads it back", func() {
		askedAt := libtime.DateTime(now.Add(-stdtime.Hour))
		request := pushRequest("session-a", "asked-at-roundtrip")
		request.AskedAt = &askedAt

		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		Expect(item.AskedAt).NotTo(BeNil())
		Expect(item.AskedAt.Time().Equal(now.Add(-stdtime.Hour))).To(BeTrue())

		// The store never substitutes its own clock.
		Expect(item.AskedAt.Time().Equal(now)).To(BeFalse())

		stored, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(stored.AskedAt).NotTo(BeNil())
		Expect(stored.AskedAt.Time().Equal(now.Add(-stdtime.Hour))).To(BeTrue())
	})

	It("leaves the ask time absent when the push carries none", func() {
		item, err := store.Push(ctx, pushRequest("session-a", "asked-at-absent"))
		Expect(err).To(BeNil())
		Expect(item.AskedAt).To(BeNil())

		raw, err := json.Marshal(item)
		Expect(err).To(BeNil())
		Expect(string(raw)).NotTo(ContainSubstring("asked_at"))
	})

	It("carries a re-push's ask time onto the existing open row", func() {
		t1 := libtime.DateTime(now.Add(-2 * stdtime.Hour))
		t2 := libtime.DateTime(now.Add(-stdtime.Hour))

		first := pushRequest("session-a", "asked-at-repush")
		first.AskedAt = &t1
		item, err := store.Push(ctx, first)
		Expect(err).To(BeNil())

		second := pushRequest("session-a", "asked-at-repush")
		second.AskedAt = &t2
		updated, err := store.Push(ctx, second)
		Expect(err).To(BeNil())

		Expect(updated.ItemID).To(Equal(item.ItemID))
		Expect(updated.AskedAt).NotTo(BeNil())
		Expect(updated.AskedAt.Time().Equal(t2.Time())).To(BeTrue())
	})

	It("clears the ask time when a re-push carries none", func() {
		askedAt := libtime.DateTime(now.Add(-stdtime.Hour))

		first := pushRequest("session-a", "asked-at-repush-cleared")
		first.AskedAt = &askedAt
		item, err := store.Push(ctx, first)
		Expect(err).To(BeNil())
		Expect(item.AskedAt).NotTo(BeNil())

		updated, err := store.Push(ctx, pushRequest("session-a", "asked-at-repush-cleared"))
		Expect(err).To(BeNil())

		Expect(updated.ItemID).To(Equal(item.ItemID))
		Expect(updated.AskedAt).To(BeNil())
	})

	// The zero instant is not a timestamp a producer can mean: a non-nil pointer
	// to a zero libtime.DateTime bypasses omitempty, so without this branch the
	// item stores 0001-01-01 as present and the board renders a fabricated
	// forwarding lag. Absent means absent, on the write path.
	It("drops a zero ask time rather than storing year one", func() {
		var zero libtime.DateTime
		req := pushRequest("session-a", "asked-at-zero")
		req.AskedAt = &zero

		item, err := store.Push(ctx, req)
		Expect(err).To(BeNil())
		Expect(item.AskedAt).To(BeNil())

		stored, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(stored.AskedAt).To(BeNil())
	})

	It("drops a zero ask time on the re-push path too", func() {
		askedAt := libtime.DateTime(now.Add(-stdtime.Hour))
		first := pushRequest("session-a", "asked-at-zero-repush")
		first.AskedAt = &askedAt
		item, err := store.Push(ctx, first)
		Expect(err).To(BeNil())
		Expect(item.AskedAt).NotTo(BeNil())

		var zero libtime.DateTime
		repush := pushRequest("session-a", "asked-at-zero-repush")
		repush.AskedAt = &zero
		updated, err := store.Push(ctx, repush)
		Expect(err).To(BeNil())
		Expect(updated.AskedAt).To(BeNil())
	})
})
