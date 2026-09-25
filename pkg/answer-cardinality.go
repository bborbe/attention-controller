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

// AnswerCardinality is whether a question takes one pick or many.
//
// It is producer-declared and never derived. The board cannot infer it from the
// option count: a one-option question and a many-option single-pick question
// are different shapes carrying lists of different lengths, so a board that
// read the length would render a checkbox for a question that admits one answer
// and silently accept a set where a value was asked for.
type AnswerCardinality string

const (
	// SingleAnswerCardinality is a question the operator answers with exactly
	// one pick. The board renders a radio button.
	SingleAnswerCardinality AnswerCardinality = "single"
	// MultipleAnswerCardinality is a question the operator may answer with
	// several picks. The board renders a checkbox.
	MultipleAnswerCardinality AnswerCardinality = "multiple"
)

// AnswerCardinalities is a collection of AnswerCardinality.
type AnswerCardinalities []AnswerCardinality

// AvailableAnswerCardinalities holds every legal cardinality.
var AvailableAnswerCardinalities = AnswerCardinalities{
	SingleAnswerCardinality,
	MultipleAnswerCardinality,
}

// String returns the cardinality as a string.
func (a AnswerCardinality) String() string {
	return string(a)
}

// Validate returns an error when the cardinality is neither declared value.
//
// An empty value validates. The schema adds no write-time rejection for an
// omitted cardinality, so an absent value reads as a pre-change item — the same
// choice silences 9, 11 and 12 made, and for the same reason.
func (a AnswerCardinality) Validate(ctx context.Context) error {
	if a == "" {
		return nil
	}
	if !AvailableAnswerCardinalities.Contains(a) {
		return errors.Wrapf(ctx, validation.Error, "unknown answerCardinality '%s'", a)
	}
	return nil
}

// Contains reports whether the collection holds the given cardinality.
func (a AnswerCardinalities) Contains(answerCardinality AnswerCardinality) bool {
	return collection.Contains(a, answerCardinality)
}
