// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog

import (
	"context"

	"github.com/bborbe/errors"
)

// expiryClosedBy is the `closed_by` a prune writes. It names the cause, so a
// reader can tell an expiry removal from an answer or a session end.
const expiryClosedBy = "Expired"

// ExpiryCloseWriter appends the close record the store owes the event log when
// it prunes an item on expiry.
//
// ⚠️ The line must land in the SAME `<session_id>.events.jsonl` that carries the
// item's `open` record. The fold is per file — `foldEventFile` in
// pkg/who-needs-me.go folds each log independently and `foldEventLogs` appends
// their results — so a close written to any other file leaves the item open in
// the fold that replays the original one. That is the whole reason this writer
// takes a session id rather than a free path.
//
// ⚠️ The record is written with `forward: false`. pkg/event-ingest treats a
// non-open record with `forward: false` as consumed-but-not-forwarded, which is
// what this is: the store already deleted the row, so forwarding the close would
// be a second, redundant mutation of an item that is gone.
type ExpiryCloseWriter struct {
	// StateDir is the directory holding the producers' `*.events.jsonl` files.
	StateDir string
}

// NewExpiryCloseWriter returns a writer rooted at stateDir.
func NewExpiryCloseWriter(stateDir string) *ExpiryCloseWriter {
	return &ExpiryCloseWriter{StateDir: stateDir}
}

// WriteExpiryClose appends one `close` record for itemID into the session's own
// event log, at ts (Unix epoch seconds, the unit every event record uses).
//
// It returns the failure rather than logging it: the caller is the store, and a
// close that did not land is a fold that still reports the item open — the
// defect this exists to fix. A hook may fail open; this may not.
func (w *ExpiryCloseWriter) WriteExpiryClose(
	ctx context.Context,
	sessionID string,
	itemID string,
	ts float64,
) error {
	if !validSessionID(sessionID) {
		return errors.Errorf(
			ctx,
			"expiry close: session id '%s' is not a plain file-name component",
			sessionID,
		)
	}
	if itemID == "" {
		return errors.Errorf(ctx, "expiry close: empty item id")
	}
	return appendStateLineErr(ctx, w.StateDir, eventsPath(sessionID), map[string]any{
		"item_id":    itemID,
		"session_id": sessionID,
		"ts":         ts,
		"type":       "close",
		"closed_by":  expiryClosedBy,
		"forward":    false,
	})
}
