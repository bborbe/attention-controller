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
//
// ⚠️ `holds` is consulted only when a byte mark (`since`) is supplied, and
// `holdsAny` regardless. The carrier's submit proof always passes the mark it
// took at typing time; its pre-type duplicate guard passes nil. Keeping the two
// apart is what lets one fake model "nothing delivered yet" before typing and
// "delivered" after it, without a real transcript.
type fakeTranscriptReader struct {
	offsets    map[string]int64
	holds      map[string]bool
	holdsAny   map[string]bool
	turnEnded  map[string]bool
	holdCalls  []string
	pathBySess map[string]string
}

func (f *fakeTranscriptReader) Path(sessionID string) string { return f.pathBySess[sessionID] }

func (f *fakeTranscriptReader) Offset(sessionID string) (int64, bool) {
	offset, ok := f.offsets[sessionID]
	return offset, ok
}

func (f *fakeTranscriptReader) HoldsLine(sessionID string, line string, since *int64) bool {
	f.holdCalls = append(f.holdCalls, line)
	if f.holdsAny[sessionID] {
		return true
	}
	return f.holds[sessionID] && since != nil
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
			holdsAny:  map[string]bool{},
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

	It("does not re-type a line the transcript holds with no pending marker", func() {
		transcript.holdsAny[sessionID] = true
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

	// ⚠️ The reaped case. `processAlive` used to gate the whole no-pane route, so a
	// headless worker whose process had exited fell through to DeliveryRefuse and
	// its answered card settled `failed` — the headless arm that knows how to
	// resume an exited session was never reached. Measured on a real reap
	// 2026-11-16: session 46e673cf, item 76901296…, `verdict=failed reason="… has
	// no live process with a tty"` with no `action=headless` line.
	It("reports no-pane for a REAPED sdk-cli session, so the headless arm resumes it", func() {
		delete(writer.sessionTTY, sessionID)
		writer.entries[sessionID] = SessionEntry{
			// Beyond the platform's pid range, so processAlive is reliably false.
			SessionID: sessionID, PID: 999999, Entrypoint: "sdk-cli",
		}
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryNoPane),
			"a reaped headless worker is still a headless worker")
		Expect(reason).To(ContainSubstring("resumable"))
		Expect(writer.sent).To(BeEmpty())
	})

	// The boundary the fix must NOT widen: a dead non-headless session stays a
	// refusal, because there is no resume path that would carry it.
	It("still refuses a reaped session that is not headless", func() {
		delete(writer.sessionTTY, sessionID)
		writer.entries[sessionID] = SessionEntry{
			SessionID: sessionID, PID: 999999, Entrypoint: "cli",
		}
		verdict, _ := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRefuse))
		Expect(writer.sent).To(BeEmpty())
	})

	// The turn-end split: a dead session whose transcript proves it finished its
	// turn is refused with a reason that says so, because "no live process with a
	// tty" reads as a crash and sends the operator looking for a fault that is not
	// there. Ported from `test_attention_watcher_turn_ended.py` case (b).
	It("names the turn-end when a reaped session ended its turn before exiting", func() {
		delete(writer.sessionTTY, sessionID)
		writer.entries[sessionID] = SessionEntry{
			SessionID: sessionID, PID: 999999, Entrypoint: "cli",
		}
		transcript.turnEnded[sessionID] = true
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRefuse))
		Expect(reason).To(ContainSubstring("ended its turn and its process exited"))
		Expect(reason).To(ContainSubstring("answer not delivered"))
		Expect(reason).NotTo(ContainSubstring("has no live process with a tty"))
		Expect(writer.sent).To(BeEmpty())
	})

	// The discriminating half: the turn-end reason is NOT the default. A session
	// that died mid-turn keeps the plain death refusal — that is the failure the
	// card exists for. An implementation that always said "ended its turn" would
	// pass the spec above and fail this one.
	It("keeps the plain death refusal when a reaped session died mid-turn", func() {
		delete(writer.sessionTTY, sessionID)
		writer.entries[sessionID] = SessionEntry{
			SessionID: sessionID, PID: 999999, Entrypoint: "cli",
		}
		transcript.turnEnded[sessionID] = false
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRefuse))
		Expect(reason).To(ContainSubstring("has no live process with a tty"))
		Expect(reason).NotTo(ContainSubstring("ended its turn"))
		Expect(writer.sent).To(BeEmpty())
	})

	// ⚠️ The turn-ended permission arm. A permission gate holds its turn open, so
	// a turn-end after it proves the gate closed inside the turn — there is
	// nothing left to answer, and carding it said a live session was dead. The
	// question arm above keeps an honest refusal: its answer really was not
	// delivered. This is the arm the Python watcher had and the Go port dropped.
	It("resolves a permission gate whose session is dead and its turn ended", func() {
		delete(writer.sessionTTY, sessionID)
		writer.entries[sessionID] = SessionEntry{
			SessionID: sessionID, PID: 999999, Entrypoint: "cli",
		}
		transcript.turnEnded[sessionID] = true
		delivery.AnswerMechanism = PermissionAnswerMechanism
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryResolved))
		Expect(reason).To(ContainSubstring("permission gate closed inside the turn"))
		Expect(writer.sent).To(BeEmpty())
	})

	// ⚠️ The record is deleted on exit, so a normally-exited session reaches the
	// verdict with ok=false. The turn-end check must still fire, or the false
	// "has no live process with a tty" card returns for exactly the population
	// this fix serves.
	It("resolves a dead turn-ended permission gate whose registry record is gone", func() {
		delete(writer.sessionTTY, sessionID)
		delete(writer.entries, sessionID)
		transcript.turnEnded[sessionID] = true
		delivery.AnswerMechanism = PermissionAnswerMechanism
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryResolved))
		Expect(reason).To(ContainSubstring("permission gate closed inside the turn"))
		Expect(writer.sent).To(BeEmpty())
	})

	It("keeps the plain refusal for a dead mid-turn permission gate", func() {
		delete(writer.sessionTTY, sessionID)
		writer.entries[sessionID] = SessionEntry{
			SessionID: sessionID, PID: 999999, Entrypoint: "cli",
		}
		delivery.AnswerMechanism = PermissionAnswerMechanism
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRefuse))
		Expect(reason).To(ContainSubstring("has no live process with a tty"))
		Expect(writer.sent).To(BeEmpty())
	})

	It("keeps the refusal for a live non-headless session with no tty", func() {
		delete(writer.sessionTTY, sessionID)
		writer.entries[sessionID] = SessionEntry{
			SessionID: sessionID, PID: os.Getpid(), Entrypoint: "cli",
		}
		transcript.turnEnded[sessionID] = true
		delivery.AnswerMechanism = PermissionAnswerMechanism
		verdict, reason := carrier.TypeIntoPane(ctx, delivery, "allow", false)
		Expect(verdict).To(Equal(DeliveryRefuse))
		Expect(reason).To(ContainSubstring("has no live process with a tty"))
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
