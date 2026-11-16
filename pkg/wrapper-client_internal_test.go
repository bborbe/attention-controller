// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
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

	Describe("DeliverMessage", func() {
		// messageDeliverer is the client seen through its delivery interface: the
		// two halves are separate interfaces, so a spec reaches the second one
		// explicitly rather than widening the first for every fake.
		messageDeliverer := func(rawURL string) WrapperMessageDeliverer {
			client, ok := NewClaudeWrapperClient(rawURL).(WrapperMessageDeliverer)
			Expect(ok).To(BeTrue())
			return client
		}

		It("posts the turn to the session's message route", func() {
			var path string
			var body map[string]string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					path = r.URL.Path
					Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
					w.WriteHeader(http.StatusAccepted)
				}),
			)
			defer server.Close()

			verdict, reason := messageDeliverer(server.URL).
				DeliverMessage(ctx, sessionID, "/workspace", "the answer")
			Expect(verdict).To(Equal(DeliveryOK))
			Expect(reason).To(ContainSubstring("message delivered"))
			Expect(path).To(Equal("/api/1.0/sessions/" + sessionID + "/message"))
			Expect(body).To(Equal(map[string]string{"text": "the answer"}))
		})

		It("resumes the session and sends the turn when the wrapper answers 404", func() {
			// ErrSessionNotFound: the wrapper holds no live child for the session,
			// so the answer is only deliverable after a resume — and the resume is
			// keyed by the RESUMED id, so it does not mint a second session.
			var resumes []map[string]string
			messages := 0
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/1.0/sessions" {
						var body map[string]string
						Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
						resumes = append(resumes, body)
						w.WriteHeader(http.StatusCreated)
						return
					}
					messages++
					if messages == 1 {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.WriteHeader(http.StatusAccepted)
				}),
			)
			defer server.Close()

			verdict, reason := messageDeliverer(server.URL).
				DeliverMessage(ctx, sessionID, "/workspace/attention-controller", "the answer")
			Expect(verdict).To(Equal(DeliveryOK))
			Expect(reason).To(ContainSubstring("resumed and delivered"))
			Expect(resumes).To(Equal([]map[string]string{{
				"resume": sessionID,
				"cwd":    "/workspace/attention-controller",
			}}))
			Expect(messages).To(Equal(2))
		})

		It("resumes when the wrapper answers 410 for an exited session", func() {
			messages := 0
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/1.0/sessions" {
						w.WriteHeader(http.StatusCreated)
						return
					}
					messages++
					if messages == 1 {
						w.WriteHeader(http.StatusGone)
						return
					}
					w.WriteHeader(http.StatusAccepted)
				}),
			)
			defer server.Close()

			verdict, _ := messageDeliverer(server.URL).
				DeliverMessage(ctx, sessionID, "/workspace", "the answer")
			Expect(verdict).To(Equal(DeliveryOK))
			Expect(messages).To(Equal(2))
		})

		It("sends the turn when the resume reports the session is already live", func() {
			// ErrSessionLive: something else brought it back between the failed
			// message and this resume. The session is there, so the turn goes in.
			messages := 0
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/1.0/sessions" {
						w.WriteHeader(http.StatusConflict)
						return
					}
					messages++
					if messages == 1 {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.WriteHeader(http.StatusAccepted)
				}),
			)
			defer server.Close()

			verdict, _ := messageDeliverer(server.URL).
				DeliverMessage(ctx, sessionID, "/workspace", "the answer")
			Expect(verdict).To(Equal(DeliveryOK))
		})

		It("retries when the wrapper cannot be reached", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusAccepted)
				}),
			)
			url := server.URL
			server.Close()

			verdict, reason := messageDeliverer(url).
				DeliverMessage(ctx, sessionID, "/workspace", "the answer")
			Expect(verdict).To(Equal(DeliveryRetry))
			Expect(reason).To(ContainSubstring("unreachable"))
		})

		It("retries rather than refusing when a sequence holds the session", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusConflict)
				}),
			)
			defer server.Close()

			verdict, reason := messageDeliverer(server.URL).
				DeliverMessage(ctx, sessionID, "/workspace", "the answer")
			Expect(verdict).To(Equal(DeliveryRetry))
			Expect(reason).To(ContainSubstring("409"))
		})

		It("refuses a non-UUID session id and an empty turn", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					Fail("the wrapper must not be called for an undeliverable request")
				}),
			)
			defer server.Close()

			verdict, _ := messageDeliverer(server.URL).
				DeliverMessage(ctx, "not-a-uuid", "/workspace", "the answer")
			Expect(verdict).To(Equal(DeliveryRefuse))

			verdict, _ = messageDeliverer(server.URL).
				DeliverMessage(ctx, sessionID, "/workspace", "  ")
			Expect(verdict).To(Equal(DeliveryRefuse))
		})
	})
})
