// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "context"

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
