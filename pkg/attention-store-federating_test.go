// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The federating store is a decorator, so its contract is "merge the peer's
// items into a read, proxy a write to whichever store owns the item, and never
// let the peer's absence take the local board down". The specs below are
// written against faked inner stores rather than a real boltkv store and a real
// HTTP peer, because either would put its own behaviour between the assertion
// and the thing being asserted.
//
// ⚠️ The cache specs drive the window through mutableClock (defined in
// session-liveness-cache_test.go) rather than sleeping. Real time would make
// them slow and, at a 2 s window, flaky — and the count of peer reads is the
// actual thing under test, so it is asserted directly rather than inferred from
// timing.
var _ = Describe("Federating attention store", func() {
	var ctx context.Context
	var local *mocks.AttentionStore
	var remote *mocks.RemoteAttentionStore
	var clock *mutableClock
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		local = &mocks.AttentionStore{}
		remote = &mocks.RemoteAttentionStore{}
		clock = newMutableClock()
		store = pkg.NewFederatingAttentionStoreWithClock(local, remote, clock)
	})

	Context("reading", func() {
		It("merges the peer's items after the local ones", func() {
			local.ReadReturns(pkg.Items{{ItemID: "local-1"}}, nil)
			remote.ReadReturns(pkg.Items{{ItemID: "peer-1"}}, nil)

			items, err := store.Read(ctx)

			Expect(err).Should(BeNil())
			Expect(items).Should(HaveLen(2))
			Expect(items[0].ItemID).Should(Equal(pkg.ItemID("local-1")))
			Expect(items[1].ItemID).Should(Equal(pkg.ItemID("peer-1")))
		})

		It("merges the peer's items into a board read too", func() {
			local.ReadBoardReturns(pkg.Items{{ItemID: "local-1"}}, nil)
			remote.ReadReturns(pkg.Items{{ItemID: "peer-1"}}, nil)

			items, err := store.ReadBoard(ctx)

			Expect(err).Should(BeNil())
			Expect(items).Should(HaveLen(2))
		})

		It("keeps the local item when an id collides", func() {
			local.ReadReturns(pkg.Items{{ItemID: "same", Payload: "local"}}, nil)
			remote.ReadReturns(pkg.Items{{ItemID: "same", Payload: "peer"}}, nil)

			items, err := store.Read(ctx)

			Expect(err).Should(BeNil())
			Expect(items).Should(HaveLen(1))
			Expect(items[0].Payload).Should(Equal(pkg.Payload("local")))
		})

		It("degrades to the local items when the peer fails, and does not fail the read", func() {
			local.ReadReturns(pkg.Items{{ItemID: "local-1"}}, nil)
			remote.ReadReturns(nil, errors.New(ctx, "peer is down"))

			items, err := store.Read(ctx)

			Expect(err).Should(BeNil())
			Expect(items).Should(HaveLen(1))
			Expect(items[0].ItemID).Should(Equal(pkg.ItemID("local-1")))
		})

		It("returns the local store's error rather than the peer's", func() {
			local.ReadReturns(nil, errors.New(ctx, "local is down"))
			remote.ReadReturns(pkg.Items{{ItemID: "peer-1"}}, nil)

			_, err := store.Read(ctx)

			Expect(err).ShouldNot(BeNil())
			Expect(remote.ReadCallCount()).Should(Equal(0))
		})
	})

	Context("caching the peer read", func() {
		It("reuses one peer read within the window", func() {
			local.ReadReturns(nil, nil)
			remote.ReadReturns(pkg.Items{{ItemID: "peer-1"}}, nil)

			_, err := store.Read(ctx)
			Expect(err).Should(BeNil())
			_, err = store.Read(ctx)
			Expect(err).Should(BeNil())

			Expect(remote.ReadCallCount()).Should(Equal(1))
		})

		It("re-reads the peer once the window has passed", func() {
			local.ReadReturns(nil, nil)
			remote.ReadReturns(pkg.Items{{ItemID: "peer-1"}}, nil)

			_, err := store.Read(ctx)
			Expect(err).Should(BeNil())
			clock.Advance(libtime.Duration(3 * time.Second))
			_, err = store.Read(ctx)
			Expect(err).Should(BeNil())

			Expect(remote.ReadCallCount()).Should(Equal(2))
		})

		It("does not cache a failed peer read", func() {
			local.ReadReturns(nil, nil)
			remote.ReadReturns(nil, errors.New(ctx, "peer is down"))

			_, err := store.Read(ctx)
			Expect(err).Should(BeNil())
			_, err = store.Read(ctx)
			Expect(err).Should(BeNil())

			Expect(remote.ReadCallCount()).Should(Equal(2))
		})

		It("does not let an empty peer read force a refetch on every render", func() {
			local.ReadReturns(nil, nil)
			remote.ReadReturns(pkg.Items{}, nil)

			_, err := store.Read(ctx)
			Expect(err).Should(BeNil())
			_, err = store.Read(ctx)
			Expect(err).Should(BeNil())

			Expect(remote.ReadCallCount()).Should(Equal(1))
		})
	})

	Context("routing a write", func() {
		It("proxies an answer for an item the local store does not hold", func() {
			local.GetReturns(nil, pkg.ErrItemNotFound)
			remote.AnswerReturns(&pkg.Item{ItemID: "peer-1"}, nil)

			item, err := store.Answer(ctx, "peer-1", "board", "session-1", "", nil, nil, nil)

			Expect(err).Should(BeNil())
			Expect(item.ItemID).Should(Equal(pkg.ItemID("peer-1")))
			Expect(remote.AnswerCallCount()).Should(Equal(1))
			Expect(local.AnswerCallCount()).Should(Equal(0))
		})

		It("applies an answer locally for an item the local store holds", func() {
			local.GetReturns(&pkg.Item{ItemID: "local-1"}, nil)
			local.AnswerReturns(&pkg.Item{ItemID: "local-1"}, nil)

			item, err := store.Answer(ctx, "local-1", "board", "session-1", "", nil, nil, nil)

			Expect(err).Should(BeNil())
			Expect(item.ItemID).Should(Equal(pkg.ItemID("local-1")))
			Expect(local.AnswerCallCount()).Should(Equal(1))
			Expect(remote.AnswerCallCount()).Should(Equal(0))
		})

		It(
			"sends the write to the local store when the local read fails for another reason",
			func() {
				local.GetReturns(nil, errors.New(ctx, "local is down"))
				local.CloseReturns(&pkg.Item{ItemID: "local-1"}, nil)

				_, err := store.Close(ctx, "local-1", "board", nil)

				Expect(err).Should(BeNil())
				Expect(local.CloseCallCount()).Should(Equal(1))
				Expect(remote.CloseCallCount()).Should(Equal(0))
			},
		)

		It("proxies an escalation for an item the local store does not hold", func() {
			local.GetReturns(nil, pkg.ErrItemNotFound)
			remote.EscalateReturns(&pkg.Item{ItemID: "peer-1"}, nil)

			_, err := store.Escalate(ctx, "peer-1", "session-1")

			Expect(err).Should(BeNil())
			Expect(remote.EscalateCallCount()).Should(Equal(1))
			Expect(local.EscalateCallCount()).Should(Equal(0))
		})
	})

	Context("delegating the rest", func() {
		It("never pushes to the peer", func() {
			local.PushReturns(&pkg.Item{ItemID: "local-1"}, nil)

			_, err := store.Push(ctx, pkg.PushRequest{})

			Expect(err).Should(BeNil())
			Expect(local.PushCallCount()).Should(Equal(1))
		})

		It("keeps history local", func() {
			local.HistoryReturns(pkg.Items{{ItemID: "local-1"}}, nil)

			items, err := store.History(ctx, 10, 0)

			Expect(err).Should(BeNil())
			Expect(items).Should(HaveLen(1))
			Expect(local.HistoryCallCount()).Should(Equal(1))
		})
	})
})
