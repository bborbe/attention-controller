// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("Answerer", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	DescribeTable("Validate",
		func(answerer pkg.Answerer, wantErr bool) {
			err := answerer.Validate(ctx)
			if wantErr {
				Expect(err).To(HaveOccurred())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("operator", pkg.OperatorAnswerer, false),
		Entry("manager", pkg.ManagerAnswerer, false),
		Entry("absent", pkg.Answerer(""), false),
		Entry("unknown", pkg.Answerer("boss"), true),
	)

	It("rejects an answer carrying an unknown answerer", func() {
		answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "x", Answerer: "boss"}
		Expect(answer.Validate(ctx)).To(HaveOccurred())
	})

	It("round-trips the answerer through the answer's JSON", func() {
		var answer pkg.Answer
		Expect(json.Unmarshal(
			[]byte(`{"kind":"text","value":"x","answerer":"manager"}`), &answer,
		)).To(Succeed())
		Expect(answer.Answerer).To(Equal(pkg.ManagerAnswerer))
		out, err := json.Marshal(pkg.Answer{Kind: pkg.TextAnswerKind, Value: "x"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).NotTo(ContainSubstring("answerer"))
	})

	DescribeTable("ManagerAnswerText",
		func(text string, want string) {
			Expect(pkg.ManagerAnswerText(text)).To(Equal(want))
		},
		Entry("bare label", "Approve", "Manager answer, via supervisor: Approve"),
		Entry("already prefixed", "Manager answer, via supervisor: Approve",
			"Manager answer, via supervisor: Approve"),
	)

	DescribeTable("IsManagerAnswered",
		func(item pkg.Item, want bool) {
			Expect(pkg.IsManagerAnswered(item)).To(Equal(want))
		},
		Entry("no answer", pkg.Item{}, false),
		Entry(
			"operator answer",
			pkg.Item{
				Answer: &pkg.Answer{
					Kind:     pkg.TextAnswerKind,
					Value:    "x",
					Answerer: pkg.OperatorAnswerer,
				},
			},
			false,
		),
		Entry("legacy answer without answerer",
			pkg.Item{Answer: &pkg.Answer{Kind: pkg.TextAnswerKind, Value: "x"}}, false),
		Entry(
			"manager answer",
			pkg.Item{
				Answer: &pkg.Answer{
					Kind:     pkg.TextAnswerKind,
					Value:    "x",
					Answerer: pkg.ManagerAnswerer,
				},
			},
			true,
		),
		Entry("manager per-question answer",
			pkg.Item{Answers: pkg.Answers{{Question: "q", Answer: pkg.Answer{
				Kind: pkg.OptionAnswerKind, Value: "a", Answerer: pkg.ManagerAnswerer,
			}}}},
			true),
	)
})

var _ = Describe("AnswerTurnText", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("opens an operator answer in the operator's voice, unchanged", func() {
		item := pkg.Item{
			Payload: "Which one?",
			Answer: &pkg.Answer{
				Kind:     pkg.TextAnswerKind,
				Value:    "Approve",
				Answerer: pkg.OperatorAnswerer,
			},
		}
		text, ok := pkg.AnswerTurnText(ctx, item, pkg.AnswerContext{})
		Expect(ok).To(BeTrue())
		Expect(text).To(HavePrefix("The operator answered on the attention board.\n"))
		Expect(text).NotTo(ContainSubstring("Manager answer"))
	})

	It("renders a legacy answer with no answerer as the operator's", func() {
		item := pkg.Item{
			Payload: "Which one?",
			Answer:  &pkg.Answer{Kind: pkg.TextAnswerKind, Value: "Approve"},
		}
		text, ok := pkg.AnswerTurnText(ctx, item, pkg.AnswerContext{})
		Expect(ok).To(BeTrue())
		Expect(text).To(HavePrefix("The operator answered on the attention board.\n"))
	})

	It("opens a manager answer in the manager's voice with no operator envelope", func() {
		item := pkg.Item{
			Payload: "Which one?",
			Answer: &pkg.Answer{
				Kind:     pkg.TextAnswerKind,
				Value:    "Manager answer, via supervisor: Approve",
				Answerer: pkg.ManagerAnswerer,
			},
		}
		text, ok := pkg.AnswerTurnText(ctx, item, pkg.AnswerContext{})
		Expect(ok).To(BeTrue())
		Expect(text).To(HavePrefix("Manager answer, via supervisor: Approve\n"))
		Expect(strings.ToLower(text)).NotTo(ContainSubstring("the operator answered"))
		Expect(strings.Count(text, "Manager answer, via supervisor:")).To(BeNumerically(">=", 1))
		Expect(text).NotTo(ContainSubstring("Manager answer, via supervisor: Manager answer"))
	})
})
