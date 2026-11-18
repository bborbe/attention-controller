// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// attributionVector is one row of the committed fixture: a stable name, the
// verdict the ladder must return, and the item to classify.
type attributionVector struct {
	Name    string   `json:"name"`
	Verdict string   `json:"verdict"`
	Item    pkg.Item `json:"item"`
}

var _ = Describe("AnsweredVersusNotOperatorAnsweredDeclinedHidden attribution ladder", func() {
	// The fixture is committed under pkg/testdata/ and read from disk on every
	// pass, exactly as completed-task-source_test.go reads its fixture. The rows
	// are the contract: a spec that built its items inline would keep passing
	// after the committed table was deleted.
	const fixture = "testdata/answer-attribution-vectors.json"

	var ctx context.Context
	var vectors []attributionVector

	BeforeEach(func() {
		ctx = context.Background()
		raw, err := os.ReadFile(fixture)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(raw, &vectors)).To(Succeed())
	})

	It("classifies every vector as declared with a non-empty reason", func() {
		for _, vector := range vectors {
			verdict, reason := pkg.ClassifyAnswerAttribution(ctx, vector.Item)
			Expect(verdict).To(Equal(pkg.AttributionVerdict(vector.Verdict)), vector.Name)
			Expect(reason).NotTo(BeEmpty(), vector.Name)
		}
	})

	It("covers every verdict", func() {
		covered := make(map[pkg.AttributionVerdict]bool, len(vectors))
		for _, vector := range vectors {
			covered[pkg.AttributionVerdict(vector.Verdict)] = true
		}
		for _, verdict := range []pkg.AttributionVerdict{
			pkg.AttributionVerdictNothing,
			pkg.AttributionVerdictUnattributed,
			pkg.AttributionVerdictDeclined,
			pkg.AttributionVerdictHidden,
			pkg.AttributionVerdictAutomated,
			pkg.AttributionVerdictAttributed,
		} {
			Expect(covered).To(HaveKey(verdict))
		}
	})

	It("carries the three boundary rows by name", func() {
		names := make([]string, 0, len(vectors))
		for _, vector := range vectors {
			names = append(names, vector.Name)
		}
		// Deleting any one of these rows must fail here rather than merely shrink
		// a count elsewhere.
		Expect(names).To(ContainElements(
			"unattributed-close-without-answered-at",
			"declined-skip-by-automation",
			"attributed-with-resolved-by",
		))
	})

	It("never reads a scripted click as the operator's answer", func() {
		row := vectorNamed(vectors, "automated-scripted-click")
		verdict, reason := pkg.ClassifyAnswerAttribution(ctx, row.Item)
		Expect(verdict).To(Equal(pkg.AttributionVerdictAutomated))
		Expect(verdict).NotTo(Equal(pkg.AttributionVerdictAttributed))
		Expect(reason).To(ContainSubstring("automated"))
	})

	It("derives the rendered vocabulary from the verdict", func() {
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictAttributed)).To(Equal("ANSWERED"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictDeclined)).To(Equal("DECLINED"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictHidden)).To(Equal("HIDDEN"))
		Expect(
			pkg.RenderedAnswerSignal(pkg.AttributionVerdictNothing),
		).To(Equal("NOT_OPERATOR_ANSWERED"))
		Expect(
			pkg.RenderedAnswerSignal(pkg.AttributionVerdictUnattributed),
		).To(Equal("NOT_OPERATOR_ANSWERED"))
		Expect(
			pkg.RenderedAnswerSignal(pkg.AttributionVerdictAutomated),
		).To(Equal("NOT_OPERATOR_ANSWERED"))
		Expect(
			pkg.RenderedAnswerSignal(pkg.AttributionVerdict("UNKNOWN")),
		).To(Equal("NOT_OPERATOR_ANSWERED"))
	})

	It("does not require resolved_by", func() {
		without := vectorNamed(vectors, "attributed-board-click")
		with := vectorNamed(vectors, "attributed-with-resolved-by")
		// The pair is the guard: one row has no resolved_by and one carries it, and
		// both must classify ATTRIBUTED. Adding a resolved_by requirement fails here.
		Expect(without.Item.ResolvedBy).To(BeEmpty())
		Expect(with.Item.ResolvedBy).NotTo(BeEmpty())

		withoutVerdict, _ := pkg.ClassifyAnswerAttribution(ctx, without.Item)
		withVerdict, _ := pkg.ClassifyAnswerAttribution(ctx, with.Item)
		Expect(withoutVerdict).To(Equal(pkg.AttributionVerdictAttributed))
		Expect(withVerdict).To(Equal(pkg.AttributionVerdictAttributed))
	})
})

// vectorNamed returns the row with the given name, failing the spec when it is
// absent so a deleted row names itself in the failure.
func vectorNamed(vectors []attributionVector, name string) attributionVector {
	for _, vector := range vectors {
		if vector.Name == name {
			return vector
		}
	}
	Fail("vector not found: " + name)
	return attributionVector{}
}

// recordingSignaller is a scripted AnsweredSignaller: it records every call
// and reports whether it emitted.
type recordingSignaller struct {
	calls []pkg.ItemID
	emits bool
}

func (r *recordingSignaller) Signal(_ context.Context, _ string, item pkg.Item) bool {
	r.calls = append(r.calls, item.ItemID)
	return r.emits
}

// recordingAnsweredSignalRecorder records every line the signaller emits.
type recordingAnsweredSignalRecorder struct {
	lines []string
}

func (r *recordingAnsweredSignalRecorder) RecordAnswered(
	_ context.Context,
	itemID pkg.ItemID,
	verdict pkg.AttributionVerdict,
) {
	r.lines = append(r.lines, pkg.RenderedAnswerSignal(verdict)+" "+string(itemID))
}

// signalledPaneItem is a pane-answered card the ladder classifies ATTRIBUTED:
// an answered state, an answer timestamp, a non-automated client, and a producer
// that is the asking session.
func signalledPaneItem(id string) pkg.Item {
	answeredAt := libtime.NewCurrentDateTime().Now()
	return pkg.Item{
		ItemID:         pkg.ItemID(id),
		ProducerID:     pkg.ProducerID(headlessSessionID),
		LivenessRef:    pkg.LivenessRef("session:" + headlessSessionID),
		State:          pkg.AnsweredState,
		AnsweredAt:     &answeredAt,
		AnsweredBy:     "operator",
		Decision:       pkg.Decision("allow"),
		AnsweredClient: &pkg.AnsweredClient{},
	}
}

// positiveMenuCard is an answered hook menu card the ladder classifies
// ATTRIBUTED. answeredMenuItem leaves ProducerID and AnsweredClient unset, so
// both are set here — without them the signaller correctly emits nothing.
func positiveMenuCard(id string) pkg.Item {
	item := answeredMenuItem(id, nil)
	item.Options = pkg.AnswerOptions{{Label: "Yes"}, {Label: "No"}}
	item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
	item.ProducerID = pkg.ProducerID(headlessSessionID)
	item.AnsweredClient = &pkg.AnsweredClient{}
	return item
}

// drivePaneArm drives one DeliveryOK through the pane arm and returns its
// carrier.
func drivePaneArm(signaller pkg.AnsweredSignaller, item pkg.Item) *recordingCarrier {
	store := &mocks.AttentionStore{}
	store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
	store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
	store.ReadBoardReturns(pkg.Items{item}, nil)
	carrier := &recordingCarrier{verdict: pkg.DeliveryOK}
	job := pkg.NewAttentionWatcherJob(
		store, carrier, &fakeHeadlessDeliverer{}, time.Hour, libtime.NewCurrentDateTime(),
		pkg.WithAttentionWatcherSignaller(signaller),
	)
	Expect(job.DeliverOnce(context.Background())).To(Succeed())
	Expect(carrier.calls).To(HaveLen(1))
	return carrier
}

// driveHeadlessArm drives one DeliveryOK through the headless arm.
func driveHeadlessArm(signaller pkg.AnsweredSignaller, item pkg.Item) *fakeMessageWrapper {
	store := &mocks.AttentionStore{}
	store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
	wrapper := &fakeMessageWrapper{verdicts: []pkg.DeliveryVerdict{pkg.DeliveryOK}}
	contexts := &fakeAnswerContext{context: pkg.AnswerContext{Cwd: "/tmp/ws"}}
	delivery := pkg.NewHeadlessAnswerDelivery(
		store, wrapper, contexts,
		fakeClock(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)),
		pkg.WithHeadlessAnswerSignaller(signaller),
	)
	Expect(delivery.DeliverHeadless(context.Background(), item)).To(BeTrue())
	Expect(wrapper.delivered).To(HaveLen(1))
	return wrapper
}

// driveMenuArm drives one DeliveryOK through the menu arm.
func driveMenuArm(signaller pkg.AnsweredSignaller, item pkg.Item) *fakeMenuWriter {
	store := &mocks.AttentionStore{}
	store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
	store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
	store.ReadBoardReturns(pkg.Items{item}, nil)
	locator := &fakeLocator{verdict: pkg.DeliveryOK, pane: 42}
	writer := &fakeMenuWriter{
		unreadableAfter: -1,
		texts: []string{
			"pick one\nEnter to select",
			"working\n",
		},
	}
	job := pkg.NewMenuAnswerJob(
		store, locator, writer, time.Hour,
		pkg.WithMenuAnswerTimings(0, 2),
		pkg.WithMenuAnswerSignaller(signaller),
	)
	Expect(job.DeliverOnce(context.Background())).To(Succeed())
	Expect(writer.sent).To(Equal([]string{"1"}))
	return writer
}

// driveSettleArm drives one DeliveryOK through the routine-gate settle arm.
func driveSettleArm(signaller pkg.AnsweredSignaller, item pkg.Item) *fakeWrapper {
	store := &mocks.AttentionStore{}
	store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
	store.ReadReturns(pkg.Items{item}, nil)
	store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
	wrapper := &fakeWrapper{
		state: pkg.WrapperProbeOK,
		session: &pkg.WrapperSession{
			Question: &pkg.WrapperQuestion{
				RequestID: "req-1",
				Raw: []byte(
					`{"request_id":"req-1","input":{"arguments":{"tool_name":"Read","input":{"file_path":"/tmp/ws/a.go"}}}}`,
				),
			},
		},
		verdict: pkg.DeliveryOK,
	}
	policy := &fakePolicy{rule: "routine-tool:Read", allowed: true}
	sessions := &fakeSessions{
		entry: pkg.SessionEntry{PID: os.Getpid(), Entrypoint: "sdk-cli", CWD: "/tmp/ws"},
		has:   true,
	}
	job := pkg.NewRoutineGateSettleJob(
		store, wrapper, policy, sessions, time.Hour, libtime.NewCurrentDateTime(),
		pkg.WithRoutineGateSettleSignaller(signaller),
	)
	Expect(job.SettleOnce(context.Background())).To(Succeed())
	Expect(wrapper.answered).To(HaveLen(1))
	return wrapper
}

// driveAllFourArms drives one DeliveryOK through every arm, each with a
// distinct item id.
func driveAllFourArms(signaller pkg.AnsweredSignaller) {
	drivePaneArm(signaller, signalledPaneItem("item-pane"))
	headless := headlessItem("item-headless")
	headless.AnsweredClient = &pkg.AnsweredClient{}
	driveHeadlessArm(signaller, headless)
	driveMenuArm(signaller, positiveMenuCard("item-menu"))
	driveSettleArm(signaller, permissionItem("item-settle"))
}

var _ = Describe("AnsweredReadFromItemStateNotOpenList", func() {
	It("signals exactly once from an attributed item's own state", func() {
		recorder := &recordingAnsweredSignalRecorder{}
		drivePaneArm(pkg.NewAnsweredSignaller(recorder), signalledPaneItem("item-1"))
		Expect(recorder.lines).To(Equal([]string{"ANSWERED item-1"}))
	})

	It("declines an attributed card whose producer is a peer session", func() {
		item := signalledPaneItem("item-1")
		// The liveness ref still names the watching session; only the producer
		// differs, so the arm still delivers and the signaller alone declines.
		item.ProducerID = pkg.ProducerID("22222222-3333-4444-5555-666666666666")
		recorder := &recordingAnsweredSignalRecorder{}
		carrier := drivePaneArm(pkg.NewAnsweredSignaller(recorder), item)
		Expect(carrier.calls).To(HaveLen(1), "the arm must deliver before the signaller declines")
		Expect(recorder.lines).To(BeEmpty())
	})

	It("declines an automated click even though the arm delivered it", func() {
		automated := true
		item := signalledPaneItem("item-1")
		item.AnsweredClient = &pkg.AnsweredClient{Automation: &automated}
		recorder := &recordingAnsweredSignalRecorder{}
		carrier := drivePaneArm(pkg.NewAnsweredSignaller(recorder), item)
		Expect(carrier.calls).To(HaveLen(1), "the arm must deliver before the signaller declines")
		Expect(recorder.lines).To(BeEmpty())
	})
})

var _ = Describe("AnsweredSignalsOnceAcrossTicks", func() {
	It("signals one answered card exactly once over three ticks", func() {
		store := &mocks.AttentionStore{}
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		store.ReadBoardReturns(pkg.Items{signalledPaneItem("item-1")}, nil)
		carrier := &recordingCarrier{verdict: pkg.DeliveryOK}
		recorder := &recordingAnsweredSignalRecorder{}
		job := pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, libtime.NewCurrentDateTime(),
			pkg.WithAttentionWatcherSignaller(pkg.NewAnsweredSignaller(recorder)),
		)
		for i := 0; i < 3; i++ {
			Expect(job.DeliverOnce(context.Background())).To(Succeed())
		}
		// The mock keeps reporting the card deliverable, so the arm delivers on
		// every tick; the signaller is what emits only once.
		Expect(carrier.calls).To(HaveLen(3))
		Expect(recorder.lines).To(Equal([]string{"ANSWERED item-1"}))
	})

	It("signals a second answered card its own single signal", func() {
		store := &mocks.AttentionStore{}
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		store.ReadBoardReturns(pkg.Items{
			signalledPaneItem("item-1"),
			signalledPaneItem("item-2"),
		}, nil)
		recorder := &recordingAnsweredSignalRecorder{}
		job := pkg.NewAttentionWatcherJob(
			store, &recordingCarrier{verdict: pkg.DeliveryOK}, &fakeHeadlessDeliverer{},
			time.Hour, libtime.NewCurrentDateTime(),
			pkg.WithAttentionWatcherSignaller(pkg.NewAnsweredSignaller(recorder)),
		)
		for i := 0; i < 3; i++ {
			Expect(job.DeliverOnce(context.Background())).To(Succeed())
		}
		Expect(recorder.lines).To(HaveLen(2))
		Expect(recorder.lines).To(ConsistOf("ANSWERED item-1", "ANSWERED item-2"))
	})
})

var _ = Describe("AnsweredWhileDownSignalsOnRestart", func() {
	It("signals an answer that arrived while the job was down on the first tick", func() {
		store := &mocks.AttentionStore{}
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		store.ReadBoardReturns(pkg.Items{signalledPaneItem("item-1")}, nil)
		recorder := &recordingAnsweredSignalRecorder{}
		job := pkg.NewAttentionWatcherJob(
			store, &recordingCarrier{verdict: pkg.DeliveryOK}, &fakeHeadlessDeliverer{},
			time.Hour, libtime.NewCurrentDateTime(),
			pkg.WithAttentionWatcherSignaller(pkg.NewAnsweredSignaller(recorder)),
		)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(recorder.lines).To(Equal([]string{"ANSWERED item-1"}))
	})

	It("does not re-signal a card already carried before the restart", func() {
		store := &mocks.AttentionStore{}
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			"item-1": {ItemID: pkg.ItemID("item-1"), Status: pkg.DeliveredStatus},
		}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		store.ReadBoardReturns(pkg.Items{signalledPaneItem("item-1")}, nil)
		carrier := &recordingCarrier{verdict: pkg.DeliveryOK}
		recorder := &recordingAnsweredSignalRecorder{}
		job := pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, libtime.NewCurrentDateTime(),
			pkg.WithAttentionWatcherSignaller(pkg.NewAnsweredSignaller(recorder)),
		)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
		Expect(recorder.lines).To(BeEmpty())
	})
})

var _ = Describe("EveryDeliveryArmSignals", func() {
	It("calls the shared helper once from each arm's DeliveryOK", func() {
		recorder := &recordingSignaller{emits: true}
		driveAllFourArms(recorder)
		Expect(recorder.calls).To(HaveLen(4))
		Expect(recorder.calls).To(ConsistOf(
			pkg.ItemID("item-pane"),
			pkg.ItemID("item-headless"),
			pkg.ItemID("item-menu"),
			pkg.ItemID("item-settle"),
		))
	})

	It("emits nothing from the settle arm's DeliveryOK", func() {
		// The settle arm only sees open gates, and an open card classifies
		// NOTHING. It reaches the helper (the spec above) but never emits.
		recorder := &recordingAnsweredSignalRecorder{}
		driveSettleArm(pkg.NewAnsweredSignaller(recorder), permissionItem("item-settle"))
		Expect(recorder.lines).To(BeEmpty())
	})

	It("emits one ANSWERED line from each answer-carrying arm", func() {
		recorder := &recordingAnsweredSignalRecorder{}
		signaller := pkg.NewAnsweredSignaller(recorder)

		drivePaneArm(signaller, signalledPaneItem("item-pane"))
		headless := headlessItem("item-headless")
		headless.AnsweredClient = &pkg.AnsweredClient{}
		driveHeadlessArm(signaller, headless)
		driveMenuArm(signaller, positiveMenuCard("item-menu"))

		Expect(recorder.lines).To(ConsistOf(
			"ANSWERED item-pane",
			"ANSWERED item-headless",
			"ANSWERED item-menu",
		))
	})

	It("leaves a hook menu card to the menu arm", func() {
		item := positiveMenuCard("item-menu")
		recorder := &recordingAnsweredSignalRecorder{}
		signaller := pkg.NewAnsweredSignaller(recorder)

		// The pane arm skips the card: no delivery and no signal.
		paneStore := &mocks.AttentionStore{}
		paneStore.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		paneStore.ReadBoardReturns(pkg.Items{item}, nil)
		paneCarrier := &recordingCarrier{verdict: pkg.DeliveryOK}
		paneJob := pkg.NewAttentionWatcherJob(
			paneStore, paneCarrier, &fakeHeadlessDeliverer{}, time.Hour,
			libtime.NewCurrentDateTime(),
			pkg.WithAttentionWatcherSignaller(signaller),
		)
		Expect(paneJob.DeliverOnce(context.Background())).To(Succeed())
		Expect(paneCarrier.calls).To(BeEmpty())
		Expect(recorder.lines).To(BeEmpty())

		// The menu arm carries it and emits exactly one line.
		driveMenuArm(signaller, item)
		Expect(recorder.lines).To(Equal([]string{"ANSWERED item-menu"}))
	})
})

var _ = Describe("FailOpenOnErrorMalformedDeadline", func() {
	It("emits nothing on an error, a malformed record or a deadline, then recovers", func() {
		store := &mocks.AttentionStore{}
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)

		malformed := signalledPaneItem("item-malformed")
		malformed.LivenessRef = pkg.LivenessRef("unparseable")
		store.ReadBoardReturnsOnCall(0, nil, errors.New("store down"))
		store.ReadBoardReturnsOnCall(1, pkg.Items{malformed}, nil)
		store.ReadBoardReturnsOnCall(2, nil, context.DeadlineExceeded)
		store.ReadBoardReturnsOnCall(3, pkg.Items{signalledPaneItem("item-good")}, nil)

		carrier := &recordingCarrier{verdict: pkg.DeliveryOK}
		recorder := &recordingAnsweredSignalRecorder{}
		// ONE job object across the failure and the recovery: the spec proves it
		// kept running rather than being rebuilt.
		job := pkg.NewAttentionWatcherJob(
			store, carrier, &fakeHeadlessDeliverer{}, time.Hour, libtime.NewCurrentDateTime(),
			pkg.WithAttentionWatcherSignaller(pkg.NewAnsweredSignaller(recorder)),
		)

		Expect(job.DeliverOnce(context.Background())).To(HaveOccurred())
		Expect(recorder.lines).To(BeEmpty())

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty(), "a malformed record must not be delivered")
		Expect(recorder.lines).To(BeEmpty())

		Expect(job.DeliverOnce(context.Background())).To(HaveOccurred())
		Expect(recorder.lines).To(BeEmpty())

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(HaveLen(1))
		Expect(recorder.lines).To(Equal([]string{"ANSWERED item-good"}))
	})
})

var _ = Describe("SignalBoundedByDeliveryInterval", func() {
	It("is bounded by the existing delivery tick and owns no timer", func() {
		Expect(pkg.AttentionWatcherDeliveryInterval).To(Equal(2 * time.Second))

		recorder := &recordingAnsweredSignalRecorder{}
		drivePaneArm(pkg.NewAnsweredSignaller(recorder), signalledPaneItem("item-1"))
		// The signal is already emitted when DeliverOnce returns: it rides the
		// delivery cycle, not a later timer.
		Expect(recorder.lines).To(Equal([]string{"ANSWERED item-1"}))

		src, err := os.ReadFile("answered-signal.go")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(src)).NotTo(ContainSubstring("time.NewTicker"))
		Expect(string(src)).NotTo(ContainSubstring("time.NewTimer"))
		Expect(string(src)).NotTo(ContainSubstring("time.After"))
		Expect(regexp.MustCompile(`Interval\s*=`).MatchString(string(src))).To(BeFalse(),
			"the signal path must not introduce a second interval constant")
	})
})

var _ = Describe("EveryDeliveryArmCallsSameHelper", func() {
	It("reaches the shared helper from every DeliveryOK site", func() {
		recorder := &recordingSignaller{emits: true}
		driveAllFourArms(recorder)
		Expect(recorder.calls).To(HaveLen(4))
	})

	It("keeps the ladder and its vocabulary in exactly one file", func() {
		src, err := os.ReadFile("answered-signal.go")
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(src), "func ClassifyAnswerAttribution(")).To(Equal(1))
		Expect(strings.Count(string(src), "func RenderedAnswerSignal(")).To(Equal(1))
		Expect(strings.Count(string(src), `"ANSWERED"`)).To(Equal(1),
			"the rendered vocabulary must be mapped exactly once")
	})
})

var _ = Describe("AnsweredSignalRecorder", func() {
	It("writes ANSWERED item-1 for an attributed verdict", func() {
		var buffer bytes.Buffer
		recorder := pkg.NewAnsweredSignalRecorder(&buffer)
		recorder.RecordAnswered(
			context.Background(),
			pkg.ItemID("item-1"),
			pkg.AttributionVerdictAttributed,
		)
		Expect(buffer.String()).To(Equal("ANSWERED item-1\n"))
	})

	It("discards on a nil writer instead of panicking", func() {
		recorder := pkg.NewAnsweredSignalRecorder(nil)
		Expect(func() {
			recorder.RecordAnswered(
				context.Background(),
				pkg.ItemID("item-1"),
				pkg.AttributionVerdictAttributed,
			)
		}).NotTo(Panic())
	})
})
