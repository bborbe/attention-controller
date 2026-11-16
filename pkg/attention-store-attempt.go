// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	"github.com/golang/glog"
)

// RecordAttempt stores what an attempting arm observed about one item's answer.
//
// ⚠️ It is called by the arm that ATTEMPTS delivery, never by the arm that
// records the answer. The answering script resolves a target and delivers
// nothing, so writing the attempt where the answer is recorded would make every
// answer read as attempted — the exact defect this record exists to close.
//
// The item must exist. An attempt against an unknown item is ErrItemNotFound
// rather than an orphan record, because the read surface joins the record to
// the item's CreatedAt, and a record with nothing to join to could never be
// read back — a write that can never be observed is a bug wearing a success.
//
// A re-attempt overwrites the previous record's carrier, outcome and
// attempted_at — but a settled `delivered` is first-write-wins: the settlement
// is carried forward untouched, so a redundant carrier arriving after the gate
// was already settled cannot erase a real delivery. The record still answers
// "what happened to this item's answer"; the last arm to try keeps the top-level
// fields, and the settlement keeps the one fact a later write must not lose.
func (a *attentionStore) RecordAttempt(
	ctx context.Context,
	itemID ItemID,
	carrier string,
	outcome DeliveryOutcome,
) (*DeliveryAttempt, error) {
	attempt := DeliveryAttempt{
		ItemID:  itemID,
		Carrier: carrier,
		Outcome: outcome,
	}
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		if _, err := a.store.Get(ctx, tx, itemID.String()); err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// Stamped from the store's own clock inside the write, exactly as
		// escalated_at is: a fact the store already holds is not one to accept
		// from a caller, and a caller-supplied time would let an arm date its
		// own attempt.
		attempt.AttemptedAt = a.currentDateTimeGetter.Now()
		if err := attempt.Validate(ctx); err != nil {
			return errors.Wrap(ctx, err, "validate delivery attempt failed")
		}
		// ⚠️ A settled delivery is first-write-wins. Once an arm has observed
		// that the answer reached the session, a redundant carrier arriving
		// later must not overwrite that with its own failure — the false
		// negative measured 2026-11-06 on card f2e9520d105f97d8911cc0f562f60454,
		// where a delivered record read failed 15 seconds later. The later write
		// is still stored in the top-level fields, so both writes stay visible
		// with their own writer and timestamp; only the settlement is frozen.
		var existing *DeliveryAttempt
		previous, getErr := a.attempts.Get(ctx, tx, itemID.String())
		if getErr == nil {
			existing = previous
		} else if !isNotFound(getErr) {
			return errors.Wrap(ctx, getErr, "get delivery attempt failed")
		}
		carrySettlement(existing, &attempt, outcome)
		// The incident's own shape: a redundant carrier reporting a non-delivery
		// against a gate that already delivered. Log it so the next occurrence is
		// diagnosable from the log, not only by reading the record.
		if attempt.Settled != nil && outcome != DeliveredOutcome {
			glog.V(2).Infof(
				"kept settled delivery for item %s; ignoring %s outcome from %s",
				itemID, outcome, carrier,
			)
		}
		if err := a.attempts.Add(ctx, tx, itemID.String(), attempt); err != nil {
			return errors.Wrap(ctx, err, "add delivery attempt failed")
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "record attempt failed")
	}
	return &attempt, nil
}

// carrySettlement sets attempt.Settled to the delivery this write must keep.
//
// A settled delivery is first-write-wins. An existing settlement is carried
// forward untouched, and so is an existing `delivered` outcome from a record
// written before the settlement field shipped — those records carry no
// settlement, so their outcome is the only evidence of the delivery they hold.
// A write that itself delivers settles when nothing is settled yet, which is
// what keeps failed-then-delivered reading as a real recovery.
func carrySettlement(existing *DeliveryAttempt, attempt *DeliveryAttempt, outcome DeliveryOutcome) {
	if existing != nil {
		attempt.Settled = existing.Settled
		if attempt.Settled == nil && existing.Outcome == DeliveredOutcome {
			attempt.Settled = &DeliverySettlement{
				Carrier:     existing.Carrier,
				AttemptedAt: existing.AttemptedAt,
			}
		}
	}
	if attempt.Settled == nil && outcome == DeliveredOutcome {
		attempt.Settled = &DeliverySettlement{
			Carrier:     attempt.Carrier,
			AttemptedAt: attempt.AttemptedAt,
		}
	}
}

// Delivery returns the derived delivery status for one item — the one query
// that answers "did my answer reach the session?" without inference from which
// files exist, which code paths are reachable, or which plugin version is
// loaded.
//
// ⚠️ The two absent-record cases are told apart by the item's own CreatedAt
// against DeliveryTrailEpoch, so an absent record is never the sole encoding of
// both. That distinction is the criterion, not a refinement of it: *no carrier
// ever looked* and *this predates the trail* must produce different results,
// and neither may read as the other.
//
// ⚠️ A `closed` item is NOT read as delivered. Per [[Attention Item Schema]],
// *a close is a clear, not an answer* — the discriminator is `answered_at`,
// never `state` — so nothing here keys on State. An item cleared from the board
// with no answer reads `never_attempted` or `pre_trail_unknown`, which is the
// truth: nothing was routed, so nothing was delivered.
func (a *attentionStore) Delivery(
	ctx context.Context,
	itemID ItemID,
) (*DeliveryReport, error) {
	var report *DeliveryReport
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		attempt, err := a.attempts.Get(ctx, tx, itemID.String())
		switch {
		case err == nil:
			report = deriveDeliveryReport(item, attempt)
		case isNotFound(err):
			report = deriveDeliveryReport(item, nil)
		default:
			return errors.Wrap(ctx, err, "get delivery attempt failed")
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "delivery failed")
	}
	return report, nil
}

// DeliveryReports returns the derived delivery status for every item given, in
// ONE read transaction.
//
// ⚠️ It exists because the board derives a report for every row it draws, and a
// per-row Delivery call opened one read transaction per row. Measured on the
// live board 2026-11-16: 772 rows per render at 3.5 renders per second, so
// ~2700 read transactions a second, and the bbolt page walk behind each of them
// was the largest single CPU consumer in the process. This is the same read,
// once.
//
// The items are passed in rather than read here because every caller already
// holds them from ReadBoard; re-reading them would be a second read of a set
// the caller is holding. An item with no attempt record still gets a report —
// the absent case is derived from the item's own timestamps, exactly as
// Delivery derives it, because both call deriveDeliveryReport.
//
// An empty items slice yields an empty non-nil map, so a caller can range over
// it without a nil check.
func (a *attentionStore) DeliveryReports(
	ctx context.Context,
	items Items,
) (map[ItemID]*DeliveryReport, error) {
	reports := make(map[ItemID]*DeliveryReport, len(items))
	if len(items) == 0 {
		return reports, nil
	}
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		for _, item := range items {
			// A board-sized read is 772 rows (measured 2026-11-16), so the loop
			// honours a caller that has gone away rather than holding the read
			// transaction open for a render nobody is waiting for any more.
			if err := ctx.Err(); err != nil {
				// ⚠️ Returned bare, not wrapped: this closure is wrapped once by
				// the caller below, and wrapping here as well would stack two
				// messages onto one failure.
				return err
			}
			// ⚠️ An item the LOCAL store does not hold is a federated row: a peer
			// answered it, no attempt was ever recorded here, and Delivery answers
			// ErrItemNotFound for it. Deriving a report anyway would fabricate
			// `never_attempted` and put a false undeliverable marker on every
			// answered remote card, so no entry is written and the caller's lookup
			// yields nil — the stage renders absent, exactly as it did through
			// Delivery.
			if _, err := a.store.Get(ctx, tx, item.ItemID.String()); err != nil {
				if isNotFound(err) {
					continue
				}
				return err
			}
			attempt, err := a.attempts.Get(ctx, tx, item.ItemID.String())
			switch {
			case err == nil:
				reports[item.ItemID] = deriveDeliveryReport(&item, attempt)
			case isNotFound(err):
				reports[item.ItemID] = deriveDeliveryReport(&item, nil)
			default:
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "delivery reports failed")
	}
	return reports, nil
}

// deriveDeliveryReport answers one item's delivery status from the item and its
// attempt record — attempt nil when no carrier has recorded one.
//
// ⚠️ One derivation, two readers: Delivery reads one item and calls this, and
// DeliveryReports reads a whole board and calls this per item. Duplicating the
// body would let the single and the batch read disagree about the same record,
// which is the drift the board's "a row sent over the stream is identical to
// the same row on a fresh load" claim cannot survive.
//
// ⚠️ It reads the EFFECTIVE outcome, never the last write: a redundant carrier
// can overwrite the top-level fields after a real delivery, and reading them
// directly would report the delivered card as failed. See
// DeliveryAttempt.EffectiveOutcome.
//
// ⚠️ A `closed` item is NOT read as delivered. Per [[Attention Item Schema]],
// *a close is a clear, not an answer* — the discriminator is `answered_at`,
// never `state` — so nothing here keys on State. An item cleared from the board
// with no answer reads `never_attempted` or `pre_trail_unknown`, which is the
// truth: nothing was routed, so nothing was delivered.
func deriveDeliveryReport(item *Item, attempt *DeliveryAttempt) *DeliveryReport {
	report := &DeliveryReport{ItemID: item.ItemID}
	if attempt == nil {
		// No record. Which absence this is turns on whether a record could have
		// existed at all — and that is a question about when the item's answer
		// was ROUTED, not when the item was created.
		//
		// ⚠️ The attempting arm is not the answering arm, so an attempt can only
		// happen strictly AFTER an answer. An item created before the epoch and
		// answered after it was inside the trail's window: reading it
		// `pre_trail_unknown` would hide a real, actionable `never_attempted`,
		// which is the failure this record exists to remove. An unanswered item
		// has no answer time, so it falls back to CreatedAt.
		since := item.CreatedAt.Time()
		if item.AnsweredAt != nil {
			since = item.AnsweredAt.Time()
		}
		if since.Before(DeliveryTrailEpoch) {
			report.Status = PreTrailUnknownStatus
		} else {
			report.Status = NeverAttemptedStatus
		}
		return report
	}
	attemptedAt := attempt.EffectiveAttemptedAt()
	report.Carrier = attempt.EffectiveCarrier()
	report.AttemptedAt = &attemptedAt
	switch attempt.EffectiveOutcome() {
	case DeliveredOutcome:
		report.Status = DeliveredStatus
	case HeldOutcome:
		report.Status = HeldStatus
	default:
		report.Status = FailedStatus
	}
	return report
}

// DeliveryAttempts returns every recorded delivery attempt, keyed by item id.
//
// ⚠️ One read transaction for the whole population, and that is the reason it
// exists rather than a loop over Delivery: Delivery opens a transaction per
// item, which a metrics scrape over every card cannot afford. Both read the
// same records, so the two can disagree about the number of transactions and
// never about an attempt.
//
// The key is the item id as the attempts bucket stores it, converted back to
// ItemID rather than carrying a second key space. An empty bucket yields an
// empty non-nil map, so a caller can range over it without a nil check.
func (a *attentionStore) DeliveryAttempts(
	ctx context.Context,
) (map[ItemID]DeliveryAttempt, error) {
	attempts := make(map[ItemID]DeliveryAttempt)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.attempts.Map(
			ctx,
			tx,
			func(ctx context.Context, key string, attempt DeliveryAttempt) error {
				attempts[ItemID(key)] = attempt
				return nil
			},
		)
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "delivery attempts failed")
	}
	return attempts, nil
}
