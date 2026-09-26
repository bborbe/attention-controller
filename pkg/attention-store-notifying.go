// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
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

// Read delegates and never signals. It is not side-effect free — it prunes dead
// askers — but that prune is driven by a producer's liveness rather than by
// this call, and signalling from a read would make each live view's own
// re-read wake every other one.
func (n *notifyingAttentionStore) Read(ctx context.Context) (Items, error) {
	return n.store.Read(ctx)
}

// History delegates and never signals: it is a counting read.
func (n *notifyingAttentionStore) History(ctx context.Context) (Items, error) {
	return n.store.History(ctx)
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
) (*Item, error) {
	item, err := n.store.Answer(
		ctx, itemID, answeredBy, resolvedBy, decision, answer, answers, answeredClient,
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
