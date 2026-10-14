// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
)

// NewSessionLedgerListHandler creates the handler that answers the whole
// spawn ledger at once.
//
// ⚠️ **This is the endpoint that replaces the frozen roster.** The defect it
// closes is not a missing row and not a colliding key: the supervisor builds
// its roster ONCE, at process import, from the ledger gated on liveness at that
// instant, and its only later mutation can DELETE but never INSERT — so a
// session that was not live when the server started stays invisible to that
// process for its whole life, however live it becomes. A read here happens at
// REQUEST time and therefore cannot freeze, which is the property a boot-time
// adoption block cannot have. (Measured 2026-10-13: 4 of 35 live-and-ledgered
// sessions were absent from a live roster, this session's own id among them.)
//
// ⚠️ It returns EVERY row, with no liveness filter and no staleness window —
// unlike the heartbeat list beside it, which carries a window because its rows
// expire. A ledger row is permanent by contract: 1480 of the legacy rows still
// say `running` for workers long dead, and filtering on that would silently
// drop most of the fleet. The caller joins liveness from the heartbeat store.
func NewSessionLedgerListHandler(store pkg.SessionLedgerStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, _ *http.Request) error {
				return handleSessionLedgerList(ctx, resp, store)
			},
		),
	)
}

// ⚠️ It takes no `*http.Request`: the whole ledger is the answer, so there is
// nothing to read off the request — no path variable and no query. The
// parameter is omitted rather than accepted-and-ignored, which is what the
// linter refuses and what would otherwise invite a filter to be added here
// without a criterion for it.
func handleSessionLedgerList(
	ctx context.Context,
	resp http.ResponseWriter,
	store pkg.SessionLedgerStore,
) error {
	entries, err := store.List(ctx)
	if err != nil {
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "list session ledger entries failed"),
			libhttp.ErrorCodeInternal,
			http.StatusInternalServerError,
		)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, entries, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}
