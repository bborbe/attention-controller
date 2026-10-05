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
// Every spec below uses an `owner:` ref on purpose. Its liveness probe is a
// constant `true`, so the deadline is the only variable and a removal cannot be
// read as a liveness prune wearing the expiry's name.

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

	push := func(store pkg.AttentionStore, expiresAt *libtime.DateTime, dedupKey string) {
		_, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("worker-1"),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("owner:operator-1"),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "pick",
			Payload:         "which way?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			ExpiresAt:       expiresAt,
		})
		Expect(err).To(BeNil())
	}

	// at builds a deadline offset from now, so a spec states which side of the
	// clock it is testing rather than an absolute instant that rots.
	at := func(offset stdtime.Duration) *libtime.DateTime {
		d := libtime.DateTimeFromUnixMicro(stdtime.Now().Add(offset).UnixMicro())
		return &d
	}

	It("removes an item whose expires_at has passed", func() {
		store, db := newStore()

		push(store, at(-stdtime.Hour), "expired")

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty(), "an item past its own declared deadline must go")

		Expect(db.Close()).To(BeNil())
	})

	It("keeps the same item when its expires_at is still ahead", func() {
		// The discriminator, in the same shape as the spec above: same producer,
		// same `owner:` ref, same ask — only the deadline differs. Without this
		// half, "removed" cannot be told apart from "removed for another reason".
		store, db := newStore()

		push(store, at(stdtime.Hour), "not-yet")

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(1), "a deadline still ahead of the clock bounds nothing yet")

		Expect(db.Close()).To(BeNil())
	})

	It("keeps an item that declares no expires_at", func() {
		// The schema's default, and the reason the field is optional: an item
		// does not expire on a timer unless its producer says it does. A lost
		// ask costs work stalled silently, so persistence is the default and
		// the timer is opt-in.
		store, db := newStore()

		push(store, nil, "no-deadline")

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(1), "an absent expires_at must not be read as an expired one")

		Expect(db.Close()).To(BeNil())
	})
})
