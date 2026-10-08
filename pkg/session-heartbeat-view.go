// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
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
	AgeSeconds int `json:"age_seconds"`
	// Live reports whether the age is within the read path's window. ⚠️ It is
	// the ONLY thing that separates `live` from `stale` — a reader that ignores
	// it and keys on the row's mere presence renders a killed session as
	// running, which is the defect this whole task exists to remove.
	Live bool `json:"live"`
}

// NewSessionHeartbeatView projects a stored heartbeat onto the read shape,
// measuring its age against the supplied clock and window.
func NewSessionHeartbeatView(
	heartbeat SessionHeartbeat,
	now libtime.DateTime,
	window libtime.Duration,
) SessionHeartbeatView {
	return SessionHeartbeatView{
		SessionHeartbeat: heartbeat,
		AgeSeconds:       int(now.Sub(heartbeat.At).Duration().Seconds()),
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
