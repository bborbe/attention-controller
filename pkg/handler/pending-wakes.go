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
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg"
)

// pendingWakeItemIDs renders a drained set for the log line, in order, so two
// lines for the same batch compare directly.
func pendingWakeItemIDs(items pkg.Items) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ItemID.String())
	}
	return ids
}

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
				// ⚠️ `source=store` is the field that makes a drain attributable
				// to THIS path. The pre-existing `wake_drained` line is emitted by
				// the pass-boundary retry and proves nothing on its own — a build
				// without the store record produces the same pair whenever a busy
				// composer frees inside the batch window.
				if len(items) > 0 {
					glog.V(2).Infof(
						"job=manager-wake action=wake_drained source=store manager=%s items=%v",
						session, pendingWakeItemIDs(items),
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
