// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
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
	var server *httptest.Server

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

		server = httptest.NewServer(handler.NewAttentionStreamHandler(
			store,
			notifier,
			&mocks.ProvenanceResolver{},
			false,
			nil,
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
		server = httptest.NewUnstartedServer(handler.NewAttentionStreamHandler(
			store,
			notifier,
			&mocks.ProvenanceResolver{},
			false,
			nil,
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
