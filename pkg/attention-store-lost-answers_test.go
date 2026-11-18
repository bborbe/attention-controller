// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"sync"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// countingLostAnswers is a LostAnswerCounter that records how many times it was
// incremented. The mutex is belt-and-braces: these specs are serial, but the
// counter is moved from the store after a close transaction commits and read
// from the spec.
type countingLostAnswers struct {
	mu    sync.Mutex
	count int
}

func (c *countingLostAnswers) LostAnswersTotalCounterInc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
}

func (c *countingLostAnswers) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// The lost-answer counter is the reading goal SC3 depends on: an item that was
// answered, then closed, with no delivery attempt ever recorded.
//
// ⚠️ The verdict is DELIBERATELY not taken at the close. The delivery arm writes
// its attempt record after it has pushed the answer, so a close can land while a
// delivery is still in flight — measured 2026-11-18, five of 24 counted losses
// had a `delivered` record written 22–560 ms after the close. These specs pin
// both halves of the deferred verdict: a candidate is settled only after
// LostAnswerGrace, and a delivery that lands inside the grace suppresses it.
var _ = Describe("AttentionStore lost-answer counter", func() {
	var ctx context.Context
	var db libkv.DB
	var clock libtime.CurrentDateTime
	var counter *countingLostAnswers
	var store pkg.AttentionStore

	const (
		heartbeatWindow = libtime.Duration(15 * 60 * 1e9)
		oneHour         = libtime.Duration(60 * 60 * 1e9)
		twoHours        = libtime.Duration(2 * 60 * 60 * 1e9)
		// pastGrace is comfortably beyond pkg.LostAnswerGrace (2 min), so a sweep
		// at this offset settles every candidate written before it.
		pastGrace = libtime.Duration(3 * 60 * 1e9)
		// insideGrace is before LostAnswerGrace has elapsed.
		insideGrace = libtime.Duration(30 * 1e9)
	)

	push := func(producer pkg.ProducerID, dedup pkg.DedupKey) pkg.ItemID {
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
		return item.ItemID
	}

	answer := func(producer pkg.ProducerID, dedup pkg.DedupKey) pkg.ItemID {
		id := push(producer, dedup)
		_, err := store.Answer(ctx, id, "arm", "session", pkg.Decision(""), nil, nil, nil)
		Expect(err).To(BeNil())
		return id
	}

	// advance moves the store's clock by d, which is what makes a candidate old
	// enough to settle.
	advance := func(d libtime.Duration) {
		clock.SetNow(clock.Now().Add(d))
	}

	// sweep runs the maintenance pass, which is the vehicle for finalising
	// pending candidates. A maxAge long enough that no answered item is due for
	// the max-age close keeps the sweep to its finalisation work.
	sweep := func() {
		_, err := store.SweepAnswered(ctx, twoHours)
		Expect(err).To(BeNil())
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)

		// The clock is held and moved rather than frozen, because the subject is
		// age: a spec that cannot advance time cannot settle a candidate at all.
		clock = libtime.NewCurrentDateTime()
		counter = &countingLostAnswers{}
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			liveness,
			clock,
			heartbeatWindow,
			pkg.WithLostAnswers(counter),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("counts an answered item closed with no delivery attempt, once the grace has passed", func() {
		id := answer("producer-lost", "key-lost")

		_, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())
		Expect(counter.Count()).To(Equal(0))

		advance(pastGrace)
		sweep()
		Expect(counter.Count()).To(Equal(1))
	})

	It("does not count before the grace has passed", func() {
		id := answer("producer-young", "key-young")

		_, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())

		advance(insideGrace)
		sweep()
		Expect(counter.Count()).To(Equal(0))

		advance(pastGrace)
		sweep()
		Expect(counter.Count()).To(Equal(1))
	})

	// The defect this change exists to remove: the delivery arm pushes the answer
	// and writes its record AFTER the close has already committed. Before the
	// fix the close counted a loss on the spot, and the record landing 22–560 ms
	// later could not take it back.
	It("does not count a close whose delivery lands inside the grace", func() {
		id := answer("producer-race", "key-race")

		_, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())
		_, err = store.RecordAttempt(ctx, id, "watcher", pkg.DeliveredOutcome)
		Expect(err).To(BeNil())

		advance(pastGrace)
		sweep()
		Expect(counter.Count()).To(Equal(0))
	})

	It("does not count a close whose delivery arm recorded a held outcome after it", func() {
		id := answer("producer-held", "key-held")

		_, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())
		_, err = store.RecordAttempt(ctx, id, "watcher", pkg.HeldOutcome)
		Expect(err).To(BeNil())

		advance(pastGrace)
		sweep()
		Expect(counter.Count()).To(Equal(0))
	})

	It("counts a lost answer once and not again when the sweep re-scans", func() {
		answer("producer-sweep", "key-sweep")
		advance(twoHours)

		closed, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(1))
		// The close above happened at this tick, so its candidate is brand new and
		// must not be settled by the same sweep.
		Expect(counter.Count()).To(Equal(0))

		advance(pastGrace)
		closed, err = store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(0))
		Expect(counter.Count()).To(Equal(1))

		// The re-scan: the candidate is gone, so nothing is settled again.
		advance(pastGrace)
		closed, err = store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(0))
		Expect(counter.Count()).To(Equal(1))
	})

	It("does not count an answered item that did receive a delivery attempt", func() {
		id := answer("producer-attempted", "key-attempted")
		_, err := store.RecordAttempt(ctx, id, "watcher", pkg.DeliveredOutcome)
		Expect(err).To(BeNil())

		_, err = store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())

		advance(pastGrace)
		sweep()
		Expect(counter.Count()).To(Equal(0))
	})

	It("does not count a close of an item that was never answered", func() {
		id := push("producer-open", "key-open")

		_, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())

		advance(pastGrace)
		sweep()
		Expect(counter.Count()).To(Equal(0))
	})
})
