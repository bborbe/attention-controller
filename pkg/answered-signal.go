// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "context"

// AttributionVerdict is what one card's answer can be attributed to.
//
// The six values are the vocabulary the supervisor's guard table names. A
// consumer that reads only `answered` treats a scripted click as the operator's
// answer; this type is what tells the two apart. See
// docs/answer-attribution.md for the contract.
type AttributionVerdict string

const (
	// AttributionVerdictNothing is a card nothing acted on: no answer and no
	// terminal transition. The rendered word is NOT_OPERATOR_ANSWERED.
	AttributionVerdictNothing AttributionVerdict = "NOTHING"
	// AttributionVerdictUnattributed is a record that cannot be attributed to a
	// client — either a close with no answer, or an answer with no recorded
	// client. The rendered word is NOT_OPERATOR_ANSWERED.
	AttributionVerdictUnattributed AttributionVerdict = "UNATTRIBUTED"
	// AttributionVerdictDeclined is an answer whose kind is `skip`. The rendered
	// word is DECLINED.
	AttributionVerdictDeclined AttributionVerdict = "DECLINED"
	// AttributionVerdictHidden is an answer whose kind is `hidden` — the operator
	// handled the card elsewhere. The rendered word is HIDDEN.
	AttributionVerdictHidden AttributionVerdict = "HIDDEN"
	// AttributionVerdictAutomated is a client-flagged automated answer. The
	// rendered word is NOT_OPERATOR_ANSWERED.
	AttributionVerdictAutomated AttributionVerdict = "AUTOMATED"
	// AttributionVerdictAttributed is a recorded client with no automation flag:
	// the operator's answer. The rendered word is ANSWERED.
	AttributionVerdictAttributed AttributionVerdict = "ATTRIBUTED"
)

// wasActedOn reports whether anything acted on the card at all. It is true when
// an answer arrived, or when an arm recorded an answering identity on a card in
// a state that can only follow a decision (answered or closed). Nothing else
// counts: an open card carrying an `answered_by` that never advanced its state
// was not acted on.
func wasActedOn(item Item) bool {
	if item.AnsweredAt != nil {
		return true
	}
	return item.AnsweredBy != "" &&
		(item.State == AnsweredState || item.State == ClosedState)
}

// ClassifyAnswerAttribution returns the verdict and a human-readable reason.
//
// It is a port of `answered-attribution.py`'s `classify()`, reproducing its
// seven rows in its order. The order is the contract, not an optimisation: the
// reason strings name the deciding row so a scripted click is diagnosable from
// the log alone.
//
// # Kind before client (2026-10-20)
//
// Rows 3–4 are evaluated before the client check in row 6. A decline is a
// decline whether a person or a script clicked it. Asking "was this flagged
// automated?" first answered ATTRIBUTED for a card the operator had explicitly
// dismissed.
//
// # A close is a clear, not an answer (2026-09-27)
//
// Row 2 returns UNATTRIBUTED for a closed card with no `answered_at`. The
// board's Acknowledge closes a card and routes nothing back, so releasing on it
// would open a gate nobody opened.
//
// # ⚠️ `resolved_by` is NOT a requirement
//
// Row 7 reads no `resolved_by` and its presence or absence changes nothing. The
// requirement was withdrawn 2026-09-27, hours after it shipped, because the
// board's JavaScript never sends it and so refused the operator's own board
// answer. The `resolved_by` requirement in `permission-answer-poll.py` is a
// different script's rule and does not apply here.
//
// ctx is accepted for consistency with the package's function shape; the
// decision reads only the item.
func ClassifyAnswerAttribution(
	ctx context.Context,
	item Item,
) (AttributionVerdict, string) {
	if !wasActedOn(item) {
		return AttributionVerdictNothing,
			"nothing: no answer and no terminal transition acted on the card"
	}
	if item.State == ClosedState && item.AnsweredAt == nil {
		return AttributionVerdictUnattributed,
			"unattributed: a close is a clear, not an answer"
	}
	if item.Answer != nil && item.Answer.Kind == SkipAnswerKind {
		return AttributionVerdictDeclined,
			"declined: the answer kind is skip"
	}
	if item.Answer != nil && item.Answer.Kind == HiddenAnswerKind {
		return AttributionVerdictHidden,
			"hidden: the answer kind is hidden"
	}
	if item.AnsweredClient == nil {
		return AttributionVerdictUnattributed,
			"unattributed: no answered_client was recorded"
	}
	if item.AnsweredClient.Automation != nil && *item.AnsweredClient.Automation {
		return AttributionVerdictAutomated,
			"automated: answered_client.automation is true"
	}
	return AttributionVerdictAttributed,
		"attributed: a client was recorded with no automation flag"
}

// RenderedAnswerSignal returns the word the guard table renders for a verdict.
//
// The rendered vocabulary is derived here and never stored beside the verdict:
// ATTRIBUTED renders ANSWERED; DECLINED and HIDDEN render themselves; every
// other value, including an unknown one, renders NOT_OPERATOR_ANSWERED.
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
