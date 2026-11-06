// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	stderrors "errors"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("Rating", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore

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
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func(dedupKey pkg.DedupKey) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("session-" + dedupKey.String()),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-" + dedupKey.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "pick",
			Payload:         "was this card worth it?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// withoutRating returns the item's JSON with the two rating fields zeroed,
	// so a before/after comparison asserts that NOTHING ELSE changed — state,
	// answer, flag pair, delivery fields and every field added later.
	withoutRating := func(item *pkg.Item) string {
		clone := *item
		clone.Rating = pkg.NoRating
		clone.RatedAt = nil
		b, err := json.Marshal(clone)
		Expect(err).To(BeNil())
		return string(b)
	}

	get := func(itemID pkg.ItemID) *pkg.Item {
		item, err := store.Get(ctx, itemID)
		Expect(err).To(BeNil())
		return item
	}

	It("stores up, clears on the same value, and switches to the other", func() {
		item := push("rate-seq")

		_, err := store.Rate(ctx, item.ItemID, pkg.UpRating)
		Expect(err).To(BeNil())
		read := get(item.ItemID)
		Expect(read.Rating).To(Equal(pkg.UpRating))
		Expect(read.RatedAt).NotTo(BeNil())

		_, err = store.Rate(ctx, item.ItemID, pkg.NoRating)
		Expect(err).To(BeNil())
		read = get(item.ItemID)
		Expect(read.Rating).To(Equal(pkg.NoRating))
		Expect(read.RatedAt).To(BeNil())

		_, err = store.Rate(ctx, item.ItemID, pkg.DownRating)
		Expect(err).To(BeNil())
		Expect(get(item.ItemID).Rating).To(Equal(pkg.DownRating))

		_, err = store.Rate(ctx, item.ItemID, pkg.UpRating)
		Expect(err).To(BeNil())
		Expect(get(item.ItemID).Rating).To(Equal(pkg.UpRating))
	})

	It("changes nothing but the rating on an open item", func() {
		item := push("rate-open")
		_, err := store.Flag(ctx, item.ItemID, "asked the wrong team")
		Expect(err).To(BeNil())
		before := get(item.ItemID)

		_, err = store.Rate(ctx, item.ItemID, pkg.DownRating)
		Expect(err).To(BeNil())

		after := get(item.ItemID)
		Expect(after.Rating).To(Equal(pkg.DownRating))
		Expect(after.State).To(Equal(pkg.OpenState))
		Expect(after.AnsweredAt).To(BeNil())
		Expect(after.FlagReason).To(Equal("asked the wrong team"))
		Expect(withoutRating(after)).To(Equal(withoutRating(before)))
	})

	It("rates an answered item and leaves the answer untouched", func() {
		item := push("rate-answered")
		_, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", nil, nil, nil)
		Expect(err).To(BeNil())
		before := get(item.ItemID)
		Expect(before.State).To(Equal(pkg.AnsweredState))

		_, err = store.Rate(ctx, item.ItemID, pkg.UpRating)
		Expect(err).To(BeNil())

		after := get(item.ItemID)
		Expect(after.Rating).To(Equal(pkg.UpRating))
		Expect(withoutRating(after)).To(Equal(withoutRating(before)))
	})

	It("rates a closed item and leaves it closed", func() {
		item := push("rate-closed")
		_, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", nil, nil, nil)
		Expect(err).To(BeNil())
		_, err = store.Close(ctx, item.ItemID, "", nil)
		Expect(err).To(BeNil())
		before := get(item.ItemID)
		Expect(before.State).To(Equal(pkg.ClosedState))

		_, err = store.Rate(ctx, item.ItemID, pkg.DownRating)
		Expect(err).To(BeNil())

		after := get(item.ItemID)
		Expect(after.Rating).To(Equal(pkg.DownRating))
		Expect(withoutRating(after)).To(Equal(withoutRating(before)))
	})

	It("leaves the read order unchanged", func() {
		first := push("rate-order-1")
		second := push("rate-order-2")
		third := push("rate-order-3")
		ids := func() []pkg.ItemID {
			items, err := store.Read(ctx)
			Expect(err).To(BeNil())
			result := make([]pkg.ItemID, 0, len(items))
			for _, item := range items {
				result = append(result, item.ItemID)
			}
			return result
		}
		before := ids()
		Expect(before).To(ConsistOf(first.ItemID, second.ItemID, third.ItemID))

		_, err := store.Rate(ctx, second.ItemID, pkg.DownRating)
		Expect(err).To(BeNil())

		Expect(ids()).To(Equal(before))
	})

	It("refuses a value that is not up, down or empty and writes nothing", func() {
		item := push("rate-invalid")
		for _, rating := range []pkg.Rating{"Up", "thumbs-up", " up", "1"} {
			_, err := store.Rate(ctx, item.ItemID, rating)
			Expect(err).NotTo(BeNil())
			Expect(stderrors.Is(err, pkg.ErrRatingInvalid)).To(BeTrue())
			read := get(item.ItemID)
			Expect(read.Rating).To(Equal(pkg.NoRating))
			Expect(read.RatedAt).To(BeNil())
		}
	})

	It("answers ErrItemNotFound for an unknown id", func() {
		_, err := store.Rate(ctx, pkg.ItemID("no-such-item"), pkg.UpRating)
		Expect(err).NotTo(BeNil())
		Expect(stderrors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())
	})
})
