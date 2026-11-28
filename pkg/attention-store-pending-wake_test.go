// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

// The pending-wake record's specs live here rather than in
// attention-store_test.go because that file sits at the repository's 2000-line
// ceiling. The suite setup is repeated rather than shared: each file's Describe
// owns its own store, and a shared fixture would couple their lifecycles.

import (
	"context"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("AttentionStore pending wakes", func() {
	var ctx context.Context
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		DeferCleanup(func() {
			Expect(db.Close()).To(BeNil())
		})
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

	Describe("PutPendingWake", func() {
		It("records a wake that PendingWakes reads back", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			Expect(store.PutPendingWake(ctx, "manager-1", item.ItemID)).To(BeNil())

			owed, err := store.PendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(owed).To(HaveLen(1))
			Expect(owed[0].ItemID).To(Equal(item.ItemID))
		})

		It("overwrites rather than duplicates a second deferral of the same item", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			Expect(store.PutPendingWake(ctx, "manager-1", item.ItemID)).To(BeNil())
			Expect(store.PutPendingWake(ctx, "manager-1", item.ItemID)).To(BeNil())

			owed, err := store.PendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(owed).To(HaveLen(1))
		})

		It("keys by manager session, so one manager's drain leaves another's record", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			Expect(store.PutPendingWake(ctx, "manager-1", item.ItemID)).To(BeNil())
			Expect(store.PutPendingWake(ctx, "manager-2", item.ItemID)).To(BeNil())

			drained, err := store.DrainPendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(drained).To(HaveLen(1))

			other, err := store.PendingWakes(ctx, "manager-2")
			Expect(err).To(BeNil())
			Expect(other).To(HaveLen(1))
		})

		It("refuses an empty manager session", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			err = store.PutPendingWake(ctx, "  ", item.ItemID)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrManagerSessionRequired)).To(BeTrue())
		})

		It("refuses an unknown item rather than writing an orphan record", func() {
			err := store.PutPendingWake(ctx, "manager-1", pkg.ItemID("does-not-exist"))
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())
		})
	})

	Describe("DrainPendingWakes", func() {
		It("returns the owed items and empties the record in one call", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			Expect(store.PutPendingWake(ctx, "manager-1", item.ItemID)).To(BeNil())

			drained, err := store.DrainPendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(drained).To(HaveLen(1))
			Expect(drained[0].ItemID).To(Equal(item.ItemID))

			remaining, err := store.PendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(remaining).To(BeEmpty())
		})

		It("delivers exactly once — a replayed drain finds nothing", func() {
			// The `Stop` hook that drains can fire more than once for one turn
			// boundary. Drain-and-mark is what makes the second call a no-op
			// rather than a second delivery.
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			Expect(store.PutPendingWake(ctx, "manager-1", item.ItemID)).To(BeNil())

			first, err := store.DrainPendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(first).To(HaveLen(1))

			second, err := store.DrainPendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(second).To(BeEmpty())
		})

		It("drops a wake whose item is no longer open, and still deletes its record", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			Expect(store.PutPendingWake(ctx, "manager-1", item.ItemID)).To(BeNil())

			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())

			drained, err := store.DrainPendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(drained).To(BeEmpty())

			remaining, err := store.PendingWakes(ctx, "manager-1")
			Expect(err).To(BeNil())
			Expect(remaining).To(BeEmpty())
		})

		It("refuses an empty manager session", func() {
			_, err := store.DrainPendingWakes(ctx, "")
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrManagerSessionRequired)).To(BeTrue())
		})
	})
})
