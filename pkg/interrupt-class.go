// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// The interrupt-class rungs the codebase declares. InterruptClass is stored,
// never derived, and the store does not reject an unknown value — these name
// the rungs the producers already use so the mint-path normalisation can speak
// in them rather than in bare strings.
const (
	// PickInterruptClass declares a card that offers the operator a choice to
	// select. A card with no selectable option at all is not a pick.
	PickInterruptClass InterruptClass = "pick"
	// AckInterruptClass declares a card the operator acknowledges rather than
	// chooses from.
	AckInterruptClass InterruptClass = "ack"
	// ApproveInterruptClass declares a permission gate.
	ApproveInterruptClass InterruptClass = "approve"
)

// normalizeInterruptClass keeps a card's declared class honest at the mint
// path: `pick` promises the operator a choice, so a card carrying no
// selectable option — neither a top-level option nor a per-question one — is
// not a pick and is normalised to `ack`.
//
// ⚠️ It normalises the CLASS, never manufactures options. Rendering placeholder
// options on a card with no real choice leaves the operator facing a
// meaningless selection, which is the defect this exists to remove.
func normalizeInterruptClass(request PushRequest) PushRequest {
	if request.InterruptClass == PickInterruptClass && !hasSelectableOptions(request) {
		request.InterruptClass = AckInterruptClass
	}
	return request
}

// hasSelectableOptions reports whether the request offers the operator at
// least one choice, either as its single top-level option set or on any of its
// per-tab questions.
func hasSelectableOptions(request PushRequest) bool {
	if len(request.Options) > 0 {
		return true
	}
	for _, question := range request.Questions {
		if len(question.Options) > 0 {
			return true
		}
	}
	return false
}
