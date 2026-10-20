// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// AnswerOption is one choice a `message` item offers the operator: the label the
// board renders, and whether the producer recommends it.
//
// It exists because a `message` item is answerable on a surface that is not the
// asker's own tab, and that surface has to know what to render. A list of labels
// is machine handle-shaped, so it cannot ride Payload, whose contract is plain
// operator-facing sentences carrying no machine handles.
type AnswerOption struct {
	// Label is the choice as the operator reads it.
	Label string `json:"label"`
	// Description is the muted line the board renders under the label — what
	// the option does and what it costs.
	//
	// It is optional, and an option carrying none renders its label alone, so
	// every option list pushed before this field existed reads as it did. It
	// belongs here rather than on the item because it is per-option: two
	// options of one question carry different costs, and a single item-level
	// line would have to describe both at once.
	Description string `json:"description,omitempty"`
	// Recommended marks the producer's recommendation. At most one option may
	// carry it when the question takes a single pick; a multiple-pick question
	// may carry several, one per option the asker would take.
	Recommended bool `json:"recommended"`
}

// Validate returns an error when the option carries no label.
func (o AnswerOption) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Label", validation.NotEmptyString(o.Label)),
	}.Validate(ctx)
}

// AnswerOptions is an ordered list of AnswerOption.
type AnswerOptions []AnswerOption

// Validate returns an error when any option carries no label, or when more than
// one carries Recommended on a list whose cardinality is absent — which reads
// as `single`. A caller holding a cardinality calls ValidateForCardinality so
// the rule sees the unit's own declaration.
//
// An empty list validates: `options` is optional, and an absent value is what
// every `permission`- and `ack`-class item carries.
func (o AnswerOptions) Validate(ctx context.Context) error {
	return o.ValidateForCardinality(ctx, "")
}

// ValidateForCardinality returns an error when any option carries no label, or
// when more than one carries Recommended on a unit that takes a single pick.
//
// A multiple-pick unit may carry any number of recommendations: the /ask rule
// tells a multi-select asker to mark each option it would take, and the board
// renders every flag it is handed. Only MultipleAnswerCardinality relaxes the
// bound; `single`, an absent value and an unknown value all keep it.
func (o AnswerOptions) ValidateForCardinality(
	ctx context.Context,
	cardinality AnswerCardinality,
) error {
	return validation.All{
		validation.Name("Labels", validation.HasValidationFunc(o.validateLabels)),
		validation.Name(
			"Recommended",
			validation.HasValidationFunc(func(ctx context.Context) error {
				return o.validateRecommended(ctx, cardinality)
			}),
		),
	}.Validate(ctx)
}

// validateLabels returns an error when any option in the list is itself invalid.
func (o AnswerOptions) validateLabels(ctx context.Context) error {
	for _, option := range o {
		if err := option.Validate(ctx); err != nil {
			return errors.Wrap(ctx, err, "validate answer option failed")
		}
	}
	return nil
}

// validateRecommended returns an error when more than one option carries
// Recommended on a unit that takes a single pick. A multiple-pick unit is
// unconstrained: every option the asker marked is a recommendation the board
// should show.
func (o AnswerOptions) validateRecommended(
	ctx context.Context,
	cardinality AnswerCardinality,
) error {
	if cardinality == MultipleAnswerCardinality {
		return nil
	}
	recommended := 0
	for _, option := range o {
		if option.Recommended {
			recommended++
		}
	}
	if recommended > 1 {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"%d options are marked recommended, at most 1 is allowed",
			recommended,
		)
	}
	return nil
}
