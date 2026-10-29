// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
)

// DeliveryOutcome is what an attempting arm observed about one answer. It holds
// three values, and each is a distinct thing an arm can report: it delivered the
// answer, it tried and failed to, or it saw the gate and declined to answer it.
//
// ⚠️ The other two states a reader needs — `never_attempted` and
// `pre_trail_unknown` — are NOT outcomes and are never stored. Nobody is
// present to write them: an item no arm ever picked up has no writer by
// definition. They are derived at read time; see DeliveryStatus.
type DeliveryOutcome string

const (
	// DeliveredOutcome is an attempt that reached the asking session.
	DeliveredOutcome DeliveryOutcome = "delivered"
	// FailedOutcome is an attempt that did not: the target was unresolvable,
	// the name was ambiguous, or the send itself failed.
	FailedOutcome DeliveryOutcome = "failed"
	// HeldOutcome is an arm that SAW the gate and deliberately did not answer
	// it, leaving it open for the operator.
	//
	// ⚠️ It is neither of the other two, and collapsing it into either is the
	// defect it exists to prevent. It is not `delivered` — nothing was answered.
	// It is not `failed` — nothing went wrong, and reading it as a failure puts a
	// broken card in front of an operator for a gate a rule held on purpose. The
	// rule that held it is named in the carrier, because this record carries no
	// free-text field of its own.
	HeldOutcome DeliveryOutcome = "held"
)

// DeliveryOutcomes is a collection of DeliveryOutcome.
type DeliveryOutcomes []DeliveryOutcome

// AvailableDeliveryOutcomes holds every legal outcome.
var AvailableDeliveryOutcomes = DeliveryOutcomes{DeliveredOutcome, FailedOutcome, HeldOutcome}

// String returns the outcome as a string.
func (o DeliveryOutcome) String() string {
	return string(o)
}

// Validate returns an error when the outcome is not one of
// AvailableDeliveryOutcomes.
func (o DeliveryOutcome) Validate(ctx context.Context) error {
	if !AvailableDeliveryOutcomes.Contains(o) {
		return errors.Wrapf(ctx, validation.Error, "unknown delivery outcome '%s'", o)
	}
	return nil
}

// Contains reports whether the collection holds the given outcome.
func (o DeliveryOutcomes) Contains(outcome DeliveryOutcome) bool {
	return collection.Contains(o, outcome)
}

// DeliveryStatus is the reader-visible answer to "did my answer reach the
// session?" — the five values the read surface may return.
//
// ⚠️ DeliveredStatus, FailedStatus and HeldStatus are STORED, one per outcome an
// arm can write. The other two are
// derived by the store from the item's own CreatedAt against
// DeliveryTrailEpoch, because an absent record is otherwise the only encoding
// available for both of them — and that is precisely what the operator could
// not read: *no carrier ever looked* and *this predates the trail* are
// different answers, and a record that renders them identically fails the
// question it was built for.
type DeliveryStatus string

const (
	// DeliveredStatus is an attempt record whose arm reported success.
	DeliveredStatus DeliveryStatus = "delivered"
	// FailedStatus is an attempt record whose arm reported failure.
	FailedStatus DeliveryStatus = "failed"
	// NeverAttemptedStatus is an item with no attempt record, created after the
	// trail shipped — no arm ever picked its answer up. Derived, never written.
	NeverAttemptedStatus DeliveryStatus = "never_attempted"
	// PreTrailUnknownStatus is an item created before the trail shipped. Its
	// answer's fate was never measured, and this reports that honestly rather
	// than guessing. Derived, never written.
	PreTrailUnknownStatus DeliveryStatus = "pre_trail_unknown"
	// HeldStatus is an attempt record whose arm declined to answer the gate and
	// left it for the operator. Stored, like the other two, because it is a
	// fact some arm observed rather than one derived from absence.
	//
	// ⚠️ It is NOT FailedStatus. A held gate is working as designed: the card
	// still needs the operator, and telling them the delivery failed would send
	// them looking for a fault that is not there.
	HeldStatus DeliveryStatus = "held"
)

// DeliveryTrailEpoch is the instant the delivery-attempt record first shipped.
//
// It is the whole of the never-attempted / pre-trail distinction: an item
// created before this instant has no attempt record because none could have
// been written, and one created after it has none because no arm ever picked
// the answer up.
//
// ⚠️ It is deliberately a fixed instant rather than "now" or a store-start
// time — a value that moved would reclassify history on every restart, and an
// item's fate is not a function of when the store was last bounced.
//
// ⚠️ **It is the ship INSTANT, not the ship date, and the difference is not
// cosmetic.** The first cut used midnight on the ship date — ~19h44m too early.
// Every item answered between midnight and the moment the endpoint actually went
// live would then read `never_attempted`, *"no arm ever picked its answer up"*,
// for a window in which the attempt route did not exist. That is the conflation
// this record exists to prevent, running in the opposite direction, and it is
// silent: the wrong answer is a plausible one.
var DeliveryTrailEpoch = time.Date(2026, 9, 29, 19, 44, 38, 0, time.UTC)

// DeliveryAttempt is the record an attempting arm writes: which arm tried, and
// what it observed.
//
// ⚠️ It is a sibling record keyed by ItemID rather than a field on Item. Every
// store-written field on an item is per-question state written on the
// `open` -> `answered` transition; an attempt is a per-answer event written
// later by a DIFFERENT arm — the one that actually tries to deliver. Folding it
// into the item would put two writers on one record, and give a field that
// names an actor the job of carrying machine data.
type DeliveryAttempt struct {
	ItemID      ItemID           `json:"item_id"`
	Carrier     string           `json:"carrier"`
	Outcome     DeliveryOutcome  `json:"outcome"`
	AttemptedAt libtime.DateTime `json:"attempted_at"`
}

// Validate returns an error when the record is not one an arm could have
// written.
func (d DeliveryAttempt) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("ItemID", validation.NotEmptyString(d.ItemID)),
		validation.Name("Carrier", validation.NotEmptyString(d.Carrier)),
		validation.Name("Outcome", d.Outcome),
	}.Validate(ctx)
}

// DeliveryReport is what the read surface returns for one item: the derived
// status, and — when an attempt was recorded — which arm attempted it and when.
//
// ⚠️ It carries the DERIVED status rather than the raw record, so a caller
// never has to know the epoch rule to read an answer's fate.
type DeliveryReport struct {
	ItemID      ItemID            `json:"item_id"`
	Status      DeliveryStatus    `json:"status"`
	Carrier     string            `json:"carrier,omitempty"`
	AttemptedAt *libtime.DateTime `json:"attempted_at,omitempty"`
}

// deliveryAttemptBucketName is the bucket attempt records live in. It is a
// STORAGE layout, not a schema: [[Attention Item Schema]] § Delivery attempts
// describes the record, and this names where it is kept.
var deliveryAttemptBucketName = libkv.NewBucketName("attention-delivery-attempts")
