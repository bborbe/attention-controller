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

// ProvenanceClass is where the claim came from — declared by the producer,
// stored, never derived. It answers "how was this claim obtained", which is
// distinct from ProducerKind's "what kind of thing asked".
//
// An empty ProvenanceClass is accepted: it marks a pre-2026-09-23 item pushed
// before this field existed. See the attention item schema § Fields,
// `provenance_class`, and silence 9.
type ProvenanceClass string

const (
	// HookProvenanceClass is a harness hook event: PermissionRequest,
	// AskUserQuestion.
	HookProvenanceClass ProvenanceClass = "hook"
	// ExplicitProvenanceClass is an explicit producer call carrying a stable
	// id.
	ExplicitProvenanceClass ProvenanceClass = "explicit"
	// ThirdPartyProvenanceClass is built from a third party's rendered text —
	// a peer's pane, a board, someone else's log line. Disallowed by the
	// stack's own rule; it exists only so its absence can be counted.
	ThirdPartyProvenanceClass ProvenanceClass = "third_party"
)

// ProvenanceClasses is a collection of ProvenanceClass.
type ProvenanceClasses []ProvenanceClass

// AvailableProvenanceClasses holds every legal provenance class.
var AvailableProvenanceClasses = ProvenanceClasses{
	HookProvenanceClass,
	ExplicitProvenanceClass,
	ThirdPartyProvenanceClass,
}

// String returns the provenance class as a string.
func (p ProvenanceClass) String() string {
	return string(p)
}

// Validate returns an error when the class is neither empty nor one of
// AvailableProvenanceClasses. Empty is accepted deliberately — the schema does
// not reject a push that omits it, since an absent value marks a pre-change
// item.
func (p ProvenanceClass) Validate(ctx context.Context) error {
	if p == "" {
		return nil
	}
	if !AvailableProvenanceClasses.Contains(p) {
		return errors.Wrapf(ctx, validation.Error, "unknown provenanceClass '%s'", p)
	}
	return nil
}

// Contains reports whether the collection holds the given provenance class.
func (p ProvenanceClasses) Contains(provenanceClass ProvenanceClass) bool {
	return collection.Contains(p, provenanceClass)
}
