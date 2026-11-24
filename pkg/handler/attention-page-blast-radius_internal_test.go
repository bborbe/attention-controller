// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This is an internal test package rather than the external `handler_test` the
// rest of the directory uses: the blast-radius derivation lives in the row
// render model (`newAttentionPageRow`) and the row template, both of which are
// unexported. Rendering the row definition directly is what makes this a
// RENDERER test — the mint path is covered separately in pkg, and forcing a
// payload through the page's own row template is the "forced through the
// renderer" probe SC2 names.
package handler

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("attention-row blast radius", func() {
	// renderRow executes the page's own row definition for one item.
	renderRow := func(item pkg.Item) string {
		row := newAttentionPageRow(item, pkg.Provenance{}, nil, false, "")
		var buffer bytes.Buffer
		Expect(
			newAttentionPageTemplate().ExecuteTemplate(&buffer, "attention-row", row),
		).To(BeNil())
		return buffer.String()
	}

	It("renders a novel irreversible-infra payload at risk with a blast radius", func() {
		// Invented at test time and present nowhere in the store, so a renderer
		// that special-cases the stored specimen fails this spec.
		html := renderRow(pkg.Item{
			ItemID:  "infra",
			Payload: pkg.Payload("move `vg0/data` from `hell` to `nuke-dev`"),
		})
		Expect(html).To(ContainSubstring(`data-risk="risk"`))
		Expect(html).To(ContainSubstring(`data-blast-radius="`))
		Expect(html).To(ContainSubstring("Blast radius:"))
	})

	It("renders the stored specimen at risk with a blast radius", func() {
		// The card 491aab84 carried this payload verbatim. Using the whole body,
		// not just its first sentence, keeps this the measured specimen SC2 names.
		html := renderRow(pkg.Item{
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
		Expect(html).To(ContainSubstring(`data-risk="risk"`))
		Expect(html).To(ContainSubstring(`data-blast-radius="`))
	})

	It("renders a routine pick at decision with no blast radius", func() {
		html := renderRow(pkg.Item{
			ItemID:          "routine",
			Payload:         pkg.Payload("Approve starting task: clean the stale build cache?"),
			InterruptClass:  pkg.PickInterruptClass,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         pkg.AnswerOptions{{Label: "approve — start the task"}},
		})
		Expect(html).To(ContainSubstring(`data-risk="decision"`))
		Expect(html).ToNot(ContainSubstring(`data-blast-radius`))
	})
})
