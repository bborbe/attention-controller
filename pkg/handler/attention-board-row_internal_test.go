// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This is an internal test package rather than the external `handler_test` the
// rest of the directory uses: newBoardRow, newBoardView and
// navigationSessionName are unexported, and the blast-radius cases force a
// payload through the board's own row derivation — the "forced through the
// renderer" probe SC2 names — without a store round-trip. The specs register
// into the same ginkgo suite handler_suite_test.go runs.
package handler

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("board row blast radius", func() {
	viewOf := func(item pkg.Item) pkg.BoardView {
		return newBoardView(newBoardRow(item, pkg.Provenance{}, nil, false, ""))
	}

	It("carries a novel irreversible-infra payload at risk with a blast radius", func() {
		// Invented at test time and present nowhere in the store, so a derivation
		// that special-cases the stored specimen fails this spec.
		view := viewOf(pkg.Item{
			ItemID:  "infra",
			Payload: pkg.Payload("move `vg0/data` from `hell` to `nuke-dev`"),
		})
		Expect(view.Risk).To(Equal(pkg.Risk("risk")))
		Expect(view.BlastRadius).NotTo(BeEmpty())
	})

	It("carries the stored specimen at risk with a blast radius", func() {
		// The card 491aab84 carried this payload verbatim.
		view := viewOf(pkg.Item{
			ItemID: "stored",
			Payload: pkg.Payload(
				"Approve migrating sun's 2 k3s VMs to nuke? [production-touching] " +
					"Change: migrate sun-k3s-master-0 (4 GiB) + sun-k3s-worker-0 (48→16 GiB) to nuke; " +
					"shrink nuke-k3s-dev-worker-0 to 24 GiB in the same change. " +
					"Blast radius: nuke is the TRADING hypervisor (runs nuke dev+prod) hosting 10 VMs — " +
					"this lands at ~130 GiB declared on 125 GiB physical (~4% declared overcommit; actual usage far lower). " +
					"Preserves cluster identity (kube-system UID, CA, node names) that SC1 and goal rows 2/3 need; " +
					"a rebuild would not. Reversible by migrating back.",
			),
		})
		Expect(view.Risk).To(Equal(pkg.Risk("risk")))
		Expect(view.BlastRadius).NotTo(BeEmpty())
	})

	It("carries a routine pick at decision with no blast radius", func() {
		view := viewOf(pkg.Item{
			ItemID:          "routine",
			Payload:         pkg.Payload("Approve starting task: clean the stale build cache?"),
			InterruptClass:  pkg.PickInterruptClass,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         pkg.AnswerOptions{{Label: "approve — start the task"}},
		})
		Expect(view.Risk).To(Equal(pkg.Risk("decision")))
		Expect(view.BlastRadius).To(BeEmpty())
	})

	It("raises the risk from the resolved production-touching marker", func() {
		item := pkg.Item{
			ItemID:          "marked",
			Payload:         pkg.Payload("Approve starting task: clean the stale build cache?"),
			InterruptClass:  pkg.PickInterruptClass,
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
		plain := newBoardView(newBoardRow(item, pkg.Provenance{}, nil, false, ""))
		marked := newBoardView(newBoardRow(item, pkg.Provenance{ProductionTouching: true}, nil, false, ""))
		Expect(marked.Risk).To(Equal(pkg.RiskOf(item, true)))
		Expect(plain.Risk).To(Equal(pkg.RiskOf(item, false)))
		Expect(marked.Risk).NotTo(Equal(plain.Risk))
	})
})

// navigationSessionName is a pure function whose whole job is one comparison,
// so every branch is reachable here.
var _ = Describe("navigationSessionName", func() {
	It("returns the name unchanged when there is no task title to compare against", func() {
		Expect(navigationSessionName(pkg.Provenance{SessionName: "Board Polish Session"})).
			To(Equal("Board Polish Session"))
	})

	It("suppresses the name when it repeats the task title", func() {
		Expect(navigationSessionName(pkg.Provenance{
			TaskName:    "Fix the board",
			SessionName: "Fix the board",
		})).To(Equal(""))
	})

	It("suppresses the name when it repeats the task title behind a status glyph", func() {
		Expect(navigationSessionName(pkg.Provenance{
			TaskName:    "Fix the board",
			SessionName: "⚙ Fix the board",
		})).To(Equal(""))
	})

	It("returns the name when it differs from the task title", func() {
		Expect(navigationSessionName(pkg.Provenance{
			TaskName:    "Fix the board",
			SessionName: "Board Polish Session",
		})).To(Equal("Board Polish Session"))
	})

	It("returns empty for a provenance carrying neither value", func() {
		Expect(navigationSessionName(pkg.Provenance{})).To(Equal(""))
	})
})
