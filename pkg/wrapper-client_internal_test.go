// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("claude wrapper client", func() {
	const sessionID = "11111111-2222-3333-4444-555555555555"

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("reports none for a 404", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		state, session := NewClaudeWrapperClient(server.URL).Probe(ctx, sessionID)
		Expect(state).To(Equal(WrapperProbeNone))
		Expect(session).To(BeNil())
	})

	It("reports unreachable for a 503, not none", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		state, _ := NewClaudeWrapperClient(server.URL).Probe(ctx, sessionID)
		Expect(state).To(Equal(WrapperProbeUnreachable))
	})

	It("returns the pending question", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(
				`{"state":"running","question":{"request_id":"req-1",` +
					`"input":{"tool_name":"Read","input":{"file_path":"/tmp/a"}}}}`))
		}))
		defer server.Close()
		state, session := NewClaudeWrapperClient(server.URL).Probe(ctx, sessionID)
		Expect(state).To(Equal(WrapperProbeOK))
		Expect(session).NotTo(BeNil())
		Expect(session.Question).NotTo(BeNil())
		Expect(session.Question.RequestID).To(Equal("req-1"))
	})

	It("reports none without a request for a non-UUID session id", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			Fail("the wrapper must not be asked about a non-UUID id")
		}))
		defer server.Close()
		state, _ := NewClaudeWrapperClient(server.URL).Probe(ctx, "not-a-uuid")
		Expect(state).To(Equal(WrapperProbeNone))
	})

	It("delivers an answer and reports ok on a 2xx", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/1.0/sessions/"+sessionID {
				_, _ = w.Write([]byte(
					`{"question":{"request_id":"req-1","input":{"tool_name":"Read"}}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		verdict, reason := NewClaudeWrapperClient(server.URL).
			AnswerViaWrapper(ctx, sessionID, "allow", nil)
		Expect(verdict).To(Equal(DeliveryOK))
		Expect(reason).To(ContainSubstring("answered allow"))
	})

	It("retries when the gate changed under the decision", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(
				`{"question":{"request_id":"req-2","input":{"tool_name":"Read"}}}`))
		}))
		defer server.Close()
		expected := "req-1"
		verdict, reason := NewClaudeWrapperClient(server.URL).
			AnswerViaWrapper(ctx, sessionID, "allow", &expected)
		Expect(verdict).To(Equal(DeliveryRetry))
		Expect(reason).To(ContainSubstring("gate changed"))
	})

	It("reports resolved when the wrapper holds no question", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"state":"running"}`))
		}))
		defer server.Close()
		verdict, reason := NewClaudeWrapperClient(server.URL).
			AnswerViaWrapper(ctx, sessionID, "allow", nil)
		Expect(verdict).To(Equal(DeliveryResolved))
		Expect(reason).To(ContainSubstring("already resolved"))
	})

	It("refuses when the wrapper rejects the answer", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/1.0/sessions/"+sessionID {
				_, _ = w.Write([]byte(
					`{"question":{"request_id":"req-1","input":{"tool_name":"Read"}}}`))
				return
			}
			w.WriteHeader(http.StatusConflict)
		}))
		defer server.Close()
		verdict, _ := NewClaudeWrapperClient(server.URL).
			AnswerViaWrapper(ctx, sessionID, "allow", nil)
		Expect(verdict).To(Equal(DeliveryRefuse))
	})

	It("disables the arm for a non-loopback URL", func() {
		verdict, reason := NewClaudeWrapperClient("http://example.com").
			AnswerViaWrapper(ctx, sessionID, "allow", nil)
		Expect(verdict).To(Equal(DeliveryRefuse))
		Expect(reason).To(ContainSubstring("no wrapper configured"))
	})
})
