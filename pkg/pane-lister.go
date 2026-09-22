// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/bborbe/errors"
)

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

//counterfeiter:generate -o ../mocks/pane-lister.go --fake-name PaneLister . PaneLister

// PaneLister lists the WezTerm panes on this host.
//
// It is an interface because the listing is a subprocess against a terminal
// multiplexer, and the store must not require one: a store running for k8s
// agents, cron jobs or dark-factory runs has no WezTerm at all. On such a host
// the listing cannot be read, the error is surfaced rather than swallowed, and
// every pane then renders **absent** — no pane claim is made at all, which is
// the correct rendering for a value that cannot be resolved.
type PaneLister interface {
	// List returns the panes keyed by pane id, or an error when the listing
	// could not be read at all.
	//
	// ⚠️ The error is not decoration and must not be flattened into an empty
	// map. "No panes" and "could not ask" are different facts, and only the
	// first one licenses a claim about a pane: an empty listing read
	// successfully proves a recorded pane is gone, while a failed read proves
	// nothing. Collapsing them renders `unroutable` — which asserts the pane
	// does not resolve to this session — on a host where the question was never
	// answerable. That is the same class of error as presenting an unresolvable
	// value as resolved, with the sign flipped, and the store's own liveness
	// checker already refuses it: an unreadable registry reads as live.
	List(ctx context.Context) (map[int]Pane, error)
}

// NewWeztermPaneLister creates a lister reading `wezterm cli list`.
func NewWeztermPaneLister() PaneLister {
	return &weztermPaneLister{}
}

type weztermPaneLister struct{}

// weztermBinaryCandidates are where the WezTerm CLI is looked for, in order.
//
// PATH comes first: it is the portable answer, and the one a Linux or Homebrew
// install resolves through. The macOS app bundle is the fallback, and it is
// load-bearing rather than cosmetic — measured 2026-09-22, WezTerm installs
// there on this fleet and that directory is deliberately **not** on the launchd
// job's PATH, so a PATH-only lookup fails on every request in the deployed
// configuration while succeeding in an interactive shell.
//
// Resolving this in the binary rather than by editing the plist is deliberate:
// the plist lives outside every repo, so a PATH fix there is unversioned,
// unreviewed and untested, and it needs a launchd reload to take effect. A
// fallback here is reviewed, tested, and ships with the release.
var weztermBinaryCandidates = []string{
	"wezterm",
	"/Applications/WezTerm.app/Contents/MacOS/wezterm",
}

// resolveWezterm returns the first candidate that resolves to an executable.
func resolveWezterm(ctx context.Context) (string, error) {
	for _, candidate := range weztermBinaryCandidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New(ctx, "wezterm not found on PATH or in the macOS app bundle")
}

// List runs `wezterm cli list --format json` and indexes the result by pane id.
//
// Every failure — wezterm absent, not running, non-zero exit, malformed JSON,
// timeout — returns an error rather than an empty map, because an unreadable
// probe must not be read as a negative answer: reporting "no panes" would mark
// every row `unroutable` the moment the store ran somewhere WezTerm is absent,
// which is a claim the store cannot support.
func (w *weztermPaneLister) List(ctx context.Context) (map[int]Pane, error) {
	binary, err := resolveWezterm(ctx)
	if err != nil {
		return nil, err
	}
	// #nosec G204 -- the reported risk is "subprocess launched with a variable",
	// and the variable is the point: `binary` is resolved by resolveWezterm from
	// the fixed, compile-time `weztermBinaryCandidates` list via exec.LookPath,
	// so neither a caller nor any request input can reach it. Keeping a literal
	// here is impossible without giving up the fallback that makes the CLI
	// findable under launchd, which is the defect this resolution exists to fix.
	// This records the provenance; it does not waive a risk.
	raw, err := exec.CommandContext(ctx, binary, "cli", "list", "--format", "json").Output()
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list wezterm panes failed")
	}
	var panes []Pane
	if err := json.Unmarshal(raw, &panes); err != nil {
		return nil, errors.Wrap(ctx, err, "parse wezterm pane listing failed")
	}
	byID := make(map[int]Pane, len(panes))
	for _, pane := range panes {
		byID[pane.PaneID] = pane
	}
	return byID, nil
}
