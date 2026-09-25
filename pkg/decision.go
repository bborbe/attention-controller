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

// Decision is what an answer decided. It exists because an arm is not a
// decision: AnsweredBy names the arm that supplied the answer, and the same arm
// supplies an allow and a deny, so before this field an item answered allow and
// one answered deny were the same record and "was this answered" could not be
// told from "what was decided".
//
// It is optional, and the schema adds no write-time rejection for an omitted
// value. An empty Decision therefore stores an item with no verdict recorded —
// which is what every item answered before this field existed reads as, and what
// a message- or ack-class item reads as, since neither carries a
// machine-readable verdict.
type Decision string

const (
	// AllowDecision is the decision to let the parked call proceed.
	AllowDecision Decision = "allow"
	// DenyDecision is the decision to refuse it.
	DenyDecision Decision = "deny"
)

// Decisions is a collection of Decision.
type Decisions []Decision

// AvailableDecisions holds every legal decision.
var AvailableDecisions = Decisions{
	AllowDecision,
	DenyDecision,
}

// String returns the decision as a string.
func (d Decision) String() string {
	return string(d)
}

// Validate returns an error when the decision is neither legal value.
//
// An empty value validates: it is absent rather than unknown, and the schema
// deliberately does not reject an omitted decision, for the same reason it does
// not reject an omitted provenance class.
func (d Decision) Validate(ctx context.Context) error {
	if d == "" {
		return nil
	}
	if !AvailableDecisions.Contains(d) {
		return errors.Wrapf(ctx, validation.Error, "unknown decision '%s'", d)
	}
	return nil
}

// Contains reports whether the collection holds the given decision.
func (d Decisions) Contains(decision Decision) bool {
	return collection.Contains(d, decision)
}
