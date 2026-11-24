// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	libtime "github.com/bborbe/time"
)

// nopHeadless is a HeadlessAnswerDeliverer that never carries anything. The
// reproduction never reaches the pane-less path, so it exists only to satisfy
// the job's constructor.
type nopHeadless struct{}

func (nopHeadless) DeliverHeadless(context.Context, Item) bool { return false }

func (nopHeadless) CanResume(context.Context, string) bool { return false }

// transcriptRoot builds a temp transcript root holding one project dir, and
// returns the root, the session id, and the transcript path.
func transcriptRoot(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "-Users-bborbe-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	session := "11111111-2222-3333-4444-555555555555"
	return root, session, filepath.Join(project, session+".jsonl")
}

// deliveredRecord renders the transcript record the harness writes when a
// mid-turn answer lands: an api-request-blob carrying the line as a top-level
// text block. That is the shape a running session receives an answer in.
func deliveredRecord(line string) string {
	return `{"type":"api-request-blob","message":{"role":"user","content":[{"type":"text","text":"` +
		line + `"}]}}` + "\n"
}

// fillerRecords writes more than transcriptReaderTailBytes of unrelated
// records, so a delivery written BEFORE them sits outside the narrow tail.
func fillerRecords() string {
	const record = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"filler"}]}}` + "\n"
	count := transcriptReaderTailBytes/len(record) + 64
	return strings.Repeat(record, count)
}

// paneWriterFor builds a fake pane writer with one live session on one pane and
// an empty composer.
func paneWriterFor(session string) *fakePaneWriter {
	return &fakePaneWriter{
		sessionTTY: map[string]string{session: "/dev/ttys001"},
		panes:      map[int]Pane{1: {TTYName: "/dev/ttys001"}},
		composer:   map[int]ComposerState{1: ComposerEmpty},
	}
}

// fastCarrier builds a real pane carrier with its submit loop collapsed to one
// poll, so a spec drives it without real sleeps.
func fastCarrier(t *testing.T, writer PaneTextWriter, reader TranscriptReader) *paneCarrier {
	t.Helper()
	carrier, ok := NewPaneCarrier(writer, reader).(*paneCarrier)
	if !ok {
		t.Fatal("NewPaneCarrier did not build the concrete carrier")
	}
	carrier.submitPollInterval = 0
	carrier.submitWaitPolls = 1
	carrier.submitAttempts = 1
	return carrier
}

// nonEnterSends counts the prose sends, ignoring the Enter keystrokes.
func nonEnterSends(sent []sentText) int {
	n := 0
	for _, s := range sent {
		if s.text != "\r" {
			n++
		}
	}
	return n
}

// answeredCard pushes and answers one message card addressed to session, and
// returns the card with its AnsweredAt. The rendered delivery line the carrier
// will type is derivable from it, so the fixture can carry the exact text.
func answeredCard(
	t *testing.T,
	ctx context.Context,
	store AttentionStore,
	session string,
) *Item {
	t.Helper()
	item, err := store.Push(ctx, PushRequest{
		ProducerID:      "session-a",
		ProducerKind:    SessionProducerKind,
		LivenessRef:     LivenessRef("session:" + session),
		DedupKey:        "redelivery-test",
		InterruptClass:  "pick",
		Payload:         "Pick one",
		AnswerMechanism: MessageAnswerMechanism,
		Options:         AnswerOptions{{Label: "allow"}},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	answered, err := store.Answer(
		ctx, item.ItemID, "attention-board", "", "",
		&Answer{Kind: OptionAnswerKind, Value: "allow"}, nil, nil,
	)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	return answered
}

// deliveryLine is the exact line the carrier renders for this answered card.
func deliveryLine(item *Item) string {
	stamp := ""
	if item.AnsweredAt != nil {
		stamp = item.AnsweredAt.Time().UTC().Format(time.RFC3339)
	}
	marker := "[" + string(item.ItemID) + "]"
	if stamp != "" {
		marker = "[" + string(item.ItemID) + " @ " + stamp + "]"
	}
	return deliverPrefix + " allow " + marker
}

// TestAnsweredCardIsNotRedeliveredAfterRestart is SC1/SC2's reproduction.
//
// It drives the REAL delivery path (job + real store + real carrier + real
// transcript reader) twice with the same answered card. The first cycle types
// the answer, and the harness records it mid-turn (an api-request-blob top-level
// text block) followed by ordinary session output, which pushes the delivery
// further back than the reader's narrow tail. The second cycle runs on a FRESH
// job after the retry backoff — the shape of a controller restart, which drops
// the in-process pending marker — and must find the copy already in the
// transcript instead of typing a second turn.
//
// On the current build the fresh-entry guard reads only the last 256 KiB, misses
// the delivery, and types again: a second arrival of one answer.
func TestAnsweredCardIsNotRedeliveredAfterRestart(t *testing.T) {
	ctx := context.Background()
	root, session, path := transcriptRoot(t)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}

	now := libtime.NewCurrentDateTime().Now()
	clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now })
	db, err := libboltkv.OpenTemp(ctx)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	store := NewAttentionStore(
		db,
		NewItemIDGenerator(),
		alwaysLiveChecker{},
		clock,
		libtime.Duration(15*60*1e9),
	)

	item := answeredCard(t, ctx, store, session)
	line := deliveryLine(item)

	writer := paneWriterFor(session)
	carrier := fastCarrier(t, writer, NewTranscriptReader(root))

	// Cycle 1: nothing delivered yet, so the arm types the answer once.
	jobA := NewAttentionWatcherJob(store, carrier, nopHeadless{}, time.Hour, clock)
	if err := jobA.DeliverOnce(ctx); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if got := nonEnterSends(writer.sent); got != 1 {
		t.Fatalf("cycle 1 typed %d copies, want 1", got)
	}

	// The harness records the mid-turn delivery, then the session keeps writing.
	if err := os.WriteFile(path, []byte(deliveredRecord(line)+fillerRecords()), 0o600); err != nil {
		t.Fatalf("record delivery: %v", err)
	}

	// Advance past both backoffs so the second cycle is eligible to retry.
	now = libtime.DateTime(now.Time().Add(6 * time.Minute))

	// Cycle 2 on a FRESH job: the in-process marker is gone (restart), and the
	// durable record says an arm already looked at this answer.
	jobB := NewAttentionWatcherJob(store, carrier, nopHeadless{}, time.Hour, clock)
	if err := jobB.DeliverOnce(ctx); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}

	if got := nonEnterSends(writer.sent); got != 1 {
		t.Fatalf("one answer typed %d times across a restart, want exactly 1 "+
			"(the transcript already holds the delivery, so the second cycle must not type again)", got)
	}
}

// TestUndeliveredAnswerStillDeliversAfterRestart is SC3's positive control.
//
// The wide transcript search must not become a silent drop: when the copy is
// genuinely absent (the answer was never typed, or its copy never landed), a
// fresh cycle must still type it.
func TestUndeliveredAnswerStillDeliversAfterRestart(t *testing.T) {
	ctx := context.Background()
	root, session, path := transcriptRoot(t)
	if err := os.WriteFile(path, []byte(fillerRecords()), 0o600); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}

	now := libtime.NewCurrentDateTime().Now()
	clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now })
	db, err := libboltkv.OpenTemp(ctx)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	store := NewAttentionStore(
		db,
		NewItemIDGenerator(),
		alwaysLiveChecker{},
		clock,
		libtime.Duration(15*60*1e9),
	)

	item := answeredCard(t, ctx, store, session)
	// A durable retry record exists, but the transcript does not carry the line:
	// the copy never landed, so the answer must still be delivered.
	if _, err := store.RecordAttempt(ctx, item.ItemID, attentionWatcherRetryCarrier, RetriedOutcome); err != nil {
		t.Fatalf("record retry: %v", err)
	}

	writer := paneWriterFor(session)
	carrier := fastCarrier(t, writer, NewTranscriptReader(root))
	job := NewAttentionWatcherJob(store, carrier, nopHeadless{}, time.Hour, clock)
	if err := job.DeliverOnce(ctx); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	if got := nonEnterSends(writer.sent); got != 1 {
		t.Fatalf(
			"an undelivered answer typed %d times, want exactly 1 (it must not be dropped)",
			got,
		)
	}
}
