// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("managerObserveRetracts", func() {
	const (
		cleared = "aaaaaaaa-1111-4000-8000-000000000001"
		parked  = "bbbbbbbb-2222-4000-8000-000000000002"
	)

	openWith := func(keys ...string) *ManagerOpenView {
		view := &ManagerOpenView{
			Producers: map[string]struct{}{},
			DedupKeys: map[string]struct{}{},
			ParkItems: map[string]ItemID{},
		}
		for _, key := range keys {
			view.DedupKeys[key] = struct{}{}
			view.ParkItems[key] = ItemID("item-" + key)
		}
		return view
	}

	It("closes a tracked card whose session cleared and keeps a still-parked one", func() {
		states := ManagerGateStates{
			managerGateTestState(cleared, "A", ManagerGateCleared, "idle:no-ask", ""),
			managerGateTestState(parked, "B", ManagerGateGated, "idle+closer", "pick — 1. X"),
		}
		retracts := managerObserveRetracts(ManagerGateObserveInput{
			States: states,
			Open:   openWith("park:aaaaaaaa", "park:bbbbbbbb"),
		}, managerParkedSessions(states))
		Expect(retracts).To(HaveLen(1))
		Expect(retracts[0].DedupKey).To(Equal(DedupKey("park:aaaaaaaa")))
		Expect(retracts[0].ItemID).To(Equal(ItemID("item-park:aaaaaaaa")))
	})

	It("never retracts a card whose session this manager does not track", func() {
		states := ManagerGateStates{
			managerGateTestState(parked, "B", ManagerGateGated, "idle+closer", "pick — 1. X"),
		}
		retracts := managerObserveRetracts(ManagerGateObserveInput{
			States: states,
			Open:   openWith("park:cccccccc"),
		}, managerParkedSessions(states))
		Expect(retracts).To(BeEmpty())
	})

	It("retracts nothing when the store read failed", func() {
		states := ManagerGateStates{
			managerGateTestState(cleared, "A", ManagerGateCleared, "idle:no-ask", ""),
		}
		retracts := managerObserveRetracts(ManagerGateObserveInput{
			States: states,
			Open:   nil,
		}, managerParkedSessions(states))
		Expect(retracts).To(BeEmpty())
	})
})
