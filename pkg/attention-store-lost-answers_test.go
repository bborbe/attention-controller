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
// counter is written from inside the store's transaction and read from the spec.
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
// answered, then closed, with no delivery attempt ever recorded. These specs pin
// the three properties the criterion turns on — it counts once, a re-scan does
// not double it, and an attempted answer is never counted.
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

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)

		// The clock is held and moved rather than frozen, because the sweep's
		// subject is age: a spec that cannot advance time cannot make an
		// answered item due for the sweep at all.
		clock = libtime.NewCurrentDateTime()
		counter = &countingLostAnswers{}
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			liveness,
			clock,
			heartbeatWindow,
			counter,
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("counts an answered item closed with no delivery attempt", func() {
		id := answer("producer-lost", "key-lost")

		_, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())
		Expect(counter.Count()).To(Equal(1))
	})

	It("counts a lost answer once and not again when the sweep re-scans", func() {
		answer("producer-sweep", "key-sweep")
		clock.SetNow(clock.Now().Add(twoHours))

		closed, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(1))
		Expect(counter.Count()).To(Equal(1))

		// The re-scan: the item is already closed, so nothing is due and the
		// counter must not move again.
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
		Expect(counter.Count()).To(Equal(0))
	})

	It("does not count a close of an item that was never answered", func() {
		id := push("producer-open", "key-open")

		_, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())
		Expect(counter.Count()).To(Equal(0))
	})
})
