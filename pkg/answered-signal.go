// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/golang/glog"
)

// AttributionVerdict is what one card's answer can be attributed to.
type AttributionVerdict string

const (
	// AttributionVerdictNothing means no arm acted on the card at all.
	AttributionVerdictNothing AttributionVerdict = "NOTHING"
	// AttributionVerdictUnattributed means the card was acted on, but no client
	// recorded an answer that could be attributed to the operator.
	AttributionVerdictUnattributed AttributionVerdict = "UNATTRIBUTED"
	// AttributionVerdictDeclined means the operator declined with a `skip` answer.
	AttributionVerdictDeclined AttributionVerdict = "DECLINED"
	// AttributionVerdictHidden means the operator handled the card elsewhere.
	AttributionVerdictHidden AttributionVerdict = "HIDDEN"
	// AttributionVerdictAutomated means a scripted client, not the operator,
	// produced the answer.
	AttributionVerdictAutomated AttributionVerdict = "AUTOMATED"
	// AttributionVerdictAttributed means the answer came from a recorded client
	// with no automation flag.
	AttributionVerdictAttributed AttributionVerdict = "ATTRIBUTED"
)

// wasActedOn reports whether anything happened to the card. A card is acted on
// when an answer timestamp was recorded, or when an answer arm was recorded
// against a card that has left the open state.
func wasActedOn(item Item) bool {
	if item.AnsweredAt != nil {
		return true
	}
	if item.AnsweredBy == "" {
		return false
	}
	return item.State == AnsweredState || item.State == ClosedState
}

// ClassifyAnswerAttribution returns the verdict and a human-readable reason.
//
// The seven conditions are evaluated in this exact order, and the order is the
// contract rather than an optimisation. Kind before client (2026-10-20): a
// decline is a decline whether a person or a script clicked it, so the answer
// kind is read before the automation flag. Asking "was this flagged automated?"
// first answered ATTRIBUTED for a card the operator explicitly dismissed.
//
// A close is a clear, not an answer (2026-09-27): the board's Acknowledge closes
// a card with answered_at unset and nothing routed back, so releasing on it
// opens a gate nobody opened.
//
// ⚠️ resolved_by is NOT a requirement. It was withdrawn 2026-09-27, hours after
// it shipped, because the board's JavaScript never sends it and so refused the
// operator's own board answer (the card read answered while the asking session
// stayed frozen). The resolved_by requirement in permission-answer-poll.py is a
// different script's rule and does not apply here. Row 7 therefore reads a
// recorded client with no automation flag as the operator's answer, and a
// present or absent resolved_by changes nothing.
func ClassifyAnswerAttribution(
	ctx context.Context,
	item Item,
) (AttributionVerdict, string) {
	if !wasActedOn(item) {
		return AttributionVerdictNothing, "row 1: the card was not acted on"
	}
	if item.State == ClosedState && item.AnsweredAt == nil {
		return AttributionVerdictUnattributed,
			"row 2: a close with no answered_at is a clear, not an answer"
	}
	if item.Answer != nil && item.Answer.Kind == SkipAnswerKind {
		return AttributionVerdictDeclined,
			"row 3: the answer kind is skip, so the operator declined"
	}
	if item.Answer != nil && item.Answer.Kind == HiddenAnswerKind {
		return AttributionVerdictHidden,
			"row 4: the answer kind is hidden, so the card was handled elsewhere"
	}
	if item.AnsweredClient == nil {
		return AttributionVerdictUnattributed,
			"row 5: no answered_client was recorded, so the answer is unattributed"
	}
	if item.AnsweredClient.Automation != nil && *item.AnsweredClient.Automation {
		return AttributionVerdictAutomated,
			"row 6: the answered_client is flagged automated, so the click was scripted"
	}
	return AttributionVerdictAttributed,
		"row 7: a client was recorded with no automation flag"
}

// RenderedAnswerSignal returns the word the guard table renders for a verdict.
//
// The rendered vocabulary is derived from the verdict and never stored beside
// it, so the two cannot disagree.
func RenderedAnswerSignal(verdict AttributionVerdict) string {
	switch verdict {
	case AttributionVerdictAttributed:
		return "ANSWERED"
	case AttributionVerdictDeclined:
		return "DECLINED"
	case AttributionVerdictHidden:
		return "HIDDEN"
	default:
		return "NOT_OPERATOR_ANSWERED"
	}
}

// AnsweredSignalRecorder writes the ANSWERED signal for one card.
type AnsweredSignalRecorder interface {
	// RecordAnswered writes the signal line for one card. It never fails the
	// caller: a lost line costs one wake, and the answer itself was already
	// carried.
	RecordAnswered(ctx context.Context, itemID ItemID, verdict AttributionVerdict)
}

// NewAnsweredSignalRecorder creates the recorder that writes one line per
// card to w. A nil w discards, so an unconfigured host emits nothing rather
// than panicking.
func NewAnsweredSignalRecorder(w io.Writer) AnsweredSignalRecorder {
	return &answeredSignalRecorder{writer: w}
}

type answeredSignalRecorder struct {
	writer io.Writer
}

// RecordAnswered writes "<rendered> <item-id>" through RenderedAnswerSignal, so
// the vocabulary is derived rather than spelled a second time.
func (r *answeredSignalRecorder) RecordAnswered(
	_ context.Context,
	itemID ItemID,
	verdict AttributionVerdict,
) {
	if r.writer == nil {
		return
	}
	if _, err := fmt.Fprintf(r.writer, "%s %s\n", RenderedAnswerSignal(verdict), itemID); err != nil {
		// The answer was already delivered; a failed write must not be reported
		// as a failed delivery. It costs one wake, nothing more.
		glog.Warningf(
			"job=attention-watcher action=signal_write_error item=%s error=%v",
			itemID,
			err,
		)
	}
}

// AnsweredSignaller decides whether a card earns the ANSWERED signal and
// records it at most once per item.
type AnsweredSignaller interface {
	// Signal emits the ANSWERED signal for one item when it classifies
	// ATTRIBUTED and its producer is the asking session. It returns true
	// only when it emitted now.
	Signal(ctx context.Context, sessionID string, item Item) bool
}

// NewAnsweredSignaller creates the signaller over a recorder.
func NewAnsweredSignaller(recorder AnsweredSignalRecorder) AnsweredSignaller {
	return &answeredSignaller{
		recorder:  recorder,
		signalled: map[ItemID]bool{},
	}
}

type answeredSignaller struct {
	recorder AnsweredSignalRecorder
	// mu guards signalled. ⚠️ ONE instance serves four arms running concurrently
	// on run.NewConcurrentRunner; an unsynchronised map here is an unrecoverable
	// fatal error, not a test flake.
	mu sync.Mutex
	// signalled is the at-most-once guard for this process. It is deliberately
	// not the durable record: the arms' own delivered gating is the durable half,
	// and a crash between the delivered write and this call loses one signal
	// rather than duplicating it.
	signalled map[ItemID]bool
}

// Signal evaluates attribution, then producer scoping, then the in-process
// dedupe, and records at most once per item.
func (s *answeredSignaller) Signal(
	ctx context.Context,
	sessionID string,
	item Item,
) bool {
	verdict, reason := ClassifyAnswerAttribution(ctx, item)
	emitted := false
	switch {
	case verdict != AttributionVerdictAttributed:
		// The automation flag and the close-is-not-an-answer rules become
		// visible here: reason is the ladder's own explanation.
	case sessionID == "" || string(item.ProducerID) != sessionID:
		// A peer's answered card is not this session's wake.
		reason = fmt.Sprintf(
			"producer %q is not the asking session %q",
			item.ProducerID,
			sessionID,
		)
	default:
		emitted = s.recordOnce(ctx, item, verdict)
	}
	// Exactly one line per call, whatever the outcome, so every decline reason
	// the ladder can return is readable in the log.
	glog.V(2).Infof(
		"job=attention-watcher action=answered_signal item=%s verdict=%s reason=%q emitted=%t session=%q producer=%q",
		item.ItemID,
		verdict,
		reason,
		emitted,
		sessionID,
		item.ProducerID,
	)
	return emitted
}

// recordOnce marks the item under the mutex and writes the signal only on the
// first sighting.
func (s *answeredSignaller) recordOnce(
	ctx context.Context,
	item Item,
	verdict AttributionVerdict,
) bool {
	s.mu.Lock()
	if s.signalled[item.ItemID] {
		s.mu.Unlock()
		return false
	}
	s.signalled[item.ItemID] = true
	s.mu.Unlock()
	if s.recorder != nil {
		s.recorder.RecordAnswered(ctx, item.ItemID, verdict)
	}
	return true
}
