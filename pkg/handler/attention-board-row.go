// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package-level note: this file holds the board's per-card derivations — an item
// plus its resolved provenance and delivery report into the render data the
// board JSON carries (see attention-board-view.go). The React frontend renders
// the card from that JSON; nothing here renders HTML.
package handler

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/attention-controller/pkg"
)

// boardRow is one item paired with what could be resolved about its origin,
// plus the values derived from the pair. It is the one builder the board JSON
// and the SSE stream's change detection both read, so the two cannot disagree
// about what a card shows.
type boardRow struct {
	Item       pkg.Item
	Provenance pkg.Provenance
	// Risk is the derived risk class: the declared level, raised and never
	// lowered by the production-touching marker (see pkg.RiskOf).
	Risk pkg.Risk
	// BlastRadius is the consequence class's blast-radius statement. Empty for
	// a routine card.
	BlastRadius string
	// DeliveryStatus is the store's DERIVED delivery status for this card's
	// answer. ⚠️ Empty means the read failed, and empty renders no marker — it is
	// deliberately not one of the statuses, so a failed read can never be
	// mistaken for `never_attempted`.
	DeliveryStatus pkg.DeliveryStatus
	// AnsweredAt is when the answer arrived. It is what lets Undeliverable tell an
	// answer that went nowhere from a card nobody has answered yet.
	AnsweredAt *libtime.DateTime
	// DeliveryAttemptedAt is when the attempting arm recorded its outcome. Nil
	// when no attempt record exists — the honest absence, never a zero instant.
	DeliveryAttemptedAt *libtime.DateTime
	// Jump is the copyable command handing a non-`message` item back to the
	// session that raised it. Empty when no pane resolved, and for a `message`
	// item.
	Jump string
	// JumpURL is the board-relative path that hands this row back to its
	// session. Set for every class. Empty when no pane resolved.
	JumpURL string
	// NoJump explains why this row carries no jump control, and is empty
	// whenever it carries one.
	NoJump string
	// Speak reports whether a tts server is configured.
	Speak bool
	// TaskURL, GoalURL and TopicURL open the item's vault task, the goal that
	// task names first, and the topic page listing that goal. Each is empty when
	// it did not resolve — an unresolvable value is absent, never a stand-in.
	TaskURL  string
	GoalURL  string
	TopicURL string
}

// Undeliverable reports whether this card's answer failed to reach the asking
// session.
//
// ⚠️ `failed` and `never_attempted` mark; `delivered`, `held` and
// `pre_trail_unknown` do not — see docs/attention-item-timeline.md. The
// AnsweredAt == nil guard is load-bearing: Delivery() derives never_attempted
// for an unanswered item too, so a predicate on the status alone would mark
// every open card.
func (r boardRow) Undeliverable() bool {
	if r.AnsweredAt == nil {
		return false
	}
	return r.DeliveryStatus == pkg.FailedStatus ||
		r.DeliveryStatus == pkg.NeverAttemptedStatus
}

// newBoardRow pairs an item with what could be resolved about its origin and
// derives the values the board JSON carries.
//
// vaultName is the vault's own name, derived once per request by
// vaultNameFromDir. delivery is the store's delivery report for this item, read
// by the caller for the whole board in one transaction; nil when that read
// failed, which renders no delivery stage and no outcome line.
func newBoardRow(
	item pkg.Item,
	provenance pkg.Provenance,
	delivery *pkg.DeliveryReport,
	speak bool,
	vaultName string,
) boardRow {
	row := boardRow{
		Item:        item,
		Provenance:  provenance,
		Jump:        jumpCommand(item, provenance),
		JumpURL:     jumpURL(item, provenance),
		NoJump:      noJumpReason(item, provenance),
		Speak:       speak,
		TaskURL:     vaultFileURL(vaultName, provenance.TaskPath),
		GoalURL:     vaultFileURL(vaultName, provenance.GoalPath),
		TopicURL:    vaultFileURL(vaultName, provenance.TopicPath),
		AnsweredAt:  item.AnsweredAt,
		Risk:        pkg.RiskOf(item, provenance.ProductionTouching),
		BlastRadius: pkg.BlastRadiusOf(item),
	}
	if delivery != nil {
		row.DeliveryStatus = delivery.Status
		row.DeliveryAttemptedAt = delivery.AttemptedAt
	}
	// Each distinct navigation value renders once: a session name equal to the
	// task title is cleared, because the task link already carries it.
	row.Provenance.SessionName = navigationSessionName(row.Provenance)
	return row
}

// recordClient renders the recorded answering client — user agent, then the
// peer address, then a forwarded address in its own parenthetical because it is
// a caller declaration. Empty when the store recorded nothing about the client.
func recordClient(item pkg.Item) string {
	client := item.AnsweredClient
	if client == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if client.UserAgent != "" {
		parts = append(parts, client.UserAgent)
	}
	if client.RemoteAddr != "" {
		parts = append(parts, "@ "+client.RemoteAddr)
	}
	rendered := strings.TrimSpace(strings.Join(parts, " "))
	if client.ForwardedFor != "" {
		if rendered == "" {
			rendered = "forwarded_for: " + client.ForwardedFor
		} else {
			rendered += " (forwarded_for: " + client.ForwardedFor + ")"
		}
	}
	return rendered
}

// vaultNameFromDir returns the vault's own name — the name an Obsidian URL
// addresses — from the configured vault directory.
//
// Empty in, empty out. ⚠️ The guard is load-bearing: filepath.Base("") is ".",
// so a host with no configured vault would otherwise emit
// `obsidian://open?vault=.` links.
func vaultNameFromDir(vaultDir string) string {
	if vaultDir == "" {
		return ""
	}
	return filepath.Base(vaultDir)
}

// obsidianQueryValue escapes one half of an `obsidian://open` query value.
//
// ⚠️ url.QueryEscape with `+` put back to `%20`: Obsidian does not decode `+`
// ([[Deep Link URL Schemes]]). Not url.PathEscape, which leaves `&`, `=` and `+`
// unescaped, so a task named `R&D notes` would open the wrong file.
func obsidianQueryValue(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

// vaultFileURL builds `obsidian://open?vault=<vault>&file=<path>` for a vault
// file, both halves escaped and a trailing `.md` dropped. It serves the task,
// goal and topic links alike, so there is one escaper rather than three. Empty
// when either half is empty, so no dangling link is ever produced.
func vaultFileURL(vaultName string, path string) string {
	if vaultName == "" || path == "" {
		return ""
	}
	return "obsidian://open?vault=" + obsidianQueryValue(vaultName) +
		"&file=" + obsidianQueryValue(strings.TrimSuffix(path, ".md"))
}

// jumpPane is the pane a row's control targets. A validated pane wins outright;
// RecordedPane is consulted only when the listing could not be read, and the
// board then marks it unvalidated.
func jumpPane(provenance pkg.Provenance) string {
	if provenance.Pane != "" {
		return provenance.Pane
	}
	if provenance.PaneListingUnreadable {
		return provenance.RecordedPane
	}
	return ""
}

// jumpCommand renders the copyable `/supervisor:jump <pane>` handover for an
// item the board must not answer. Empty for a `message` item and when no pane
// resolved.
func jumpCommand(item pkg.Item, provenance pkg.Provenance) string {
	if item.AnswerMechanism == pkg.MessageAnswerMechanism {
		return ""
	}
	pane := jumpPane(provenance)
	if pane == "" {
		return ""
	}
	return "/supervisor:jump " + pane
}

// jumpURL is the board-relative path the Jump button calls; the board performs
// the jump in-process, so no fleet-jump token reaches the browser. Empty when no
// pane resolved.
func jumpURL(item pkg.Item, provenance pkg.Provenance) string {
	if jumpPane(provenance) == "" {
		return ""
	}
	return "/jump/" + string(item.ItemID)
}

// noJumpReason explains why a row carries no jump control. Empty whenever the
// row carries one; it asks jumpCommand and jumpURL themselves so the
// explanation cannot drift from the control. It names no pane, host or cwd.
func noJumpReason(item pkg.Item, provenance pkg.Provenance) string {
	if jumpCommand(item, provenance) != "" || jumpURL(item, provenance) != "" {
		return ""
	}
	if provenance.PaneRecorded {
		return "The pane recorded for this item does not resolve to this session."
	}
	return "No pane was recorded for this item, so there is no session to jump to."
}

// navigationSessionName is the registry session name, except when it repeats
// the task title the task link already carries, in which case it is empty.
//
// ⚠️ Compared glyph-stripped: the registry name may carry Claude Code's leading
// status glyph (`⚙ …`) while the task title never does. The value returned is
// the registry name as given.
func navigationSessionName(provenance pkg.Provenance) string {
	if pkg.StripStatusGlyph(provenance.SessionName) == pkg.StripStatusGlyph(provenance.TaskName) {
		return ""
	}
	return provenance.SessionName
}

// deliveryReportsOf reads every card's delivery report in ONE transaction, and
// answers an empty map when the read fails.
//
// ⚠️ One read for the whole board, not one per row (measured 2026-11-16: ~2700
// read transactions a second per-row). The failure is swallowed deliberately:
// the report decorates a card already on the board, so a failed read degrades
// the delivery stage to absent rather than failing the whole read.
func deliveryReportsOf(
	ctx context.Context,
	store pkg.AttentionStore,
	items pkg.Items,
) map[pkg.ItemID]*pkg.DeliveryReport {
	reports, err := store.DeliveryReports(ctx, items)
	if err != nil || reports == nil {
		return map[pkg.ItemID]*pkg.DeliveryReport{}
	}
	return reports
}
