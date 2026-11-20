// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"time"

	libtime "github.com/bborbe/time"
)

// managerRepostExpiry bounds a re-presented card: an unanswered re-post must not
// stay on the board forever.
const managerRepostExpiry = 24 * time.Hour

// managerRepostInterruptClass is the interrupt class a re-post declares. It
// matches the unforwarded-closer scanner's own re-presentation.
const managerRepostInterruptClass InterruptClass = "pick"

// managerRepostPushRequest renders a re-post as a store push that passes
// PushRequest.Validate.
//
// ⚠️ The obvious six-field form is REJECTED and the arm would silently never
// post a card, so every field Validate names is filled: ProducerKind is a real
// kind, ProvenanceClass and Risk a real class, and the liveness ref is
// `session:<worker>` — never a bare `owner:` with nothing after the colon.
func managerRepostPushRequest(
	request ManagerRepostRequest,
	now libtime.DateTime,
) PushRequest {
	expiresAt := now.Add(libtime.Duration(managerRepostExpiry))
	return PushRequest{
		ProducerID:      ProducerID(request.ProducerID),
		ProducerKind:    CronProducerKind,
		ProvenanceClass: ExplicitProvenanceClass,
		LivenessRef:     LivenessRef("session:" + request.ProducerID),
		DedupKey:        request.DedupKey,
		InterruptClass:  managerRepostInterruptClass,
		Risk:            DecisionRisk,
		Payload:         request.Payload,
		AnswerMechanism: request.AnswerMechanism,
		Options:         request.Options,
		ExpiresAt:       &expiresAt,
	}
}
