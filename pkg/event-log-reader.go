// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// EventLogReader reads a producer's event log incrementally, so a render over
// an unchanged log costs a stat rather than a scan.
//
// ⚠️ It is a seam rather than a bare function because the read path's cost is
// the thing under test: a spec wraps the real reader in a counting fake and
// asserts that a second render over an unchanged log reads nothing at all.
// Without the seam the read is only observable against the real state
// directory, which is not a thing a spec can watch.
type EventLogReader interface {
	// Size returns the current size in bytes of producerID's event log, and
	// false when the log does not exist or cannot be stat'd. Both are
	// ordinary for a producer that never wrote one, never an error.
	Size(producerID ProducerID) (int64, bool)
	// ReadFrom returns the bytes of producerID's event log from offset to the
	// end of the file. It returns an empty slice and no error when the log is
	// absent, and a wrapped error when it exists but cannot be read. It
	// returns BYTES, not records: the caller owns the torn-final-line rule and
	// the per-line decode, so a byte offset is never applied to a text handle.
	ReadFrom(ctx context.Context, producerID ProducerID, offset int64) ([]byte, error)
}

// eventLogReader is the real reader: a state directory and the `os.Root`
// confinement every other event-log read in this package already uses.
type eventLogReader struct {
	stateDir string
}

// NewEventLogReader creates a reader over the event logs beneath stateDir.
//
// ⚠️ It reads in binary rather than through a text handle, because the caller
// applies a byte offset and the payload carries prose. A text-mode offset is
// only correct while every line is pure ASCII; a single multi-byte rune before
// the offset would make the next read start mid-rune.
func NewEventLogReader(stateDir string) EventLogReader {
	return &eventLogReader{stateDir: stateDir}
}

// eventLogName is the file name a producer's log is written under, exactly as
// readEvents built it before this seam existed.
func eventLogName(producerID ProducerID) string {
	return string(producerID) + ".events.jsonl"
}

// Size returns the log's size in bytes, or false when it is absent.
func (r *eventLogReader) Size(producerID ProducerID) (int64, bool) {
	if r.stateDir == "" {
		return 0, false
	}
	// Confined through an os.Root: the producer id is producer-supplied and
	// validated only by NotEmptyString, so a crafted id containing path
	// separators would otherwise walk out of the state directory.
	root, err := os.OpenRoot(r.stateDir)
	if err != nil {
		// Absent state dir is the ordinary case for a store with no Claude Code
		// beside it, not an error worth reporting per item.
		glog.V(3).Infof("open attention state dir %s failed: %v", r.stateDir, err)
		return 0, false
	}
	defer root.Close()

	info, err := root.Stat(eventLogName(producerID))
	if err != nil {
		// Absent log is the ordinary case for a producer that never wrote one.
		glog.V(3).
			Infof("stat event log %s failed: %v", filepath.Join(r.stateDir, eventLogName(producerID)), err)
		return 0, false
	}
	return info.Size(), true
}

// ReadFrom returns the log's bytes from offset to EOF.
//
// An absent log yields (nil, nil) rather than an error, because a producer
// that never wrote one is the ordinary case. A log that exists but cannot be
// opened or read yields a wrapped error, which the resolver fails soft on —
// the same direction the old whole-file scan took.
func (r *eventLogReader) ReadFrom(
	ctx context.Context,
	producerID ProducerID,
	offset int64,
) ([]byte, error) {
	if r.stateDir == "" {
		return nil, nil
	}
	root, err := os.OpenRoot(r.stateDir)
	if err != nil {
		glog.V(3).Infof("open attention state dir %s failed: %v", r.stateDir, err)
		return nil, nil
	}
	defer root.Close()

	name := eventLogName(producerID)
	file, err := root.Open(name)
	if err != nil {
		if os.IsNotExist(err) {
			// Absent log is the ordinary case for a producer that never wrote one.
			glog.V(3).Infof("open event log %s failed: %v", filepath.Join(r.stateDir, name), err)
			return nil, nil
		}
		return nil, errors.Wrap(ctx, err, "open event log failed")
	}
	defer file.Close()

	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return nil, errors.Wrap(ctx, err, "seek event log failed")
		}
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read event log failed")
	}
	return data, nil
}
