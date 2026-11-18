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
		Expect(answered.EvidenceClaims).To(Equal([]string{"https://github.com/bborbe/attention-controller/pull/77"}))
	})

	It("closes the item once the claimed pull request is merged", func() {
		item := pushQuestion()
		answerWith(item.ItemID, &Answer{
			Kind:  TextAnswerKind,
			Value: "done https://github.com/bborbe/attention-controller/pull/77",
		})

		original := store.evidenceRunner
		DeferCleanup(func() { store.evidenceRunner = original })
		store.evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
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

		original := store.evidenceRunner
		DeferCleanup(func() { store.evidenceRunner = original })
		store.evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			return "OPEN", nil
		}

		now = now.Add(libtime.Duration(EvidenceDeadline))
		_, err := store.SweepAnswered(ctx, libtime.Duration(15*60*1e9))
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(OpenState))
		Expect(got.EvidenceDeadline).To(BeNil())
		Expect(got.EvidenceClaims).NotTo(BeEmpty(), "the claim survives so the card can say what it waits for")
		// The card must actually reappear: a leftover answer stamp keeps the ask
		// in the durable answered index, which makes the board suppress the
		// reopened card and the re-raise sweep close it.
		Expect(got.AnsweredAt).To(BeNil(), "a reopened card is not answered")
		Expect(got.AnsweredBy).To(BeEmpty())
		Expect(got.Answer).To(BeNil())
		// ⚠️ The assertion above reads the item bucket, which bypasses every read
		// filter. The reopen only counts if the BOARD shows the card again — the
		// durable answered index is add-only, so a leftover entry makes
		// suppressAnsweredTwins hide it while the item still reads `open`.
		board, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		visible := false
		for _, candidate := range board {
			if candidate.ItemID == item.ItemID {
				visible = true
			}
		}
		Expect(visible).To(BeTrue(), "a reopened card must reappear on the board")
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

		original := store.evidenceRunner
		DeferCleanup(func() { store.evidenceRunner = original })
		store.evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			return "OPEN", nil
		}

		_, err := store.SweepAnswered(ctx, libtime.Duration(15*60*1e9))
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(AwaitingEvidenceState))
	})

	It("treats a failed probe as unobserved rather than verified", func() {
		original := store.evidenceRunner
		DeferCleanup(func() { store.evidenceRunner = original })
		store.evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			return "", errors.New("gh: not logged in")
		}

		Expect(EvidenceObserved(ctx, "https://github.com/bborbe/attention-controller/pull/77", store.evidenceRunner)).To(BeFalse())
	})

	It("never guesses an artifact from an answer that named none", func() {
		Expect(EvidenceClaimsFromText("done — reviewed and flipped both")).To(BeEmpty())
		Expect(EvidenceClaimsFromText("/etc/hosts")).To(Equal([]string{"/etc/hosts"}))
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "Done"})).To(BeTrue())
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "not yet"})).To(BeFalse())
	})

	// The two blocking review findings, pinned: a closed-unmerged PR is not
	// proof, and a two-artifact answer needs both artifacts.
	It("does not accept a closed-unmerged pull request as proof", func() {
		original := store.evidenceRunner
		DeferCleanup(func() { store.evidenceRunner = original })
		store.evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			return "CLOSED", nil
		}

		Expect(EvidenceObserved(ctx, "https://github.com/bborbe/attention-controller/pull/77", store.evidenceRunner)).To(BeFalse())
	})

	It("records every artifact a two-pull-request answer names", func() {
		item := pushQuestion()

		answered := answerWith(item.ItemID, &Answer{
			Kind:  TextAnswerKind,
			Value: "done — reviewed and flipped both https://github.com/bborbe/attention-controller/pull/77 https://github.com/bborbe/attention-controller/pull/80",
		})

		Expect(answered.State).To(Equal(AwaitingEvidenceState))
		Expect(answered.EvidenceClaims).To(HaveLen(2))
	})

	It("keeps the item parked while only one of two claimed artifacts is observed", func() {
		item := pushQuestion()
		answerWith(item.ItemID, &Answer{
			Kind:  TextAnswerKind,
			Value: "done https://github.com/bborbe/attention-controller/pull/77 https://github.com/bborbe/attention-controller/pull/80",
		})

		original := store.evidenceRunner
		DeferCleanup(func() { store.evidenceRunner = original })
		store.evidenceRunner = func(ctx context.Context, name string, args ...string) (string, error) {
			for _, arg := range args {
				if arg == "77" {
					return "MERGED", nil
				}
			}
			return "OPEN", nil
		}

		_, err := store.SweepAnswered(ctx, libtime.Duration(15*60*1e9))
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(AwaitingEvidenceState), "one merged PR is not the whole claim")
	})

	// The review's path findings, pinned: prose punctuation and a markdown link
	// both have to yield the path itself, or a real artifact never verifies.
	It("extracts a path from prose and from a markdown link", func() {
		Expect(EvidenceClaimsFromText("done, see /srv/app/build.log.")).To(Equal([]string{"/srv/app/build.log"}))
		Expect(EvidenceClaimsFromText("done [build](/srv/app/build.log)")).To(Equal([]string{"/srv/app/build.log"}))
	})

	// The option-label finding, pinned: a closed option set must not acquire
	// false positives from a bare prefix.
	It("parks a whole-word done option but not a label that merely starts with it", func() {
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "done — reviewed and flipped both"})).To(BeTrue())
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "done"})).To(BeTrue())
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "done for now"})).To(BeFalse())
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "deployed to staging"})).To(BeFalse())
		Expect(IsDoneClassAnswer(&Answer{Kind: OptionAnswerKind, Value: "fixed pending review"})).To(BeFalse())
		// Free text keeps the prefix rule that the motivating answer needs.
		Expect(IsDoneClassAnswer(&Answer{Kind: TextAnswerKind, Value: "done — reviewed and flipped both"})).To(BeTrue())
	})
})
