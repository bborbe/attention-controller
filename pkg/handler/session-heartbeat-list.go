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

	"github.com/bborbe/attention-controller/pkg"
)

// NewSessionHeartbeatListHandler creates the handler that answers every
// session's liveness at once.
//
// ⚠️ It returns STALE rows too, each carrying its own `live` flag, rather than
// filtering to the fresh ones. A caller that needs a count of live sessions
// filters on `live`; a caller that needs to know a session recently died needs
// the stale row to still be there. Filtering here would make the second
// question unanswerable from the same endpoint, and the store keeps no history
// to answer it any other way.
func NewSessionHeartbeatListHandler(
	store pkg.SessionHeartbeatStore,
	now libtime.CurrentDateTimeGetter,
	window libtime.Duration,
) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleSessionHeartbeatList(ctx, resp, store, now, window)
			},
		),
	)
}

func handleSessionHeartbeatList(
	ctx context.Context,
	resp http.ResponseWriter,
	store pkg.SessionHeartbeatStore,
	now libtime.CurrentDateTimeGetter,
	window libtime.Duration,
) error {
	heartbeats, err := store.List(ctx)
	if err != nil {
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "list heartbeats failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	views := pkg.NewSessionHeartbeatViews(heartbeats, now.Now(), window)
	if err := libhttp.SendJSONResponse(ctx, resp, views, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}
