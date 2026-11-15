// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// fakeMenuWriter is a scripted PaneTextWriter: `Text` replays a queue (the last
// entry repeats) and `SendText` records every key.
type fakeMenuWriter struct {
	texts []string
	index int
	sent  []string
}

func (w *fakeMenuWriter) Activate(_ context.Context, _ string) error { return nil }

func (w *fakeMenuWriter) SendText(_ context.Context, _ int, text string) error {
	w.sent = append(w.sent, text)
	return nil
}

func (w *fakeMenuWriter) ComposerState(_ context.Context, _ int) pkg.ComposerState {
	return pkg.ComposerUnknown
}

func (w *fakeMenuWriter) Text(_ context.Context, _ int) (string, bool) {
	if len(w.texts) == 0 {
		return "", true
	}
	text := w.texts[w.index]
	if w.index < len(w.texts)-1 {
		w.index++
	}
	return text, true
}

func (w *fakeMenuWriter) PaneTTY(_ context.Context, _ int) string { return "" }

func (w *fakeMenuWriter) ListPanes(_ context.Context) (map[int]pkg.Pane, error) {
	return map[int]pkg.Pane{}, nil
}

func (w *fakeMenuWriter) SessionTTY(_ context.Context, _ string) string { return "" }

func (w *fakeMenuWriter) SessionEntry(_ context.Context, _ string) (pkg.SessionEntry, bool) {
	return pkg.SessionEntry{}, false
}

// fakeLocator is a scripted PaneLocator.
type fakeLocator struct {
	verdict pkg.DeliveryVerdict
	pane    int
	reason  string
}

func (l *fakeLocator) Locate(
	_ context.Context,
	_ *pkg.PaneDelivery,
) (pkg.DeliveryVerdict, int, string) {
	return l.verdict, l.pane, l.reason
}

// answeredMenuItem builds an answered hook menu card.
func answeredMenuItem(id string, questions pkg.Questions) pkg.Item {
	answeredAt := libtime.NewCurrentDateTime().Now()
	return pkg.Item{
		ItemID:          pkg.ItemID(id),
		LivenessRef:     pkg.LivenessRef("session:11111111-2222-3333-4444-555555555555"),
		ProvenanceClass: pkg.HookProvenanceClass,
		AnswerMechanism: pkg.MessageAnswerMechanism,
		Questions:       questions,
		State:           pkg.AnsweredState,
		AnsweredAt:      &answeredAt,
		AnsweredBy:      "operator",
	}
}

var _ = Describe("menu answer job", func() {
	var (
		store   *mocks.AttentionStore
		locator *fakeLocator
		writer  *fakeMenuWriter
		job     pkg.MenuAnswerJob
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		locator = &fakeLocator{verdict: pkg.DeliveryOK, pane: 42}
		writer = &fakeMenuWriter{}
		// Zero every sleep: the loop shape is what these specs pin, not its pace.
		job = pkg.NewMenuAnswerJob(
			store, locator, writer, time.Hour,
			pkg.WithMenuAnswerTimings(0, 2),
		)
	})

	It("picks the option and records delivered once the menu closes", func() {
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}, {Label: "No"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{
			"pick one\nEnter to select", // the open menu
			"working\n",                 // the menu closed
		}
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(Equal([]string{"1"}))
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, itemID, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(itemID).To(Equal(pkg.ItemID("item-1")))
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("submits a multi-question review screen with the row's own digit", func() {
		item := answeredMenuItem("item-1", pkg.Questions{
			{Tab: "A", Options: pkg.AnswerOptions{{Label: "x"}}},
			{Tab: "B", Options: pkg.AnswerOptions{{Label: "y"}}},
		})
		item.Answers = pkg.Answers{
			{Question: "A", Answer: pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "x"}},
			{Question: "B", Answer: pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "y"}},
		}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{
			"menu\nEnter to select",
			"Review your answers\n3. Submit answers",
			"done\n",
		}
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		// Two option digits, then the submit row's own digit.
		Expect(writer.sent).To(Equal([]string{"1", "1", "3"}))
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
	})

	It("retries without recording when the pane shows no menu", func() {
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{"an ordinary prompt\n"}
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("records failed when the answer cannot be keyed", func() {
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Z"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
	})

	It("retries without recording when the menu never closes", func() {
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{"menu\nEnter to select"}
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("skips an item a prior attempt already delivered", func() {
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		store.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{
			"item-1": {ItemID: "item-1", Outcome: pkg.DeliveredOutcome},
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
	})

	It("skips an item the originate arm settled", func() {
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		item.AnsweredBy = "attention-watcher"
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
	})

	It("skips an unanswered item", func() {
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.State = pkg.OpenState
		item.AnsweredAt = nil
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
	})

	It("skips a board-answered card that is not a menu", func() {
		item := answeredMenuItem("item-1", nil)
		item.ProvenanceClass = pkg.ExplicitProvenanceClass
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
	})
})
