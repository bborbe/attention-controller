// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// CloseReason is why an item left the queue without an answer.
//
// It exists because ClosedAt records that an item left but not which closer
// moved it, and for a card that was never answered that is the whole question:
// several mechanisms close items, and before this field the store could not say
// which one had. It is a name for a PATH, never a constant — a value such as
// "closed" or "swept" would satisfy a non-empty check while telling a reader
// nothing, which is the shape the field exists to remove.
//
// It is optional. An empty CloseReason stores an item whose close was caused by
// an answer, which AnsweredAt and AnsweredBy already record, and which every
// item closed before this field existed reads as.
type CloseReason string

const (
	// RedundantReraiseCloseReason is a re-raise whose ask had already been
	// answered, closed by closeRedundantReraises.
	RedundantReraiseCloseReason CloseReason = "redundant_reraise"
	// UnanswerableCloseReason is a row whose ask could not be captured.
	UnanswerableCloseReason CloseReason = "unanswerable"
)

// CloseReasons is a collection of CloseReason.
type CloseReasons []CloseReason

// AvailableCloseReasons holds every legal close reason.
var AvailableCloseReasons = CloseReasons{
	RedundantReraiseCloseReason,
	UnanswerableCloseReason,
}

// String returns the close reason as a string.
func (c CloseReason) String() string {
	return string(c)
}

// Validate returns an error when the close reason is not a legal value.
//
// An empty value validates: it is absent rather than unknown, and the schema
// deliberately does not reject an omitted reason, for the same reason it does
// not reject an omitted decision.
func (c CloseReason) Validate(ctx context.Context) error {
	if c == "" {
		return nil
	}
	if !AvailableCloseReasons.Contains(c) {
		return errors.Wrapf(ctx, validation.Error, "unknown close reason '%s'", c)
	}
	return nil
}

// Contains reports whether the collection holds the given close reason.
func (c CloseReasons) Contains(reason CloseReason) bool {
	return collection.Contains(c, reason)
}
