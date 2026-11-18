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

// attributionVector is one row of the committed fixture: a stable name, the
// verdict the ladder must return, and the item to classify.
type attributionVector struct {
	Name    string   `json:"name"`
	Verdict string   `json:"verdict"`
	Item    pkg.Item `json:"item"`
}

var _ = Describe("AnsweredVersusNotOperatorAnsweredDeclinedHidden attribution ladder", func() {
	// The fixture is committed under pkg/testdata/ and read from disk on every
	// pass, exactly as completed-task-source_test.go reads its fixture. The rows
	// are the contract: a spec that built its items inline would keep passing
	// after the committed table was deleted.
	const fixture = "testdata/answer-attribution-vectors.json"

	var ctx context.Context
	var vectors []attributionVector

	BeforeEach(func() {
		ctx = context.Background()
		raw, err := os.ReadFile(fixture)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(raw, &vectors)).To(Succeed())
	})

	It("classifies every vector as declared with a non-empty reason", func() {
		for _, vector := range vectors {
			verdict, reason := pkg.ClassifyAnswerAttribution(ctx, vector.Item)
			Expect(verdict).To(Equal(pkg.AttributionVerdict(vector.Verdict)), vector.Name)
			Expect(reason).NotTo(BeEmpty(), vector.Name)
		}
	})

	It("covers every verdict", func() {
		covered := make(map[pkg.AttributionVerdict]bool, len(vectors))
		for _, vector := range vectors {
			covered[pkg.AttributionVerdict(vector.Verdict)] = true
		}
		for _, verdict := range []pkg.AttributionVerdict{
			pkg.AttributionVerdictNothing,
			pkg.AttributionVerdictUnattributed,
			pkg.AttributionVerdictDeclined,
			pkg.AttributionVerdictHidden,
			pkg.AttributionVerdictAutomated,
			pkg.AttributionVerdictAttributed,
		} {
			Expect(covered).To(HaveKey(verdict))
		}
	})

	It("carries the three boundary rows by name", func() {
		names := make([]string, 0, len(vectors))
		for _, vector := range vectors {
			names = append(names, vector.Name)
		}
		// Deleting any one of these rows must fail here rather than merely shrink
		// a count elsewhere.
		Expect(names).To(ContainElements(
			"unattributed-close-without-answered-at",
			"declined-skip-by-automation",
			"attributed-with-resolved-by",
		))
	})

	It("never reads a scripted click as the operator's answer", func() {
		row := vectorNamed(vectors, "automated-scripted-click")
		verdict, reason := pkg.ClassifyAnswerAttribution(ctx, row.Item)
		Expect(verdict).To(Equal(pkg.AttributionVerdictAutomated))
		Expect(verdict).NotTo(Equal(pkg.AttributionVerdictAttributed))
		Expect(reason).To(ContainSubstring("automated"))
	})

	It("derives the rendered vocabulary from the verdict", func() {
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictAttributed)).To(Equal("ANSWERED"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictDeclined)).To(Equal("DECLINED"))
		Expect(pkg.RenderedAnswerSignal(pkg.AttributionVerdictHidden)).To(Equal("HIDDEN"))
		Expect(
			pkg.RenderedAnswerSignal(pkg.AttributionVerdictNothing),
		).To(Equal("NOT_OPERATOR_ANSWERED"))
		Expect(
			pkg.RenderedAnswerSignal(pkg.AttributionVerdictUnattributed),
		).To(Equal("NOT_OPERATOR_ANSWERED"))
		Expect(
			pkg.RenderedAnswerSignal(pkg.AttributionVerdictAutomated),
		).To(Equal("NOT_OPERATOR_ANSWERED"))
		Expect(
			pkg.RenderedAnswerSignal(pkg.AttributionVerdict("UNKNOWN")),
		).To(Equal("NOT_OPERATOR_ANSWERED"))
	})

	It("does not require resolved_by", func() {
		without := vectorNamed(vectors, "attributed-board-click")
		with := vectorNamed(vectors, "attributed-with-resolved-by")
		// The pair is the guard: one row has no resolved_by and one carries it, and
		// both must classify ATTRIBUTED. Adding a resolved_by requirement fails here.
		Expect(without.Item.ResolvedBy).To(BeEmpty())
		Expect(with.Item.ResolvedBy).NotTo(BeEmpty())

		withoutVerdict, _ := pkg.ClassifyAnswerAttribution(ctx, without.Item)
		withVerdict, _ := pkg.ClassifyAnswerAttribution(ctx, with.Item)
		Expect(withoutVerdict).To(Equal(pkg.AttributionVerdictAttributed))
		Expect(withVerdict).To(Equal(pkg.AttributionVerdictAttributed))
	})
})

// vectorNamed returns the row with the given name, failing the spec when it is
// absent so a deleted row names itself in the failure.
func vectorNamed(vectors []attributionVector, name string) attributionVector {
	for _, vector := range vectors {
		if vector.Name == name {
			return vector
		}
	}
	Fail("vector not found: " + name)
	return attributionVector{}
}
