// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("AttentionStore SweepAnswered", func() {
	var ctx context.Context
	var db libkv.DB
	var clock libtime.CurrentDateTime
	var store pkg.AttentionStore
	var liveness *mocks.SessionLivenessChecker

	const (
		heartbeatWindow = libtime.Duration(15 * 60 * 1e9)
		oneHour         = libtime.Duration(60 * 60 * 1e9)
		twoHours        = libtime.Duration(2 * 60 * 60 * 1e9)
	)

	// answer pushes a fresh ask and answers it, returning its id.
	answer := func(producer pkg.ProducerID, dedup pkg.DedupKey) pkg.ItemID {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      producer,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producer.String()),
			DedupKey:        dedup,
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		_, err = store.Answer(ctx, item.ItemID, "arm", "session", pkg.Decision(""), nil, nil, nil)
		Expect(err).To(BeNil())
		return item.ItemID
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		liveness = &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)

		// The clock is held and moved rather than frozen, because the sweep's whole
		// subject is age: a spec that cannot advance time cannot express "older
		// than the max age" at all.
		clock = libtime.NewCurrentDateTime()
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			liveness,
			clock,
			heartbeatWindow,
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("closes the answered item past the max age and leaves the fresh one alone", func() {
		// ⚠️ The bound exists because the live index admits `answered` items —
		// liveIndexWorthy is `State != ClosedState` — so an answered item that is
		// never closed is decoded on every read for the life of the store.
		old := answer("producer-old", "key-old")

		// Move past the window, then answer a second item NOW, so the sweep has
		// exactly one item due and one that is not.
		clock.SetNow(clock.Now().Add(twoHours))
		fresh := answer("producer-fresh", "key-fresh")

		closed, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(1))

		got, err := store.Get(ctx, old)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.ClosedState))
		// The fresh one is still answered, which is the half that makes this a
		// bound rather than a flush.
		got, err = store.Get(ctx, fresh)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.AnsweredState))
	})

	It("leaves the closed item visible to History, so the sweep loses nothing", func() {
		// The bound is a state change, not a deletion: an item the sweep closes
		// must still be countable, or the bound would be destroying the record of
		// what was answered.
		id := answer("producer-history", "key-history")
		clock.SetNow(clock.Now().Add(twoHours))

		closed, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(1))

		history, err := store.History(ctx)
		Expect(err).To(BeNil())
		found := false
		for _, item := range history {
			if item.ItemID == id {
				found = true
				Expect(item.State).To(Equal(pkg.ClosedState))
			}
		}
		Expect(found).To(BeTrue(), "the swept item must still be in the history")
	})

	It("is a no-op when nothing is due, so a ticker never closes fresh answers", func() {
		// ⚠️ The sweep runs on a ticker, so this is the common case by a wide
		// margin: an unconditional close would make the max age meaningless.
		answer("producer-fresh-2", "key-fresh-2")

		closed, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(0))
	})

	It("does not close an OPEN item however old it is", func() {
		// The bound is on ANSWERED items only. An open ask is the queue, and
		// closing it would silently withdraw a question nobody answered.
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "producer-open",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-open"),
			DedupKey:        "key-open",
			InterruptClass:  "approve",
			Payload:         "still waiting",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		clock.SetNow(clock.Now().Add(twoHours))

		closed, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(0))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.OpenState))
	})
})
