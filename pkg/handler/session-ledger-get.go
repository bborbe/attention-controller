// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewSessionLedgerGetHandler creates the handler that answers one session's
// ledger row.
//
// ⚠️ **It answers IDENTITY, and it deliberately carries no `live` flag.** The
// heartbeat endpoint beside it returns freshness because freshness is what its
// store is for; this one returns what was spawned, by whom, and where — and a
// reader that needs liveness joins the heartbeat store on `session_id`. Adding
// a `live` field here would put a second, divergent liveness rule in the
// service, which is the split `registry-rehydrate.mjs` names as the reason its
// own two channels are kept apart.
//
// ⚠️ `absent` is HTTP 404 and is NOT the same answer as a row that exists with
// `ended_at` set. A caller deciding whether to resume a worker needs to tell
// "never spawned" from "spawned and ended", and collapsing them would make the
// ledger unable to answer the question it exists for.
func NewSessionLedgerGetHandler(store pkg.SessionLedgerStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleSessionLedgerGet(ctx, resp, req, store)
			},
		),
	)
}

func handleSessionLedgerGet(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.SessionLedgerStore,
) error {
	sessionID := mux.Vars(req)["sessionID"]
	entry, found, err := store.Get(ctx, sessionID)
	if err != nil {
		// An unreadable store is a FAILURE, never `absent` — the same rule the
		// heartbeat get handler states, and the one `worker-sessions.py` gives as
		// "an unreadable store is UNKNOWN, never 0".
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "get session ledger entry failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	if !found {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, pkg.ErrSessionLedgerEntryNotFound, "session ledger entry not found"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"session_id": sessionID},
		)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, entry, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}
