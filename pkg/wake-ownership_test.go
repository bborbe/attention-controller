// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"os"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// wakeSubjectFixture is the shared fixture's shape. The Python test in
// claude-supervisor reads the SAME file, so a change to the rule that one side
// does not carry fails that side's test.
type wakeSubjectFixture struct {
	Cases []wakeSubjectCase `json:"cases"`
}

type wakeSubjectCase struct {
	Name     string          `json:"name"`
	Manager  wakeSubjectFact `json:"manager"`
	Producer wakeSubjectFact `json:"producer"`
	Keep     bool            `json:"keep"`
}

type wakeSubjectFact struct {
	Subject  string `json:"subject"`
	Fleet    bool   `json:"fleet"`
	Resolved bool   `json:"resolved"`
}

// fakeWakeSubjectResolver is the test's resolver: a map, no filesystem.
type fakeWakeSubjectResolver map[string]pkg.WakeSubject

func (r fakeWakeSubjectResolver) Resolve(_ context.Context, sessionID string) pkg.WakeSubject {
	return r[sessionID]
}

var _ = Describe("wake subject ownership", func() {
	var (
		store      *mocks.AttentionStore
		ledger     *mocks.SessionLedgerStore
		provenance *mocks.ProvenanceResolver
		notifier   *mocks.AttentionChangeNotifier
		carrier    *wakeRecordingCarrier
		sink       *recordingEmissionSink
		live       map[string]bool
		now        stdtime.Time
		clock      libtime.CurrentDateTimeGetter
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		ledger = &mocks.SessionLedgerStore{}
		provenance = &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})
		notifier = &mocks.AttentionChangeNotifier{}
		notifier.SubscribeReturns(make(chan struct{}), func() {})
		carrier = newWakeRecordingCarrier()
		sink = &recordingEmissionSink{}
		live = map[string]bool{}
		store.IsSessionLiveCalls(func(_ context.Context, sessionID string) bool {
			return live[sessionID]
		})
		now = stdtime.Date(2026, 11, 21, 12, 0, 0, 0, stdtime.UTC)
		clock = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
	})

	It("TestSharedFixture — the Go rule matches the fixture the Python arms also read", func() {
		raw, err := os.ReadFile("../testdata/wake-subject-predicate.json")
		Expect(err).To(BeNil())
		var fixture wakeSubjectFixture
		Expect(json.Unmarshal(raw, &fixture)).To(BeNil())
		Expect(len(fixture.Cases)).To(BeNumerically(">=", 6))
		for _, testCase := range fixture.Cases {
			ownership := pkg.WakeOwnership{
				Manager: pkg.WakeSubject{
					Subject:  testCase.Manager.Subject,
					Fleet:    testCase.Manager.Fleet,
					Resolved: testCase.Manager.Resolved,
				},
				Producer: pkg.WakeSubject{
					Subject:  testCase.Producer.Subject,
					Fleet:    testCase.Producer.Fleet,
					Resolved: testCase.Producer.Resolved,
				},
			}
			Expect(ownership.Keeps()).To(Equal(testCase.Keep), testCase.Name)
		}
	})

	It(
		"TestForeignSubjectNotTyped — a card whose subject is another goal is not typed to this manager",
		func() {
			live["mgr-1"] = true
			live["w-foreign"] = true
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRow("mgr-1", "manager", ""),
				ledgerRow("w-foreign", "agent", "someone-else"),
			}, nil)
			store.ReadReturns(pkg.Items{wakeItem("item-1", "w-foreign")}, nil)
			subjects := fakeWakeSubjectResolver{
				"mgr-1":     {Subject: "Goal A", Resolved: true},
				"w-foreign": {Subject: "Goal B", Resolved: true},
			}
			job := pkg.NewManagerWakeJob(
				store, ledger, provenance, carrier, notifier, clock, sink,
				pkg.WithManagerWakeSubjectResolver(subjects),
			)

			// The first pass buffers the entering card; the window closes a pass later.
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			now = now.Add(pkg.ManagerWakeBatchWindow)
			Expect(job.WakeOnce(context.Background())).To(Succeed())

			// The card's subject is another goal, so the manager is never woken for it.
			Expect(carrier.recorded()).To(BeEmpty())
		},
	)

	It("TestOwnSubjectTyped — a card whose subject is this manager's IS typed", func() {
		live["mgr-1"] = true
		live["w-own"] = true
		ledger.ListReturns(pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("w-own", "agent", "someone-else"),
		}, nil)
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-own")}, nil)
		subjects := fakeWakeSubjectResolver{
			"mgr-1": {Subject: "Goal A", Resolved: true},
			"w-own": {Subject: "Goal A", Resolved: true},
		}
		job := pkg.NewManagerWakeJob(
			store, ledger, provenance, carrier, notifier, clock, sink,
			pkg.WithManagerWakeSubjectResolver(subjects),
		)

		Expect(job.WakeOnce(context.Background())).To(Succeed())
		now = now.Add(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())

		Expect(carrier.recorded()).To(HaveLen(1))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-1"))
	})
})
