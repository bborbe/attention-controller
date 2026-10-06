// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// PaneActivator brings a WezTerm pane forward: it activates the pane inside
// WezTerm and raises the OS window.
//
// ⚠️ It exists so the store performs the jump **in-process**. Before this, the
// board's Jump button reached a Python server on another port, which shelled
// out to `jump.py`; the store owned neither the process nor its failure modes.
// The capability is one interface rather than a subprocess call buried in a
// handler, which is also what makes it testable without a live WezTerm.
//
//counterfeiter:generate -o ../mocks/pane-activator.go --fake-name PaneActivator . PaneActivator
type PaneActivator interface {
	// Activate brings pane forward. It returns an error when the pane cannot be
	// resolved or activated, so a caller reports a failed jump rather than a
	// silent no-op.
	//
	// ⚠️ The pane id is a lease, not an identifier — WezTerm renumbers and reuses
	// it across tab moves and restarts — so it is resolved against the LIVE pane
	// list here rather than trusted. A pane that does not resolve activates
	// nothing and says so.
	Activate(ctx context.Context, pane string) error
}

// NewWeztermPaneActivator creates the activator the board's Jump button and the
// legacy pane-addressed route both go through.
//
// The lister is injected rather than created here: the pane list is already a
// subprocess against a terminal multiplexer with its own failure semantics (see
// PaneLister), and a second lookup path would be a second place for those
// semantics to drift.
func NewWeztermPaneActivator(panes PaneLister) PaneActivator {
	return &weztermPaneActivator{panes: panes}
}

type weztermPaneActivator struct {
	panes PaneLister
}

// ⚠️ Every subprocess and every write below carries a bound. The Python this
// replaces had THREE untimed `subprocess.call` sites, and the only ceiling on a
// jump was the caller's — which is how a jump came to be in flight when the
// client gave up, producing an empty response the operator read as a hang.
// An unbounded call here would reproduce exactly that.
const (
	// activateTimeout bounds the whole activation, so no single jump can outlive
	// the request that asked for it.
	activateTimeout = 5 * time.Second
	// raiseVarName is the WezTerm user-var whose change fires the hook in
	// ~/.config/wezterm/wezterm.lua, which runs pane:activate() + window:focus()
	// with the real window object in hand.
	raiseVarName = "raise"
)

func (a *weztermPaneActivator) Activate(ctx context.Context, pane string) error {
	ctx, cancel := context.WithTimeout(ctx, activateTimeout)
	defer cancel()

	paneID, err := strconv.Atoi(pane)
	if err != nil {
		return errors.Wrapf(ctx, err, "pane %q is not a pane id", pane)
	}
	panes, err := a.panes.List(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "list panes failed")
	}
	target, ok := panes[paneID]
	if !ok {
		// A pane id is renumbered by a WezTerm restart, so a stale one is refused
		// rather than handed to the terminal.
		return errors.Errorf(ctx, "pane %d is not a live pane", paneID)
	}

	raised := a.raise(ctx, target)
	if !raised {
		// The escape could not be written — no tty, or the write failed. Fall
		// back to WezTerm's own activation, which is same-window only: it cannot
		// cross OS windows, which is why the escape is the primary path.
		if err := a.activatePane(ctx, pane); err != nil {
			return err
		}
	}
	// ⚠️ The app activation is best-effort and must NOT turn a written escape
	// into a reported failure: the pane is already active inside WezTerm by this
	// point, and `window:focus()` does not reliably make WezTerm the frontmost
	// macOS app — measured, `open -a` is what does that. A failure here degrades
	// to "focused inside WezTerm", never to "the jump failed".
	a.raiseApp(ctx)
	return nil
}

// raise writes the OSC 1337 SetUserVar escape to the pane's tty.
//
// ⚠️ The raise happens INSIDE WezTerm rather than through `wezterm cli`: that
// CLI cannot cross OS windows, and macOS Accessibility cannot focus WezTerm's
// windows at all (AXRaise / AXMain / AXFocused / `activate` all measured as
// no-ops). Writing the escape fires the user-var hook in wezterm.lua, which has
// the real window object and can call window:focus().
//
// Returns false when the escape was not written; the caller falls back.
func (a *weztermPaneActivator) raise(ctx context.Context, target Pane) bool {
	if target.TTYName == "" {
		return false
	}
	// ⚠️ The tty write is the one unbounded risk here that is not a subprocess:
	// a write to a tty can block. It is bounded explicitly rather than assumed
	// to return, because an unbounded write is the shape this port exists to
	// remove.
	payload := base64.StdEncoding.EncodeToString([]byte("1"))
	escape := "\033]1337;SetUserVar=" + raiseVarName + "=" + payload + "\007"

	done := make(chan error, 1)
	go func() {
		// #nosec G304 -- the path is the tty_name of a pane returned by the LIVE
		// wezterm pane list, not request input: the pane id is resolved against
		// that list before this runs, so neither a caller nor a request can reach
		// this path. It is a device file the terminal itself named.
		fh, err := os.OpenFile(target.TTYName, os.O_WRONLY, 0)
		if err != nil {
			done <- err
			return
		}
		defer fh.Close()
		_, err = fh.WriteString(escape)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			// The tty path is safe to report; it carries no credential.
			glog.V(2).Infof("raise pane %d failed: tty write error: %v", target.PaneID, err)
			return false
		}
		glog.V(2).Infof("raise pane %d: escape written", target.PaneID)
		return true
	case <-ctx.Done():
		glog.V(2).Infof("raise pane %d failed: tty write timed out", target.PaneID)
		return false
	}
}

// activatePane is the fallback: WezTerm's own activation, same-window only.
//
// ⚠️ `wezterm` is looked up through the same resolution the pane lister uses,
// because the macOS app bundle is deliberately NOT on the launchd job's PATH —
// a PATH-only lookup succeeds in an interactive shell and fails in the deployed
// configuration, which is the worst of both.
func (a *weztermPaneActivator) activatePane(ctx context.Context, pane string) error {
	binary, err := resolveWezterm(ctx)
	if err != nil {
		return err
	}
	// #nosec G204 -- the reported risk is "subprocess launched with a variable",
	// and `binary` is resolved by resolveWezterm from the fixed, compile-time
	// weztermBinaryCandidates list via exec.LookPath, so neither a caller nor any
	// request input can reach it. Same provenance as the pane lister's own call.
	if err := exec.CommandContext(ctx, binary, "cli", "--no-auto-start", "activate-pane", "--pane-id", pane).Run(); err != nil {
		glog.V(2).Infof("activate pane %s failed: %v", pane, err)
		return errors.Wrap(ctx, err, "activate pane failed")
	}
	glog.V(2).Infof("activate pane %s: wezterm cli activate-pane ok", pane)
	return nil
}

// raiseApp makes WezTerm the frontmost macOS app.
//
// ⚠️ It is not decoration. The escape's window:focus() picks the window inside
// WezTerm but does not reliably make WezTerm frontmost: measured 2026-09-27,
// four direct calls with Telegram frontmost twice left Telegram frontmost.
// Bounded and best-effort — see the caller.
//
// ⚠️ The binary is a literal, not a variable, so gosec G204 does not fire and no
// suppression is needed.
func (a *weztermPaneActivator) raiseApp(ctx context.Context) {
	// #nosec G204 -- the command and every argument are compile-time literals.
	if err := exec.CommandContext(ctx, "open", "-a", "WezTerm").Run(); err != nil {
		glog.V(2).Infof("open -a WezTerm failed (best-effort): %v", err)
	}
}
