// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"container/list"
	"context"
	"strings"
	"sync"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
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

// historyPageCacheBudget is the total encoded bytes the cache may retain.
//
// ⚠️ It bounds BYTES rather than entries, and that is the fix for the GC spiral
// of 2026-11-16. The cache used to hold DECODED Items, and one 10,000-row page of
// them measured 156 MB of live heap — 80 % of the process's live set — against a
// launchd GOMEMLIMIT of 112 MiB. A live set permanently above the soft limit is a
// collector that never idles: measured 96.7 % of one core with `gcBgMarkWorker`
// at 74.4 % of a 15 s profile, 35 minutes after a deploy that had started at
// 17 %. The same page encoded is 11,636,621 bytes (measured on the live board),
// a factor of ~13, and the encoded form is exactly what goes on the wire.
//
// ⚠️ 64 MiB is four of those pages — enough that the production poller shapes
// (`limit=50000`, and the same with `dedup_key_prefix=park:`) and a bounded read
// coexist without evicting each other — and 17 % of the 384 MiB GOMEMLIMIT the
// launchd plist sets. It is a `var` only so a spec can lower it; the bound is the
// cache's whole contract and a spec that had to build 64 MiB to exercise it would
// not run. Same seam shape as historyPageCollector below.
var historyPageCacheBudget = 64 << 20

// historyPageDecodeLimit caps how many pages are being built at once.
//
// ⚠️ The budget bounds what is RETAINED; this bounds what is IN FLIGHT. A burst
// of distinct pages is the case the budget alone cannot cover: twelve concurrent
// misses each walk the index and hold their own buffer, so twelve 16 MB bodies
// exist before any of them is stored. Two is enough to keep the read path busy
// without letting a burst set the process footprint.
var historyPageDecodeLimit = 2

// historyPageCache serves repeat History reads without re-walking the index.
//
// ⚠️ It exists because the history endpoint was the process's largest single
// allocator: a lifetime `alloc_space` profile on 2026-11-07 (683 GB over 10.5 h,
// v0.69.1) put `collectHistoryPage` at 67.9 % cumulative — 464 GB — with
// `decodeHistoryIndexEntry` 60.4 %, `json.makeString` 21.3 % and `slices.Grow`
// 12.0 % behind it. About 22 pollers each ask for the full 10,000-row page every
// 60 s, and every one of those requests re-decoded every row to answer a page
// that had not changed.
//
// ⚠️ **It caches the ENCODED page, not the decoded Items.** The decoded form is
// what made the cache the memory problem it was built to solve: a `[]Item` of
// 10,000 rows carries every pointer field of Item, while the JSON body carries
// only the fields that are set. The handler writes the body straight to the
// response, so a hit costs a `Write` and no decode at all — the decode was 60 %
// of the original allocation profile and it no longer happens on the hot path.
//
// ⚠️ **The generation counter is what makes the cache safe, not the mutex.** A
// History that started before a write would otherwise store a page that predates
// it, and the next reader would be answered a history missing that write. Every
// write bumps the generation, and a page is only stored if the generation it was
// read under is still current — so a racing write costs one cache miss, never a
// stale answer.
//
// ⚠️ **Expiry is bounded by a horizon, not re-applied on read.** `walkHistoryPage`
// drops an OPEN item once it expires, and an expiry is not a write, so no
// invalidate arrives to drop the page. The previous cache re-walked the decoded
// page on every hit to filter those rows; the encoded page cannot be filtered
// without decoding it, which is the cost this cache exists to avoid. Instead each
// entry records the earliest deadline among its OPEN items: before that instant
// the page is exactly what a fresh walk would return, and at it the entry is
// dropped and the next read re-walks. The endpoint's contract — an expired OPEN
// row is not served — holds without a decode.
//
// ⚠️ **A page larger than the budget is served but never retained.** `History`
// documents `limit <= 0` as an unbounded compatibility path, and the live store
// held 90,000+ rows on 2026-11-16: that page is ~100 MB encoded and ~1.3 GB
// decoded, so it must not sit on the heap between reads. The budget is the one
// rule that covers both the endpoint's 10,000-row page and that path.
//
// ⚠️ **One walk per page, however many callers ask for it.** The pollers tick on
// the same cadence, so a cache miss is routinely several misses at once; without
// the flight map each of them walks the index and the cache is refilled N times
// for one answer.
type historyPageCache struct {
	mu      sync.Mutex
	gen     uint64
	total   int
	entries map[historyPageKey]*historyPageEntry
	order   *list.List
	flights map[historyPageKey]*historyPageFlight
	decodes chan struct{}
}

// historyPageEntry is one cached page and its recency slot.
type historyPageEntry struct {
	key        historyPageKey
	body       []byte
	validUntil libtime.DateTime
	elem       *list.Element
}

// historyPageFlight is one page being built. Every caller that arrives while it
// is in flight waits on done and receives the same body.
type historyPageFlight struct {
	gen  uint64
	done chan struct{}
	body []byte
	err  error
}

// historyPageCacheLogged guards the budget log line so it is written once per
// process. Every spec that builds a store would otherwise log it.
var historyPageCacheLogged sync.Once

// newHistoryPageCache builds an empty cache and names its bound in the log.
//
// ⚠️ The line is `glog.Infof` at V(0), not V(2): the budget is the operator-visible
// answer to "how much heap may this cache take", and it has to be readable from
// the running instance's default-verbosity log rather than inferred from a heap
// profile. Same reasoning as the pprof mount line in main.go.
func newHistoryPageCache() *historyPageCache {
	historyPageCacheLogged.Do(func() {
		glog.Infof(
			"history page cache: budget=%d bytes decodeLimit=%d",
			historyPageCacheBudget,
			historyPageDecodeLimit,
		)
	})
	return &historyPageCache{
		entries: make(map[historyPageKey]*historyPageEntry),
		order:   list.New(),
		flights: make(map[historyPageKey]*historyPageFlight),
		decodes: make(chan struct{}, historyPageDecodeLimit),
	}
}

// load returns the cached body for key and the generation it was read under.
//
// A nil body is a miss. The entry is dropped rather than returned once its expiry
// horizon has passed, so the caller re-walks and the page loses the row that has
// just expired.
func (c *historyPageCache) load(key historyPageKey, now libtime.DateTime) ([]byte, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, c.gen
	}
	if !entry.validUntil.Time().IsZero() && !now.Time().Before(entry.validUntil.Time()) {
		c.removeLocked(entry)
		return nil, c.gen
	}
	c.order.MoveToFront(entry.elem)
	return entry.body, c.gen
}

// begin returns the in-flight build for key, or starts one.
//
// The second result is true when the caller joined an existing flight and must
// wait on its done channel instead of walking the index itself.
func (c *historyPageCache) begin(key historyPageKey) (*historyPageFlight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if flight, ok := c.flights[key]; ok {
		return flight, true
	}
	flight := &historyPageFlight{gen: c.gen, done: make(chan struct{})}
	c.flights[key] = flight
	return flight, false
}

// complete publishes a built page to every waiter and caches it when no write
// landed while it was being read.
func (c *historyPageCache) complete(
	key historyPageKey,
	flight *historyPageFlight,
	body []byte,
	validUntil libtime.DateTime,
	err error,
) {
	c.mu.Lock()
	if err == nil {
		c.storeLocked(flight.gen, key, body, validUntil)
	}
	delete(c.flights, key)
	flight.body = body
	flight.err = err
	close(flight.done)
	c.mu.Unlock()
}

// acquireDecode takes one decode slot, or reports false when ctx ended first.
func (c *historyPageCache) acquireDecode(ctx context.Context) bool {
	select {
	case c.decodes <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// releaseDecode returns one decode slot.
func (c *historyPageCache) releaseDecode() {
	<-c.decodes
}

// storeLocked caches a page only when no write landed since it was read, and
// evicts least-recently-used pages until the total is back inside the budget.
func (c *historyPageCache) storeLocked(
	gen uint64,
	key historyPageKey,
	body []byte,
	validUntil libtime.DateTime,
) {
	if c.gen != gen {
		return
	}
	if len(body) > historyPageCacheBudget {
		if entry, ok := c.entries[key]; ok {
			c.removeLocked(entry)
		}
		return
	}
	if entry, ok := c.entries[key]; ok {
		c.total += len(body) - len(entry.body)
		entry.body = body
		entry.validUntil = validUntil
		c.order.MoveToFront(entry.elem)
	} else {
		entry := &historyPageEntry{key: key, body: body, validUntil: validUntil}
		entry.elem = c.order.PushFront(entry)
		c.entries[key] = entry
		c.total += len(body)
	}
	for c.total > historyPageCacheBudget {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.removeLocked(oldest.Value.(*historyPageEntry))
	}
}

// invalidate drops every cached page and advances the generation.
//
// In-flight builds are left alone: their gen no longer matches, so they publish
// to their own waiters and are not stored.
func (c *historyPageCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.entries = make(map[historyPageKey]*historyPageEntry)
	c.order.Init()
	c.total = 0
}

// size reports the bytes currently retained. It exists for the specs that pin
// the budget; nothing in the read path consults it.
func (c *historyPageCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// removeLocked drops one entry and its recency slot. The caller holds c.mu.
func (c *historyPageCache) removeLocked(entry *historyPageEntry) {
	c.order.Remove(entry.elem)
	delete(c.entries, entry.key)
	c.total -= len(entry.body)
}

// historyPageCollector runs one index walk for History and returns the encoded
// page with the instant it stops matching a fresh walk.
//
// ⚠️ It is a package variable so a spec can count walks: the cache's whole claim
// is that a repeat read does not re-walk the index, and a count cannot be
// asserted against a call the spec cannot see. Same seam shape as
// sessionRegistryLister in session-liveness-checker.go.
var historyPageCollector = func(
	a *attentionStore,
	ctx context.Context,
	limit int,
	offset int,
	filter historyMatcher,
) ([]byte, libtime.DateTime, error) {
	return a.collectHistoryPageJSON(ctx, limit, offset, filter)
}
