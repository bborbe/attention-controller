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
	// unreadableAfter is the Text-call index from which the pane reports
	// unreadable; -1 means never.
	unreadableAfter int
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
	if w.unreadableAfter >= 0 && w.index >= w.unreadableAfter {
		return "", false
	}
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
		writer = &fakeMenuWriter{unreadableAfter: -1}
		// Zero every sleep and shorten the retry allowance: the loop shape is
		// what these specs pin, not its pace.
		job = pkg.NewMenuAnswerJob(
			store, locator, writer, time.Hour,
			pkg.WithMenuAnswerTimings(0, 2),
			pkg.WithMenuAnswerMaxCycles(2),
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

	It("records held without typing when the board hid the answer", func() {
		// The corner X writes `hidden`: handled elsewhere, deliver nothing. It is
		// `held`, not `failed` — a failure record renders an undeliverable card for
		// a gate nothing failed on.
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.HiddenAnswerKind}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.HeldOutcome))
	})

	It("releases the gate with a lone ESC when every question was skipped", func() {
		item := answeredMenuItem("item-1", pkg.Questions{
			{Tab: "A", Options: pkg.AnswerOptions{{Label: "x"}}},
			{Tab: "B", Options: pkg.AnswerOptions{{Label: "y"}}},
		})
		item.Answers = pkg.Answers{
			{Question: "A", Answer: pkg.Answer{Kind: pkg.SkipAnswerKind}},
			{Question: "B", Answer: pkg.Answer{Kind: pkg.SkipAnswerKind}},
		}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{"menu\nEnter to select", "done\n"}
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(Equal([]string{"\x1b"}))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("retries without recording when the pane becomes unreadable while confirming", func() {
		// An unreadable pane is not a closed menu: treating it as one would record
		// a delivery that never happened and drop the answer for good.
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{"menu\nEnter to select", "menu\nEnter to select"}
		writer.unreadableAfter = 1
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(Equal([]string{"1"}))
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("retries without typing when the pane is unreadable before the keys", func() {
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{"menu\nEnter to select"}
		writer.unreadableAfter = 0
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("sends the keys once and gives up after the retry allowance", func() {
		// A re-sent sequence is not idempotent: a multi-select re-toggles and a
		// typed answer duplicates. The second cycle must re-check the close only.
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}, {Label: "No"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "No"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{"menu\nEnter to select"}
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(Equal([]string{"2"}))
		Expect(store.RecordAttemptCallCount()).To(Equal(0))

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(Equal([]string{"2"}), "keys must not be re-sent")
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
	})

	It("records nothing when the session is pane-less and no headless arm is wired", func() {
		// With no delivery arm configured nothing can carry the answer, and
		// nothing was attempted — so a `failed` record would render an
		// undeliverable card for a failure the arm never experienced. `main.go`
		// always wires the headless arm; this is the degenerate configuration.
		locator.verdict = pkg.DeliveryNoPane
		locator.pane = 0
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(BeEmpty())
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("types again when the operator re-answers the card", func() {
		// A changed answer must win over the stale at-most-once marker, exactly as
		// the prose carrier lets a different answer win over its pending marker.
		item := answeredMenuItem("item-1", nil)
		item.Options = pkg.AnswerOptions{{Label: "Yes"}, {Label: "No"}}
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Yes"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		writer.texts = []string{"menu\nEnter to select"}
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(Equal([]string{"1"}))

		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "No"}
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(writer.sent).To(Equal([]string{"1", "2"}))
	})
})
