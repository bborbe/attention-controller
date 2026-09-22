// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/golang/glog"
)

//counterfeiter:generate -o ../mocks/pane-lister.go --fake-name PaneLister . PaneLister

// Pane is one WezTerm pane, reduced to the two fields the ownership check
// needs. The listing carries seventeen fields; nothing else here is read.
type Pane struct {
	// PaneID is WezTerm's pane number. It is an integer in the listing, and it
	// is a lease rather than an identifier — WezTerm renumbers and reuses it
	// across tab moves and restarts, which is why existence alone proves
	// nothing and Title is the field that does the work.
	PaneID int `json:"pane_id"`
	// Title is the pane's title, which Claude Code prefixes with a status glyph
	// (`✳ ◐ ◑ ◒ ◓ ⠿ …`). It is compared against a session's name after that
	// glyph is stripped from both sides.
	Title string `json:"title"`
}

// PaneLister lists the WezTerm panes on this host.
//
// It is an interface because the listing is a subprocess against a terminal
// multiplexer, and the store must not require one: a store running for k8s
// agents, cron jobs or dark-factory runs has no WezTerm at all. Behind the
// interface, a host with no WezTerm returns an empty listing rather than an
// error, and every pane then renders absent — which is the correct rendering
// for a value that cannot be resolved.
type PaneLister interface {
	// List returns the panes keyed by pane id. An empty map means no panes
	// could be listed, which is indistinguishable from a host with none —
	// deliberately, because both cases resolve to the same rendering.
	List(ctx context.Context) map[int]Pane
}

// NewWeztermPaneLister creates a lister reading `wezterm cli list`.
func NewWeztermPaneLister() PaneLister {
	return &weztermPaneLister{}
}

type weztermPaneLister struct{}

// List runs `wezterm cli list --format json` and indexes the result by pane id.
//
// Every failure — wezterm absent, not running, non-zero exit, malformed JSON,
// timeout — returns an empty map and logs, never an error. A pane listing that
// cannot be read cannot prove a pane is gone, and the store's own liveness rule
// takes the same position for the same reason: an unreadable probe must not be
// read as a negative answer, because doing so would strip provenance from every
// row the moment the store ran somewhere WezTerm is absent.
func (w *weztermPaneLister) List(ctx context.Context) map[int]Pane {
	raw, err := exec.CommandContext(ctx, "wezterm", "cli", "list", "--format", "json").Output()
	if err != nil {
		glog.V(2).Infof("list wezterm panes failed: %v", err)
		return map[int]Pane{}
	}
	var panes []Pane
	if err := json.Unmarshal(raw, &panes); err != nil {
		glog.V(2).Infof("parse wezterm pane listing failed: %v", err)
		return map[int]Pane{}
	}
	byID := make(map[int]Pane, len(panes))
	for _, pane := range panes {
		byID[pane.PaneID] = pane
	}
	return byID
}
