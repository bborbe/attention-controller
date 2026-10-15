// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// This file is the push path's counterpart to `read-decode-count_test.go`, and
// it exists to be RED on the pre-fix revision.
//
// `findOpenByDedupKey` used to walk the WHOLE items bucket and decode every
// stored item on every push — including the create path's, because
// `updateExistingIfLive` runs before the row is created. That walk held
// bbolt's single writer lock for its whole duration. Measured 2026-10-12 on the
// deployed store (38,194 items, bolt.db 147 MB): idle POST 0.10–0.19 s (n=8),
// a 2.26 s peak under 20 concurrent readers.
//
// Like the read probe, this counts the thing the defect is about rather than a
// shape that merely correlates with it: **how many stored items a push
// decodes**. The wrappers live in `read-decode-count_test.go` — same package,
// so `decodeCounter` and `decodeCountingDB` are in scope and are reused rather
// than redefined.

var _ = Describe("Push decode cost", func() {
	var ctx context.Context
	var counter *decodeCounter
	var db *decodeCountingDB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker

	// Both measured populations hold the SAME open set; only the closed
	// population differs. Declared here rather than beside their first use
	// because a function-body const is scoped from its declaration point, and
	// `pushDecodeCountOf` below needs them.
	const (
		openItemCount = 50
		smallClosed   = 1000
		largeClosed   = 20000
	)

	BeforeEach(func() {
		ctx = context.Background()
		raw, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		counter = &decodeCounter{}
		db = &decodeCountingDB{DB: raw, counter: counter}

		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
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

	// pushRequest mirrors the local helper of the same name in
	// `attention-store_test.go`. It is redeclared rather than shared because
	// that one is scoped to its own Describe closure.
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

	// seedOpen pushes `count` genuinely open items through the real write path,
	// each with a DISTINCT producer and dedup key, so nothing dedups and the open
	// set is exactly `count`.
	//
	// ⚠️ It is also what makes the measured push a STEADY-STATE one, and that is
	// load-bearing rather than incidental. These pushes write the dedup index's
	// built-marker — `putItem`'s `firstEver` branch on the first one,
	// `ensureDedupIndex` on the rest — so the push under test never pays the
	// one-time rebuild. A probe that measured the migration would report a
	// full-store decode count and read as a failure.
	seedOpen := func(count int) {
		for i := 0; i < count; i++ {
			_, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      pkg.ProducerID(fmt.Sprintf("session-%d", i)),
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef(fmt.Sprintf("session:session-%d", i)),
				DedupKey:        pkg.DedupKey(fmt.Sprintf("gate-%d", i)),
				InterruptClass:  "approve",
				Payload:         "deploy prod?",
				AnswerMechanism: pkg.MessageAnswerMechanism,
			})
			Expect(err).To(BeNil())
		}
	}

	// seedClosed writes `count` closed items straight into the items bucket in
	// ONE transaction. Going through Push/Close would cost two transactions per
	// item, which at 20,000 items is slow enough to make the probe unusable — and
	// it would prove nothing extra, because a closed item is inert to
	// suppression: only open items are indexed and only open items can be
	// collapsed onto.
	seedClosed := func(offset, count int) {
		err := db.DB.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			bucket, err := tx.CreateBucketIfNotExists(ctx, pkg.AttentionStoreBucketName)
			if err != nil {
				return err
			}
			for i := 0; i < count; i++ {
				id := pkg.ItemID(fmt.Sprintf("closed-%06d", offset+i))
				payload, err := json.Marshal(pkg.Item{
					ItemID: id,
					State:  pkg.ClosedState,
				})
				if err != nil {
					return err
				}
				if err := bucket.Put(ctx, []byte(id.String()), payload); err != nil {
					return err
				}
			}
			return nil
		})
		Expect(err).To(BeNil())
	}

	// pushDecodeCountOf runs one push and returns how many items it decoded. The
	// counter is reset immediately before the push, so the push's own decodes are
	// the whole count.
	pushDecodeCountOf := func(request pkg.PushRequest) int64 {
		counter.values = 0
		_, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		return counter.values
	}

	// The probe's own validity guard, mirroring the read probe's. A counter that
	// never fires would make every assertion below compare 0 to 0 and the suite
	// green while measuring nothing.
	It("counts decodes at all — the counter cannot silently read zero", func() {
		seedOpen(openItemCount)
		seedClosed(0, smallClosed)

		Expect(pushDecodeCountOf(pushRequest("session-0", "gate-0"))).To(
			BeNumerically(">=", 1),
			"the push decoded at least its own row, so the counter must have fired",
		)
	})

	// SC1 — the duplicate re-push path. The measured push re-sends a
	// (producer_id, dedup_key) the open set already holds, so
	// `updateExistingIfLive` finds a match and its producer is live.
	It("SC1 — a duplicate re-push's decode count does not track the store's item count", func() {
		seedOpen(openItemCount)
		seedClosed(0, smallClosed)
		smallDecodes := pushDecodeCountOf(pushRequest("session-0", "gate-0"))

		seedClosed(smallClosed, largeClosed-smallClosed)
		largeDecodes := pushDecodeCountOf(pushRequest("session-0", "gate-0"))

		delta := int64(largeClosed - smallClosed)
		tolerance := float64(delta) * 0.05

		GinkgoWriter.Printf(
			"Push SC1 raw: open=%d closed=%d decodes=%d | closed=%d decodes=%d | delta=%d tolerance=%.0f\n",
			openItemCount,
			smallClosed,
			smallDecodes,
			largeClosed,
			largeDecodes,
			delta,
			tolerance,
		)

		// ⚠️ The ABSOLUTE difference, for the reason the read probe's SC1 gives:
		// the signed form is negative whenever the first measurement is the
		// larger, and a negative number satisfies "< tolerance" no matter how far
		// apart the two counts actually are.
		Expect(math.Abs(float64(largeDecodes-smallDecodes))).To(
			BeNumerically("<", tolerance),
			"a duplicate re-push's decode count scales with the store's item count",
		)
	})

	// SC2 — the fresh-create path, which is the common one and the one a fix
	// that only short-circuits the HIT path leaves untouched.
	//
	// ⚠️ The two measured pushes use keys the open set does NOT hold, and a
	// DIFFERENT fresh key at each population: reusing one key would make the
	// second measurement a hit and quietly turn SC2 into SC1.
	It("SC2 — a fresh create's decode count does not track the store's item count", func() {
		seedOpen(openItemCount)
		seedClosed(0, smallClosed)
		smallDecodes := pushDecodeCountOf(pushRequest("session-fresh-small", "gate-fresh-small"))

		seedClosed(smallClosed, largeClosed-smallClosed)
		largeDecodes := pushDecodeCountOf(pushRequest("session-fresh-large", "gate-fresh-large"))

		delta := int64(largeClosed - smallClosed)
		tolerance := float64(delta) * 0.05

		GinkgoWriter.Printf(
			"Push SC2 raw: open=%d closed=%d decodes=%d | closed=%d decodes=%d | delta=%d tolerance=%.0f\n",
			openItemCount,
			smallClosed,
			smallDecodes,
			largeClosed,
			largeDecodes,
			delta,
			tolerance,
		)

		Expect(math.Abs(float64(largeDecodes-smallDecodes))).To(
			BeNumerically("<", tolerance),
			"a fresh create's decode count scales with the store's item count",
		)
	})
})
