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

var _ = Describe("AttentionStore History filter", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			liveness,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func(producer pkg.ProducerID, dedup pkg.DedupKey) {
		_, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      producer,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producer.String()),
			DedupKey:        dedup,
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
	}

	producersOf := func(items pkg.Items) []pkg.ProducerID {
		ids := make([]pkg.ProducerID, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ProducerID)
		}
		return ids
	}

	BeforeEach(func() {
		push("session-a", "a-1")
		push("session-b", "b-1")
		push("session-a", "a-2")
		push("session-c", "c-1")
	})

	It("returns every producer when none is named", func() {
		items, err := store.History(ctx, 0, 0)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(4))
	})

	It("narrows to one producer", func() {
		items, err := store.History(
			ctx,
			0,
			0,
			pkg.HistoryFilter{Producers: []pkg.ProducerID{"session-a"}},
		)
		Expect(err).To(BeNil())
		Expect(
			producersOf(items),
		).To(ConsistOf(pkg.ProducerID("session-a"), pkg.ProducerID("session-a")))
	})

	It("narrows to a set of producers", func() {
		items, err := store.History(
			ctx,
			0,
			0,
			pkg.HistoryFilter{Producers: []pkg.ProducerID{"session-a", "session-c"}},
		)
		Expect(err).To(BeNil())
		Expect(producersOf(items)).To(ConsistOf(
			pkg.ProducerID("session-a"), pkg.ProducerID("session-a"), pkg.ProducerID("session-c"),
		))
	})

	It("applies the filter before limit and offset", func() {
		page, err := store.History(
			ctx,
			1,
			0,
			pkg.HistoryFilter{Producers: []pkg.ProducerID{"session-a"}},
		)
		Expect(err).To(BeNil())
		Expect(producersOf(page)).To(Equal([]pkg.ProducerID{"session-a"}))

		next, err := store.History(
			ctx,
			1,
			1,
			pkg.HistoryFilter{Producers: []pkg.ProducerID{"session-a"}},
		)
		Expect(err).To(BeNil())
		Expect(producersOf(next)).To(Equal([]pkg.ProducerID{"session-a"}))
		Expect(next[0].ItemID).NotTo(Equal(page[0].ItemID))
	})

	It("does not match a producer id that is only a prefix of another", func() {
		// The raw-byte pre-check must anchor on the closing quote, or
		// "session-a" would keep "session-ab"'s rows and rely on the exact
		// compare alone.
		push("session-ab", "ab-1")
		items, err := store.History(
			ctx,
			0,
			0,
			pkg.HistoryFilter{Producers: []pkg.ProducerID{"session-a"}},
		)
		Expect(err).To(BeNil())
		Expect(producersOf(items)).NotTo(ContainElement(pkg.ProducerID("session-ab")))
		Expect(items).To(HaveLen(2))
	})

	It("matches an id JSON escapes", func() {
		// The needle is built through json.Marshal, so an id carrying a
		// character the encoder escapes still matches its stored bytes.
		push("session<&>", "esc-1")
		items, err := store.History(
			ctx,
			0,
			0,
			pkg.HistoryFilter{Producers: []pkg.ProducerID{"session<&>"}},
		)
		Expect(err).To(BeNil())
		Expect(producersOf(items)).To(Equal([]pkg.ProducerID{"session<&>"}))
	})

	It("treats an empty producer id as no filter", func() {
		items, err := store.History(ctx, 0, 0, pkg.HistoryFilter{Producers: []pkg.ProducerID{""}})
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(4))
	})

	It("narrows to a dedup key prefix", func() {
		push("session-a", "park:1234abcd")
		push("session-b", "park:5678efgh")
		items, err := store.History(ctx, 0, 0, pkg.HistoryFilter{DedupKeyPrefix: "park:"})
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(2))
		for _, item := range items {
			Expect(string(item.DedupKey)).To(HavePrefix("park:"))
		}
	})

	It("does not match a prefix that appears only inside the key", func() {
		push("session-a", "xpark:9")
		items, err := store.History(ctx, 0, 0, pkg.HistoryFilter{DedupKeyPrefix: "park:"})
		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty())
	})

	It("requires every set field to hold", func() {
		push("session-a", "park:1234abcd")
		push("session-b", "park:5678efgh")
		items, err := store.History(ctx, 0, 0, pkg.HistoryFilter{
			Producers:      []pkg.ProducerID{"session-b"},
			DedupKeyPrefix: "park:",
		})
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(1))
		Expect(items[0].ProducerID).To(Equal(pkg.ProducerID("session-b")))
	})

	It("returns nothing for an unknown producer", func() {
		items, err := store.History(
			ctx,
			0,
			0,
			pkg.HistoryFilter{Producers: []pkg.ProducerID{"session-zz"}},
		)
		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty())
	})
})
