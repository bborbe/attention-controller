// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The read-aloud endpoint is a server-side proxy to the tts server, because the
// tts server has no CORS middleware and a page on this origin cannot POST to it
// directly. These specs assert what the proxy forwards and what it returns, so
// the board's control is backed by a real round trip rather than by a shape
// assumed from the tts server's docs.
var _ = Describe("AttentionSpeakHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	pushItem := func(payload string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "question-1",
			InterruptClass:  "pick",
			Payload:         pkg.Payload(payload),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// speak posts to the endpoint under test.
	speak := func(httpHandler http.Handler, itemID pkg.ItemID) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/speak",
			nil,
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		return rec
	}

	// fakeTTS stands in for the tts server. It captures the body it was sent, so
	// the assertion is what the proxy forwarded rather than that it forwarded
	// something.
	newFakeTTS := func(status int, body string) (*httptest.Server, *string, *string) {
		var gotBody, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			gotBody = string(raw)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		return server, &gotBody, &gotPath
	}

	It("forwards the item's payload and returns the tts message id", func() {
		server, gotBody, gotPath := newFakeTTS(
			http.StatusOK,
			`{"message_id":"msg-1","status":"queued","queue_position":0}`,
		)
		defer server.Close()

		item := pushItem("Deploy to prod?")
		rec := speak(handler.NewAttentionSpeakHandler(store, server.URL), item.ItemID)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(*gotPath).To(Equal("/say"))

		var sent struct {
			Text   string `json:"text"`
			Sender string `json:"sender"`
		}
		Expect(json.Unmarshal([]byte(*gotBody), &sent)).To(BeNil())
		Expect(sent.Text).To(Equal("Deploy to prod?"))
		Expect(sent.Sender).To(Equal("attention-board"))

		var got struct {
			MessageID string `json:"message_id"`
		}
		Expect(json.Unmarshal(rec.Body.Bytes(), &got)).To(BeNil())
		Expect(got.MessageID).To(Equal("msg-1"))
	})

	It("returns 404 for an unknown item", func() {
		server, gotBody, _ := newFakeTTS(http.StatusOK, `{"message_id":"msg-1"}`)
		defer server.Close()

		rec := speak(
			handler.NewAttentionSpeakHandler(store, server.URL),
			pkg.ItemID("does-not-exist"),
		)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
		// The upstream must not have been called: an item that does not exist has
		// nothing to read aloud.
		Expect(*gotBody).To(BeEmpty())
	})

	// A rejection from the tts server must arrive with its explanation attached,
	// not as a bare gateway error: "unknown engine" is actionable and "502" is
	// not.
	It("surfaces the tts server's rejection with its body", func() {
		server, _, _ := newFakeTTS(
			http.StatusBadRequest,
			`{"detail":"Engine 'piper' is unavailable"}`,
		)
		defer server.Close()

		item := pushItem("Deploy to prod?")
		rec := speak(handler.NewAttentionSpeakHandler(store, server.URL), item.ItemID)

		Expect(rec.Code).To(Equal(http.StatusBadGateway))
		Expect(rec.Body.String()).To(ContainSubstring("Engine 'piper' is unavailable"))
	})

	It("returns 502 when the tts server is unreachable", func() {
		// A server that is closed immediately, so the port is refused rather than
		// answered.
		server, _, _ := newFakeTTS(http.StatusOK, `{"message_id":"msg-1"}`)
		url := server.URL
		server.Close()

		item := pushItem("Deploy to prod?")
		rec := speak(handler.NewAttentionSpeakHandler(store, url), item.ItemID)

		Expect(rec.Code).To(Equal(http.StatusBadGateway))
	})

	It("tolerates a trailing slash in the configured base URL", func() {
		server, _, gotPath := newFakeTTS(http.StatusOK, `{"message_id":"msg-1"}`)
		defer server.Close()

		item := pushItem("Deploy to prod?")
		rec := speak(handler.NewAttentionSpeakHandler(store, server.URL+"/"), item.ItemID)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(*gotPath).To(Equal("/say"))
		Expect(strings.TrimSpace(rec.Body.String())).NotTo(BeEmpty())
	})
})
