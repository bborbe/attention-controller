// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// Error codes this service defines for failure modes libhttp has no constant
// for. They live here rather than inline at the call site so a typo fails at
// compile time and every use is greppable from one place — which is the whole
// point of libhttp's own ErrorCodeXxx set.
//
// They are deliberately not folded into the generic libhttp codes: a caller
// that lost an answer race must be able to tell that apart from a validation
// failure, and collapsing them onto ErrorCodeValidation would erase exactly the
// distinction the compare-and-set exists to report.
//
// ⚠️ They live in `pkg` rather than `pkg/handler` because they are the wire
// names of this package's own sentinel errors — ErrAlreadyAnswered,
// ErrIllegalTransition and ErrItemNotFound are declared here, and a code is
// what those sentinels read as on the wire. A client that maps a response BACK
// into the sentinel cannot import `pkg/handler`: handler imports pkg, so the
// reverse edge is a cycle. Pairing each code with the sentinel it names is what
// lets a response round-trip an error faithfully instead of collapsing the two
// 409s.
const (
	// ErrorCodeAlreadyEscalated is the code a caller receives when another
	// session stamped the item first. Distinct from ErrorCodeItemNotOpen: the
	// item is still open and renderable, it is simply already carried.
	ErrorCodeAlreadyEscalated = "ALREADY_ESCALATED"

	// ErrorCodeItemNotOpen is the code a caller receives when escalating an
	// item that has already left the queue, so nothing would render the stamp.
	ErrorCodeItemNotOpen = "ITEM_NOT_OPEN"

	// ErrorCodeAlreadyAnswered is the code an arm receives when another arm
	// answered the item first.
	ErrorCodeAlreadyAnswered = "ALREADY_ANSWERED"

	// ErrorCodeItemClosed is the code an arm receives when it answers an item
	// that left the queue between the render and the answer — the
	// render-snapshot race. The item was real and open when the arm drew it; the
	// producer's exit closed it underneath the arm before the answer arrived.
	//
	// It is deliberately not ErrorCodeValidation. The request was well formed
	// and named a real item, so reporting it as a malformed request asks the
	// caller to correct something that is not wrong — and the operator reading
	// that body cannot tell "I sent it wrong" from "this had already gone".
	//
	// It is deliberately not ErrorCodeAlreadyAnswered either. A lost race means
	// another arm answered and the answer was routed; this means nobody did and
	// nothing was routed. The store keeps the two apart — ErrAlreadyAnswered
	// names the arm that won, ErrIllegalTransition names no arm because there
	// was none — and collapsing them here would erase the distinction between
	// "already handled" and "no longer exists".
	//
	// ⚠️ Both are HTTP 409, which is why a client reads this field
	// rather than the status: a status-only mapping cannot tell them apart, and
	// an answer that lost a race would report "no longer exists".
	//
	// The response carries the item's closed_at, because the terminal state is
	// the whole of what the caller can act on: the item is not coming back, and
	// the arm's next move is to return to the queue rather than to retry.
	ErrorCodeItemClosed = "ITEM_CLOSED"

	// ErrorCodePermissionCloseWithoutDecision is the code a caller receives when
	// it closes an open `permission` item naming an arm and carrying no decision.
	//
	// It is deliberately not ErrorCodeValidation, which is what it used to
	// arrive as. The request was well formed and named a real item, so reporting
	// it as a malformed request asks the caller to correct something that is not
	// wrong — and the caller cannot tell "this transition does not exist" from
	// "this close is not an acceptable way to end a gate".
	//
	// It is deliberately not the illegal-transition reading either: `open ->
	// closed` IS a row of the schema's table, so the transition exists and is
	// merely not one that records a decision. The refusal is keyed on the
	// stored item's mechanism, never on anything the caller supplied.
	ErrorCodePermissionCloseWithoutDecision = "PERMISSION_CLOSE_WITHOUT_DECISION"

	// ErrorCodeHeldCardCloseNeedsArm is the code a caller receives when it
	// closes a HELD card — an answer recorded but not yet delivered — without
	// naming the arm that caused the close.
	//
	// It is deliberately not ErrorCodeValidation: the request was well formed
	// and named a real item, and the caller's fix is to name its arm (or to
	// leave the card to the delivery arm) rather than to correct a body.
	// It is deliberately not ITEM_CLOSED or ALREADY_ANSWERED either: the card
	// is still open, and no answer was routed.
	ErrorCodeHeldCardCloseNeedsArm = "HELD_CARD_CLOSE_NEEDS_ARM"

	// ErrorCodeFlagReasonRequired is the code a caller receives when a flag
	// carries no usable reason. Distinct from ErrorCodeValidation: the request
	// may be well-formed JSON and still name no reason, and the caller's fix is
	// to supply the reason rather than to correct a malformed body.
	ErrorCodeFlagReasonRequired = "FLAG_REASON_REQUIRED"

	// ErrorCodeRatingInvalid is the code a caller receives when a rating is not
	// "up", "down" or "" (clear).
	ErrorCodeRatingInvalid = "RATING_INVALID"

	// ErrorCodeOperatorTierLowerArm is the code a caller receives when an arm
	// that is not the operator's tries to answer or close an operator-tier
	// message card — an `approve`/`decision` card, or a `pick` the placement
	// table rates operator.
	//
	// It is deliberately not ErrorCodeValidation: the request was well formed
	// and named a real, open item, so the caller's fix is to leave the card to
	// the operator rather than to correct a body. It is deliberately not
	// ITEM_CLOSED or ALREADY_ANSWERED either — the card is still open and no
	// answer was routed.
	ErrorCodeOperatorTierLowerArm = "OPERATOR_TIER_LOWER_ARM"
)
