// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// decisionWaitingForAckPrefix is the only text a card for an
// irreversible-infrastructure decision may carry. It announces the decision;
// it never asks for it.
const decisionWaitingForAckPrefix = "decision waiting for ack: "

// applyDecisionRecordRule is the mint-path half of [[Decision Table]] row 46:
// a push that would card an irreversible infrastructure decision is turned into
// a Decision Record page plus an acknowledgement pointer, never a `pick`.
//
// ⚠️ It is applied at the store's Push — the one mint path every producer
// reaches — rather than in each producer. Producers are spread across several
// plugins and helpers, and a rule each of them must remember is the half-armed
// shape normalizeInterruptClass already exists to close.
//
// The rewritten card is `ack`-shaped on purpose. A `pick` promises the operator
// a choice, and a choice is exactly what this class must not offer: answering
// it could release the work, which only the operator's own
// `vault-cli decision ack` may do.
func applyDecisionRecordRule(
	ctx context.Context,
	request PushRequest,
	writer DecisionRecordWriter,
) (PushRequest, error) {
	// Only a decision card can be routed to a record. A notification (`ack`
	// class) about a completed migration, or an `approve`/`permission` gate that
	// carries its own answer routing, must not be silently converted into an
	// optionless ack card: that would discard its context/options and change how
	// it is answered. The rule targets the `pick`/`message` card — the shape the
	// 2026-11-24 incident took — and leaves the other classes to the render-time
	// risk backstop (RiskOf).
	if request.InterruptClass != PickInterruptClass ||
		request.AnswerMechanism != MessageAnswerMechanism {
		return request, nil
	}
	// No writer means no vault to write the page into (the cluster backend's
	// case). Do not rewrite the card to point at a page that will never exist:
	// leave the request as a pick, which the render backstop escalates to `risk`
	// with its blast radius. A fake pointer is worse than an honestly-risky card.
	if writer == nil {
		return request, nil
	}
	if RequestConsequence(request) != IrreversibleInfraConsequence {
		return request, nil
	}
	record := NewDecisionRecord(request)
	// Build and validate the rewritten request BEFORE writing the page, so a
	// declaration that cannot become a legal card does not leave an orphan page
	// behind.
	rewritten := decisionRecordPushRequest(request, record, "")
	if err := rewritten.Validate(ctx); err != nil {
		return request, errors.Wrap(ctx, err, "validate decision record card failed")
	}
	pagePath, err := writer.Write(ctx, record)
	if err != nil {
		return request, errors.Wrap(ctx, err, "write decision record failed")
	}
	return decisionRecordPushRequest(request, record, pagePath), nil
}

// decisionRecordPushRequest builds the acknowledgement card. pagePath is empty
// on the pre-write validation pass, in which case the card points at the
// record's title rather than a path that does not exist yet.
func decisionRecordPushRequest(
	request PushRequest,
	record DecisionRecord,
	pagePath string,
) PushRequest {
	reference := pagePath
	if reference == "" {
		reference = record.Title
	}
	return PushRequest{
		ProducerID:      request.ProducerID,
		ProducerKind:    request.ProducerKind,
		ProvenanceClass: request.ProvenanceClass,
		LivenessRef:     request.LivenessRef,
		DedupKey:        request.DedupKey,
		InterruptClass:  AckInterruptClass,
		Risk:            ElevatedRisk,
		Payload:         Payload(decisionWaitingForAckPrefix + reference),
		// The blast radius rides the context so the operator sees the cost on
		// the card, while the full reasoning lives on the page.
		Context: ItemContext(record.BlastRadius),
		// An acknowledgement carries no choices, no question units and no
		// cardinality: the only act is to open the page and decide there.
		AnswerMechanism:   AckAnswerMechanism,
		Options:           nil,
		AnswerCardinality: "",
		Questions:         nil,
		ExpiresAt:         request.ExpiresAt,
		AskedAt:           request.AskedAt,
	}
}
