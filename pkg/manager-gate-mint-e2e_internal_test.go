// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// SC2 e2e (integration): a manager whose last closer literally reads `foo —`
// must mint no `park:` card over two rounds, while a `pick —` manager is carded.
// The gate state is derived from the closer through the REAL `managerGateVerdict`,
// then threaded through `managerParkedSessions` and `managerRepostPlan` — the
// same chain the wake job runs — so this is not a hand-constructed state.
var _ = Describe("manager mint e2e (SC2)", func() {
	It("mints 0 cards for a foo — closer and cards a pick — closer, over two rounds", func() {
		fooDecision, fooReason := managerGateVerdict(managerGateInput{
			Status: managerGateTestStatus("idle"), CloserKnown: true, CloserBody: "foo —",
		})
		Expect(fooDecision).To(Equal(ManagerGateCleared))
		Expect(fooReason).To(Equal("idle:no-ask"))

		pickDecision, pickReason := managerGateVerdict(managerGateInput{
			Status: managerGateTestStatus("idle"), CloserKnown: true, CloserBody: "pick — 1. X",
		})
		Expect(pickDecision).To(Equal(ManagerGateGated))
		Expect(pickReason).To(Equal("idle+closer"))

		fooState := managerGateTestState(
			"aaaaaaaa-1111-4000-8000-000000000001", "foo-mgr", fooDecision, fooReason, "foo —")
		pickState := managerGateTestState(
			"bbbbbbbb-2222-4000-8000-000000000002", "pick-mgr", pickDecision, pickReason, "pick — 1. X")

		for round := 1; round <= 2; round++ {
			states := ManagerGateStates{fooState, pickState}
			parked := managerParkedSessions(states)
			Expect(parked).To(ConsistOf(pickState.SessionID), "round %d: only the pick manager is parked", round)

			entries := map[string]managerTrackedEntry{
				managerSessionPrefix(fooState.SessionID):  {FullID: fooState.SessionID, Label: "foo-mgr"},
				managerSessionPrefix(pickState.SessionID): {FullID: pickState.SessionID, Label: "pick-mgr"},
			}
			reasons := map[string]string{
				managerSessionPrefix(fooState.SessionID):  fooReason,
				managerSessionPrefix(pickState.SessionID): pickReason,
			}
			prefixes := make([]string, 0, len(parked))
			for _, session := range parked {
				prefixes = append(prefixes, managerSessionPrefix(session))
			}
			requests := managerRepostPlan(managerRepostInput{
				Parked:  prefixes,
				Entries: entries,
				Open: &ManagerOpenView{
					Producers: map[string]struct{}{},
					DedupKeys: map[string]struct{}{},
					ParkItems: map[string]ItemID{},
				},
				Answered: &ManagerAnswerView{BySession: map[string]ManagerParkAnswer{}},
				Reasons:  reasons,
			})
			// Exactly one card: the pick manager. The foo manager mints nothing.
			Expect(requests).To(HaveLen(1), "round %d", round)
			Expect(requests[0].ProducerID).To(Equal(pickState.SessionID))
			Expect(requests[0].DedupKey).To(Equal(DedupKey("park:bbbbbbbb")))
		}
	})
})
