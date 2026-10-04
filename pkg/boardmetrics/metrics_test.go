// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package boardmetrics_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
)

// metricFamily returns the gathered family with the given name, failing the
// spec when the registry does not expose it.
func metricFamily(registry *prometheus.Registry, name string) *dto.MetricFamily {
	families, err := registry.Gather()
	Expect(err).Should(BeNil())
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	return nil
}

// counterValue reads a single-series counter family's value.
func counterValue(registry *prometheus.Registry, name string) float64 {
	family := metricFamily(registry, name)
	Expect(family).ShouldNot(BeNil())
	return family.GetMetric()[0].GetCounter().GetValue()
}

var _ = Describe("Metrics", func() {
	var (
		registry *prometheus.Registry
		metrics  pkg.Metrics
	)

	BeforeEach(func() {
		// ⚠️ A private registry, never prometheus.DefaultRegisterer: MustRegister
		// panics on the second registration of the same collector, so a spec
		// reusing the default registry would panic once anything else had
		// registered these names.
		registry = prometheus.NewRegistry()
		metrics = boardmetrics.NewMetrics(registry)
	})

	It("records renders and rows on the registry it was given", func() {
		metrics.BoardRendersTotalCounterInc()
		metrics.BoardRendersTotalCounterInc()
		metrics.BoardRendersTotalCounterInc()
		metrics.BoardRowsRenderedTotalCounterAdd(7)

		Expect(counterValue(registry, "attention_board_renders_total")).To(Equal(3.0))
		Expect(counterValue(registry, "attention_board_rows_rendered_total")).To(Equal(7.0))
	})

	It("gives each counter a distinct non-empty description", func() {
		rendersHelp := metricFamily(registry, "attention_board_renders_total").GetHelp()
		rowsHelp := metricFamily(registry, "attention_board_rows_rendered_total").GetHelp()

		Expect(rendersHelp).ShouldNot(BeEmpty())
		Expect(rowsHelp).ShouldNot(BeEmpty())
		Expect(rendersHelp).ShouldNot(Equal(rowsHelp))
	})
})
