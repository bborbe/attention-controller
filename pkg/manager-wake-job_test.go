// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"errors"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// wakeCall is one line the carrier was asked to type.
type wakeCall struct {
	session string
	item    string
	text    string
	bare    bool
}

// wakeRecordingCarrier is a scripted PaneCarrier that records every call. The
// mutex is not decoration: a spec that drives Run reads this from the test
// goroutine while the job writes from its own.
type wakeRecordingCarrier struct {
	mutex   chan struct{}
	calls   []wakeCall
	verdict pkg.DeliveryVerdict
}

func newWakeRecordingCarrier() *wakeRecordingCarrier {
	carrier := &wakeRecordingCarrier{mutex: make(chan struct{}, 1), verdict: pkg.DeliveryOK}
	carrier.mutex <- struct{}{}
	return carrier
}

func (c *wakeRecordingCarrier) TypeIntoPane(
	_ context.Context,
	delivery *pkg.PaneDelivery,
	text string,
	bare bool,
) (pkg.DeliveryVerdict, string) {
	<-c.mutex
	defer func() { c.mutex <- struct{}{} }()
	c.calls = append(c.calls, wakeCall{
		session: delivery.SessionID,
		item:    delivery.StoreItemID,
		text:    text,
		bare:    bare,
	})
	return c.verdict, "scripted"
}

// recorded returns a copy of the calls made so far.
func (c *wakeRecordingCarrier) recorded() []wakeCall {
	<-c.mutex
	defer func() { c.mutex <- struct{}{} }()
	copied := make([]wakeCall, len(c.calls))
	copy(copied, c.calls)
	return copied
}

// sessions returns the session of every recorded call.
func (c *wakeRecordingCarrier) sessions() []string {
	sessions := make([]string, 0, len(c.recorded()))
	for _, call := range c.recorded() {
		sessions = append(sessions, call.session)
	}
	return sessions
}

// setVerdict swaps the verdict the carrier returns.
func (c *wakeRecordingCarrier) setVerdict(verdict pkg.DeliveryVerdict) {
	<-c.mutex
	defer func() { c.mutex <- struct{}{} }()
	c.verdict = verdict
}

// recordingEmissionSink records every emission.
type recordingEmissionSink struct {
	records []pkg.ManagerWakeEmission
}

func (s *recordingEmissionSink) Append(
	_ context.Context,
	emission pkg.ManagerWakeEmission,
) error {
	s.records = append(s.records, emission)
	return nil
}

// wakeItem builds an open item raised by a session.
func wakeItem(id string, session string) pkg.Item {
	return pkg.Item{
		ItemID:      pkg.ItemID(id),
		ProducerID:  pkg.ProducerID(session),
		LivenessRef: pkg.LivenessRef("session:" + session),
		Payload:     pkg.Payload("gate " + id),
	}
}

// ledgerRow builds one spawn-ledger row.
func ledgerRow(session string, role string, spawner string) pkg.SessionLedgerEntry {
	return pkg.SessionLedgerEntry{
		SessionID:        session,
		Role:             role,
		SpawnerSessionID: spawner,
	}
}

// ledgerRowInVault builds one spawn-ledger row carrying the cwd the vault
// comparison reads.
func ledgerRowInVault(
	session string,
	role string,
	spawner string,
	cwd string,
) pkg.SessionLedgerEntry {
	row := ledgerRow(session, role, spawner)
	row.Cwd = cwd
	return row
}

var _ = Describe("manager wake job", func() {
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
		now = stdtime.Date(2026, 11, 18, 12, 0, 0, 0, stdtime.UTC)
		clock = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
	})

	newJob := func(options ...pkg.ManagerWakeOption) pkg.ManagerWakeJob {
		return pkg.NewManagerWakeJob(
			store, ledger, provenance, carrier, notifier, clock, sink, options...,
		)
	}

	// advance moves the injected clock forward.
	advance := func(duration stdtime.Duration) {
		now = now.Add(duration)
	}

	// flushBatch runs the pass that buffers an entering card and the pass one
	// batch window later that types it.
	flushBatch := func(job pkg.ManagerWakeJob) {
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
	}

	// oneManager seeds a live manager with one worker of its own.
	oneManager := func() {
		live["mgr-1"] = true
		ledger.ListReturns(pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("w-1", "agent", "mgr-1"),
		}, nil)
	}

	It(
		"TestBatchWindowStaysWithinOneTick — an entering card is typed within the job's own wake target",
		func() {
			// The window is a delay on every card, so it may never exceed the tick
			// the job already promises (raised → woken within ManagerWakeInterval).
			Expect(pkg.ManagerWakeBatchWindow).To(BeNumerically("<=", pkg.ManagerWakeInterval))

			oneManager()
			store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
			job := newJob()

			// The first pass buffers the card; nothing is typed inside the window.
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(BeEmpty())

			// ONE tick later — not the old 60 s — the card is typed.
			advance(pkg.ManagerWakeInterval)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(1))
			Expect(carrier.recorded()[0].text).To(ContainSubstring("item-1"))
		},
	)

	It("TestEmitOnlyOnEnterNotOnRepoll — emits only entries that enter the kept set", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1"), wakeItem("item-2", "w-1")}, nil)
		job := newJob()

		// The first pass buffers both cards; nothing is typed inside the window.
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())

		// The window closes: both cards ride ONE line.
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-1"))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-2"))

		// A second pass over the unchanged store types nothing.
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))

		// A third entry emits exactly one line, naming only the third.
		store.ReadReturns(pkg.Items{
			wakeItem("item-1", "w-1"),
			wakeItem("item-2", "w-1"),
			wakeItem("item-3", "w-1"),
		}, nil)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))
		Expect(carrier.recorded()[1].text).To(ContainSubstring("item-3"))
		Expect(carrier.recorded()[1].text).NotTo(ContainSubstring("item-1"))

		// An entry that leaves the set and re-enters it emits again.
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1"), wakeItem("item-2", "w-1")}, nil)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))
		store.ReadReturns(pkg.Items{
			wakeItem("item-1", "w-1"),
			wakeItem("item-2", "w-1"),
			wakeItem("item-3", "w-1"),
		}, nil)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(3))
		Expect(carrier.recorded()[2].text).To(ContainSubstring("item-3"))
	})

	It(
		"TestRearmReEmitsEntryOlderThanReemitAfter — one re-emit per window, none inside it",
		func() {
			oneManager()
			store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
			job := newJob()

			Expect(job.WakeOnce(context.Background())).To(Succeed())
			advance(pkg.ManagerWakeBatchWindow)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(1))

			// Inside the window: no re-emit.
			advance(pkg.ManagerWakeReemitAfter - stdtime.Minute)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(1))

			// Past the window: the entry re-enters the batch...
			advance(2 * stdtime.Minute)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(1))

			// ...and is typed when its own batch window closes: exactly one re-emit.
			advance(pkg.ManagerWakeBatchWindow)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(2))

			// A second consecutive pass past the window emits nothing, because the
			// first one moved the entry's last-emission timestamp.
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(2))
		},
	)

	It("TestProducerIDScopingIgnoresPeerCard — a live peer manager's card never fires", func() {
		live["mgr-1"] = true
		live["mgr-2"] = true
		peerWorker := pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("mgr-2", "manager", ""),
			ledgerRow("w-2", "agent", "mgr-2"),
		}
		ledger.ListReturns(peerWorker, nil)
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-2")}, nil)

		flushBatch(newJob())
		Expect(carrier.sessions()).To(ConsistOf("mgr-2"))

		// Positive control: the same fixture with the peer DEAD keeps the item.
		carrier = newWakeRecordingCarrier()
		live["mgr-2"] = false
		flushBatch(newJob())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1"))

		// Positive control: this manager's OWN worker is kept.
		carrier = newWakeRecordingCarrier()
		live["mgr-2"] = true
		ledger.ListReturns(pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("mgr-2", "manager", ""),
			ledgerRow("w-1", "agent", "mgr-1"),
		}, nil)
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
		flushBatch(newJob())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1"))

		// Positive control: a card whose SESSION is this manager but which
		// another producer raised is still kept — only a card the manager
		// ITSELF produced is dropped.
		carrier = newWakeRecordingCarrier()
		store.ReadReturns(pkg.Items{{
			ItemID:      "item-1",
			ProducerID:  "elsewhere",
			LivenessRef: "session:mgr-1",
			Payload:     "gate item-1",
		}}, nil)
		flushBatch(newJob())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1"))

		// Positive control: an item owned by NOBODY is kept by every manager.
		carrier = newWakeRecordingCarrier()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "ghost")}, nil)
		flushBatch(newJob())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1", "mgr-2"))
	})

	It("TestSelfProducedCardNeverWakesItsProducer — a manager is never typed its own card", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "mgr-1")}, nil)

		// The card the manager raised is buffered-then-flushed like any other;
		// the drop happens in the keep-set, so nothing is ever typed and nothing
		// is recorded.
		flushBatch(newJob())
		Expect(carrier.sessions()).To(BeEmpty())
		Expect(sink.records).To(BeEmpty())

		// ⚠️ The test is on ProducerID, not on the liveness_ref-resolved session.
		// A card the manager PRODUCED for its own worker is still the manager's
		// own card, and is dropped even though liveness_ref names the worker.
		carrier = newWakeRecordingCarrier()
		store.ReadReturns(pkg.Items{{
			ItemID:      "item-1",
			ProducerID:  "mgr-1",
			LivenessRef: "session:w-1",
			Payload:     "gate item-1",
		}}, nil)
		flushBatch(newJob())
		Expect(carrier.sessions()).To(BeEmpty())

		// Positive control: the same worker card raised by ANOTHER producer is
		// kept, so the drop is scoped to the producer rather than to the worker.
		carrier = newWakeRecordingCarrier()
		store.ReadReturns(pkg.Items{{
			ItemID:      "item-1",
			ProducerID:  "elsewhere",
			LivenessRef: "session:w-1",
			Payload:     "gate item-1",
		}}, nil)
		flushBatch(newJob())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1"))

		// A producer written with the `session:` marker names the same manager
		// (`sessionIDFromItem` strips it as a last resort).
		carrier = newWakeRecordingCarrier()
		store.ReadReturns(pkg.Items{{
			ItemID:     "item-1",
			ProducerID: "session:mgr-1",
			Payload:    "gate item-1",
		}}, nil)
		flushBatch(newJob())
		Expect(carrier.sessions()).To(BeEmpty())

		// An item whose `liveness_ref` resolves EMPTY but whose producer is the
		// manager is still the manager's own card.
		carrier = newWakeRecordingCarrier()
		store.ReadReturns(pkg.Items{{
			ItemID:      "item-1",
			ProducerID:  "mgr-1",
			LivenessRef: "session:   ",
			Payload:     "gate item-1",
		}}, nil)
		flushBatch(newJob())
		Expect(carrier.sessions()).To(BeEmpty())
	})

	It("TestHop3bDropsOnLiveNonWorker — a live peer manager's own gate is dropped", func() {
		live["mgr-1"] = true
		live["mgr-2"] = true
		ledger.ListReturns(pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("mgr-2", "manager", ""),
		}, nil)
		store.ReadReturns(pkg.Items{wakeItem("item-1", "mgr-2")}, nil)

		// The card is mgr-2's OWN (producer == mgr-2): mgr-1 drops it as a live
		// peer's gate, and mgr-2 drops it as self-produced. Neither is typed.
		flushBatch(newJob())
		Expect(carrier.sessions()).To(BeEmpty())

		// Positive control: a dead peer manager's own gate is kept by the peer.
		carrier = newWakeRecordingCarrier()
		live["mgr-2"] = false
		flushBatch(newJob())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1"))
	})

	It(
		"TestRoleHumanNeverWakes — the operator's own workspace is not a manager's to route",
		func() {
			live["mgr-1"] = true
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRow("mgr-1", "manager", ""),
				// The reported case: the manager IS the human row's spawner, so the
				// `own-worker` branch would keep it — which is exactly the wake that
				// fired three times on 2026-10-27 and relayed nothing.
				ledgerRow("human-1", "human", "mgr-1"),
				ledgerRow("w-1", "agent", "mgr-1"),
			}, nil)
			store.ReadReturns(pkg.Items{
				wakeItem("item-human", "human-1"),
				wakeItem("item-worker", "w-1"),
			}, nil)

			flushBatch(newJob())
			// The worker's card still wakes the manager; only the human row is dropped.
			Expect(carrier.recorded()).To(HaveLen(1))
			Expect(carrier.recorded()[0].text).To(ContainSubstring("item-worker"))

			// Positive control: the same row read as an agent is kept, so the drop is
			// the `role: human` predicate and not the spawn edge.
			carrier = newWakeRecordingCarrier()
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRow("mgr-1", "manager", ""),
				ledgerRow("human-1", "agent", "mgr-1"),
			}, nil)
			store.ReadReturns(pkg.Items{wakeItem("item-human", "human-1")}, nil)
			flushBatch(newJob())
			Expect(carrier.recorded()).To(HaveLen(1))
			Expect(carrier.recorded()[0].text).To(ContainSubstring("item-human"))
		},
	)

	It(
		"TestCrossVaultSpawnerNeverWakes — a manager does not keep a worker it spawned in another vault",
		func() {
			live["mgr-1"] = true
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRowInVault(
					"mgr-1", "manager", "", "/Users/x/Documents/Obsidian/seibert-brogrammers",
				),
				ledgerRowInVault(
					"w-1", "agent", "mgr-1", "/Users/x/Documents/Obsidian/private-personal",
				),
			}, nil)
			store.ReadReturns(pkg.Items{wakeItem("item-cross", "w-1")}, nil)

			// The spawn edge names mgr-1, but the two vaults differ, so ownership is
			// not conferred and the card is dropped. Measured 2026-11-25: `2410e53c`
			// (seibert-brogrammers) spawned `76e9af4b` (private-personal) and kept 142
			// emission rows.
			flushBatch(newJob())
			Expect(carrier.recorded()).To(BeEmpty())

			// Positive control: the same spawn edge inside ONE vault still keeps the
			// card, so the drop is the vault comparison and not the spawn edge.
			carrier = newWakeRecordingCarrier()
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRowInVault(
					"mgr-1", "manager", "", "/Users/x/Documents/Obsidian/private-personal",
				),
				ledgerRowInVault(
					"w-1", "agent", "mgr-1", "/Users/x/Documents/Obsidian/private-personal",
				),
			}, nil)
			flushBatch(newJob())
			Expect(carrier.recorded()).To(HaveLen(1))
		},
	)

	It("TestForeignGoalNeverWakes — another goal's worker does not wake this manager", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-foreign", "w-1")}, nil)

		// The vault says the gate belongs to another goal. It sits above the spawn
		// edge, which would keep it: w-1 was spawned by this manager.
		flushBatch(newJob(pkg.WithManagerWakeGoalScope(&stubGoalScope{
			goal: "Other Goal", foreign: true, ok: true,
		})))
		Expect(carrier.recorded()).To(BeEmpty())

		// Positive control: the same item with the vault answering "mine" is kept,
		// so the drop is the scope and not the spawn edge.
		carrier = newWakeRecordingCarrier()
		flushBatch(newJob(pkg.WithManagerWakeGoalScope(&stubGoalScope{ok: true})))
		Expect(carrier.recorded()).To(HaveLen(1))

		// Positive control: a vault that cannot answer (ok false) keeps the gate —
		// an unresolvable owner is not a reason to silence it.
		carrier = newWakeRecordingCarrier()
		flushBatch(newJob(pkg.WithManagerWakeGoalScope(&stubGoalScope{})))
		Expect(carrier.recorded()).To(HaveLen(1))
	})

	It(
		"TestNoGoalTaskGoesToTheFleetManagerOnly — a card whose task declares no goal wakes only the Fleet Manager",
		func() {
			// The task EXISTS but declares no `goals:`, so Foreign answers
			// ok=false while SessionResolved is true. Before this change that
			// combination missed the unowned branch, fell through to hop 3b, and
			// was kept by EVERY manager — measured 2026-11-24 as three cards typed
			// to two goal managers in one hour.
			carrier = newWakeRecordingCarrier()
			live["mgr-1"] = true
			live["fleet-1"] = true
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRow("mgr-1", "manager", ""),
				ledgerRow("fleet-1", "manager", ""),
			}, nil)
			fleet := &stubFleetRegistry{registrations: []pkg.FleetManagerRegistration{
				{SessionID: "fleet-1", ResolvedAt: "2026-11-21T10:00:00+01:00"},
			}}
			store.ReadReturns(pkg.Items{wakeItem("item-nogoal", "w-nogoal")}, nil)
			flushBatch(newJob(
				pkg.WithManagerWakeGoalScope(&stubGoalScope{}),
				pkg.WithManagerWakeFleetRegistry(fleet),
			))
			Expect(carrier.sessions()).To(Equal([]string{"fleet-1"}))

			// With no live Fleet Manager it is typed nowhere, rather than into
			// every manager.
			carrier = newWakeRecordingCarrier()
			live["fleet-1"] = false
			store.ReadReturns(pkg.Items{wakeItem("item-nogoal", "w-nogoal")}, nil)
			flushBatch(newJob(
				pkg.WithManagerWakeGoalScope(&stubGoalScope{}),
				pkg.WithManagerWakeFleetRegistry(&stubFleetRegistry{}),
			))
			Expect(carrier.recorded()).To(BeEmpty())
		},
	)

	It(
		"TestUnownedClassGoesToTheFleetManagerOnly — a card whose producer anchors no task wakes only the Fleet Manager",
		func() {
			twoManagers := func() {
				live["mgr-1"] = true
				live["fleet-1"] = true
				ledger.ListReturns(pkg.SessionLedgerEntries{
					ledgerRow("mgr-1", "manager", ""),
					ledgerRow("fleet-1", "manager", ""),
				}, nil)
			}
			fleet := &stubFleetRegistry{registrations: []pkg.FleetManagerRegistration{
				{SessionID: "fleet-1", ResolvedAt: "2026-11-21T10:00:00+01:00"},
			}}

			// The vault can name no owner (ok false): the unowned class. It goes
			// to the Fleet Manager alone.
			twoManagers()
			store.ReadReturns(pkg.Items{wakeItem("item-cron", "exit-observer-keeper")}, nil)
			flushBatch(newJob(
				pkg.WithManagerWakeGoalScope(&stubGoalScope{noTask: true}),
				pkg.WithManagerWakeFleetRegistry(fleet),
			))
			Expect(carrier.sessions()).To(Equal([]string{"fleet-1"}))

			// ⚠️ The regression this change exists for: a manager with NO goal
			// page is still a real manager (it has a ledger row), so the unowned
			// card must NOT be kept for it. v0.107.9 gated on `ManagerResolved`
			// and typed the card to `5a44d47d` here.
			carrier = newWakeRecordingCarrier()
			twoManagers()
			store.ReadReturns(pkg.Items{wakeItem("item-cron", "exit-observer-keeper")}, nil)
			flushBatch(newJob(
				pkg.WithManagerWakeGoalScope(&stubGoalScope{noTask: true}),
				pkg.WithManagerWakeFleetRegistry(&stubFleetRegistry{}),
			))
			Expect(carrier.recorded()).To(BeEmpty())

			// Positive control: the manager's OWN worker is still typed to it even
			// though its task does not resolve — the own-worker branch sits above
			// the unowned routing.
			carrier = newWakeRecordingCarrier()
			live["mgr-1"] = true
			ledger.ListReturns(pkg.SessionLedgerEntries{
				ledgerRow("mgr-1", "manager", ""),
				ledgerRow("w-own", "agent", "mgr-1"),
			}, nil)
			store.ReadReturns(pkg.Items{wakeItem("item-own", "w-own")}, nil)
			flushBatch(newJob(
				pkg.WithManagerWakeGoalScope(&stubGoalScope{noTask: true}),
				pkg.WithManagerWakeFleetRegistry(fleet),
			))
			Expect(carrier.sessions()).To(Equal([]string{"mgr-1"}))

			// With no live Fleet Manager registration the unowned card is typed
			// nowhere rather than into every manager.
			carrier = newWakeRecordingCarrier()
			twoManagers()
			live["fleet-1"] = false
			store.ReadReturns(pkg.Items{wakeItem("item-cron", "exit-observer-keeper")}, nil)
			flushBatch(newJob(
				pkg.WithManagerWakeGoalScope(&stubGoalScope{noTask: true}),
				pkg.WithManagerWakeFleetRegistry(fleet),
			))
			Expect(carrier.recorded()).To(BeEmpty())
		},
	)

	It("TestExpiredItemNeverWakes — the population read drops an expired item", func() {
		oneManager()
		// The store's own Read already excludes the expired card, so the job
		// receives only the live one: it adds no second expiry check, and an
		// expired item therefore never reaches the wake computation.
		expired := wakeItem("item-expired", "w-1")
		expiredAt := libtime.DateTime(now.Add(-stdtime.Hour))
		expired.ExpiresAt = &expiredAt
		store.ReadReturns(pkg.Items{wakeItem("item-live", "w-1")}, nil)

		flushBatch(newJob())
		Expect(carrier.recorded()).To(HaveLen(1))
		Expect(carrier.recorded()[0].item).To(Equal("item-live"))
		Expect(carrier.recorded()[0].item).NotTo(Equal(string(expired.ItemID)))
	})

	It("TestFailOpenOnErrorMalformedDeadline — every read failure emits nothing", func() {
		oneManager()
		items := pkg.Items{wakeItem("item-1", "w-1")}
		store.ReadReturns(items, nil)
		job := newJob()

		store.ReadReturns(nil, errors.New("store is down"))
		Expect(job.WakeOnce(context.Background())).NotTo(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())

		store.ReadReturns(nil, context.DeadlineExceeded)
		Expect(job.WakeOnce(context.Background())).NotTo(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())

		store.ReadReturns(items, nil)
		ledger.ListReturns(nil, errors.New("malformed ledger row"))
		Expect(job.WakeOnce(context.Background())).NotTo(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())

		ledger.ListReturns(nil, context.DeadlineExceeded)
		Expect(job.WakeOnce(context.Background())).NotTo(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())

		// Positive control: the next healthy pass wakes normally, once its
		// batch window closes.
		oneManager()
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
	})

	It("TestWakeTypedIntoManagerPane — the wake is one plain line into the pane", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
		job := newJob()

		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
		Expect(carrier.recorded()[0].session).To(Equal("mgr-1"))
		Expect(carrier.recorded()[0].text).To(Equal("attention gate: item-1: gate item-1"))
		Expect(carrier.recorded()[0].bare).To(BeTrue())
	})

	It("TestWakeNoPaneParks — a no-pane or refused manager is parked, not retried", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)

		carrier.setVerdict(pkg.DeliveryNoPane)
		job := newJob()
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
		// Parked: the next pass does not retry.
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))

		// A refuse verdict parks the same way.
		carrier = newWakeRecordingCarrier()
		carrier.setVerdict(pkg.DeliveryRefuse)
		refusing := newJob()
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		advance(pkg.ManagerWakeBatchWindow)
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))

		// The park clears on liveness, exactly as a delivery park does; the card
		// kept its place in the buffer, so it is typed on the next live pass.
		live["mgr-1"] = false
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		live["mgr-1"] = true
		carrier.setVerdict(pkg.DeliveryOK)
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))
	})

	It(
		"TestWakeBoundedByManagerWakeInterval — a tick runs a pass, a change short-circuits it",
		func() {
			Expect(pkg.ManagerWakeInterval).To(Equal(10 * stdtime.Second))
			oneManager()
			store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(done)
				_ = newJob(pkg.WithManagerWakeInterval(5 * stdtime.Millisecond)).Run(ctx)
			}()
			Eventually(store.ReadCallCount).Should(BeNumerically(">=", 2))
			// The injected clock never advances, so the batch window never closes and
			// an unchanged store types nothing however many ticks pass.
			Consistently(func() int { return len(carrier.recorded()) }, 50*stdtime.Millisecond).
				Should(Equal(0))
			cancel()
			Eventually(done).Should(BeClosed())

			// A notifier signal runs a pass without advancing the clock, long before
			// the tick could.
			store.ReadCalls(func(context.Context) (pkg.Items, error) {
				return pkg.Items{wakeItem("item-1", "w-1")}, nil
			})
			changes := make(chan struct{}, 1)
			notifier.SubscribeReturns(changes, func() {})
			before := store.ReadCallCount()
			signalledCtx, signalledCancel := context.WithCancel(context.Background())
			signalledDone := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(signalledDone)
				_ = newJob(pkg.WithManagerWakeInterval(stdtime.Hour)).Run(signalledCtx)
			}()
			Consistently(store.ReadCallCount, 50*stdtime.Millisecond).Should(Equal(before))
			changes <- struct{}{}
			Eventually(store.ReadCallCount).Should(Equal(before + 1))
			Expect(now).To(Equal(stdtime.Date(2026, 11, 18, 12, 0, 0, 0, stdtime.UTC)))
			signalledCancel()
			Eventually(signalledDone).Should(BeClosed())
		},
	)

	It("TestRestartReTypesOnce — a fresh process re-types the kept set once", func() {
		oneManager()
		store.ReadReturns(pkg.Items{
			wakeItem("item-1", "w-1"),
			wakeItem("item-2", "w-1"),
			wakeItem("item-3", "w-1"),
		}, nil)
		job := newJob()

		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-1"))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-2"))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-3"))
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
	})

	It("TestWakeWritesEmissionRecord — every batched line appends one emission", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1"), wakeItem("item-2", "w-1")}, nil)
		job := newJob()

		Expect(job.WakeOnce(context.Background())).To(Succeed())
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
		// ONE emission for the ONE delivery, naming every card it carried.
		Expect(sink.records).To(HaveLen(1))
		Expect(sink.records[0]).To(Equal(pkg.ManagerWakeEmission{
			ManagerSession: "mgr-1",
			EmittedAt:      libtime.DateTime(now),
			ItemIDs:        []pkg.ItemID{"item-1", "item-2"},
		}))
	})

	It(
		"TestBatchDeferredDeliveryBacksOff — a busy pane is re-probed once per window, not per pass",
		func() {
			oneManager()
			store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
			job := newJob()

			carrier.setVerdict(pkg.DeliveryRetry)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			advance(stdtime.Second)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			// The quiet pass tried and was deferred; the batch keeps its cards.
			Expect(carrier.recorded()).To(HaveLen(1))

			// The deferral restarted the batch's clock, so the next pass does not probe
			// the pane again.
			advance(stdtime.Second)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(1))

			// Once the window has elapsed it is retried, and this time it lands.
			carrier.setVerdict(pkg.DeliveryOK)
			advance(pkg.ManagerWakeBatchWindow)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(2))
			Expect(carrier.recorded()[1].text).To(ContainSubstring("item-1"))
		},
	)

	It("TestBatchFlushesOnTheFirstQuietPass — a lone card pays one pass, never the window", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
		job := newJob()

		// The pass that buffers the card types nothing: the card may be the first
		// of a burst.
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())

		// The next pass sees no new card, so the producer has stopped raising and
		// the line is typed — one tick, not the whole window.
		advance(stdtime.Second)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-1"))
	})

	It(
		"TestBatchWindowCapsAGrowingBatch — a batch that never goes quiet is still typed at the window",
		func() {
			oneManager()
			names := []string{"item-0", "item-1", "item-2", "item-3", "item-4", "item-5", "item-6"}
			items := make(pkg.Items, 0, len(names))
			job := newJob()

			// A card on every pass keeps the batch from ever being quiet, so only the
			// window can close it.
			for i, name := range names {
				items = append(items, wakeItem(name, "w-1"))
				store.ReadReturns(items, nil)
				Expect(job.WakeOnce(context.Background())).To(Succeed())
				if i < len(names)-1 {
					advance(stdtime.Second)
				}
			}

			// Exactly one line, carrying the cards that had accumulated when the window
			// elapsed; the card raised after that opened a fresh batch.
			Expect(carrier.recorded()).To(HaveLen(1))
			Expect(carrier.recorded()[0].text).To(ContainSubstring("item-0"))
			Expect(carrier.recorded()[0].text).To(ContainSubstring("item-5"))
		},
	)

	It("TestBatchJoinsLaterArrival — the window runs from the first card", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
		job := newJob()

		Expect(job.WakeOnce(context.Background())).To(Succeed())
		advance(pkg.ManagerWakeBatchWindow / 2)
		store.ReadReturns(pkg.Items{
			wakeItem("item-1", "w-1"),
			wakeItem("item-2", "w-1"),
		}, nil)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(BeEmpty())

		// One full window after the FIRST card, both ride one line.
		advance(pkg.ManagerWakeBatchWindow / 2)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-1"))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-2"))
	})

	It("TestBatchSeparatesProducerSessions — one delivery per producer session", func() {
		live["mgr-1"] = true
		ledger.ListReturns(pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("w-1", "agent", "mgr-1"),
			ledgerRow("w-2", "agent", "mgr-1"),
		}, nil)
		store.ReadReturns(pkg.Items{
			wakeItem("item-a", "w-1"),
			wakeItem("item-b", "w-2"),
		}, nil)
		job := newJob()

		Expect(job.WakeOnce(context.Background())).To(Succeed())
		advance(pkg.ManagerWakeBatchWindow)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))
		Expect(carrier.recorded()[0].text).To(ContainSubstring("item-a"))
		Expect(carrier.recorded()[0].text).NotTo(ContainSubstring("item-b"))
		Expect(carrier.recorded()[1].text).To(ContainSubstring("item-b"))
		Expect(carrier.recorded()[1].text).NotTo(ContainSubstring("item-a"))
		Expect(sink.records).To(HaveLen(2))
	})

	It(
		"TestBatchSkipsCardAnsweredBeforeFlush — a card gone before the window needs no wake",
		func() {
			oneManager()
			store.ReadReturns(pkg.Items{
				wakeItem("item-1", "w-1"),
				wakeItem("item-2", "w-1"),
			}, nil)
			job := newJob()

			Expect(job.WakeOnce(context.Background())).To(Succeed())
			// item-2 is answered inside the window: it leaves the kept set.
			store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			advance(pkg.ManagerWakeBatchWindow)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(1))
			Expect(carrier.recorded()[0].text).To(ContainSubstring("item-1"))
			Expect(carrier.recorded()[0].text).NotTo(ContainSubstring("item-2"))
		},
	)
})

// stubGoalScope is the vault's answer, scripted. It exists so a spec can pin the
// three outcomes the exclusion must distinguish: foreign, mine, and could-not-answer.
type stubGoalScope struct {
	goal    string
	foreign bool
	ok      bool
	noTask  bool
}

func (s *stubGoalScope) Foreign(_, _ string) (string, bool, bool) {
	return s.goal, s.foreign, s.ok
}

// SessionResolved separates the cron class (no task row) from a real task that
// merely declares no goal. The zero stub answers TRUE, so a spec must opt IN to
// the unowned class rather than land in it by accident.
func (s *stubGoalScope) SessionResolved(_ string) bool {
	return !s.noTask
}

// stubFleetRegistry is the Fleet Manager registration list, scripted.
type stubFleetRegistry struct {
	registrations []pkg.FleetManagerRegistration
}

func (s *stubFleetRegistry) Registrations(_ context.Context) []pkg.FleetManagerRegistration {
	return s.registrations
}

// recordingTranscriptSource records the open-item set the pass hands it.
type recordingTranscriptSource struct {
	seen map[string]struct{}
}

func (r *recordingTranscriptSource) GateStates(
	_ context.Context,
	sessionIDs []string,
	openItems map[string]struct{},
) (pkg.ManagerGateStates, error) {
	r.seen = openItems
	states := make(pkg.ManagerGateStates, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		states = append(states, pkg.ManagerGateState{SessionID: sessionID, Label: sessionID})
	}
	return states, nil
}

// stubGateTracker accepts every pass and emits nothing.
type stubGateTracker struct{}

func (s *stubGateTracker) Observe(
	_ context.Context,
	_ pkg.ManagerGateObserveInput,
) (pkg.ManagerGateOutcome, error) {
	return pkg.ManagerGateOutcome{}, nil
}

var _ = Describe("manager wake transcript source wiring", func() {
	It("OpenItemSetReachesTheTranscriptSource", func() {
		store := &mocks.AttentionStore{}
		ledger := &mocks.SessionLedgerStore{}
		provenance := &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})
		notifier := &mocks.AttentionChangeNotifier{}
		notifier.SubscribeReturns(make(chan struct{}), func() {})
		clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(stdtime.Date(2026, 11, 18, 12, 0, 0, 0, stdtime.UTC))
		})
		store.IsSessionLiveReturns(true)
		ledger.ListReturns(pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("w-1", "agent", "mgr-1"),
		}, nil)
		store.ReadReturns(pkg.Items{
			{ItemID: "i1", ProducerID: "w-1", LivenessRef: "session:w-1", State: pkg.OpenState},
			{ItemID: "i2", State: pkg.OpenState},
		}, nil)

		source := &recordingTranscriptSource{}
		job := pkg.NewManagerWakeJob(
			store, ledger, provenance, newWakeRecordingCarrier(), notifier, clock,
			&recordingEmissionSink{},
			pkg.WithManagerWakeTranscriptSource(source, &stubGateTracker{}),
		)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(source.seen).To(HaveKey("w-1"))
		Expect(source.seen).NotTo(HaveKey(""))
	})
})
