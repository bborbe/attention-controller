// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// managerGateTestStatus returns a pointer to a registry status word.
func managerGateTestStatus(value string) *string {
	return &value
}

// managerGateTestState builds one tracked session's reading.
func managerGateTestState(
	session string,
	label string,
	decision ManagerGateDecision,
	reason string,
	detail string,
) ManagerGateState {
	return ManagerGateState{
		SessionID: session,
		Label:     label,
		Decision:  decision,
		Reason:    reason,
		Detail:    detail,
	}
}

// managerGateTestStable builds a stability set from session ids.
func managerGateTestStable(sessions ...string) map[string]struct{} {
	stable := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		stable[session] = struct{}{}
	}
	return stable
}

// managerGateTestRecordsOfKind filters a record slice by kind.
func managerGateTestRecordsOfKind(
	records []ManagerGateRecord,
	kind ManagerGateRecordKind,
) []ManagerGateRecord {
	matched := make([]ManagerGateRecord, 0, len(records))
	for _, record := range records {
		if record.Kind == kind {
			matched = append(matched, record)
		}
	}
	return matched
}

// managerGateTestCase is one row of the union expressed as a verdict input.
type managerGateTestCase struct {
	row      string
	input    managerGateInput
	decision ManagerGateDecision
	reason   string
}

// managerGateTestCases covers every row of the ten-row union.
var managerGateTestCases = []managerGateTestCase{
	{
		"row 1 unregistered",
		managerGateInput{CloserKnown: true, CloserBody: "approve: run it"},
		ManagerGateHeld,
		"unregistered",
	},
	{
		"row 2 stuck headless",
		managerGateInput{HeadlessLive: true, Stuck: "stream-closed"},
		ManagerGateGated,
		"stuck:stream-closed",
	},
	{
		"row 2 stuck registered",
		managerGateInput{Status: managerGateTestStatus("idle"), Stuck: "permission-failures"},
		ManagerGateGated,
		"stuck-tab:permission-failures",
	},
	{
		"row 3 closer unknown headless",
		managerGateInput{HeadlessLive: true},
		ManagerGateHeld,
		"closer-unknown",
	},
	{
		"row 4 headless ask",
		managerGateInput{HeadlessLive: true, CloserKnown: true, CloserBody: "approve: run it"},
		ManagerGateGated,
		"headless+closer",
	},
	{
		"row 5 headless no ask",
		managerGateInput{HeadlessLive: true, CloserKnown: true, CloserBody: "nothing"},
		ManagerGateCleared,
		"headless:no-ask",
	},
	{
		"row 6 waiting",
		managerGateInput{Status: managerGateTestStatus("waiting")},
		ManagerGateGated,
		"registry:waiting",
	},
	{
		"row 7 parked modal",
		managerGateInput{
			Status:      managerGateTestStatus("busy"),
			PendingTool: "AskUserQuestion",
			Frozen:      true,
		},
		ManagerGateGated,
		"busy+parked-modal",
	},
	{
		"row 8 closer unknown registered",
		managerGateInput{
			Status:      managerGateTestStatus("busy"),
			PendingTool: "Bash",
			Frozen:      true,
		},
		ManagerGateHeld,
		"closer-unknown",
	},
	{
		"row 9a idle ask",
		managerGateInput{
			Status:      managerGateTestStatus("idle"),
			CloserKnown: true,
			CloserBody:  "pick — 1. X",
		},
		ManagerGateGated,
		"idle+closer",
	},
	{
		"row 9b idle no ask",
		managerGateInput{
			Status:      managerGateTestStatus("idle"),
			CloserKnown: true,
			CloserBody:  "nothing",
		},
		ManagerGateCleared,
		"idle:no-ask",
	},
	{
		"row 10 busy",
		managerGateInput{
			Status:      managerGateTestStatus("busy"),
			PendingTool: "Bash",
			CloserKnown: true,
			CloserBody:  "nothing",
		},
		ManagerGateHeld,
		"held:busy",
	},
	{
		"row 10 shell",
		managerGateInput{
			Status:      managerGateTestStatus("shell"),
			CloserKnown: true,
			CloserBody:  "nothing",
		},
		ManagerGateHeld,
		"held:shell",
	},
	{
		"row 5 open item",
		managerGateInput{
			HeadlessLive: true,
			CloserKnown:  true,
			CloserBody:   "done",
			SessionID:    "s1",
			OpenItems:    map[string]struct{}{"s1": {}},
		},
		ManagerGateGated,
		"store:open-item",
	},
	{
		"row 9 open item",
		managerGateInput{
			Status:      managerGateTestStatus("idle"),
			CloserKnown: true,
			CloserBody:  "done",
			SessionID:   "s1",
			OpenItems:   map[string]struct{}{"s1": {}},
		},
		ManagerGateGated,
		"store:open-item",
	},
}

// expectOpenItemOutranksNoAsk proves an open store item beats the two no-ask
// rows, and that a nil set leaves them exactly as they were.
func expectOpenItemOutranksNoAsk() {
	open := map[string]struct{}{"s1": {}}

	By("gating a headless session that holds an open item")
	decision, reason := managerGateVerdict(managerGateInput{
		HeadlessLive: true,
		CloserKnown:  true,
		CloserBody:   "done",
		SessionID:    "s1",
		OpenItems:    open,
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("store:open-item"))

	By("gating an idle session that holds an open item")
	decision, reason = managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("idle"),
		CloserKnown: true,
		CloserBody:  "done",
		SessionID:   "s1",
		OpenItems:   open,
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("store:open-item"))

	By("leaving both rows cleared when the store was not read")
	decision, reason = managerGateVerdict(managerGateInput{
		HeadlessLive: true,
		CloserKnown:  true,
		CloserBody:   "done",
		SessionID:    "s1",
	})
	Expect(decision).To(Equal(ManagerGateCleared))
	Expect(reason).To(Equal("headless:no-ask"))
	decision, reason = managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("idle"),
		CloserKnown: true,
		CloserBody:  "done",
		SessionID:   "s1",
	})
	Expect(decision).To(Equal(ManagerGateCleared))
	Expect(reason).To(Equal("idle:no-ask"))

	By("keeping the registry gate's own reason")
	decision, reason = managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("waiting"),
		CloserKnown: true,
		CloserBody:  "done",
		SessionID:   "s1",
		OpenItems:   open,
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("registry:waiting"))

	By("keeping the closer ask's own reason")
	decision, reason = managerGateVerdict(managerGateInput{
		HeadlessLive: true,
		CloserKnown:  true,
		CloserBody:   "approve: run it",
		SessionID:    "s1",
		OpenItems:    open,
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("headless+closer"))
	decision, reason = managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("idle"),
		CloserKnown: true,
		CloserBody:  "approve: run it",
		SessionID:   "s1",
		OpenItems:   open,
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("idle+closer"))
}

// expectGateTableMirrorsRows pins the code's own table, row for row, so a
// reordered or edited table is a failing spec rather than a silent drift.
func expectGateTableMirrorsRows() {
	Expect(ManagerGateRows).To(Equal([]ManagerGateRow{
		{"status is None and not headless_live", "held", "unregistered"},
		{"stuck truthy", "gated", "stuck:<cause> or stuck-tab:<cause>"},
		{"status is None and closer unknown", "held", "closer-unknown"},
		{"status is None and closer is an ask", "gated", "headless+closer"},
		{
			"status is None and closer is not an ask",
			"gated / cleared",
			"store:open-item / headless:no-ask",
		},
		{`status == "waiting"`, "gated", "registry:waiting"},
		{
			`status == "busy" and pending == "AskUserQuestion" and frozen`,
			"gated",
			"busy+parked-modal",
		},
		{"closer unknown", "held", "closer-unknown"},
		{`status == "idle"`, "gated / cleared", "idle+closer / store:open-item / idle:no-ask"},
		{"anything else (busy, shell)", "held", "held:<status>"},
	}))
}

// expectGateUnionCases asserts the verdict and the exact reason for every row.
func expectGateUnionCases() {
	for _, testCase := range managerGateTestCases {
		decision, reason := managerGateVerdict(testCase.input)
		Expect(decision).To(Equal(testCase.decision), "%s: decision", testCase.row)
		Expect(reason).To(Equal(testCase.reason), "%s: reason", testCase.row)
	}
}

// expectGateUnionNegativeHalves asserts the two orderings the rows depend on.
func expectGateUnionNegativeHalves() {
	By("never reading a live headless session as unregistered")
	decision, reason := managerGateVerdict(managerGateInput{
		HeadlessLive: true,
		CloserKnown:  true,
		CloserBody:   "approve: run it",
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("headless+closer"))
	Expect(reason).NotTo(Equal("unregistered"))

	By("settling a dead session ahead of its stale stuck marker")
	decision, reason = managerGateVerdict(managerGateInput{Stuck: "stream-closed"})
	Expect(decision).To(Equal(ManagerGateHeld))
	Expect(reason).To(Equal("unregistered"))
	Expect(reason).NotTo(Equal("stuck:stream-closed"))
}

// expectClearedFromBothFalseSites asserts both clear sites and the negative.
func expectClearedFromBothFalseSites() {
	By("clearing an idle session whose ask is gone")
	decision, reason := managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("idle"),
		CloserKnown: true,
		CloserBody:  "nothing",
	})
	Expect(decision).To(Equal(ManagerGateCleared))
	Expect(reason).To(Equal("idle:no-ask"))
	records, next := managerTransitions(
		map[string]string{"idle": "idle+closer :: approve: run it"},
		ManagerGateStates{
			managerGateTestState("idle", "worker", ManagerGateCleared, reason, ""),
		},
		managerGateTestStable("idle"),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordCleared)).To(HaveLen(1))
	Expect(next).NotTo(HaveKey("idle"))

	By("clearing a live headless session that answered its gate")
	decision, reason = managerGateVerdict(managerGateInput{
		HeadlessLive: true,
		CloserKnown:  true,
		CloserBody:   "nothing",
	})
	Expect(decision).To(Equal(ManagerGateCleared))
	Expect(reason).To(Equal("headless:no-ask"))
	records, next = managerTransitions(
		map[string]string{"headless": "headless+closer :: approve: run it"},
		ManagerGateStates{
			managerGateTestState("headless", "worker", ManagerGateCleared, reason, ""),
		},
		managerGateTestStable("headless"),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordCleared)).To(HaveLen(1))
	Expect(next).NotTo(HaveKey("headless"))

	By("emitting no clear for a waiting session")
	decision, reason = managerGateVerdict(managerGateInput{
		Status: managerGateTestStatus("waiting"),
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("registry:waiting"))
	records, next = managerTransitions(
		map[string]string{"waiting": "registry:waiting :: "},
		ManagerGateStates{
			managerGateTestState("waiting", "worker", ManagerGateGated, reason, ""),
		},
		managerGateTestStable("waiting"),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordCleared)).To(BeEmpty())
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKey("waiting"))
}

// expectHeldCausesAreDistinct asserts the four held causes keep their
// behaviours.
func expectHeldCausesAreDistinct() {
	By("dropping and announcing an unregistered session")
	records, next := managerTransitions(
		map[string]string{"dead": "idle+closer :: approve: run it"},
		ManagerGateStates{
			managerGateTestState("dead", "worker", ManagerGateHeld, "unregistered", "unregistered"),
		},
		managerGateTestStable("dead"),
	)
	Expect(records).To(HaveLen(1))
	Expect(records[0].Kind).To(Equal(ManagerGateRecordHeld))
	Expect(records[0].Detail).To(Equal("unregistered; no CLEARED emitted"))
	Expect(next).NotTo(HaveKey("dead"))

	By("dropping a lost-sight session with the unannounced detail")
	records, next = managerTransitions(
		map[string]string{"gone": "idle+closer :: approve: run it"},
		nil,
		managerGateTestStable("gone"),
	)
	Expect(records).To(HaveLen(1))
	Expect(records[0].Kind).To(Equal(ManagerGateRecordHeld))
	Expect(records[0].Detail).To(Equal(managerUntrackedDetail))
	Expect(next).NotTo(HaveKey("gone"))

	By("emitting nothing at all for a mid-turn hold and keeping membership")
	records, next = managerTransitions(
		map[string]string{"busy": "idle+closer :: approve: run it"},
		ManagerGateStates{
			managerGateTestState("busy", "worker", ManagerGateHeld, "held:busy", ""),
		},
		managerGateTestStable("busy"),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKeyWithValue("busy", "idle+closer :: approve: run it"))

	By("emitting nothing at all for a failed closer read and keeping membership")
	records, next = managerTransitions(
		map[string]string{"unknown": "idle+closer :: approve: run it"},
		ManagerGateStates{
			managerGateTestState("unknown", "worker", ManagerGateHeld, "closer-unknown", ""),
		},
		managerGateTestStable("unknown"),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKeyWithValue("unknown", "idle+closer :: approve: run it"))

	By("naming the two membership-preserving reasons and not unregistered")
	Expect(managerHoldKeepsMembership("held:busy")).To(BeTrue())
	Expect(managerHoldKeepsMembership("held:shell")).To(BeTrue())
	Expect(managerHoldKeepsMembership("closer-unknown")).To(BeTrue())
	Expect(managerHoldKeepsMembership("unregistered")).To(BeFalse())
}

// expectBusyParkedModalIsGated asserts the third limb and its negative halves.
func expectBusyParkedModalIsGated() {
	By("gating a busy session parked on a modal with a frozen transcript")
	decision, reason := managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("busy"),
		PendingTool: "AskUserQuestion",
		Frozen:      true,
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("busy+parked-modal"))

	By("not gating a busy session whose pending call is a Bash")
	decision, reason = managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("busy"),
		PendingTool: "Bash",
		Frozen:      true,
		CloserKnown: true,
		CloserBody:  "nothing",
	})
	Expect(decision).NotTo(Equal(ManagerGateGated))
	Expect(reason).To(Equal("held:busy"))

	By("not demoting the park to held when the closer is unreadable")
	decision, reason = managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("busy"),
		PendingTool: "AskUserQuestion",
		Frozen:      true,
	})
	Expect(decision).To(Equal(ManagerGateGated))
	Expect(reason).To(Equal("busy+parked-modal"))

	By("not gating a busy modal whose transcript is still moving")
	decision, reason = managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("busy"),
		PendingTool: "AskUserQuestion",
		Frozen:      false,
		CloserKnown: true,
		CloserBody:  "nothing",
	})
	Expect(decision).To(Equal(ManagerGateHeld))
	Expect(reason).To(Equal("held:busy"))

	By("refusing a freeze an unreadable mtime would have manufactured")
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	Expect(managerTranscriptFrozen(time.Time{}, false, now, managerLiveWindow)).To(BeFalse())
	Expect(managerTranscriptFrozen(now.Add(-managerLiveWindow), true, now, managerLiveWindow)).
		To(BeTrue())
	Expect(managerTranscriptFrozen(
		now.Add(-managerLiveWindow+time.Second), true, now, managerLiveWindow,
	)).To(BeFalse())
}

// expectCloserAskIsNormalized asserts the ask test runs on the normalized body.
func expectCloserAskIsNormalized() {
	By("reading an entity-padded nothing panel as no ask")
	body, found := managerCloserBody("👤 You: &nbsp;&nbsp;&nbsp;&nbsp;nothing")
	Expect(found).To(BeTrue())
	Expect(managerIsAsk(body)).To(BeFalse())
	decision, reason := managerGateVerdict(managerGateInput{
		Status:      managerGateTestStatus("idle"),
		CloserKnown: true,
		CloserBody:  body,
	})
	Expect(decision).To(Equal(ManagerGateCleared))
	Expect(reason).To(Equal("idle:no-ask"))

	// ⚠️ The entity strip is only load-bearing when an entity HIDES a verb.
	// `&nbsp;nothing` passes with or without the strip (neither spelling opens
	// with an ask verb), so on its own it let the guard be deleted with the
	// suite still green. An entity-padded `pick` flips: stripped it reads as an
	// ask, unstripped the body opens with `&` and reads as none.
	By("reading an entity-padded ask verb as an ask, so the entity strip is load-bearing")
	entityAskBody, entityAskFound := managerCloserBody("👤 You: &nbsp;pick — 1. X")
	Expect(entityAskFound).To(BeTrue())
	Expect(managerIsAsk(entityAskBody)).To(BeTrue())

	By("reading a later (on …) wait as no ask")
	Expect(managerIsAsk("later (on PR #12 merge): nothing to do")).To(BeFalse())

	By("still reading the four ask verbs as asks, so normalization is not refuse-all")
	Expect(managerIsAsk("approve: run it")).To(BeTrue())
	Expect(managerIsAsk("pick — 1. X")).To(BeTrue())
	Expect(managerIsAsk("review: check the diff")).To(BeTrue())
	Expect(managerIsAsk("you run: make deploy")).To(BeTrue())

	By("reading a verb outside the four as no ask, so an unseen verb cannot gate")
	Expect(managerIsAsk("ack")).To(BeFalse())
}

// expectChurnSuppressed asserts a flapping session emits nothing until two
// consecutive polls agree.
func expectChurnSuppressed() {
	By("emitting neither NEW GATE nor CLEARED while the set churns")
	records, next := managerTransitions(
		map[string]string{},
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateGated, "idle+closer", "approve: x"),
		},
		managerStableSessions([]string{}, []string{}, []string{"A"}),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(BeEmpty())

	records, next = managerTransitions(
		map[string]string{},
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateCleared, "idle:no-ask", ""),
		},
		managerStableSessions([]string{}, []string{"A"}, []string{}),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(BeEmpty())

	records, next = managerTransitions(
		map[string]string{},
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateGated, "idle+closer", "approve: x"),
		},
		managerStableSessions([]string{}, []string{}, []string{"A"}),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(BeEmpty())

	By("raising NEW GATE once two consecutive polls agree the session is gated")
	records, next = managerTransitions(
		map[string]string{},
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateGated, "idle+closer", "approve: x"),
		},
		managerStableSessions([]string{}, []string{"A"}, []string{"A"}),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordNewGate)).To(HaveLen(1))
	Expect(next).To(HaveKey("A"))

	By("emitting no CLEARED on the poll that only starts the absence")
	records, next = managerTransitions(
		next,
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateCleared, "idle:no-ask", ""),
		},
		managerStableSessions([]string{"A"}, []string{"A"}, []string{}),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKey("A"))

	By("emitting the CLEARED once the absence is two polls old")
	records, next = managerTransitions(
		next,
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateCleared, "idle:no-ask", ""),
		},
		managerStableSessions([]string{"A"}, []string{}, []string{}),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordCleared)).To(HaveLen(1))
	Expect(next).To(BeEmpty())
}

// expectDeferredCleared proves a session answered on a poll clears on the next.
func expectDeferredCleared() {
	By("deferring a CLEARED for a session answered on this poll")
	records, next := managerTransitions(
		map[string]string{"S": "idle+closer :: approve: run it"},
		ManagerGateStates{
			managerGateTestState("S", "worker", ManagerGateCleared, "idle:no-ask", ""),
		},
		managerStableSessions([]string{"S"}, []string{"S"}, []string{}),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKeyWithValue("S", "idle+closer :: approve: run it"))

	By("emitting exactly one CLEARED on the next pass, when it is stable")
	records, next = managerTransitions(
		next,
		ManagerGateStates{
			managerGateTestState("S", "worker", ManagerGateCleared, "idle:no-ask", ""),
		},
		managerStableSessions([]string{"S"}, []string{}, []string{}),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordCleared)).To(HaveLen(1))
	Expect(next).To(BeEmpty())
}

// expectDeferredLostSight proves the lost-sight HELD defers the same way.
func expectDeferredLostSight() {
	By("deferring a lost-sight HELD while the absence is one poll old")
	records, next := managerTransitions(
		map[string]string{"L": "idle+closer :: approve: run it"},
		nil,
		managerStableSessions([]string{"L"}, []string{"L"}, []string{}),
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKey("L"))

	By("emitting it once the absence is two polls old")
	records, next = managerTransitions(
		next,
		nil,
		managerStableSessions([]string{"L"}, []string{}, []string{}),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordHeld)).To(HaveLen(1))
	Expect(records[0].Detail).To(Equal(managerUntrackedDetail))
	Expect(next).To(BeEmpty())
}

// expectStabilityGateIsPerSession asserts a churning peer cannot withhold a
// stable session's transition.
func expectStabilityGateIsPerSession() {
	By("holding A stable while B churns in")
	stable := managerStableSessions(
		[]string{"A"}, []string{"A"}, []string{"A", "B"},
	)
	Expect(stable).To(HaveKey("A"))
	Expect(stable).NotTo(HaveKey("B"))
	records, next := managerTransitions(
		map[string]string{"A": "idle+closer :: approve: x"},
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateGated, "idle+closer", "approve: x"),
			managerGateTestState("B", "worker", ManagerGateGated, "idle+closer", "approve: y"),
		},
		stable,
	)
	Expect(records).To(BeEmpty())
	Expect(next).To(HaveKey("A"))
	Expect(next).NotTo(HaveKey("B"))

	By("deferring A's CLEARED on the poll it is answered")
	records, next = managerTransitions(
		next,
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateCleared, "idle:no-ask", ""),
			managerGateTestState("B", "worker", ManagerGateGated, "idle+closer", "approve: y"),
		},
		managerStableSessions([]string{"A"}, []string{"A", "B"}, []string{"B"}),
	)
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordCleared)).To(BeEmpty())
	Expect(managerGateTestRecordsOfKind(records, ManagerGateRecordNewGate)).To(HaveLen(1))
	Expect(records[0].Session).To(Equal("B"))
	Expect(next).To(HaveKey("A"))

	By("emitting A's CLEARED while B is still churning")
	stable = managerStableSessions([]string{"A", "B"}, []string{"B"}, []string{})
	Expect(stable).To(HaveKey("A"))
	Expect(stable).NotTo(HaveKey("B"))
	records, next = managerTransitions(
		next,
		ManagerGateStates{
			managerGateTestState("A", "worker", ManagerGateCleared, "idle:no-ask", ""),
		},
		stable,
	)
	cleared := managerGateTestRecordsOfKind(records, ManagerGateRecordCleared)
	Expect(cleared).To(HaveLen(1))
	Expect(cleared[0].Session).To(Equal("A"))
	Expect(next).NotTo(HaveKey("A"))
	Expect(next).To(HaveKey("B"))
}

// expectRegistryStatusParses asserts the status field parses out of the registry.
func expectRegistryStatusParses() {
	By("parsing a registry entry carrying a status")
	dir := GinkgoT().TempDir()
	Expect(os.WriteFile(
		filepath.Join(dir, "1.json"),
		[]byte(`{"sessionId":"abc","name":"claude-1","nameSource":"user","status":"waiting"}`),
		0o600,
	)).To(Succeed())
	statuses, err := readSessionStatuses(context.Background(), dir)
	Expect(err).NotTo(HaveOccurred())
	Expect(statuses).To(HaveKeyWithValue("abc", "waiting"))

	By("skipping an entry that carries no status")
	Expect(os.WriteFile(
		filepath.Join(dir, "2.json"),
		[]byte(`{"sessionId":"def","name":"claude-2","nameSource":"user"}`),
		0o600,
	)).To(Succeed())
	statuses, err = readSessionStatuses(context.Background(), dir)
	Expect(err).NotTo(HaveOccurred())
	Expect(statuses).To(HaveKeyWithValue("abc", "waiting"))
	Expect(statuses).NotTo(HaveKey("def"))

	By("reporting an unreadable registry directory as an error")
	_, err = readSessionStatuses(
		context.Background(),
		filepath.Join(dir, "does-not-exist"),
	)
	Expect(err).To(HaveOccurred())
}

var _ = Describe("manager gate computation", func() {
	It("GatedUnionMatchesTable", func() {
		expectGateTableMirrorsRows()
		expectGateUnionCases()
		expectGateUnionNegativeHalves()
	})

	It("ClearedFromBothFalseSites", func() {
		expectClearedFromBothFalseSites()
	})

	It("HeldCausesAreDistinct", func() {
		expectHeldCausesAreDistinct()
	})

	It("BusyParkedModalIsGated", func() {
		expectBusyParkedModalIsGated()
	})

	It("CloserAskIsNormalized", func() {
		expectCloserAskIsNormalized()
	})

	It("StabilityGateSuppressesChurn", func() {
		expectChurnSuppressed()
		expectDeferredCleared()
		expectDeferredLostSight()
	})

	It("StabilityGateIsPerSession", func() {
		expectStabilityGateIsPerSession()
	})

	It("RegistryStatusParses", func() {
		expectRegistryStatusParses()
	})
})

var _ = Describe("OpenItemOutranksNoAsk", func() {
	It("OpenItemOutranksNoAsk", func() {
		expectOpenItemOutranksNoAsk()
	})
})
