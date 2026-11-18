// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// headlessSessionID is the producer every headless spec delivers to.
const headlessSessionID = "11111111-2222-3333-4444-555555555555"

// fakeHeadlessDeliverer is a scripted HeadlessAnswerDeliverer.
type fakeHeadlessDeliverer struct {
	calls     []pkg.ItemID
	terminal  bool
	canResume bool
}

func (f *fakeHeadlessDeliverer) DeliverHeadless(_ context.Context, item pkg.Item) bool {
	f.calls = append(f.calls, item.ItemID)
	return f.terminal
}

// CanResume reports the scripted answer, so a spec can drive the menu arm's
// reaped-session branch without a wrapper.
func (f *fakeHeadlessDeliverer) CanResume(_ context.Context, _ string) bool {
	return f.canResume
}

// fakeMessageWrapper is a scripted WrapperMessageDeliverer: it replays a queue
// of verdicts (the last entry repeats) and records every turn it was given.
type fakeMessageWrapper struct {
	verdicts []pkg.DeliveryVerdict
	reasons  []string
	index    int
	// delivered records "session|cwd|text" per call.
	delivered []string
	// mode is what SessionMode reports; "" means the wrapper holds no record.
	mode string
}

func (f *fakeMessageWrapper) SessionMode(_ context.Context, _ string) (string, bool) {
	return f.mode, f.mode != ""
}

func (f *fakeMessageWrapper) DeliverMessage(
	_ context.Context,
	sessionID string,
	cwd string,
	text string,
) (pkg.DeliveryVerdict, string) {
	f.delivered = append(f.delivered, sessionID+"|"+cwd+"|"+text)
	verdict := pkg.DeliveryOK
	reason := "scripted"
	if len(f.verdicts) > 0 {
		verdict = f.verdicts[f.index]
		if f.index < len(f.verdicts)-1 {
			f.index++
		}
	}
	if len(f.reasons) > f.index {
		reason = f.reasons[f.index]
	}
	return verdict, reason
}

// fakeAnswerContext is a scripted AnswerContextResolver.
type fakeAnswerContext struct {
	context pkg.AnswerContext
}

func (f *fakeAnswerContext) AnswerContext(
	_ context.Context,
	_ pkg.Item,
) pkg.AnswerContext {
	return f.context
}

// headlessItem builds an answered menu card asked by a headless session.
func headlessItem(id string) pkg.Item {
	answeredAt := libtime.NewCurrentDateTime().Now()
	return pkg.Item{
		ItemID:          pkg.ItemID(id),
		ProducerID:      pkg.ProducerID(headlessSessionID),
		LivenessRef:     pkg.LivenessRef("session:" + headlessSessionID),
		ProvenanceClass: pkg.HookProvenanceClass,
		AnswerMechanism: pkg.MessageAnswerMechanism,
		Payload:         "Should the migration run tonight?",
		Options:         pkg.AnswerOptions{{Label: "Yes"}, {Label: "No"}},
		State:           pkg.AnsweredState,
		AnsweredAt:      &answeredAt,
		AnsweredBy:      "operator",
		Answer:          &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"},
	}
}

// fakeClock returns a fixed instant.
func fakeClock(now time.Time) libtime.CurrentDateTimeGetter {
	return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
		return libtime.DateTime(now)
	})
}

var _ = Describe("headless answer delivery", func() {
	var (
		store    *mocks.AttentionStore
		wrapper  *fakeMessageWrapper
		contexts *fakeAnswerContext
		now      time.Time
		delivery pkg.HeadlessAnswerDeliverer
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		wrapper = &fakeMessageWrapper{verdicts: []pkg.DeliveryVerdict{pkg.DeliveryOK}}
		contexts = &fakeAnswerContext{
			context: pkg.AnswerContext{Cwd: "/workspace/attention-controller"},
		}
		now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
		delivery = pkg.NewHeadlessAnswerDelivery(store, wrapper, contexts, fakeClock(now))
	})

	It("delivers the answer once through the wrapper's message route", func() {
		item := headlessItem("item-1")
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())

		Expect(wrapper.delivered).To(HaveLen(1))
		Expect(wrapper.delivered[0]).To(ContainSubstring(headlessSessionID + "|/workspace/"))
		// The turn carries the question AND the answer: the session has ended its
		// turn and holds no memory of the card.
		Expect(wrapper.delivered[0]).To(ContainSubstring("Should the migration run tonight?"))
		Expect(wrapper.delivered[0]).To(ContainSubstring("Yes"))

		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, itemID, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(itemID).To(Equal(pkg.ItemID("item-1")))
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("does not deliver a second time on a later pass", func() {
		item := headlessItem("item-1")
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(wrapper.delivered).To(HaveLen(1), "a delivered answer must never be re-sent")
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
	})

	It("ends the turn with the task command only when the task is known", func() {
		item := headlessItem("item-1")
		contexts.context.TaskName = "Migrate Kafka to Per-Env Clusters"
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(wrapper.delivered[0]).To(HaveSuffix(
			"/vault-cli:work-on-task 'Migrate Kafka to Per-Env Clusters'\n",
		))
	})

	It("omits the task command when no task resolved", func() {
		item := headlessItem("item-1")
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(wrapper.delivered[0]).NotTo(ContainSubstring("/vault-cli:work-on-task"))
	})

	It("leaves the answer pending while the wrapper is unreachable and retries", func() {
		// The wrapper is down: nothing was learned, so nothing failed. Recording
		// `failed` here would drop the operator's answer over a transient outage —
		// but recording NOTHING left the card reading `never_attempted`, which the
		// lost-answer counter counts. `retried` is the third thing it can say.
		wrapper.verdicts = []pkg.DeliveryVerdict{
			pkg.DeliveryRetry,
			pkg.DeliveryRetry,
			pkg.DeliveryOK,
		}
		wrapper.reasons = []string{"wrapper unreachable", "wrapper unreachable", "delivered"}
		item := headlessItem("item-1")

		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeFalse())
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeFalse())
		Expect(store.RecordAttemptCallCount()).To(Equal(1),
			"one record for the retry, not one per cycle")
		_, _, _, retryOutcome := store.RecordAttemptArgsForCall(0)
		Expect(retryOutcome).To(Equal(pkg.RetriedOutcome))

		// The wrapper recovers and the answer lands exactly once.
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(store.RecordAttemptCallCount()).To(Equal(2))
		_, _, _, outcome := store.RecordAttemptArgsForCall(1)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("stops retrying at the item's deadline and records the failure", func() {
		wrapper.verdicts = []pkg.DeliveryVerdict{pkg.DeliveryRetry}
		wrapper.reasons = []string{"wrapper unreachable"}
		item := headlessItem("item-1")
		expired := libtime.DateTime(now.Add(-time.Minute))
		item.ExpiresAt = &expired

		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(wrapper.delivered).To(BeEmpty(), "an expired answer must not be delivered")
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
	})

	It("refuses an item whose producer id cannot be a wrapper key", func() {
		item := headlessItem("item-1")
		item.ProducerID = "session-a"
		item.LivenessRef = pkg.LivenessRef("owner:session-a")
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(wrapper.delivered).To(BeEmpty())
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
	})

	It("records a failure when the card carries no answer at all", func() {
		item := headlessItem("item-1")
		item.Answer = nil
		Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
		Expect(wrapper.delivered).To(BeEmpty())
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
	})
	// ⚠️ ONE deliverer is wired into both the attention-watcher job and the
	// menu-answer job, and those run concurrently on `run.NewConcurrentRunner`.
	// Before the mutex, two cycles touching the delivered/attempts maps at the
	// same instant were an unrecoverable fatal error, not a recoverable panic.
	It("delivers once when two concurrent cycles race on the same card", func() {
		item := headlessItem("item-race")
		var wg sync.WaitGroup
		wg.Add(2)
		for range 2 {
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				delivery.DeliverHeadless(context.Background(), item)
			}()
		}
		wg.Wait()

		Expect(wrapper.delivered).To(HaveLen(1),
			"a re-sent turn is a second instruction to a worker that already acted on the first")
	})

	It(
		"quotes a task name carrying an apostrophe so the closing command stays one argument",
		func() {
			contexts.context = pkg.AnswerContext{
				Cwd:      "/workspace/attention-controller",
				TaskName: "A Worker's Ask Reaches an Answerer",
			}
			item := headlessItem("item-quote")
			Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())

			Expect(wrapper.delivered).To(HaveLen(1))
			// Single-quoted with the apostrophe escaped: the shell must see ONE
			// argument, and the name must not be able to inject syntax.
			Expect(wrapper.delivered[0]).To(ContainSubstring(
				`/vault-cli:work-on-task 'A Worker'\''s Ask Reaches an Answerer'`))
		},
	)

	It("leaves the card pending when the cycle was already cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		item := headlessItem("item-cancelled")
		Expect(delivery.DeliverHeadless(ctx, item)).To(BeFalse())
		Expect(wrapper.delivered).To(BeEmpty(), "nothing was attempted, so nothing may be recorded")
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})
})

var _ = Describe("menu answer job headless path", func() {
	var (
		store    *mocks.AttentionStore
		locator  *fakeLocator
		writer   *fakeMenuWriter
		wrapper  *fakeMessageWrapper
		delivery pkg.HeadlessAnswerDeliverer
		job      pkg.MenuAnswerJob
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		locator = &fakeLocator{verdict: pkg.DeliveryNoPane, reason: "session is pane-less"}
		writer = &fakeMenuWriter{unreadableAfter: -1}
		wrapper = &fakeMessageWrapper{verdicts: []pkg.DeliveryVerdict{pkg.DeliveryOK}}
		delivery = pkg.NewHeadlessAnswerDelivery(
			store,
			wrapper,
			&fakeAnswerContext{},
			libtime.NewCurrentDateTime(),
		)
		job = pkg.NewMenuAnswerJob(
			store, locator, writer, time.Hour,
			pkg.WithMenuAnswerTimings(0, 2),
			pkg.WithMenuAnswerMaxCycles(2),
			pkg.WithMenuAnswerHeadlessDelivery(delivery),
		)
	})

	It("carries a headless session's menu answer through the wrapper once", func() {
		store.ReadBoardReturns(pkg.Items{headlessItem("item-1")}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(wrapper.delivered).To(HaveLen(1))
		Expect(writer.sent).To(BeEmpty(), "no keys can be typed into a pane-less session")

		// A second cycle must not deliver the same answer again.
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(wrapper.delivered).To(HaveLen(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("retries a pending headless delivery on the next cycle", func() {
		wrapper.verdicts = []pkg.DeliveryVerdict{pkg.DeliveryRetry, pkg.DeliveryOK}
		wrapper.reasons = []string{"wrapper unreachable", "delivered"}
		store.ReadBoardReturns(pkg.Items{headlessItem("item-1")}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		// The first cycle records the retry, so the card never reads
		// `never_attempted` while an arm is retrying it.
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, retryOutcome := store.RecordAttemptArgsForCall(0)
		Expect(retryOutcome).To(Equal(pkg.RetriedOutcome))

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(wrapper.delivered).To(HaveLen(2))
		_, _, _, outcome := store.RecordAttemptArgsForCall(1)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("keeps the keystroke path for a session with a pane", func() {
		locator.verdict = pkg.DeliveryOK
		locator.pane = 42
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}, {Label: "No"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{"pick one\nEnter to select", "working\n"}

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(Equal([]string{"1"}))
		Expect(wrapper.delivered).To(BeEmpty(), "a tab session is answered in its pane")
	})
})
