// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package attnlogpanes reads `wezterm cli list` for the attention-log hook's
// owned-pane check. It lives outside `pkg/attnlog` so the hook's hot path and
// the writer package carry no subprocess dependency; the lister is consulted
// only when an item opens.
package attnlogpanes

import (
	"bytes"
	"encoding/json"
	stderrors "errors"
	"os/exec"
	"time"

	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg/attnlog"
)

const (
	// listingTimeout bounds the `wezterm cli list` subprocess.
	listingTimeout = 2 * time.Second
)

// binaryCandidates are where the WezTerm CLI is looked for, in order.
var binaryCandidates = []string{
	"wezterm",
	"/Applications/WezTerm.app/Contents/MacOS/wezterm",
}

// New creates a pane lister backed by `wezterm cli list --format json`.
func New() attnlog.PaneLister {
	return &lister{}
}

type lister struct{}

// ListPanes returns pane id -> title, or an error when the listing could not be
// read. An unreadable listing is not an empty one: the caller keeps the pane
// rather than eliminating it on a question it never got answered.
func (l *lister) ListPanes() (map[int]string, error) {
	binary, err := resolveBinary()
	if err != nil {
		return nil, err
	}
	// #nosec G204 -- the variable is the point: `binary` is resolved from the
	// fixed, compile-time `binaryCandidates` list via exec.LookPath, so no
	// caller or payload input can reach it.
	command := exec.Command(binary, "cli", "--no-auto-start", "list", "--format", "json")
	var output bytes.Buffer
	command.Stdout = &output
	if err := command.Start(); err != nil {
		return nil, stderrors.New("start wezterm cli list: " + err.Error())
	}
	timer := time.AfterFunc(listingTimeout, func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	defer timer.Stop()
	if err := command.Wait(); err != nil {
		glog.V(2).Infof("wezterm cli list failed: binary=%s err=%v", binary, err)
		return nil, stderrors.New("run wezterm cli list: " + err.Error())
	}
	return parseListing(output.Bytes())
}

// resolveBinary returns the first candidate that resolves to an executable.
func resolveBinary() (string, error) {
	for _, candidate := range binaryCandidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", stderrors.New("wezterm not found on PATH or in the macOS app bundle")
}

// parseListing indexes the listing by pane id.
func parseListing(raw []byte) (map[int]string, error) {
	var panes []struct {
		PaneID int    `json:"pane_id"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(raw, &panes); err != nil {
		return nil, stderrors.New("parse wezterm pane listing: " + err.Error())
	}
	titles := make(map[int]string, len(panes))
	for _, pane := range panes {
		titles[pane.PaneID] = pane.Title
	}
	return titles, nil
}
