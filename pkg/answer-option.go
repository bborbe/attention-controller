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
	// Recommended marks the producer's recommendation. At most one option in a
	// list may carry it.
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
// one carries Recommended.
//
// The single-recommendation rule is the schema's: an ordered list of
// {label, recommended} with at most one entry marked. Two recommendations
// describe a `pick` shape whose recommended first option is ambiguous, which is
// the one thing that shape exists to remove.
//
// An empty list validates: `options` is optional, and an absent value is what
// every `permission`- and `ack`-class item carries.
func (o AnswerOptions) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Labels", validation.HasValidationFunc(o.validateLabels)),
		validation.Name("Recommended", validation.HasValidationFunc(o.validateSingleRecommended)),
	}.Validate(ctx)
}

// validateLabels returns an error when any option in the list is itself invalid.
func (o AnswerOptions) validateLabels(ctx context.Context) error {
	for _, option := range o {
		if err := option.Validate(ctx); err != nil {
			return err
		}
	}
	return nil
}

// validateSingleRecommended returns an error when more than one option carries
// Recommended.
func (o AnswerOptions) validateSingleRecommended(ctx context.Context) error {
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
