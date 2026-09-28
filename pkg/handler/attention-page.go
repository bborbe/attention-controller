// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// attentionPageQuestion is one question unit as the card renders it: the unit a
// tab selects and a panel shows. A single-question item renders exactly one of
// these, built from the item's own Payload, Options and AnswerCardinality, so
// the template has one panel shape to render rather than two.
type attentionPageQuestion struct {
	// Tab is the tab label, and the key an answer names. Empty on a
	// single-question item, which renders no tab strip.
	Tab string
	// Payload is the question itself.
	Payload pkg.Payload
	// Hint is the cardinality hint appended to the question line in the
	// producer's own wording, e.g. "pick any number". Empty when the question
	// offers no options, where a statement about picks would describe a choice
	// the question does not offer.
	Hint string
	// Multi reports whether the question takes several picks. It selects the
	// control — a checkbox when true, a radio button when false — and is read
	// from the declared cardinality, never from the option count.
	Multi bool
	// Active marks the question whose panel renders open. Exactly one carries it,
	// which is what the tab strip and the panels agree on before any click.
	Active bool
	// Name is the input group name for this question's controls, scoped to the
	// item as well as to the question so two cards on one page cannot share a
	// radio group — a shared name would let a pick on one card clear another's.
	Name string
	// Options are this question's choices, in the order the producer declared
	// them. They are the schema's own type rather than a mirror of it, exactly as
	// Provenance is, so the card cannot drift from the field it renders.
	Options pkg.AnswerOptions
}

// attentionPageRow is one item paired with what could be resolved about its
// origin. Pairing here rather than in the template keeps the lookup out of the
// template language: a map indexed by item id inside `range` is exactly the
// kind of indirection html/template makes awkward, and it would put a silent
// zero-value Provenance one typo away from a rendered row.
type attentionPageRow struct {
	Item       pkg.Item
	Provenance pkg.Provenance
	// Message reports whether this row renders answer controls. True for
	// `message` items only: a `permission` item is approve-shaped and only the
	// operator may answer it in the session that raised it, so a control there
	// would be the permission laundering the schema forbids.
	Message bool
	// Ack reports whether this row renders the acknowledge control. True for
	// `ack` items only.
	//
	// ⚠️ This is the second half of an affordance that used to be a single
	// boolean, and the gap it closes was not cosmetic. An `ack` item is a
	// condition report: it asks nothing and routes nothing back, so the schema
	// closes it by acknowledgement — `open` → `closed` with `answered_at`
	// unset — and names an arm as a causer of that row. With no branch for it,
	// an `ack` row fell through the same `not .Message` path as a `permission`
	// row and rendered its payload and a state line and nothing else, so it sat
	// on the only surface that showed it and could not be cleared from there.
	//
	// A `permission` row is deliberately still in that position: it is
	// approve-shaped, only the operator may answer it in the session that
	// raised it, and a control here would be the permission laundering the
	// schema forbids. The two classes rendering identically was the accident;
	// they are separated by this field rather than by a shared absence.
	Ack bool
	// ⚠️ There was a `Permission bool` here for exactly one release, read by the
	// jump corner to except permission rows from the disabled control. It was
	// removed 2026-09-27 on the operator's own read of the live board: with the
	// exception in place, **2 of 77 open cards rendered no jump control at all**,
	// and the operator's ask is that the control be in the same place on every
	// card. The exception was mine to propose and theirs to overrule; the field
	// went with it rather than being left as a dead gate. See
	// [[Attention Item Schema]] silence 20.
	// Questions are the question units this row's card renders: the item's own
	// when it carries several, otherwise a single unit built from the item's own
	// Payload, Options and AnswerCardinality. Empty when the row renders no card,
	// which is what keeps the template's card a single `if .Message` away from a
	// `permission` row rather than a second thing to remember.
	//
	// Precomputed here for the reason Provenance is: html/template cannot index a
	// map inside `range`, and a zero-value question one typo away would render a
	// card with no controls at all.
	Questions []attentionPageQuestion
	// Tabs reports whether the card renders a tab strip — true when the item
	// carries several questions. It is the flag the template reads and the flag
	// the page's own script reads, so the strip and the wire shape cannot
	// disagree about whether this item answers by `answer` or by `answers`.
	Tabs bool
	// Dimmed reports whether this row is the dimmed record of an item that has
	// already been answered, rather than an open card. It is what the template
	// branches on to render a record instead of a prompt: a dimmed row carries
	// no form, no option row, no Dismiss and no Next, because a control there
	// would offer an answer to a question that already has one.
	//
	// It is derived from the item's state, never from the answer's presence: an
	// item answered before `answer` existed carries no content and is still a
	// record.
	Dimmed bool
	// Record is the answer the store recorded, rendered for the dimmed card by
	// recordAnswer. Empty on every row that is not dimmed.
	Record string
	// Jump is the copyable command handing a non-`message` item back to the
	// session that raised it. Empty when no pane resolved — an unresolvable
	// value renders absent rather than as a stand-in, per the schema's silence 7.
	//
	// It is empty for a `message` item as well, which renders the button without
	// the command: the div's label is false for an item the board may answer in
	// place, and the command text is redundant beside the button.
	Jump string
	// JumpURL is the board-relative path that hands this row back to its
	// session, rendered as the Jump button. Set for every class, `message`
	// included. Empty when no pane resolved, on the same rule as Jump.
	//
	// It is also empty when the token is unreadable, and ⚠️ that must degrade
	// the button *alone*: the template renders the div when either field is
	// set, so a host with no token still gets the copyable command exactly as
	// it rendered before this change. Gating the whole div on JumpURL would
	// silently drop the command too.
	JumpURL string
	// NoJump explains why this row carries no jump control, and is empty
	// whenever it carries one — the template reads it in the `else` of the same
	// condition, so the explanation and the control are one decision rather than
	// two that could drift apart.
	//
	// It is a rendering of an absence, never a stand-in for a value: it names no
	// pane, host or cwd, so it does not present an unresolvable value as
	// resolved. See [[Attention Item Schema]] silence 20 for the one case it
	// covers that the schema does not yet state a rule for.
	NoJump string
	// Speak reports whether this row renders the read-aloud control. It is
	// carried on the row rather than read from the page root because the row is
	// a sub-template: `{{template "attention-row" .}}` passes the row as the
	// data, so `$` inside it is the row and not the page, and a `$.Speak` left
	// in place would resolve against the wrong value.
	Speak bool
	// TaskURL is the link that opens this item's vault task, rendered as the
	// first element of the provenance line. Empty when no task resolved — an
	// unresolvable value renders absent rather than as a stand-in, the same rule
	// Jump and JumpURL follow.
	//
	// ⚠️ It is a template.URL rather than a string, and the type is load-bearing
	// rather than decorative. html/template's URL filter admits only `http`,
	// `https`, `mailto` and relative URLs, so a plain-string `href="{{ .TaskURL }}"`
	// renders `href="#ZgotmplZ"`: the link is dead in the browser while every test
	// that asserts on the row field still passes. `obsidian://` is exactly the
	// scheme that filter refuses, so the conversion is what makes the href emit at
	// all — and its cost is that the value is trusted unescaped, which is why
	// taskURL escapes both halves before building it.
	TaskURL template.URL
}

// attentionPageData is what the template renders: the items the store's read
// path returned, in the order it returned them. It carries no ordering of its
// own — the store never ranks, so the page must not either.
type attentionPageData struct {
	Items []attentionPageRow
	// Speak reports whether a read-aloud control should render. False when no
	// tts server is configured, so the page never offers a control whose
	// endpoint is unrouted.
	Speak bool
	// HideAnswered is the filter's initial position, read from the request's
	// `hide` parameter. It renders the switch's aria-checked so the served
	// markup agrees with the URL; the script applies the same reading to the
	// rows. It is a rendering input, never a store input — the page still
	// renders every row it was given.
	//
	// The default is true: only an explicit `?hide=none` renders it false, so a
	// fresh load of the board shows what is left rather than mixing the dimmed
	// answered records in with the cards that still need the operator.
	HideAnswered bool
	// BuildIdentity is what the running binary can say about the source it was
	// built from, rendered in the page's footer.
	//
	// It is a rendering input like Speak, and it is read from the BINARY rather
	// than from the repo on purpose: the footer answers "which build is
	// running", and a value read from the checkout answers a different question
	// that agrees with this one only while the two are in step. A build that
	// carries no identity renders an explicit line saying so — see
	// buildidentity.Identity.Known.
	BuildIdentity buildidentity.Identity
}

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
		TaskURL:    taskURL(vaultName, provenance.TaskPath),
	}
	row.Message, row.Ack = affordance(item.AnswerMechanism)
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

// taskURL builds the link that opens an item's vault task in Obsidian, from the
// vault's own name and the task file's path relative to the vault root.
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
func taskURL(vaultName string, taskPath string) template.URL {
	if vaultName == "" || taskPath == "" {
		return ""
	}
	link := "obsidian://open?vault=" + obsidianQueryValue(vaultName) +
		"&file=" + obsidianQueryValue(strings.TrimSuffix(taskPath, ".md"))
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

// boardHideParam is the query parameter carrying the board's view state, and
// boardHideAnswered / boardHideNone are the two values it recognises. Together
// they make the view addressable — `?hide=none` survives a reload, a bookmark
// and a shared link instead of resetting — which is what the operator asked
// for: *"The hide button at the top should be a URL parameter, so a reload of
// the page keeps the preview setting."*
//
// ⚠️ The value names the SET that is hidden rather than being a boolean, so the
// parameter can describe a different view later without a second parameter
// name, and so the URL reads as an instruction rather than as a flag whose
// meaning depends on knowing what it refers to. `answered` hides the dimmed
// answered records; `none` hides nothing.
//
// ⚠️ **The default is ON, and absence is how it is spelled.** From 2026-09-27
// the operator asked for the reverse of the position this parameter was built
// with — *"can we make hide answers the default — so open
// http://127.0.0.1:18080/ ... show no answer?"* — so a request carrying no
// `hide` value renders the filtered view, and `none` is the value that turns it
// back off. That is why the test is `!= boardHideNone` rather than
// `== boardHideAnswered`: an unrecognised value falls back to the default
// instead of silently disabling the filter, and "off" needs a spelling the
// script can write back, which a deleted parameter can no longer be.
//
// ⚠️ All three are mirrored as HIDE_PARAM, HIDE_ANSWERED and HIDE_NONE in the
// page's script, because the server renders the switch's initial position while
// the script applies the filter and writes the parameter back. That mirror is
// the only thing in this file that exists in two languages, so a spec asserts
// the script carries the same literals: drift there would leave the server
// rendering from one parameter while the script writes another, and the
// reload-reproduces-the-view property would stop working with every other test
// still green.
const (
	boardHideParam    = "hide"
	boardHideAnswered = "answered"
	boardHideNone     = "none"
)

// NewAttentionPageHandler creates the read-only HTML page a human opens to see
// what currently needs attention.
//
// ⚠️ This handler reverses the contract it was built with, and the reversal is
// deliberate rather than drift. It previously performed **no render-time
// lookup at all**, on the reasoning that ProducerID is a session id, the
// registry deletes its entry when the session exits, and a render-time lookup
// would therefore find nothing and present an unresolvable value as resolved.
// That reasoning is still honoured — an unresolvable value renders absent, and
// a pane is shown only when it validates — but its blanket prohibition could
// not survive the operator's ask, which is precisely for values the store does
// not carry: the store's fifteen-field Item has no host, cwd, tool or pane.
//
// The join is best-effort and the page stays usable without it. Every source is
// read through a resolver that fails soft: a host with no WezTerm, a store with
// no Claude Code state directory, an unreadable registry — each resolves to no
// values for that row, and the page renders exactly what it rendered before.
// That is the honest scoping of this page's standalone claim: it still works
// without Claude Code, but it works *with provenance absent* rather than
// without looking.
//
// ⚠️ The Jump button is gated on the pane alone, and the token gate that used
// to sit here was REMOVED by the jump fold rather than merely dropped. It read
// the jump token per render and suppressed the button when the token was
// unreadable — a precondition that held while the button's endpoint forwarded
// to the Python fleet-jump server, which authenticated with that token. The
// endpoint now performs the jump in-process (NewAttentionJumpHandler) and never
// reads a token, so the gate had become a false one: it would hide a working
// control because of a credential the control no longer touches. The token
// survives on the legacy pane-addressed route, which is the one surface that
// still needs it.
//
// vaultDir is the configured vault directory, and the vault's own *name* is
// derived from it as `filepath.Base(vaultDir)` — the name the Obsidian URL
// addresses, which is the directory's base name rather than anything stored in
// the vault. It is derived once per request rather than once per row, since
// every row on the page shares it, and an empty vaultDir yields an empty name
// so a host with no vault renders no task link. ⚠️ The guard on the empty
// directory is load-bearing: filepath.Base("") is "." rather than "", so
// without it an unconfigured vault would name a vault called `.`.
// buildIdentity is the running binary's own provenance, rendered in the page's
// footer. It is a parameter rather than a call to buildidentity.Read inside the
// handler so the handler stays a pure renderer: the value is read once at
// startup, from the binary, and a handler that re-read it per request would be
// re-deriving a constant while making the page's footer untestable.
func NewAttentionPageHandler(
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	vaultDir string,
	buildIdentity buildidentity.Identity,
) http.Handler {
	// Parsed once at construction rather than per request: the template is a
	// compile-time constant, so a parse failure is a programming error, and
	// template.Must makes it a startup failure rather than a per-request one.
	page := template.Must(template.New("attention-page").Parse(attentionPageTemplate))
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				// ReadBoard, not Read: the board renders the answered items as
				// dimmed records as well as the open ones. Read stays as it is
				// for the JSON read API, whose consumers act on what they are
				// given and must not be handed an already-answered item.
				items, err := store.ReadBoard(ctx)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "read failed"),
						libhttp.ErrorCodeInternal,
						http.StatusInternalServerError,
					)
				}
				// Resolved for the whole page at once, so the host-wide reads
				// behind it (the pane listing, the session registry) happen once
				// rather than once per row.
				provenances := provenance.Resolve(ctx, items)
				// ⚠️ No token is read here. The Jump button is gated on the
				// resolved pane alone — see the constructor's comment on why the
				// token gate was removed rather than kept.
				//
				// Derived once per request rather than per row: every row on the
				// page addresses the same vault.
				vaultName := vaultNameFromDir(vaultDir)
				rows := make([]attentionPageRow, 0, len(items))
				for _, item := range items {
					rows = append(
						rows,
						newAttentionPageRow(
							item,
							provenances[item.ItemID],
							speakEnabled,
							vaultName,
						),
					)
				}
				// Rendered into a buffer first so a render failure can still
				// produce the standard JSON error body. Writing straight to the
				// response would commit a 200 and a partial document before the
				// error handler had a chance to report anything.
				var body bytes.Buffer
				// Read once per render so the served markup agrees with the URL
				// the operator is looking at. A no-JS client cannot filter at
				// all, so this is not the filter — it is the switch's initial
				// position, and the script applies the same reading.
				//
				// The default is ON: only an explicit `?hide=none` turns the
				// filter off, and absence (or any unrecognised value) leaves it
				// on.
				hideAnswered := req.URL.Query().Get(boardHideParam) != boardHideNone
				if err := page.Execute(
					&body,
					attentionPageData{
						Items:         rows,
						Speak:         speakEnabled,
						HideAnswered:  hideAnswered,
						BuildIdentity: buildIdentity,
					},
				); err != nil {
					return errors.Wrap(ctx, err, "render page failed")
				}
				resp.Header().Set(
					libhttp.ContentTypeHeaderName,
					libhttp.TextHTML+"; charset=utf-8",
				)
				if _, err := resp.Write(body.Bytes()); err != nil {
					return errors.Wrap(ctx, err, "write response failed")
				}
				return nil
			},
		),
	)
}
