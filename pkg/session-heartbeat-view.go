// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"math"

	libtime "github.com/bborbe/time"
)

// SessionHeartbeatView is one heartbeat row as a READER sees it: the stored
// declaration plus the freshness the store deliberately does not compute.
//
// ⚠️ Freshness lives here rather than in the store because the store is shared
// with the legacy Node writers, which know nothing of this task's window. The
// store answers "what does the row say"; the read path answers "is that row
// still live", against the one window it was built with. Putting the verdict in
// the store would give the 60-second constant a second home.
type SessionHeartbeatView struct {
	SessionHeartbeat
	// AgeSeconds is how long ago the store stamped the row. Exposed as a
	// number rather than left to the reader to derive from `at`, so a script
	// and the board cannot disagree about the arithmetic.
	//
	// ⚠️ It can be NEGATIVE, and when it is, `Live` is the answer — not this
	// field. A row stamped ahead of the reader's clock yields a negative age
	// (e.g. `-300` beside `live: false`), and a reader testing `age <= window`
	// would read that as in-window. Clamping to zero does NOT fix it, since
	// `0 <= window` is true as well; the only honest rule is that a negative
	// age means the clock is skewed, and the verdict is the one to trust. The
	// store stamps `At` from its own clock, so reaching this needs cross-host
	// skew rather than a hostile caller — but a reader must not have to work
	// that out to get the right answer.
	AgeSeconds int `json:"age_seconds"`
	// Live reports whether the age is within the read path's window. ⚠️ It is
	// the ONLY thing that separates `live` from `stale` — a reader that ignores
	// it and keys on the row's mere presence renders a killed session as
	// running, which is the defect this whole task exists to remove.
	Live bool `json:"live"`
}

// NewSessionHeartbeatView projects a stored heartbeat onto the read shape,
// measuring its age against the supplied clock and window.
//
// ⚠️ `AgeSeconds` is ROUNDED UP, and that is what keeps it from contradicting
// `Live`. Truncating would render a 60.9-second-old row as `age_seconds: 60`
// while `Live` — which compares the real duration against the window — said
// false, so a reader keying on the age would see an in-window row the same
// struct marks dead. Rounding up makes the integer agree with the verdict at
// every boundary: 60.0 s stays 60 (Live), 60.9 s becomes 61 (not Live).
func NewSessionHeartbeatView(
	heartbeat SessionHeartbeat,
	now libtime.DateTime,
	window libtime.Duration,
) SessionHeartbeatView {
	age := now.Sub(heartbeat.At)
	return SessionHeartbeatView{
		SessionHeartbeat: heartbeat,
		AgeSeconds:       int(math.Ceil(age.Duration().Seconds())),
		Live:             heartbeat.IsFresh(now, window),
	}
}

// NewSessionHeartbeatViews projects a whole listing, in the store's own order.
func NewSessionHeartbeatViews(
	heartbeats SessionHeartbeats,
	now libtime.DateTime,
	window libtime.Duration,
) []SessionHeartbeatView {
	views := make([]SessionHeartbeatView, 0, len(heartbeats))
	for _, heartbeat := range heartbeats {
		views = append(views, NewSessionHeartbeatView(heartbeat, now, window))
	}
	return views
}
