// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakePaneWriter is a scripted PaneTextWriter.
type fakePaneWriter struct {
	sessionTTY map[string]string
	entries    map[string]SessionEntry
	panes      map[int]Pane
	listErr    error
	composer   map[int]ComposerState
	sent       []sentText
	// sendErr, when set, makes every SendText fail.
	sendErr error
}

type sentText struct {
	pane int
	text string
}

func (f *fakePaneWriter) Activate(_ context.Context, _ string) error { return nil }

func (f *fakePaneWriter) SendText(_ context.Context, pane int, text string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, sentText{pane: pane, text: text})
	// The real TUI draws the typed text into the composer, so a non-Enter send
	// turns an empty composer busy; Enter submits and clears it.
	if text == "\r" {
		f.composer[pane] = ComposerEmpty
	} else {
		f.composer[pane] = ComposerBusy
	}
	return nil
}

func (f *fakePaneWriter) ComposerState(_ context.Context, pane int) ComposerState {
	return f.composer[pane]
}

func (f *fakePaneWriter) Text(_ context.Context, _ int) (string, bool) { return "", false }

func (f *fakePaneWriter) PaneTTY(_ context.Context, pane int) string {
	paneInfo, ok := f.panes[pane]
	if !ok {
		return ""
	}
	return paneInfo.TTYName
}

func (f *fakePaneWriter) ListPanes(_ context.Context) (map[int]Pane, error) {
	return f.panes, f.listErr
}

func (f *fakePaneWriter) SessionTTY(_ context.Context, sessionID string) string {
	return f.sessionTTY[sessionID]
}

func (f *fakePaneWriter) SessionEntry(_ context.Context, sessionID string) (SessionEntry, bool) {
	entry, ok := f.entries[sessionID]
	return entry, ok
}

// fakeTranscriptReader is a scripted TranscriptReader.
type fakeTranscriptReader struct {
	offsets    map[string]int64
	holds      map[string]bool
	turnEnded  map[string]bool
	holdCalls  []string
	pathBySess map[string]string
}

func (f *fakeTranscriptReader) Path(sessionID string) string { return f.pathBySess[sessionID] }

func (f *fakeTranscriptReader) Offset(sessionID string) (int64, bool) {
	offset, ok := f.offsets[sessionID]
	return offset, ok
}

func (f *fakeTranscriptReader) HoldsLine(sessionID string, line string, _ *int64) bool {
	f.holdCalls = append(f.holdCalls, line)
	return f.holds[sessionID]
}

func (f *fakeTranscriptReader) TurnEnded(sessionID string) bool { return f.turnEnded[sessionID] }

var _ = Describe("pane carrier", func() {
	const (
		sessionID = "11111111-2222-3333-4444-555555555555"
		storeID   = "abcdef0123456789"
		tty       = "/dev/ttys004"
	)

	var (
		ctx        context.Context
		writer     *fakePaneWriter
		transcript *fakeTranscriptReader
		carrier    *paneCarrier
		delivery   *PaneDelivery
	)

	BeforeEach(func() {
		ctx = context.Background()
		writer = &fakePaneWriter{
			sessionTTY: map[string]string{sessionID: tty},
			entries: map[string]SessionEntry{
				sessionID: {SessionID: sessionID, PID: 4242, Entrypoint: "cli", CWD: "/tmp/ws"},
			},
			panes: map[int]Pane{
				7: {PaneID: 7, TTYName: tty},
			},
			composer: map[int]ComposerState{7: ComposerEmpty},
		}
		transcript = &fakeTranscriptReader{
			offsets:   map[string]int64{sessionID: 100},
			holds:     map[string]bool{sessionID: true},
			turnEnded: map[string]bool{},
		}
		carrier = &paneCarrier{
			writer:             writer,
			transcripts:        transcript,
			submitPollInterval: 0,
			submitWaitPolls:    3,
			submitAttempts:     2,
		}
		delivery = &PaneDelivery{SessionID: sessionID, Pane: 7, StoreItemID: storeID}
	})

	It("types the prefixed answer, submits it, and reports delivered", func() {
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryOK))
		Expect(reason).To(ContainSubstring("submitted"))
		Expect(writer.sent).To(HaveLen(2))
		Expect(writer.sent[0].text).To(Equal(
			"Operator answer, via attention board: allow [" + storeID + "]"))
		Expect(writer.sent[1].text).To(Equal("\r"))
		Expect(delivery.PendingLine).To(BeEmpty())
	})

	It("refuses to type into a busy composer", func() {
		writer.composer[7] = ComposerBusy
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRetry))
		Expect(reason).To(ContainSubstring("busy"))
		Expect(writer.sent).To(BeEmpty())
	})

	It("refuses to type into an unreadable composer", func() {
		writer.composer[7] = ComposerUnknown
		verdict, _ := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRetry))
		Expect(writer.sent).To(BeEmpty())
	})

	It("does not re-type a line the transcript already carries", func() {
		delivery.PendingLine = "Operator answer, via attention board: allow [" + storeID + "]"
		delivery.PendingAt = 50
		delivery.HasPendingAt = true
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryOK))
		Expect(reason).To(ContainSubstring("already delivered"))
		Expect(writer.sent).To(BeEmpty())
	})

	It("keeps the pending marker when the submit is never proven", func() {
		transcript.holds[sessionID] = false
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRetry))
		Expect(reason).To(ContainSubstring("unsubmitted"))
		Expect(delivery.PendingLine).NotTo(BeEmpty())
	})

	It("re-resolves the pane from the session tty when the recorded pane is stale", func() {
		writer.panes = map[int]Pane{9: {PaneID: 9, TTYName: tty}}
		writer.composer[9] = ComposerEmpty
		delivery.Pane = 7
		verdict, _ := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryOK))
		Expect(delivery.Pane).To(Equal(9))
	})

	It("reports no-pane for a live sdk-cli session with no tty", func() {
		delete(writer.sessionTTY, sessionID)
		writer.entries[sessionID] = SessionEntry{
			SessionID: sessionID, PID: os.Getpid(), Entrypoint: "sdk-cli",
		}
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryNoPane))
		Expect(reason).To(ContainSubstring("pane-less"))
		Expect(writer.sent).To(BeEmpty())
	})

	It("retries when the pane list could not be read", func() {
		writer.panes = map[int]Pane{}
		writer.listErr = errors.New("wezterm down")
		verdict, _ := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRetry))
		Expect(writer.sent).To(BeEmpty())
	})

	It("sends a bare command without the prefix or store id", func() {
		verdict, _ := carrier.TypeIntoPane(ctx, delivery, "/vault-cli:prepare-compact", true)
		Expect(verdict).To(Equal(DeliveryOK))
		Expect(writer.sent[0].text).To(Equal("/vault-cli:prepare-compact"))
	})

	It("retries when send-text fails and clears the pending marker", func() {
		writer.sendErr = errors.New("pane gone")
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRetry))
		Expect(reason).To(ContainSubstring("send-text"))
		Expect(delivery.PendingLine).To(BeEmpty())
	})
})
