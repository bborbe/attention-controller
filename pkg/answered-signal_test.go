// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// attributionVector is one row of the committed fixture: a name, the verdict
// the ladder must return, and the item it is decided over. The item is in the
// struct's own JSON shape, so the fixture unmarshals straight into pkg.Item.
type attributionVector struct {
	Name    string   `json:"name"`
	Verdict string   `json:"verdict"`
	Item    pkg.Item `json:"item"`
}

var _ = Describe("AnsweredVersusNotOperatorAnsweredDeclinedHidden answer attribution", func() {
	// The fixture is read from disk on every pass, never built inline: the
	// checked-in table is the contract, and a spec that invented its own rows
	// would prove nothing about the vectors the guard table ships.
	const fixture = "testdata/answer-attribution-vectors.json"

	var vectors []attributionVector

	BeforeEach(func() {
		data, err := os.ReadFile(fixture)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(data, &vectors)).To(Succeed())
		Expect(vectors).NotTo(BeEmpty())
	})

	It("classifies every vector as declared, with a non-empty reason", func() {
		for _, vector := range vectors {
			verdict, reason := pkg.ClassifyAnswerAttribution(
				context.Background(), vector.Item,
			)
			Expect(verdict).To(Equal(pkg.AttributionVerdict(vector.Verdict)),
				"vector %q", vector.Name)
			Expect(reason).NotTo(BeEmpty(), "vector %q", vector.Name)
		}
	})

	It("covers every verdict", func() {
		seen := make(map[pkg.AttributionVerdict]bool, len(vectors))
		for _, vector := range vectors {
			seen[pkg.AttributionVerdict(vector.Verdict)] = true
		}
		for _, verdict := range []pkg.AttributionVerdict{
			pkg.AttributionVerdictNothing,
			pkg.AttributionVerdictUnattributed,
			pkg.AttributionVerdictDeclined,
			pkg.AttributionVerdictHidden,
			pkg.AttributionVerdictAutomated,
			pkg.AttributionVerdictAttributed,
		} {
			Expect(seen).To(HaveKey(verdict),
				"no vector covers verdict %q", verdict)
		}
	})

	It("carries the three boundary rows by name", func() {
		names := make(map[string]bool, len(vectors))
		for _, vector := range vectors {
			names[vector.Name] = true
		}
		// Each of these is a row deleting which must fail the suite, not merely
		// shrink a count: a close is not an answer, kind wins over the automation
		// flag, and a present resolved_by changes nothing.
		Expect(names).To(HaveKey("unattributed-close-without-answered-at"))
		Expect(names).To(HaveKey("declined-skip-by-automation"))
		Expect(names).To(HaveKey("attributed-with-resolved-by"))
	})

	It("never reads a scripted click as the operator's answer", func() {
		var scripted *attributionVector
		for i := range vectors {
			if vectors[i].Name == "automated-scripted-click" {
				scripted = &vectors[i]
			}
		}
		Expect(scripted).NotTo(BeNil(), "the scripted-click vector is missing")

		verdict, reason := pkg.ClassifyAnswerAttribution(
			context.Background(), scripted.Item,
		)
		Expect(verdict).To(Equal(pkg.AttributionVerdictAutomated))
		Expect(verdict).NotTo(Equal(pkg.AttributionVerdictAttributed))
		Expect(reason).To(ContainSubstring("automation"),
			"the reason must name the flag so the failure is diagnosable")
	})

	It("derives the rendered vocabulary from the verdict", func() {
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictAttributed)).
			To(Equal("ANSWERED"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictDeclined)).
			To(Equal("DECLINED"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictHidden)).
			To(Equal("HIDDEN"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictNothing)).
			To(Equal("NOT_OPERATOR_ANSWERED"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictUnattributed)).
			To(Equal("NOT_OPERATOR_ANSWERED"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictAutomated)).
			To(Equal("NOT_OPERATOR_ANSWERED"))
	})

	It("does not require resolved_by", func() {
		byName := make(map[string]pkg.Item, len(vectors))
		for _, vector := range vectors {
			byName[vector.Name] = vector.Item
		}
		withoutResolvedBy, ok := byName["attributed-board-click"]
		Expect(ok).To(BeTrue())
		withResolvedBy, ok := byName["attributed-with-resolved-by"]
		Expect(ok).To(BeTrue())
		Expect(withoutResolvedBy.ResolvedBy).To(BeEmpty())
		Expect(withResolvedBy.ResolvedBy).NotTo(BeEmpty())

		withoutVerdict, _ := pkg.ClassifyAnswerAttribution(
			context.Background(), withoutResolvedBy,
		)
		withVerdict, _ := pkg.ClassifyAnswerAttribution(
			context.Background(), withResolvedBy,
		)
		Expect(withoutVerdict).To(Equal(pkg.AttributionVerdictAttributed))
		Expect(withVerdict).To(Equal(pkg.AttributionVerdictAttributed),
			"a present resolved_by must not change the verdict")
	})
})
