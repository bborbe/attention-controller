// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"

	"github.com/bborbe/attention-controller/pkg"
)

// boardItemsOf pairs every item with the render data the board shows for it.
//
// ⚠️ It builds each entry from newBoardRow — the same builder the SSE stream's
// change detection reads — so the board JSON and the stream derive the jump
// target, the navigation line, the uncovered marker and the timeline through one
// builder rather than two copies.
//
// Provenance and the delivery reports are resolved for the whole board at once:
// the host-wide reads behind them happen once per request, never once per row.
func boardItemsOf(
	ctx context.Context,
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	items pkg.Items,
	speakEnabled bool,
	vaultName string,
) []pkg.BoardItem {
	provenances := provenance.Resolve(ctx, items)
	reports := deliveryReportsOf(ctx, store, items)
	result := make([]pkg.BoardItem, 0, len(items))
	for _, item := range items {
		row := newBoardRow(
			item,
			provenances[item.ItemID],
			reports[item.ItemID],
			speakEnabled,
			vaultName,
		)
		result = append(result, pkg.BoardItem{Item: item, Board: newBoardView(row)})
	}
	return result
}

// newBoardView projects a row onto the fields the React card renders.
func newBoardView(row boardRow) pkg.BoardView {
	return pkg.BoardView{
		JumpURL:         row.JumpURL,
		JumpCommand:     row.Jump,
		NoJump:          row.NoJump,
		Uncovered:       row.Provenance.Uncovered,
		Provenance:      newBoardProvenance(row),
		Timeline:        row.Timeline(),
		DeliveryStatus:  string(row.DeliveryStatus),
		DeliveryOutcome: row.DeliveryOutcome(),
		Undeliverable:   row.Undeliverable(),
		SpeakEnabled:    row.Speak,
		Risk:            row.Risk,
		BlastRadius:     row.BlastRadius,
	}
}

// newBoardProvenance carries the navigation line and the info panel's machine
// identity, with the pane folded into one of three shapes: a
// validated pane, else the recorded pane marked unvalidated when the listing
// was unreadable, else `unroutable` when a pane was recorded at all.
func newBoardProvenance(row boardRow) pkg.BoardProvenance {
	p := row.Provenance
	result := pkg.BoardProvenance{
		TaskName:    p.TaskName,
		TaskHref:    row.TaskURL,
		GoalName:    p.GoalName,
		GoalHref:    row.GoalURL,
		TopicName:   p.TopicName,
		TopicHref:   row.TopicURL,
		SessionName: p.SessionName,
		Host:        p.Host,
		Cwd:         p.Cwd,
		Tool:        p.Tool,
	}
	switch {
	case p.Pane != "":
		result.Pane = p.Pane
	case p.PaneListingUnreadable:
		result.Pane = p.RecordedPane
		result.PaneUnvalidated = true
	case p.PaneRecorded:
		result.Unroutable = true
	}
	return result
}
