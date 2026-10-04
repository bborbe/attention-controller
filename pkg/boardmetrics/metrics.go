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
// both counters on the given registry.
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
	registry.MustRegister(renders, rowsRendered)
	return &boardMetrics{
		renders:      renders,
		rowsRendered: rowsRendered,
	}
}

type boardMetrics struct {
	renders      prometheus.Counter
	rowsRendered prometheus.Counter
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
