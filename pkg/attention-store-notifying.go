// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	libtime "github.com/bborbe/time"
)

// NewNotifyingAttentionStore wraps an AttentionStore so every successful write
// signals an AttentionChangeNotifier.
//
// It is a decorator rather than a change to the store. The store's interface,
// its read path and the attention item schema are all untouched, and only the
// write path gains a side effect — so the live view's transport stays out of
// the store, which remains the durable home for items and nothing else.
//
// The signal is emitted only after the write returns without error, so a
// rejected transition never wakes a subscriber to re-read an unchanged store.
// It is emitted for a write that changed nothing observable as well — a push
// that updated an existing open item in place, say — because the notifier
// carries no payload and a redundant wake-up costs one read rather than a
// missed update.
func NewNotifyingAttentionStore(
	store AttentionStore,
	notifier AttentionChangeNotifier,
) AttentionStore {
	return &notifyingAttentionStore{store: store, notifier: notifier}
}

type notifyingAttentionStore struct {
	store    AttentionStore
	notifier AttentionChangeNotifier
}

// Push delegates and signals on success.
func (n *notifyingAttentionStore) Push(
	ctx context.Context,
	request PushRequest,
) (*Item, error) {
	item, err := n.store.Push(ctx, request)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// Get delegates and never signals: a read cannot change the store.
func (n *notifyingAttentionStore) Get(ctx context.Context, itemID ItemID) (*Item, error) {
	return n.store.Get(ctx, itemID)
}

// Read delegates and never signals. It is not side-effect free — it prunes
// EXPIRED items — but that prune is driven by an item's own `expires_at` rather
// than by this call, and signalling from a read would make each live view's own
// re-read wake every other one.
func (n *notifyingAttentionStore) Read(ctx context.Context) (Items, error) {
	return n.store.Read(ctx)
}

// ReadBoard delegates and never signals, on the same reasoning as Read: it is a
// read, and the prune it shares with Read is driven by an item's `expires_at`
// rather than by this call. Signalling from it would make each live view's own
// re-read wake every other one — the read is what the board does on every
// change, so a signal here would be a feedback loop.
func (n *notifyingAttentionStore) ReadBoard(ctx context.Context) (Items, error) {
	return n.store.ReadBoard(ctx)
}

// History delegates and never signals: it is a counting read.
func (n *notifyingAttentionStore) History(
	ctx context.Context,
	limit int,
	offset int,
	filters ...HistoryFilter,
) (Items, error) {
	return n.store.History(ctx, limit, offset, filters...)
}

// HistoryJSON delegates and never signals: it is the same counting read, encoded.
func (n *notifyingAttentionStore) HistoryJSON(
	ctx context.Context,
	limit int,
	offset int,
	filters ...HistoryFilter,
) ([]byte, error) {
	return n.store.HistoryJSON(ctx, limit, offset, filters...)
}

// HistoryCreatedSince delegates and never signals: it is a counting read.
func (n *notifyingAttentionStore) HistoryCreatedSince(
	ctx context.Context,
	since libtime.DateTime,
) (Items, error) {
	return n.store.HistoryCreatedSince(ctx, since)
}

// Answer delegates and signals on success.
func (n *notifyingAttentionStore) Answer(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	resolvedBy string,
	decision Decision,
	answer *Answer,
	answers Answers,
	answeredClient *AnsweredClient,
	route ...AnswerRoute,
) (*Item, error) {
	item, err := n.store.Answer(
		ctx, itemID, answeredBy, resolvedBy, decision, answer, answers, answeredClient, route...,
	)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// Escalate delegates and signals on success.
func (n *notifyingAttentionStore) Escalate(
	ctx context.Context,
	itemID ItemID,
	escalatedBy string,
) (*Item, error) {
	item, err := n.store.Escalate(ctx, itemID, escalatedBy)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// Flag delegates and signals on success.
func (n *notifyingAttentionStore) Flag(
	ctx context.Context,
	itemID ItemID,
	reason string,
) (*Item, error) {
	item, err := n.store.Flag(ctx, itemID, reason)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// Unflag delegates and signals on success.
func (n *notifyingAttentionStore) Unflag(ctx context.Context, itemID ItemID) (*Item, error) {
	item, err := n.store.Unflag(ctx, itemID)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// Rate delegates and signals on success. The signal re-renders the board row
// so the highlight follows the stored value; it reaches the board's stream
// only, never a producer.
func (n *notifyingAttentionStore) Rate(
	ctx context.Context,
	itemID ItemID,
	rating Rating,
) (*Item, error) {
	item, err := n.store.Rate(ctx, itemID, rating)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// MarkSeen delegates and deliberately does NOT signal.
//
// ⚠️ Every other write here wakes the board's stream so a row re-renders with
// the new value. This one must not: the call is made once per card on every
// page load, so a signal would turn one page load into one full board render
// per card, and the row swap that followed would paint the same card again.
// The value it writes decorates the info panel, which the next load or the
// next unrelated change renders anyway.
func (n *notifyingAttentionStore) MarkSeen(ctx context.Context, itemID ItemID) (*Item, error) {
	return n.store.MarkSeen(ctx, itemID)
}

// Close delegates and signals on success.
func (n *notifyingAttentionStore) Close(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	answeredClient *AnsweredClient,
) (*Item, error) {
	item, err := n.store.Close(ctx, itemID, answeredBy, answeredClient)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// RecordAttempt delegates and signals on success. It is a write, so it wakes
// subscribers exactly as every other write here does — a board rendering a
// delivery outcome should not have to wait for the next unrelated change to
// see it.
func (n *notifyingAttentionStore) RecordAttempt(
	ctx context.Context,
	itemID ItemID,
	carrier string,
	outcome DeliveryOutcome,
) (*DeliveryAttempt, error) {
	attempt, err := n.store.RecordAttempt(ctx, itemID, carrier, outcome)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return attempt, nil
}

// RecordAttemptCount delegates and signals on success, for the same reason
// RecordAttempt does: it is a write, and a board showing how often an item has
// been tried should not wait for the next unrelated change to see it.
func (n *notifyingAttentionStore) RecordAttemptCount(
	ctx context.Context,
	itemID ItemID,
	verdict DeliveryVerdict,
	reason string,
) (int, error) {
	count, err := n.store.RecordAttemptCount(ctx, itemID, verdict, reason)
	if err != nil {
		return 0, err
	}
	n.notifier.Notify()
	return count, nil
}

// DeliveryAttemptCounts delegates and never signals: it is a read.
func (n *notifyingAttentionStore) DeliveryAttemptCounts(
	ctx context.Context,
) (map[ItemID]DeliveryAttemptCount, error) {
	return n.store.DeliveryAttemptCounts(ctx)
}

// ParkDelivery delegates and signals: a park is a board-visible state change.
func (n *notifyingAttentionStore) ParkDelivery(ctx context.Context, park DeliveryPark) error {
	if err := n.store.ParkDelivery(ctx, park); err != nil {
		return err
	}
	n.notifier.Notify()
	return nil
}

// DeliveryParks delegates and never signals: it is a read.
func (n *notifyingAttentionStore) DeliveryParks(
	ctx context.Context,
) (map[ItemID]DeliveryPark, error) {
	return n.store.DeliveryParks(ctx)
}

// ClearDeliveryPark delegates and signals: ending a park is a board-visible
// state change.
func (n *notifyingAttentionStore) ClearDeliveryPark(ctx context.Context, itemID ItemID) error {
	if err := n.store.ClearDeliveryPark(ctx, itemID); err != nil {
		return err
	}
	n.notifier.Notify()
	return nil
}

// IsSessionLive delegates and never signals: it is a read of the session
// registry, and it changes nothing a subscriber renders.
func (n *notifyingAttentionStore) IsSessionLive(ctx context.Context, sessionID string) bool {
	return n.store.IsSessionLive(ctx, sessionID)
}

// Delivery delegates and never signals: it is a read, and the record it reads
// was written by an arm outside this store's write path.
func (n *notifyingAttentionStore) Delivery(
	ctx context.Context,
	itemID ItemID,
) (*DeliveryReport, error) {
	return n.store.Delivery(ctx, itemID)
}

// DeliveryAttempts delegates unchanged. It is a read and signals nothing.
func (n *notifyingAttentionStore) DeliveryAttempts(
	ctx context.Context,
) (map[ItemID]DeliveryAttempt, error) {
	return n.store.DeliveryAttempts(ctx)
}

// DeliveryReports delegates unchanged. It is a read and signals nothing.
func (n *notifyingAttentionStore) DeliveryReports(
	ctx context.Context,
	items Items,
) (map[ItemID]*DeliveryReport, error) {
	return n.store.DeliveryReports(ctx, items)
}

// SettleAnswer delegates and signals on success: the card leaving the open set
// is a board-visible change.
func (n *notifyingAttentionStore) SettleAnswer(ctx context.Context, itemID ItemID) (*Item, error) {
	item, err := n.store.SettleAnswer(ctx, itemID)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// CloseUndelivered delegates and signals on success: the card leaving the open
// set is a board-visible change.
func (n *notifyingAttentionStore) CloseUndelivered(
	ctx context.Context,
	itemID ItemID,
) (*Item, error) {
	item, err := n.store.CloseUndelivered(ctx, itemID)
	if err != nil {
		return nil, err
	}
	n.notifier.Notify()
	return item, nil
}

// SweepAnswered delegates and signals ONLY when it closed something.
//
// ⚠️ The conditional is the point, not a micro-optimisation. Every other write
// here signals unconditionally because a caller asked for it; this one runs on a
// ticker, so an unconditional signal would wake every live view to re-read an
// unchanged store once per interval for the life of the process — a permanent
// false alarm that no caller caused.
func (n *notifyingAttentionStore) SweepAnswered(
	ctx context.Context,
	maxAge libtime.Duration,
) (int, error) {
	closed, err := n.store.SweepAnswered(ctx, maxAge)
	if err != nil {
		return 0, err
	}
	if closed > 0 {
		n.notifier.Notify()
	}
	return closed, nil
}

// RenewLive delegates and NEVER signals, on a stricter reasoning than
// SweepAnswered's conditional.
//
// ⚠️ It runs on the same ticker and moves nothing a view renders: a renewal
// writes `expires_at` on an item that is still open and still inside its window,
// so the set of cards and their content are unchanged. Signalling would wake
// every live view to re-read an unchanged store once per interval for the life of
// the process — and unlike the sweep, it would do so on EVERY pass, because a
// renewal happens whenever a defaulted deadline crosses the threshold.
func (n *notifyingAttentionStore) RenewLive(ctx context.Context) (int, error) {
	return n.store.RenewLive(ctx)
}
