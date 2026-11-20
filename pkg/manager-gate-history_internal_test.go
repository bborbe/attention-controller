// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// managerGateHistoryStart is the fixed instant every spec's clock starts at, so
// a threshold is crossed by arithmetic rather than by sleeping.
var managerGateHistoryStart = libtime.DateTime(
	time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
)

// managerGateHistoryClock returns a clock pinned at managerGateHistoryStart.
func managerGateHistoryClock() libtime.CurrentDateTime {
	clock := libtime.NewCurrentDateTime()
	clock.SetNow(managerGateHistoryStart)
	return clock
}

// managerGateHistoryAt advances the clock by the given duration.
func managerGateHistoryAt(clock libtime.CurrentDateTime, offset time.Duration) {
	clock.SetNow(managerGateHistoryStart.Add(libtime.Duration(offset)))
}

// managerGateHistoryInput builds one pass's input for a manager.
func managerGateHistoryInput(
	manager string,
	states ManagerGateStates,
) ManagerGateObserveInput {
	return ManagerGateObserveInput{
		Manager: manager,
		States:  states,
		Open: &ManagerOpenView{
			Producers: map[string]struct{}{},
			DedupKeys: map[string]struct{}{},
		},
		Answered: &ManagerAnswerView{BySession: map[string]ManagerParkAnswer{}},
	}
}

// managerGateHistoryParked builds a gated state at a park reason.
func managerGateHistoryParked(session, reason string) ManagerGateState {
	return managerGateTestState(
		session,
		"worker",
		ManagerGateGated,
		reason,
		"waiting on the operator",
	)
}

// managerGateHistoryRepostInput is the base re-post fixture: one parked worker,
// no open card, no answer.
func managerGateHistoryRepostInput() managerRepostInput {
	return managerRepostInput{
		Parked: []string{"abcdef12"},
		Entries: map[string]managerTrackedEntry{
			"abcdef12": {FullID: "abcdef12-3456-4789-8abc-def012345678", Label: "worker"},
		},
		Open: &ManagerOpenView{
			Producers: map[string]struct{}{},
			DedupKeys: map[string]struct{}{},
		},
		Answered:  &ManagerAnswerView{BySession: map[string]ManagerParkAnswer{}},
		ParkSince: map[string]libtime.DateTime{},
		Reasons:   map[string]string{"abcdef12": "idle+closer"},
	}
}

// expectNewGateIsContentKeyed proves NEW GATE is keyed on reason :: detail.
func expectNewGateIsContentKeyed() {
	By("hashing content, never the session alone")
	Expect(managerContentKey("idle+closer", "approve: run it")).To(HaveLen(16))
	Expect(managerContentKey("idle+closer", "approve: run it")).
		NotTo(Equal(managerContentKey("idle+closer", "pick — 1. X")))

	By("re-emitting when the body changes under a stable reason")
	records, next := managerTransitions(
		map[string]string{"S": managerContentKey("idle+closer", "approve: run it")},
		ManagerGateStates{
			managerGateTestState("S", "worker", ManagerGateGated, "idle+closer", "pick — 1. X"),
		},
		managerGateTestStable("S"),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordNewGate)).To(HaveLen(1))
	Expect(next).To(HaveKeyWithValue("S", managerContentKey("idle+closer", "pick — 1. X")))

	By("emitting nothing when the content is unchanged")
	records, next = managerTransitions(
		map[string]string{"S": managerContentKey("idle+closer", "approve: run it")},
		ManagerGateStates{
			managerGateTestState("S", "worker", ManagerGateGated, "idle+closer", "approve: run it"),
		},
		managerGateTestStable("S"),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKeyWithValue("S", managerContentKey("idle+closer", "approve: run it")))

	By("committing the key for a legacy membership-only entry without re-emitting")
	records, next = managerTransitions(
		map[string]string{"S": ""},
		ManagerGateStates{
			managerGateTestState("S", "worker", ManagerGateGated, "idle+closer", "approve: run it"),
		},
		managerGateTestStable("S"),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKeyWithValue("S", managerContentKey("idle+closer", "approve: run it")))
}

// expectAgedEscalatesOncePerThreshold proves each threshold fires exactly once
// per continuous park.
func expectAgedEscalatesOncePerThreshold() {
	clock := managerGateHistoryClock()
	tracker := NewManagerGateTracker(GinkgoT().TempDir(), clock)
	states := ManagerGateStates{managerGateHistoryParked("session-15m", "registry:waiting")}
	ctx := context.Background()

	By("starting the clock without escalating")
	outcome, err := tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)).To(BeEmpty())

	By("escalating once when the park crosses 15 minutes")
	managerGateHistoryAt(clock, 15*time.Minute)
	outcome, err = tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
	Expect(err).NotTo(HaveOccurred())
	aged := managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)
	Expect(aged).To(HaveLen(1))
	Expect(aged[0].Detail).To(Equal("15 minutes"))
	Expect(aged[0].Session).To(Equal("session-15m"))

	By("not repeating the 15-minute escalation on a later pass")
	managerGateHistoryAt(clock, 20*time.Minute)
	outcome, err = tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)).To(BeEmpty())

	By("escalating once more when the park crosses 60 minutes")
	managerGateHistoryAt(clock, 60*time.Minute)
	outcome, err = tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
	Expect(err).NotTo(HaveOccurred())
	aged = managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)
	Expect(aged).To(HaveLen(1))
	Expect(aged[0].Detail).To(Equal("60 minutes"))

	By("not repeating the 60-minute escalation either")
	managerGateHistoryAt(clock, 90*time.Minute)
	outcome, err = tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)).To(BeEmpty())
}

// expectAgedOnlyForParkedReasons proves the closed set: only ManagerParkedReasons
// age, and every member of it does.
func expectAgedOnlyForParkedReasons() {
	Expect(ManagerParkedReasons).To(ConsistOf(
		"registry:waiting", "busy+parked-modal", "headless+closer", "idle+closer",
	))
	ctx := context.Background()
	nonParked := []string{
		"stuck:stream-closed",
		"stuck-tab:permission-failures",
		"stuck:permission-channel",
		"stuck:blocked-closer",
		"closer-unknown",
		"unregistered",
		"held:busy",
	}

	By("emitting zero AGED for every reason outside the closed set")
	for _, reason := range nonParked {
		clock := managerGateHistoryClock()
		tracker := NewManagerGateTracker(GinkgoT().TempDir(), clock)
		states := ManagerGateStates{
			managerGateTestState("session-x", "worker", ManagerGateGated, reason, "held"),
		}
		_, err := tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
		Expect(err).NotTo(HaveOccurred())
		managerGateHistoryAt(clock, 61*time.Minute)
		outcome, err := tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
		Expect(err).NotTo(HaveOccurred())
		Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)).
			To(BeEmpty(), "%s must never age", reason)
	}

	By("still ageing every reason inside the closed set")
	for _, reason := range ManagerParkedReasons {
		clock := managerGateHistoryClock()
		tracker := NewManagerGateTracker(GinkgoT().TempDir(), clock)
		states := ManagerGateStates{managerGateHistoryParked("session-y", reason)}
		_, err := tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
		Expect(err).NotTo(HaveOccurred())
		managerGateHistoryAt(clock, 61*time.Minute)
		outcome, err := tracker.Observe(ctx, managerGateHistoryInput("mgr", states))
		Expect(err).NotTo(HaveOccurred())
		Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)).
			NotTo(BeEmpty(), "%s must age", reason)
	}
}

// expectRepostOnlyWhenNoCardOpen covers the open-card test and the card shape.
func expectRepostOnlyWhenNoCardOpen() {
	By("re-posting a parked worker with no open card and no answer")
	plan := managerRepostPlan(managerGateHistoryRepostInput())
	Expect(plan).To(HaveLen(1))
	Expect(plan[0].ProducerID).To(Equal("abcdef12-3456-4789-8abc-def012345678"))
	Expect(plan[0].DedupKey).To(Equal(DedupKey("park:abcdef12")))
	Expect(plan[0].Payload).NotTo(BeEmpty())
	Expect(plan[0].AnswerMechanism).To(Equal(MessageAnswerMechanism))
	Expect(plan[0].Options).To(BeEmpty())

	By("skipping a worker whose park dedup key is already open")
	withDedup := managerGateHistoryRepostInput()
	withDedup.Open = &ManagerOpenView{
		Producers: map[string]struct{}{},
		DedupKeys: map[string]struct{}{"park:abcdef12": {}},
	}
	Expect(managerRepostPlan(withDedup)).To(BeEmpty())

	By("skipping a worker whose producer already has an open item")
	withProducer := managerGateHistoryRepostInput()
	withProducer.Open = &ManagerOpenView{
		Producers: map[string]struct{}{"abcdef12-3456-4789-8abc-def012345678": {}},
		DedupKeys: map[string]struct{}{},
	}
	Expect(managerRepostPlan(withProducer)).To(BeEmpty())

	By("posting an ack card with no options for a permission-class park")
	for _, reason := range []string{"registry:waiting", "busy+parked-modal"} {
		permission := managerGateHistoryRepostInput()
		permission.Reasons["abcdef12"] = reason
		plan = managerRepostPlan(permission)
		Expect(plan).To(HaveLen(1))
		Expect(plan[0].AnswerMechanism).To(Equal(AckAnswerMechanism))
		Expect(plan[0].Options).To(HaveLen(0))
	}

	By("posting a message card for a question-class park")
	question := managerGateHistoryRepostInput()
	question.Reasons["abcdef12"] = "idle+closer"
	Expect(managerRepostPlan(question)[0].AnswerMechanism).To(Equal(MessageAnswerMechanism))
}

// expectAnswerPredatingParkDoesNotSuppress proves the direction that keeps the
// answered read from silencing a later park.
func expectAnswerPredatingParkDoesNotSuppress() {
	since := managerGateHistoryStart

	By("suppressing an answer that postdates the park")
	after := managerGateHistoryRepostInput()
	after.ParkSince["abcdef12"] = since
	after.Answered = &ManagerAnswerView{BySession: map[string]ManagerParkAnswer{
		"abcdef12": {ItemID: "item-1", CreatedAt: since.Add(libtime.Duration(time.Minute))},
	}}
	Expect(managerRepostPlan(after)).To(BeEmpty())

	By("re-posting when the answer PREDATES the park — it belongs to a previous one")
	before := managerGateHistoryRepostInput()
	before.ParkSince["abcdef12"] = since
	before.Answered = &ManagerAnswerView{BySession: map[string]ManagerParkAnswer{
		"abcdef12": {ItemID: "item-1", CreatedAt: since.Add(-libtime.Duration(time.Minute))},
	}}
	Expect(managerRepostPlan(before)).To(HaveLen(1))

	By("re-posting when the caller has no park clock at all")
	undated := managerGateHistoryRepostInput()
	undated.Answered = &ManagerAnswerView{BySession: map[string]ManagerParkAnswer{
		"abcdef12": {ItemID: "item-1", CreatedAt: since.Add(libtime.Duration(time.Minute))},
	}}
	Expect(managerRepostPlan(undated)).To(HaveLen(1))
	Expect(managerAnswerCoversCurrentPark(
		ManagerParkAnswer{CreatedAt: since}, nil,
	)).To(BeFalse())
}

// expectStoreReadFailureSuppressesRepost proves nil is never an empty set.
func expectStoreReadFailureSuppressesRepost() {
	By("re-posting nothing when the open-items read failed")
	failedOpen := managerGateHistoryRepostInput()
	failedOpen.Open = nil
	Expect(managerRepostPlan(failedOpen)).To(BeEmpty())

	By("re-posting nothing when the answered read failed")
	failedAnswered := managerGateHistoryRepostInput()
	failedAnswered.Answered = nil
	Expect(managerRepostPlan(failedAnswered)).To(BeEmpty())

	By("re-posting nothing when nothing is parked, without reading the store")
	idle := managerGateHistoryRepostInput()
	idle.Parked = nil
	idle.Open = nil
	Expect(managerRepostPlan(idle)).To(BeEmpty())
}

// expectStatePersistsAcrossRestart proves the previous set, the content keys and
// the park ages survive a restart without a burst.
func expectStatePersistsAcrossRestart() {
	ctx := context.Background()
	dir := GinkgoT().TempDir()
	clock := managerGateHistoryClock()
	manager := "mgr-restart"
	states := ManagerGateStates{
		managerGateTestState(
			"aaaaaaaa-1111-4111-8111-111111111111",
			"one",
			ManagerGateGated,
			"idle+closer",
			"approve: run it",
		),
		managerGateTestState(
			"bbbbbbbb-2222-4222-8222-222222222222",
			"two",
			ManagerGateGated,
			"idle+closer",
			"pick — 1. X",
		),
	}
	expectRestartKeepsState(ctx, dir, clock, manager, states)
	expectRestartReemitsEntered(ctx, dir, clock, manager, states)
	expectRestartKeepsParkAge(ctx, clock)
}

// expectRestartKeepsState proves a restart over a readable state file emits no
// NEW GATE and leaves the file byte-identical.
func expectRestartKeepsState(
	ctx context.Context,
	dir string,
	clock libtime.CurrentDateTime,
	manager string,
	states ManagerGateStates,
) {
	statePath := managerStatePath(dir, manager)
	By("emitting nothing on the first pass and the whole set on the second")
	first := NewManagerGateTracker(dir, clock)
	outcome, err := first.Observe(ctx, managerGateHistoryInput(manager, states))
	Expect(err).NotTo(HaveOccurred())
	Expect(outcome.Records).To(BeEmpty())
	outcome, err = first.Observe(ctx, managerGateHistoryInput(manager, states))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)).To(HaveLen(2))

	loaded, ok := managerLoadPrevState(statePath)
	Expect(ok).To(BeTrue())
	Expect(loaded).To(HaveKey("aaaaaaaa-1111-4111-8111-111111111111"))
	Expect(loaded).To(HaveKey("bbbbbbbb-2222-4222-8222-222222222222"))
	before, err := os.ReadFile(statePath)
	Expect(err).NotTo(HaveOccurred())

	By("emitting no NEW GATE and writing nothing when a restart reads that state")
	restarted := NewManagerGateTracker(dir, clock)
	outcome, err = restarted.Observe(ctx, managerGateHistoryInput(manager, states))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)).To(BeEmpty())
	after, err := os.ReadFile(statePath)
	Expect(err).NotTo(HaveOccurred())
	Expect(after).To(Equal(before))
	outcome, err = restarted.Observe(ctx, managerGateHistoryInput(manager, states))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)).To(BeEmpty())
}

// expectRestartReemitsEntered proves the one entered session re-emits alone, and
// that an absent or corrupt state file re-emits the set exactly once.
func expectRestartReemitsEntered(
	ctx context.Context,
	dir string,
	clock libtime.CurrentDateTime,
	manager string,
	states ManagerGateStates,
) {
	statePath := managerStatePath(dir, manager)
	entered := append(ManagerGateStates{
		managerGateTestState(
			"cccccccc-3333-4333-8333-333333333333",
			"three",
			ManagerGateGated,
			"registry:waiting",
			"waiting",
		),
	}, states...)

	By("re-emitting exactly the session that entered while the process was down")
	down := NewManagerGateTracker(dir, clock)
	outcome, err := down.Observe(ctx, managerGateHistoryInput(manager, entered))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)).To(BeEmpty())
	outcome, err = down.Observe(ctx, managerGateHistoryInput(manager, entered))
	Expect(err).NotTo(HaveOccurred())
	newGates := managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)
	Expect(newGates).To(HaveLen(1))
	Expect(newGates[0].Session).To(Equal("cccccccc-3333-4333-8333-333333333333"))

	By("re-emitting the set once when the state file is removed, then never again")
	Expect(os.Remove(statePath)).To(Succeed())
	absent := NewManagerGateTracker(dir, clock)
	outcome, err = absent.Observe(ctx, managerGateHistoryInput(manager, entered))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)).To(BeEmpty())
	outcome, err = absent.Observe(ctx, managerGateHistoryInput(manager, entered))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)).To(HaveLen(3))
	again := NewManagerGateTracker(dir, clock)
	for range 2 {
		outcome, err = again.Observe(ctx, managerGateHistoryInput(manager, entered))
		Expect(err).NotTo(HaveOccurred())
		Expect(
			managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate),
		).To(BeEmpty())
	}

	By("warning and re-emitting once when the state file is corrupt")
	Expect(os.WriteFile(statePath, []byte("{not json"), 0o600)).To(Succeed())
	corrupt := NewManagerGateTracker(dir, clock)
	outcome, err = corrupt.Observe(ctx, managerGateHistoryInput(manager, entered))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)).To(BeEmpty())
	outcome, err = corrupt.Observe(ctx, managerGateHistoryInput(manager, entered))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordNewGate)).To(HaveLen(3))
}

// expectRestartKeepsParkAge proves a park open across a restart keeps its age.
func expectRestartKeepsParkAge(ctx context.Context, clock libtime.CurrentDateTime) {
	By("keeping a park's age across a restart so its AGED lands at the original age")
	parkDir := GinkgoT().TempDir()
	parkStates := ManagerGateStates{managerGateHistoryParked("parked-session", "registry:waiting")}
	parkFirst := NewManagerGateTracker(parkDir, clock)
	outcome, err := parkFirst.Observe(ctx, managerGateHistoryInput("mgr-park", parkStates))
	Expect(err).NotTo(HaveOccurred())
	Expect(managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)).To(BeEmpty())
	managerGateHistoryAt(clock, 16*time.Minute)
	parkRestarted := NewManagerGateTracker(parkDir, clock)
	outcome, err = parkRestarted.Observe(ctx, managerGateHistoryInput("mgr-park", parkStates))
	Expect(err).NotTo(HaveOccurred())
	aged := managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordAged)
	Expect(aged).To(HaveLen(1))
	Expect(aged[0].Detail).To(Equal("15 minutes"))
}

// expectStateIsScopedPerManager proves two managers never read each other's
// previous set.
func expectStateIsScopedPerManager() {
	ctx := context.Background()
	dir := GinkgoT().TempDir()
	clock := managerGateHistoryClock()
	statesA := ManagerGateStates{
		managerGateTestState(
			"aaaaaaaa-1111-4111-8111-111111111111",
			"one",
			ManagerGateGated,
			"idle+closer",
			"approve: run it",
		),
	}
	statesB := ManagerGateStates{
		managerGateTestState(
			"bbbbbbbb-2222-4222-8222-222222222222",
			"two",
			ManagerGateGated,
			"idle+closer",
			"approve: run it",
		),
	}

	By("scoping the two state paths to distinct files")
	Expect(managerStatePath(dir, "mgr-a")).NotTo(Equal(managerStatePath(dir, "mgr-b")))

	trackerA := NewManagerGateTracker(dir, clock)
	trackerB := NewManagerGateTracker(dir, clock)
	for range 3 {
		outcome, err := trackerA.Observe(ctx, managerGateHistoryInput("mgr-a", statesA))
		Expect(err).NotTo(HaveOccurred())
		Expect(
			managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordCleared),
		).To(BeEmpty())
		outcome, err = trackerB.Observe(ctx, managerGateHistoryInput("mgr-b", statesB))
		Expect(err).NotTo(HaveOccurred())
		Expect(
			managerGateTestRecordsOfKind(outcome.Records, ManagerGateRecordCleared),
		).To(BeEmpty())
		Expect(managerGateHistoryNamesA(outcome.Records)).To(BeFalse())
	}

	By("writing one scope-keyed file per manager")
	for _, manager := range []string{"mgr-a", "mgr-b"} {
		_, err := os.Stat(managerStatePath(dir, manager))
		Expect(err).NotTo(HaveOccurred())
	}
}

// managerGateHistoryNamesA reports whether any record names manager A's session.
func managerGateHistoryNamesA(records []ManagerGateRecord) bool {
	for _, record := range records {
		if record.Session == "aaaaaaaa-1111-4111-8111-111111111111" {
			return true
		}
	}
	return false
}

var _ = Describe("manager gate history", func() {
	It("NewGateIsContentKeyed", func() {
		expectNewGateIsContentKeyed()
	})

	It("AgedEscalatesOncePerThreshold", func() {
		expectAgedEscalatesOncePerThreshold()
	})

	It("AgedOnlyForParkedReasons", func() {
		expectAgedOnlyForParkedReasons()
	})

	It("RepostOnlyWhenNoCardOpen", func() {
		expectRepostOnlyWhenNoCardOpen()
	})

	It("AnswerPredatingParkDoesNotSuppress", func() {
		expectAnswerPredatingParkDoesNotSuppress()
	})

	It("StoreReadFailureSuppressesRepost", func() {
		expectStoreReadFailureSuppressesRepost()
	})

	It("StatePersistsAcrossRestart", func() {
		expectStatePersistsAcrossRestart()
	})

	It("StateIsScopedPerManager", func() {
		expectStateIsScopedPerManager()
	})
})
