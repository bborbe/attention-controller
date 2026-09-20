// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// LivenessModel is which of the two liveness models a ref uses. The two exist
// because the same on-disk fact — "the producer is not running" — means
// opposite things for the two classes.
type LivenessModel string

const (
	// SessionLivenessModel is a long-lived producer whose absence is
	// meaningful. An item it *asked* is removed; an item it *reported* is kept.
	SessionLivenessModel LivenessModel = "session"
	// HeartbeatLivenessModel is a short-lived producer that exits by design. A
	// stale heartbeat means the producer is gone, and its *report* stays.
	HeartbeatLivenessModel LivenessModel = "heartbeat"
)

// LivenessModels is a collection of LivenessModel.
type LivenessModels []LivenessModel

// AvailableLivenessModels holds every legal liveness model.
var AvailableLivenessModels = LivenessModels{
	SessionLivenessModel,
	HeartbeatLivenessModel,
}

// String returns the liveness model as a string.
func (l LivenessModel) String() string {
	return string(l)
}

// Validate returns an error when the model is not one of AvailableLivenessModels.
func (l LivenessModel) Validate(ctx context.Context) error {
	if !AvailableLivenessModels.Contains(l) {
		return errors.Wrapf(ctx, validation.Error, "unknown livenessModel '%s'", l)
	}
	return nil
}

// Contains reports whether the collection holds the given liveness model.
func (l LivenessModels) Contains(livenessModel LivenessModel) bool {
	return collectionContains(l, livenessModel)
}

// Parse splits a liveness ref into its model and its value. The ref is written
// `<model>:<value>` — `session:<id>` or `heartbeat:<path>`. A ref that is not
// one of the two models, or that carries no value, is rejected.
func (l LivenessRef) Parse(ctx context.Context) (LivenessModel, string, error) {
	model, value, found := strings.Cut(l.String(), ":")
	if !found {
		return "", "", errors.Wrapf(
			ctx,
			validation.Error,
			"livenessRef '%s' is not '<model>:<value>'",
			l,
		)
	}
	livenessModel := LivenessModel(model)
	if err := livenessModel.Validate(ctx); err != nil {
		return "", "", errors.Wrapf(ctx, err, "invalid livenessRef '%s'", l)
	}
	if value == "" {
		return "", "", errors.Wrapf(
			ctx,
			validation.Error,
			"livenessRef '%s' carries no value",
			l,
		)
	}
	return livenessModel, value, nil
}
