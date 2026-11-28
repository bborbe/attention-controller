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

// NewPendingWakesHandler creates an HTTP handler that drains the wakes owed to
// one manager session and returns them.
//
// ⚠️ It is a POST because it is a WRITE: the drain deletes the records it
// returns, in one transaction. A GET would let a retry, a prefetch or a proxy
// consume a wake that the manager never saw.
//
// ⚠️ The drain is the manager's only read path. The store is in-process bbolt,
// so a manager session cannot open the database itself; this endpoint is what
// lets it read the wake the controller queued while its composer was busy.
//
// The response is `{"items": [...]}` — the items as they stand now. An item
// answered since it was queued is dropped, and its record is still deleted, so
// a manager is never asked to handle a gate it has already handled.
func NewPendingWakesHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				session := req.URL.Query().Get("session")
				if session == "" {
					return libhttp.WrapWithCode(
						errors.New(ctx, "session is empty"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
					)
				}
				items, err := store.DrainPendingWakes(ctx, session)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "drain pending wakes failed"),
						libhttp.ErrorCodeInternal,
						http.StatusInternalServerError,
					)
				}
				resp.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(resp).Encode(
					map[string]any{"items": items},
				); err != nil {
					return errors.Wrap(ctx, err, "encode response failed")
				}
				return nil
			},
		),
	)
}
