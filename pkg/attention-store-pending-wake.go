// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// pendingWakeBucketName is the bucket a deferred manager wake lives in.
//
// It is the durable half of a deferral that flushPending otherwise holds only in
// memory (wakePass.pending): when a manager's pane composer is busy the batch is
// retained with a deferredAt and re-probed after ManagerWakeBatchWindow, and a
// manager continuously busy past every pass never frees. Persisting the wake
// means a controller restart no longer forgets that a wake is owed, and a drain
// at the manager's own turn boundary can deliver it without waiting for a pass.
var pendingWakeBucketName = libkv.NewBucketName("attention-pending-wakes")

// pendingWakeKey is the bucket key for one owed wake: "<manager-session>/<item-id>".
//
// The item id is in the KEY, not a field on the value, so a second deferral of
// the same item for the same manager overwrites the first. A wake is owed once,
// however many passes find the composer busy.
func pendingWakeKey(managerSession string, itemID ItemID) string {
	return managerSession + "/" + itemID.String()
}

// pendingWakeKeyPrefix is the prefix every key of one manager session shares.
func pendingWakeKeyPrefix(managerSession string) string {
	return managerSession + "/"
}

// PutPendingWake records that a wake for itemID is owed to managerSession
// because the manager's composer was busy.
//
// ⚠️ It is written by the arm that ATTEMPTS delivery, on the deferral verdict —
// never by the arm that answers. Writing it where the answer is recorded would
// make every answer read as a deferred wake.
//
// The item must exist. A pending wake against an unknown item is
// ErrItemNotFound rather than an orphan record: DrainPendingWakes returns the
// items it drains, and a record with nothing to return could never be observed —
// a write that can never be read is a bug wearing a success.
//
// Re-queuing overwrites the stored item with the current one, so the record
// always carries the most recent deferral.
func (a *attentionStore) PutPendingWake(
	ctx context.Context,
	managerSession string,
	itemID ItemID,
) error {
	if strings.TrimSpace(managerSession) == "" {
		return errors.Wrapf(
			ctx,
			ErrManagerSessionRequired,
			"item %s: manager session is empty",
			itemID,
		)
	}
	return a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		return a.pendingWakes.Add(
			ctx,
			tx,
			pendingWakeKey(managerSession, itemID),
			*item,
		)
	})
}

// DrainPendingWakes returns the wakes owed to managerSession and deletes their
// records in ONE transaction — drain-and-mark, never drain-and-hope.
//
// ⚠️ The atomicity IS the idempotency. The `Stop` hook that drains can fire more
// than once for one turn boundary; a second call finds the bucket empty because
// the first committed the deletion, so a wake is delivered exactly once.
//
// An item that is no longer open is dropped rather than returned: the wake it
// queued for is moot, and returning it would ask the manager to handle a gate
// that is already answered. Its record is still deleted — the wake is not owed
// any more, whatever the reason.
//
// The items are returned as they stand NOW, not as they stood when queued: a
// stored snapshot would still read `open` after the gate was answered, and the
// manager would be woken to handle a card it has already handled.
func (a *attentionStore) DrainPendingWakes(
	ctx context.Context,
	managerSession string,
) (Items, error) {
	if strings.TrimSpace(managerSession) == "" {
		return nil, errors.Wrapf(
			ctx,
			ErrManagerSessionRequired,
			"manager session is empty",
		)
	}
	var (
		keys  []string
		items Items
	)
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		prefix := pendingWakeKeyPrefix(managerSession)
		if err := a.pendingWakes.Map(
			ctx,
			tx,
			func(ctx context.Context, key string, item Item) error {
				if strings.HasPrefix(key, prefix) {
					keys = append(keys, key)
				}
				return nil
			},
		); err != nil {
			return errors.Wrap(ctx, err, "map pending wakes failed")
		}
		// Re-read and remove in a SECOND pass: bbolt does not permit deleting a
		// key from inside the iterator that is walking it, and the stored
		// snapshot is stale by construction — it cannot tell a still-open gate
		// from one answered since.
		for _, key := range keys {
			itemID := ItemID(strings.TrimPrefix(key, prefix))
			item, err := a.store.Get(ctx, tx, itemID.String())
			if err != nil {
				if !isNotFound(err) {
					return errors.Wrapf(ctx, err, "get item %s failed", itemID)
				}
				// The item is gone: the wake is moot, and the record is deleted
				// below exactly as an answered one's is.
			} else if item.State == OpenState {
				items = append(items, *item)
			}
			if err := a.pendingWakes.Remove(ctx, tx, key); err != nil {
				return errors.Wrapf(ctx, err, "remove pending wake %s failed", key)
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "drain pending wakes failed")
	}
	return items, nil
}

// PendingWakes returns the wakes owed to managerSession without draining them.
//
// It is the read half the drain's tests and the no-double-delivery check use;
// the drain itself never calls it.
func (a *attentionStore) PendingWakes(
	ctx context.Context,
	managerSession string,
) (Items, error) {
	if strings.TrimSpace(managerSession) == "" {
		return nil, errors.Wrapf(
			ctx,
			ErrManagerSessionRequired,
			"manager session is empty",
		)
	}
	var items Items
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		prefix := pendingWakeKeyPrefix(managerSession)
		return a.pendingWakes.Map(
			ctx,
			tx,
			func(ctx context.Context, key string, item Item) error {
				if strings.HasPrefix(key, prefix) {
					items = append(items, item)
				}
				return nil
			},
		)
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read pending wakes failed")
	}
	return items, nil
}
