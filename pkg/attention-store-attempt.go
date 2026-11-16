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

// RecordAttemptCount increments how many times one item's answer has been
// attempted, and returns the new total.
//
// ⚠️ Every verdict counts, which is why this is a separate write from
// RecordAttempt: `no-pane` and `retry` attempts are the population that loops
// forever, and they write no attempt record at all — the record is derived from
// what an arm OBSERVED, and they observed nothing they could report as an
// outcome without inventing one. Counting here keeps `never_attempted`
// derivable from the attempt record's absence while still bounding the loop.
//
// The item must exist, for the same reason RecordAttempt requires it: a count
// with nothing to join to could never be read back.
func (a *attentionStore) RecordAttemptCount(
	ctx context.Context,
	itemID ItemID,
	verdict DeliveryVerdict,
	reason string,
) (int, error) {
	var count int
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		if _, err := a.store.Get(ctx, tx, itemID.String()); err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		existing, getErr := a.attemptCounts.Get(ctx, tx, itemID.String())
		switch {
		case getErr == nil:
			count = existing.Count + 1
		case isNotFound(getErr):
			count = 1
		default:
			return errors.Wrap(ctx, getErr, "get delivery attempt count failed")
		}
		if err := a.attemptCounts.Add(ctx, tx, itemID.String(), DeliveryAttemptCount{
			ItemID:          itemID,
			Count:           count,
			LastAttemptedAt: a.currentDateTimeGetter.Now(),
			LastVerdict:     verdict,
			LastReason:      reason,
		}); err != nil {
			return errors.Wrap(ctx, err, "add delivery attempt count failed")
		}
		return nil
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "record attempt count failed")
	}
	return count, nil
}

// DeliveryAttemptCounts returns every recorded attempt count, keyed by item id.
//
// It mirrors DeliveryAttempts: one read transaction for the whole population,
// because the deliver arm consults it for every item on every cycle and a
// transaction per item would cost more than the attempts it is bounding.
func (a *attentionStore) DeliveryAttemptCounts(
	ctx context.Context,
) (map[ItemID]DeliveryAttemptCount, error) {
	counts := make(map[ItemID]DeliveryAttemptCount)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.attemptCounts.Map(
			ctx,
			tx,
			func(ctx context.Context, key string, count DeliveryAttemptCount) error {
				counts[ItemID(key)] = count
				return nil
			},
		)
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "delivery attempt counts failed")
	}
	return counts, nil
}

// DeliveryParks returns every parked item, keyed by item id.
func (a *attentionStore) DeliveryParks(
	ctx context.Context,
) (map[ItemID]DeliveryPark, error) {
	parks := make(map[ItemID]DeliveryPark)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.parks.Map(
			ctx,
			tx,
			func(ctx context.Context, key string, park DeliveryPark) error {
				parks[ItemID(key)] = park
				return nil
			},
		)
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "delivery parks failed")
	}
	return parks, nil
}

// ParkDelivery records that an item's answer stopped being attempted, naming the
// verdict and reason that ended it.
func (a *attentionStore) ParkDelivery(ctx context.Context, park DeliveryPark) error {
	if err := park.Validate(ctx); err != nil {
		return errors.Wrap(ctx, err, "validate delivery park failed")
	}
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.parks.Add(ctx, tx, park.ItemID.String(), park)
	})
	if err != nil {
		return errors.Wrap(ctx, err, "park delivery failed")
	}
	return nil
}

// ClearDeliveryPark ends a park: it removes the park record AND the attempt
// count that earned it.
//
// ⚠️ The count is cleared with the park so the two facts stay consistent: the
// count is attempts-since-last-reopen, and a park that has been re-opened must
// not immediately re-park on the next cycle because the old count is still at
// the threshold. The park record itself carried the count at park time, so the
// history is not lost — it was read before this write.
func (a *attentionStore) ClearDeliveryPark(ctx context.Context, itemID ItemID) error {
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		if err := a.parks.Remove(ctx, tx, itemID.String()); err != nil {
			return errors.Wrap(ctx, err, "delete delivery park failed")
		}
		if err := a.attemptCounts.Remove(ctx, tx, itemID.String()); err != nil {
			return errors.Wrap(ctx, err, "delete delivery attempt count failed")
		}
		return nil
	})
	if err != nil {
		return errors.Wrap(ctx, err, "clear delivery park failed")
	}
	return nil
}

// IsSessionLive reports whether the session a parked item is addressed to
// currently has a live process.
//
// ⚠️ It is a read of the registry, never an attempt: probing by attempting would
// increment the count and defeat the park, so a session that never returns would
// still be retried forever.
func (a *attentionStore) IsSessionLive(ctx context.Context, sessionID string) bool {
	if a.sessionLivenessChecker == nil || sessionID == "" {
		return false
	}
	return a.sessionLivenessChecker.IsLive(ctx, sessionID)
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
		report = &DeliveryReport{ItemID: itemID}

		attempt, err := a.attempts.Get(ctx, tx, itemID.String())
		switch {
		case err == nil:
			// ⚠️ Read the EFFECTIVE outcome, never the last write: a redundant
			// carrier can overwrite the top-level fields after a real delivery,
			// and reading them directly would report the delivered card as
			// failed. See DeliveryAttempt.EffectiveOutcome.
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
		case isNotFound(err):
			// No record. Which absence this is turns on whether a record could
			// have existed at all — and that is a question about when the item's
			// answer was ROUTED, not when the item was created.
			//
			// ⚠️ The attempting arm is not the answering arm, so an attempt can
			// only happen strictly AFTER an answer. An item created before the
			// epoch and answered after it was inside the trail's window: reading it
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
