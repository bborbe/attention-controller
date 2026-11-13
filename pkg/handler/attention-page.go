// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// NewAttentionPageHandler creates the read-only HTML page a human opens to see
// what currently needs attention.
//
// ⚠️ This handler reverses the contract it was built with, and the reversal is
// deliberate rather than drift. It previously performed **no render-time
// lookup at all**, on the reasoning that ProducerID is a session id, the
// registry deletes its entry when the session exits, and a render-time lookup
// would therefore find nothing and present an unresolvable value as resolved.
// That reasoning is still honoured — an unresolvable value renders absent, and
// a pane is shown only when it validates — but its blanket prohibition could
// not survive the operator's ask, which is precisely for values the store does
// not carry: the store's fifteen-field Item has no host, cwd, tool or pane.
//
// The join is best-effort and the page stays usable without it. Every source is
// read through a resolver that fails soft: a host with no WezTerm, a store with
// no Claude Code state directory, an unreadable registry — each resolves to no
// values for that row, and the page renders exactly what it rendered before.
// That is the honest scoping of this page's standalone claim: it still works
// without Claude Code, but it works *with provenance absent* rather than
// without looking.
//
// ⚠️ The Jump button is gated on the pane alone, and the token gate that used
// to sit here was REMOVED by the jump fold rather than merely dropped. It read
// the jump token per render and suppressed the button when the token was
// unreadable — a precondition that held while the button's endpoint forwarded
// to the Python fleet-jump server, which authenticated with that token. The
// endpoint now performs the jump in-process (NewAttentionJumpHandler) and never
// reads a token, so the gate had become a false one: it would hide a working
// control because of a credential the control no longer touches. The token
// survives on the legacy pane-addressed route, which is the one surface that
// still needs it.
//
// vaultDir is the configured vault directory, and the vault's own *name* is
// derived from it as `filepath.Base(vaultDir)` — the name the Obsidian URL
// addresses, which is the directory's base name rather than anything stored in
// the vault. It is derived once per request rather than once per row, since
// every row on the page shares it, and an empty vaultDir yields an empty name
// so a host with no vault renders no task link. ⚠️ The guard on the empty
// directory is load-bearing: filepath.Base("") is "." rather than "", so
// without it an unconfigured vault would name a vault called `.`.
// buildIdentity is the running binary's own provenance, rendered in the page's
// footer. It is a parameter rather than a call to buildidentity.Read inside the
// handler so the handler stays a pure renderer: the value is read once at
// startup, from the binary, and a handler that re-read it per request would be
// re-deriving a constant while making the page's footer untestable.
//
// metrics is the process-wide instance `main` builds on the registry /metrics serves.
func NewAttentionPageHandler(store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	vaultDir string,
	buildIdentity buildidentity.Identity,
	metrics pkg.Metrics,
) http.Handler {
	// Parsed once at construction rather than per request: the template is built
	// at package init from embedded assets that cannot change at runtime, so a
	// parse failure is a programming error, and template.Must makes it a startup
	// failure rather than a per-request one.
	page := newAttentionPageTemplate()
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				// ⚠️ Per request, before the store read: a failed render still counts.
				metrics.BoardPageRequestsTotalCounterInc()
				// Parsed before the store read: a rejected parameter must cost a
				// 400, not a full board read that is then thrown away.
				producerKind, allKinds, err := boardProducerKindOf(ctx, req)
				if err != nil {
					return libhttp.WrapWithCode(
						err,
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
					)
				}
				// ReadBoard, not Read: the board renders the answered items as
				// dimmed records as well as the open ones. Read stays as it is
				// for the JSON read API, whose consumers act on what they are
				// given and must not be handed an already-answered item.
				items, err := store.ReadBoard(ctx)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "read failed"),
						libhttp.ErrorCodeInternal,
						http.StatusInternalServerError,
					)
				}
				// Resolved for the whole page at once, so the host-wide reads
				// behind it (the pane listing, the session registry) happen once
				// rather than once per row.
				provenances := provenance.Resolve(ctx, items)
				// ⚠️ No token is read here. The Jump button is gated on the
				// resolved pane alone — see the constructor's comment on why the
				// token gate was removed rather than kept.
				//
				// Derived once per request rather than per row: every row on the
				// page addresses the same vault.
				vaultName := vaultNameFromDir(vaultDir)
				rows := make([]attentionPageRow, 0, len(items))
				for _, item := range items {
					rows = append(
						rows,
						newAttentionPageRow(
							item,
							provenances[item.ItemID],
							deliveryReportOf(ctx, store, item.ItemID),
							speakEnabled,
							vaultName,
						),
					)
				}
				// Rendered into a buffer first so a render failure can still
				// produce the standard JSON error body. Writing straight to the
				// response would commit a 200 and a partial document before the
				// error handler had a chance to report anything.
				var body bytes.Buffer
				// Read once per render so the served markup agrees with the URL
				// the operator is looking at. A no-JS client cannot filter at
				// all, so this is not the filter — it is the switch's initial
				// position, and the script applies the same reading.
				//
				// The default is ON: only an explicit `?hide=none` turns the
				// filter off, and absence (or any unrecognised value) leaves it
				// on.
				flagsParam := req.URL.Query().Get(boardFlagsParam)
				hideAnswered := req.URL.Query().Get(boardHideParam) != boardHideNone
				// Read from its own parameter alone: the flag switch's position
				// is not derived from the answered switch's, so `?hide=none`
				// leaves it on. The script applies the same independent reading.
				flagFilter := flagsParam != boardFlagsNone
				shown, hiddenOpen, hiddenAll := filterByProducerKind(
					rows, producerKind, allKinds,
				)
				data := attentionPageData{
					Items:         shown,
					Speak:         speakEnabled,
					HideAnswered:  hideAnswered,
					FlagFilter:    flagFilter,
					AllKinds:      allKinds,
					KindHref:      boardProducerKindHref(req.URL, allKinds),
					HiddenOpen:    hiddenOpen,
					HiddenAll:     hiddenAll,
					BuildIdentity: buildIdentity,
				}
				data.setKindCount(hideAnswered)
				if err := page.Execute(&body, data); err != nil {
					return errors.Wrap(ctx, err, "render page failed")
				}
				resp.Header().Set(
					libhttp.ContentTypeHeaderName,
					libhttp.TextHTML+"; charset=utf-8",
				)
				resp.Header().Set("Cache-Control", "no-store")
				if _, err := resp.Write(body.Bytes()); err != nil {
					return errors.Wrap(ctx, err, "write response failed")
				}
				return nil
			},
		),
	)
}
