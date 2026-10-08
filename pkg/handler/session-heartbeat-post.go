// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
)

// NewSessionHeartbeatPostHandler creates the handler a session's MCP timer
// calls to declare that it is still alive.
//
// ⚠️ The store stamps the row's own `at` from its clock; the caller's value is
// ignored. A session that could set its own timestamp could declare itself
// alive indefinitely, which is the liveness check deleted by the party it
// checks — the same rule escalated_at carries.
func NewSessionHeartbeatPostHandler(store pkg.SessionHeartbeatStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleSessionHeartbeatPost(ctx, resp, req, store)
			},
		),
	)
}

func handleSessionHeartbeatPost(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.SessionHeartbeatStore,
) error {
	var heartbeat pkg.SessionHeartbeat
	if err := json.NewDecoder(req.Body).Decode(&heartbeat); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"reason": "request body is not valid JSON"},
		)
	}
	// Validated BEFORE the store is touched, so a malformed declaration is
	// rejected from the push response rather than stored and found later —
	// the same split validatePushRequest makes.
	if err := heartbeat.Validate(ctx); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "validate heartbeat failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"session_id": heartbeat.SessionID},
		)
	}
	if err := store.Post(ctx, heartbeat); err != nil {
		// ⚠️ The rejection reason decides the status, and the two must not be
		// collapsed. A malformed id is the CALLER's error and answers 400; a
		// disk-full, permission-denied or rename failure is the SERVER's and
		// answers 500. Reporting the second as 400 tells an operator to fix a
		// request that was perfectly well formed — the same conflation
		// session-heartbeat-get.go splits on the read side.
		if errors.Is(err, pkg.ErrInvalidSessionID) {
			return libhttp.WrapWithDetails(
				errors.Wrap(ctx, err, "invalid session id"),
				libhttp.ErrorCodeValidation,
				http.StatusBadRequest,
				map[string]any{"session_id": heartbeat.SessionID},
			)
		}
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "post heartbeat failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	// Read back rather than echoing the request: the stored row carries the
	// store's own stamp, and a caller that saw its own `at` echoed would have
	// no way to tell whether the stamp was applied.
	stored, found, err := store.Get(ctx, heartbeat.SessionID)
	if err != nil {
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "read back heartbeat failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	// ⚠️ `!found` is its own case and must NOT share the branch above. There is
	// no error to wrap here — `errors.Wrap` returns nil for a nil error, so
	// folding the two together would hand a nil error to the response builder.
	// Reaching this needs the row to vanish between the Post and the read-back,
	// which is a genuine server-side inconsistency rather than a client error.
	if !found {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, pkg.ErrSessionHeartbeatNotFound, "read back found no row"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
			map[string]any{"session_id": heartbeat.SessionID},
		)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, stored, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}
