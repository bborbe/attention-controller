// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// This file is the probe for `expires_at` as a real bound.
//
// The field was written at push and read nowhere: nothing compared it to the
// current time, so an item carrying a deadline outlived it indefinitely. That
// was invisible while every ask was pruned by producer liveness within minutes
// — the prune was the de facto bound, and it was the wrong one.
//
// It stops being invisible with the `owner:` marker, which is never pruned by
// liveness. An `owner:` item with no enforced deadline is unbounded, so the
// marker that fixes "gates vanish before they are answered" would trade it for
// "gates never vanish" — the skim-training surface the marker exists to avoid.
//
// ⚠️ THE ASSERTION THAT MATTERS IS ON THE STORE, NOT ON THE BOARD. The first
// version of these specs asserted only that `ReadBoard` no longer returned the
// item, and it passed while the row was still there: `pruneDead` re-checks
// liveness before deleting, and an `owner:` ref reads as live forever, so the
// key was skipped and the item was merely HIDDEN. A dedup re-push would then
// have resurrected it. Every removal spec below therefore asserts `store.Get`
// as well — a board that hides a row is not a row that is gone.
//
// Every removal spec uses an `owner:` ref on purpose. That model's liveness
// probe is a constant `true`, so a removal cannot be read as a liveness prune
// wearing the expiry's name — which is exactly the confusion the defect hid in.

var _ = Describe("expires_at bound", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	newStore := func() (pkg.AttentionStore, libkv.DB) {
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		store := pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			pkg.NewSessionLivenessChecker(GinkgoT().TempDir()),
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
		return store, db
	}

	// push posts one open ask and returns it, so a spec can assert on the row
	// rather than only on what a read chose to render.
	push := func(
		store pkg.AttentionStore,
		ref pkg.LivenessRef,
		expiresAt *libtime.DateTime,
		dedupKey string,
	) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("worker-1"),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     ref,
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "pick",
			Payload:         "which way?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			ExpiresAt:       expiresAt,
		})
		Expect(err).To(BeNil())
		return item
	}

	// at builds a deadline offset from now, so a spec states which side of the
	// clock it is testing rather than an absolute instant that rots.
	at := func(offset stdtime.Duration) *libtime.DateTime {
		d := libtime.DateTimeFromUnixMicro(stdtime.Now().Add(offset).UnixMicro())
		return &d
	}

	It("removes an item whose expires_at has passed", func() {
		store, db := newStore()

		item := push(store, pkg.LivenessRef("owner:operator-1"), at(-stdtime.Hour), "expired")

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty(), "an item past its own declared deadline must go")

		_, err = store.Get(ctx, item.ItemID)
		Expect(err).To(MatchError(ContainSubstring("not found")),
			"the row must be DELETED, not merely hidden from the board — "+
				"a surviving row is resurrected by the next dedup re-push")

		Expect(db.Close()).To(BeNil())
	})

	It("keeps the same item when its expires_at is still ahead", func() {
		// The discriminator, in the same shape as the spec above: same producer,
		// same `owner:` ref, same ask — only the deadline differs. Without this
		// half, "removed" cannot be told apart from "removed for another reason".
		store, db := newStore()

		item := push(store, pkg.LivenessRef("owner:operator-1"), at(stdtime.Hour), "not-yet")

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(1), "a deadline still ahead of the clock bounds nothing yet")

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.ExpiresAt).NotTo(BeNil(), "the deadline must round-trip")

		Expect(db.Close()).To(BeNil())
	})

	It("keeps an item that declares no expires_at", func() {
		// The schema's default, and the reason the field is optional: an item
		// does not expire on a timer unless its producer says it does. A lost
		// ask costs work stalled silently, so persistence is the default and
		// the timer is opt-in.
		store, db := newStore()

		item := push(store, pkg.LivenessRef("owner:operator-1"), nil, "no-deadline")

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(1), "an absent expires_at must not be read as an expired one")

		_, err = store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())

		Expect(db.Close()).To(BeNil())
	})

	It("keeps an answered item whose expires_at has passed", func() {
		// History is never rewritten. The answered branch returns before the
		// state check, so a deadline cannot reach a resolution the operator has
		// already recorded — the same rule that keeps `pruneDead` from
		// destroying an answer it raced.
		store, db := newStore()

		item := push(
			store,
			pkg.LivenessRef("owner:operator-1"),
			at(-stdtime.Hour),
			"answered-expired",
		)

		_, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", &pkg.Answer{
			Kind:  pkg.OptionAnswerKind,
			Value: "which way?",
		}, nil, nil)
		Expect(err).To(BeNil())

		_, err = store.ReadBoard(ctx)
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil(), "an answered item's history is never rewritten by expiry")
		Expect(got.State).To(Equal(pkg.AnsweredState))

		Expect(db.Close()).To(BeNil())
	})

	It("removes a session item whose expires_at has passed", func() {
		// Coverage of the other models the field now reaches. ⚠️ This spec does
		// NOT discriminate on its own: with an empty registry a `session:` ref
		// reads as not-live, so the item would be pruned by liveness with or
		// without the deadline. The `owner:` specs above are the ones that pin
		// expiry as the cause; this one pins only that a `session:` item
		// carrying a past deadline does not survive.
		store, db := newStore()

		item := push(
			store,
			pkg.LivenessRef("session:worker-1"),
			at(-stdtime.Hour),
			"session-expired",
		)

		_, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())

		_, err = store.Get(ctx, item.ItemID)
		Expect(err).To(MatchError(ContainSubstring("not found")))

		Expect(db.Close()).To(BeNil())
	})
})
