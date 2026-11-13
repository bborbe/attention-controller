// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/attention-controller/pkg/boardmetrics"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("AttentionWatcherCycleHandler", func() {
	var (
		registry *prometheus.Registry
		subject  http.Handler
	)

	BeforeEach(func() {
		registry = prometheus.NewRegistry()
		subject = handler.NewAttentionWatcherCycleHandler(
			boardmetrics.NewMetrics(registry),
		)
	})

	post := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		subject.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)),
		)
		return recorder
	}

	// cycleHistogram returns the watcher cycle family, failing when absent.
	cycleHistogram := func() *dto.Histogram {
		families, err := registry.Gather()
		Expect(err).Should(BeNil())
		for _, family := range families {
			if family.GetName() == "attention_watcher_cycle_seconds" {
				return family.GetMetric()[0].GetHistogram()
			}
		}
		Fail("attention_watcher_cycle_seconds is not registered")
		return nil
	}

	It("observes the reported cycle duration", func() {
		Expect(post(`{"seconds":2.5}`).Code).To(Equal(http.StatusNoContent))

		histogram := cycleHistogram()
		Expect(histogram.GetSampleCount()).To(Equal(uint64(1)))
		Expect(histogram.GetSampleSum()).To(BeNumerically("~", 2.5, 1e-9))
	})

	It("accumulates one observation per call", func() {
		Expect(post(`{"seconds":1}`).Code).To(Equal(http.StatusNoContent))
		Expect(post(`{"seconds":3}`).Code).To(Equal(http.StatusNoContent))

		histogram := cycleHistogram()
		Expect(histogram.GetSampleCount()).To(Equal(uint64(2)))
		Expect(histogram.GetSampleSum()).To(BeNumerically("~", 4.0, 1e-9))
	})

	It("rejects a missing seconds field instead of observing zero", func() {
		// A zero observed here would be a real, fast point on a chart whose
		// purpose is to show a slow cycle — a wrong shape must not become a
		// measurement.
		Expect(post(`{}`).Code).To(Equal(http.StatusBadRequest))
		Expect(cycleHistogram().GetSampleCount()).To(Equal(uint64(0)))
	})

	It("rejects a negative duration", func() {
		Expect(post(`{"seconds":-1}`).Code).To(Equal(http.StatusBadRequest))
		Expect(cycleHistogram().GetSampleCount()).To(Equal(uint64(0)))
	})

	It("rejects a body that is not JSON", func() {
		Expect(post(`not json`).Code).To(Equal(http.StatusBadRequest))
		Expect(cycleHistogram().GetSampleCount()).To(Equal(uint64(0)))
	})
})
