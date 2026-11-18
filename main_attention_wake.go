// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"

	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/attention-controller/pkg"
)

// addManagerWakeJob registers the in-process manager-wake job.
//
// ⚠️ It lives in its own file, and the placement is load-bearing rather than
// tidy: the arm sits beside the delivery arms because it is the SECOND writer
// on the same books (the pane carrier the delivery arm already types through),
// and it is registered on the board router's host only — panes, the session
// registry and the ledger exist nowhere else, so the cluster listener has
// nothing to wake.
//
// It is extracted from `addAttentionWatcherJobs` because that function had
// reached the repo's `funlen` bound; the extraction is a move, not a change in
// what is registered or in what order.
func (a *application) addManagerWakeJob(
	ctx context.Context,
	runner run.ConcurrentRunner,
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	carrier pkg.PaneCarrierAndLocator,
	sessionLedger pkg.SessionLedgerStore,
	notifier pkg.AttentionChangeNotifier,
) {
	runner.Add(ctx, pkg.NewManagerWakeJob(
		store,
		sessionLedger,
		provenance,
		carrier,
		notifier,
		libtime.NewCurrentDateTime(),
		pkg.NewManagerWakeEmissionLog(),
	).Run)
}
