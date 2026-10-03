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

// ProducerKind is what kind of thing the producer is. It exists so a
// per-producer interrupt rate can be measured later without a schema change.
type ProducerKind string

const (
	// SessionProducerKind is a Claude Code session.
	SessionProducerKind ProducerKind = "session"
	// ManagerProducerKind is a manager session — the layer that resolves
	// prompts between a worker and the operator. It is deliberately distinct
	// from SessionProducerKind, because "a manager asked" and "a worker
	// asked" are different populations, and the per-producer rate this field
	// exists to measure is only useful if they can be told apart.
	//
	// ⚠️ It is a *kind*, never a route. The jump back to a manager's pane
	// resolves from LivenessRef — pkg/provenance.go never reads ProducerKind
	// — so adding this value grants no jump and widens no liveness model.
	// A `pane:` liveness model stays rejected: a pane id is a lease, so a
	// liveness verdict keyed on one resolves to another session's pane
	// rather than failing.
	ManagerProducerKind ProducerKind = "manager"
	// AgentProducerKind is a k8s Pattern B agent.
	AgentProducerKind ProducerKind = "agent"
	// CronProducerKind is a cron job.
	CronProducerKind ProducerKind = "cron"
	// DarkFactoryProducerKind is a dark-factory run.
	DarkFactoryProducerKind ProducerKind = "dark-factory"
)

// ProducerKinds is a collection of ProducerKind.
type ProducerKinds []ProducerKind

// AvailableProducerKinds holds every legal producer kind.
var AvailableProducerKinds = ProducerKinds{
	SessionProducerKind,
	ManagerProducerKind,
	AgentProducerKind,
	CronProducerKind,
	DarkFactoryProducerKind,
}

// String returns the producer kind as a string.
func (p ProducerKind) String() string {
	return string(p)
}

// Validate returns an error when the kind is not one of AvailableProducerKinds.
func (p ProducerKind) Validate(ctx context.Context) error {
	if !AvailableProducerKinds.Contains(p) {
		return errors.Wrapf(ctx, validation.Error, "unknown producerKind '%s'", p)
	}
	return nil
}

// Contains reports whether the collection holds the given producer kind.
func (p ProducerKinds) Contains(producerKind ProducerKind) bool {
	return collection.Contains(p, producerKind)
}
