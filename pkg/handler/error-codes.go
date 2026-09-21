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
)
