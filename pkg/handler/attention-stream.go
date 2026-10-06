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
	"sync"
	"time"

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
//
// vaultDir is the configured vault directory. The vault's own name is derived
// from it by vaultNameFromDir — the *same* function the page handler calls, so
// the two surfaces cannot drift: a row arriving over this channel must render
// identically to the same row on a fresh load, and a name derived differently
// here would put a different link on the same card depending on how it arrived.
// ⚠️ It is derived once at construction rather than per push: this handler
// outlives any one request, and the directory cannot change under it. An empty
// vaultDir yields an empty name, so a host with no vault renders no task link on
// either surface.
//
// metrics is injected rather than reached for, so a spec can build the counters
// on its own registry and read them back — and so the running service registers
// them on the one registry /metrics already serves. It is handed to the shared
// renderer and not held by the handler, because the renderer is the only place
// a render happens.
func NewAttentionStreamHandler(
	store pkg.AttentionStore,
	notifier pkg.AttentionChangeNotifier,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	vaultDir string,
	metrics pkg.Metrics,
) http.Handler {
	// The same template the page parses, so `attention-row` renders from one
	// definition rather than from a copy kept in step by hand. Parsed through
	// the shared constructor rather than here: the funcs the template calls have
	// to be registered on both paths, and a second `Parse` call is a second
	// place for them to drift.
	rows := newAttentionPageTemplate()
	handler := &attentionStreamHandler{
		store:        store,
		notifier:     notifier,
		provenance:   provenance,
		speakEnabled: speakEnabled,
		vaultName:    vaultNameFromDir(vaultDir),
		rows:         rows,
	}
	// ⚠️ One renderer per handler, and the handler is built once per process
	// (`factory.CreateAttentionStreamHandler` is called from
	// `createHTTPServer`), so every stream in the process shares this one. That
	// is the whole point: `render` takes no client input, so building it per
	// connection would recompute an identical result once per client.
	handler.board = &boardRenderer{render: handler.render, metrics: metrics}
	return handler
}

type attentionStreamHandler struct {
	store        pkg.AttentionStore
	notifier     pkg.AttentionChangeNotifier
	provenance   pkg.ProvenanceResolver
	speakEnabled bool
	vaultName    string
	rows         *template.Template
	board        *boardRenderer
}

// boardSnapshot is one rendered board, shared by every stream that receives it.
type boardSnapshot struct {
	// seq orders snapshots within one boardRenderer: it rises by one per
	// render, so a stream can tell a snapshot it has already applied from one
	// it has not, and a diff can be skipped outright when they are the same.
	seq uint64
	// rendered is each item's row as HTML, keyed by item id. ⚠️ Never written
	// after publication, which is what makes it safe for every stream to read
	// concurrently — the same property `provenance`'s hostState relies on.
	rendered map[string]string
}

// boardRefresh is one render in flight. The stream that starts it fills it in
// and closes done; every stream arriving while it runs waits on done and reads
// the outcome rather than starting a second render of the same change.
type boardRefresh struct {
	done     chan struct{}
	snapshot *boardSnapshot
	err      error
}

// wait blocks until this render finishes or the caller goes away. A caller that
// gives up returns its own error and never reads the refresh's outcome, which is
// what keeps that read free of a data race with the goroutine still filling it
// in.
func (r *boardRefresh) wait(ctx context.Context) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx, ctx.Err(), "wait for board render failed")
	}
}

// boardRenderer renders the board once per store change and shares the result
// with every connected stream.
//
// ⚠️ This is the fix for a cost that scaled with clients rather than with
// changes. `render` takes no client input — it reads the store, resolves
// provenance and executes the row template — so with eight streams attached
// every change used to decode ~2,563 items and execute ~2,548 templates eight
// times over, producing eight identical maps. Here the first wake renders and
// the rest are served that result.
//
// The generation is what makes it exact. Every subscriber woken by one write
// reads the same notifier generation, so the renderer can answer "has this
// change already been rendered for?" rather than merely "is the cache warm?" —
// and a wake carrying a later generation still renders, because a read taken
// before that write cannot cover it.
//
// It reports what it did through the injected counters: every completed render
// moves attention_board_renders_total by one and adds the rows it produced to
// attention_board_rows_rendered_total, while a render that returned an error
// reports nothing at all.
type boardRenderer struct {
	render func(ctx context.Context) (map[string]string, error)
	// metrics is the counters the shared render path reports through; held by
	// the renderer and not the handler, so the increment cannot be reached from
	// a per-client path.
	metrics pkg.Metrics

	mu                 sync.Mutex
	seq                uint64
	current            *boardSnapshot
	renderedGeneration uint64
	refreshing         *boardRefresh
}

// snapshot returns a board rendered at or after the given notifier generation,
// rendering one only if no snapshot that new is already held.
//
// A caller that has just been woken passes the generation it read after waking;
// a new connection passes the notifier's current one. Both mean the same thing
// — "give me a board at least as new as this" — and a snapshot older than the
// request is never returned, because a caller diffing against a stale board
// would re-send rows its client already has.
//
// ⚠️ The render runs under context.WithoutCancel — see run. A stream that
// disconnects while its wake happens to be the one rendering must not be able
// to cancel a render the other seven are waiting on.
func (b *boardRenderer) snapshot(ctx context.Context, generation uint64) (*boardSnapshot, error) {
	for {
		b.mu.Lock()
		if b.current != nil && b.renderedGeneration >= generation {
			current := b.current
			b.mu.Unlock()
			return current, nil
		}
		// Somebody is already rendering. Wait for them rather than duplicating
		// the work — this is the case that turns eight renders per change into
		// one, and it is the common one: a write wakes every subscriber at
		// once.
		if refresh := b.refreshing; refresh != nil {
			b.mu.Unlock()
			if err := refresh.wait(ctx); err != nil {
				return nil, err
			}
			if refresh.err != nil {
				return nil, refresh.err
			}
			// A change can land while a render is in flight, so the snapshot
			// that just arrived may still predate this generation. Re-check
			// rather than assume it covers us.
			continue
		}
		refresh := &boardRefresh{done: make(chan struct{})}
		b.refreshing = refresh
		b.mu.Unlock()

		b.run(ctx, refresh, generation)
		if refresh.err != nil {
			return nil, refresh.err
		}
		return refresh.snapshot, nil
	}
}

// run performs one render and publishes it, then releases every waiter.
func (b *boardRenderer) run(ctx context.Context, refresh *boardRefresh, generation uint64) {
	// ⚠️ WithoutCancel, and it is load bearing rather than defensive. The
	// render is shared, so the client whose wake triggered it must not be able
	// to cancel it for the streams waiting on the same result: a board tab
	// closing mid-render would otherwise fail the render for every
	// `answered-watch.py` behind it. The operations it wraps are bounded on
	// their own — `ReadBoard` is a local BoltDB read and the one subprocess
	// `Resolve` reaches for is bounded by `paneListingTimeout` — so removing
	// the caller's deadline does not make an unbounded call unbounded.
	rendered, err := b.render(context.WithoutCancel(ctx))

	b.mu.Lock()
	defer b.mu.Unlock()
	refresh.err = err
	if err == nil {
		b.metrics.BoardRendersTotalCounterInc()
		b.metrics.BoardRowsRenderedTotalCounterAdd(len(rendered))
		b.seq++
		b.current = &boardSnapshot{seq: b.seq, rendered: rendered}
		// ⚠️ Stamped with the generation this render was FOR, never with the
		// notifier's current one. A read taken for generation 5 may not cover
		// generation 7, and stamping 7 would mark that change as rendered and
		// strand it until the next write.
		b.renderedGeneration = generation
		refresh.snapshot = b.current
	}
	b.refreshing = nil
	close(refresh.done)
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

	// ⚠️ libhttp's NewServer sets a 30-second WriteTimeout by default, and a write
	// deadline is fatal to a stream: once it passes, the connection is killed
	// mid-response, the browser reports ERR_INCOMPLETE_CHUNKED_ENCODING, and
	// EventSource silently reconnects. The board would look like it worked while
	// actually dropping and re-establishing the channel every 30 seconds — and it
	// would pass a restart-and-reconnect criterion for the wrong reason, because
	// reconnection was happening constantly anyway. Measured 2026-09-26: two
	// ERR_INCOMPLETE_CHUNKED_ENCODING on this route within a minute of a page
	// load, with nothing in the service log, because the deadline fires in the
	// net/http layer rather than in this handler.
	//
	// Cleared for THIS response rather than by raising the server's WriteTimeout,
	// which is shared by every route and would remove a real protection from the
	// handlers that do finish.
	if err := http.NewResponseController(resp).SetWriteDeadline(time.Time{}); err != nil {
		glog.V(2).Infof("stream write deadline not cleared: %v", err)
	}

	// Subscribe and baseline BEFORE the headers go out, so a client that has
	// received a response is guaranteed to be both attached and baselined.
	// Flushing first would leave a window in which a write is neither in the
	// baseline nor delivered — the client would be attached to a stream that had
	// already decided the change was not a change.
	//
	// ⚠️ The baseline is the SHARED board, not a render of this client's own,
	// so a connection made while the board is warm costs no render at all — and
	// that snapshot comes from the same renderer the page reads through, which
	// is what keeps a row arriving here identical to the same row on a fresh
	// load.
	//
	// ⚠️ It is asked for at the notifier's CURRENT generation, not at zero.
	// Zero would return whatever the board last held, which can predate the page
	// this client is looking at — and a baseline older than the page makes the
	// first diff re-send rows the page already drew, replacing nodes that did
	// not change, which is the one thing the diff exists to prevent. Asking at
	// the current generation keeps the pre-shared behaviour exactly: the
	// baseline is the store as of this moment, and it costs a render only when
	// the board is genuinely behind, which is precisely when one is owed.
	//
	// The baseline is what the page already rendered, so the first wake-up
	// reports a difference rather than the whole board. A change landing between
	// the page's own render and this subscribe is still missed, and is corrected
	// by the next one.
	rendered, err := a.board.snapshot(ctx, a.notifier.Generation())
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
			// The generation is read AFTER waking, so it names the write this
			// wake is for — or a later one, if another landed while this stream
			// was busy. Either way the renderer is asked for a board at least
			// that new, and a wake whose change another stream has already
			// rendered for costs no render here.
			next, err := a.board.snapshot(ctx, a.notifier.Generation())
			if err != nil {
				glog.V(2).Infof("stream push failed: %v", err)
				return
			}
			if err := a.push(ctx, resp, flusher, rendered, next); err != nil {
				glog.V(2).Infof("stream push failed: %v", err)
				return
			}
			rendered = next
		}
	}
}

// push sends what differs from the last board this stream applied, and adopts
// the new one. It sends the difference rather than the whole board so a row the
// operator is typing into is never replaced underneath them.
//
// A row is sent when its rendered HTML changed, which is a stricter test than
// its fields changing and costs nothing extra: the render is already in hand,
// and comparing the output is what the page actually shows.
//
// ⚠️ The diff stays per-client even though the render is now shared, and it has
// to. Each stream's baseline is the last board IT sent, so two clients that
// joined at different moments must be told different things about one change —
// sharing the render is what removes the repeated decoding and templating, not
// the comparison, which is a map walk over the same map every stream already
// holds a reference to.
func (a *attentionStreamHandler) push(
	ctx context.Context,
	resp http.ResponseWriter,
	flusher http.Flusher,
	previous *boardSnapshot,
	next *boardSnapshot,
) error {
	// The common case by design: a write wakes every stream, one of them
	// renders, and the rest are handed the very snapshot they already applied.
	// Nothing changed for this client, so nothing is sent.
	if next.seq == previous.seq {
		return nil
	}
	for itemID, html := range next.rendered {
		if prior, seen := previous.rendered[itemID]; seen && prior == html {
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
	for itemID := range previous.rendered {
		if _, still := next.rendered[itemID]; still {
			continue
		}
		if err := writeEvent(ctx, resp, flusher, attentionStreamEvent{
			Type:   "remove",
			ItemID: itemID,
		}); err != nil {
			return errors.Wrapf(ctx, err, "write remove for %s failed", itemID)
		}
	}
	return nil
}

// render reads the store and returns each item's row as rendered HTML, keyed by
// item id. It resolves provenance and the jump token exactly as the page does,
// so a row arriving over the stream is identical to the same row on a fresh
// load.
//
// ⚠️ ReadBoard, not Read — the same reader the page uses, and for the same
// reason. Read returns open items only, so an item that was answered left this
// map and the channel sent a removal for it: the board dropped the row at the
// moment the dimmed record became worth reading, and the record reappeared only
// on the next load. That contradicted § The dimmed record card, which has the
// card appear when the item is answered and leave when it reaches closed. The
// two readers have to agree, because the stream's whole claim is that a row it
// sends is identical to the same row on a fresh load — with Read here and
// ReadBoard on the page, that claim was false for every answered item.
func (a *attentionStreamHandler) render(ctx context.Context) (map[string]string, error) {
	items, err := a.store.ReadBoard(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read failed")
	}
	provenances := a.provenance.Resolve(ctx, items)
	// ⚠️ No token is read here either: the stream's rows must render exactly as
	// the page's do, and the page's Jump button is now gated on the pane alone.
	rendered := make(map[string]string, len(items))
	for _, item := range items {
		row := newAttentionPageRow(
			item,
			provenances[item.ItemID],
			a.speakEnabled,
			a.vaultName,
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
