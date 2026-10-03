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
	"strings"

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
				writeLegacyJumpError(
					resp,
					http.StatusNotFound,
					"warn",
					"Not found",
					"Only /jump and /health exist.",
				)
				return
			}

			if err := requireLoopbackHost(ctx, req); err != nil {
				writeLegacyJumpError(
					resp,
					http.StatusForbidden,
					"err",
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
				writeLegacyJumpError(
					resp,
					http.StatusForbidden,
					"err",
					"Forbidden",
					"Missing or invalid token.",
				)
				return
			}

			pane := req.URL.Query().Get("pane")
			if _, err := strconv.Atoi(pane); err != nil {
				writeLegacyJumpError(
					resp,
					http.StatusBadRequest,
					"warn",
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
				writeLegacyJumpError(
					resp,
					http.StatusBadGateway,
					"err",
					"Jump failed",
					"The pane could not be activated — it may have been renumbered by a WezTerm restart.",
				)
				return
			}
			glog.V(2).Infof("legacy jump pane %s: activated", pane)
			writeLegacyJumpPage(
				resp,
				http.StatusOK,
				"ok",
				"Jumped",
				"Terminal focus moved to the pane below.",
				pane,
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
// ⚠️ **Loopback is decided by net.IP.IsLoopback, not by string comparison.**
// The first version accepted exactly `127.0.0.1` and `localhost`, which refused
// every IPv6 loopback form — `[::1]:1337`, `::1`, and the IPv4-mapped
// `[::ffff:127.0.0.1]:1337`. That is a real gap rather than a theoretical one:
// `-jump-listen` takes any address, so a host configured on `[::1]:1337` would
// have had every jump refused by its own guard. IsLoopback covers the whole
// family — 127.0.0.0/8, ::1, and the mapped forms — without enumerating them.
//
// ⚠️ `localhost` is still accepted by name, because it is a name and not an
// address: ParseIP cannot classify it, and resolving it here would make the
// guard depend on the host's resolver — a DNS-rebinding guard that asks DNS is
// the wrong shape.
//
// ⚠️ The port is stripped with net.SplitHostPort, and a bare host is NOT an
// error here: the header is frequently `localhost` with no port at all, which
// that call reports as a failure. Treating the whole header as the host in that
// case is what makes `127.0.0.1` and `127.0.0.1:1337` behave the same.
func requireLoopbackHost(ctx context.Context, req *http.Request) error {
	// SplitHostPort is IPv6-aware and strips the brackets: `[::1]:1337` yields
	// `::1`, which ParseIP accepts. A bare `[::1]` has no port and fails the
	// split, so the brackets are trimmed on the fallback path.
	host, _, err := net.SplitHostPort(req.Host)
	if err != nil {
		host = strings.TrimSuffix(strings.TrimPrefix(req.Host, "["), "]")
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return errors.Errorf(ctx, "host %q is not loopback", host)
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

// legacyJumpPageCSS is the house style of the local service pages, copied
// from the Python fleet-jump server this route replaced so a stray jump tab
// still reads as part of the same family as the tts and attention pages.
const legacyJumpPageCSS = `
:root {
  color-scheme: dark;
  --bg: #111418;
  --panel: #1a1f26;
  --border: #2a3038;
  --text: #e8edf2;
  --muted: #8b95a3;
  --ok: #38c172;
  --warn: #d08b5b;
  --err: #e55b5b;
}
* { box-sizing: border-box; }
body {
  margin: 0 auto;
  max-width: 760px;
  padding: 24px;
  background: var(--bg);
  color: var(--text);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}
h1 { font-size: 20px; margin: 0 0 4px; }
h1.ok { color: var(--ok); }
h1.warn { color: var(--warn); }
h1.err { color: var(--err); }
.subtitle { color: var(--muted); font-size: 13px; margin: 0 0 20px; }
.card {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 16px;
  margin-bottom: 16px;
}
.label {
  color: var(--muted);
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  margin: 0 0 4px;
}
.value { font-size: 15px; line-height: 1.45; margin: 0; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
`

// writeLegacyJumpError writes a styled refusal page, which names no pane.
func writeLegacyJumpError(resp http.ResponseWriter, code int, tone, heading, detail string) {
	writeLegacyJumpPage(resp, code, tone, heading, detail, "")
}

// writeLegacyJumpPage writes the styled status page this route answers with.
//
// The route's caller is a human following a hyperlink, so the page explains
// itself: tone colours the heading (ok / warn / err) and a non-empty pane
// renders a card naming the pane that was reached. Every value is escaped
// here, once.
func writeLegacyJumpPage(resp http.ResponseWriter, code int, tone, heading, detail, pane string) {
	card := ""
	if pane != "" {
		card = "<div class=\"card\"><p class=\"label\">Pane</p><p class=\"value mono\">" +
			html.EscapeString(pane) + "</p></div>"
	}
	resp.Header().Set("Content-Type", "text/html; charset=utf-8")
	resp.WriteHeader(code)
	_, _ = resp.Write(
		[]byte(
			"<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">" +
				"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
				"<title>" + html.EscapeString(heading) + "</title><style>" + legacyJumpPageCSS +
				"</style></head><body><h1 class=\"" + html.EscapeString(tone) + "\">" +
				html.EscapeString(heading) + "</h1><p class=\"subtitle\">" +
				html.EscapeString(detail) + "</p>" + card + "</body></html>",
		),
	)
}
