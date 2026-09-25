// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The attention item schema's silence 12 added three fields — `options`,
// `context` and `answer` — so that a `message` item is answerable on a surface
// that is not the asker's own tab. These specs cover the rules the schema
// states for them: options are message-only and carry at most one
// recommendation, and the answer's value must match the shape its kind
// describes.
var _ = Describe("Board answers", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// boltkv, not memorykv, for the same reason the store suite uses it: the
		// answer path's compare-and-set is atomic only because the backend
		// serializes write transactions.
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
		Expect(db.Close()).To(BeNil())
	})

	// messageRequest is a question with options — the class `options` exists for.
	messageRequest := func() pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "question-1",
			InterruptClass:  "pick",
			Payload:         "Which surface should the answer land on?",
			Context:         "The board is the store's own page, so answering there needs no second tab.",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options: pkg.AnswerOptions{
				{Label: "the board", Recommended: true},
				{Label: "the tab"},
			},
		}
	}

	Describe("Options and context", func() {
		It("round-trips the options and the context through push and get", func() {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Options).To(HaveLen(2))
			Expect(got.Options[0].Label).To(Equal("the board"))
			Expect(got.Context).To(Equal(pkg.ItemContext(
				"The board is the store's own page, so answering there needs no second tab.",
			)))
		})

		// Negative control for the spec above: if the store dropped Recommended
		// on the way through, a list of two labels would still pass. The
		// recommendation is the part the board renders differently.
		It("preserves which option is recommended", func() {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Options[0].Recommended).To(BeTrue())
			Expect(got.Options[1].Recommended).To(BeFalse())
		})

		It("accepts a message item that declares no options", func() {
			request := messageRequest()
			request.Options = nil
			request.Context = ""
			Expect(request.Validate(ctx)).To(BeNil())

			item, err := store.Push(ctx, request)
			Expect(err).To(BeNil())
			Expect(item.Options).To(BeEmpty())
		})

		It("rejects options on a permission item", func() {
			request := messageRequest()
			request.AnswerMechanism = pkg.PermissionAnswerMechanism

			err := request.Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})

		It("rejects options on an ack item", func() {
			request := messageRequest()
			request.AnswerMechanism = pkg.AckAnswerMechanism

			err := request.Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})

		It("rejects two recommended options", func() {
			request := messageRequest()
			request.Options[1].Recommended = true

			err := request.Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})

		It("rejects an option carrying no label", func() {
			request := messageRequest()
			request.Options[1].Label = ""

			err := request.Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})
	})

	Describe("Answer content", func() {
		pushQuestion := func() *pkg.Item {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			return item
		}

		It("stores the chosen option's label", func() {
			item := pushQuestion()

			answered, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", &pkg.Answer{
				Kind:  pkg.OptionAnswerKind,
				Value: "the board",
			}, nil)
			Expect(err).To(BeNil())
			Expect(answered.Answer).NotTo(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.AnsweredState))
			Expect(got.Answer.Kind).To(Equal(pkg.OptionAnswerKind))
			Expect(got.Answer.Value).To(Equal("the board"))
		})

		// Negative control: a skip and an option are the same record unless the
		// kind is carried. Answering "skip" must not read back as the option.
		It("stores a skip with no value", func() {
			item := pushQuestion()

			_, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", &pkg.Answer{
				Kind: pkg.SkipAnswerKind,
			}, nil)
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Answer.Kind).To(Equal(pkg.SkipAnswerKind))
			Expect(got.Answer.Value).To(BeEmpty())
		})

		It("stores free text", func() {
			item := pushQuestion()

			_, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", &pkg.Answer{
				Kind:  pkg.TextAnswerKind,
				Value: "neither — put it in the notification core",
			}, nil)
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Answer.Kind).To(Equal(pkg.TextAnswerKind))
			Expect(got.Answer.Value).To(Equal("neither — put it in the notification core"))
		})

		It("leaves the answer absent when the caller omits one", func() {
			item := pushQuestion()

			_, err := store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil)
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Answer).To(BeNil())
		})

		It("rejects an option answer carrying no value", func() {
			err := (&pkg.Answer{Kind: pkg.OptionAnswerKind}).Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})

		It("rejects a skip carrying a value", func() {
			err := (&pkg.Answer{Kind: pkg.SkipAnswerKind, Value: "the board"}).Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})

		It("rejects an unknown kind", func() {
			err := (&pkg.Answer{Kind: pkg.AnswerKind("bogus"), Value: "x"}).Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})

		It("rejects a second answer to the same item", func() {
			item := pushQuestion()

			_, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", &pkg.Answer{
				Kind:  pkg.OptionAnswerKind,
				Value: "the board",
			}, nil)
			Expect(err).To(BeNil())

			_, err = store.Answer(ctx, item.ItemID, "telegram", "", "", &pkg.Answer{
				Kind:  pkg.OptionAnswerKind,
				Value: "the tab",
			}, nil)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrAlreadyAnswered)).To(BeTrue())

			// The loser must not have overwritten the winner's answer.
			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Answer.Value).To(Equal("the board"))
		})
	})
})
