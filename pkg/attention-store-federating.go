// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// federationCacheWindow is how long one read of the peer store is reused before
// the next caller re-reads it.
//
// ⚠️ It is NOT a correctness budget. The window bounds load, not staleness: a
// card the operator needs to see is at most this much late, and the answer path
// never consults the cache at all (it reads the local store directly to decide
// where an item lives). So a stale window can only ever delay a row's
// appearance, never misroute a write.
//
// Measured reason for the bound: the board renders on every change and the JSON
// read API is polled by every supervisor twice every 2 s, so an unconditional
// peer fetch would make each of those a network round-trip to the cluster —
// turning one operator's board into a steady load on the cluster store. Two
// seconds is the poll period, so the bound removes the duplicate calls within a
// poll window without adding latency the operator can perceive.
const federationCacheWindow = libtime.Duration(2 * time.Second)

// NewFederatingAttentionStore wraps a local AttentionStore so the items a peer
// store holds are visible on the surfaces this store feeds, and so a write
// addressed to one of those items is proxied to the peer that owns it.
//
// ⚠️ It is a decorator rather than a change to the handlers, and the reason is
// reach: one value is built in main and handed to every read surface — the
// board page, the live stream and the JSON read API all read through it — so
// wrapping it here federates all three at once. Threading a client through the
// page and stream handlers instead would have federated whichever of them was
// remembered.
//
// The direction is one-way by design: the peer's items appear here, and this
// store's items do not appear there. The peer is authoritative for its own
// items, which is what makes the answer path a proxy rather than a copy — the
// item's state lives in exactly one place.
func NewFederatingAttentionStore(
	local AttentionStore,
	remote RemoteAttentionStore,
) AttentionStore {
	return NewFederatingAttentionStoreWithClock(local, remote, libtime.NewCurrentDateTime())
}

// NewFederatingAttentionStoreWithClock creates a federating store measuring its
// cache window against the given clock.
func NewFederatingAttentionStoreWithClock(
	local AttentionStore,
	remote RemoteAttentionStore,
	now libtime.CurrentDateTimeGetter,
) AttentionStore {
	return &federatingAttentionStore{local: local, remote: remote, now: now}
}

type federatingAttentionStore struct {
	local  AttentionStore
	remote RemoteAttentionStore
	now    libtime.CurrentDateTimeGetter

	// mu guards the cached peer read below. ⚠️ It IS held across the peer call
	// itself, following the sibling cache in session-liveness-checker: without
	// that, every caller arriving while a slow peer is being read starts its own
	// call, which is the thundering herd the cache exists to prevent.
	mu sync.Mutex
	// cached is the last peer read and cachedAt the clock reading at which it
	// was taken. haveCached separates "no read yet" from a read that legitimately
	// returned nothing, which are different states — the second is a working peer
	// with an empty queue and must not force a refetch on every render.
	cached     Items
	cachedAt   libtime.DateTime
	haveCached bool
}

// Push delegates to the local store.
//
// ⚠️ Nothing federates in the reverse direction: this store never pushes to the
// peer. A local producer's item is the local store's, and a peer that wanted it
// would read it from here — which is the asymmetry the whole design rests on.
func (f *federatingAttentionStore) Push(
	ctx context.Context,
	request PushRequest,
) (*Item, error) {
	return f.local.Push(ctx, request)
}

// Get returns the item from whichever store holds it.
func (f *federatingAttentionStore) Get(ctx context.Context, itemID ItemID) (*Item, error) {
	if f.isRemote(ctx, itemID) {
		return f.remote.Get(ctx, itemID)
	}
	return f.local.Get(ctx, itemID)
}

// Read returns the local store's items with the peer's open items merged in.
//
// A peer that cannot be reached degrades this to the local items alone rather
// than failing the read: the board is the operator's local surface, and a
// cluster outage must not take it down. The degradation is logged, because a
// silently short board is indistinguishable from an empty queue.
func (f *federatingAttentionStore) Read(ctx context.Context) (Items, error) {
	local, err := f.local.Read(ctx)
	if err != nil {
		return nil, err
	}
	return f.withRemote(ctx, local), nil
}

// ReadBoard returns the local store's board items with the peer's open items
// merged in, degrading to local-only on a peer failure exactly as Read does.
//
// ⚠️ It merges the peer's READ, not a board read of its own, because the peer
// exposes only the open-item read through its business API. The consequence is
// deliberate and worth naming: an ANSWERED peer item does not linger here as a
// dimmed record the way a local one does — the peer's answer is recorded at the
// peer, and the local board stops showing the card. This store does not keep a
// second copy of the peer's answer, which is what would be needed to render one
// and would put the item's state in two places.
func (f *federatingAttentionStore) ReadBoard(ctx context.Context) (Items, error) {
	local, err := f.local.ReadBoard(ctx)
	if err != nil {
		return nil, err
	}
	return f.withRemote(ctx, local), nil
}

// History delegates to the local store. It is a counting read over what THIS
// store recorded, and the peer's items were not recorded here.
func (f *federatingAttentionStore) History(
	ctx context.Context,
	limit int,
	offset int,
) (Items, error) {
	return f.local.History(ctx, limit, offset)
}

// SweepAnswered delegates to the local store. The peer sweeps its own.
func (f *federatingAttentionStore) SweepAnswered(
	ctx context.Context,
	maxAge libtime.Duration,
) (int, error) {
	return f.local.SweepAnswered(ctx, maxAge)
}

// Answer proxies to the peer when the peer owns the item, and applies locally
// otherwise.
//
// The peer's own compare-and-set decides the outcome either way, so a federated
// answer loses a race exactly as a local one does and reports the same
// sentinel — the client maps the peer's error code back rather than flattening
// it (see remoteResponseError).
func (f *federatingAttentionStore) Answer(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	resolvedBy string,
	decision Decision,
	answer *Answer,
	answers Answers,
	answeredClient *AnsweredClient,
) (*Item, error) {
	if f.isRemote(ctx, itemID) {
		return f.remote.Answer(
			ctx, itemID, answeredBy, resolvedBy, decision, answer, answers, answeredClient,
		)
	}
	return f.local.Answer(
		ctx, itemID, answeredBy, resolvedBy, decision, answer, answers, answeredClient,
	)
}

// Escalate proxies to the peer when the peer owns the item, and applies locally
// otherwise.
func (f *federatingAttentionStore) Escalate(
	ctx context.Context,
	itemID ItemID,
	escalatedBy string,
) (*Item, error) {
	if f.isRemote(ctx, itemID) {
		return f.remote.Escalate(ctx, itemID, escalatedBy)
	}
	return f.local.Escalate(ctx, itemID, escalatedBy)
}

// Close proxies to the peer when the peer owns the item, and applies locally
// otherwise.
func (f *federatingAttentionStore) Close(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	answeredClient *AnsweredClient,
) (*Item, error) {
	if f.isRemote(ctx, itemID) {
		return f.remote.Close(ctx, itemID, answeredBy, answeredClient)
	}
	return f.local.Close(ctx, itemID, answeredBy, answeredClient)
}

// RecordAttempt delegates to the local store, on the same reasoning as
// Delivery: it records what THIS process attempted, which is a fact about the
// local arm rather than about the item's owner.
func (f *federatingAttentionStore) RecordAttempt(
	ctx context.Context,
	itemID ItemID,
	carrier string,
	outcome DeliveryOutcome,
) (*DeliveryAttempt, error) {
	return f.local.RecordAttempt(ctx, itemID, carrier, outcome)
}

// Delivery delegates to the local store.
//
// ⚠️ Deliberately not proxied even for a peer-owned item: the delivery trail
// answers "did this item's answer reach the session?", and the session it
// reached is a local one. The peer holds no record of this process's delivery,
// so asking it would answer a different question than the caller asked.
func (f *federatingAttentionStore) Delivery(
	ctx context.Context,
	itemID ItemID,
) (*DeliveryReport, error) {
	return f.local.Delivery(ctx, itemID)
}

// isRemote reports whether the peer, rather than the local store, owns this
// item.
//
// ⚠️ The rule is "the local store does not hold it", and it is derived here
// rather than carried on the item for a reason worth recording: the render has
// no channel to the answer path. An answer arrives as a bare item id in the URL
// plus a body of answer fields, so a mark drawn on the board could not reach
// this decision even in principle. Deriving it at the store is the only place
// both paths can agree.
//
// A local read that fails for any OTHER reason reads as not-remote, so an
// unreadable local store sends the write to the local path — the failure
// direction that cannot misroute a peer's item onto the wrong store.
func (f *federatingAttentionStore) isRemote(ctx context.Context, itemID ItemID) bool {
	_, err := f.local.Get(ctx, itemID)
	return errors.Is(err, ErrItemNotFound)
}

// withRemote merges the peer's open items into a local result.
//
// ⚠️ The merge is where the peer's pruning side effect is ACCEPTED rather than
// worked around, and the reasoning belongs here rather than on the call that
// merely triggers it. The prune is the peer store's own designed semantics and
// every reader of that store triggers it, so the federation introduces no
// defect — it is being one more reader. The alternative was measured and
// rejected: the peer's non-pruning History read would avoid the delete but would
// then SHOW dead-asker items the peer's own board deliberately hides, which
// makes this board a less faithful mirror of the one it federates. If the added
// frequency ever bites, the lever is the cache window — a mitigation, not a
// change of reader.
func (f *federatingAttentionStore) withRemote(ctx context.Context, local Items) Items {
	remote, err := f.remoteItems(ctx)
	if err != nil {
		glog.Warningf(
			"federated read failed, serving %d local items only: %v",
			len(local),
			err,
		)
		return local
	}
	return mergeFederatedItems(local, remote)
}

// remoteItems returns the peer's open items, reusing the last read while the
// cache window holds.
//
// ⚠️ A FAILED read is never cached. Caching it would turn one transient peer
// failure into a window of silence for every caller behind it, and the next
// caller is exactly the one that should retry.
//
// ⚠️ THIS CALL IS NOT READ-ONLY — it mutates the peer. The peer's read prunes
// its dead-asker items as a side effect ("Dead askers are removed from the store
// as a side effect of the read"), so rendering a peer's card can DELETE that
// card upstream. Observed 2026-10-08 on the live dev pair: the operator's board
// showed the pod's card, and minutes later the same card was gone from both the
// peer and the board. See withRemote for why that is accepted.
//
// ⚠️ What the prune costs is FREQUENCY, and only frequency. An item is pruned
// only when it is open, its producer is dead AND it was asked — and such an item
// is omitted from the peer's read either way, so a peer consumer polling a
// moment later sees exactly the same state whether or not this store read
// first. No observation is lost. What changes is that the peer now prunes on
// THIS store's cadence rather than a human's, which is extra write traffic
// against the peer and nothing beyond it.
func (f *federatingAttentionStore) remoteItems(ctx context.Context) (Items, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now.Now()
	if f.haveCached && now.Sub(f.cachedAt) < federationCacheWindow {
		return f.cached, nil
	}
	items, err := f.remote.Read(ctx)
	if err != nil {
		return nil, err
	}
	f.cached = items
	f.cachedAt = f.now.Now()
	f.haveCached = true
	return items, nil
}

// mergeFederatedItems appends the peer's items to the local ones.
//
// ⚠️ An id collision resolves to the LOCAL item. The two stores hold disjoint
// id spaces in practice — an id is generated by the store that owns the row —
// so a collision means something is wrong, and of the two possible wrong
// answers, keeping the row this store can actually act on is the one that
// cannot strand an operator on a card whose controls all fail.
//
// The peer's items are appended rather than interleaved, so a row's position is
// stable across renders and the cluster's cards sit together after the local
// ones. Re-sorting would be this store ranking another store's queue, which it
// has no basis to do: it does not know the peer's liveness or its own order.
func mergeFederatedItems(local Items, remote Items) Items {
	if len(remote) == 0 {
		return local
	}
	held := make(map[ItemID]struct{}, len(local))
	for _, item := range local {
		held[item.ItemID] = struct{}{}
	}
	merged := make(Items, 0, len(local)+len(remote))
	merged = append(merged, local...)
	for _, item := range remote {
		if _, ok := held[item.ItemID]; ok {
			continue
		}
		merged = append(merged, item)
	}
	return merged
}
