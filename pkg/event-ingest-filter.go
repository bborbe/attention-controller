// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"time"
)

// CompletedTask is a task the vault records as finished, with the moment it
// finished.
type CompletedTask struct {
	// Name is the task's title — its filename without the `.md` suffix.
	Name string
	// CompletedAt is when the vault recorded it finished. A zero value means
	// the record carries no completion time, and a task whose completion time
	// is unknown cannot be used to suppress a card.
	CompletedAt time.Time
}

// CompletedTaskSource returns the vault's finished tasks.
//
// ⚠️ It is a source function rather than the TaskIndex interface because the
// index is keyed by session and this filter needs the whole finished
// population. Binding the filter to the index would widen an interface every
// fake implements for one caller's read.
type CompletedTaskSource func(ctx context.Context) []CompletedTask

// EventIngestFilter decides whether an open record is consumed without being
// forwarded.
//
// It is the seam for the class-3 rule: a record that would otherwise raise a
// card is dropped when the thing it reports on has already been disposed of.
type EventIngestFilter interface {
	// Skip reports whether the record should be consumed silently. It is only
	// consulted for records that would otherwise be forwarded.
	Skip(ctx context.Context, record EventIngestRecord) bool
}

// NewFinishedTaskFilter creates the class-3 filter.
func NewFinishedTaskFilter(source CompletedTaskSource) EventIngestFilter {
	return &finishedTaskFilter{source: source}
}

type finishedTaskFilter struct {
	source CompletedTaskSource
}

// Skip reports whether this record names a task that was already finished when
// the record was written.
//
// ⚠️ **`Stop`-sourced ONLY, and that is the whole safety argument.** An
// `AskUserQuestion` card is the only signal that a session is BLOCKED on the
// operator; suppressing it would trade a low-value card for a stranded
// session, which is strictly worse. A `Stop` closer strands nothing — the
// session is idle either way.
func (f *finishedTaskFilter) Skip(ctx context.Context, record EventIngestRecord) bool {
	if !record.Open || record.Event != "Stop" {
		return false
	}
	if record.TS == nil || record.Detail == "" {
		// No usable ask time means the ordering cannot be established, and no
		// detail means nothing names a task. Neither is a reason to suppress.
		return false
	}
	askedAt := time.Unix(int64(*record.TS), 0)
	for _, task := range f.source(ctx) {
		if task.Name == "" || task.CompletedAt.IsZero() {
			continue
		}
		if !task.CompletedAt.After(askedAt) && strings.Contains(record.Detail, task.Name) {
			return true
		}
	}
	return false
}

// Compile-time guard: the filter must satisfy its own interface.
var _ EventIngestFilter = (*finishedTaskFilter)(nil)
