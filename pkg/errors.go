// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	stderrors "errors"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

var (
	// ErrItemNotFound is returned when no item carries the requested id.
	ErrItemNotFound = stderrors.New("item not found")

	// ErrIllegalTransition is returned when a move is not a row of the schema's
	// transitions table — it is illegal from every state, not merely from the
	// one the item happens to be in.
	ErrIllegalTransition = stderrors.New("illegal transition")

	// ErrAlreadyAnswered is returned when an answer write loses the
	// compare-and-set: the item was no longer open, so another arm answered it
	// first. The loser reads back and reports; it must not retry and must not
	// route its own answer anyway.
	ErrAlreadyAnswered = stderrors.New("item already answered")

	// ErrAlreadyEscalated is returned when an escalation write loses the
	// compare-and-set: another session stamped the item first. The loser reads
	// back EscalatedBy and reports who holds it; it must not stamp over it,
	// which would erase the only record of who is carrying the item.
	//
	// This is NOT returned when the loser is the session that already stamped
	// the item — a manager re-running its own sweep must never be blocked by
	// its own stamp.
	ErrAlreadyEscalated = stderrors.New("item already escalated")

	// ErrItemNotOpen is returned when escalation is attempted on an item that
	// has already left the queue.
	//
	// It is deliberately not ErrIllegalTransition: escalation is not a
	// transition, and the item's state is untouched by it. The schema says a
	// stamp on a closed item is unreachable because the item has left the
	// queue, so the store refuses rather than stamping an item nothing renders.
	ErrItemNotOpen = stderrors.New("item not open")

	// ErrInvalidLivenessRef is returned when a liveness ref is neither of the
	// two models, or carries no value.
	ErrInvalidLivenessRef = stderrors.New("invalid liveness ref")
)

// isNotFound reports whether a libkv read failed because the thing asked for
// does not exist — either the key is absent, or the bucket itself has never
// been created.
//
// Both cases are one answer to the caller. A store that has never been written
// to has no bucket at all, and libkv reports that as a bucket error rather than
// a key error: `tx.Bucket()` fails before the key is ever consulted. Treating
// only the key case as not-found would make the very first `Get` against an
// empty store surface a 500 instead of a 404.
func isNotFound(err error) bool {
	return errors.Is(err, libkv.ErrKeyNotFound) || errors.Is(err, libkv.ErrBucketNotFound)
}
