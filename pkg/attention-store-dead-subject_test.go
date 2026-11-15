// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"path/filepath"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// These specs pin the rule that a dead liveness subject alone never removes an
// item. The idle-reaper ends headless workers after 15–30 minutes, and the
// store used to delete their open questions on the next read — so an answer the
// operator gave a minute later hit a 404, and a resumed session found nothing.
// Only `expires_at` clears an item now, and an item pushed without one gets
// `created_at + 24h`.
//
// ⚠️ The clock is fake and mutable throughout, and that is load-bearing rather
// than tidy: the whole subject is a 24 h window, so a real clock can only assert
// that two events a millisecond apart differ, never that a renewal moved a
// deadline or that a window closed.
var _ = Describe("AttentionStore with a dead liveness subject", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var checker *mocks.SessionLivenessChecker
	var now libtime.DateTime

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		checker = &mocks.SessionLivenessChecker{}
		checker.IsLiveReturns(true)

		now = libtime.NewCurrentDateTime().Now()
		store = newStoreWith(db, checker, func() libtime.DateTime { return now })
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// advance moves the fake clock by offset, at the clock's own precision.
	//
	// ⚠️ The instant is NOT rounded to a microsecond. It used to be rebuilt with
	// libtime.DateTimeFromUnixMicro, and that made the deadline boundary
	// platform-dependent rather than merely untidy: `time.Now()` is
	// microsecond-aligned on darwin, so the rounding was a no-op here, while on
	// Linux it carries nanoseconds. The stored deadline is created_at + 24h at
	// RFC3339Nano precision and keeps them, so a rounded `now` landed a fraction
	// of a microsecond BEFORE the deadline — `!now.Before(deadline)` read false
	// and the item was never cleared. Measured: created ...348077789, now
	// ...348077, delta 789ns.
	advance := func(offset stdtime.Duration) {
		now = libtime.DateTime(now.Time().Add(offset))
	}

	push := func(dedupKey string, expiresAt *libtime.DateTime) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("worker-1"),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:worker-1"),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "pick",
			Payload:         "which way?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			ExpiresAt:       expiresAt,
		})
		Expect(err).To(BeNil())
		return item
	}

	// at is advance's read-only twin: same precision rule, so a producer TTL and
	// a deadline the store defaulted are built from the same kind of instant.
	at := func(offset stdtime.Duration) *libtime.DateTime {
		d := libtime.DateTime(now.Time().Add(offset))
		return &d
	}

	It("keeps an open ask whose subject is gone, inside its window, still open", func() {
		item := push("dead-open", nil)

		checker.IsLiveReturns(false)

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(1), "a dead subject alone must not remove the ask")

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil(), "the ask must stay readable by id after its subject is gone")
		Expect(got.State).To(Equal(pkg.OpenState))
	})

	It("keeps an ask answered after its subject died, with the answer readable", func() {
		item := push("dead-answered", nil)

		checker.IsLiveReturns(false)

		// A read between the subject's death and the answer is what used to
		// delete the row, so the answer below found nothing to land on.
		_, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())

		_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", &pkg.Answer{
			Kind:  pkg.OptionAnswerKind,
			Value: "left",
		}, nil, nil)
		Expect(err).To(BeNil(), "the operator must be able to answer an ask whose asker is gone")

		_, err = store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		_, err = store.Read(ctx)
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.AnsweredState))
		Expect(got.Answer).NotTo(BeNil())
		Expect(got.Answer.Value).To(Equal("left"),
			"a restarted session must still find the answer")
	})

	It("clears an ask whose subject is gone once its expires_at has passed", func() {
		item := push("dead-expired", at(-stdtime.Hour))

		checker.IsLiveReturns(false)

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty())

		_, err = store.Get(ctx, item.ItemID)
		Expect(err).To(MatchError(ContainSubstring("not found")),
			"expires_at is the one clock that clears an item")
	})

	It("stores created_at + 24h when the push carries no expires_at", func() {
		item := push("default-expiry", nil)

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(
			got.ExpiresAt,
		).NotTo(BeNil(), "the default is stored on the item, not derived at read")
		Expect(got.ExpiresAt.Time()).To(Equal(got.CreatedAt.Time().Add(24 * stdtime.Hour)))
		Expect(got.ExpiresAtDefaulted).To(BeTrue(),
			"the default is recorded, so the renewal pass can tell it from a producer TTL")
		Expect(stdtime.Duration(pkg.DefaultItemExpiry)).To(Equal(24 * stdtime.Hour))
	})

	It("clears a push without expires_at from a dead session once created_at + 24h passes", func() {
		item := push("default-dead", nil)
		checker.IsLiveReturns(false)

		// One minute short of the window: still there, and still answerable — the
		// window exists so an operator who comes back late is not punished.
		advance(24*stdtime.Hour - stdtime.Minute)
		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(1), "inside the default window a dead subject removes nothing")

		// The deadline itself. `!now.Before(deadline)` makes the boundary instant
		// expired, so this is the exact tick the default is meant to fire on.
		advance(stdtime.Minute)
		items, err = store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty(), "the 24 h default is what bounds a dead session's item")

		_, err = store.Get(ctx, item.ItemID)
		Expect(err).To(MatchError(ContainSubstring("not found")))
	})

	It("re-arms the default from the new created_at on a dedup re-push without one", func() {
		first := push("default-repush", nil)

		// ⚠️ The clock advances between the two pushes. On the real clock both land
		// in the same instant, so a re-push that failed to re-arm the deadline from
		// its new created_at would still satisfy the equality below — the check
		// would pass without testing anything.
		advance(2 * stdtime.Hour)
		second := push("default-repush", nil)
		Expect(second.ItemID).To(Equal(first.ItemID), "a re-push updates the open row in place")
		Expect(second.CreatedAt.Time()).To(Equal(now.Time()))

		got, err := store.Get(ctx, first.ItemID)
		Expect(err).To(BeNil())
		Expect(got.ExpiresAt).NotTo(BeNil(), "a re-push must not clear the stored default")
		Expect(got.ExpiresAt.Time()).To(Equal(got.CreatedAt.Time().Add(24 * stdtime.Hour)))
		Expect(got.ExpiresAtDefaulted).To(BeTrue())
	})

	// The operator ruling of 2026-11-15 ~22:58: "renew while alive". `expires_at`
	// stays the one clock that deletes an item, but a DEFAULTED deadline is pushed
	// forward while the asking session is still alive, so a session reaped at 15
	// minutes and resumed an hour later keeps its question and its answer.
	Describe("renewing a defaulted deadline while the subject is alive", func() {
		It(
			"keeps a live session's defaulted ask past created_at + 24h, deadline moved forward",
			func() {
				item := push("live-renew", nil)
				created := item.CreatedAt.Time()

				// Past the threshold, so the next maintenance pass has work to do.
				advance(13 * stdtime.Hour)
				renewed, err := store.RenewLive(ctx)
				Expect(err).To(BeNil())
				Expect(renewed).To(Equal(1), "a live session's defaulted deadline is renewed")

				got, err := store.Get(ctx, item.ItemID)
				Expect(err).To(BeNil())
				// ⚠️ Compared as UnixMicro, not as time.Time: the item read back has been
				// through the store's encoding, and its Location differs from a locally
				// built instant even when the instant is identical. libtime stores
				// microseconds, so that is the unit both sides agree on.
				Expect(
					got.ExpiresAt.Time().UnixMicro(),
				).To(Equal(now.Time().Add(24*stdtime.Hour).UnixMicro()),
					"the deadline is now + 24h, not created_at + 24h")
				Expect(got.ExpiresAt.Time().After(created.Add(24 * stdtime.Hour))).To(BeTrue())

				// Past the ORIGINAL window, which is what the renewal exists to survive.
				advance(12 * stdtime.Hour)
				items, err := store.ReadBoard(ctx)
				Expect(err).To(BeNil())
				Expect(items).To(HaveLen(1), "a renewed item outlives its first created_at + 24h")
			},
		)

		It("clears a dead session's item 24h after its last renewal", func() {
			item := push("dead-after-renewal", nil)

			advance(13 * stdtime.Hour)
			renewed, err := store.RenewLive(ctx)
			Expect(err).To(BeNil())
			Expect(renewed).To(Equal(1))
			lastRenewal := now.Time()

			checker.IsLiveReturns(false)

			// 23 h after the last renewal: no further renewal, but still inside the
			// window it was given.
			advance(23 * stdtime.Hour)
			renewed, err = store.RenewLive(ctx)
			Expect(err).To(BeNil())
			Expect(renewed).To(Equal(0), "a dead subject is never renewed")
			items, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(HaveLen(1))

			// Exactly 24 h after the last renewal: the one clock fires.
			advance(stdtime.Hour)
			items, err = store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(BeEmpty(),
				"the bound is DefaultItemExpiry after the last renewal, not after the first push")
			Expect(lastRenewal.Add(24 * stdtime.Hour).UnixMicro()).To(Equal(now.Time().UnixMicro()))

			_, err = store.Get(ctx, item.ItemID)
			Expect(err).To(MatchError(ContainSubstring("not found")))
		})

		It("does not renew a producer-set TTL", func() {
			item := push("producer-ttl", at(30*stdtime.Hour))

			// 20 h in, 10 h of the producer's own 30 h remain — inside the renewal
			// threshold, so only the recorded provenance can be what declines it.
			advance(20 * stdtime.Hour)
			renewed, err := store.RenewLive(ctx)
			Expect(err).To(BeNil())
			Expect(renewed).To(Equal(0), "a producer-set deadline is the producer's to keep")

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.ExpiresAtDefaulted).To(BeFalse())
			Expect(got.ExpiresAt.Time().UnixMicro()).To(Equal(item.ExpiresAt.Time().UnixMicro()),
				"the store does not overrule a deadline the producer declared")
		})

		It("renews when the session registry cannot be read", func() {
			// The real checker, pointed at a directory that does not exist: an
			// unreadable registry proves nothing, so it reads as live and the item is
			// kept. The fail-safe direction is the point — a store that could not see
			// the registry must not let a session's question lapse.
			unreadable := pkg.NewSessionLivenessChecker(
				filepath.Join(GinkgoT().TempDir(), "missing"),
			)
			store = newStoreWith(db, unreadable, func() libtime.DateTime { return now })
			item := push("unreadable-registry", nil)

			advance(13 * stdtime.Hour)
			renewed, err := store.RenewLive(ctx)
			Expect(err).To(BeNil())
			Expect(
				renewed,
			).To(Equal(1), "an unreadable registry renews rather than lets an item lapse")

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(
				got.ExpiresAt.Time().UnixMicro(),
			).To(Equal(now.Time().Add(24 * stdtime.Hour).UnixMicro()))
		})

		It("renews nothing when the deadline is still far off", func() {
			push("not-due", nil)

			advance(stdtime.Hour)
			renewed, err := store.RenewLive(ctx)
			Expect(err).To(BeNil())
			Expect(renewed).To(Equal(0), "renewal is not a per-tick rewrite of every row")
		})
	})
})

// newStoreWith builds a store over db with the given liveness checker and clock.
func newStoreWith(
	db libkv.DB,
	checker pkg.SessionLivenessChecker,
	clock func() libtime.DateTime,
) pkg.AttentionStore {
	return pkg.NewAttentionStore(
		db,
		pkg.NewItemIDGenerator(),
		checker,
		libtime.CurrentDateTimeGetterFunc(clock),
		libtime.Duration(15*stdtime.Minute),
	)
}
