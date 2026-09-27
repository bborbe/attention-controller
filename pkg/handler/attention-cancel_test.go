// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/handler"
)

// The stop half of the read-aloud control is a server-side proxy to the tts
// server's /cancel, for the same reason the speak half is: the tts server has
// no CORS middleware, so a page on this origin can only talk to its own.
//
// ⚠️ The assertion carrying the most weight here is the one about what is NOT
// forwarded. The tts server is a single queue every session speaks through, so
// a body-less or `{all: true}` cancel stops whatever is playing — another
// session's narration as readily as this board's. A proxy that forwarded either
// would be a worse defect than the one-way control this task replaces, and a
// spec that only checked "a cancel was sent" would pass on it.
var _ = Describe("AttentionCancelHandler", func() {
	// cancel posts to the endpoint under test. An empty body sends no body at
	// all, which is the shape the tts server reads as "stop whatever is
	// playing" — reachable here only so a spec can prove the endpoint refuses
	// it rather than forwarding it.
	cancel := func(httpHandler http.Handler, itemID string, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID+"/cancel",
			reader,
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID})
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		return rec
	}

	// fakeTTS stands in for the tts server. It captures the body it was sent,
	// so the assertion is on what the proxy forwarded rather than on the fact
	// that it forwarded something.
	newFakeTTS := func(status int, responseBody string) (*httptest.Server, *string, *string) {
		var gotBody, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			gotBody = string(raw)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(responseBody))
		}))
		return server, &gotBody, &gotPath
	}

	It("forwards the message id and returns the tts response", func() {
		server, gotBody, gotPath := newFakeTTS(
			http.StatusOK,
			`{"cancelled":["msg-1"],"queued":0}`,
		)
		defer server.Close()

		rec := cancel(
			handler.NewAttentionCancelHandler(server.URL),
			"item-1",
			`{"message_id":"msg-1"}`,
		)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(*gotPath).To(Equal("/cancel"))

		var sent map[string]any
		Expect(json.Unmarshal([]byte(*gotBody), &sent)).To(BeNil())
		Expect(sent["message_id"]).To(Equal("msg-1"))

		var got struct {
			Cancelled []string `json:"cancelled"`
			Queued    int      `json:"queued"`
		}
		Expect(json.Unmarshal(rec.Body.Bytes(), &got)).To(BeNil())
		Expect(got.Cancelled).To(Equal([]string{"msg-1"}))
	})

	// ⚠️ The load-bearing spec. `all` and a body-less cancel both drain the
	// shared queue, so the key must be absent from what the board forwards —
	// absent, not false. The tts server reads a missing field and a false field
	// the same way, but asserting on absence is what goes red if the field is
	// ever added back to the request struct, and that is the regression worth
	// catching. The request below carries `all: true` deliberately: a caller
	// cannot smuggle a queue drain through the proxy by asking for one.
	It("never forwards an `all` key, even when the caller sends one", func() {
		server, gotBody, _ := newFakeTTS(http.StatusOK, `{"cancelled":["msg-1"],"queued":0}`)
		defer server.Close()

		rec := cancel(
			handler.NewAttentionCancelHandler(server.URL),
			"item-1",
			`{"message_id":"msg-1","all":true}`,
		)
		Expect(rec.Code).To(Equal(http.StatusOK))

		var sent map[string]any
		Expect(json.Unmarshal([]byte(*gotBody), &sent)).To(BeNil())
		Expect(sent).To(HaveKey("message_id"))
		Expect(sent).NotTo(HaveKey("all"))
	})

	// A body with no id is refused before anything is forwarded. The only
	// default the tts server offers is "stop whatever is playing", so
	// defaulting here would send exactly the queue drain this endpoint exists
	// to avoid.
	It("refuses an empty message id without calling the tts server", func() {
		server, gotBody, _ := newFakeTTS(http.StatusOK, `{"cancelled":[],"queued":0}`)
		defer server.Close()

		rec := cancel(handler.NewAttentionCancelHandler(server.URL), "item-1", `{}`)

		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(*gotBody).To(BeEmpty())
	})

	It("refuses an empty item id without calling the tts server", func() {
		server, gotBody, _ := newFakeTTS(http.StatusOK, `{"cancelled":[],"queued":0}`)
		defer server.Close()

		rec := cancel(handler.NewAttentionCancelHandler(server.URL), "", `{"message_id":"msg-1"}`)

		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(*gotBody).To(BeEmpty())
	})

	// ⚠️ 404 is an outcome, not a failure. The tts server answers it for an id
	// that has aged out of its TTL-pruned status table, which means the reading
	// is over and there is nothing left to stop — the caller reports that
	// differently from a gateway error, and the control returns to idle rather
	// than stranding the operator on a stop button that cannot stop anything.
	It("reports an unknown message id as 404, not as a gateway failure", func() {
		server, _, _ := newFakeTTS(
			http.StatusNotFound,
			`{"detail":"Unknown message ID: msg-gone"}`,
		)
		defer server.Close()

		rec := cancel(
			handler.NewAttentionCancelHandler(server.URL),
			"item-1",
			`{"message_id":"msg-gone"}`,
		)

		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Body.String()).To(ContainSubstring("msg-gone"))
	})

	It("surfaces any other tts rejection as a gateway error with its body", func() {
		server, _, _ := newFakeTTS(http.StatusInternalServerError, `{"detail":"boom"}`)
		defer server.Close()

		rec := cancel(
			handler.NewAttentionCancelHandler(server.URL),
			"item-1",
			`{"message_id":"msg-1"}`,
		)

		Expect(rec.Code).To(Equal(http.StatusBadGateway))
		Expect(rec.Body.String()).To(ContainSubstring("boom"))
	})

	It("returns 502 when the tts server is unreachable", func() {
		// A server closed immediately, so the port is refused rather than
		// answered.
		server, _, _ := newFakeTTS(http.StatusOK, `{"cancelled":[],"queued":0}`)
		url := server.URL
		server.Close()

		rec := cancel(handler.NewAttentionCancelHandler(url), "item-1", `{"message_id":"msg-1"}`)

		Expect(rec.Code).To(Equal(http.StatusBadGateway))
	})

	It("tolerates a trailing slash in the configured base URL", func() {
		server, _, gotPath := newFakeTTS(http.StatusOK, `{"cancelled":["msg-1"],"queued":0}`)
		defer server.Close()

		rec := cancel(
			handler.NewAttentionCancelHandler(server.URL+"/"),
			"item-1",
			`{"message_id":"msg-1"}`,
		)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(*gotPath).To(Equal("/cancel"))
	})
})
