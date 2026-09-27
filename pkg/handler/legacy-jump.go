// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"crypto/subtle"
	"html"
	"net"
	"net/http"
	"strconv"

	"github.com/bborbe/errors"
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg"
)

// NewLegacyJumpHandler creates the pane-addressed jump route the manager layer
// follows: `GET /jump?pane=<N>&t=<token>`.
//
// ⚠️ **This route is why the fold is not a cross-repo change.** `jump-link.py`
// emits pane-addressed links into nine manager-layer surfaces, and it does not
// know about item ids. Serving the same path and query shape from this process
// means those consumers keep working untouched while the Python server is
// retired — one process answers for the card and its jump control, which is the
// whole point of the task.
//
// It answers an HTML page rather than JSON, because its caller is a hyperlink
// the operator follows in a browser. The page is deliberately minimal: what
// happened, and to which pane. The Python original's styled page is not
// reproduced, because a card's rendering chrome is explicitly out of scope for
// this change and a cosmetic copy would be the largest untested part of it.
//
// ⚠️ Three controls, all carried over from the server this replaces, and each
// is load-bearing rather than inherited by habit:
//
//   - **The Host header must be loopback.** A browser that resolves an attacker
//     domain to 127.0.0.1 still sends that domain in Host, so this closes a
//     DNS-rebinding hole the token alone does not.
//   - **The token is required and compared in constant time.** A GET that
//     performs an action is CSRF-able; cross-origin JS cannot read the token,
//     so a blind CSRF fails. ⚠️ The token is never logged — not its value, not
//     its length — and the log records only whether one was present.
//   - **The pane is validated against the LIVE pane list.** The activator
//     re-resolves it before activating, so a stale id is refused rather than
//     handed to the terminal. A pane id is a lease, not an identifier.
//
// ⚠️ It takes no store and no provenance resolver, though the Python server it
// replaces rendered the pane's session title. That render is deliberately not
// reproduced here: it needs a title-to-session join this route does not
// otherwise require, and the operator's ask was a working jump, not a styled
// page. A future change that wants the title adds the dependency it actually
// needs, rather than this signature carrying two unused parameters on the guess
// that it will.
func NewLegacyJumpHandler(
	jumpTokens pkg.JumpTokenReader,
	activator pkg.PaneActivator,
) http.Handler {
	return http.HandlerFunc(
		func(resp http.ResponseWriter, req *http.Request) {
			ctx := req.Context()

			// The route's own path check. Registered on a router this is
			// redundant, but the handler is also served directly in tests, and a
			// handler that answers /jump regardless of the path it was reached by
			// is a handler whose contract is only true by accident of wiring.
			if req.URL.Path != "/jump" {
				writeLegacyJumpPage(
					resp,
					http.StatusNotFound,
					"Not found",
					"Only /jump and /health exist.",
				)
				return
			}

			if err := requireLoopbackHost(ctx, req); err != nil {
				writeLegacyJumpPage(
					resp,
					http.StatusForbidden,
					"Forbidden",
					"Host header is not loopback.",
				)
				return
			}

			if err := requireJumpToken(ctx, req, jumpTokens); err != nil {
				// ⚠️ The failure is logged by shape only. Whether a token was
				// present is a fact about the request; its value is a credential
				// and never reaches the log.
				glog.V(2).
					Infof("legacy jump refused: token_present=%t", req.URL.Query().Get("t") != "")
				writeLegacyJumpPage(
					resp,
					http.StatusForbidden,
					"Forbidden",
					"Missing or invalid token.",
				)
				return
			}

			pane := req.URL.Query().Get("pane")
			if _, err := strconv.Atoi(pane); err != nil {
				writeLegacyJumpPage(
					resp,
					http.StatusBadRequest,
					"Bad request",
					"pane must be an integer.",
				)
				return
			}

			// The activator re-resolves the pane against the live list and
			// activates it. A pane that does not resolve is refused there rather
			// than here, so this route has one definition of "live pane" rather
			// than two that can drift.
			if err := activator.Activate(ctx, pane); err != nil {
				glog.V(2).Infof("legacy jump pane %s failed: %v", pane, err)
				writeLegacyJumpPage(
					resp,
					http.StatusBadGateway,
					"Jump failed",
					"The pane could not be activated — it may have been renumbered by a WezTerm restart.",
				)
				return
			}
			glog.V(2).Infof("legacy jump pane %s: activated", pane)
			writeLegacyJumpPage(
				resp,
				http.StatusOK,
				"Jumped",
				"Terminal focus moved to pane "+html.EscapeString(pane)+".",
			)
		},
	)
}

// requireLoopbackHost rejects a request whose Host header is not loopback.
//
// ⚠️ This is the DNS-rebinding guard, and it is independent of the token: a
// browser that resolves an attacker-controlled domain to 127.0.0.1 sends that
// domain in Host, so the request reaches this port while claiming to be
// somewhere else. Checking Host is cheap and closes a hole the token does not.
//
// ⚠️ The port is stripped with net.SplitHostPort, and a bare host is NOT an
// error here: the header is frequently `localhost` with no port at all, which
// that call reports as a failure. Treating the whole header as the host in that
// case is what makes the two accepted forms — `127.0.0.1` and `127.0.0.1:1337`
// — behave the same.
func requireLoopbackHost(ctx context.Context, req *http.Request) error {
	host, _, err := net.SplitHostPort(req.Host)
	if err != nil {
		host = req.Host
	}
	if host != "127.0.0.1" && host != "localhost" {
		return errors.Errorf(ctx, "host %q is not loopback", host)
	}
	return nil
}

// requireJumpToken checks the request's token against the configured one.
//
// ⚠️ The comparison is constant time. A byte-by-byte comparison leaks the
// token's prefix through timing, which turns an unguessable token into a
// guessable one for an attacker who can measure — and a local attacker can.
func requireJumpToken(
	ctx context.Context,
	req *http.Request,
	jumpTokens pkg.JumpTokenReader,
) error {
	expected, err := jumpTokens.Read(ctx)
	if err != nil {
		// Fail closed: a server that cannot read its own token must refuse every
		// request rather than admit them. The error is wrapped without the path's
		// contents; the reader's own error never carries the token.
		return errors.Wrap(ctx, err, "read jump token failed")
	}
	presented := req.URL.Query().Get("t")
	if presented == "" {
		return errors.New(ctx, "jump token is missing")
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
		return errors.New(ctx, "jump token does not match")
	}
	return nil
}

// writeLegacyJumpPage writes the minimal HTML page this route answers with.
//
// It is plain text with a status line, and nothing else. The route's caller is
// a human following a hyperlink, so a bare status code would leave them looking
// at an empty document; but the Python original's styled page is chrome, and
// chrome is out of scope for this change.
func writeLegacyJumpPage(resp http.ResponseWriter, code int, heading, detail string) {
	resp.Header().Set("Content-Type", "text/html; charset=utf-8")
	resp.WriteHeader(code)
	_, _ = resp.Write(
		[]byte(
			"<!doctype html><html><head><meta charset=\"utf-8\"><title>" +
				html.EscapeString(heading) + "</title></head><body><h1>" +
				html.EscapeString(heading) + "</h1><p>" + html.EscapeString(detail) +
				"</p></body></html>",
		),
	)
}
