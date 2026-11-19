// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// This file holds the crossing book: the record of which sessions have already
// been cued for their CURRENT crossing.
//
// ⚠️ It exists because the card cannot be the record. The crossing card is an
// `ack`, and an acknowledgement CLOSES the item (`answered_at` stays unset),
// which removes it from every read the sink can make — ReadBoard renders open
// and answered items only, and the store's duplicate suppression collapses onto
// an open row only. So a guard anchored to the card is destroyed by the very
// answer it is meant to absorb, and the next 60 s tick mints another card.
// Measured 2026-11-19 on the live store: 1 376 `compact:` cards on 51 keys,
// 1 325 of them re-mints, the worst key minting 161 at a 1.1 min median gap.
//
// The guard therefore has to be anchored to the CROSSING: one raise per span in
// which a session sits at or above the threshold, re-armed only by a real
// sub-threshold reading. This book is that record, and it is persisted because
// the controller restarts (every deploy does), and an in-memory book would mint
// one fresh card per live crossing at each restart.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// ContextCrossingBookFileName is the file the crossing book is persisted to,
// beneath the attention state dir. The statusline owns the context-state
// directory, so the book is written beside the event logs instead of among the
// readings it judges.
const ContextCrossingBookFileName = "context-crossing-book.json"

// contextCrossingBook is the per-session record of the current crossing.
type contextCrossingBook struct {
	path string
	// entries is keyed by session id. A session present here has been cued for
	// the crossing it is currently in.
	entries map[string]contextCrossingEntry
	// loaded reports whether the file has been read. ScanOnce loads lazily so a
	// job can be constructed without a context.
	loaded bool
}

// contextCrossingEntry is what the book keeps for one cued session.
//
// ⚠️ The token count is diagnostic only — nothing reads it back to decide
// anything. The decision is the entry's PRESENCE, because the crossing is
// re-armed by a sub-threshold reading, never by the count drifting.
type contextCrossingEntry struct {
	Tokens int `json:"tokens"`
}

// newContextCrossingBook creates an empty book over the given file.
//
// An empty path makes the book memory-only. That is the test seam, and it is
// also the honest degradation for a job constructed without a state dir: the
// fix still holds within one process lifetime.
func newContextCrossingBook(path string) *contextCrossingBook {
	return &contextCrossingBook{
		path:    path,
		entries: map[string]contextCrossingEntry{},
	}
}

// load reads the book from disk.
//
// ⚠️ A missing file is an empty book, not an error: the first run after this
// change, and every run with no state dir, starts with nothing cued.
//
// ⚠️ An UNREADABLE file is also an empty book, logged rather than returned. The
// cost of an empty book is bounded and self-correcting — one extra card per live
// crossing, once — while failing the scan would stop the arm entirely, which is
// the defect this book exists to bound. The log line is what keeps the silent
// degradation visible.
func (b *contextCrossingBook) load(ctx context.Context) error {
	b.loaded = true
	if b.path == "" {
		return nil
	}
	content, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.Wrap(ctx, err, "read crossing book failed")
	}
	var raw struct {
		Crossings map[string]contextCrossingEntry `json:"crossings"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		glog.Warningf(
			"job=attention-watcher action=crossing_book_unreadable path=%s error=%v",
			b.path,
			err,
		)
		return nil
	}
	if raw.Crossings != nil {
		b.entries = raw.Crossings
	}
	return nil
}

// raised reports whether this session has already been cued for its crossing.
func (b *contextCrossingBook) raised(sessionID string) bool {
	_, ok := b.entries[sessionID]
	return ok
}

// mark records that the session has been cued for its crossing.
func (b *contextCrossingBook) mark(ctx context.Context, record ContextCrossingRecord) error {
	b.entries[record.SessionID] = contextCrossingEntry{Tokens: record.Tokens}
	return b.save(ctx)
}

// clear re-arms the session, so its next crossing raises a fresh card.
//
// ⚠️ It is called on a real sub-threshold reading and on a session that is not
// live — never on a zero reading. A zero is "nothing measured yet", not "the
// context emptied", and treating it as a re-arm would mint a second card for a
// crossing that never ended (see readContextCrossing).
func (b *contextCrossingBook) clear(ctx context.Context, sessionID string) error {
	if _, ok := b.entries[sessionID]; !ok {
		return nil
	}
	delete(b.entries, sessionID)
	return b.save(ctx)
}

// prune drops entries whose session file is gone and whose session is not live.
//
// ⚠️ Both conditions are required. A state file that has not been rewritten in
// a while is normal for a quiet live session, so absence of a file alone must
// not re-arm it; and a session that is not live cannot be cued, so its entry is
// pure growth. A resumed session keeps its id, so dropping the entry is what
// lets a resumed run receive its own card.
func (b *contextCrossingBook) prune(
	ctx context.Context,
	present map[string]bool,
	liveness SessionLivenessChecker,
) error {
	removed := false
	for sessionID := range b.entries {
		if present[sessionID] {
			continue
		}
		if liveness.IsLive(ctx, sessionID) {
			continue
		}
		delete(b.entries, sessionID)
		removed = true
	}
	if !removed {
		return nil
	}
	return b.save(ctx)
}

// save writes the book atomically: a temp file in the same directory, then a
// rename, so a reader never sees half a book.
func (b *contextCrossingBook) save(ctx context.Context) error {
	if b.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o750); err != nil {
		return errors.Wrap(ctx, err, "create crossing book dir failed")
	}
	body, err := json.Marshal(struct {
		Crossings map[string]contextCrossingEntry `json:"crossings"`
	}{Crossings: b.entries})
	if err != nil {
		return errors.Wrap(ctx, err, "encode crossing book failed")
	}
	// ⚠️ 0600: the book names session ids and nothing but this process reads it.
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return errors.Wrap(ctx, err, "write crossing book failed")
	}
	if err := os.Rename(tmp, b.path); err != nil {
		return errors.Wrap(ctx, err, "replace crossing book failed")
	}
	return nil
}
