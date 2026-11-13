// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// ComposerState is what a pane's input line currently holds.
//
// The composer is the LAST glyph line: echoed prompts above it carry the same
// glyph. Only an empty composer may be typed into — a line holding text is the
// operator's own half-typed input, and Enter would submit it.
type ComposerState string

const (
	// ComposerEmpty is an input line with nothing after the prompt glyph.
	ComposerEmpty ComposerState = "empty"
	// ComposerBusy is an input line holding text.
	ComposerBusy ComposerState = "busy"
	// ComposerUnknown is a pane that could not be read, or that draws no
	// composer. ⚠️ It is deliberately NOT collapsed into `empty`: treating an
	// unreadable composer as empty is what types into a pane whose state was
	// never seen.
	ComposerUnknown ComposerState = "unknown"
)

// composerPromptGlyph is the prompt marker that opens the composer line.
//
// It is the `❯` character. Echoed prompts above the composer carry the same
// glyph, which is why the state is read from the LAST matching line rather than
// the first.
const composerPromptGlyph = "❯"

// paneTextTimeout bounds one `wezterm get-text` / `send-text` call.
//
// ⚠️ Without it the call inherits the caller's context, which is routinely a
// long-lived stream or a delivery sweep with no deadline — and `send-text` into
// a busy or wedged pane is the call that can eat a whole sweep slice on its
// own. Measured 2026-11-13: `scan_context_crossings=18.319s` inside a 6 s
// budget, the cost being iterations rather than one call.
const paneTextTimeout = 2 * time.Second

// PaneTextWriter extends PaneActivator with the TEXT half of the pane
// capability: reading a pane's visible text and typing into it.
//
// ⚠️ It EXTENDS the existing PaneActivator rather than replacing it, because
// activation already exists on master and the watcher port only needs the text
// half. A second pane abstraction would be a second place for the tty and
// pane-id semantics to drift.
type PaneTextWriter interface {
	PaneActivator

	// SendText writes text into the pane WITHOUT submitting it. Enter is sent
	// separately, and only once the text is drawn — sent as one burst the TUI
	// swallowed the `\r` and left the answer sitting unsubmitted (measured
	// 2026-09-29, pane 88).
	SendText(ctx context.Context, pane int, text string) error

	// ComposerState reports what the pane's input line holds.
	ComposerState(ctx context.Context, pane int) ComposerState

	// Text returns the pane's visible text, and whether it could be read.
	Text(ctx context.Context, pane int) (string, bool)

	// PaneTTY returns the pane's tty device, or "" when the pane does not
	// resolve. It is how delivery proves the pane is still this session's.
	PaneTTY(ctx context.Context, pane int) string

	// ListPanes returns the live pane listing, or an error when it could not be
	// read at all. ⚠️ The error is not decoration: "no panes" and "could not ask"
	// are different facts, and only the first licenses a claim about a pane.
	ListPanes(ctx context.Context) (map[int]Pane, error)

	// SessionTTY returns the tty of the session's own live process, or "" when
	// the session has no live process with a tty. ⚠️ An empty result covers
	// three different states — no registry record, a dead pid, and a live
	// process with no tty — and the caller must not treat them alike.
	SessionTTY(ctx context.Context, sessionID string) string

	// SessionEntry returns the session's registry record, or false when there is
	// none. The delivery path needs the pid (is the process alive?), the
	// entrypoint (is the session pane-less by construction?) and the cwd (the
	// routine policy's workspace boundary).
	SessionEntry(ctx context.Context, sessionID string) (SessionEntry, bool)
}

// SessionEntry is the part of a session registry record the delivery path reads.
type SessionEntry struct {
	// SessionID is the session's own id.
	SessionID string
	// PID is the session process's pid, or 0 when the record carries none.
	PID int
	// Entrypoint is the process's entrypoint, e.g. `sdk-cli`.
	Entrypoint string
	// CWD is the session's working directory.
	CWD string
}

// NewWeztermPaneTextWriter creates the text writer the delivery carrier uses.
func NewWeztermPaneTextWriter(panes PaneLister) PaneTextWriter {
	return &weztermPaneTextWriter{
		panes:     panes,
		activator: NewWeztermPaneActivator(panes),
	}
}

type weztermPaneTextWriter struct {
	panes     PaneLister
	activator PaneActivator
}

// Activate delegates to the existing activator, so the writer is a PaneActivator
// by construction rather than by a parallel implementation.
func (w *weztermPaneTextWriter) Activate(ctx context.Context, pane string) error {
	return w.activator.Activate(ctx, pane)
}

// SendText writes text into the pane without submitting it.
//
// ⚠️ `--no-paste` is load-bearing: without it WezTerm routes the text through
// the paste path, which the TUI renders differently and which a bracketed-paste
// guard can swallow.
func (w *weztermPaneTextWriter) SendText(ctx context.Context, pane int, text string) error {
	ctx, cancel := context.WithTimeout(ctx, paneTextTimeout)
	defer cancel()
	binary, err := resolveWezterm(ctx)
	if err != nil {
		return err
	}
	// #nosec G204 -- the reported risk is "subprocess launched with a variable",
	// and `binary` is resolved by resolveWezterm from the fixed, compile-time
	// candidate list via exec.LookPath, so neither a caller nor any request input
	// can reach it. Same provenance as the pane lister's own call.
	cmd := exec.CommandContext(
		ctx,
		binary,
		"cli", "--no-auto-start", "send-text", "--pane-id", strconv.Itoa(pane), "--no-paste", text,
	)
	cmd.WaitDelay = paneListingWaitDelay
	if err := cmd.Run(); err != nil {
		return errors.Wrapf(ctx, err, "send-text into pane %d failed", pane)
	}
	return nil
}

// ComposerState reads the pane's input line.
func (w *weztermPaneTextWriter) ComposerState(ctx context.Context, pane int) ComposerState {
	text, ok := w.Text(ctx, pane)
	if !ok {
		return ComposerUnknown
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.HasPrefix(lines[i], composerPromptGlyph) {
			continue
		}
		if strings.TrimSpace(strings.TrimPrefix(lines[i], composerPromptGlyph)) == "" {
			return ComposerEmpty
		}
		return ComposerBusy
	}
	return ComposerUnknown
}

// Text returns the pane's visible text.
func (w *weztermPaneTextWriter) Text(ctx context.Context, pane int) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, paneTextTimeout)
	defer cancel()
	binary, err := resolveWezterm(ctx)
	if err != nil {
		return "", false
	}
	// #nosec G204 -- see SendText: `binary` comes from the fixed candidate list
	// via exec.LookPath, never from caller or request input.
	cmd := exec.CommandContext(
		ctx, binary, "cli", "--no-auto-start", "get-text", "--pane-id", strconv.Itoa(pane),
	)
	cmd.WaitDelay = paneListingWaitDelay
	raw, err := cmd.Output()
	if err != nil {
		glog.V(2).Infof("get-text pane %d failed: %v", pane, err)
		return "", false
	}
	return string(raw), true
}

// PaneTTY returns the pane's tty device, or "".
func (w *weztermPaneTextWriter) PaneTTY(ctx context.Context, pane int) string {
	panes, err := w.panes.List(ctx)
	if err != nil {
		return ""
	}
	target, ok := panes[pane]
	if !ok {
		return ""
	}
	return target.TTYName
}

// ListPanes returns the live pane listing.
func (w *weztermPaneTextWriter) ListPanes(ctx context.Context) (map[int]Pane, error) {
	return w.panes.List(ctx)
}

// SessionEntry returns the session's registry record.
func (w *weztermPaneTextWriter) SessionEntry(
	ctx context.Context,
	sessionID string,
) (SessionEntry, bool) {
	record, ok := readSessionProcessRecord(ctx, w.sessionsDir(), sessionID)
	if !ok {
		return SessionEntry{}, false
	}
	// The two structs carry the same fields, so this is a conversion rather than
	// a field-by-field copy.
	return SessionEntry(record), true
}

// SessionTTY returns the tty of the session's own live process, or "".
//
// ⚠️ The registry record is matched by `sessionId`, not by the file name — the
// file is named by pid. An entry whose pid is dead has no tty either, and that
// state must stay distinguishable from "a live process with no tty" at the
// caller, so this returns "" for both and the caller consults the registry
// itself.
func (w *weztermPaneTextWriter) SessionTTY(ctx context.Context, sessionID string) string {
	record, ok := readSessionProcessRecord(ctx, w.sessionsDir(), sessionID)
	if !ok || record.PID <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, paneTextTimeout)
	defer cancel()
	// #nosec G204 -- the command and flags are compile-time literals; the only
	// variable is a numeric pid read from the local registry, passed as its own
	// argument (never through a shell).
	cmd := exec.CommandContext(ctx, "ps", "-o", "tty=", "-p", strconv.Itoa(record.PID))
	raw, err := cmd.Output()
	if err != nil {
		return ""
	}
	tty := strings.TrimSpace(string(raw))
	if tty == "" || tty == "??" {
		return ""
	}
	if !strings.HasPrefix(tty, "/dev/") {
		return "/dev/" + tty
	}
	return tty
}

// sessionsDir is where the session registry lives. It is a method so a spec can
// construct the writer without touching the real registry when it only exercises
// the pane half.
func (w *weztermPaneTextWriter) sessionsDir() string {
	return defaultSessionRegistryDir()
}

// sessionProcessRecord is the part of a registry entry the tty resolution reads.
type sessionProcessRecord struct {
	SessionID  string `json:"sessionId"`
	PID        int    `json:"pid"`
	Entrypoint string `json:"entrypoint"`
	CWD        string `json:"cwd"`
}

// readSessionProcessRecord finds the registry entry for sessionID.
//
// ⚠️ It scans the directory rather than opening `<sessionID>.json`, because the
// registry names its files by pid. An unreadable directory or entry reports
// false, which the callers treat as "unprovable" rather than as a negative
// answer.
func readSessionProcessRecord(
	ctx context.Context,
	sessionsDir string,
	sessionID string,
) (sessionProcessRecord, bool) {
	if sessionsDir == "" || sessionID == "" {
		return sessionProcessRecord{}, false
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		glog.V(2).Infof("read session registry %s failed: %v", sessionsDir, err)
		return sessionProcessRecord{}, false
	}
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return sessionProcessRecord{}, false
		default:
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sessionsDir, entry.Name()))
		if err != nil {
			continue
		}
		var record sessionProcessRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			continue
		}
		if record.SessionID == sessionID {
			return record, true
		}
	}
	return sessionProcessRecord{}, false
}

// defaultSessionRegistryDir resolves ~/.claude/sessions.
func defaultSessionRegistryDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

// Compile-time guard: the writer must satisfy its own interface.
var _ PaneTextWriter = (*weztermPaneTextWriter)(nil)
