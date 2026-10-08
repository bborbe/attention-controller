// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// readAll drains a request body so a spec can assert what the client sent. An
// unreadable body reads as empty rather than failing the spec: the assertions
// that follow then report the missing field, which is a better failure message
// than a decode error raised from inside the test server.
func readAll(req *http.Request) []byte {
	if req.Body == nil {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil
	}
	return body
}

// The remote client's contract is two-sided: it must speak the peer's wire
// shape, and it must turn the peer's failure back into the sentinel error this
// store's own callers switch on. The second half is the one worth pinning,
// because the peer reports a lost answer race and an item that left the queue
// as the SAME HTTP status — only the body's code separates them, and a client
// that mapped on status alone would report "no longer exists" for a race
// another arm won.
var _ = Describe("HTTP remote attention store", func() {
	var ctx context.Context
	var server *httptest.Server
	var remote pkg.RemoteAttentionStore
	// lastRequest is what the server saw, so the request side can be asserted
	// without a second server per spec.
	var lastRequest *http.Request
	var lastBody []byte

	// respondWith starts a peer that answers every request with the given status
	// and JSON body, recording the request it received.
	respondWith := func(status int, body string) {
		server = httptest.NewServer(http.HandlerFunc(
			func(resp http.ResponseWriter, req *http.Request) {
				lastRequest = req
				lastBody = readAll(req)
				resp.Header().Set("Content-Type", "application/json")
				resp.WriteHeader(status)
				_, _ = resp.Write([]byte(body))
			},
		))
		remote = pkg.NewHTTPRemoteAttentionStore(server.URL, "peer-token")
	}

	BeforeEach(func() {
		ctx = context.Background()
		lastRequest = nil
		lastBody = nil
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
			server = nil
		}
	})

	Context("reading", func() {
		It("decodes the peer's items with no conversion step", func() {
			respondWith(http.StatusOK, `[{"item_id":"peer-1","producer_id":"pod","state":"open"}]`)

			items, err := remote.Read(ctx)

			Expect(err).Should(BeNil())
			Expect(items).Should(HaveLen(1))
			Expect(items[0].ItemID).Should(Equal(pkg.ItemID("peer-1")))
			Expect(items[0].ProducerID).Should(Equal(pkg.ProducerID("pod")))
		})

		It("presents the bearer token and never in the URL", func() {
			respondWith(http.StatusOK, `[]`)

			_, err := remote.Read(ctx)
			Expect(err).Should(BeNil())

			Expect(lastRequest.Header.Get("Authorization")).Should(Equal("Bearer peer-token"))
			Expect(lastRequest.URL.RawQuery).Should(BeEmpty())
			Expect(lastRequest.URL.Path).Should(Equal("/api/1.0/attention"))
		})
	})

	Context("mapping the peer's failure back to a sentinel", func() {
		It("maps ALREADY_ANSWERED to ErrAlreadyAnswered", func() {
			respondWith(
				http.StatusConflict,
				`{"error":{"code":"ALREADY_ANSWERED","message":"another arm won"}}`,
			)

			_, err := remote.Answer(ctx, "peer-1", "board", "session-1", "", nil, nil, nil)

			Expect(errors.Is(err, pkg.ErrAlreadyAnswered)).Should(BeTrue())
		})

		It("maps ITEM_CLOSED to ErrIllegalTransition, not to ErrAlreadyAnswered", func() {
			respondWith(
				http.StatusConflict,
				`{"error":{"code":"ITEM_CLOSED","message":"left the queue"}}`,
			)

			_, err := remote.Answer(ctx, "peer-1", "board", "session-1", "", nil, nil, nil)

			Expect(errors.Is(err, pkg.ErrIllegalTransition)).Should(BeTrue())
			Expect(errors.Is(err, pkg.ErrAlreadyAnswered)).Should(BeFalse())
		})

		It("maps a 404 to ErrItemNotFound", func() {
			respondWith(
				http.StatusNotFound,
				`{"error":{"code":"NOT_FOUND","message":"no such item"}}`,
			)

			_, err := remote.Get(ctx, "peer-1")

			Expect(errors.Is(err, pkg.ErrItemNotFound)).Should(BeTrue())
		})

		It("maps a 404 with an unreadable body to ErrItemNotFound too", func() {
			respondWith(http.StatusNotFound, `<html>not json</html>`)

			_, err := remote.Get(ctx, "peer-1")

			Expect(errors.Is(err, pkg.ErrItemNotFound)).Should(BeTrue())
		})

		It("maps ALREADY_ESCALATED and ITEM_NOT_OPEN", func() {
			respondWith(
				http.StatusConflict,
				`{"error":{"code":"ALREADY_ESCALATED","message":"carried already"}}`,
			)
			_, err := remote.Escalate(ctx, "peer-1", "session-1")
			Expect(errors.Is(err, pkg.ErrAlreadyEscalated)).Should(BeTrue())

			server.Close()
			respondWith(
				http.StatusConflict,
				`{"error":{"code":"ITEM_NOT_OPEN","message":"not open"}}`,
			)
			_, err = remote.Escalate(ctx, "peer-1", "session-1")
			Expect(errors.Is(err, pkg.ErrItemNotOpen)).Should(BeTrue())
		})

		It("does not flatten an unauthenticated peer into a sentinel", func() {
			respondWith(
				http.StatusUnauthorized,
				`{"error":{"code":"UNAUTHORIZED","message":"bad token"}}`,
			)

			_, err := remote.Read(ctx)

			Expect(err).ShouldNot(BeNil())
			Expect(errors.Is(err, pkg.ErrItemNotFound)).Should(BeFalse())
			Expect(err.Error()).Should(ContainSubstring("401"))
		})
	})

	Context("answering", func() {
		It("posts the peer's own body shape to the item's answer route", func() {
			respondWith(http.StatusOK, `{"item_id":"peer-1","state":"answered"}`)
			automation := true

			item, err := remote.Answer(
				ctx,
				"peer-1",
				"attention-board",
				"session-1",
				pkg.AllowDecision,
				nil,
				nil,
				&pkg.AnsweredClient{Automation: &automation},
			)

			Expect(err).Should(BeNil())
			Expect(item.State).Should(Equal(pkg.AnsweredState))
			Expect(lastRequest.Method).Should(Equal(http.MethodPost))
			Expect(lastRequest.URL.Path).Should(Equal("/api/1.0/attention/peer-1/answer"))

			var body map[string]any
			Expect(json.Unmarshal(lastBody, &body)).Should(Succeed())
			Expect(body["answered_by"]).Should(Equal("attention-board"))
			Expect(body["resolved_by"]).Should(Equal("session-1"))
			Expect(body["decision"]).Should(Equal("allow"))
			Expect(body["automation"]).Should(BeTrue())
			// ⚠️ The peer derives these from its own request and never accepts
			// them from a body, so the client must not send them at all.
			Expect(body).ShouldNot(HaveKey("user_agent"))
			Expect(body).ShouldNot(HaveKey("remote_addr"))
		})
	})
})
