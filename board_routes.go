// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"io/fs"
	"net/http"

	"github.com/bborbe/errors"

	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
	"github.com/bborbe/attention-controller/pkg/factory"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// registerBoardRoutes mounts the operator board's own surface on the board
// router: the page at `/`, its live channel and its JSON read. The three share
// the same inputs (the store, the provenance resolver, the speak flag, the
// vault directory, the board metrics) and the same ordering constraint — each
// must be registered ahead of the shared `/api/1.0/attention/{itemID}` table —
// so they are registered together, before registerAttentionAPIRoutes. It fails
// only when the embedded frontend build cannot be opened.
func (a *application) registerBoardRoutes(
	ctx context.Context,
	router *mux.Router,
	deps storeDeps,
	provenance pkg.ProvenanceResolver,
	buildIdentity buildidentity.Identity,
) error {
	store, notifier, boardMetrics := deps.store, deps.notifier, deps.boardMetrics
	// The attention page sits at / rather than under /api/1.0/ because it
	// renders HTML for a human rather than JSON for an API client — it is
	// the store's operator-facing surface, not a business endpoint. It is
	// read-only, so GET and HEAD are the only methods routed here; without
	// .Methods, gorilla mux would route POST and DELETE to it as well.
	//
	// The document is the embedded React build (frontend/dist, carried by
	// assets.go). The client fetches the JSON API and subscribes to the SSE
	// stream; the binary serves only the static bundle and the build-identity
	// footer the deploy gate reads.
	dist, distErr := fs.Sub(frontendDist, "frontend/dist")
	if distErr != nil {
		return errors.Wrap(ctx, distErr, "open embedded frontend dist failed")
	}
	// ⚠️ The page is mounted at BOTH the exact root and the /assets/ prefix.
	// The React build loads a hashed JS/CSS bundle from /assets/, and an exact
	// root route leaves those unmatched, so gorilla mux 404s them and the page
	// renders an empty #root. A PathPrefix("/") registered here would instead shadow every
	// API route registered below it, so the prefix is narrowed to the asset tree
	// the build actually emits.
	reactPage := factory.CreateAttentionReactPageHandler(dist, buildIdentity, boardMetrics)
	router.PathPrefix("/assets/").
		Methods(http.MethodGet, http.MethodHead).
		Handler(reactPage)
	router.Path("/").
		Methods(http.MethodGet, http.MethodHead).
		Handler(reactPage)

	// The board's live channel. It is registered here, ahead of the
	// `/api/1.0/attention/{itemID}` route below, because gorilla mux matches
	// in registration order: registered after it, this path would resolve as
	// an item whose id is literally `stream`.
	//
	// ⚠️ Wrapped so each stream ends when this process's context does:
	// Shutdown never cancels a request context, so an open stream would
	// otherwise outlive the graceful drain and hold SIGTERM up.
	router.Path("/api/1.0/attention/stream").
		Methods(http.MethodGet).
		Handler(handler.NewShutdownAwareHandler(ctx.Done(), factory.CreateAttentionStreamHandler(
			store,
			notifier,
			provenance,
			a.TTSURL != "",
			a.VaultDir,
			boardMetrics,
		)))

	// The board page's own read: open items plus the answered records it
	// renders dimmed, each with its render data (jump target, navigation line,
	// uncovered marker, timeline). Registered here, before the shared table's
	// `/{itemID}` routes so "board" is never read as an id, and ⚠️ on the
	// board's router only, like the stream: it resolves host-local provenance
	// (panes, the session registry, the vault), which the cluster-reachable
	// bearer listener built from registerAttentionAPIRoutes does not carry.
	router.Path("/api/1.0/attention/board").
		Methods(http.MethodGet).
		Handler(factory.CreateAttentionBoardReadHandler(
			store,
			provenance,
			a.TTSURL != "",
			a.VaultDir,
		))
	return nil
}
