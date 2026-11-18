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

		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
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
		// the bound writes nothing, so the marker is the bound firing and not the
		// retry verdict itself.
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("writes the undeliverable marker exactly once across retries", func() {
		clock := libtime.NewCurrentDateTime()
		job = pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, clock,
		)
		carrier.verdict = pkg.DeliveryRetry
		item := boundItem(clock, 10*time.Minute)
		store.ReadBoardReturns(pkg.Items{item}, nil)

		// First tick past the bound writes the marker.
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, markerCarrier, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))

		// A later tick, once the backoff has passed, must not re-write it. The
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
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
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
		Expect(store.RecordAttemptCallCount()).To(Equal(1))

		// The store now carries the marker; the arm retries once the backoff has
		// passed, and a successful attempt settles the record as delivered. The
		// bound must never be satisfied by dropping the answer.
		markedAt := libtime.DateTime(clock.Now().Time())
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {
				ItemID:      pkg.ItemID("item-1"),
				Status:      pkg.FailedStatus,
				AttemptedAt: &markedAt,
			},
		}, nil)
		clock.SetNow(
			clock.Now().Add(libtime.Duration(pkg.AttentionDeliveryRetryBackoff + time.Second)),
		)
		carrier.verdict = pkg.DeliveryOK

		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(store.RecordAttemptCallCount()).To(Equal(2))
		_, _, _, outcome := store.RecordAttemptArgsForCall(1)
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
