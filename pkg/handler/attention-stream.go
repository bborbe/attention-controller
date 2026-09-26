// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/bborbe/errors"
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg"
)

// attentionStreamEvent is what a change is sent as. It carries the rendered row
// rather than the item, so the page swaps a node instead of re-implementing the
// card, its answer controls and its jump button in JavaScript — one renderer,
// not two that drift.
type attentionStreamEvent struct {
	// Type is `upsert` for a row that appeared or changed and `remove` for one
	// that left. A removal carries no HTML.
	Type string `json:"type"`
	// ItemID is the row this event is about, and the key the page matches on.
	ItemID string `json:"item_id"`
	// HTML is the rendered `li`, present on an upsert only.
	HTML string `json:"html,omitempty"`
}

// NewAttentionStreamHandler serves the board's live channel: a
// `text/event-stream` response that pushes a row when the store changes, so an
// open board tracks the store without the operator reloading it.
//
// ⚠️ This handler is deliberately NOT wrapped in libhttp.NewJSONErrorHandler,
// which every other handler here is. That wrapper buffers the whole body and
// writes it once, so a render failure can still produce the standard JSON error
// body — correct for a page, and fatal for a stream, which must Flush() each
// event as it happens. The exception is the point, not an oversight: a wrapped
// stream would deliver nothing until the connection closed.
//
// The channel is server-to-client only. Answers, escalations, closes and speak
// keep going over their existing POST routes, so nothing is ever read from this
// connection.
func NewAttentionStreamHandler(
	store pkg.AttentionStore,
	notifier pkg.AttentionChangeNotifier,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	jumpTokens pkg.JumpTokenReader,
) http.Handler {
	// The same template the page parses, so `attention-row` renders from one
	// definition rather than from a copy kept in step by hand.
	rows := template.Must(template.New("attention-page").Parse(attentionPageTemplate))
	return &attentionStreamHandler{
		store:        store,
		notifier:     notifier,
		provenance:   provenance,
		jumpTokens:   jumpTokens,
		speakEnabled: speakEnabled,
		rows:         rows,
	}
}

type attentionStreamHandler struct {
	store        pkg.AttentionStore
	notifier     pkg.AttentionChangeNotifier
	provenance   pkg.ProvenanceResolver
	jumpTokens   pkg.JumpTokenReader
	speakEnabled bool
	rows         *template.Template
}

// ServeHTTP streams row changes until the client goes away.
func (a *attentionStreamHandler) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	flusher, ok := resp.(http.Flusher)
	if !ok {
		http.Error(resp, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	changes, unsubscribe := a.notifier.Subscribe()
	defer unsubscribe()

	// Subscribe and baseline BEFORE the headers go out, so a client that has
	// received a response is guaranteed to be both attached and baselined.
	// Flushing first would leave a window in which a write is neither in the
	// baseline nor delivered — the client would be attached to a stream that had
	// already decided the change was not a change.
	//
	// The baseline is what the page already rendered, so the first wake-up
	// reports a difference rather than the whole board. A change landing between
	// the page's own render and this subscribe is still missed, and is corrected
	// by the next one.
	rendered, err := a.render(ctx)
	if err != nil {
		glog.V(2).Infof("stream baseline failed: %v", err)
		return
	}

	resp.Header().Set("Content-Type", "text/event-stream")
	resp.Header().Set("Cache-Control", "no-cache")
	resp.Header().Set("Connection", "keep-alive")
	resp.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-changes:
			if !open {
				return
			}
			if err := a.push(ctx, resp, flusher, &rendered); err != nil {
				glog.V(2).Infof("stream push failed: %v", err)
				return
			}
		}
	}
}

// push re-reads the store, sends what differs from the last render, and adopts
// the new state. It sends the difference rather than the whole board so a row
// the operator is typing into is never replaced underneath them.
//
// A row is sent when its rendered HTML changed, which is a stricter test than
// its fields changing and costs nothing extra: the render is already in hand,
// and comparing the output is what the page actually shows.
func (a *attentionStreamHandler) push(
	ctx context.Context,
	resp http.ResponseWriter,
	flusher http.Flusher,
	rendered *map[string]string,
) error {
	next, err := a.render(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "render failed")
	}
	for itemID, html := range next {
		if previous, seen := (*rendered)[itemID]; seen && previous == html {
			continue
		}
		if err := writeEvent(ctx, resp, flusher, attentionStreamEvent{
			Type:   "upsert",
			ItemID: itemID,
			HTML:   html,
		}); err != nil {
			return errors.Wrapf(ctx, err, "write upsert for %s failed", itemID)
		}
	}
	for itemID := range *rendered {
		if _, still := next[itemID]; still {
			continue
		}
		if err := writeEvent(ctx, resp, flusher, attentionStreamEvent{
			Type:   "remove",
			ItemID: itemID,
		}); err != nil {
			return errors.Wrapf(ctx, err, "write remove for %s failed", itemID)
		}
	}
	*rendered = next
	return nil
}

// render reads the store and returns each item's row as rendered HTML, keyed by
// item id. It resolves provenance and the jump token exactly as the page does,
// so a row arriving over the stream is identical to the same row on a fresh
// load.
func (a *attentionStreamHandler) render(ctx context.Context) (map[string]string, error) {
	items, err := a.store.Read(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read failed")
	}
	provenances := a.provenance.Resolve(ctx, items)
	jumpEnabled := false
	if a.jumpTokens != nil {
		if _, err := a.jumpTokens.Read(ctx); err != nil {
			glog.V(3).Infof("jump token unavailable, rendering no jump buttons: %v", err)
		} else {
			jumpEnabled = true
		}
	}
	rendered := make(map[string]string, len(items))
	for _, item := range items {
		row := newAttentionPageRow(
			item,
			provenances[item.ItemID],
			jumpEnabled,
			a.speakEnabled,
		)
		var body bytes.Buffer
		if err := a.rows.ExecuteTemplate(&body, "attention-row", row); err != nil {
			return nil, errors.Wrap(ctx, err, "render row failed")
		}
		rendered[item.ItemID.String()] = body.String()
	}
	return rendered, nil
}

// writeEvent frames one change as a server-sent event and flushes it, so the
// page sees it now rather than when a buffer fills.
//
// No keep-alive comment is sent on an idle channel. It would be the usual
// defence against a proxy closing a quiet connection, but this service is bound
// to the loopback interface with no proxy in front of it, and an idle stream is
// required to be silent — a heartbeat would be a message on a channel whose
// whole claim is that nothing is sent until the store changes.
func writeEvent(
	ctx context.Context,
	resp http.ResponseWriter,
	flusher http.Flusher,
	event attentionStreamEvent,
) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return errors.Wrap(ctx, err, "marshal event failed")
	}
	if _, err := resp.Write([]byte("data: ")); err != nil {
		return errors.Wrap(ctx, err, "write event failed")
	}
	if _, err := resp.Write(payload); err != nil {
		return errors.Wrap(ctx, err, "write event failed")
	}
	if _, err := resp.Write([]byte("\n\n")); err != nil {
		return errors.Wrap(ctx, err, "write event failed")
	}
	flusher.Flush()
	return nil
}
