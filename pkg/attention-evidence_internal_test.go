// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A `done` answer is a CLAIM. Measured 2026-11-13: a card was answered
// `done — reviewed and flipped both` and the two PRs it named were still open
// drafts with unchanged heads minutes later. These specs pin the state that
// keeps that from reading as satisfied.
var _ = Describe("Evidence-backed close", func() {
	var ctx context.Context
	var store *attentionStore
	var now libtime.DateTime

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		DeferCleanup(func() { Expect(db.Close()).To(BeNil()) })

		now = libtime.NewCurrentDateTime().Now()
		clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now })

		built, ok := NewAttentionStore(
			db,
			NewItemIDGenerator(),
			alwaysLiveChecker{},
			clock,
			libtime.Duration(15*60*1e9),
		).(*attentionStore)
		Expect(ok).To(BeTrue(), "the constructor must build the concrete store")
		store = built
	})

	pushQuestion := func() *Item {
		item, err := store.Push(ctx, PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    SessionProducerKind,
			LivenessRef:     LivenessRef("session:session-a"),
			DedupKey:        "question-evidence",
			InterruptClass:  "pick",
			Payload:         "Did the flip land?",
			AnswerMechanism: MessageAnswerMechanism,
			Options: AnswerOptions{
				{Label: "done — reviewed and flipped both", Recommended: true},
				{Label: "not yet"},
			},
		})
		Expect(err).To(BeNil())
		return item
	}

	answerWith := func(itemID ItemID, answer *Answer) *Item {
		answered, err := store.Answer(ctx, itemID, "attention-board", "", "", answer, nil, nil)
		Expect(err).To(BeNil())
		return answered
	}

	It("parks a done-class answer in awaiting_evidence instead of satisfied", func() {
		item := pushQuestion()

		answered := answerWith(item.ItemID, &Answer{
			Kind:  OptionAnswerKind,
			Value: "done — reviewed and flipped both",
		})

		Expect(answered.State).To(Equal(AwaitingEvidenceState))
		Expect(answered.AnsweredAt).NotTo(BeNil(), "the answer itself is still recorded")
		Expect(answered.EvidenceDeadline).NotTo(BeNil())
		Expect(answered.EvidenceDeadline.Time().Sub(now.Time())).To(Equal(time.Duration(EvidenceDeadline)))
	})

	// Negative control: without this, "parked" could be the state every answer
	// reaches and the specs above would pass on a store that parks everything.
	It("still answers an ordinary answer immediately", func() {
		item := pushQuestion()

		answered := answerWith(item.ItemID, &Answer{
			Kind:  OptionAnswerKind,
			Value: "not yet",
		})

		Expect(answered.State).To(Equal(AnsweredState))
		Expect(answered.EvidenceDeadline).To(BeNil())
	})

	It("records the pull request a text answer names", func() {
		item := pushQuestion()

		answered := answerWith(item.ItemID, &Answer{
			Kind:  TextAnswerKind,
			Value: "done, see https://github.com/bborbe/attention-controller/pull/77",
		})

		Expect(answered.State).To(Equal(AwaitingEvidenceState))
		Expect(answered.EvidenceClaim).To(Equal("https://github.com/bborbe/attention-controller/pull/77"))
	})

	It("closes the item once the claimed pull request is merged", func() {
		item := pushQuestion()
		answerWith(item.ItemID, &Answer{
			Kind:  TextAnswerKind,
			Value: "done https://github.com/bborbe/attention-controller/pull/77",
		})

		original := evidenceRunner
		DeferCleanup(func() { evidenceRunner = original })
		evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			return "MERGED", nil
		}

		_, err := store.SweepAnswered(ctx, libtime.Duration(15*60*1e9))
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(AnsweredState))
		Expect(got.EvidenceDeadline).To(BeNil())
	})

	It("reopens the item on the board when the deadline passes unobserved", func() {
		item := pushQuestion()
		answerWith(item.ItemID, &Answer{
			Kind:  TextAnswerKind,
			Value: "done https://github.com/bborbe/attention-controller/pull/77",
		})

		original := evidenceRunner
		DeferCleanup(func() { evidenceRunner = original })
		evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			return "OPEN", nil
		}

		now = now.Add(libtime.Duration(EvidenceDeadline))
		_, err := store.SweepAnswered(ctx, libtime.Duration(15*60*1e9))
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(OpenState))
		Expect(got.EvidenceDeadline).To(BeNil())
		Expect(got.EvidenceClaim).NotTo(BeEmpty(), "the claim survives so the card can say what it waits for")
		Expect(string(got.Context)).To(ContainSubstring(EvidenceNote))
	})

	// The window matters: a sweep that reopened everything parked would pass the
	// spec above and destroy the feature. This is the absence half's positive
	// control.
	It("leaves a parked item alone while its deadline is still in the future", func() {
		item := pushQuestion()
		answerWith(item.ItemID, &Answer{
			Kind:  TextAnswerKind,
			Value: "done https://github.com/bborbe/attention-controller/pull/77",
		})

		original := evidenceRunner
		DeferCleanup(func() { evidenceRunner = original })
		evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			return "OPEN", nil
		}

		_, err := store.SweepAnswered(ctx, libtime.Duration(15*60*1e9))
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(AwaitingEvidenceState))
	})

	It("treats a failed probe as unobserved rather than verified", func() {
		original := evidenceRunner
		DeferCleanup(func() { evidenceRunner = original })
		evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			return "", errors.New("gh: not logged in")
		}

		Expect(EvidenceObserved(ctx, "https://github.com/bborbe/attention-controller/pull/77", evidenceRunner)).To(BeFalse())
	})

	It("never guesses an artifact from an answer that named none", func() {
		Expect(EvidenceClaimFromText("done — reviewed and flipped both")).To(BeEmpty())
		Expect(EvidenceClaimFromText("/etc/hosts")).To(Equal("/etc/hosts"))
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "Done"})).To(BeTrue())
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "not yet"})).To(BeFalse())
	})
})
