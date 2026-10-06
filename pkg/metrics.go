// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

//counterfeiter:generate -o ../mocks/metrics.go --fake-name Metrics . Metrics

// Metrics counts what the board's shared render path does, so the fan-out
// ratio can be read off the deployed binary's /metrics rather than only
// proven by a spec.
//
// It is injected rather than reached for: the handler's constructor takes
// one, so a spec can build the counters on its own registry and read them
// back without touching the process-wide default registry the running
// service serves.
type Metrics interface {
	// BoardRendersTotalCounterInc records that the shared renderer completed
	// one render of the board. It is called from the shared renderer, never
	// from a per-client path, so N attached streams and M store changes move
	// it by M and not by N×M.
	BoardRendersTotalCounterInc()

	// BoardRowsRenderedTotalCounterAdd records that one render produced the
	// given number of rows, so the rows total is the render count multiplied
	// by the board's size.
	BoardRowsRenderedTotalCounterAdd(rows int)

	// BoardPageRequestsTotalCounterInc records that one client asked for the
	// board page. It is called from the per-client page path, so it moves once
	// per request a client makes — which is exactly the reading
	// BoardRendersTotalCounterInc cannot provide, because that one is
	// incremented only inside the shared renderer and so does not move when a
	// single client loads the page.
	BoardPageRequestsTotalCounterInc()
}
