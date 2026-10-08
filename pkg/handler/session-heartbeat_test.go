// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	stdtime "time"

	libtime "github.com/bborbe/time"
	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The endpoint's load-bearing claims are the REJECTIONS and the three-way read.
// A valid post returning 200 proves almost nothing — the interesting cases are
// that a declaration missing a required field is refused rather than stored,
// and that `absent` (404) stays distinct from `stale` (200 with live:false).
// Those are the two a reader would otherwise collapse.
var _ = Describe("Session heartbeat handlers", func() {
	var ctx context.Context
	var dir string
	var store pkg.SessionHeartbeatStore

	// heartbeatWindow is the endpoint's own window. See the config field for
	// why it is not the supervisor's 15-minute HEARTBEAT_WINDOW.
	const heartbeatWindow = libtime.Duration(60 * stdtime.Second)

	// now is the movable clock. It is what lets the stale case be produced
	// without sleeping.
	var now stdtime.Time
	clock := func() libtime.CurrentDateTimeGetter {
		return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
	}

	// body is a well-formed declaration. Each rejection spec removes exactly
	// one field from it, so a failure names the field it is about.
	body := func() map[string]any {
		return map[string]any{
			"session_id": "5f2a1c34-0000-4000-8000-000000000001",
			"task":       "Session Liveness Comes From a Heartbeat Store in attention-controller",
			"vault":      "private-personal",
			"location":   "local",
			"state":      "idle",
			"source":     "mcp-timer",
		}
	}

	post := func(payload map[string]any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(payload)
		Expect(err).To(BeNil())
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/session-heartbeat",
			bytes.NewReader(raw),
		)
		resp := httptest.NewRecorder()
		handler.NewSessionHeartbeatPostHandler(store).ServeHTTP(resp, req)
		return resp
	}

	BeforeEach(func() {
		ctx = context.Background()
		now = stdtime.Date(2026, 10, 8, 9, 0, 0, 0, stdtime.UTC)
		var err error
		dir, err = os.MkdirTemp("", "session-heartbeat-*")
		Expect(err).To(BeNil())
		store = pkg.NewSessionHeartbeatStore(dir, clock())
	})

	AfterEach(func() {
		Expect(os.RemoveAll(dir)).To(BeNil())
	})

	Describe("POST", func() {
		It("accepts a well-formed declaration and returns 200", func() {
			Expect(post(body()).Code).To(Equal(http.StatusOK))
		})

		It("stamps the row from the store's own clock, not the caller's", func() {
			// A caller-supplied timestamp would let a session declare itself
			// immortal, so the posted value must be ignored.
			payload := body()
			payload["at"] = "2099-01-01T00:00:00Z"
			Expect(post(payload).Code).To(Equal(http.StatusOK))

			stored, found, err := store.Get(ctx, "5f2a1c34-0000-4000-8000-000000000001")
			Expect(err).To(BeNil())
			Expect(found).To(BeTrue())
			Expect(stdtime.Time(stored.At).UTC()).To(Equal(now))
		})

		// One spec per required field, so a rejection that stops covering a
		// field fails by name rather than silently.
		for _, field := range []string{"session_id", "location", "state", "source"} {
			field := field
			It("rejects a declaration missing "+field+" with 400", func() {
				payload := body()
				delete(payload, field)
				Expect(post(payload).Code).To(Equal(http.StatusBadRequest))
			})
		}

		It("rejects an unknown location with 400", func() {
			payload := body()
			payload["location"] = "moon"
			Expect(post(payload).Code).To(Equal(http.StatusBadRequest))
		})

		It("rejects a task declared without its vault with 400", func() {
			payload := body()
			delete(payload, "vault")
			Expect(post(payload).Code).To(Equal(http.StatusBadRequest))
		})

		It("rejects a session id carrying a path separator with 400", func() {
			// ⚠️ The id arrives from the network and becomes a FILENAME, so this
			// is the one input that could escape the store directory. A row
			// written outside it would be invisible to every reader and would
			// overwrite whatever it landed on.
			payload := body()
			payload["session_id"] = "../../escaped"
			Expect(post(payload).Code).To(Equal(http.StatusBadRequest))
		})

		It("rejects a session id that is a bare dot with 400", func() {
			payload := body()
			payload["session_id"] = ".."
			Expect(post(payload).Code).To(Equal(http.StatusBadRequest))
		})

		It("rejects a body that is not valid JSON with 400", func() {
			req := httptest.NewRequest(
				http.MethodPost, "/api/1.0/session-heartbeat", bytes.NewReader([]byte("{")),
			)
			resp := httptest.NewRecorder()
			handler.NewSessionHeartbeatPostHandler(store).ServeHTTP(resp, req)
			Expect(resp.Code).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("GET one", func() {
		get := func(sessionID string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(
				http.MethodGet, "/api/1.0/session-heartbeat/"+sessionID, nil,
			)
			req = mux.SetURLVars(req, map[string]string{"sessionID": sessionID})
			resp := httptest.NewRecorder()
			handler.NewSessionHeartbeatGetHandler(store, clock(), heartbeatWindow).
				ServeHTTP(resp, req)
			return resp
		}

		It("answers 404 for a session that never posted", func() {
			// `absent`, and it must not be reported as `stale`.
			Expect(get("never-seen").Code).To(Equal(http.StatusNotFound))
		})

		It("answers 400 for a malformed session id, not 500", func() {
			// ⚠️ The id becomes a filename, so the store refuses `..` — and that
			// is the CALLER's error. Answering 500 would blame the server for the
			// caller's input, and folding it into the store-failure branch would
			// blur the guarantee this file documents: an unreadable store is a
			// FAILURE, never `absent`.
			Expect(get("..").Code).To(Equal(http.StatusBadRequest))
		})

		It("answers 200 with live true for a fresh row", func() {
			Expect(post(body()).Code).To(Equal(http.StatusOK))
			resp := get("5f2a1c34-0000-4000-8000-000000000001")
			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(resp.Body.String()).To(ContainSubstring(`"live":true`))
		})

		It("answers 200 with live false for a row past the window", func() {
			Expect(post(body()).Code).To(Equal(http.StatusOK))
			// 61 seconds on — one second past the 60 s window.
			now = now.Add(61 * stdtime.Second)
			resp := get("5f2a1c34-0000-4000-8000-000000000001")
			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(resp.Body.String()).To(ContainSubstring(`"live":false`))
		})

		It("keeps absent distinct from stale", func() {
			// The pair that matters: one session posted and died, another never
			// existed. They must not read the same.
			Expect(post(body()).Code).To(Equal(http.StatusOK))
			now = now.Add(61 * stdtime.Second)
			Expect(get("5f2a1c34-0000-4000-8000-000000000001").Code).To(Equal(http.StatusOK))
			Expect(get("never-seen").Code).To(Equal(http.StatusNotFound))
		})
	})

	Describe("GET list", func() {
		list := func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/api/1.0/session-heartbeat", nil)
			resp := httptest.NewRecorder()
			handler.NewSessionHeartbeatListHandler(store, clock(), heartbeatWindow).
				ServeHTTP(resp, req)
			return resp
		}

		It("returns an empty list when nothing has posted", func() {
			resp := list()
			Expect(resp.Code).To(Equal(http.StatusOK))
			// MatchJSON rather than an exact string: the response writer appends
			// a newline, and pinning the whitespace would make this spec fail on
			// a formatting change that breaks no caller.
			Expect(resp.Body.String()).To(MatchJSON("[]"))
		})

		It("keeps stale rows in the listing so a recent death stays visible", func() {
			Expect(post(body()).Code).To(Equal(http.StatusOK))
			now = now.Add(61 * stdtime.Second)
			resp := list()
			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(resp.Body.String()).To(ContainSubstring(`"live":false`))
		})
	})
})
