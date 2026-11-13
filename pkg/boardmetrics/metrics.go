// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package boardmetrics provides the Prometheus-backed implementation of the
// board's render metrics port.
package boardmetrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/attention-controller/pkg"
)

// NewMetrics creates the Prometheus-backed Metrics implementation and registers
// its counters on the given registry.
//
// It panics if a collector of the same name is already registered there:
// registration is a startup-time invariant, and a duplicate name means two
// packages claim the same series, which is a programming error rather than a
// condition to recover from.
//
// The process constructs it once on prometheus.DefaultRegisterer — the registry
// promhttp.Handler serves — while a spec constructs it on a private registry,
// so a spec reads its own counters back without touching the process-wide one.
func NewMetrics(registry prometheus.Registerer) pkg.Metrics {
	// ⚠️ Name carries the FULL frozen metric name with Namespace and Subsystem
	// left empty. prometheus.NewCounter builds the exposed name as
	// BuildFQName(Namespace, Subsystem, Name), so a Namespace here would expose
	// the counter under a different fully-qualified name and the deployed grep
	// for the frozen name would silently match nothing.
	renders := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "attention_board_renders_total",
		Help: "Total number of renders performed by the board's shared render path.",
	})
	rowsRendered := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "attention_board_rows_rendered_total",
		Help: "Total number of board rows produced by the board's shared render path.",
	})
	pageRequests := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "attention_board_page_requests_total",
		Help: "Total number of requests for the board page made by a client.",
	})
	// The watcher's cycle duration. A plain Histogram rather than a
	// HistogramVec: the cycle has no dimension worth splitting on, and a label
	// with one value would only add a series to keep in sync.
	//
	// The bucket edges are chosen around what a cycle actually costs — most of
	// a cycle is a vault scan, so the interesting resolution is between a
	// fraction of a second and a few minutes, not in the millisecond range a
	// request-duration histogram would need.
	watcherCycle := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "attention_watcher_cycle_seconds",
		Help: "Duration of one attention-watcher cycle in seconds, reported by the watcher over HTTP.",
		Buckets: []float64{
			0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300,
		},
	})
	registry.MustRegister(renders, rowsRendered, pageRequests, watcherCycle)
	return &boardMetrics{
		renders:      renders,
		rowsRendered: rowsRendered,
		pageRequests: pageRequests,
		watcherCycle: watcherCycle,
	}
}

type boardMetrics struct {
	renders      prometheus.Counter
	rowsRendered prometheus.Counter
	pageRequests prometheus.Counter
	watcherCycle prometheus.Histogram
}

// BoardRendersTotalCounterInc records that the shared renderer completed one
// render of the board.
func (m *boardMetrics) BoardRendersTotalCounterInc() {
	m.renders.Inc()
}

// BoardRowsRenderedTotalCounterAdd records that one render produced the given
// number of rows.
func (m *boardMetrics) BoardRowsRenderedTotalCounterAdd(rows int) {
	// prometheus.Counter.Add takes a float64, so the row count is converted.
	m.rowsRendered.Add(float64(rows))
}

// BoardPageRequestsTotalCounterInc records that one client asked for the board
// page.
func (m *boardMetrics) BoardPageRequestsTotalCounterInc() {
	m.pageRequests.Inc()
}

// WatcherCycleSecondsHistogramObserve records one watcher cycle's duration.
func (m *boardMetrics) WatcherCycleSecondsHistogramObserve(seconds float64) {
	m.watcherCycle.Observe(seconds)
}
