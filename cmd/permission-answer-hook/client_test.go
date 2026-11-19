// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("TestStoreClientHTTPContract", func() {
	const itemID = pkg.ItemID("item-1")

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("TestStoreClientHTTPContract lists via GET /api/1.0/attention", func() {
		var method, path string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			_, _ = w.Write([]byte(`[{"item_id":"item-1","state":"open"}]`))
		}))
		defer server.Close()

		items, err := newHTTPStoreClient(server.URL).List(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(method).To(Equal(http.MethodGet))
		Expect(path).To(Equal("/api/1.0/attention"))
		Expect(items).To(HaveLen(1))
		Expect(items[0].ItemID).To(Equal(itemID))
	})

	It("TestStoreClientHTTPContract gets one item via GET /api/1.0/attention/{id}", func() {
		var method, path string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			_, _ = w.Write([]byte(`{"item_id":"item-1","state":"answered"}`))
		}))
		defer server.Close()

		item, err := newHTTPStoreClient(server.URL).Get(ctx, itemID)
		Expect(err).ToNot(HaveOccurred())
		Expect(method).To(Equal(http.MethodGet))
		Expect(path).To(Equal("/api/1.0/attention/item-1"))
		Expect(item).ToNot(BeNil())
		Expect(item.State).To(Equal(pkg.AnsweredState))
	})

	It("TestStoreClientHTTPContract wraps pkg.ErrItemNotFound on a 404", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		_, err := newHTTPStoreClient(server.URL).Get(ctx, itemID)
		Expect(err).To(HaveOccurred())
		Expect(stderrors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())
	})

	It("TestStoreClientHTTPContract records an attempt via POST with carrier and outcome", func() {
		var method, path string
		var body map[string]string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		err := newHTTPStoreClient(server.URL).
			RecordAttempt(ctx, itemID, attemptCarrier, pkg.DeliveredOutcome)
		Expect(err).ToNot(HaveOccurred())
		Expect(method).To(Equal(http.MethodPost))
		Expect(path).To(Equal("/api/1.0/attention/item-1/attempt"))
		Expect(body).To(Equal(map[string]string{
			"carrier": "supervisor:permission-answer-poll",
			"outcome": "delivered",
		}))
	})

	It("TestStoreClientHTTPContract reports an unexpected status as an error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		_, err := newHTTPStoreClient(server.URL).List(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("500"))
	})
})

var _ = Describe("TestBoardURLMustBeLoopback", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("TestBoardURLMustBeLoopback disables a non-loopback base URL", func() {
		Expect(normaliseBoardURL("http://example.com")).To(BeEmpty())
		Expect(normaliseBoardURL("https://10.0.0.5:18080")).To(BeEmpty())
		Expect(normaliseBoardURL("not a url")).To(BeEmpty())
	})

	It("TestBoardURLMustBeLoopback keeps a loopback base URL", func() {
		Expect(normaliseBoardURL("http://127.0.0.1:18080")).To(Equal("http://127.0.0.1:18080"))
		Expect(normaliseBoardURL("http://localhost:18080")).To(Equal("http://localhost:18080"))
	})

	It("TestBoardURLMustBeLoopback makes no request for a remote host", func() {
		var hits int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(`[]`))
		}))
		defer server.Close()

		// Positive control: a loopback URL reaches the server, so the difference
		// below is the host check and not a broken client.
		_, err := newHTTPStoreClient(server.URL).List(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(atomic.LoadInt32(&hits)).To(Equal(int32(1)))

		// A remote host is disabled before any request is built.
		_, err = newHTTPStoreClient("http://example.com").List(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no board configured"))
		Expect(atomic.LoadInt32(&hits)).To(Equal(int32(1)))
	})
})
