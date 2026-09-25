// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// Answer is what the operator actually said on a `message` item.
//
// It is store-written on the existing open -> answered transition, exactly as
// AnsweredAt, AnsweredBy and Decision are. Decision carries a `permission`
// item's verdict and Answer carries a `message` item's content; neither
// substitutes for the other, because an allow/deny verdict and a chosen label
// are different kinds of thing.
type Answer struct {
	// Kind is the shape the answer took. Empty means no answer content was
	// recorded, which is what a `permission`- or `ack`-class item reads as, and
	// what every item answered before this field existed reads as.
	Kind AnswerKind `json:"kind,omitempty"`
	// Value carries the chosen option's label for `option`, the operator's words
	// for `text`, and nothing for `skip`.
	Value string `json:"value,omitempty"`
}

// Validate returns an error when the kind and the value disagree with the shape
// the kind describes.
//
// The three rules are the schema's own descriptions of the kinds: an `option`
// carries the label of the entry that was chosen, a `text` carries the
// operator's words, and a `skip` carries nothing. An empty Kind validates — the
// schema adds no write-time rejection for an omitted answer, so an absent value
// reads as a pre-change item rather than as an error.
func (a Answer) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Kind", a.Kind),
		validation.Name("Value", validation.HasValidationFunc(a.validateValue)),
	}.Validate(ctx)
}

// validateValue returns an error when the value does not match the shape the
// declared kind describes.
func (a Answer) validateValue(ctx context.Context) error {
	switch a.Kind {
	case OptionAnswerKind, TextAnswerKind:
		if err := validation.NotEmptyString(a.Value).Validate(ctx); err != nil {
			return errors.Wrapf(ctx, err, "answerKind '%s' requires a value", a.Kind)
		}
	case SkipAnswerKind:
		if a.Value != "" {
			return errors.Wrapf(
				ctx,
				validation.Error,
				"answerKind 'skip' carries no value, got '%s'",
				a.Value,
			)
		}
	case "":
		// Absent, not unknown: the schema declines to reject an omitted answer.
	}
	return nil
}
