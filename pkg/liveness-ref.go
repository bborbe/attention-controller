// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// LivenessModel is which liveness model a ref uses. There are three models but
// only two probes: the models exist because the same on-disk fact — "the
// producer is not running" — means opposite things for the classes, while the
// probe that answers it does not change with the class.
type LivenessModel string

const (
	// SessionLivenessModel is a long-lived producer whose absence is
	// meaningful. An item it *asked* is removed; an item it *reported* is kept.
	SessionLivenessModel LivenessModel = "session"
	// HeartbeatLivenessModel is a short-lived producer that exits by design. A
	// stale heartbeat means the producer is gone, and its *report* stays.
	HeartbeatLivenessModel LivenessModel = "heartbeat"
	// OwnerLivenessModel is a producer that is *supposed* to exit, whose
	// question is the operator's to answer rather than its own — a worker that
	// posts an operator gate and then ends its turn. The item's survival is the
	// owner's, not the producer's, and its answer routes to the owner.
	//
	// It is a third MODEL and only the second PROBE: it resolves against the
	// same session registry SessionLivenessModel reads, and differs only in
	// whose survival is tested. See [[Attention Item Schema]] § How liveness is
	// checked.
	OwnerLivenessModel LivenessModel = "owner"
)

// LivenessModels is a collection of LivenessModel.
type LivenessModels []LivenessModel

// AvailableLivenessModels holds every legal liveness model.
var AvailableLivenessModels = LivenessModels{
	SessionLivenessModel,
	HeartbeatLivenessModel,
	OwnerLivenessModel,
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
	return collection.Contains(l, livenessModel)
}

// Parse splits a liveness ref into its model and its value. The ref is written
// `<model>:<value>` — `session:<id>`, `owner:<id>` or `heartbeat:<path>`. A ref
// that is not one of the three models, or that carries no value, is rejected.
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
