// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package-level note: these are the page's pure derivations — an item into the
// row it renders as, and a task or pane reference into the URL it links to.
// They are split from attention-page.go, which keeps the document itself, only
// because that file crossed revive's 2000-line file-length limit; the template
// deliberately stays there, byte-identical, because a live sibling branch edits
// its JavaScript.
package handler

import (
	"html/template"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/bborbe/attention-controller/pkg"
)

// affordance derives the controls a card carries from the item's answer
// mechanism. It is the one place that decision is made.
//
// ⚠️ It was two comparisons over the same field, and that shape read as a single
// derivation without being one: nothing in `Message: mechanism == message, Ack:
// mechanism == ack` states that a mechanism holds at most one affordance, so
// the two predicates were free to disagree and a mechanism matching neither fell
// through to whatever the template's `else` branch happened to be. That
// fall-through was not hypothetical — it is how a report-only `ack` card once
// rendered nothing at all, and how an `ack` card inherited a `permission` card's
// shape by accident. A switch over the one field makes the exclusivity
// structural: one mechanism, one arm, one answer.
//
// The `default` is deliberate rather than defensive. A mechanism the board has
// not been taught renders NO control rather than inheriting one: an inherited
// control offers the operator a move the mechanism does not support, and it does
// so silently, where rendering nothing is visible. `permission` reaches this arm
// today — a gate is approve-shaped and only the operator may answer it in the
// session that raised it, so a control there would be the permission laundering
// the schema forbids.
func affordance(mechanism pkg.AnswerMechanism) (message bool, ack bool) {
	switch mechanism {
	case pkg.MessageAnswerMechanism:
		return true, false
	case pkg.AckAnswerMechanism:
		return false, true
	default:
		return false, false
	}
}

// newAttentionPageRow pairs an item with what could be resolved about its origin
// and precomputes the question units its card renders.
//
// vaultName is the vault's own name, derived by the caller as
// `filepath.Base(vaultDir)`. ⚠️ It is passed in rather than derived here, and
// the reason is the stream: both callers of this function — the page handler and
// the SSE stream — must pass the *same* name, because a row arriving over the
// live channel must not differ from the same row on a fresh load, and that is
// the invariant the stream handler exists to preserve. Deriving it per row would
// also re-derive a per-page constant on every item. It is empty when no vault is
// configured, which renders no task link at all.
//
// The question units are built only for a `message` item. A `permission` item
// renders no card, so building units it would never render would be a value
// carried for nothing — and, worse, one a later change could render by
// accident.
func newAttentionPageRow(
	item pkg.Item,
	provenance pkg.Provenance,
	speak bool,
	vaultName string,
) attentionPageRow {
	row := attentionPageRow{
		Item:       item,
		Provenance: provenance,
		Jump:       jumpCommand(item, provenance),
		JumpURL:    jumpURL(item, provenance),
		NoJump:     noJumpReason(item, provenance),
		Speak:      speak,
		TaskURL:    vaultFileURL(vaultName, provenance.TaskPath),
		GoalURL:    vaultFileURL(vaultName, provenance.GoalPath),
		TopicURL:   vaultFileURL(vaultName, provenance.TopicPath),
	}
	row.Message, row.Ack = affordance(item.AnswerMechanism)
	// The Allow / Deny pair renders only for a headless worker's park. A tab
	// worker's gate is answered by pressing the prompt in the session's own pane,
	// so a board verdict there would be permission laundering — the exact failure
	// the schema forbids — while a headless worker has no pane of its own, so the
	// board is the one place its gate can honestly be answered. The fact is read
	// fail-closed from the supervisor's spawn ledger: every uncertainty about a
	// session's mode (an absent record, an unreadable directory, an unparseable
	// file, an unrecognised mode) leaves it false and renders no control, whose
	// worst case is a missing control rather than a control on a tab worker's gate.
	row.Decide = item.AnswerMechanism == pkg.PermissionAnswerMechanism && provenance.Headless
	if item.State == pkg.AnsweredState {
		// The board renders the record of what was answered so the operator can
		// see the answer standing in their name. `answered_by` is a caller
		// declaration, so a card that vanished on answering would destroy that
		// evidence at exactly the moment it could be noticed.
		row.Dimmed = true
		row.Record = recordAnswer(item)
	}
	if row.Message {
		row.Questions = pageQuestions(item)
		row.Tabs = len(item.Questions) > 0
	}
	return row
}

// recordAnswer renders the answer the store recorded, as the dimmed card shows
// it. It reads the field the schema names for each mechanism rather than
// guessing at one: `decision` carries a `permission` item's verdict, `answers`
// carries a multi-question item's entry per tab, and `answer` carries a
// single-question `message` item's content. ⚠️ A `permission` item never
// carries `answer`, so a renderer reading that field for every mechanism would
// show every permission record blank.
func recordAnswer(item pkg.Item) string {
	switch {
	case item.AnswerMechanism == pkg.PermissionAnswerMechanism:
		if item.Decision == "" {
			return "no decision recorded"
		}
		return "decision: " + string(item.Decision)
	case len(item.Answers) > 0:
		parts := make([]string, 0, len(item.Answers))
		for _, answer := range item.Answers {
			parts = append(parts, answer.Question+": "+answerText(answer.Answer))
		}
		return strings.Join(parts, " · ")
	case item.Answer != nil:
		return answerText(*item.Answer)
	default:
		// An item answered before `answer` existed carries none — the schema
		// adds no write-time rejection, so it reads as an item with no recorded
		// content rather than as an error.
		return "no answer recorded"
	}
}

// answerText renders one answer's content from its kind. `skip` is the case
// that needs saying out loud: it carries neither value nor values, so a card
// rendering the empty string would be indistinguishable from one whose answer
// failed to load.
func answerText(answer pkg.Answer) string {
	switch answer.Kind {
	case pkg.SkipAnswerKind:
		return "skipped"
	case pkg.OptionAnswerKind, pkg.TextAnswerKind:
		if len(answer.Values) > 0 {
			return strings.Join(answer.Values, ", ")
		}
		return answer.Value
	default:
		return "no answer recorded"
	}
}

// pageQuestions builds the question units a row's card renders. An item carrying
// `questions` renders one unit per question in the declared order; a
// single-question item renders exactly one, built from the item's own Payload,
// Options and AnswerCardinality — which is what makes the template's panel loop
// the only rendering path rather than one of two.
//
// The first unit is the active one, so the strip and the panels agree on which
// question is open before any click.
func pageQuestions(item pkg.Item) []attentionPageQuestion {
	if len(item.Questions) == 0 {
		return []attentionPageQuestion{
			{
				Payload: item.Payload,
				Hint:    cardinalityHint(item.AnswerCardinality, len(item.Options)),
				Multi:   item.AnswerCardinality == pkg.MultipleAnswerCardinality,
				Active:  true,
				Name:    optionName(item.ItemID, ""),
				Options: item.Options,
			},
		}
	}
	questions := make([]attentionPageQuestion, 0, len(item.Questions))
	for index, question := range item.Questions {
		questions = append(questions, attentionPageQuestion{
			Tab:     question.Tab,
			Payload: question.Payload,
			Hint:    cardinalityHint(question.Cardinality, len(question.Options)),
			Multi:   question.Cardinality == pkg.MultipleAnswerCardinality,
			Active:  index == 0,
			Name:    optionName(item.ItemID, question.Tab),
			Options: question.Options,
		})
	}
	return questions
}

// cardinalityHint renders the parenthetical that rides the question line, in the
// producer's own wording rather than as a machine value.
//
// An absent cardinality reads as single, exactly as the schema says it does, so
// a pre-change item renders the single-pick hint rather than none — the control
// it gets is a radio button, and a hint agreeing with the control is what makes
// the card self-explaining. A question offering no options renders no hint at
// all: a statement about picks would describe a choice the question does not
// offer.
func cardinalityHint(cardinality pkg.AnswerCardinality, optionCount int) string {
	if optionCount == 0 {
		return ""
	}
	if cardinality == pkg.MultipleAnswerCardinality {
		return "pick any number"
	}
	return "pick one"
}

// optionName is the input group name for one question's controls. It carries the
// item id as well as the tab so two cards on one page cannot share a radio
// group: a shared name would let a pick on one card clear another's.
func optionName(itemID pkg.ItemID, tab string) string {
	return "option-" + itemID.String() + "-" + tab
}

// vaultNameFromDir returns the vault's own name — the name an Obsidian URL
// addresses — from the configured vault directory.
//
// It exists so the page handler and the SSE stream derive that name through one
// function rather than two copies of the same expression: a row arriving over
// the live channel must render identically to the same row on a fresh load, and
// a name derived differently on either surface would put a different link on the
// same card depending on how it arrived.
//
// Empty in, empty out. ⚠️ The guard is load-bearing rather than defensive:
// filepath.Base("") is ".", so a host with no configured vault would otherwise
// name a vault called `.` and emit `obsidian://open?vault=.` links.
func vaultNameFromDir(vaultDir string) string {
	if vaultDir == "" {
		return ""
	}
	return filepath.Base(vaultDir)
}

// obsidianQueryValue escapes one half of an `obsidian://open` query value.
//
// ⚠️ It is url.QueryEscape with the `+` put back to `%20`, which is the vault's
// own documented convention rather than a choice made here — see [[Deep Link URL
// Schemes]] § "`+` is inert", measured against a live Obsidian on 2026-09-18:
// Obsidian does not decode `+`, so a link carrying one opens nothing while
// looking perfectly correct. The swap is safe because QueryEscape renders a
// literal `+` as `%2B`, so no genuine `+` can be corrupted by it.
//
// ⚠️ Not url.PathEscape, the tempting choice for a value that reads as a path.
// PathEscape leaves `&`, `=` and `+` unescaped — they are legal *inside a path
// segment* — so a task file named `R&D notes.md` would render
// `…&file=25%20Tasks%2FR&D%20notes`: Obsidian reads that as a `file` of
// `25 Tasks/R` plus a stray `D notes` parameter, and the link opens the wrong
// file. QueryEscape escapes all three (`&`→`%26`, `=`→`%3D`, `+`→`%2B`) and
// still renders a slash as `%2F`.
func obsidianQueryValue(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

// vaultFileURL builds the link that opens a vault file in Obsidian, from the
// vault's own name and the file's path relative to the vault root.
//
// It serves all three spans of the provenance line that link into the vault —
// the task, the goal the task names first and the topic page that lists that
// goal — because those are one link shape: a second builder for the goal and
// topic spans would be a second escaper, and two escapers are free to disagree
// about `&` or a space while both look correct.
//
// The form is `obsidian://open?vault=<vault>&file=<path>`, with both values
// escaped so a space becomes `%20` and a slash `%2F`, and a trailing `.md`
// dropped if present. The extension is stripped idempotently — strings.TrimSuffix
// rather than an assumed suffix — because the index's own path field is not
// specified to carry one, and a strip that assumed it would either double-handle
// the path or mangle a task whose name genuinely ends in those characters.
//
// Both halves go through obsidianQueryValue, the vault name as well as the path:
// the name is only the configured directory's base name, and nothing guarantees
// it is free of a space.
//
// Empty when either half is empty, so an item whose session anchors no task, and
// a host with no configured vault, each render no link rather than a dangling
// one.
func vaultFileURL(vaultName string, path string) template.URL {
	if vaultName == "" || path == "" {
		return ""
	}
	link := "obsidian://open?vault=" + obsidianQueryValue(vaultName) +
		"&file=" + obsidianQueryValue(strings.TrimSuffix(path, ".md"))
	// #nosec G203 -- the reported risk is "use of unescaped data in an HTML
	// template", and the conversion is the point: html/template's URL filter
	// admits only `http`, `https`, `mailto` and relative URLs, so an
	// `obsidian://` href is representable no other way — without this the
	// template renders `href="#ZgotmplZ"` and the link is dead. The value is
	// built two lines above from the operator-configured vault name (the base
	// name of the configured vault directory) and a filesystem-derived task
	// path, never from producer input, and both halves are escaped with
	// obsidianQueryValue before they are concatenated. This records the
	// provenance; it does not waive a risk.
	return template.URL(link)
}

// jumpCommand renders the copyable half of the handover for an item the board
// must not answer.
//
// ⚠️ This was the *whole* handover, and a command only, on the reasoning that a
// browser cannot activate a WezTerm tab. That premise is false against the
// fleet-jump server, which activates a pane from a plain HTTP GET, so the row
// now carries jumpURL's button as well. The command stays because an operator
// may still want to paste it — it is added to, never replaced.
//
// An item whose pane did not resolve yields no command, so its row renders
// exactly as it did before: an unresolvable value renders absent rather than as
// a stand-in, which is what the schema's silence 7 forbids.
//
// It is empty for a `message` item, which renders the button without the
// command — the label above it is false for an item the board may answer in
// place.
func jumpCommand(item pkg.Item, provenance pkg.Provenance) string {
	if item.AnswerMechanism == pkg.MessageAnswerMechanism {
		return ""
	}
	if provenance.Pane == "" {
		return ""
	}
	return "/supervisor:jump " + provenance.Pane
}

// jumpURL renders the clickable half of the handover: a path on this board, not
// the fleet-jump server's URL.
//
// The fleet-jump URL carries the shared token as a query parameter, so putting
// it in the page would publish the token in the served document on every load.
// This path is called in the background and the board performs the jump
// in-process, which keeps the browser on the board, since a real link would
// navigate it away.
//
// Empty when no pane resolved — the same absence rule as jumpCommand, so a row
// with no resolvable pane renders no button and no placeholder.
//
// ⚠️ The `enabled` parameter that used to gate this on a readable jump token is
// gone, removed by the jump fold: the endpoint this path points at no longer
// reads a token, so gating on one would hide a control that works. The pane is
// now the only condition, which is also why a row's button and its explanation
// cannot disagree about why it is missing.
func jumpURL(item pkg.Item, provenance pkg.Provenance) string {
	if provenance.Pane == "" {
		return ""
	}
	return "/jump/" + string(item.ItemID)
}

// noJumpReason explains, in the row's own words, why it carries no jump control.
//
// Empty whenever the row carries one. ⚠️ It asks jumpCommand and jumpURL
// themselves rather than restating their guards, so the explanation cannot drift
// from the control it explains: there is one decision, read twice.
//
// ⚠️ Three sentences, not one per code path, and the coarseness is read rather
// than chosen. The reasons a pane is absent subdivide by *writer* — no state dir,
// an unopenable one, a missing producer log, a log with no line for the key — but
// readEvents collapses every one of its own failures into the same empty map and
// Resolve cannot tell "no entry" from "no log", so the board has no fact to
// separate them by. A finer split would have to be invented here rather than
// read from the resolver, and it would name host-internal paths on a card the
// operator reads. See [[A Card With No Jump Target Explains Why Instead of
// Rendering Nothing]] § Results.
// ⚠️ The "jump is unavailable on this host" sentence is gone with the token
// gate that produced it. It explained a row that HAD a resolvable pane yet
// carried no control — the state that existed only while the button was gated
// on a readable token. With the gate removed that state is unreachable, so the
// sentence is deleted rather than left as a branch nothing can take. A case
// that cannot occur is worse than a missing one: it reads as coverage.
func noJumpReason(item pkg.Item, provenance pkg.Provenance) string {
	if jumpCommand(item, provenance) != "" || jumpURL(item, provenance) != "" {
		return ""
	}
	if provenance.PaneRecorded {
		// A pane was recorded and does not resolve to this session: the case
		// silence 7 marks `unroutable`. The provenance line still carries that
		// marker; this sentence is additive rather than a replacement for it.
		return "The pane recorded for this item does not resolve to this session."
	}
	return "No pane was recorded for this item, so there is no session to jump to."
}
