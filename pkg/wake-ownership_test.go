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

	It(
		"TestSessionlessCardGoesToTheFleetManagerOnly — an unowned card wakes only a Fleet Manager",
		func() {
			live["mgr-1"] = true
			live["fleet-1"] = true
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRow("mgr-1", "manager", ""),
				ledgerRow("fleet-1", "manager", ""),
			}, nil)
			store.ReadReturns(pkg.Items{sessionlessItem("item-1")}, nil)
			subjects := fakeWakeSubjectResolver{
				"mgr-1":   {Subject: "Goal A", Resolved: true},
				"fleet-1": {Fleet: true, Resolved: true},
			}
			job := pkg.NewManagerWakeJob(
				store, ledger, provenance, carrier, notifier, clock, sink,
				pkg.WithManagerWakeSubjectResolver(subjects),
			)

			Expect(job.WakeOnce(context.Background())).To(Succeed())
			now = now.Add(pkg.ManagerWakeBatchWindow)
			Expect(job.WakeOnce(context.Background())).To(Succeed())

			Expect(carrier.sessions()).To(Equal([]string{"fleet-1"}))
		},
	)

	It(
		"TestOwnWorkerSurvivesAMissingSubjectRecord — an own worker is never silenced by the subject rule",
		func() {
			live["mgr-1"] = true
			live["w-own"] = true
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRow("mgr-1", "manager", ""),
				ledgerRow("w-own", "agent", "mgr-1"),
			}, nil)
			store.ReadReturns(pkg.Items{wakeItem("item-1", "w-own")}, nil)
			// The worker has NO subject record at all — the wrapper-backed case.
			subjects := fakeWakeSubjectResolver{
				"mgr-1": {Subject: "Goal A", Resolved: true},
			}
			job := pkg.NewManagerWakeJob(
				store, ledger, provenance, carrier, notifier, clock, sink,
				pkg.WithManagerWakeSubjectResolver(subjects),
			)

			Expect(job.WakeOnce(context.Background())).To(Succeed())
			now = now.Add(pkg.ManagerWakeBatchWindow)
			Expect(job.WakeOnce(context.Background())).To(Succeed())

			Expect(carrier.recorded()).To(HaveLen(1))
		},
	)
})

var _ = Describe("default wake subject resolver", func() {
	var (
		stateDir   string
		contextDir string
		vaultsRoot string
		resolver   pkg.WakeSubjectResolver
	)

	BeforeEach(func() {
		root := GinkgoT().TempDir()
		stateDir = root + "/state"
		contextDir = root + "/context"
		vaultsRoot = root + "/Obsidian"
		Expect(os.MkdirAll(stateDir+"/worker-manager", 0o755)).To(Succeed())
		Expect(os.MkdirAll(contextDir, 0o755)).To(Succeed())
		Expect(os.MkdirAll(vaultsRoot+"/private-personal/25 Tasks", 0o755)).To(Succeed())
		resolver = pkg.NewDefaultWakeSubjectResolver(stateDir, contextDir, vaultsRoot)
	})

	write := func(path string, body string) {
		Expect(os.WriteFile(path, []byte(body), 0o644)).To(Succeed())
	}

	It("TestWorkerManagerRecord — reads the subject and the fleet branch", func() {
		write(stateDir+"/worker-manager/mgr-1.json", `{"subject":"Goal A","branch":"goal"}`)
		write(stateDir+"/worker-manager/fleet-1.json", `{"subject":"","branch":"fleet"}`)

		Expect(resolver.Resolve(context.Background(), "mgr-1")).To(Equal(
			pkg.WakeSubject{Subject: "Goal A", Resolved: true},
		))
		Expect(resolver.Resolve(context.Background(), "fleet-1")).To(Equal(
			pkg.WakeSubject{Fleet: true, Resolved: true},
		))
	})

	It("TestContextNameToGoal — falls back to the session name and the task page's goal", func() {
		write(contextDir+"/w-1.json", `{"session_name":"Some Task"}`)
		write(
			vaultsRoot+"/private-personal/25 Tasks/Some Task.md",
			"---\ngoals:\n    - '[[Goal B]]'\n---\n\nbody\n",
		)

		Expect(resolver.Resolve(context.Background(), "w-1")).To(Equal(
			pkg.WakeSubject{Subject: "Goal B", Resolved: true},
		))
	})

	It("TestBodyBulletIsNotAGoal — a `- [[` line outside the frontmatter is not a goal", func() {
		write(contextDir+"/w-2.json", `{"session_name":"No Goals"}`)
		write(
			vaultsRoot+"/private-personal/25 Tasks/No Goals.md",
			"---\nphase: execution\n---\n\n- [[Not A Goal]]\n",
		)

		Expect(resolver.Resolve(context.Background(), "w-2")).To(Equal(pkg.WakeSubject{}))
	})

	It("TestMissingAndMalformedRecords — an absent or unreadable record resolves nothing", func() {
		Expect(resolver.Resolve(context.Background(), "nobody")).To(Equal(pkg.WakeSubject{}))
		write(stateDir+"/worker-manager/bad.json", "{not json")
		Expect(resolver.Resolve(context.Background(), "bad")).To(Equal(pkg.WakeSubject{}))
	})

	It(
		"TestUnresolvedIsNotCached — a record written after a miss is seen on the next read",
		func() {
			Expect(resolver.Resolve(context.Background(), "late").Resolved).To(BeFalse())
			write(stateDir+"/worker-manager/late.json", `{"subject":"Goal C","branch":"goal"}`)
			Expect(resolver.Resolve(context.Background(), "late")).To(Equal(
				pkg.WakeSubject{Subject: "Goal C", Resolved: true},
			))
		},
	)
})

// sessionlessItem builds an item that names no session at all — the unowned class.
func sessionlessItem(id string) pkg.Item {
	return pkg.Item{
		ItemID:     pkg.ItemID(id),
		Payload:    pkg.Payload("cron " + id),
		ProducerID: "exit-observer-keeper",
	}
}
