// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"

	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/attention-controller/pkg"
)

// registerCompactTrio adds the compact trio job to the runner.
//
// ⚠️ It lives here rather than inline in `addAttentionWatcherJobs` for a
// mechanical reason with a real cost: that function sits at the linter's length
// bound and `main.go` at the file-length limit, so a registration added inline
// fails the build's own lint rather than the review. The rationale belongs
// beside the job in `pkg/compact-trio.go`; this file only wires it.
//
// It shares the delivery arm's carrier and writer, so a send inherits the same
// tty re-proof, composer gate and separate text/Enter.
func registerCompactTrio(
	ctx context.Context,
	runner run.ConcurrentRunner,
	store pkg.AttentionStore,
	carrier pkg.PaneCarrierAndLocator,
	writer pkg.PaneTextWriter,
	transcriptRoot string,
) {
	runner.Add(ctx, pkg.NewCompactTrioJob(
		store,
		carrier,
		writer,
		pkg.NewTranscriptReader(transcriptRoot),
		pkg.CompactTrioInterval,
		libtime.NewCurrentDateTime(),
	).Run)
}
