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

// AnswerKind is what shape the operator's answer took on a `message` item.
//
// It exists because AnsweredAt and AnsweredBy record *that* an answer arrived
// and *which arm* supplied it, and Decision is permission-only. Without it an
// item answered "option 2" and one answered "skip" were the same record — the
// same defect Decision fixed one class over, where an allow and a deny were
// indistinguishable on the record.
type AnswerKind string

const (
	// OptionAnswerKind is a chosen entry from the item's options. Value carries
	// the chosen label.
	OptionAnswerKind AnswerKind = "option"
	// SkipAnswerKind is a declined answer. Value is empty.
	SkipAnswerKind AnswerKind = "skip"
	// TextAnswerKind is free text. Value carries it.
	TextAnswerKind AnswerKind = "text"
)

// AnswerKinds is a collection of AnswerKind.
type AnswerKinds []AnswerKind

// AvailableAnswerKinds holds every legal answer kind.
var AvailableAnswerKinds = AnswerKinds{
	OptionAnswerKind,
	SkipAnswerKind,
	TextAnswerKind,
}

// String returns the answer kind as a string.
func (a AnswerKind) String() string {
	return string(a)
}

// Validate returns an error when the kind is neither legal value.
//
// An empty value validates: it is absent rather than unknown, and the schema
// deliberately does not reject an omitted answer, for the same reason it does
// not reject an omitted decision or provenance class.
func (a AnswerKind) Validate(ctx context.Context) error {
	if a == "" {
		return nil
	}
	if !AvailableAnswerKinds.Contains(a) {
		return errors.Wrapf(ctx, validation.Error, "unknown answerKind '%s'", a)
	}
	return nil
}

// Contains reports whether the collection holds the given answer kind.
func (a AnswerKinds) Contains(answerKind AnswerKind) bool {
	return collection.Contains(a, answerKind)
}
