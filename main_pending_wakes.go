// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"net/http"

	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/factory"
)

// registerPendingWakesRoute wires POST /api/1.0/pending-wakes/drain on the
// board router.
//
// ⚠️ Board-only, for the same reason registerWhoNeedsMeRoute is: the record is
// written by the instance that types into a manager's pane, which is this one.
// The cluster listener serves the read API and carries no pane carrier, so it
// never queues a wake and has nothing to drain.
//
// It lives in its own file rather than main.go because that file sits at the
// repository's 2000-line ceiling, and its caller sits inside
// registerWhoNeedsMeRoute because createHTTPServer is at its own funlen limit.
func (a *application) registerPendingWakesRoute(
	router *mux.Router,
	store pkg.AttentionStore,
) {
	router.Path("/api/1.0/pending-wakes/drain").
		Methods(http.MethodPost).
		Handler(factory.CreatePendingWakesHandler(store))
}
