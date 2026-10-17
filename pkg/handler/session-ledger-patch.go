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
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewSessionLedgerPatchHandler creates the handler a supervisor calls to add
// fields to a row it did not create.
//
// ⚠️ **It is the route that closes the single-writer gap, and it is deliberately
// NOT a relaxed `Post`.** `Post` validates every required field because it writes a
// whole row from scratch; a patch names only what changes, so requiring the spawn
// fields here would make a stop unrecordable — a caller adopting a row has never
// held its `spawned_at`. What replaces that validation is the MERGED row's own
// validation inside the store, so a patch still cannot leave the row in a state
// `Post` would have refused.
//
// ⚠️ **There is no caller identity on this route, and the merge is NOT gated on
// one.** The ledger routes are wired only into the loopback listener, and the
// contract here is that any supervisor may add a field to any row — that is the
// point of the change. A caller that needs to attribute a write reads the row, not
// the request.
func NewSessionLedgerPatchHandler(store pkg.SessionLedgerStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleSessionLedgerPatch(ctx, resp, req, store)
			},
		),
	)
}

func handleSessionLedgerPatch(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.SessionLedgerStore,
) error {
	sessionID := mux.Vars(req)["sessionID"]
	var patch pkg.SessionLedgerPatch
	if err := json.NewDecoder(req.Body).Decode(&patch); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"reason": "request body is not a JSON object"},
		)
	}
	entry, found, err := store.Patch(ctx, sessionID, patch)
	if err != nil {
		// ⚠️ A malformed patch is the CALLER's error and must not share the 500
		// branch below. The three refusals it covers — identity in the body, an
		// unknown field, a merge that would invalidate the row — are all things the
		// caller can fix, and answering 500 would send them looking at the store.
		if errors.Is(err, pkg.ErrSessionLedgerPatchInvalid) {
			return libhttp.WrapWithDetails(
				errors.Wrap(ctx, err, "patch session ledger entry failed"),
				libhttp.ErrorCodeValidation,
				http.StatusBadRequest,
				map[string]any{"session_id": sessionID},
			)
		}
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "patch session ledger entry failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	// ⚠️ `!found` is its own case and must NOT share the branch above — the same
	// rule the POST handler states, and the reason the store reports a bool rather
	// than folding absence into its error. A patch against a row that was never
	// spawned is `absent` (404); reporting it as a store outage would collapse the
	// one distinction this store exists to keep.
	if !found {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, pkg.ErrSessionLedgerEntryNotFound, "session ledger entry not found"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"session_id": sessionID},
		)
	}
	// Read back rather than echoing the patch: the stored row is what a later
	// reader joins against, and a caller that saw its own payload echoed would have
	// no way to tell whether the merge landed — the same reason POST reads back.
	if err := libhttp.SendJSONResponse(ctx, resp, entry, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}
