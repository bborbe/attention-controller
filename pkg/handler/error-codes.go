// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

// Error codes this service defines for failure modes libhttp has no constant
// for. They live here rather than inline at the call site so a typo fails at
// compile time and every use is greppable from one place — which is the whole
// point of libhttp's own ErrorCodeXxx set.
//
// They are deliberately not folded into the generic libhttp codes: a caller
// that lost an answer race must be able to tell that apart from a validation
// failure, and collapsing them onto ErrorCodeValidation would erase exactly the
// distinction the compare-and-set exists to report.
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
	// The response carries the item's closed_at, because the terminal state is
	// the whole of what the caller can act on: the item is not coming back, and
	// the arm's next move is to return to the queue rather than to retry.
	ErrorCodeItemClosed = "ITEM_CLOSED"
)
