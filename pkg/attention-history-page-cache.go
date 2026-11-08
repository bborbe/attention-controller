// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"sync"

	libkv "github.com/bborbe/kv"
)

// historyPageKey identifies one page request. Every field is part of the answer,
// so two requests with different keys must never share a cache entry.
type historyPageKey struct {
	limit       int
	offset      int
	producers   string
	dedupPrefix string
}

// newHistoryPageKey folds a History call's bounds and filters into the key.
//
// ⚠️ The producers are joined with a NUL, not a comma: a ProducerID is free text,
// so `["a,b"]` and `["a", "b"]` would collide on a comma-joined key and one
// caller would be answered the other's page.
func newHistoryPageKey(limit int, offset int, filters []HistoryFilter) historyPageKey {
	var producers []string
	var dedupPrefix string
	for _, filter := range filters {
		for _, producer := range filter.Producers {
			producers = append(producers, string(producer))
		}
		if filter.DedupKeyPrefix != "" {
			dedupPrefix = string(filter.DedupKeyPrefix)
		}
	}
	return historyPageKey{
		limit:       limit,
		offset:      offset,
		producers:   strings.Join(producers, "\x00"),
		dedupPrefix: dedupPrefix,
	}
}

// historyPageCache serves a repeat History read without re-walking the index.
//
// ⚠️ It exists because the history endpoint was the process's largest single
// allocator: a lifetime `alloc_space` profile on 2026-11-07 (683 GB over 10.5 h,
// v0.69.1) put `collectHistoryPage` at 67.9 % cumulative — 464 GB — with
// `decodeHistoryIndexEntry` 60.4 %, `json.makeString` 21.3 % and `slices.Grow`
// 12.0 % behind it. About 22 pollers each ask for the full 10,000-row page every
// 60 s, and every one of those requests re-decoded every row to answer a page
// that had not changed. The resulting allocation fed ~0.6 GCs/s against the
// 112 MiB soft limit, and that GC cost 15-20 % of the process's CPU.
//
// ⚠️ **The generation counter is what makes the cache safe, not the mutex.** A
// History that started before a write would otherwise store a page that predates
// it, and the next reader would be answered a history missing that write. Every
// write bumps the generation, and a page is only stored if the generation it was
// read under is still current — so a racing write costs one cache miss, never a
// stale answer.
//
// ⚠️ One entry, not a map. The pollers issue the same query, so a single slot
// captures the traffic that matters while keeping the memory bounded; a second
// distinct query simply evicts the first.
type historyPageCache struct {
	mu    sync.Mutex
	gen   uint64
	key   historyPageKey
	items Items
	have  bool
}

// load returns the cached page and the generation it was read under.
func (c *historyPageCache) load(key historyPageKey) (Items, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.have && c.key == key {
		return c.items, c.gen
	}
	return nil, c.gen
}

// store caches a page only when no write landed since it was read.
func (c *historyPageCache) store(gen uint64, key historyPageKey, items Items) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen {
		c.key = key
		c.items = items
		c.have = true
	}
}

// invalidate drops the cached page and advances the generation.
func (c *historyPageCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.items = nil
	c.have = false
}

// reapplyExpiry re-applies the wall-clock narrowing to a cached page.
//
// ⚠️ The cache key cannot carry time, but `collectHistoryPage` drops an OPEN
// item once it expires — the endpoint's ONE documented narrowing. Serving a
// cached page unchanged would silently undo it, and no write would arrive to
// drop the entry, because an expiry is not a write. Re-checking costs a walk
// over the page's own structs and no decode at all.
//
// ⚠️ The page may therefore come back SHORTER than limit rather than shifting
// the later rows up. That is inside the endpoint's stated contract: its doc
// already says a row can stop being reachable partway through a paginated walk
// while an item is expiring, and a consumer counting must walk faster than the
// expiry horizon or pin a cutoff.
func (a *attentionStore) reapplyExpiry(items Items) Items {
	now := a.currentDateTimeGetter.Now()
	expired := false
	for i := range items {
		if items[i].State == OpenState && isExpired(&items[i], now) {
			expired = true
			break
		}
	}
	if !expired {
		return items
	}
	kept := make(Items, 0, len(items))
	for i := range items {
		if items[i].State == OpenState && isExpired(&items[i], now) {
			continue
		}
		kept = append(kept, items[i])
	}
	return kept
}

// historyPageCollector runs one index walk for History.
//
// ⚠️ It is a package variable so a spec can count walks: the cache's whole claim
// is that a repeat read does not re-walk the index, and a count cannot be
// asserted against a call the spec cannot see. Same seam shape as
// sessionRegistryLister in session-liveness-checker.go.
var historyPageCollector = func(
	a *attentionStore,
	ctx context.Context,
	tx libkv.Tx,
	limit int,
	offset int,
	filter historyMatcher,
) (Items, error) {
	return a.collectHistoryPage(ctx, tx, limit, offset, filter)
}
