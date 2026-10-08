// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	libtime "github.com/bborbe/time"
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewSessionHeartbeatGetHandler creates the handler that answers one session's
// liveness.
//
// ⚠️ It reports three outcomes, and keeping them distinct is the whole point:
// a row inside the window is `live`, a row outside it is `stale` (HTTP 200 with
// `live: false`), and NO ROW AT ALL is `absent` (HTTP 404). Collapsing `absent`
// onto `stale` would make a never-seen id and a killed session read the same,
// which is exactly the distinction a caller uses to decide between Resume and
// Stale.
func NewSessionHeartbeatGetHandler(
	store pkg.SessionHeartbeatStore,
	now libtime.CurrentDateTimeGetter,
	window libtime.Duration,
) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleSessionHeartbeatGet(ctx, resp, req, store, now, window)
			},
		),
	)
}

func handleSessionHeartbeatGet(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.SessionHeartbeatStore,
	now libtime.CurrentDateTimeGetter,
	window libtime.Duration,
) error {
	sessionID := mux.Vars(req)["sessionID"]
	heartbeat, found, err := store.Get(ctx, sessionID)
	if err != nil {
		// ⚠️ A MALFORMED id is the CLIENT's error, and it must not be folded into
		// the store-failure branch below. `validateSessionID` refuses `..`, `.`
		// and anything carrying a path separator, so a caller sending one gets
		// 400 — reporting 500 would blame the server for the caller's input, and
		// routing it through the same branch would blur the guarantee this file
		// exists to state: an unreadable STORE is a failure, never `absent`.
		if errors.Is(err, pkg.ErrInvalidSessionID) {
			return libhttp.WrapWithDetails(
				errors.Wrap(ctx, err, "invalid session id"),
				libhttp.ErrorCodeValidation,
				http.StatusBadRequest,
				map[string]any{"session_id": sessionID},
			)
		}
		// An unreadable store is a FAILURE, never `absent` — reporting a read
		// error as "no such session" is what would render a live session's card
		// Resume. The distinction is the one worker-sessions.py states as "an
		// unreadable store is UNKNOWN, never 0".
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "get heartbeat failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	if !found {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, pkg.ErrSessionHeartbeatNotFound, "heartbeat not found"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"session_id": sessionID},
		)
	}
	view := pkg.NewSessionHeartbeatView(heartbeat, now.Now(), window)
	if err := libhttp.SendJSONResponse(ctx, resp, view, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}
