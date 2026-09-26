// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The attention item schema's silences 14-16 added a per-option `description`,
// a declared `answer_cardinality`, and the `questions`/`answers` pair that makes
// one item able to ask several things at once. These specs cover the rules the
// schema states for them: cardinality is a closed vocabulary that is declared
// and never derived, two questions may not share a tab, one question has one
// answer, and the two answer fields are mutually exclusive.
var _ = Describe("Questions and cardinality", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// boltkv, not memorykv, for the same reason the store suite uses it: the
		// write path is atomic only because the backend serializes write
		// transactions.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		if db != nil {
			Expect(db.Close()).To(BeNil())
		}
	})

	// pushRequest builds a valid `message` declaration that each spec then
	// mutates, so a failing assertion is about the field under test rather than
	// about a missing required value.
	pushRequest := func(dedupKey string) pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      "producer-card",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-card"),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "ask",
			Payload:         "Which chores should I queue?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
	}

	Describe("AnswerCardinality", func() {
		It("accepts both declared values", func() {
			Expect(pkg.SingleAnswerCardinality.Validate(ctx)).To(BeNil())
			Expect(pkg.MultipleAnswerCardinality.Validate(ctx)).To(BeNil())
		})

		It("accepts an absent value, which reads as single", func() {
			Expect(pkg.AnswerCardinality("").Validate(ctx)).To(BeNil())
		})

		It("rejects an undeclared value", func() {
			Expect(pkg.AnswerCardinality("sometimes").Validate(ctx)).NotTo(BeNil())
		})
	})

	Describe("Questions", func() {
		It("accepts an empty list, which is what a single-question item carries", func() {
			Expect(pkg.Questions{}.Validate(ctx)).To(BeNil())
		})

		It("accepts questions with distinct tabs", func() {
			Expect(pkg.Questions{
				{
					Tab:         "Chores",
					Payload:     "Which chores?",
					Cardinality: pkg.MultipleAnswerCardinality,
				},
				{Tab: "Priority", Payload: "How urgent?"},
			}.Validate(ctx)).To(BeNil())
		})

		It(
			"rejects two questions sharing a tab, because an answer names its question by tab",
			func() {
				err := pkg.Questions{
					{Tab: "Chores", Payload: "Which chores?"},
					{Tab: "Chores", Payload: "Which chores again?"},
				}.Validate(ctx)
				Expect(err).NotTo(BeNil())
				Expect(err.Error()).To(ContainSubstring("Chores"))
			},
		)

		It("rejects a question carrying no tab or no payload", func() {
			Expect(pkg.Questions{{Payload: "Which chores?"}}.Validate(ctx)).NotTo(BeNil())
			Expect(pkg.Questions{{Tab: "Chores"}}.Validate(ctx)).NotTo(BeNil())
		})
	})

	Describe("Answers", func() {
		It("accepts an empty list", func() {
			Expect(pkg.Answers{}.Validate(ctx)).To(BeNil())
		})

		It(
			"rejects two entries answering the same question, because one question has one answer",
			func() {
				err := pkg.Answers{
					{
						Question: "Chores",
						Answer:   pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "a"},
					},
					{
						Question: "Chores",
						Answer:   pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "b"},
					},
				}.Validate(ctx)
				Expect(err).NotTo(BeNil())
				Expect(err.Error()).To(ContainSubstring("Chores"))
			},
		)
	})

	Describe("Push", func() {
		It("round-trips option descriptions, the cardinality and several questions", func() {
			request := pushRequest("card-round-trip")
			request.AnswerCardinality = pkg.MultipleAnswerCardinality
			request.Questions = pkg.Questions{
				{
					Tab:         "Chores",
					Payload:     "Which vault-cleanup chores should I queue?",
					Cardinality: pkg.MultipleAnswerCardinality,
					Options: pkg.AnswerOptions{
						{
							Label:       "Broken wikilinks",
							Description: "Scan 50 Knowledge Base + 65 Runbooks. ~20 min, low risk.",
							Recommended: true,
						},
						{Label: "Huge pages", Description: "Split anything over 300 lines."},
					},
				},
				{
					Tab:     "Priority",
					Payload: "How urgent is this?",
					Options: pkg.AnswerOptions{{Label: "This week"}},
				},
			}

			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			read, err := store.Get(ctx, pushed.ItemID)
			Expect(err).To(BeNil())
			Expect(read.AnswerCardinality).To(Equal(pkg.MultipleAnswerCardinality))
			Expect(read.Questions).To(HaveLen(2))
			Expect(read.Questions[0].Tab).To(Equal("Chores"))
			Expect(read.Questions[0].Cardinality).To(Equal(pkg.MultipleAnswerCardinality))
			Expect(read.Questions[0].Options[0].Description).To(ContainSubstring("low risk"))
			Expect(read.Questions[0].Options[0].Recommended).To(BeTrue())
			Expect(read.Questions[1].Tab).To(Equal("Priority"))
		})

		It("rejects an out-of-range cardinality", func() {
			request := pushRequest("card-out-of-range")
			request.AnswerCardinality = pkg.AnswerCardinality("sometimes")
			_, err := store.Push(ctx, request)
			Expect(err).NotTo(BeNil())
		})

		It("rejects questions on a permission item", func() {
			request := pushRequest("card-permission-questions")
			request.AnswerMechanism = pkg.PermissionAnswerMechanism
			request.Questions = pkg.Questions{{Tab: "Chores", Payload: "Which chores?"}}
			_, err := store.Push(ctx, request)
			Expect(err).NotTo(BeNil())
		})

		It("rejects a cardinality on a permission item", func() {
			request := pushRequest("card-permission-cardinality")
			request.AnswerMechanism = pkg.PermissionAnswerMechanism
			request.AnswerCardinality = pkg.MultipleAnswerCardinality
			_, err := store.Push(ctx, request)
			Expect(err).NotTo(BeNil())
		})

		It("rejects options on a permission item", func() {
			request := pushRequest("card-permission-options")
			request.AnswerMechanism = pkg.PermissionAnswerMechanism
			request.Options = pkg.AnswerOptions{{Label: "Allow"}}
			_, err := store.Push(ctx, request)
			Expect(err).NotTo(BeNil())
		})

		It("rejects an answer naming a question the item does not carry", func() {
			request := pushRequest("card-unknown-question")
			request.Questions = pkg.Questions{{Tab: "Chores", Payload: "Which chores?"}}
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			_, err = store.Answer(ctx, pushed.ItemID, "attention-board", "", "", nil, pkg.Answers{
				{Question: "Nonsense", Answer: pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "x"}},
			}, nil)
			Expect(err).NotTo(BeNil())
		})

		It("stores per-question answers and keeps the single answer field absent", func() {
			request := pushRequest("card-answers-stored")
			request.Questions = pkg.Questions{
				{
					Tab:         "Chores",
					Payload:     "Which chores?",
					Cardinality: pkg.MultipleAnswerCardinality,
				},
				{Tab: "Priority", Payload: "How urgent?"},
			}
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			answered, err := store.Answer(
				ctx,
				pushed.ItemID,
				"attention-board",
				"",
				"",
				nil,
				pkg.Answers{
					{
						Question: "Chores",
						Answer: pkg.Answer{
							Kind:   pkg.OptionAnswerKind,
							Values: []string{"Broken wikilinks", "Huge pages"},
						},
					},
					{Question: "Priority", Answer: pkg.Answer{Kind: pkg.SkipAnswerKind}},
				}, nil)
			Expect(err).To(BeNil())
			Expect(answered.State).To(Equal(pkg.AnsweredState))
			Expect(answered.Answers).To(HaveLen(2))
			Expect(answered.Answers[0].Question).To(Equal("Chores"))
			// A multi-pick question's labels ride in values, so two picks are two
			// labels rather than one string a reader would have to split — which is
			// what makes the encoding reversible.
			Expect(answered.Answers[0].Values).
				To(Equal([]string{"Broken wikilinks", "Huge pages"}))
			Expect(answered.Answers[0].Value).To(BeEmpty())
			Expect(answered.Answers[1].Kind).To(Equal(pkg.SkipAnswerKind))
			// The single-answer field stays absent on a multi-question item, so a
			// reader never has to guess which of the two holds the content.
			Expect(answered.Answer).To(BeNil())
		})

		It("rejects value on a question that takes several picks", func() {
			request := pushRequest("card-multiple-needs-values")
			request.Questions = pkg.Questions{
				{
					Tab:         "Chores",
					Payload:     "Which chores?",
					Cardinality: pkg.MultipleAnswerCardinality,
				},
			}
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			_, err = store.Answer(ctx, pushed.ItemID, "attention-board", "", "", nil, pkg.Answers{
				{
					Question: "Chores",
					Answer:   pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Broken wikilinks"},
				},
			}, nil)
			Expect(err).NotTo(BeNil())
		})

		It("rejects values on a question that takes one pick", func() {
			request := pushRequest("card-single-needs-value")
			request.Questions = pkg.Questions{
				{Tab: "Chores", Payload: "Which chores?", Cardinality: pkg.SingleAnswerCardinality},
			}
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			_, err = store.Answer(ctx, pushed.ItemID, "attention-board", "", "", nil, pkg.Answers{
				{
					Question: "Chores",
					Answer: pkg.Answer{
						Kind:   pkg.OptionAnswerKind,
						Values: []string{"Broken wikilinks"},
					},
				},
			}, nil)
			Expect(err).NotTo(BeNil())
		})

		It("stores several picks on a single-question item declared multiple", func() {
			// The case a card with checkboxes and no tabs produces: the item is
			// not multi-question, so the answer rides the item-level field rather
			// than an answers entry, and it still has to carry every pick. Before
			// values lived on Answer this was unrepresentable, and the board sent
			// a body the store rejected — the card promised a multi-pick it could
			// not deliver.
			request := pushRequest("card-single-question-multiple")
			request.AnswerCardinality = pkg.MultipleAnswerCardinality
			request.Options = pkg.AnswerOptions{{Label: "a"}, {Label: "b"}}
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			answered, err := store.Answer(
				ctx,
				pushed.ItemID,
				"attention-board",
				"",
				"",
				&pkg.Answer{Kind: pkg.OptionAnswerKind, Values: []string{"a", "b"}},
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			Expect(answered.Answer).NotTo(BeNil())
			Expect(answered.Answer.Values).To(Equal([]string{"a", "b"}))
			Expect(answered.Answer.Value).To(BeEmpty())
		})

		It("rejects a single value on a single-question item declared multiple", func() {
			request := pushRequest("card-single-question-multiple-value")
			request.AnswerCardinality = pkg.MultipleAnswerCardinality
			request.Options = pkg.AnswerOptions{{Label: "a"}}
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			_, err = store.Answer(
				ctx,
				pushed.ItemID,
				"attention-board",
				"",
				"",
				&pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "a"},
				nil,
				nil,
			)
			Expect(err).NotTo(BeNil())
		})

		It("rejects the single answer field on an item carrying questions", func() {
			// The mutual exclusion runs both ways. Accepting `answer` here would
			// store content with no tab attached, so the producer reading
			// `answers` back would find nothing for any question and the routing
			// the questions exist to provide would be gone.
			request := pushRequest("card-questions-need-answers")
			request.Questions = pkg.Questions{{Tab: "Chores", Payload: "Which chores?"}}
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			_, err = store.Answer(
				ctx,
				pushed.ItemID,
				"attention-board",
				"",
				"",
				&pkg.Answer{Kind: pkg.SkipAnswerKind},
				nil,
				nil,
			)
			Expect(err).NotTo(BeNil())
		})

		It("rejects an answer carrying both value and values", func() {
			err := pkg.Answers{
				{
					Question: "Chores",
					Answer: pkg.Answer{
						Kind:   pkg.OptionAnswerKind,
						Value:  "a",
						Values: []string{"b"},
					},
				},
			}.Validate(ctx)
			Expect(err).NotTo(BeNil())
		})

		It("rejects values on a skip or a text answer, which have no labels to list", func() {
			Expect(pkg.Answers{
				{
					Question: "Chores",
					Answer: pkg.Answer{
						Kind:   pkg.SkipAnswerKind,
						Values: []string{"a"},
					},
				},
			}.Validate(ctx)).NotTo(BeNil())
			Expect(pkg.Answers{
				{
					Question: "Chores",
					Answer: pkg.Answer{
						Kind:   pkg.TextAnswerKind,
						Values: []string{"a"},
					},
				},
			}.Validate(ctx)).NotTo(BeNil())
		})

		It("rejects a call carrying both answer and answers", func() {
			request := pushRequest("card-both-answer-fields")
			request.Questions = pkg.Questions{{Tab: "Chores", Payload: "Which chores?"}}
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			_, err = store.Answer(
				ctx,
				pushed.ItemID,
				"attention-board",
				"",
				"",
				&pkg.Answer{Kind: pkg.SkipAnswerKind},
				pkg.Answers{
					{Question: "Chores", Answer: pkg.Answer{Kind: pkg.SkipAnswerKind}},
				},
				nil,
			)
			Expect(err).NotTo(BeNil())
		})

		It("still answers a single-question item through the unchanged answer field", func() {
			request := pushRequest("card-single-question-unchanged")
			request.Options = pkg.AnswerOptions{
				{Label: "Yes", Description: "Do it now.", Recommended: true},
				{Label: "No", Description: "Skip it."},
			}
			request.AnswerCardinality = pkg.SingleAnswerCardinality
			pushed, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			answered, err := store.Answer(
				ctx,
				pushed.ItemID,
				"attention-board",
				"",
				"",
				&pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"},
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			Expect(answered.Answer).NotTo(BeNil())
			Expect(answered.Answer.Value).To(Equal("Yes"))
			Expect(answered.Answers).To(BeEmpty())
			Expect(answered.Options[0].Description).To(Equal("Do it now."))
		})
	})
})
