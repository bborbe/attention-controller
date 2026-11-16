// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("AttentionStore HistoryCreatedSince", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var now time.Time

	base := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)
		now = base
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			liveness,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			}),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// pushAt pushes one item whose CreatedAt is the given offset from base.
	pushAt := func(offset time.Duration, dedup pkg.DedupKey) {
		now = base.Add(offset)
		_, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     "session:session-a",
			DedupKey:        dedup,
			InterruptClass:  "question",
			Payload:         "continue?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
	}

	dedupKeysOf := func(items pkg.Items) []pkg.DedupKey {
		keys := make([]pkg.DedupKey, 0, len(items))
		for _, item := range items {
			keys = append(keys, item.DedupKey)
		}
		return keys
	}

	It("returns only items created at or after since, newest-first", func() {
		pushAt(-3*time.Hour, "old-1")
		pushAt(-2*time.Hour, "old-2")
		pushAt(-30*time.Minute, "new-1")
		pushAt(-10*time.Minute, "new-2")

		items, err := store.HistoryCreatedSince(ctx, libtime.DateTime(base.Add(-time.Hour)))

		Expect(err).To(BeNil())
		Expect(dedupKeysOf(items)).To(Equal([]pkg.DedupKey{"new-2", "new-1"}))
	})

	// ⚠️ The boundary is inclusive. The key comparison relies on `|` sorting above
	// every digit, so an item created at EXACTLY since must compare at or above
	// the bare time prefix — an off-by-one here would drop the row the scan most
	// often looks at, the one created as the window opened.
	It("includes an item created exactly at since", func() {
		pushAt(-time.Hour-time.Nanosecond, "just-before")
		pushAt(-time.Hour, "exactly-at")

		items, err := store.HistoryCreatedSince(ctx, libtime.DateTime(base.Add(-time.Hour)))

		Expect(err).To(BeNil())
		Expect(dedupKeysOf(items)).To(Equal([]pkg.DedupKey{"exactly-at"}))
	})

	It("returns an empty result when nothing was created since", func() {
		pushAt(-3*time.Hour, "old-1")

		items, err := store.HistoryCreatedSince(ctx, libtime.DateTime(base.Add(-time.Hour)))

		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty())
	})

	// ⚠️ Unlike History, an expired OPEN item is kept. The forwarded lookup asks
	// "did the store ever hold this row", and an item that later expired is still
	// proof the ingest carried it — dropping it would re-mint a lost-gate card for
	// a delivered gate, the false positive this read exists to remove.
	It("keeps an expired open item that History would skip", func() {
		pushAt(-30*time.Minute, "expires")
		now = base.Add(48 * time.Hour)

		items, err := store.HistoryCreatedSince(ctx, libtime.DateTime(base.Add(-time.Hour)))

		Expect(err).To(BeNil())
		Expect(dedupKeysOf(items)).To(Equal([]pkg.DedupKey{"expires"}))
	})
})
