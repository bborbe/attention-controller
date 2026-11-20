// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"strings"
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
	entries []*pkg.PaneDelivery
	verdict pkg.DeliveryVerdict
}

func (c *recordingCarrier) TypeIntoPane(
	_ context.Context,
	delivery *pkg.PaneDelivery,
	text string,
	_ bool,
) (pkg.DeliveryVerdict, string) {
	c.calls = append(c.calls, delivery.StoreItemID+"="+text)
	c.entries = append(c.entries, delivery)
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

	It(
		"reuses ONE delivery entry across cycles, so the at-most-once marker survives a retry",
		func() {
			// ⚠️ This is the duplicate-delivery regression. `TypeIntoPane` writes
			// `PendingLine` and the transcript byte mark onto the entry it is given,
			// and `retypeGuard` uses them to answer "composer empty, transcript has
			// not caught up — wait, do not type a second copy". A job that builds a
			// fresh entry per cycle throws both away, and once a busy session has
			// written past the transcript tail window the delivered line is
			// unreachable, so the answer is typed again on every backoff.
			// Measured 2026-11-19 on card 982bdcf8…: one answer, three arrivals
			// (16:24:24, 16:34:31, 16:55:03) at the 5-minute retry cadence.
			carrier.verdict = pkg.DeliveryRetry
			store.ReadBoardReturns(pkg.Items{
				answeredItem("item-1", "operator", pkg.Decision("allow")),
			}, nil)

			Expect(job.DeliverOnce(context.Background())).To(Succeed())
			Expect(job.DeliverOnce(context.Background())).To(Succeed())

			Expect(carrier.entries).To(HaveLen(2))
			Expect(carrier.entries[1]).To(BeIdenticalTo(carrier.entries[0]),
				"a fresh entry per cycle discards PendingLine/PendingAt and re-types the answer")
		},
	)

	It("drops the entry once the card is delivered, so a later answer starts clean", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(carrier.entries).To(HaveLen(2))
		Expect(carrier.entries[1]).NotTo(BeIdenticalTo(carrier.entries[0]),
			"a delivered entry must not be reused by a later answer for the same card")
	})

	It("settles a held card (open + answered) once delivery is observed", func() {
		held := answeredItem("item-1", "operator", pkg.Decision("allow"))
		held.State = pkg.OpenState
		store.ReadBoardReturns(pkg.Items{held}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		Expect(store.SettleAnswerCallCount()).To(Equal(1),
			"the held card leaves the open set only after delivery was observed")
		_, itemID := store.SettleAnswerArgsForCall(0)
		Expect(itemID).To(Equal(pkg.ItemID("item-1")))
	})

	It("does not settle an item that is already answered", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.SettleAnswerCallCount()).To(Equal(0))
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

	It("records the retry as an attempt so the card is not never_attempted", func() {
		// ⚠️ Inverted 2026-11-18. This spec used to pin "records nothing on a
		// retry verdict", and that pin WAS the defect: the lost-answer counter
		// counts an answered+closed card with no attempt record, so every card a
		// busy or unreadable composer sent into this loop closed reading *no arm
		// ever looked at this answer*. Measured on the live board: 14 of the 24
		// closes in the counter's 12.7 h window were exactly this.
		carrier.verdict = pkg.DeliveryRetry
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, attemptCarrier, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.RetriedOutcome),
			"a retry is an arm having looked, not a failure and not silence")
		Expect(attemptCarrier).NotTo(Equal("watcher:gate-resolved"),
			"the record must not be the close's backfill")
	})

	It("writes the retry record once per answer, not once per cycle", func() {
		// The loop runs every two seconds for as long as the card lives. One
		// durable write per cycle is the hot loop AttentionDeliveryRetryBackoff
		// exists to stop, so the record is written on the FIRST retry only.
		carrier.verdict = pkg.DeliveryRetry
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(1))

		_, _, attemptCarrier, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.RetriedOutcome))
		// The store now carries this arm's own retry record; the next cycle must
		// not write it again.
		attemptedAt := libtime.DateTime(libtime.NewCurrentDateTime().Now().Time())
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {
				ItemID:      pkg.ItemID("item-1"),
				Status:      pkg.RetryingStatus,
				Carrier:     attemptCarrier,
				AttemptedAt: &attemptedAt,
			},
		}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
	})

	It(
		"records the resolved verdict as a held attempt so a cleared gate is not counted lost",
		func() {
			// ⚠️ The defect this pins: the prose arm's `resolved` branch — the gate was
			// already gone when the arm looked — recorded nothing, while the menu arm
			// records `held` for the same verdict (`menu-answer.go` case
			// DeliveryResolved). An answered card whose only verdict was `resolved` then
			// closed with just the close's `watcher:gate-resolved` backfill, so
			// `countLostAnswer` counted a look as a loss. Measured 2026-11-19: two live
			// closes, 3a5a4ab9… and f3a96f81….
			carrier.verdict = pkg.DeliveryResolved
			store.ReadBoardReturns(pkg.Items{
				answeredItem("item-1", "operator", pkg.Decision("allow")),
			}, nil)
			Expect(job.DeliverOnce(context.Background())).To(Succeed())

			Expect(store.RecordAttemptCallCount()).To(Equal(1))
			_, _, attemptCarrier, outcome := store.RecordAttemptArgsForCall(0)
			Expect(outcome).To(Equal(pkg.HeldOutcome),
				"a resolved verdict is an arm that looked — not a failure, and not silence")
			Expect(attemptCarrier).To(Equal("watcher:attention-watcher"),
				"the record must be the arm's own carrier, not the close's backfill")
		},
	)

	It("skips an open card", func() {
		item := answeredItem("item-1", "operator", pkg.Decision("allow"))
		// An open card with no recorded answer is unanswered; an open card WITH
		// one is the hold, which the arm does deliver.
		item.State = pkg.OpenState
		item.AnsweredAt = nil
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

	It("delivers a skip answer as a decline, not silence", func() {
		// The board's ✕ Dismiss writes `answer.kind: skip`. Reading it as no
		// content left the card `answered` while the asking session was told
		// nothing and no attempt was recorded — the measured 2026-10-17 defect
		// on three compact cards.
		item := answeredItem("item-1", "operator", pkg.Decision(""))
		item.AnswerMechanism = pkg.MessageAnswerMechanism
		item.Answer = &pkg.Answer{Kind: pkg.SkipAnswerKind}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(carrier.calls).To(ConsistOf("item-1=declined (no answer given)"))
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("leaves an approved compact card to the trio arm", func() {
		// An approved compact card's answer is three commands, not prose. Typing
		// the picked label here would send a message that starts with "Operator"
		// rather than "/", so the session would dispatch nothing — while the trio
		// compacted it underneath. A DECLINED compact card is still prose.
		item := answeredItem("item-1", "operator", pkg.Decision(""))
		item.DedupKey = pkg.DedupKey("compact-approval:11111111-2222-3333-4444-555555555555")
		item.AnswerMechanism = pkg.MessageAnswerMechanism
		item.Options = pkg.AnswerOptions{{Label: "Compact now", Recommended: true}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Compact now"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(carrier.calls).To(BeEmpty())
	})

	It("delivers a dismissed compact card as prose, never the approve label", func() {
		// A compact card's approve signal is its recommended option label, so a
		// decline must carry prose that matches none of the options — anything
		// else runs the trio against the operator's own decline.
		item := answeredItem("item-1", "operator", pkg.Decision(""))
		item.AnswerMechanism = pkg.AckAnswerMechanism
		item.Options = pkg.AnswerOptions{{Label: "Compact now", Recommended: true}}
		item.Answer = &pkg.Answer{Kind: pkg.SkipAnswerKind}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(carrier.calls).To(HaveLen(1))
		Expect(carrier.calls[0]).NotTo(ContainSubstring("Compact now"))
		Expect(carrier.calls[0]).To(Equal("item-1=declined (no answer given)"))
	})

	It("delivers nothing for a hidden answer, and records no refusal", func() {
		// A corner-X `hidden` close means the operator answered elsewhere and only
		// wanted the card off the board. Delivering a decline would assert the
		// reverse of what happened, and recording a refusal would report a
		// failure that never occurred.
		item := answeredItem("item-1", "operator", pkg.Decision(""))
		item.AnswerMechanism = pkg.MessageAnswerMechanism
		item.Answer = &pkg.Answer{Kind: pkg.HiddenAnswerKind}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(carrier.calls).To(BeEmpty())
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
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

	It("settles a held card after the headless arm delivers it", func() {
		// A held permission card (open + answered) must leave the open set once the
		// headless arm carries it — the pane-less path is the one a parked sdk-cli
		// worker takes, and without this settle the card would stay open forever.
		headless := &fakeHeadlessDeliverer{terminal: true}
		job = pkg.NewAttentionWatcherJob(
			store, carrier, headless, time.Hour, libtime.NewCurrentDateTime(),
		)
		carrier.verdict = pkg.DeliveryNoPane
		held := answeredItem("item-1", "operator", pkg.Decision("allow"))
		held.State = pkg.OpenState
		store.ReadBoardReturns(pkg.Items{held}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(headless.calls).To(Equal([]pkg.ItemID{"item-1"}))
		Expect(store.SettleAnswerCallCount()).To(Equal(1),
			"a headless delivery of a held card must settle it")
	})

	It("settles a delivered-but-open held card past the bound", func() {
		// The arm can record `delivered` and die before settling. Without settling
		// here the card is skipped every tick and stays open forever.
		held := answeredItem("item-1", "operator", pkg.Decision("allow"))
		held.State = pkg.OpenState
		old := libtime.DateTime(time.Now().Add(-10 * time.Minute))
		held.AnsweredAt = &old
		store.ReadBoardReturns(pkg.Items{held}, nil)
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {ItemID: "item-1", Status: pkg.DeliveredStatus},
		}, nil)
		store.DeliveryReturns(&pkg.DeliveryReport{
			ItemID: pkg.ItemID("item-1"), Status: pkg.DeliveredStatus,
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.SettleAnswerCallCount()).To(Equal(1),
			"a delivered-but-open card must settle, not be skipped forever")
	})

	It("closes an overdue undelivered held card and writes the failed marker", func() {
		// Job-level coverage for closeOverdueHeld: an open answered card past the
		// bound with no delivered attempt must get the failed marker and be SETTLED
		// to answered (leaving the open set, keeping the answer deliverable), not
		// closed. LivenessRef is cleared so the deliver path skips it and only the
		// overdue fallback acts.
		held := answeredItem("item-1", "operator", pkg.Decision("allow"))
		held.State = pkg.OpenState
		held.LivenessRef = ""
		old := libtime.DateTime(time.Now().Add(-10 * time.Minute))
		held.AnsweredAt = &old
		store.ReadBoardReturns(pkg.Items{held}, nil)
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		store.DeliveryReturns(&pkg.DeliveryReport{
			ItemID: pkg.ItemID("item-1"), Status: pkg.NeverAttemptedStatus,
		}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		wroteFailed := false
		for i := 0; i < store.RecordAttemptCallCount(); i++ {
			_, _, _, outcome := store.RecordAttemptArgsForCall(i)
			if outcome == pkg.FailedOutcome {
				wroteFailed = true
			}
		}
		Expect(wroteFailed).To(BeTrue(), "the visible failed marker must be written")
		Expect(store.SettleAnswerCallCount()).To(Equal(1),
			"the overdue undelivered card must be settled to answered, not closed")
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

	It("resumes an exited session through the headless arm when the pane refuses", func() {
		// The 2026-11-17 defect: a session once delivered to through a pane exited,
		// and every cycle refused with "no live process with a tty".
		headless := &fakeHeadlessDeliverer{terminal: true, canResume: true}
		job = pkg.NewAttentionWatcherJob(
			store, carrier, headless, time.Hour, libtime.NewCurrentDateTime(),
		)
		carrier.verdict = pkg.DeliveryRefuse
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(headless.calls).To(Equal([]pkg.ItemID{"item-1"}))
		// The headless arm settles the card itself; the pane arm records no failure.
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("keeps a live tty session on the pane path", func() {
		headless := &fakeHeadlessDeliverer{terminal: true, canResume: true}
		job = pkg.NewAttentionWatcherJob(
			store, carrier, headless, time.Hour, libtime.NewCurrentDateTime(),
		)
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(ConsistOf("item-1=allow"))
		Expect(headless.calls).To(BeEmpty())
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("refuses with a distinct reason when the exited session cannot be resumed", func() {
		headless := &fakeHeadlessDeliverer{terminal: true, canResume: false}
		job = pkg.NewAttentionWatcherJob(
			store, carrier, headless, time.Hour, libtime.NewCurrentDateTime(),
		)
		carrier.verdict = pkg.DeliveryRefuse
		store.RecordAttemptCountReturns(pkg.DeliveryAttemptParkThreshold, nil)
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(headless.calls).To(BeEmpty())
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
		// Parked, not retried in a hot loop, and the park names the missing fallback.
		Expect(store.ParkDeliveryCallCount()).To(Equal(1))
		_, park := store.ParkDeliveryArgsForCall(0)
		Expect(park.Verdict).To(Equal(pkg.DeliveryRefuse))
		Expect(park.Reason).To(ContainSubstring("cannot be resumed through the headless path"))
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

	// ⚠️ This is the only spec that pins storeReadTimeout, and it is the store
	// read's discriminating half: a spec that only asserted the constant's value
	// would pass on a constant wired to nothing. The fake's ReadBoard blocks until
	// the bound fires, so removing the WithTimeout leaves the call parked on the
	// fallback timer and the ceiling below fails.
	It("bounds the board read so a wedged store cannot stop the arm", func() {
		store.ReadBoardCalls(func(ctx context.Context) (pkg.Items, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(8 * time.Second):
				// The fallback is what makes an unbounded call fail the ceiling
				// instead of hanging the suite: with no deadline the arm parks here.
				return nil, nil
			}
		})

		start := time.Now()
		err := job.DeliverOnce(context.Background())
		elapsed := time.Since(start)

		// The read is bounded, so the failure surfaces as an error rather than a
		// wedged arm — that is the behaviour, not merely the elapsed time.
		Expect(err).To(HaveOccurred())
		// The lower bound is the positive control: if the read never ran, this
		// spec would pass vacuously on a store that returned immediately.
		Expect(elapsed).To(BeNumerically(">=", 1*time.Second),
			"the board read never blocked, so nothing was bounded")
		// The bound is 2 s, so anything approaching the 8 s fallback means the
		// declared bound is not the one in force.
		Expect(elapsed).To(BeNumerically("<=", 6*time.Second),
			"storeReadTimeout is not applied at the delivery arm's board read")
	})

	// The delivery bound. A busy or unreadable composer is a `retry`, which is
	// not a failure — so it recorded nothing and retried every two seconds for as
	// long as the card lived. Measured 2026-11-17 from the controller's own
	// stderr: 148 distinct ids entered this loop, the longest spans 3771 s, and
	// two of them were swept closed at 60 min still reading `never_attempted` —
	// the answer was lost with no marker at all. These specs pin the bound that
	// turns that into a marked, visible non-delivery while the arm keeps trying.
	//
	// ⚠️ The bound is anchored on the card's ANSWER time, so every case below
	// places AnsweredAt relative to the SAME clock the job reads.
	boundItem := func(clock libtime.CurrentDateTimeGetter, answeredAgo time.Duration) pkg.Item {
		item := answeredItem("item-1", "operator", pkg.Decision("allow"))
		answeredAt := libtime.DateTime(clock.Now().Time().Add(-answeredAgo))
		item.AnsweredAt = &answeredAt
		return item
	}

	It("marks an answer undeliverable once its retry loop passes the bound", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		carrier.verdict = pkg.DeliveryRetry
		store.ReadBoardReturns(pkg.Items{
			boundItem(clock, pkg.AttentionDeliveryBound+time.Second),
		}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		// Two records, in this order: the retry (an arm looked) and the marker
		// (it has waited past the bound). The first is the fix for the
		// never_attempted defect; the second is the operator's cue to look.
		Expect(store.RecordAttemptCallCount()).To(Equal(2))
		_, _, _, first := store.RecordAttemptArgsForCall(0)
		Expect(first).To(Equal(pkg.RetriedOutcome))
		_, _, markerCarrier, outcome := store.RecordAttemptArgsForCall(1)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
		Expect(markerCarrier).NotTo(Equal("watcher:gate-resolved"))
	})

	It("does not mark an answer undeliverable before the bound", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		carrier.verdict = pkg.DeliveryRetry
		store.ReadBoardReturns(pkg.Items{
			boundItem(clock, pkg.AttentionDeliveryBound-time.Second),
		}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		// The positive control for the spec above: the same retry verdict inside
		// the bound writes the RETRY record and nothing else, so the marker is the
		// bound firing and not the retry verdict itself.
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.RetriedOutcome))
	})

	It("writes the undeliverable marker exactly once across retries", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		carrier.verdict = pkg.DeliveryRetry
		item := boundItem(clock, 10*time.Minute)
		store.ReadBoardReturns(pkg.Items{item}, nil)

		// First tick past the bound writes the retry record and the marker.
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(2))
		_, _, markerCarrier, outcome := store.RecordAttemptArgsForCall(1)
		Expect(outcome).To(Equal(pkg.FailedOutcome))

		// A later tick, once the backoff has passed, must not re-write either. The
		// record carries the marker's own carrier, so it IS this answer's marker —
		// that, and not the mere presence of an attempt, is what suppresses.
		markedAt := libtime.DateTime(clock.Now().Time())
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {
				ItemID:      pkg.ItemID("item-1"),
				Status:      pkg.FailedStatus,
				Carrier:     markerCarrier,
				AttemptedAt: &markedAt,
			},
		}, nil)
		clock.SetNow(
			clock.Now().Add(libtime.Duration(pkg.AttentionDeliveryRetryBackoff + time.Second)),
		)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(2))
	})

	// The regression this bound shipped without: card f1b46ed9… (2026-11-17) was
	// refused 0.4 s after its answer, then retried for 1216 s with no marker at
	// all. The guard treated that earlier refuse — an attempt at/after the answer
	// but BEFORE the retry loop — as proof the loop was already marked.
	It("marks the loop even when an earlier refuse was recorded after the answer", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		carrier.verdict = pkg.DeliveryRetry
		item := boundItem(clock, 10*time.Minute)
		// A refuse written after the answer but before the loop began: exactly the
		// shape the old guard mistook for a marker.
		refusedAt := libtime.DateTime(clock.Now().Time().Add(-9 * time.Minute))
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {
				ItemID:      pkg.ItemID("item-1"),
				Status:      pkg.FailedStatus,
				Carrier:     "watcher:attention-watcher",
				AttemptedAt: &refusedAt,
			},
		}, nil)
		store.ReadBoardReturns(pkg.Items{item}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, writtenCarrier, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
		Expect(writtenCarrier).NotTo(Equal("watcher:attention-watcher"),
			"the marker must be distinguishable from an ordinary attempt")
	})

	It("keeps retrying after the marker so a later delivery settles delivered", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		carrier.verdict = pkg.DeliveryRetry
		store.ReadBoardReturns(pkg.Items{
			boundItem(clock, pkg.AttentionDeliveryBound+time.Second),
		}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(2))

		// The store now carries the marker; the arm retries once the backoff has
		// passed, and a successful attempt settles the record as delivered. The
		// bound must never be satisfied by dropping the answer.
		markedAt := libtime.DateTime(clock.Now().Time())
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {
				ItemID:      pkg.ItemID("item-1"),
				Status:      pkg.FailedStatus,
				Carrier:     "watcher:attention-watcher:undeliverable",
				AttemptedAt: &markedAt,
			},
		}, nil)
		clock.SetNow(
			clock.Now().Add(libtime.Duration(pkg.AttentionDeliveryRetryBackoff + time.Second)),
		)
		carrier.verdict = pkg.DeliveryOK

		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		// Neither the retry record nor the marker is rewritten: the marker's
		// carrier is this answer's own, so only the delivery is added.
		Expect(store.RecordAttemptCallCount()).To(Equal(3))
		_, _, _, outcome := store.RecordAttemptArgsForCall(2)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	// SC3's pin, adapted to the port: the Python watcher had a booked and an
	// unbooked arm and this constant bounded only one of them. The port unified
	// both into a single arm, so one constant governs every retry. The literal
	// scan is the discriminating half — a constant assertion alone would pass on a
	// value re-typed at a call site, which is exactly how one path drifts
	// unbounded while the constant still reads five minutes.
	It("pins the single delivery bound and forbids a second five-minute literal", func() {
		Expect(pkg.AttentionDeliveryBound).To(Equal(5 * time.Minute))

		src, err := os.ReadFile("attention-watcher-job.go")
		Expect(err).NotTo(HaveOccurred())
		// One literal, in AttentionDeliveryRetryBackoff, from which the bound is
		// derived. A second 5-minute literal in the arm is a duplicated bound.
		Expect(strings.Count(string(src), "5 * time.Minute")).To(Equal(1),
			"the delivery arm carries a second five-minute literal")
		Expect(string(src)).To(ContainSubstring("answeredAt.Add(AttentionDeliveryBound)"),
			"the bound must be read by name, never re-typed at the call site")
	})
})
