// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// This file lives in `package pkg` so the pure planning half can be exercised
// without the mock package, which imports `pkg` and would form a test-time
// import cycle here. The job half is in `menu-answer_test.go` (external).

// menuItem builds an answered hook menu card.
func menuItem(id string, questions Questions) Item {
	answeredAt := libtime.NewCurrentDateTime().Now()
	return Item{
		ItemID:          ItemID(id),
		LivenessRef:     LivenessRef("session:11111111-2222-3333-4444-555555555555"),
		ProvenanceClass: HookProvenanceClass,
		AnswerMechanism: MessageAnswerMechanism,
		Questions:       questions,
		State:           AnsweredState,
		AnsweredAt:      &answeredAt,
		AnsweredBy:      "operator",
	}
}

// singleMenu builds a one-question menu with the given labels.
func singleMenu(labels ...string) []MenuQuestion {
	return []MenuQuestion{{Labels: labels}}
}

var _ = Describe("menu question extraction", func() {
	It("reads a multi-question hook menu", func() {
		item := menuItem("item-1", Questions{
			{Tab: "Approach", Options: AnswerOptions{{Label: "A"}, {Label: "B"}}},
			{
				Tab:         "Extras",
				Cardinality: MultipleAnswerCardinality,
				Options:     AnswerOptions{{Label: "X"}},
			},
		})
		menu, ok := MenuQuestionsOf(context.Background(), item)
		Expect(ok).To(BeTrue())
		Expect(menu).To(Equal([]MenuQuestion{
			{Tab: "Approach", Labels: []string{"A", "B"}},
			{Tab: "Extras", Labels: []string{"X"}, Multi: true},
		}))
	})

	It("reads a single-question hook menu from the item's options", func() {
		item := menuItem("item-1", nil)
		item.Options = AnswerOptions{{Label: "Yes"}, {Label: "No"}}
		item.AnswerCardinality = MultipleAnswerCardinality
		menu, ok := MenuQuestionsOf(context.Background(), item)
		Expect(ok).To(BeTrue())
		Expect(menu).To(Equal([]MenuQuestion{
			{Labels: []string{"Yes", "No"}, Multi: true},
		}))
	})

	It("is not a menu on a permission item", func() {
		item := menuItem("item-1", nil)
		item.AnswerMechanism = PermissionAnswerMechanism
		item.Options = AnswerOptions{{Label: "Yes"}}
		_, ok := MenuQuestionsOf(context.Background(), item)
		Expect(ok).To(BeFalse())
	})

	It("is not a menu on a board-answered card", func() {
		// A `message` card with options that no hook asked: it has options for
		// RENDERING, but no menu is open in any pane, so typing a digit would
		// submit it into whatever prompt the session happens to show.
		item := menuItem("item-1", nil)
		item.ProvenanceClass = ExplicitProvenanceClass
		item.Options = AnswerOptions{{Label: "Yes"}}
		_, ok := MenuQuestionsOf(context.Background(), item)
		Expect(ok).To(BeFalse())
	})

	It("is not a menu when it carries neither questions nor options", func() {
		_, ok := MenuQuestionsOf(context.Background(), menuItem("item-1", nil))
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("menu digits", func() {
	ctx := context.Background()

	It("picks a single-select option by its digit", func() {
		keys, err := MenuDigits(ctx, singleMenu("Alpha", "Beta"), nil, &Answer{
			Kind:  OptionAnswerKind,
			Value: "Beta",
		})
		Expect(err).To(BeNil())
		Expect(keys).To(Equal([]string{"2"}))
	})

	It("toggles every pick of a multiple-select option then tabs out", func() {
		menu := []MenuQuestion{{Labels: []string{"A", "B", "C"}, Multi: true}}
		keys, err := MenuDigits(ctx, menu, nil, &Answer{
			Kind:   OptionAnswerKind,
			Values: []string{"A", "C"},
		})
		Expect(err).To(BeNil())
		Expect(keys).To(Equal([]string{"1", "3", "\t"}))
	})

	It("answers a free-text question through the row past the options", func() {
		keys, err := MenuDigits(ctx, singleMenu("A", "B", "C"), nil, &Answer{
			Kind:  TextAnswerKind,
			Value: "custom",
		})
		Expect(err).To(BeNil())
		Expect(keys).To(Equal([]string{"4", "custom", "\r"}))
	})

	It("collapses line breaks in a free-text answer", func() {
		keys, err := MenuDigits(ctx, singleMenu("A"), nil, &Answer{
			Kind:  TextAnswerKind,
			Value: "one\ntwo",
		})
		Expect(err).To(BeNil())
		Expect(keys).To(Equal([]string{"2", "one / two", "\r"}))
	})

	It("releases the gate with a lone ESC when every question was skipped", func() {
		menu := []MenuQuestion{{Tab: "A", Labels: []string{"x"}}, {Tab: "B", Labels: []string{"y"}}}
		keys, err := MenuDigits(ctx, menu, Answers{
			{Question: "A", Answer: Answer{Kind: SkipAnswerKind}},
			{Question: "B", Answer: Answer{Kind: SkipAnswerKind}},
		}, nil)
		Expect(err).To(BeNil())
		Expect(keys).To(Equal([]string{menuCancel}))
	})

	It("tabs past a question the operator left alone on a multi-question menu", func() {
		menu := []MenuQuestion{{Tab: "A", Labels: []string{"x"}}, {Tab: "B", Labels: []string{"y"}}}
		keys, err := MenuDigits(ctx, menu, Answers{
			{Question: "A", Answer: Answer{Kind: OptionAnswerKind, Value: "x"}},
		}, nil)
		Expect(err).To(BeNil())
		Expect(keys).To(Equal([]string{"1", "\t"}))
	})

	It("refuses when every question carries no answer at all", func() {
		_, err := MenuDigits(ctx, singleMenu("A"), nil, nil)
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("nothing to deliver"))
	})

	It("refuses a free-text answer that is empty", func() {
		_, err := MenuDigits(ctx, singleMenu("A"), nil, &Answer{Kind: TextAnswerKind})
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("free-text answer is empty"))
	})

	It("refuses a free-text row with no single digit", func() {
		labels := make([]string, 9)
		for i := range labels {
			labels[i] = string(rune('a' + i))
		}
		_, err := MenuDigits(ctx, singleMenu(labels...), nil, &Answer{
			Kind:  TextAnswerKind,
			Value: "x",
		})
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("no single digit"))
	})

	It("refuses an option above the ninth", func() {
		labels := make([]string, 10)
		for i := range labels {
			labels[i] = string(rune('a' + i))
		}
		_, err := MenuDigits(ctx, singleMenu(labels...), nil, &Answer{
			Kind:  OptionAnswerKind,
			Value: "j",
		})
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("no single digit"))
	})

	It("refuses a label the question does not offer", func() {
		_, err := MenuDigits(ctx, singleMenu("A"), nil, &Answer{
			Kind:  OptionAnswerKind,
			Value: "Z",
		})
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("not one of its options"))
	})

	It("refuses a single-select question answered with several options", func() {
		_, err := MenuDigits(ctx, singleMenu("A", "B"), nil, &Answer{
			Kind:   OptionAnswerKind,
			Values: []string{"A", "B"},
		})
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("single-select"))
	})

	It("refuses an answer kind that is not keyed", func() {
		_, err := MenuDigits(ctx, singleMenu("A"), nil, &Answer{Kind: HiddenAnswerKind})
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("not keyed"))
	})
})
