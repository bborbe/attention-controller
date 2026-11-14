// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang/glog"
)

// DeliveryVerdict is what one delivery attempt concluded.
//
// ⚠️ `ok` and `retry` are NOT interchangeable and the difference is load
// bearing. The caller records `ok` as `delivered` and drops the entry, so a
// false `ok` means the answer is never retried while its gate stays open — and
// the card, already `answered` in the store, is swept closed 60 min later with
// the worker still waiting (measured 2026-10-14: 372 such lines).
type DeliveryVerdict string

const (
	// DeliveryOK means the answer reached the session.
	DeliveryOK DeliveryVerdict = "ok"
	// DeliveryRetry means nothing was delivered and nothing failed — try again.
	DeliveryRetry DeliveryVerdict = "retry"
	// DeliveryRefuse means the answer cannot be delivered.
	DeliveryRefuse DeliveryVerdict = "refuse"
	// DeliveryNoPane means the session is live but pane-less by construction,
	// so nothing was attempted and nothing failed.
	DeliveryNoPane DeliveryVerdict = "no-pane"
	// DeliveryResolved means the gate was already gone — nothing to deliver.
	DeliveryResolved DeliveryVerdict = "resolved"
)

// deliverPrefix opens a carried board answer. A session's own rule accepts a
// board delivery only when it names the card, which is why the store id travels
// WITH the answer.
const deliverPrefix = "Operator answer, via attention board:"

// transcriptLagBytes is how far the transcript must have moved past a typing's
// byte mark before an empty composer counts as proof the copy was SWALLOWED
// rather than merely not yet flushed.
//
// ⚠️ The harness appends asynchronously: measured 2026-10-19, a turn that
// started at 21:41:27Z was still absent from the file at the 21:42:36Z retry,
// and re-typing there delivered a duplicate 2m17s later.
const transcriptLagBytes = 65536

// Defaults for the submit loop. They are fields on the carrier rather than
// package constants so a spec can drive the loop without real sleeps.
const (
	defaultSubmitPollInterval = 250 * time.Millisecond
	defaultSubmitWaitPolls    = 12 // up to 3 s for the typed text to be drawn
	defaultSubmitAttempts     = 3
)

// PaneDelivery is one answer being carried into a session's pane.
//
// ⚠️ It is mutated in place, never copied. `TypeIntoPane` records the pending
// line and its byte mark on the entry it is given; on a copy those markers are
// written and thrown away, and every retry re-types.
type PaneDelivery struct {
	// SessionID is the session the answer belongs to.
	SessionID string
	// Pane is the recorded pane id. ⚠️ It is a LEASE, not an identifier — it is
	// frozen at post time and WezTerm recycles ids, so ownership is re-proved at
	// delivery rather than trusted.
	Pane int
	// StoreItemID is the store id the answer must name.
	StoreItemID string
	// PendingLine is the exact line already typed into the pane, or "".
	PendingLine string
	// PendingAt is the transcript byte mark taken before that line was typed.
	PendingAt int64
	// HasPendingAt reports whether PendingAt is meaningful.
	HasPendingAt bool
	// BoundText is the answer text the delivery clock was started for.
	BoundText string
}

// PaneCarrier types an answer into a session's pane and proves it landed.
//
// ⚠️ It is the one arm that actually carries a board answer INTO a session for
// a tab session, which no server holds. The store-polling arms only settle a
// park they hold, so for the common case this is the only arm that ever
// delivers anything.
type PaneCarrier interface {
	// TypeIntoPane types the answer and submits it, returning the verdict and a
	// human-readable reason.
	//
	// bare=True sends `text` as the line itself — no prefix, no store id. The
	// compact trio needs it: a line beginning "Operator answer, …" does not
	// start with "/", so the session reads it as prose and never dispatches the
	// command inside it.
	TypeIntoPane(
		ctx context.Context,
		delivery *PaneDelivery,
		text string,
		bare bool,
	) (DeliveryVerdict, string)
}

// NewPaneCarrier creates the carrier the delivery arm uses.
func NewPaneCarrier(writer PaneTextWriter, transcripts TranscriptReader) PaneCarrier {
	return &paneCarrier{
		writer:             writer,
		transcripts:        transcripts,
		submitPollInterval: defaultSubmitPollInterval,
		submitWaitPolls:    defaultSubmitWaitPolls,
		submitAttempts:     defaultSubmitAttempts,
	}
}

type paneCarrier struct {
	writer      PaneTextWriter
	transcripts TranscriptReader
	// The submit loop is field-driven so a spec can set every interval to zero.
	submitPollInterval time.Duration
	submitWaitPolls    int
	submitAttempts     int
}

// TypeIntoPane types the answer and submits it.
//
// ⚠️ Text and Enter go in SEPARATE sends, and Enter only once the text is drawn.
// Sent as one burst the TUI swallowed the `\r` and left the answer sitting
// unsubmitted in the composer (measured 2026-09-29, pane 88) — and a recheck on
// a fixed sleep read the composer before the text had rendered, saw it empty,
// and never retried.
//
// ⚠️ AT MOST ONE COPY of `line` per card, ever. This function is re-invoked on
// every poll for an entry the caller kept as `retry`, and the composer cannot
// tell a submitted turn from a swallowed one — so a retry that re-typed was
// delivered twice (measured 2026-10-18: card `b175095e`, two turns 24 min
// apart). `PendingLine` is the persisted proof that this exact line already
// went into this pane.
func (c *paneCarrier) TypeIntoPane(
	ctx context.Context,
	delivery *PaneDelivery,
	text string,
	bare bool,
) (DeliveryVerdict, string) {
	// A bare send is one command of the compact trio, not the card's answer:
	// resetting on it would restart the clock every time the trio advanced.
	if !bare {
		c.resetBoundForAnswer(delivery, text)
	}
	verdict, pane, why := c.resolveDeliveryPane(ctx, delivery)
	if verdict != DeliveryOK {
		return verdict, why
	}
	line := c.renderLine(delivery, text, bare)
	if verdict, why := c.retypeGuard(ctx, delivery, pane, line, bare); verdict != "" {
		return verdict, why
	}
	return c.send(ctx, delivery, pane, line, bare)
}

// resetBoundForAnswer starts a fresh delivery bound when the card's answer text
// changed, so a DIFFERENT answer never inherits the old answer's expired clock.
func (c *paneCarrier) resetBoundForAnswer(delivery *PaneDelivery, text string) {
	if delivery.BoundText == text {
		return
	}
	delivery.BoundText = text
}

// renderLine builds the exact line to type.
func (c *paneCarrier) renderLine(delivery *PaneDelivery, text string, bare bool) string {
	if bare {
		return strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	}
	line := deliverPrefix + " " + text + " [" + delivery.StoreItemID + "]"
	return strings.NewReplacer("\r", " ", "\n", " / ").Replace(line)
}

// retypeGuard is the at-most-once check. It returns a non-empty verdict when the
// caller must NOT type again, and "" when typing is safe.
func (c *paneCarrier) retypeGuard(
	ctx context.Context,
	delivery *PaneDelivery,
	pane int,
	line string,
	bare bool,
) (DeliveryVerdict, string) {
	pending := delivery.PendingLine
	if pending != "" && pending != line {
		// A DIFFERENT answer for the same card wins over the stale marker.
		pending = ""
		delivery.PendingLine = ""
		delivery.HasPendingAt = false
	}
	if pending == "" {
		// ⚠️ Check the transcript BEFORE typing even with no marker. A delivery
		// arm that rebuilds its entry every cycle (the in-process job does) has
		// no persisted `PendingLine`, so a lost attempt record would otherwise
		// re-type a copy the session already received. The rendered line carries
		// the card's store id, so a match is card-specific. Bare trio commands
		// are exempt: re-sending one is legitimate and the trio owns its own
		// ordering.
		if !bare && c.transcripts.HoldsLine(delivery.SessionID, line, nil) {
			return DeliveryOK, "already delivered to pane " + strconv.Itoa(pane) + " (transcript)"
		}
		state := c.writer.ComposerState(ctx, pane)
		if state != ComposerEmpty {
			return DeliveryRetry, "pane " + strconv.Itoa(pane) + " composer is " + string(state)
		}
		return "", ""
	}
	if c.transcripts.HoldsLine(delivery.SessionID, pending, delivery.since()) {
		delivery.clearPending()
		return DeliveryOK, "already delivered to pane " + strconv.Itoa(pane) + " (transcript)"
	}
	state := c.writer.ComposerState(ctx, pane)
	if state == ComposerBusy {
		// Never press Enter here: the composer may hold the operator's own
		// half-typed input, and Enter would submit it.
		return DeliveryRetry, "pane " + strconv.Itoa(pane) + " composer is busy"
	}
	if state == ComposerUnknown {
		return DeliveryRetry, "pane " + strconv.Itoa(pane) + " composer is unreadable"
	}
	// Empty composer and nothing in the transcript. That is NOT yet proof the
	// copy was swallowed: the harness writes the transcript asynchronously, so
	// a delivery that has already started a turn can be absent for tens of
	// seconds. Retype only once the file has demonstrably moved past the mark.
	if nowAt, ok := c.transcripts.Offset(delivery.SessionID); ok &&
		delivery.HasPendingAt && nowAt-delivery.PendingAt < transcriptLagBytes {
		return DeliveryRetry, "pane " + strconv.Itoa(pane) +
			" composer empty; transcript still behind — waiting"
	}
	delivery.clearPending()
	return "", ""
}

// send types the line, waits for it to be drawn, then submits it.
func (c *paneCarrier) send(
	ctx context.Context,
	delivery *PaneDelivery,
	pane int,
	line string,
	bare bool,
) (DeliveryVerdict, string) {
	markAt, markOK := c.transcripts.Offset(delivery.SessionID)
	// Record the intent BEFORE the text goes in. A crash, a restart, or a submit
	// that lands before the composer is ever polled otherwise leaves the next
	// attempt with no marker AND no byte mark to bound its transcript scan.
	delivery.PendingLine = line
	delivery.PendingAt = markAt
	delivery.HasPendingAt = markOK
	if err := c.writer.SendText(ctx, pane, line); err != nil {
		delivery.clearPending()
		return DeliveryRetry, "send-text into pane " + strconv.Itoa(pane) + " failed"
	}
	if !c.waitForDrawn(ctx, pane) {
		// Never retype HERE: the text may yet land, and a second copy is worse.
		// But this is not a delivery, so it must not report `ok` — the caller
		// would record `delivered` and drop the entry, losing the answer while
		// its gate stays open.
		return DeliveryRetry, "typed into pane " + strconv.Itoa(pane) +
			", but the text never showed in the composer — submit not sent"
	}
	for attempt := 1; attempt <= c.submitAttempts; attempt++ {
		if err := c.writer.SendText(ctx, pane, "\r"); err != nil {
			glog.V(2).Infof("send Enter into pane %d failed: %v", pane, err)
		}
		time.Sleep(c.submitPollInterval * 2)
		if c.delivered(ctx, delivery, pane, line, bare) {
			delivery.clearPending()
			return DeliveryOK, "typed into pane " + strconv.Itoa(pane) +
				" and submitted (Enter attempt " + strconv.Itoa(attempt) + ")"
		}
	}
	// Every Enter is spent and there is still no proof of a submit. `ok` here
	// was the 2026-10-14 defect; `PendingLine` stays set, so the caller's retry
	// re-checks the transcript and never types a second copy while one may still
	// be in the pane.
	return DeliveryRetry, "typed into pane " + strconv.Itoa(pane) +
		", but it is still unsubmitted after " + strconv.Itoa(c.submitAttempts) + " Enters"
}

// waitForDrawn polls until the composer holds the typed text.
func (c *paneCarrier) waitForDrawn(ctx context.Context, pane int) bool {
	for i := 0; i < c.submitWaitPolls; i++ {
		if c.writer.ComposerState(ctx, pane) == ComposerBusy {
			return true
		}
		time.Sleep(c.submitPollInterval)
	}
	return false
}

// delivered is the submit proof.
//
// ⚠️ The TRANSCRIPT is the proof for an answer delivery: an empty composer is
// also what a swallowed message looks like, and `!= busy` additionally accepts
// an unreadable pane, which is evidence of nothing. The trio keeps the composer
// check because its commands are dispatched from the input line rather than
// recorded as ordinary user turns.
func (c *paneCarrier) delivered(
	ctx context.Context,
	delivery *PaneDelivery,
	pane int,
	line string,
	bare bool,
) bool {
	if bare {
		return c.writer.ComposerState(ctx, pane) == ComposerEmpty
	}
	return c.transcripts.HoldsLine(delivery.SessionID, line, delivery.since())
}

// resolveDeliveryPane returns the pane to deliver into.
//
// ⚠️ The RECORDED pane is tried first, and that ordering is load-bearing.
// Resolving unconditionally would turn a healthy delivery into an ambiguity
// refusal whenever the recorded pane is one of two panes sharing the session's
// tty. Only a pane that does NOT carry the session's tty needs re-resolving.
func (c *paneCarrier) resolveDeliveryPane(
	ctx context.Context,
	delivery *PaneDelivery,
) (DeliveryVerdict, int, string) {
	want := c.writer.SessionTTY(ctx, delivery.SessionID)
	if want == "" {
		return c.noPaneVerdict(ctx, delivery.SessionID, "session "+shortID(delivery.SessionID)+
			" has no live process with a tty")
	}
	if delivery.Pane > 0 && c.writer.PaneTTY(ctx, delivery.Pane) == want {
		return DeliveryOK, delivery.Pane, "ok"
	}
	pane, reason := c.paneForSession(ctx, delivery.SessionID, want)
	if pane <= 0 {
		if isTransientPaneReason(reason) {
			return DeliveryRetry, 0, reason
		}
		return DeliveryRefuse, 0, reason
	}
	delivery.Pane = pane
	return DeliveryOK, pane, reason
}

// noPaneVerdict classifies a session whose pane could not be resolved.
//
// ⚠️ `no-pane` is not `refuse`. A pane-less live session attempted nothing and
// failed at nothing; minting a delivery-failure card for it is noise, not news.
func (c *paneCarrier) noPaneVerdict(
	ctx context.Context,
	sessionID string,
	reason string,
) (DeliveryVerdict, int, string) {
	entry, ok := c.writer.SessionEntry(ctx, sessionID)
	if ok && strings.EqualFold(strings.TrimSpace(entry.Entrypoint), "sdk-cli") &&
		processAlive(entry.PID) && c.writer.SessionTTY(ctx, sessionID) == "" {
		return DeliveryNoPane, 0,
			"session " + shortID(sessionID) + " is pane-less — no pane by construction"
	}
	if ok && !processAlive(entry.PID) && c.transcripts.TurnEnded(sessionID) {
		return DeliveryRefuse, 0,
			"session " + shortID(
				sessionID,
			) + " ended its turn and its process exited — answer not delivered"
	}
	return DeliveryRefuse, 0, reason
}

// paneForSession finds the unique pane carrying the session's tty.
func (c *paneCarrier) paneForSession(
	ctx context.Context,
	sessionID string,
	want string,
) (int, string) {
	panes, err := c.writer.ListPanes(ctx)
	if err != nil {
		return 0, "panes could not be listed"
	}
	matches := make([]int, 0, 1)
	for id, pane := range panes {
		if pane.TTYName == want {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return 0, "no pane on " + want + " for session " + shortID(sessionID)
	case 1:
		return matches[0], "resolved pane " + strconv.Itoa(
			matches[0],
		) + " for session " + shortID(
			sessionID,
		)
	default:
		return 0, strconv.Itoa(len(matches)) + " panes on " + want + " — ambiguous"
	}
}

// isTransientPaneReason reports whether a resolution reason describes a FAILED
// READ rather than an absent pane. Those must be retried rather than allowed to
// destroy the entry.
func isTransientPaneReason(reason string) bool {
	return reason == "panes could not be listed" || reason == "pane list was not JSON"
}

// processAlive reports whether pid names a live process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := findProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// findProcess is a seam over os.FindProcess so a spec can drive the liveness
// branch without a real process.
var findProcess = os.FindProcess

// since returns the pending byte mark as the optional form the transcript
// reader takes.
func (d *PaneDelivery) since() *int64 {
	if !d.HasPendingAt {
		return nil
	}
	value := d.PendingAt
	return &value
}

// clearPending forgets the pending marker once the line is proven delivered.
func (d *PaneDelivery) clearPending() {
	d.PendingLine = ""
	d.PendingAt = 0
	d.HasPendingAt = false
}

// Compile-time guard: the carrier must satisfy its own interface.
var _ PaneCarrier = (*paneCarrier)(nil)
