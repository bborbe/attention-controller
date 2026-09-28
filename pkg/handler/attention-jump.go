// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewAttentionJumpHandler creates the endpoint the board's Jump button calls to
// hand an item back to the session that raised it.
//
// ⚠️ It performs the jump **in-process** and answers 204 with no body. The
// capability is the injected PaneActivator, so the board owns the whole path:
// it used to reach a Python fleet-jump server on another port, which owned
// neither the process nor its failure modes. A card and its jump control are
// now served by one process.
//
// ⚠️ **The handler no longer reads the jump token, and that is the fold's most
// easily-missed consequence.** The token existed to authenticate a call to the
// fleet-jump server; with the jump in-process there is no such call, so reading
// it here would be a credential read that gates nothing. The token survives on
// the legacy pane-addressed route (see NewLegacyJumpHandler), which is the one
// surface that still needs it. ⚠️ This is why the board no longer reports 503
// on an unreadable token file: the button no longer depends on one.
//
// The pane is re-resolved here rather than carried in the request, because a
// pane id is recycled across tab moves and WezTerm restarts: a request holding
// one would keep pointing at a pane that has since become another session's,
// which is a wrong answer wearing the appearance of a resolved one. An item
// whose pane does not resolve jumps nowhere and says so.
//
// The 204 is what keeps the browser on the board: it is not a navigation, so
// the page's fetch() resolves in place and the operator's screen stays put.
// That was the operator's own ask — "so we dont switch the screen".
func NewAttentionJumpHandler(
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	activator pkg.PaneActivator,
) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				if err := requireSameOrigin(ctx, req); err != nil {
					return err
				}
				itemID := pkg.ItemID(mux.Vars(req)["itemID"])
				if itemID == "" {
					return libhttp.WrapWithCode(
						errors.New(ctx, "itemID is empty"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
					)
				}
				item, err := store.Get(ctx, itemID)
				if err != nil {
					return wrapTransitionError(ctx, err, itemID)
				}
				pane := provenance.Resolve(ctx, pkg.Items{*item})[item.ItemID].Pane
				if pane == "" {
					return libhttp.WrapWithCode(
						errors.New(ctx, "item has no resolvable pane"),
						libhttp.ErrorCodeNotFound,
						http.StatusNotFound,
					)
				}
				// ⚠️ The jump is performed here rather than handed to the browser
				// as a redirect. A redirect would navigate the board out of view —
				// the exact behaviour the operator asked to remove. The response
				// carries no body, so there is nowhere for anything to land either.
				if err := activator.Activate(ctx, pane); err != nil {
					// The error is logged; the caller gets the failure.
					glog.V(2).Infof("jump pane %s failed: %v", pane, err)
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "jump failed"),
						libhttp.ErrorCodeInternal,
						http.StatusBadGateway,
					)
				}
				resp.WriteHeader(http.StatusNoContent)
				return nil
			},
		),
	)
}

// requireSameOrigin rejects a cross-site request to the jump route.
//
// ⚠️ The route is a GET that performs an action, so any page the operator
// visits can carry `<img src=".../jump/<itemID>">` and move their terminal. The
// item id is 128-bit crypto-random, which makes guessing impractical rather
// than impossible, and a posture that depends on that is one refactor away from
// being no posture at all. The route therefore checks the request itself.
//
// ⚠️ The gate matters more after the fold, not less: the token used to be a
// second control behind this one, and the board's path no longer reads it. This
// check is now the board route's only cross-site defence, which is why it is
// stated here rather than left implicit.
//
// ⚠️ Sec-Fetch-Site is the gate, not Origin. A same-origin <a href> GET
// navigation sends Sec-Fetch-Site: same-origin but no Origin header at all —
// Origin accompanies CORS requests and non-GET/HEAD same-origin requests — so
// an Origin-only check would reject the very button click this route exists to
// serve. `none` is allowed because it is a direct address-bar or bookmark
// entry, which is not a cross-site vector.
func requireSameOrigin(ctx context.Context, req *http.Request) error {
	switch req.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return nil
	default:
		return libhttp.WrapWithCode(
			errors.New(ctx, "cross-site jump refused"),
			libhttp.ErrorCodeForbidden,
			http.StatusForbidden,
		)
	}
}
