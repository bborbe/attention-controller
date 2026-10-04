// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bufio"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The live channel is the one handler here that does not return: it holds the
// connection open and writes as the store changes. These specs therefore drive
// it through a real httptest server rather than a recorder — the handler's
// lifetime belongs to the server, which is also what makes the flushing real
// rather than asserted, and it keeps a raw goroutine out of the test.
//
// The absence assertions are ordered rather than windowed. "Nothing is sent
// while the store is quiet" cannot be shown by waiting and observing silence —
// that is the shape that passes on a handler which never sends at all. So the
// specs perform the quiet action FIRST and then a real write, and assert the
// FIRST event is the write's: had the quiet action emitted anything, it would
// have been read first.
var _ = Describe("AttentionStreamHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var notifier pkg.AttentionChangeNotifier
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var provenance *mocks.ProvenanceResolver
	var server *httptest.Server
	// vaultDir ends in a known name so the expected task link is a hand-written
	// literal. The directory need not exist: the handler only reads its base name.
	var vaultDir string

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: Read prunes
		// open items whose producer is not live, so a faked store would let the
		// stream render fixtures the production read path would have dropped.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).Should(BeNil())

		// The mock defaults to false, which would prune every fixture during
		// Read and make the positive assertions fail for the wrong reason.
		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		notifier = pkg.NewAttentionChangeNotifier()
		store = pkg.NewNotifyingAttentionStore(
			pkg.NewAttentionStore(
				db,
				pkg.NewItemIDGenerator(),
				sessionLivenessChecker,
				libtime.NewCurrentDateTime(),
				libtime.Duration(15*60*1e9),
			),
			notifier,
		)

		provenance = &mocks.ProvenanceResolver{}
		vaultDir = filepath.Join(GinkgoT().TempDir(), "Personal")
		// ⚠️ A private registry, never the process-wide default registry:
		// MustRegister panics on a second registration of the same collector, so
		// a spec on the default registry would panic as soon as another spec
		// registered the same names.
		registry := prometheus.NewRegistry()
		metrics := boardmetrics.NewMetrics(registry)
		server = httptest.NewServer(handler.NewAttentionStreamHandler(
			store,
			notifier,
			provenance,
			false,
			vaultDir,
			metrics,
		))
	})

	AfterEach(func() {
		server.Close()
	})

	// connect opens the stream and returns a reader positioned at the first
	// event, plus a cancel that ends the connection. The request carries a
	// deadline so a spec that waits for an event which never arrives fails in
	// seconds rather than at the suite timeout.
	connect := func() (*bufio.Reader, context.CancelFunc) {
		streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL, nil)
		Expect(err).Should(BeNil())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).Should(BeNil())
		Expect(resp.StatusCode).Should(Equal(http.StatusOK))
		Expect(resp.Header.Get("Content-Type")).Should(Equal("text/event-stream"))
		return bufio.NewReader(resp.Body), cancel
	}

	push := func(payload string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "producer-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-a"),
			DedupKey:        pkg.DedupKey(payload),
			InterruptClass:  "approve",
			Payload:         pkg.Payload(payload),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).Should(BeNil())
		return item
	}

	readEvent := func(reader *bufio.Reader) map[string]string {
		var payload string
		for {
			line, err := reader.ReadString('\n')
			Expect(err).Should(BeNil())
			line = strings.TrimRight(line, "\n")
			if line == "" {
				break
			}
			if after, found := strings.CutPrefix(line, "data: "); found {
				payload = after
			}
		}
		var event map[string]string
		Expect(json.Unmarshal([]byte(payload), &event)).Should(BeNil())
		return event
	}

	It("sends a pushed row over the open stream", func() {
		reader, cancel := connect()
		defer cancel()

		item := push("deploy prod?")

		event := readEvent(reader)
		Expect(event["type"]).Should(Equal("upsert"))
		Expect(event["item_id"]).Should(Equal(item.ItemID.String()))
		Expect(event["html"]).Should(ContainSubstring("deploy prod?"))
	})

	// The task link must arrive over the live channel as well as on a fresh load,
	// and this is the case that catches the wiring rather than the rendering: a
	// stream handler built with an empty vault name compiles, passes every page
	// spec above and renders a link-less row — the same card differing by how it
	// arrived, which is the drift this handler exists to prevent.
	It("sends the row's vault task link over the stream", func() {
		// Resolved from the items the handler actually asks about rather than
		// keyed on an id the spec cannot know before the push: the stub returns a
		// task for whatever the store hands it, so the row is linked whenever it
		// renders rather than only if the timing happens to favour it.
		provenance.ResolveStub = func(_ context.Context, items pkg.Items) pkg.Provenances {
			resolved := make(pkg.Provenances, len(items))
			for _, item := range items {
				resolved[item.ItemID] = pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
				}
			}
			return resolved
		}

		reader, cancel := connect()
		defer cancel()

		item := push("which task is this?")

		event := readEvent(reader)
		Expect(event["type"]).Should(Equal("upsert"))
		Expect(event["item_id"]).Should(Equal(item.ItemID.String()))
		// The hand-written literal, as the page's own spec writes it, so a change
		// to the URL shape has to be made in two places rather than agreeing with
		// itself through a shared helper.
		Expect(event["html"]).Should(ContainSubstring(
			`<span class="task"><a href="obsidian://open?vault=Personal&amp;file=25%20Tasks%2FFix%20the%20board">Fix the board</a></span>`,
		))
	})

	// The dimmed record reaches an open board over the live channel, not only
	// on the next load. The channel reads through ReadBoard, the same reader
	// the page uses, so an item that is answered arrives as a dimmed upsert
	// rather than leaving the rendered set.
	//
	// ⚠️ This is also the class the board's view filter keys on. A row that
	// arrived undimmed would render as a live card offering an answer to a
	// question that already has one, and the filter would have nothing to
	// hide — which is why the reader is asserted here rather than left to the
	// page's own specs.
	It("streams the dimmed record when an item is answered", func() {
		reader, cancel := connect()
		defer cancel()

		item := push("already answered?")

		// Positive control: the same row arrives undimmed while the item is
		// open, so this cannot pass on a channel that dims everything it
		// sends.
		open := readEvent(reader)
		Expect(open["type"]).Should(Equal("upsert"))
		Expect(open["item_id"]).Should(Equal(item.ItemID.String()))
		Expect(open["html"]).Should(ContainSubstring(`data-item-id="` + item.ItemID.String() + `"`))
		Expect(open["html"]).ShouldNot(ContainSubstring("item dimmed"))

		answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "yes"}
		_, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", &answer, nil, nil)
		Expect(err).Should(BeNil())

		event := readEvent(reader)
		Expect(event["type"]).Should(Equal("upsert"))
		Expect(event["item_id"]).Should(Equal(item.ItemID.String()))
		Expect(event["html"]).Should(ContainSubstring(
			`class="item dimmed" data-item-id="` + item.ItemID.String() + `"`,
		))
	})

	It("sends a removal when the item is resolved elsewhere", func() {
		reader, cancel := connect()
		defer cancel()

		item := push("deploy prod?")
		Expect(readEvent(reader)["type"]).Should(Equal("upsert"))

		_, err := store.Close(ctx, item.ItemID, "attention-board", nil)
		Expect(err).Should(BeNil())

		event := readEvent(reader)
		Expect(event["type"]).Should(Equal("remove"))
		Expect(event["item_id"]).Should(Equal(item.ItemID.String()))
		Expect(event["html"]).Should(BeEmpty())
	})

	It("sends nothing for a read", func() {
		reader, cancel := connect()
		defer cancel()

		// The quiet action first. If Read signalled, its event would be read
		// below instead of the push's — which is what makes this an assertion
		// rather than a window of silence.
		_, err := store.Read(ctx)
		Expect(err).Should(BeNil())

		item := push("deploy prod?")

		event := readEvent(reader)
		Expect(event["type"]).Should(Equal("upsert"))
		Expect(event["item_id"]).Should(Equal(item.ItemID.String()))
	})

	It("sends nothing when a write is rejected", func() {
		reader, cancel := connect()
		defer cancel()

		// No answer mechanism, so the store rejects the push before writing.
		_, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:   "producer-a",
			ProducerKind: pkg.SessionProducerKind,
			LivenessRef:  pkg.LivenessRef("session:producer-a"),
			DedupKey:     "rejected",
		})
		Expect(err).ShouldNot(BeNil())

		item := push("deploy prod?")

		event := readEvent(reader)
		Expect(event["type"]).Should(Equal("upsert"))
		Expect(event["item_id"]).Should(Equal(item.ItemID.String()))
	})
})

// The stream has to outlive the server's write deadline. libhttp's NewServer
// imposes a 30-second WriteTimeout by default, and a write deadline is fatal to a
// stream: the connection is killed mid-response, the browser reports
// ERR_INCOMPLETE_CHUNKED_ENCODING, and EventSource silently reconnects. The board
// then looks like it works while dropping and re-establishing the channel every
// 30 seconds — and it would satisfy a restart-and-reconnect criterion for the
// wrong reason, because reconnection was happening constantly anyway.
//
// This is the only spec that can catch it. httptest's default server sets no
// write deadline, so every spec above passes whether or not the handler clears
// one: the defect is a property of the server the handler runs under, not of the
// handler's own logic. Measured on the deployed service 2026-09-26 — two
// ERR_INCOMPLETE_CHUNKED_ENCODING on this route within a minute of a page load,
// with nothing in the service log, because the deadline fires in net/http rather
// than in the handler.
var _ = Describe("AttentionStreamHandler under a write deadline", func() {
	var ctx context.Context
	var db libkv.DB
	var notifier pkg.AttentionChangeNotifier
	var store pkg.AttentionStore
	var server *httptest.Server

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).Should(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		notifier = pkg.NewAttentionChangeNotifier()
		store = pkg.NewNotifyingAttentionStore(
			pkg.NewAttentionStore(
				db,
				pkg.NewItemIDGenerator(),
				sessionLivenessChecker,
				libtime.NewCurrentDateTime(),
				libtime.Duration(15*60*1e9),
			),
			notifier,
		)

		// One second rather than the real 30: the mechanism is identical and the
		// assertion is the same, and a spec that waited out the real default
		// would cost half a minute for no extra evidence.
		registry := prometheus.NewRegistry()
		metrics := boardmetrics.NewMetrics(registry)
		server = httptest.NewUnstartedServer(handler.NewAttentionStreamHandler(
			store,
			notifier,
			&mocks.ProvenanceResolver{},
			false,
			"",
			metrics,
		))
		server.Config.WriteTimeout = 1 * time.Second
		server.Start()
	})

	AfterEach(func() {
		server.Close()
	})

	It("still delivers an event after the deadline would have expired", func() {
		streamCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL, nil)
		Expect(err).Should(BeNil())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).Should(BeNil())
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)

		// Past the server's write deadline. Without the handler clearing it the
		// connection is already dead here, and the read below fails rather than
		// returning an event.
		time.Sleep(2 * time.Second)

		_, err = store.Push(ctx, pkg.PushRequest{
			ProducerID:      "producer-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-a"),
			DedupKey:        "after-deadline",
			InterruptClass:  "approve",
			Payload:         "past the deadline",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).Should(BeNil())

		var payload string
		for {
			line, err := reader.ReadString('\n')
			Expect(err).Should(BeNil(), "stream closed before delivering the event")
			line = strings.TrimRight(line, "\n")
			if line == "" {
				break
			}
			if after, found := strings.CutPrefix(line, "data: "); found {
				payload = after
			}
		}
		var event map[string]string
		Expect(json.Unmarshal([]byte(payload), &event)).Should(BeNil())
		Expect(event["type"]).Should(Equal("upsert"))
		Expect(event["html"]).Should(ContainSubstring("past the deadline"))
	})
})

// readCountingStore counts ReadBoard calls through the real store, so a spec
// measures the work the render path actually does rather than inferring it from
// a fake. It wraps rather than replaces: the count has to ride on the read the
// production path performs, or it counts something else.
type readCountingStore struct {
	pkg.AttentionStore
	reads int64
}

func (s *readCountingStore) ReadBoard(ctx context.Context) (pkg.Items, error) {
	atomic.AddInt64(&s.reads, 1)
	return s.AttentionStore.ReadBoard(ctx)
}

// Reads returns the number of ReadBoard calls made so far.
func (s *readCountingStore) Reads() int64 {
	return atomic.LoadInt64(&s.reads)
}

// metricFamily returns the gathered family with the given name, failing the
// spec when the registry does not expose it.
func metricFamily(registry *prometheus.Registry, name string) *dto.MetricFamily {
	families, err := registry.Gather()
	Expect(err).Should(BeNil())
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	return nil
}

// counterValue reads a single-series counter family's value.
func counterValue(registry *prometheus.Registry, name string) float64 {
	family := metricFamily(registry, name)
	Expect(family).ShouldNot(BeNil())
	return family.GetMetric()[0].GetCounter().GetValue()
}

// failingReadBoardStore makes the render path fail, so a spec can prove a
// failed render moves neither counter.
type failingReadBoardStore struct {
	pkg.AttentionStore
}

func (s *failingReadBoardStore) ReadBoard(ctx context.Context) (pkg.Items, error) {
	return nil, stderrors.New("read board failed")
}

// The board is rendered once per change and shared by every stream, not once
// per stream per change.
//
// ⚠️ The counting spec is SC1's probe and it exists to be RED on the pre-fix
// revision. The pre-fix handler renders inside each stream, so with N streams it
// reads N (one baseline each) + N×M (one render each per change). The fix makes
// it 1 + M for any N: one shared baseline, one render per change.
//
// ⚠️ N ≥ 2 is load bearing rather than decoration. At a single subscriber a
// per-client render returns exactly 1 + M — the same number the fixed handler
// returns — so the spec would be GREEN against the very defect it exists to
// catch.
var _ = Describe("AttentionStreamHandler render fan-out", func() {
	var ctx context.Context
	var db libkv.DB
	var notifier pkg.AttentionChangeNotifier
	var store *readCountingStore
	var server *httptest.Server
	// registry is Describe-scoped because the counter specs read the render
	// counter back off it, and it must be the same registry the handler under
	// test reports through.
	var registry *prometheus.Registry

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).Should(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		notifier = pkg.NewAttentionChangeNotifier()
		store = &readCountingStore{AttentionStore: pkg.NewNotifyingAttentionStore(
			pkg.NewAttentionStore(
				db,
				pkg.NewItemIDGenerator(),
				sessionLivenessChecker,
				libtime.NewCurrentDateTime(),
				libtime.Duration(15*60*1e9),
			),
			notifier,
		)}

		registry = prometheus.NewRegistry()
		metrics := boardmetrics.NewMetrics(registry)
		server = httptest.NewServer(handler.NewAttentionStreamHandler(
			store,
			notifier,
			&mocks.ProvenanceResolver{},
			false,
			filepath.Join(GinkgoT().TempDir(), "Personal"),
			metrics,
		))
	})

	AfterEach(func() {
		server.Close()
	})

	connect := func() (*bufio.Reader, context.CancelFunc) {
		streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL, nil)
		Expect(err).Should(BeNil())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).Should(BeNil())
		Expect(resp.StatusCode).Should(Equal(http.StatusOK))
		Expect(resp.Header.Get("Content-Type")).Should(Equal("text/event-stream"))
		return bufio.NewReader(resp.Body), cancel
	}

	push := func(payload string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "producer-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-a"),
			DedupKey:        pkg.DedupKey(payload),
			InterruptClass:  "approve",
			Payload:         pkg.Payload(payload),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).Should(BeNil())
		return item
	}

	readEvent := func(reader *bufio.Reader) map[string]string {
		var payload string
		for {
			line, err := reader.ReadString('\n')
			Expect(err).Should(BeNil())
			line = strings.TrimRight(line, "\n")
			if line == "" {
				break
			}
			if after, found := strings.CutPrefix(line, "data: "); found {
				payload = after
			}
		}
		var event map[string]string
		Expect(json.Unmarshal([]byte(payload), &event)).Should(BeNil())
		return event
	}

	DescribeTable("reads the store once per change however many streams are attached",
		func(streams int) {
			const changes = 3

			readers := make([]*bufio.Reader, 0, streams)
			for i := 0; i < streams; i++ {
				reader, cancel := connect()
				defer cancel()
				readers = append(readers, reader)
			}

			// The baseline is shared, so several connections cost one render
			// between them rather than one each. It is asserted here, before any
			// change, so a per-client baseline cannot hide inside the total below.
			Expect(store.Reads()).Should(Equal(int64(1)))

			// The counter baseline is read AFTER the streams are attached, so the
			// single render the first connection performs is already counted and is
			// excluded from the delta below. Read before connecting, the same window
			// would be 1 + changes.
			rendersBefore := counterValue(registry, "attention_board_renders_total")

			for i := 0; i < changes; i++ {
				item := push(fmt.Sprintf("change-%d", i))
				// Every stream is drained before the next change is pushed, so each
				// change's renders are counted on their own rather than racing the
				// next one's.
				for _, reader := range readers {
					event := readEvent(reader)
					Expect(event["type"]).Should(Equal("upsert"))
					Expect(event["item_id"]).Should(Equal(item.ItemID.String()))
				}
			}

			// 1 + changes, and the subscriber count does not appear in the figure at
			// all — which is the whole claim. Asserted at two and at four streams so
			// the claim is shown independent of N rather than merely correct at one.
			Expect(store.Reads()).Should(Equal(int64(1 + changes)))
			// The render counter's rise is the change count, never streams × changes:
			// a per-client render would move it by streams × changes here.
			Expect(
				counterValue(registry, "attention_board_renders_total") - rendersBefore,
			).Should(Equal(float64(changes)))
		},
		Entry("two streams", 2),
		Entry("four streams", 4),
	)

	// ⚠️ SC4's probe. Sharing the render must not cost the per-client diff: a
	// row the operator is typing into is still never replaced underneath them,
	// which means a row whose rendered HTML did not change is not sent — even
	// though the render that produced it is now shared by every stream.
	//
	// The absence is ordered rather than windowed, because "nothing was sent"
	// cannot be shown by waiting and observing silence: a handler that sent
	// nothing at all would pass that. So a real write follows, and the FIRST
	// event read after the change under test must be that write's.
	It("sends only the rows whose own HTML changed", func() {
		reader, cancel := connect()
		defer cancel()

		// Two items stand before the change under test, so a handler that
		// re-sent the whole board would have something wrong to send.
		first := push("first?")
		Expect(readEvent(reader)["item_id"]).Should(Equal(first.ItemID.String()))
		second := push("second?")
		Expect(readEvent(reader)["item_id"]).Should(Equal(second.ItemID.String()))

		// The change under test touches `first` alone; `second`'s rendered HTML
		// is untouched and must not be re-sent.
		answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "yes"}
		_, err := store.Answer(ctx, first.ItemID, "attention-board", "", "", &answer, nil, nil)
		Expect(err).Should(BeNil())

		event := readEvent(reader)
		Expect(event["type"]).Should(Equal("upsert"))
		Expect(event["item_id"]).Should(Equal(first.ItemID.String()))
		Expect(event["html"]).Should(ContainSubstring("item dimmed"))

		// The real write. Had the answer above also re-sent `second`, its event
		// would be read here instead of this push's.
		third := push("third?")
		Expect(readEvent(reader)["item_id"]).Should(Equal(third.ItemID.String()))
	})

	// A stream that joins while the board is warm costs no render at all: the
	// snapshot the handler already holds is the one the page has just drawn, so
	// asking for it is cheaper than rendering a second, identical board.
	It("does not render for a connection made while the board is warm", func() {
		first, cancelFirst := connect()
		defer cancelFirst()
		item := push("warm?")
		Expect(readEvent(first)["item_id"]).Should(Equal(item.ItemID.String()))

		rendersBefore := store.Reads()

		_, cancelSecond := connect()
		defer cancelSecond()

		Expect(store.Reads()).Should(Equal(rendersBefore))
	})
})

// The board's shared render path reports what it did, so the fan-out ratio can
// be read off the deployed binary's /metrics rather than only proven by a spec.
//
// ⚠️ Every spec here builds its OWN registry and reads the counters back off it,
// never the process-wide default registry: MustRegister panics on a second
// registration of the same collector, so a spec on the default registry would
// panic as soon as another spec registered the same names.
var _ = Describe("AttentionStreamHandler render counters", func() {
	var ctx context.Context
	var db libkv.DB
	var notifier pkg.AttentionChangeNotifier
	var store pkg.AttentionStore
	var registry *prometheus.Registry
	var metrics pkg.Metrics
	var server *httptest.Server

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).Should(BeNil())

		// The mock defaults to false, which would prune every fixture during
		// ReadBoard and change the row counts these specs assert on.
		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		notifier = pkg.NewAttentionChangeNotifier()
		store = pkg.NewNotifyingAttentionStore(
			pkg.NewAttentionStore(
				db,
				pkg.NewItemIDGenerator(),
				sessionLivenessChecker,
				libtime.NewCurrentDateTime(),
				libtime.Duration(15*60*1e9),
			),
			notifier,
		)

		registry = prometheus.NewRegistry()
		metrics = boardmetrics.NewMetrics(registry)
		server = httptest.NewServer(handler.NewAttentionStreamHandler(
			store,
			notifier,
			&mocks.ProvenanceResolver{},
			false,
			filepath.Join(GinkgoT().TempDir(), "Personal"),
			metrics,
		))
	})

	AfterEach(func() {
		server.Close()
	})

	connect := func() (*bufio.Reader, context.CancelFunc) {
		streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL, nil)
		Expect(err).Should(BeNil())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).Should(BeNil())
		Expect(resp.StatusCode).Should(Equal(http.StatusOK))
		Expect(resp.Header.Get("Content-Type")).Should(Equal("text/event-stream"))
		return bufio.NewReader(resp.Body), cancel
	}

	push := func(payload string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "producer-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-a"),
			DedupKey:        pkg.DedupKey(payload),
			InterruptClass:  "approve",
			Payload:         pkg.Payload(payload),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).Should(BeNil())
		return item
	}

	readEvent := func(reader *bufio.Reader) map[string]string {
		var payload string
		for {
			line, err := reader.ReadString('\n')
			Expect(err).Should(BeNil())
			line = strings.TrimRight(line, "\n")
			if line == "" {
				break
			}
			if after, found := strings.CutPrefix(line, "data: "); found {
				payload = after
			}
		}
		var event map[string]string
		Expect(json.Unmarshal([]byte(payload), &event)).Should(BeNil())
		return event
	}

	// The figure is the number of renders performed, not the number of streams
	// woken: one change costs one render however many streams are attached.
	It("counts one render per change", func() {
		reader, cancel := connect()
		defer cancel()

		// The baseline render is complete once the response headers arrive, so
		// the counter already reads one here rather than racing the render.
		Expect(counterValue(registry, "attention_board_renders_total")).Should(Equal(1.0))

		first := push("first change?")
		Expect(readEvent(reader)["item_id"]).Should(Equal(first.ItemID.String()))
		Expect(counterValue(registry, "attention_board_renders_total")).Should(Equal(2.0))

		second := push("second change?")
		Expect(readEvent(reader)["item_id"]).Should(Equal(second.ItemID.String()))
		Expect(counterValue(registry, "attention_board_renders_total")).Should(Equal(3.0))
	})

	// The rows counter is the render count multiplied by the board's size, which
	// is what makes the fan-out ratio readable off a deployed binary.
	It("counts the rows each render produced", func() {
		// Pushed before any subscriber attaches, so the signals go nowhere and
		// the board is two rows when the baseline render runs.
		first := push("first row?")
		second := push("second row?")

		reader, cancel := connect()
		defer cancel()
		Expect(counterValue(registry, "attention_board_renders_total")).Should(Equal(1.0))
		Expect(counterValue(registry, "attention_board_rows_rendered_total")).Should(Equal(2.0))

		// Two changes that keep the board at two rows: an answered item stays in
		// ReadBoard as a dimmed row, so each render still draws both. Answering
		// a DIFFERENT item each time is load bearing — Answer is a compare-and-
		// set from open, so answering the same item twice fails.
		answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "yes"}
		_, err := store.Answer(ctx, first.ItemID, "attention-board", "", "", &answer, nil, nil)
		Expect(err).Should(BeNil())
		Expect(readEvent(reader)["item_id"]).Should(Equal(first.ItemID.String()))

		_, err = store.Answer(ctx, second.ItemID, "attention-board", "", "", &answer, nil, nil)
		Expect(err).Should(BeNil())
		Expect(readEvent(reader)["item_id"]).Should(Equal(second.ItemID.String()))

		// Three renders of two rows each: the baseline plus one per change.
		Expect(counterValue(registry, "attention_board_renders_total")).Should(Equal(3.0))
		Expect(counterValue(registry, "attention_board_rows_rendered_total")).Should(Equal(6.0))
	})

	// A render that returned an error reports nothing, so a board that cannot be
	// read does not inflate either figure.
	It("counts neither counter when a render fails", func() {
		// ⚠️ A second server on the SAME metrics, and a plain http.Get rather
		// than connect: the handler returns without writing headers once the
		// baseline render fails, so the response arrives only after the render
		// has already failed — and it is not a text/event-stream response.
		failingServer := httptest.NewServer(handler.NewAttentionStreamHandler(
			&failingReadBoardStore{AttentionStore: store},
			notifier,
			&mocks.ProvenanceResolver{},
			false,
			"",
			metrics,
		))
		defer failingServer.Close()

		resp, err := http.Get(failingServer.URL)
		Expect(err).Should(BeNil())
		resp.Body.Close()

		// Registered-but-unincremented counters gather as 0, so this asserts the
		// value rather than the absence of the series.
		Expect(counterValue(registry, "attention_board_renders_total")).Should(Equal(0.0))
		Expect(counterValue(registry, "attention_board_rows_rendered_total")).Should(Equal(0.0))
	})
})
