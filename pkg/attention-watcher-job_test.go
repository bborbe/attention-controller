// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// recordingCarrier is a scripted PaneCarrier that records what it was asked to
// carry and returns a fixed verdict.
type recordingCarrier struct {
	calls   []string
	verdict pkg.DeliveryVerdict
}

func (c *recordingCarrier) TypeIntoPane(
	_ context.Context,
	delivery *pkg.PaneDelivery,
	text string,
	_ bool,
) (pkg.DeliveryVerdict, string) {
	c.calls = append(c.calls, delivery.StoreItemID+"="+text)
	return c.verdict, "scripted"
}

// answeredItem builds an answered card for a session.
func answeredItem(id string, answeredBy string, decision pkg.Decision) pkg.Item {
	answeredAt := libtime.NewCurrentDateTime().Now()
	return pkg.Item{
		ItemID:      pkg.ItemID(id),
		LivenessRef: pkg.LivenessRef("session:11111111-2222-3333-4444-555555555555"),
		State:       pkg.AnsweredState,
		AnsweredAt:  &answeredAt,
		AnsweredBy:  answeredBy,
		Decision:    decision,
	}
}

var _ = Describe("attention watcher delivery job", func() {
	var (
		store   *mocks.AttentionStore
		carrier *recordingCarrier
		job     pkg.AttentionWatcherJob
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		carrier = &recordingCarrier{verdict: pkg.DeliveryOK}
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, libtime.NewCurrentDateTime(),
		)
	})

	It("carries an answered permission card and records delivered", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(carrier.calls).To(ConsistOf("item-1=allow"))
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("skips a card the originate arm settled", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "attention-watcher", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})

	It("skips a card already recorded as delivered", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {ItemID: "item-1", Status: pkg.DeliveredStatus},
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})

	It("records a failed attempt when the carrier refuses", func() {
		carrier.verdict = pkg.DeliveryRefuse
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("deny")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
	})

	It("records nothing on a retry verdict", func() {
		carrier.verdict = pkg.DeliveryRetry
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("skips an open card", func() {
		item := answeredItem("item-1", "operator", pkg.Decision("allow"))
		item.State = pkg.OpenState
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})

	It("leaves a hook menu card to the menu arm", func() {
		// A hook menu is answered by KEYSTROKES into its open menu, not by a prose
		// line. Typing text there would submit it into whatever prompt the session
		// is showing, so this arm must not carry it — the menu arm owns it.
		item := answeredItem("item-1", "operator", pkg.Decision(""))
		item.ProvenanceClass = pkg.HookProvenanceClass
		item.AnswerMechanism = pkg.MessageAnswerMechanism
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})

	It("hands a pane-less card to the headless arm instead of dropping it", func() {
		// A `sdk-cli` worker has no pane by construction. The prose carrier
		// reports `no-pane`, and before the headless arm existed that answer was
		// dropped with no record at all.
		headless := &fakeHeadlessDeliverer{terminal: true}
		job = pkg.NewAttentionWatcherJob(
			store, carrier, headless, time.Hour, libtime.NewCurrentDateTime(),
		)
		carrier.verdict = pkg.DeliveryNoPane
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(headless.calls).To(Equal([]pkg.ItemID{"item-1"}))
		// The prose arm attempts the pane first — that is how it learns there is
		// none — but records nothing: a pane-less session is not a failed delivery.
		Expect(carrier.calls).To(HaveLen(1))
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("keeps the pane path for a tab session", func() {
		// A tab session's answer still goes through the carrier: the headless arm
		// must not take a card that has a pane.
		headless := &fakeHeadlessDeliverer{terminal: true}
		job = pkg.NewAttentionWatcherJob(
			store, carrier, headless, time.Hour, libtime.NewCurrentDateTime(),
		)
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(ConsistOf("item-1=allow"))
		Expect(headless.calls).To(BeEmpty())
	})

	// The backoff triple. A card that could not be carried used to be re-attempted
	// on every tick for the life of the process: `failed` is not terminal, so
	// `deliverable` stayed true. On 2026-11-16 that was ~20 refusals a second
	// across ~40 stuck cards, each one a durable write that also woke the board.
	//
	// ⚠️ Every case below sets AnsweredAt from the SAME clock the job reads, so
	// "the record predates the answer" is a fact of the spec rather than a race
	// against the wall clock.
	answeredAtNow := func(clock libtime.CurrentDateTimeGetter) pkg.Item {
		answeredAt := clock.Now()
		item := answeredItem("item-1", "operator", pkg.Decision("allow"))
		item.AnsweredAt = &answeredAt
		return item
	}

	It("does not re-attempt a card that failed inside the retry backoff", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		item := answeredAtNow(clock)
		failedAt := clock.Now()
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {
				ItemID:      pkg.ItemID("item-1"),
				Status:      pkg.FailedStatus,
				Carrier:     "watcher:attention-watcher",
				AttemptedAt: &failedAt,
			},
		}, nil)
		store.ReadBoardReturns(pkg.Items{item}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})

	It("re-attempts a failed card once the retry backoff has passed", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		item := answeredAtNow(clock)
		failedAt := clock.Now()
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {
				ItemID:      pkg.ItemID("item-1"),
				Status:      pkg.FailedStatus,
				Carrier:     "watcher:attention-watcher",
				AttemptedAt: &failedAt,
			},
		}, nil)
		store.ReadBoardReturns(pkg.Items{item}, nil)
		clock.SetNow(
			clock.Now().Add(libtime.Duration(pkg.AttentionDeliveryRetryBackoff + time.Second)),
		)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(ConsistOf("item-1=allow"))
	})

	// ⚠️ The backoff must not withhold an answer because of a record written
	// BEFORE it. The routine-gate settle arm writes while the gate is still OPEN —
	// a HeldOutcome on refusal, and a FailedOutcome under this very carrier when
	// its wrapper answer fails — so a carrier- or recency-based gate found a
	// recent non-delivered record and sat on the operator's answer for up to five
	// minutes. Only a failure of THIS answer may delay it.
	It("delivers an answered card whose only failure predates the answer", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		item := answeredAtNow(clock)
		failedAt := libtime.DateTime(clock.Now().Time().Add(-10 * time.Second))
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {
				ItemID:      pkg.ItemID("item-1"),
				Status:      pkg.FailedStatus,
				Carrier:     "watcher:attention-watcher",
				AttemptedAt: &failedAt,
			},
		}, nil)
		store.ReadBoardReturns(pkg.Items{item}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(ConsistOf("item-1=allow"))
	})
})
