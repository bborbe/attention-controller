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
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
)

// lostAnswersTotalValue reads the seeded counter back off a private registry.
func lostAnswersTotalValue(registry *prometheus.Registry) float64 {
	families, err := registry.Gather()
	Expect(err).To(BeNil())
	for _, family := range families {
		if family.GetName() == "attention_lost_answers_total" {
			return family.GetMetric()[0].GetCounter().GetValue()
		}
	}
	Fail("attention_lost_answers_total is not registered")
	return 0
}

// The durable total is the half of the counter that survives a process. These
// specs pin the reload path, not the increment: a restart is simulated by
// building a second store on the same database and seeding a fresh Prometheus
// registry from LoadLostAnswersTotal, which is exactly what main does at startup.
var _ = Describe("Lost-answer total across a restart", func() {
	var ctx context.Context
	var db libkv.DB
	var dataDir string
	var clock libtime.CurrentDateTime

	const (
		heartbeatWindow = libtime.Duration(15 * 60 * 1e9)
		// twoHours keeps the sweep's max-age close out of these specs: every item
		// here is already closed, and the pass exists only to settle candidates.
		twoHours = libtime.Duration(2 * 60 * 60 * 1e9)
		// pastGrace is comfortably beyond pkg.LostAnswerGrace (2 min).
		pastGrace = libtime.Duration(3 * 60 * 1e9)
	)

	newStore := func(counter pkg.LostAnswerCounter) pkg.AttentionStore {
		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)
		return pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			liveness,
			clock,
			heartbeatWindow,
			pkg.WithLostAnswers(counter),
		)
	}

	// answerAndClose pushes, answers with CONTENT, then closes an item with no
	// delivery attempt — the lost-answer candidate shape.
	//
	// ⚠️ The answer must carry something to deliver. An empty answer is the
	// nothing-to-deliver shape this helper deliberately does not exercise: it is
	// excluded from the count (see answerCarriesDelivery), so answering empty
	// here would make these specs pass for the wrong reason.
	answerAndClose := func(store pkg.AttentionStore, producer, dedup string) {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID(producer),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producer),
			DedupKey:        pkg.DedupKey(dedup),
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		_, err = store.Answer(
			ctx,
			item.ItemID,
			"attention-board",
			"session",
			pkg.Decision(""),
			&pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"},
			nil,
			nil,
		)
		Expect(err).To(BeNil())
		_, err = store.Close(ctx, item.ItemID, "", nil)
		Expect(err).To(BeNil())
	}

	// finalize advances the clock past the grace and runs the maintenance pass,
	// which is what settles a candidate into the durable total.
	finalize := func(store pkg.AttentionStore) {
		clock.SetNow(clock.Now().Add(pastGrace))
		_, err := store.SweepAnswered(ctx, twoHours)
		Expect(err).To(BeNil())
	}

	BeforeEach(func() {
		ctx = context.Background()
		// ⚠️ A real directory, not OpenTemp: the restart spec CLOSES this database
		// and reopens the same file, which is what production does. A second
		// handle on a still-open db would not catch a total kept in process
		// memory.
		dataDir = GinkgoT().TempDir()
		var err error
		db, err = libboltkv.OpenDir(ctx, dataDir)
		Expect(err).To(BeNil())
		clock = libtime.NewCurrentDateTime()
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("reads zero from a store that never counted a lost answer", func() {
		total, err := pkg.LoadLostAnswersTotal(ctx, db)
		Expect(err).To(BeNil())
		Expect(total).To(Equal(uint64(0)))
	})

	It("keeps the total across a restart and seeds a fresh counter from it", func() {
		first := &countingLostAnswers{}
		firstStore := newStore(first)
		answerAndClose(firstStore, "producer-before", "key-before")
		finalize(firstStore)
		Expect(first.Count()).To(Equal(1))

		persisted, err := pkg.LoadLostAnswersTotal(ctx, db)
		Expect(err).To(BeNil())
		Expect(persisted).To(Equal(uint64(1)))

		// The restart, composed exactly as main does it: the database is CLOSED
		// and reopened from the same file, then ONE counter is built from the
		// durable total and handed to the store, so the seed and the continuation
		// are the same object. A regression that seeded one counter and gave the
		// store a different one leaves the registry at 1.0 below.
		Expect(db.Close()).To(BeNil())
		db, err = libboltkv.OpenDir(ctx, dataDir)
		Expect(err).To(BeNil())

		total, err := pkg.LoadLostAnswersTotal(ctx, db)
		Expect(err).To(BeNil())
		registry := prometheus.NewRegistry()
		seeded := boardmetrics.NewMetrics(registry, total)
		Expect(lostAnswersTotalValue(registry)).To(Equal(1.0))

		afterStore := newStore(seeded)
		answerAndClose(afterStore, "producer-after", "key-after")
		finalize(afterStore)
		Expect(lostAnswersTotalValue(registry)).To(Equal(2.0))
		durable, err := pkg.LoadLostAnswersTotal(ctx, db)
		Expect(err).To(BeNil())
		Expect(durable).To(Equal(uint64(2)))
	})

	// ⚠️ ANY attempt record suppresses the count, whatever its outcome — the
	// condition is the record's existence, not what it says. A regression that
	// switched on the outcome would pass a delivered-only spec, so the table
	// covers all three.
	DescribeTable(
		"does not persist a count for an answered item that was attempted",
		func(outcome pkg.DeliveryOutcome) {
			store := newStore(&countingLostAnswers{})
			item, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      pkg.ProducerID("producer-attempted"),
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-attempted"),
				DedupKey:        pkg.DedupKey("key-attempted"),
				InterruptClass:  "approve",
				Payload:         "deploy prod?",
				AnswerMechanism: pkg.MessageAnswerMechanism,
			})
			Expect(err).To(BeNil())
			_, err = store.Answer(
				ctx,
				item.ItemID,
				"attention-board",
				"session",
				pkg.Decision(""),
				&pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"},
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			_, err = store.RecordAttempt(ctx, item.ItemID, "watcher", outcome)
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			finalize(store)

			total, err := pkg.LoadLostAnswersTotal(ctx, db)
			Expect(err).To(BeNil())
			Expect(total).To(Equal(uint64(0)))
		},
		Entry("delivered", pkg.DeliveredOutcome),
		Entry("held", pkg.HeldOutcome),
		Entry("failed", pkg.FailedOutcome),
	)

	// The candidate is durable, so a close whose grace expires while the process
	// is down is settled by the next pass rather than lost with the process.
	It("settles a candidate written before a restart on the next pass", func() {
		before := newStore(&countingLostAnswers{})
		answerAndClose(before, "producer-pending", "key-pending")
		Expect(pkg.LoadLostAnswersTotal(ctx, db)).To(Equal(uint64(0)))

		Expect(db.Close()).To(BeNil())
		var err error
		db, err = libboltkv.OpenDir(ctx, dataDir)
		Expect(err).To(BeNil())

		after := &countingLostAnswers{}
		afterStore := newStore(after)
		finalize(afterStore)
		Expect(after.Count()).To(Equal(1))

		durable, err := pkg.LoadLostAnswersTotal(ctx, db)
		Expect(err).To(BeNil())
		Expect(durable).To(Equal(uint64(1)))
	})
})

// ⚠️ The counter measures answers that were LOST, not answers that had nothing
// to deliver. Two shapes reach a close with no attempt record precisely because
// no arm was supposed to look, and counting them made goal SC3 ("zero lost
// answers per week") unable to read zero. Measured 2026-11-18/19: 5 of the 29
// closes in the counter's windows were these shapes.
//
// ⚠️ The third spec is the POSITIVE CONTROL and is not optional. A counter that
// stopped moving for every class also reads zero here, so a suite asserting only
// the two exclusions would pass with the instrument dead.
var _ = Describe("Lost-answer total excludes answers with nothing to deliver", func() {
	var ctx context.Context
	var db libkv.DB
	var clock libtime.CurrentDateTime
	var store pkg.AttentionStore

	const (
		heartbeatWindow = libtime.Duration(15 * 60 * 1e9)
		// twoHours keeps the sweep's max-age close out of these specs: every item
		// here is already closed, and the pass exists only to settle candidates.
		twoHours = libtime.Duration(2 * 60 * 60 * 1e9)
		// pastGrace is comfortably beyond pkg.LostAnswerGrace (2 min).
		pastGrace = libtime.Duration(3 * 60 * 1e9)
	)

	// pushAnswerClose pushes a message item, answers it with the given shape,
	// then closes it with no delivery attempt — the shape under test.
	pushAnswerClose := func(
		decision pkg.Decision,
		answer *pkg.Answer,
		answers pkg.Answers,
	) pkg.ItemID {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-shape"),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-shape"),
			DedupKey:        pkg.DedupKey("key-shape"),
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		_, err = store.Answer(
			ctx,
			item.ItemID,
			"attention-board",
			"session",
			decision,
			answer,
			answers,
			nil,
		)
		Expect(err).To(BeNil())
		_, err = store.Close(ctx, item.ItemID, "", nil)
		Expect(err).To(BeNil())
		return item.ItemID
	}

	finalize := func() {
		clock.SetNow(clock.Now().Add(pastGrace))
		_, err := store.SweepAnswered(ctx, twoHours)
		Expect(err).To(BeNil())
	}

	total := func() uint64 {
		value, err := pkg.LoadLostAnswersTotal(ctx, db)
		Expect(err).To(BeNil())
		return value
	}

	BeforeEach(func() {
		ctx = context.Background()
		dataDir := GinkgoT().TempDir()
		var err error
		db, err = libboltkv.OpenDir(ctx, dataDir)
		Expect(err).To(BeNil())
		clock = libtime.NewCurrentDateTime()
		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)
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

	It("does not count a hidden-answer dismissal", func() {
		itemID := pushAnswerClose("", &pkg.Answer{Kind: pkg.HiddenAnswerKind}, nil)
		finalize()
		Expect(total()).To(Equal(uint64(0)))

		// The close still writes a terminal record, under its own carrier: the
		// read surface must not fall back to `never_attempted` for a card that
		// had nothing to deliver.
		attempts, err := store.DeliveryAttempts(ctx)
		Expect(err).To(BeNil())
		Expect(attempts).To(HaveKey(itemID))
		Expect(attempts[itemID].Carrier).To(Equal("watcher:nothing-to-deliver"))
		Expect(attempts[itemID].Outcome).To(Equal(pkg.HeldOutcome))
	})

	It("does not count a no-content supervisor answer", func() {
		itemID := pushAnswerClose("", nil, nil)
		finalize()
		Expect(total()).To(Equal(uint64(0)))

		// The carrier assertion is load-bearing here: without it a regression that
		// stopped writing the record entirely would also read 0, and the card
		// would fall back to `never_attempted` — the false alarm this record
		// exists to prevent.
		attempts, err := store.DeliveryAttempts(ctx)
		Expect(err).To(BeNil())
		Expect(attempts).To(HaveKey(itemID))
		Expect(attempts[itemID].Carrier).To(Equal("watcher:nothing-to-deliver"))
	})

	It("still counts a genuine lost answer (positive control)", func() {
		itemID := pushAnswerClose(
			"",
			&pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"},
			nil,
		)
		finalize()
		Expect(total()).To(Equal(uint64(1)))

		attempts, err := store.DeliveryAttempts(ctx)
		Expect(err).To(BeNil())
		Expect(attempts[itemID].Carrier).To(Equal("watcher:gate-resolved"))
	})
})
