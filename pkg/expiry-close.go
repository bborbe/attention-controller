// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "context"

// ExpiryCloseWriter records an expiry removal into the event log a fold may
// replay, so an item the store pruned does not stay open there.
//
// ⚠️ The record must be appended to the producer session's own
// `<session_id>.events.jsonl` — the file carrying the item's `open` record.
// `foldEventFile` folds each log independently, so a close written anywhere else
// is invisible to the fold that replays the item's own log.
//
// ts is Unix epoch seconds, the unit every event record uses.
//
// The store calls this AFTER the removal transaction commits, and logs rather
// than propagates a failure: the row is already gone, and a read must not fail
// because a diagnostic log could not be appended.
type ExpiryCloseWriter interface {
	WriteExpiryClose(ctx context.Context, sessionID string, itemID string, ts float64) error
}
