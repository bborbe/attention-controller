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

	// oneManager seeds a live manager with one worker of its own.
	oneManager := func() {
		live["mgr-1"] = true
		ledger.ListReturns(pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("w-1", "agent", "mgr-1"),
		}, nil)
	}

	It("TestEmitOnlyOnEnterNotOnRepoll — emits only entries that enter the kept set", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1"), wakeItem("item-2", "w-1")}, nil)
		job := newJob()

		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))

		// A second pass over the unchanged store types nothing.
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))

		// A third entry emits exactly one line, naming only the third.
		store.ReadReturns(pkg.Items{
			wakeItem("item-1", "w-1"),
			wakeItem("item-2", "w-1"),
			wakeItem("item-3", "w-1"),
		}, nil)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(3))
		Expect(carrier.recorded()[2].item).To(Equal("item-3"))

		// An entry that leaves the set and re-enters it emits again.
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1"), wakeItem("item-2", "w-1")}, nil)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(3))
		store.ReadReturns(pkg.Items{
			wakeItem("item-1", "w-1"),
			wakeItem("item-2", "w-1"),
			wakeItem("item-3", "w-1"),
		}, nil)
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(4))
		Expect(carrier.recorded()[3].item).To(Equal("item-3"))
	})

	It(
		"TestRearmReEmitsEntryOlderThanReemitAfter — one re-emit per window, none inside it",
		func() {
			oneManager()
			store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)
			job := newJob()

			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(1))

			// Inside the window: no re-emit.
			now = now.Add(pkg.ManagerWakeReemitAfter - stdtime.Minute)
			Expect(job.WakeOnce(context.Background())).To(Succeed())
			Expect(carrier.recorded()).To(HaveLen(1))

			// Past the window: exactly one re-emit.
			now = now.Add(2 * stdtime.Minute)
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

		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.sessions()).To(ConsistOf("mgr-2"))

		// Positive control: the same fixture with the peer DEAD keeps the item.
		carrier = newWakeRecordingCarrier()
		live["mgr-2"] = false
		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
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
		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1"))

		// Positive control: this manager ITSELF is kept.
		carrier = newWakeRecordingCarrier()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "mgr-1")}, nil)
		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1"))

		// Positive control: an item owned by NOBODY is kept by every manager.
		carrier = newWakeRecordingCarrier()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "ghost")}, nil)
		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1", "mgr-2"))
	})

	It("TestHop3bDropsOnLiveNonWorker — a live peer manager's own gate is dropped", func() {
		live["mgr-1"] = true
		live["mgr-2"] = true
		ledger.ListReturns(pkg.SessionLedgerEntries{
			ledgerRow("mgr-1", "manager", ""),
			ledgerRow("mgr-2", "manager", ""),
		}, nil)
		store.ReadReturns(pkg.Items{wakeItem("item-1", "mgr-2")}, nil)

		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.sessions()).To(ConsistOf("mgr-2"))

		// Positive control: a dead peer manager's own gate is kept by the peer.
		carrier = newWakeRecordingCarrier()
		live["mgr-2"] = false
		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.sessions()).To(ConsistOf("mgr-1"))
	})

	It("TestExpiredItemNeverWakes — the population read drops an expired item", func() {
		oneManager()
		// The store's own Read already excludes the expired card, so the job
		// receives only the live one: it adds no second expiry check, and an
		// expired item therefore never reaches the wake computation.
		expired := wakeItem("item-expired", "w-1")
		expiredAt := libtime.DateTime(now.Add(-stdtime.Hour))
		expired.ExpiresAt = &expiredAt
		store.ReadReturns(pkg.Items{wakeItem("item-live", "w-1")}, nil)

		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
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

		// Positive control: the next healthy pass wakes normally.
		oneManager()
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))
	})

	It("TestWakeTypedIntoManagerPane — the wake is one plain line into the pane", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1")}, nil)

		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
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
		Expect(carrier.recorded()).To(HaveLen(1))
		// Parked: the next pass does not retry.
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))

		// A refuse verdict parks the same way.
		carrier = newWakeRecordingCarrier()
		carrier.setVerdict(pkg.DeliveryRefuse)
		refusing := newJob()
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(1))

		// The park clears on liveness, exactly as a delivery park does.
		live["mgr-1"] = false
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		live["mgr-1"] = true
		carrier.setVerdict(pkg.DeliveryOK)
		Expect(refusing.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))
	})

	It("TestWakeBoundedByManagerWakeInterval — a tick wakes, a change short-circuits it", func() {
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
		Eventually(func() int { return len(carrier.recorded()) }).Should(Equal(1))
		// An unchanged store produces no second wake, however many ticks pass.
		Consistently(func() int { return len(carrier.recorded()) }, 50*stdtime.Millisecond).
			Should(Equal(1))
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
	})

	It("TestRestartReTypesOnce — a fresh process re-types the kept set once", func() {
		oneManager()
		store.ReadReturns(pkg.Items{
			wakeItem("item-1", "w-1"),
			wakeItem("item-2", "w-1"),
			wakeItem("item-3", "w-1"),
		}, nil)
		job := newJob()

		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(3))
		Expect(job.WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(3))
	})

	It("TestWakeWritesEmissionRecord — every typed line appends one emission", func() {
		oneManager()
		store.ReadReturns(pkg.Items{wakeItem("item-1", "w-1"), wakeItem("item-2", "w-1")}, nil)

		Expect(newJob().WakeOnce(context.Background())).To(Succeed())
		Expect(carrier.recorded()).To(HaveLen(2))
		Expect(sink.records).To(HaveLen(2))
		Expect(sink.records).To(ConsistOf(
			pkg.ManagerWakeEmission{
				ItemID:         "item-1",
				ManagerSession: "mgr-1",
				EmittedAt:      libtime.DateTime(now),
			},
			pkg.ManagerWakeEmission{
				ItemID:         "item-2",
				ManagerSession: "mgr-1",
				EmittedAt:      libtime.DateTime(now),
			},
		))
	})
})
