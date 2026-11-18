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

	const heartbeatWindow = libtime.Duration(15 * 60 * 1e9)

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

	// answerAndClose pushes, answers, then closes an item with no delivery
	// attempt — the lost-answer shape.
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
		_, err = store.Answer(ctx, item.ItemID, "arm", "session", pkg.Decision(""), nil, nil, nil)
		Expect(err).To(BeNil())
		_, err = store.Close(ctx, item.ItemID, "", nil)
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
		answerAndClose(newStore(first), "producer-before", "key-before")
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

		answerAndClose(newStore(seeded), "producer-after", "key-after")
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
				"arm",
				"session",
				pkg.Decision(""),
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			_, err = store.RecordAttempt(ctx, item.ItemID, "watcher", outcome)
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())

			total, err := pkg.LoadLostAnswersTotal(ctx, db)
			Expect(err).To(BeNil())
			Expect(total).To(Equal(uint64(0)))
		},
		Entry("delivered", pkg.DeliveredOutcome),
		Entry("held", pkg.HeldOutcome),
		Entry("failed", pkg.FailedOutcome),
	)
})
