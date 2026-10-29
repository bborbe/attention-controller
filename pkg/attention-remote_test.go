// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

		It("maps the peer's decision-less permission refusal back to its own sentinel", func() {
			// ⚠️ The body is built from the constant rather than typed as a literal, so
			// the code this client reads and the code the peer writes cannot drift: the
			// wire string is the contract the mapping round-trips.
			respondWith(
				http.StatusConflict,
				fmt.Sprintf(
					`{"error":{"code":%q,"message":"records no decision"}}`,
					pkg.ErrorCodePermissionCloseWithoutDecision,
				),
			)

			_, err := remote.Close(ctx, "peer-1", "attention-board", nil)

			Expect(errors.Is(err, pkg.ErrPermissionCloseWithoutDecision)).Should(BeTrue())
			// ⚠️ Not ErrPeerUnavailable: a deterministic refusal is not a retryable
			// outage, and reporting it as one is the one instruction that is wrong here.
			Expect(errors.Is(err, pkg.ErrPeerUnavailable)).Should(BeFalse())
		})

		It("maps an unclassified peer failure to ErrPeerUnavailable", func() {
			// ⚠️ This is the arm that used to carry NO sentinel at all, which is
			// what let every handler mapping fall through to its default and
			// answer 409 VALIDATION_ERROR — a peer outage reported to the client
			// as a malformed request.
			respondWith(
				http.StatusServiceUnavailable,
				`{"error":{"code":"","message":"peer is down"}}`,
			)

			_, err := remote.Read(ctx)

			Expect(errors.Is(err, pkg.ErrPeerUnavailable)).Should(BeTrue())
			// ⚠️ Not ErrItemNotFound: a failed call is not evidence of absence,
			// so a 404 here would answer a question the read never settled.
			Expect(errors.Is(err, pkg.ErrItemNotFound)).Should(BeFalse())
		})

		It("maps an UNREACHABLE peer to ErrPeerUnavailable", func() {
			// ⚠️ The arm that matters most, and the one an httptest server
			// structurally cannot produce: every server above ANSWERS, so every
			// failure it can raise arrives as a response and is classified by
			// remoteResponseError. A refused connection never gets there — there
			// is no response to classify — and this path was still sentinel-less
			// after the first fix, still answering 409 VALIDATION_ERROR. It took
			// a live probe of the built binary against a dead port to find that;
			// these specs could not, which is why this one exists.
			//
			// The port is bound and then released, so the connection is refused
			// promptly rather than hanging until the client's own timeout.
			listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
			Expect(listenErr).To(BeNil())
			deadURL := "http://" + listener.Addr().String()
			Expect(listener.Close()).To(BeNil())

			remote = pkg.NewHTTPRemoteAttentionStore(deadURL, "peer-token")

			_, err := remote.Get(ctx, "peer-1")

			Expect(errors.Is(err, pkg.ErrPeerUnavailable)).Should(BeTrue())
			// ⚠️ Not ErrItemNotFound: a connection that was refused is not
			// evidence that the item is absent.
			Expect(errors.Is(err, pkg.ErrItemNotFound)).Should(BeFalse())
		})

		It("maps a rejected token to ErrPeerUnavailable, not to ErrItemNotFound", func() {
			// ⚠️ The 401 is the reading observed LIVE against the configured peer
			// (the tokenless call), not a constructed case.
			//
			// It is a different OPERATOR problem from an unreachable peer, and the
			// message keeps both the status and the peer's own code so the two stay
			// distinguishable there — but it is the same CLIENT answer, because
			// neither is something the caller can correct.
			respondWith(
				http.StatusUnauthorized,
				`{"error":{"code":"UNAUTHORIZED","message":"bad token"}}`,
			)

			_, err := remote.Read(ctx)

			Expect(err).ShouldNot(BeNil())
			Expect(errors.Is(err, pkg.ErrPeerUnavailable)).Should(BeTrue())
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

		// ⚠️ The route has to survive the wire, not just the local store: a
		// federated answer records where it came from on the PEER, and a dropped
		// field here would make the record depend on which store owned the item.
		It("carries the answer route into the peer's body", func() {
			respondWith(http.StatusOK, `{"item_id":"peer-1","state":"answered"}`)

			_, err := remote.Answer(
				ctx,
				"peer-1",
				"attention-watcher",
				"session-1",
				pkg.AllowDecision,
				nil,
				nil,
				nil,
				pkg.AnswerRoute{RequestID: "dmhc3urrw8uo", Policy: "routine-tool:Read"},
			)
			Expect(err).Should(BeNil())

			var body map[string]any
			Expect(json.Unmarshal(lastBody, &body)).Should(Succeed())
			Expect(body["request_id"]).Should(Equal("dmhc3urrw8uo"))
			Expect(body["policy"]).Should(Equal("routine-tool:Read"))
		})

		// An arm that supplies no route posts no route keys, so the peer's
		// present-only stamping cannot blank a route a deciding arm already wrote.
		It("omits the route keys when the caller supplies none", func() {
			respondWith(http.StatusOK, `{"item_id":"peer-1","state":"answered"}`)

			_, err := remote.Answer(
				ctx, "peer-1", "attention-board", "session-1", pkg.AllowDecision, nil, nil, nil,
			)
			Expect(err).Should(BeNil())

			var body map[string]any
			Expect(json.Unmarshal(lastBody, &body)).Should(Succeed())
			Expect(body).ShouldNot(HaveKey("request_id"))
			Expect(body).ShouldNot(HaveKey("policy"))
		})
	})

	Context("flagging", func() {
		It("sends the reason to the peer's flag route", func() {
			respondWith(http.StatusOK, `{"item_id":"peer-1","state":"open"}`)

			item, err := remote.Flag(ctx, "peer-1", "asked the wrong team")

			Expect(err).Should(BeNil())
			Expect(item.ItemID).Should(Equal(pkg.ItemID("peer-1")))
			Expect(lastRequest.Method).Should(Equal(http.MethodPost))
			Expect(lastRequest.URL.Path).Should(Equal("/api/1.0/attention/peer-1/flag"))
			Expect(lastRequest.Header.Get("Authorization")).Should(Equal("Bearer peer-token"))

			var body map[string]any
			Expect(json.Unmarshal(lastBody, &body)).Should(Succeed())
			Expect(body["reason"]).Should(Equal("asked the wrong team"))
		})

		It("sends no body to the peer's unflag route", func() {
			respondWith(http.StatusOK, `{"item_id":"peer-1","state":"open"}`)

			item, err := remote.Unflag(ctx, "peer-1")

			Expect(err).Should(BeNil())
			Expect(item.ItemID).Should(Equal(pkg.ItemID("peer-1")))
			Expect(lastRequest.Method).Should(Equal(http.MethodPost))
			Expect(lastRequest.URL.Path).Should(Equal("/api/1.0/attention/peer-1/unflag"))
			Expect(lastBody).Should(BeEmpty())
		})

		It("maps the peer's ITEM_NOT_OPEN to ErrItemNotOpen", func() {
			respondWith(
				http.StatusConflict,
				`{"error":{"code":"ITEM_NOT_OPEN","message":"not open"}}`,
			)

			_, err := remote.Flag(ctx, "peer-1", "asked the wrong team")

			Expect(errors.Is(err, pkg.ErrItemNotOpen)).Should(BeTrue())
		})

		It("maps the peer's 404 to ErrItemNotFound", func() {
			respondWith(
				http.StatusNotFound,
				`{"error":{"code":"NOT_FOUND","message":"no such item"}}`,
			)

			_, err := remote.Unflag(ctx, "peer-1")

			Expect(errors.Is(err, pkg.ErrItemNotFound)).Should(BeTrue())
		})

		It("maps the peer's FLAG_REASON_REQUIRED to ErrFlagReasonRequired", func() {
			respondWith(
				http.StatusBadRequest,
				`{"error":{"code":"FLAG_REASON_REQUIRED","message":"reason required"}}`,
			)

			_, err := remote.Flag(ctx, "peer-1", "")

			Expect(errors.Is(err, pkg.ErrFlagReasonRequired)).Should(BeTrue())
		})

		It("maps an unreachable peer to ErrPeerUnavailable", func() {
			// The port is bound and then released, so the connection is refused
			// promptly rather than hanging until the client's own timeout.
			listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
			Expect(listenErr).To(BeNil())
			deadURL := "http://" + listener.Addr().String()
			Expect(listener.Close()).To(BeNil())

			remote = pkg.NewHTTPRemoteAttentionStore(deadURL, "peer-token")

			_, err := remote.Flag(ctx, "peer-1", "asked the wrong team")

			Expect(errors.Is(err, pkg.ErrPeerUnavailable)).Should(BeTrue())
		})
	})
})
