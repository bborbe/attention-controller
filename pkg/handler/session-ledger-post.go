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

// NewSessionLedgerPostHandler creates the handler a supervisor calls to record
// one spawn.
//
// ⚠️ **It is the write path the file ledger is being replaced BY**, so the row
// it accepts is the supervisor's own spawn record rather than a caller's
// summary of one. `spawned_at` is therefore taken from the CALLER here — unlike
// the heartbeat's `at`, which the store stamps — because the instant a worker
// was spawned is a fact the supervisor holds and the store cannot reconstruct:
// a store-stamped time would date every imported row to the migration instead
// of to the spawn.
func NewSessionLedgerPostHandler(store pkg.SessionLedgerStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleSessionLedgerPost(ctx, resp, req, store)
			},
		),
	)
}

func handleSessionLedgerPost(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.SessionLedgerStore,
) error {
	var entry pkg.SessionLedgerEntry
	if err := json.NewDecoder(req.Body).Decode(&entry); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"reason": "request body is not valid JSON"},
		)
	}
	// ⚠️ Validated BEFORE the store is touched, so a row missing a required
	// field is refused from the write response rather than stored and found by
	// whoever reads the ledger next. This is the 400-per-field contract, and it
	// holds for the one-time import too: `Import` validates every row it is
	// handed, so a malformed legacy file fails the migration loudly rather than
	// entering the store as a row nothing can join to.
	if err := entry.Validate(ctx); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "validate session ledger entry failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"session_id": entry.SessionID},
		)
	}
	if err := store.Post(ctx, entry); err != nil {
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "post session ledger entry failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	// Read back rather than echoing the request: the stored row is what a later
	// reader will join against, and a caller that saw its own payload echoed
	// would have no way to tell whether the write landed.
	stored, found, err := store.Get(ctx, entry.SessionID)
	if err != nil {
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "read back session ledger entry failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	// ⚠️ `!found` is its own case and must NOT share the branch above. There is
	// no error to wrap here — `errors.Wrap` returns nil for a nil error, so
	// folding the two together would hand a nil error to the response builder.
	// Reaching this needs the row to vanish between the Post and the read-back,
	// which is a server-side inconsistency rather than a client error.
	if !found {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, pkg.ErrSessionLedgerEntryNotFound, "read back found no row"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
			map[string]any{"session_id": entry.SessionID},
		)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, stored, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}
