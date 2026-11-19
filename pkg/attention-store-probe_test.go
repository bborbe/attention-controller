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

// A synthetic probe is a card posted to exercise the stack, never a question for
// the operator, and the board must not render it. The three specs below are the
// SC1/SC2/SC3 pair from the task that filed the defect: absent from the board,
// keyed on the tag rather than the producer, and not widened to ordinary cards.
//
// They live in their own file rather than inside attention-store_test.go, which
// sits at its 2000-line lint ceiling, and they carry their own fixture set for
// the same reason — nothing here is shared state a sibling spec could disturb.
//
// See docs/probe-tag.md for the convention.
var _ = Describe("Synthetic probe exclusion", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB, not a memory double: the read path's expiry prune is
		// a compare-and-delete whose atomicity depends on the backend serializing
		// write transactions, and this is the backend the deployed service opens.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

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

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// declaration builds a `message` push. The payload is the only field any of
	// these specs varies, because the payload is the whole of the exclusion's key.
	declaration := func(
		producerID pkg.ProducerID,
		dedupKey pkg.DedupKey,
		payload pkg.Payload,
	) pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         payload,
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
	}

	probePayload := func(suffix string) pkg.Payload {
		return pkg.Payload(pkg.ProbeTagPrefix + suffix)
	}

	itemIDs := func(items pkg.Items) []pkg.ItemID {
		ids := make([]pkg.ItemID, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ItemID)
		}
		return ids
	}

	// SC1. Absent from the board AND still readable through Get: a store that
	// pruned the probe would satisfy the first half alone, and the two tasks that
	// produced the 2026-11-17 burst require it to remain store history.
	It("omits a probe from the board without deleting it", func() {
		probe, err := store.Push(ctx, declaration(
			"session-a",
			"probe-1",
			probePayload(" #1: synthetic dismissal"),
		))
		Expect(err).To(BeNil())

		board, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(itemIDs(board)).NotTo(ContainElement(probe.ItemID))

		_, err = store.Get(ctx, probe.ItemID)
		Expect(err).To(BeNil())
	})

	// SC3. The exclusion must not widen: an untagged card posted alongside a probe
	// is still served, so a filter that dropped every open item fails here.
	It("still serves an untagged card posted alongside a probe", func() {
		_, err := store.Push(ctx, declaration(
			"session-a",
			"probe-1",
			probePayload(" #1: synthetic dismissal"),
		))
		Expect(err).To(BeNil())

		ordinary, err := store.Push(ctx, declaration("session-b", "gate-1", "deploy prod?"))
		Expect(err).To(BeNil())

		board, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(itemIDs(board)).To(ConsistOf(ordinary.ItemID))
	})

	// SC2. The key is the payload tag, never the producer id. Both cards come from
	// the SAME producer, so a producer-keyed filter drops the untagged one too and
	// fails, while a tag-keyed filter drops only the probe. Neither side of the
	// pairing can pass by coincidence, and the producer id is one that appears
	// nowhere in the code or in the 2026-11-17 burst.
	It("keys the exclusion on the payload tag, not on the producer id", func() {
		probe, err := store.Push(ctx, declaration(
			"session-never-seen-before",
			"probe-fresh",
			probePayload(" #2: synthetic dismissal"),
		))
		Expect(err).To(BeNil())

		card, err := store.Push(
			ctx,
			declaration("session-never-seen-before", "gate-1", "deploy prod?"),
		)
		Expect(err).To(BeNil())

		board, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(itemIDs(board)).NotTo(ContainElement(probe.ItemID))
		Expect(itemIDs(board)).To(ContainElement(card.ItemID))
	})
})
