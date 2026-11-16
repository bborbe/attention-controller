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

// These specs reproduce the operator's report of 2026-11-16: one crash-loop
// question answered `keep stopped` three times in one afternoon on an identical
// dedup key. The mechanism is that the answered twin leaves the live index when
// the answered sweep closes it at `answeredMaxAge`, so a comparison built from
// the items a single read decoded stops seeing it — and a manager restart
// changes the producer, which the old identity also required to match.
var _ = Describe("AttentionStore crash-loop re-ask", func() {
	var ctx context.Context
	var db libkv.DB
	var clock libtime.CurrentDateTime
	var store pkg.AttentionStore
	var liveness *mocks.SessionLivenessChecker

	const (
		heartbeatWindow = libtime.Duration(15 * 60 * 1e9)
		oneHour         = libtime.Duration(60 * 60 * 1e9)
		twoHours        = libtime.Duration(2 * 60 * 60 * 1e9)

		crashLoopKey = pkg.DedupKey(
			"crash-loop:the-dark-factory-stall-detector-is-suppressed",
		)
	)

	// The producer's own text, verbatim from the live cards. The resume count is
	// the only part that could drift, and it did NOT drift between the three
	// re-mints measured on 2026-11-16 — all three read "3 resumes with no
	// progress", so payload identity matched and the miss was elsewhere.
	crashLoopPayload := pkg.Payload(
		"WORKER CRASH-LOOP: The Dark-Factory Stall Detector Is Suppressed and a " +
			"Successful Run Is Recorded as Failed — 3 resumes with no progress. " +
			"Resume anyway?",
	)

	push := func(producer pkg.ProducerID, dedup pkg.DedupKey, payload pkg.Payload) pkg.ItemID {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      producer,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producer.String()),
			DedupKey:        dedup,
			InterruptClass:  "pick",
			Payload:         payload,
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item.ItemID
	}

	answer := func(id pkg.ItemID) {
		_, err := store.Answer(
			ctx, id, "attention-board", "", pkg.Decision(""), nil, nil, nil,
		)
		Expect(err).To(BeNil())
	}

	board := func() map[pkg.ItemID]struct{} {
		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		out := make(map[pkg.ItemID]struct{}, len(items))
		for _, item := range items {
			out[item.ItemID] = struct{}{}
		}
		return out
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		liveness = &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)

		// The clock is held and moved because the defect only appears once the
		// answered twin is older than the max age.
		clock = libtime.NewCurrentDateTime()
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

	// The regression this task exists for. Before the durable answered record,
	// the sweep closed the twin, the twin left the live index, and the next
	// identical push rendered as a fresh card — so the operator answered one
	// question twice, an hour apart.
	It("suppresses a re-mint after the answered twin is swept", func() {
		first := push("session-manager-1", crashLoopKey, crashLoopPayload)
		answer(first)

		clock.SetNow(clock.Now().Add(twoHours))
		_, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())

		// The twin is now `closed` — absent from the live index — and still
		// records its answer. That is the state the old comparison could not see.
		twin, err := store.Get(ctx, first)
		Expect(err).To(BeNil())
		Expect(twin.State).To(Equal(pkg.ClosedState))
		Expect(twin.AnsweredAt).NotTo(BeNil())

		second := push("session-manager-1", crashLoopKey, crashLoopPayload)
		Expect(second).NotTo(Equal(first))
		Expect(board()).NotTo(HaveKey(second))
	})

	// The second miss: a crash-loop card is posted by whichever manager session
	// ran the keep-alive, so a manager restart changes the producer while the ask
	// stays the same. The old identity required the producer to match.
	It("suppresses a re-mint from a different producer session", func() {
		first := push("session-manager-1", crashLoopKey, crashLoopPayload)
		answer(first)

		second := push("session-manager-2", crashLoopKey, crashLoopPayload)
		Expect(second).NotTo(Equal(first))
		Expect(board()).NotTo(HaveKey(second))
	})

	// ⚠️ The negative control the fix must not break. A genuinely new crash loop
	// is a different ask, and it reaches the operator even under the same subject
	// and the same producer.
	It("cards the same subject after a genuinely new crash-loop signature", func() {
		first := push("session-manager-1", crashLoopKey, crashLoopPayload)
		answer(first)

		clock.SetNow(clock.Now().Add(twoHours))
		_, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())

		// Progress was made and the loop started over, so the producer's own text
		// differs. Withholding it would be the blanket mute SC4 forbids.
		changed := pkg.Payload(
			"WORKER CRASH-LOOP: The Dark-Factory Stall Detector Is Suppressed and a " +
				"Successful Run Is Recorded as Failed — 4 resumes with no progress. " +
				"Resume anyway?",
		)
		second := push("session-manager-1", crashLoopKey, changed)
		Expect(board()).To(HaveKey(second))
	})

	// ⚠️ The second negative control: a different subject is a different ask.
	It("cards a different subject", func() {
		first := push("session-manager-1", crashLoopKey, crashLoopPayload)
		answer(first)

		second := push(
			"session-manager-1",
			pkg.DedupKey("crash-loop:another-subject"),
			crashLoopPayload,
		)
		Expect(board()).To(HaveKey(second))
	})

	// ⚠️ The third negative control, carried over from the existing suite: a
	// close is a CLEAR, not an answer, so a cleared sibling suppresses nothing.
	It("cards a re-raise whose sibling was cleared, not answered", func() {
		first := push("session-manager-1", crashLoopKey, crashLoopPayload)
		_, err := store.Close(ctx, first, "", nil)
		Expect(err).To(BeNil())

		second := push("session-manager-1", crashLoopKey, crashLoopPayload)
		Expect(board()).To(HaveKey(second))
	})
})
