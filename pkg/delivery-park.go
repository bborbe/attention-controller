// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
)

const (
	// DeliveryAttemptBackoff is the minimum gap between two attempts of the
	// SAME item.
	//
	// ⚠️ It exists because the delivery arm re-scans the board every 2 s, and a
	// permanently unreachable item was re-attempted on every one of those ticks:
	// measured 2026-11-16, 454,187 attempts over 647 items in 5.5 h — ~21
	// attempts per item per 10 minutes. A 5-minute gap caps one item at 3
	// attempts per 10-minute window, which is the bound SC1 reads.
	DeliveryAttemptBackoff = 5 * time.Minute

	// DeliveryAttemptParkThreshold is the attempt count at which an item is
	// parked. It is deliberately at least 2, because a threshold of 1 parks on
	// first sighting — a degenerate "backoff" that never re-tries a transient
	// failure and would satisfy an upper bound while breaking delivery.
	//
	// ⚠️ A parked item is NOT abandoned: it is re-opened the moment its session
	// is observed live, which is the predicate SC2 reads. See DeliveryPark.
	DeliveryAttemptParkThreshold = 8
)

// deliveryParkBucketName is the bucket park records live in. It is a STORAGE
// layout, not a schema: [[Attention Item Schema]] § Delivery attempts describes
// the attempt record, and a park is not an attempt outcome — it is the
// scheduling decision made AFTER attempts stopped producing a delivery.
var deliveryParkBucketName = libkv.NewBucketName("attention-delivery-parks")

// DeliveryPark records that an item's answer stopped being attempted: it failed
// DeliveryAttemptParkThreshold times in a row and will not be tried again until
// its session is observed live.
//
// ⚠️ The park is a liveness gate, never a permanent verdict. `refuse` and
// `no-pane` both cover a session whose process may come back — `noPaneVerdict`
// returns `no-pane` for a reaped-but-resumable headless worker, and `refuse` for
// a session that ended its turn and exited — so parking on the verdict class
// alone would abandon exactly the population the headless arm exists to serve.
// The deliver arm re-opens a park as soon as SessionLivenessChecker reports the
// session live, whatever its last verdict was.
type DeliveryPark struct {
	ItemID ItemID `json:"item_id"`
	// Verdict is the verdict of the attempt that parked the item.
	Verdict DeliveryVerdict `json:"verdict"`
	// Reason is the carrier's own reason for that verdict, carried verbatim so
	// the park names why the item stopped rather than only that it did.
	Reason string `json:"reason"`
	// Attempts is the persisted attempt count when the park was written. It is
	// the number SC2 reads, and it is preserved on the record because a re-open
	// clears the count to start a fresh cycle.
	Attempts int `json:"attempts"`
	// ParkedAt is when the park was written, from the store's own clock.
	ParkedAt libtime.DateTime `json:"parked_at"`
}

// Validate returns an error when the record is not one the arm could have
// written.
func (p DeliveryPark) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("ItemID", validation.NotEmptyString(p.ItemID)),
		validation.Name("Verdict", p.Verdict),
		validation.Name("Reason", validation.NotEmptyString(p.Reason)),
	}.Validate(ctx)
}

// Validate returns an error when the verdict is not one the carrier can return.
func (v DeliveryVerdict) Validate(ctx context.Context) error {
	switch v {
	case DeliveryOK, DeliveryRetry, DeliveryRefuse, DeliveryNoPane, DeliveryResolved:
		return nil
	default:
		return errors.Wrapf(ctx, validation.Error, "unknown delivery verdict '%s'", v)
	}
}
