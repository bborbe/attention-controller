// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"time"

	libtime "github.com/bborbe/time"
)

// DefaultItemExpiry is how long an item lives when its producer declares no
// `expires_at`. [[Attention Item Schema]] § Expiry: `expires_at` is the one
// clock that clears an item, and a push without one is stored with
// `created_at + 24h`.
//
// ⚠️ It is the bound the liveness prune used to provide. A dead liveness
// subject no longer removes an item — the idle-reaper ends headless workers
// after 15–30 minutes, and removing their asks on exit deleted questions, and
// the answers about to land on them, from sessions that were then resumed. With
// that prune gone, an item without a deadline would never leave, so the default
// is what keeps the store bounded — for `session:` and `heartbeat:` items.
//
// ⚠️ It does NOT bound an `owner:` item, and that exception is the one hole in
// the sentence above: `isProducerLive` returns a constant true for the owner
// model, so a defaulted owner deadline is renewed on every pass and an
// unanswered owner gate lives until something else closes it.
const DefaultItemExpiry = libtime.Duration(24 * time.Hour)

// expiresAtOrDefault returns the producer's deadline and whether it had one, or
// createdAt + DefaultItemExpiry when it declared none. Both write paths — a
// fresh push and a dedup re-push — call it, so a re-push without a deadline
// re-arms the default from its new createdAt rather than clearing the stored
// one, and the second result is what the item records as
// `expires_at_defaulted`.
func expiresAtOrDefault(
	expiresAt *libtime.DateTime,
	createdAt libtime.DateTime,
) (*libtime.DateTime, bool) {
	if expiresAt != nil {
		return expiresAt, false
	}
	deadline := createdAt.Add(DefaultItemExpiry)
	return &deadline, true
}

// effectiveExpiresAt is the deadline an item is judged against. A row written
// before the default existed carries no `expires_at`, and reading that absence
// as "never" would keep it forever now that liveness removes nothing — so it is
// given the same createdAt + DefaultItemExpiry a fresh push would have stored.
func effectiveExpiresAt(item *Item) libtime.DateTime {
	deadline, _ := expiresAtOrDefault(item.ExpiresAt, item.CreatedAt)
	return *deadline
}

// defaultedDeadline reports whether the item's deadline came from the store's
// own default and is therefore renewable.
//
// ⚠️ A nil `ExpiresAt` counts as defaulted, and that is the LEGACY case rather
// than a defensive one. Rows written before `ExpiresAtDefaulted` existed carry
// neither the flag nor a deadline, so the flag reads false for every one of them
// — and a gate on the flag alone skipped them all. Combined with
// effectiveExpiresAt's fallback that meant a legacy row with a LIVE producer was
// never renewed and died at created_at + DefaultItemExpiry: the exact loss the
// renewal pass exists to prevent, applied to the whole pre-existing population.
// A row with no deadline is one expiresAtOrDefault gives the same default a
// fresh push would have stored, so it is renewable by the same rule.
//
// ⚠️ A producer-set deadline is still never renewed: it has a non-nil
// `ExpiresAt` and the flag false. Both halves are needed — the flag alone misses
// every legacy row, and the nil test alone would treat a producer that cleared
// its own deadline as defaulted.
func defaultedDeadline(item *Item) bool {
	return item.ExpiresAtDefaulted || item.ExpiresAt == nil
}

// renewalThreshold is how little lifetime a defaulted deadline must have left
// before the renewal pass writes it forward to now + DefaultItemExpiry.
//
// ⚠️ It is half the window, and the two bounds that buys are the whole reason it
// is not zero. Renewing on every pass would rewrite every live item's
// `expires_at` once a minute for no gain — the value only matters when it is
// near. Renewing at a threshold keeps the promise that a live session's item is
// never cleared: the deadline never drops below this much remaining while its
// subject is alive, so the worst case is one renewal per threshold rather than
// one per tick. The item's lifetime after its LAST renewal is still
// DefaultItemExpiry, which is the bound § Expiry states.
const renewalThreshold = DefaultItemExpiry / 2
