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

var _ = Describe("Interrupt class normalisation at the mint path", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
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

	// push fills the fields every producer must supply and returns the minted
	// item, so each case declares only the fields under test.
	push := func(request pkg.PushRequest) *pkg.Item {
		request.ProducerID = pkg.ProducerID("producer-" + request.DedupKey.String())
		request.ProducerKind = pkg.SessionProducerKind
		request.LivenessRef = pkg.LivenessRef("session:producer-" + request.DedupKey.String())
		request.Payload = "a question"
		if request.AnswerMechanism == "" {
			request.AnswerMechanism = pkg.MessageAnswerMechanism
		}
		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		return item
	}

	It("normalises a pick card with no options to a non-pick class", func() {
		item := push(pkg.PushRequest{
			DedupKey:       "park:12345678",
			InterruptClass: pkg.PickInterruptClass,
		})
		Expect(item.InterruptClass).NotTo(Equal(pkg.PickInterruptClass),
			"a pick card with nothing to pick must not keep the pick class")

		stored, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(stored.InterruptClass).To(Equal(pkg.AckInterruptClass),
			"the stored item, not only the returned one, must carry the normalised class")
	})

	It("keeps the pick class when the card offers options", func() {
		item := push(pkg.PushRequest{
			DedupKey:       "pick-with-options",
			InterruptClass: pkg.PickInterruptClass,
			Options: pkg.AnswerOptions{
				{Label: "Yes"},
				{Label: "No"},
			},
		})
		Expect(item.InterruptClass).To(Equal(pkg.PickInterruptClass))
	})

	It("keeps the pick class when a per-question option set is present", func() {
		item := push(pkg.PushRequest{
			DedupKey:       "pick-with-question-options",
			InterruptClass: pkg.PickInterruptClass,
			Questions: pkg.Questions{
				{
					Tab:     "one",
					Payload: "which?",
					Options: pkg.AnswerOptions{{Label: "Yes"}, {Label: "No"}},
				},
			},
		})
		Expect(item.InterruptClass).To(Equal(pkg.PickInterruptClass))
	})
})
