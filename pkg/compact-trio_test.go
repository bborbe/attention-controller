// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// trioCarrier records every bare send and answers Locate with a fixed pane.
type trioCarrier struct {
	sent    []string
	bare    []bool
	pane    int
	verdict pkg.DeliveryVerdict
}

func (c *trioCarrier) TypeIntoPane(
	_ context.Context,
	_ *pkg.PaneDelivery,
	text string,
	bare bool,
) (pkg.DeliveryVerdict, string) {
	c.sent = append(c.sent, text)
	c.bare = append(c.bare, bare)
	return pkg.DeliveryOK, "ok"
}

func (c *trioCarrier) Locate(
	_ context.Context,
	_ *pkg.PaneDelivery,
) (pkg.DeliveryVerdict, int, string) {
	if c.verdict != "" {
		return c.verdict, 0, "scripted"
	}
	return pkg.DeliveryOK, c.pane, "ok"
}

// trioWriter is a PaneTextWriter whose composer the spec drives.
type trioWriter struct {
	composer pkg.ComposerState
}

func (w *trioWriter) Activate(_ context.Context, _ string) error { return nil }

func (w *trioWriter) SendText(_ context.Context, _ int, _ string) error { return nil }

func (w *trioWriter) ComposerState(_ context.Context, _ int) pkg.ComposerState {
	return w.composer
}

func (w *trioWriter) Text(_ context.Context, _ int) (string, bool) { return "", false }

func (w *trioWriter) PaneTTY(_ context.Context, _ int) string { return "" }

func (w *trioWriter) ListPanes(_ context.Context) (map[int]pkg.Pane, error) {
	return map[int]pkg.Pane{}, nil
}

func (w *trioWriter) SessionTTY(_ context.Context, _ string) string { return "" }

func (w *trioWriter) SessionEntry(_ context.Context, _ string) (pkg.SessionEntry, bool) {
	return pkg.SessionEntry{}, false
}

var _ = Describe("compact trio", func() {
	const (
		sessionID = "11111111-2222-3333-4444-555555555555"
		pane      = 4242
	)

	var (
		store          *mocks.AttentionStore
		carrier        *trioCarrier
		writer         *trioWriter
		transcripts    pkg.TranscriptReader
		transcriptPath string
		root           string
		now            stdtime.Time
		clock          libtime.CurrentDateTimeGetter
		job            pkg.CompactTrioJob
	)

	// writeTranscript rewrites the session transcript and stamps its mtime, so
	// the reader's quiet gate sees exactly what the spec wants it to see.
	writeTranscript := func(body string, modified stdtime.Time) {
		Expect(os.WriteFile(transcriptPath, []byte(body), 0o600)).To(Succeed())
		Expect(os.Chtimes(transcriptPath, modified, modified)).To(Succeed())
	}

	// approvedCompactItem is a session-raised compact card the operator approved.
	approvedCompactItem := func() pkg.Item {
		item := pkg.Item{
			ItemID:          "item-1",
			DedupKey:        pkg.DedupKey("compact-approval:" + sessionID),
			LivenessRef:     pkg.LivenessRef("session:" + sessionID),
			State:           pkg.AnsweredState,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options: pkg.AnswerOptions{
				{Label: "Compact now", Recommended: true},
				{Label: "Not now"},
			},
			Answer: &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Compact now"},
		}
		answered := libtime.DateTime(now)
		item.AnsweredAt = &answered
		return item
	}

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		carrier = &trioCarrier{pane: pane}
		writer = &trioWriter{composer: pkg.ComposerEmpty}
		now = stdtime.Date(2026, 11, 19, 12, 0, 0, 0, stdtime.UTC)
		clock = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})

		root = GinkgoT().TempDir()
		transcriptPath = filepath.Join(root, "project", sessionID+".jsonl")
		Expect(os.MkdirAll(filepath.Dir(transcriptPath), 0o750)).To(Succeed())
		// A transcript last written two minutes ago is idle: the quiet window is
		// 90 s, so the gate is open from the first tick.
		writeTranscript("{\"type\":\"user\"}\n", now.Add(-2*stdtime.Minute))
		transcripts = pkg.NewTranscriptReader(root)

		job = pkg.NewCompactTrioJob(
			store, carrier, writer, transcripts, pkg.CompactTrioInterval, clock,
		)
	})

	It("TestTrioSendsThreeBareCommandsInOrder — three sends, in order, each unprefixed", func() {
		store.ReadBoardReturns(pkg.Items{approvedCompactItem()}, nil)

		for i := 0; i < len(pkg.CompactTrioCommands); i++ {
			Expect(job.AdvanceOnce(context.Background())).To(Succeed())
			// Advance the clock past the quiet window so the next gate can open.
			now = now.Add(pkg.CompactTrioQuiet)
			// `post-compact` additionally needs a new compaction boundary.
			if i == 1 {
				writeTranscript(
					"{\"type\":\"user\"}\n{\"subtype\":\"compact_boundary\"}\n",
					now.Add(-2*stdtime.Minute),
				)
			}
		}

		Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands))
		Expect(carrier.bare).To(Equal([]bool{true, true, true}))
	})

	It("TestTrioWaitsForABusyComposer — nothing is typed into a composer holding text", func() {
		store.ReadBoardReturns(pkg.Items{approvedCompactItem()}, nil)
		writer.composer = pkg.ComposerBusy

		Expect(job.AdvanceOnce(context.Background())).To(Succeed())

		Expect(carrier.sent).To(BeEmpty())
	})

	It("TestTrioWaitsForTheTranscriptToGoQuiet — a session mid-turn is not typed into", func() {
		store.ReadBoardReturns(pkg.Items{approvedCompactItem()}, nil)
		// The transcript was written a moment ago: a turn is running.
		writeTranscript("{\"type\":\"user\"}\n", now.Add(-stdtime.Second))

		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		Expect(carrier.sent).To(BeEmpty())

		// Past the quiet window, the same card advances.
		now = now.Add(pkg.CompactTrioQuiet)
		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands[:1]))
	})

	It("TestTrioWithholdsPostCompactWithoutABoundary — no boundary, no third command", func() {
		store.ReadBoardReturns(pkg.Items{approvedCompactItem()}, nil)

		// Steps 1 and 2 land with the transcript quiet.
		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		now = now.Add(pkg.CompactTrioQuiet)
		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands[:2]))

		// No new boundary yet: `post-compact` is withheld.
		now = now.Add(pkg.CompactTrioQuiet)
		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands[:2]))

		// The compaction finishes and appends a boundary: the third command goes.
		writeTranscript(
			"{\"type\":\"user\"}\n{\"subtype\":\"compact_boundary\"}\n",
			now.Add(-2*stdtime.Minute),
		)
		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands))
	})

	It("TestTrioNeverRunsOnADecline — a non-recommended answer types no command", func() {
		item := approvedCompactItem()
		item.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "Not now"}
		store.ReadBoardReturns(pkg.Items{item}, nil)

		Expect(job.AdvanceOnce(context.Background())).To(Succeed())

		Expect(carrier.sent).To(BeEmpty())
	})

	It("TestTrioIgnoresAnUnrelatedCard — the prefix, not the option shape, selects a trio", func() {
		item := approvedCompactItem()
		item.DedupKey = pkg.DedupKey("some-other-question")
		store.ReadBoardReturns(pkg.Items{item}, nil)

		Expect(job.AdvanceOnce(context.Background())).To(Succeed())

		Expect(carrier.sent).To(BeEmpty())
	})

	It(
		"TestTrioStopsRatherThanRecompactingOnABoundaryTimeout — never restarts after /compact",
		func() {
			store.ReadBoardReturns(pkg.Items{approvedCompactItem()}, nil)

			Expect(job.AdvanceOnce(context.Background())).To(Succeed())
			now = now.Add(pkg.CompactTrioQuiet)
			Expect(job.AdvanceOnce(context.Background())).To(Succeed())
			Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands[:2]))

			// The boundary never arrives; the wait ceiling passes.
			now = now.Add(pkg.CompactTrioBoundaryCeiling + stdtime.Second)
			Expect(job.AdvanceOnce(context.Background())).To(Succeed())

			// It gives up instead of re-running `prepare-compact` and `/compact`,
			// which would compact the session a second time.
			Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands[:2]))
			Expect(job.AdvanceOnce(context.Background())).To(Succeed())
			Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands[:2]))
		},
	)

	It("TestTrioRestartsABusyFirstStep — a session busy at approval still gets compacted", func() {
		store.ReadBoardReturns(pkg.Items{approvedCompactItem()}, nil)
		writer.composer = pkg.ComposerBusy

		// The first step waits past its ceiling with nothing sent, so it restarts
		// rather than abandoning the operator's approval.
		now = now.Add(pkg.CompactTrioIdleCeiling + stdtime.Second)
		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		Expect(carrier.sent).To(BeEmpty())

		// Once the composer clears, the sequence runs from the first command.
		writer.composer = pkg.ComposerEmpty
		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		Expect(carrier.sent).To(Equal(pkg.CompactTrioCommands[:1]))
	})

	It(
		"TestTrioAbandonsAfterTheRestartCap — a session that never idles stops being retried",
		func() {
			store.ReadBoardReturns(pkg.Items{approvedCompactItem()}, nil)
			writer.composer = pkg.ComposerBusy

			// One tick creates the progress record, then each further tick past the
			// ceiling spends one restart.
			for i := 0; i <= pkg.CompactTrioRestarts+1; i++ {
				now = now.Add(pkg.CompactTrioIdleCeiling + stdtime.Second)
				Expect(job.AdvanceOnce(context.Background())).To(Succeed())
			}
			Expect(carrier.sent).To(BeEmpty())

			// Abandoned: a cleared composer no longer revives the sequence.
			writer.composer = pkg.ComposerEmpty
			Expect(job.AdvanceOnce(context.Background())).To(Succeed())
			Expect(carrier.sent).To(BeEmpty())
		},
	)

	It("TestTrioDropsAPaneLessCard — a session with no pane types nothing", func() {
		store.ReadBoardReturns(pkg.Items{approvedCompactItem()}, nil)
		carrier.verdict = pkg.DeliveryNoPane

		Expect(job.AdvanceOnce(context.Background())).To(Succeed())
		Expect(carrier.sent).To(BeEmpty())
	})
})
