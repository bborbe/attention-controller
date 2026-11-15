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
// is what keeps the store bounded.
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
