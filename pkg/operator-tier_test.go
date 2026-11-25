// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The operator-tier rule: a message-class `approve`/`decision` card, or a `pick`
// the placement table rates operator, may be answered or closed only by an
// operator arm. These specs pin both routes, the operator set, the word list, and
// the exclusions (permission, ack, producer withdrawal).
var _ = Describe("operator-tier message card", func() {
	var ctx context.Context
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		DeferCleanup(func() {
			Expect(db.Close()).To(BeNil())
		})
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

	// push builds a message-class item with the given class, producer kind and
	// payload. The dedup key is unique per push so a suite can push several.
	push := func(
		class pkg.InterruptClass,
		kind pkg.ProducerKind,
		payload string,
		dedupKey pkg.DedupKey,
	) *pkg.Item {
		request := pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    kind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        dedupKey,
			InterruptClass:  class,
			Payload:         pkg.Payload(payload),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
		if class == pkg.PickInterruptClass {
			// A pick with no selectable option is normalised to `ack` at the mint
			// path, which is not operator-tier — so a real option keeps the card
			// a pick.
			request.Options = pkg.AnswerOptions{{Label: "yes"}, {Label: "no"}}
		}
		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		return item
	}

	pushApprove := func(dedupKey pkg.DedupKey) *pkg.Item {
		return push(pkg.ApproveInterruptClass, pkg.SessionProducerKind, "open the PR?", dedupKey)
	}

	expectOperatorTierRefusal := func(err error) {
		Expect(errors.Is(err, pkg.ErrOperatorTierLowerArm)).To(BeTrue())
		// The transition exists, so "illegal transition" would name the wrong
		// thing, and no answer was routed, so "already answered" would too.
		Expect(errors.Is(err, pkg.ErrIllegalTransition)).To(BeFalse())
		Expect(errors.Is(err, pkg.ErrAlreadyAnswered)).To(BeFalse())
	}

	Describe("close route", func() {
		It("refuses a manager closing an approve card and leaves it open", func() {
			item := pushApprove("close-manager")
			_, err := store.Close(ctx, item.ItemID, "manager", nil)
			expectOperatorTierRefusal(err)

			unchanged, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(unchanged.State).To(Equal(pkg.OpenState))
			Expect(unchanged.AnsweredBy).To(BeEmpty())
		})

		It("refuses the observed goal-manager breach arm", func() {
			item := pushApprove("close-goal-manager")
			_, err := store.Close(
				ctx,
				item.ItemID,
				"goal manager e408dc5c — moot: PR opened by manager after worker exited",
				nil,
			)
			expectOperatorTierRefusal(err)
		})

		It("refuses a controller-tier arm", func() {
			item := pushApprove("close-controller")
			_, err := store.Close(ctx, item.ItemID, "attention-watcher", nil)
			expectOperatorTierRefusal(err)
		})

		It("refuses a manager closing a decision card", func() {
			item := push(
				pkg.InterruptClass("decision"),
				pkg.SessionProducerKind,
				"merge it?",
				"close-decision",
			)
			_, err := store.Close(ctx, item.ItemID, "manager", nil)
			expectOperatorTierRefusal(err)
		})

		It("allows the board to close an approve card", func() {
			item := pushApprove("close-board")
			closed, err := store.Close(ctx, item.ItemID, "attention-board", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
			Expect(closed.AnsweredBy).To(Equal("attention-board"))
		})

		It("matches attention-board as a substring, mirroring the probe", func() {
			item := pushApprove("close-board-substring")
			closed, err := store.Close(ctx, item.ItemID, "board:attention-board:relay", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
		})

		It("allows a producer to withdraw its own card, which names no arm", func() {
			item := pushApprove("close-no-arm")
			closed, err := store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
			Expect(closed.AnsweredBy).To(BeEmpty())
		})

		It("does not govern an ack card", func() {
			item := push(
				pkg.AckInterruptClass,
				pkg.SessionProducerKind,
				"deploy prod?",
				"close-ack",
			)
			closed, err := store.Close(ctx, item.ItemID, "manager", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
		})
	})

	Describe("answer route", func() {
		It("refuses a manager answering an approve card and records nothing", func() {
			item := pushApprove("answer-manager")
			_, err := store.Answer(ctx, item.ItemID, "manager", "", "", nil, nil, nil)
			expectOperatorTierRefusal(err)

			unchanged, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(unchanged.AnsweredAt).To(BeNil())
			Expect(unchanged.AnsweredBy).To(BeEmpty())
		})

		It("refuses an armless answer, which names no operator", func() {
			item := pushApprove("answer-empty-arm")
			_, err := store.Answer(ctx, item.ItemID, "", "", "", nil, nil, nil)
			expectOperatorTierRefusal(err)
		})

		It("allows the operator arm", func() {
			item := pushApprove("answer-operator")
			answered, err := store.Answer(ctx, item.ItemID, "operator", "", "", nil, nil, nil)
			Expect(err).To(BeNil())
			Expect(answered.AnsweredBy).To(Equal("operator"))
		})

		It("allows the supervisor:attention-next prefix", func() {
			item := pushApprove("answer-attention-next")
			answered, err := store.Answer(
				ctx,
				item.ItemID,
				"supervisor:attention-next-abc",
				"",
				"",
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			Expect(answered.AnsweredBy).To(Equal("supervisor:attention-next-abc"))
		})
	})

	Describe("pick cards", func() {
		It("refuses a manager closing a session pick carrying an operator word", func() {
			item := push(
				pkg.PickInterruptClass,
				pkg.SessionProducerKind,
				"deploy the fix?",
				"pick-deploy",
			)
			_, err := store.Close(ctx, item.ItemID, "manager", nil)
			expectOperatorTierRefusal(err)
		})

		It("allows a manager closing a session pick with no operator word", func() {
			item := push(
				pkg.PickInterruptClass,
				pkg.SessionProducerKind,
				"which surface?",
				"pick-plain",
			)
			closed, err := store.Close(ctx, item.ItemID, "manager", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
		})

		It("refuses a manager answering a manager-produced pick, whatever its words", func() {
			item := push(
				pkg.PickInterruptClass,
				pkg.ManagerProducerKind,
				"which surface?",
				"pick-manager",
			)
			_, err := store.Answer(ctx, item.ItemID, "manager", "", "", nil, nil, nil)
			expectOperatorTierRefusal(err)
		})

		It("refuses the gate answerer on a session pick carrying an operator word", func() {
			item := push(
				pkg.PickInterruptClass,
				pkg.SessionProducerKind,
				"deploy the fix?",
				"pick-gate-answerer",
			)
			_, err := store.Answer(
				ctx,
				item.ItemID,
				"supervisor:gate-answerer",
				"",
				"",
				nil,
				nil,
				nil,
			)
			expectOperatorTierRefusal(err)
		})

		It("matches every operator word in the table", func() {
			for index, word := range pkg.OperatorTierWords {
				// The pattern is word-bounded, so `approve:` needs a word char after
				// the colon to close the trailing boundary; every other word is
				// followed by a space.
				payload := "please " + word + " now"
				if strings.HasSuffix(word, ":") {
					payload = "please " + word + "yes"
				}
				item := push(
					pkg.PickInterruptClass,
					pkg.SessionProducerKind,
					payload,
					pkg.DedupKey("word-"+word),
				)
				_, err := store.Close(ctx, item.ItemID, "manager", nil)
				Expect(errors.Is(err, pkg.ErrOperatorTierLowerArm)).To(
					BeTrue(),
					"operator word %d (%q) must be refused",
					index,
					word,
				)
			}
		})

		It("is case-insensitive and word-bounded, like the probe", func() {
			upper := push(
				pkg.PickInterruptClass,
				pkg.SessionProducerKind,
				"DEPLOY now",
				"case-upper",
			)
			_, err := store.Close(ctx, upper.ItemID, "manager", nil)
			Expect(errors.Is(err, pkg.ErrOperatorTierLowerArm)).To(BeTrue())

			substring := push(
				pkg.PickInterruptClass,
				pkg.SessionProducerKind,
				"redeploy now",
				"case-substring",
			)
			closed, err := store.Close(ctx, substring.ItemID, "manager", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
		})
	})

	Describe("exclusions", func() {
		It("does not govern a permission card", func() {
			item, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "session-a",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:session-a"),
				DedupKey:        "permission-gate",
				InterruptClass:  pkg.ApproveInterruptClass,
				Payload:         pkg.Payload("deploy prod?"),
				AnswerMechanism: pkg.PermissionAnswerMechanism,
			})
			Expect(err).To(BeNil())
			answered, err := store.Answer(
				ctx,
				item.ItemID,
				"supervisor:gate-answerer",
				"",
				pkg.AllowDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			Expect(answered.AnsweredBy).To(Equal("supervisor:gate-answerer"))
		})
	})
})
