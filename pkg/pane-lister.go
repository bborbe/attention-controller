// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// Pane is one WezTerm pane, reduced to the fields this package actually reads.
// The listing carries seventeen fields; nothing else here is read.
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
	// TTYName is the pane's tty device, e.g. `/dev/ttys004`. ⚠️ It is read for
	// one reason: the OSC 1337 escape that activates a pane across OS windows
	// must be written to the pane's OWN tty, so the PaneActivator cannot work
	// without it. It is a device path, not a credential and not a title — never
	// render it on a card.
	TTYName string `json:"tty_name"`
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

// paneListingTimeout bounds the `wezterm cli list` subprocess.
//
// ⚠️ Without it this call has NO bound at all. The ctx handed in is the
// caller's — a long-lived stream context, or an HTTP request that carries no
// deadline — so exec.CommandContext has nothing to fire on, and a WezTerm mux
// that stops answering leaves the child running and the caller parked in
// wait4 for as long as the process lives. Measured 2026-10-03: a wedged mux
// held one child for over ten minutes; because the resolver held its mutex
// across this call, every Resolve in the process queued behind it, and the
// board page timed out at 25 s while /healthz and the API — which do not call
// Resolve — answered in 1 ms and 20 ms.
//
// Three seconds is two orders of magnitude of headroom: the listing is a local
// IPC round trip measuring ~26 ms by hand and ~54 ms on a healthy mux. Anything
// slower is a mux that is not going to answer, and the correct rendering for an
// unreadable probe is no pane claim at all — never a stalled request.
const paneListingTimeout = 3 * time.Second

// paneListingWaitDelay bounds how long the listing waits for the child's output
// pipes to close AFTER the process itself has been killed.
//
// ⚠️ It exists because paneListingTimeout alone bounds the PROCESS and not the
// CALL. exec.CommandContext kills the direct child when the deadline fires, but
// Output reads that child's stdout to EOF, and EOF requires every holder of the
// write end to close it — including any process the child spawned. A child that
// forks and exits leaves the descendant holding the pipe: the kill lands, the
// read does not return, and the caller is still parked. Measured 2026-10-03 by
// the spec in pane-lister_test.go, which is exactly that shape — a `sh` wrapper
// around `sleep 30` blocked Output for the full 30 s under a 3 s bound.
//
// One second is generous for closing a pipe nothing else holds, and the worst
// case stays bounded either way: at most paneListingTimeout plus this.
const paneListingWaitDelay = 1 * time.Second

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
	// ⚠️ Bounded here rather than trusting the caller's ctx, which is routinely
	// deadline-free — see paneListingTimeout. This is the line that turns a
	// wedged mux from a process-wide outage into one absent pane column.
	ctx, cancel := context.WithTimeout(ctx, paneListingTimeout)
	defer cancel()
	// #nosec G204 -- the reported risk is "subprocess launched with a variable",
	// and the variable is the point: `binary` is resolved by resolveWezterm from
	// the fixed, compile-time `weztermBinaryCandidates` list via exec.LookPath,
	// so neither a caller nor any request input can reach it. Keeping a literal
	// here is impossible without giving up the fallback that makes the CLI
	// findable under launchd, which is the defect this resolution exists to fix.
	// This records the provenance; it does not waive a risk.
	//
	// ⚠️ Two ways to silently re-arm the check above, both hit while writing this
	// note, and neither is visible in the diff — a passing build is the only way
	// to tell. First, the marker must be the FIRST line of the block directly
	// above the call: a statement inserted between block and call splits them,
	// which is what moving the timeout above originally did. Second, the marker's
	// own spelling must appear nowhere else in the block — a prose sentence that
	// named it, as an earlier draft of this note did, stops it applying. Naming
	// the rule id instead, as this sentence does, is harmless; the marker is the
	// thing that must not repeat. Both failures compile cleanly and fail only
	// `make precommit`.
	cmd := exec.CommandContext(ctx, binary, "cli", "list", "--format", "json")
	// ⚠️ Set here rather than left to the default, and the spec below is why: see
	// paneListingWaitDelay. Without it the deadline bounds the process and not the
	// call, so a child that forks still parks the caller for as long as its
	// descendant lives — the exact stall this fix exists to remove.
	cmd.WaitDelay = paneListingWaitDelay
	raw, err := cmd.Output()
	if err != nil {
		// Logged, not just returned. This boundary call is the one whose
		// failure is hardest to see from outside: when WezTerm is missing the
		// page simply renders no pane, which is indistinguishable from a host
		// that has none. Measured 2026-09-22 — the launchd PATH gap behind the
		// first deployment of this feature was found only by probing the
		// environment by hand, because the store's own log said nothing.
		glog.V(2).Infof("wezterm cli list failed: binary=%s err=%v", binary, err)
		return nil, errors.Wrap(ctx, err, "list wezterm panes failed")
	}
	var panes []Pane
	if err := json.Unmarshal(raw, &panes); err != nil {
		glog.V(2).
			Infof("wezterm cli list parse failed: binary=%s bytes=%d err=%v", binary, len(raw), err)
		return nil, errors.Wrap(ctx, err, "parse wezterm pane listing failed")
	}
	byID := make(map[int]Pane, len(panes))
	for _, pane := range panes {
		byID[pane.PaneID] = pane
	}
	glog.V(2).Infof("wezterm cli list: binary=%s panes=%d", binary, len(byID))
	return byID, nil
}
