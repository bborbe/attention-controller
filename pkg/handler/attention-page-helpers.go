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
	// Info is the single gate for both the info-toggle and its panel, so the
	// control and the panel it opens cannot disagree about whether this row
	// carries machine identity. It is the implementation of
	// [[Attention Item Schema]] silence 26's placement rule — the ask leads,
	// the machine identity relocates behind the affordance — rather than a
	// restatement of it: a row with none of these values renders neither.
	row.Info = item.ProducerID != "" ||
		item.ProducerKind != "" ||
		provenance.Host != "" ||
		provenance.Cwd != "" ||
		provenance.Tool != "" ||
		provenance.Pane != "" ||
		provenance.PaneRecorded ||
		item.State != "" ||
		!item.CreatedAt.Time().IsZero()
	row.Meta = infoMetaLine(item)
	// The navigation line renders each distinct value once. Clearing the field
	// rather than adding a second flag is deliberate: the template's div gate and
	// its session-name span both read this one field, so they cannot disagree
	// about whether the line renders or what it carries.
	row.Provenance.SessionName = navigationSessionName(row.Provenance)
	// The Allow / Deny pair renders only for a headless worker's park. A tab
	// worker's gate is answered by pressing the prompt in the session's own pane,
	// so a board verdict there would be permission laundering — the exact failure
	// the schema forbids — while a headless worker has no pane of its own, so the
	// board is the one place its gate can honestly be answered. The fact is read
	// fail-closed from the supervisor's spawn ledger: every uncertainty about a
	// session's mode (an absent record, an unreadable directory, an unparseable
	// file, an unrecognised mode) leaves it false and renders no control, whose
	// worst case is a missing control rather than a control on a tab worker's gate.
	//
	// ⚠️ The pair is additionally withheld when the item's task declares a
	// production-touching step. Such a park is irreversible, and a one-click board
	// approval of it is the harm the exclusion exists to prevent. The fact is read
	// from the task file the resolver already opens — no second read and no new
	// scan — and it fails **open**, the opposite polarity to the Headless term
	// beside it: an absent, unreadable or unparsable task file and an absent marker
	// all leave it false, which renders the pair. The worst case of that direction
	// is a pair on a park whose task did not declare one, never a missing pair on a
	// park that did.
	row.Decide = item.AnswerMechanism == pkg.PermissionAnswerMechanism &&
		provenance.Headless &&
		!provenance.ProductionTouching
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

// infoMetaLine renders the panel's state-and-timestamp line, and is empty when
// the item carries neither. ⚠️ It is a derived string rather than a template
// expression over the item's own fields because libtime.DateTime is a struct:
// a zero value is truthy in a Go template, so `{{if .Item.CreatedAt}}` can
// never test for absence, and its String() renders a zero value as
// `0001-01-01T00:00:00Z` rather than "". Deriving here is what lets the panel
// draw no element for an absent value, per the schema's absence rule.
func infoMetaLine(item pkg.Item) string {
	state := item.State.String()
	created := item.CreatedAt.String()
	switch {
	case item.State == "" && item.CreatedAt.Time().IsZero():
		return ""
	case item.CreatedAt.Time().IsZero():
		return state
	case item.State == "":
		return created
	default:
		return state + " - " + created
	}
}

// navigationSessionName is the value the session-name span renders: the
// registry name the resolver returned, except when it repeats the task title
// the task link already carries, in which case it is empty and the span
// renders nothing.
//
// ⚠️ The two are different fields that coincide whenever a session is named
// after its task — this vault's own `/rename <task name>` convention — so the
// busiest cards are the ones that pay it. The task link carries the value; the
// span is what goes, because hiding the link behind the affordance is the
// alternative the schema rejected. A name that differs from the title is
// returned unchanged, so no navigation value is dropped.
func navigationSessionName(provenance pkg.Provenance) string {
	// ⚠️ Compare GLYPH-STRIPPED, and the raw comparison is the defect this
	// replaces. The registry name may carry Claude Code's leading status glyph
	// (`⚙ …`) while the task title never does, so `SessionName == TaskName` was
	// false for exactly the case this exists for — a session named after its
	// task — and the card rendered that title twice anyway. Measured on the live
	// board 2026-10-03: the second occurrence read
	// `⚙ An Attention Card's Task Name Renders Twice Below the Ask and the
	// Header Text Overlaps the Corner Icons` beside a task link carrying the
	// same title without the glyph.
	//
	// pkg.StripStatusGlyph is the same stripper the pane-ownership comparison
	// uses, deliberately: the reader and the page must not drift on what "the
	// name" is. The value returned is the registry name as given — the glyph is
	// the session's own decoration and is kept whenever the span renders at all.
	if pkg.StripStatusGlyph(provenance.SessionName) == pkg.StripStatusGlyph(provenance.TaskName) {
		return ""
	}
	return provenance.SessionName
}

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

// attentionPageStyles is the board's stylesheet, split out of the template in
// attention-page.go for the reason this file exists at all: that file sits at
// revive's 2000-line file-length limit, and the template is one raw string that
// cannot shed part of itself. The block is concatenated back into the document at
// the point it was cut from, so the served bytes are unchanged.
//
// ⚠️ It is a separate const rather than a file behind //go:embed on purpose: the
// html/template engine strips CSS comments from the served page, and an embed
// would move that stripping out of the engine's hands.
const attentionPageStyles = `
/* Dark theme, matching the tts-mcp page this store's UI is modelled on — same
   token names and values, so the two surfaces read as one family. The
   color-scheme property is set so the scrollbar and any native control render
   dark too; without it the page is dark but the chrome around it stays light. */
:root {
  color-scheme: dark;
  --bg: #111418;
  --panel: #1a1f26;
  --border: #2a3038;
  --text: #e8edf2;
  --muted: #8b95a3;
  --warn: #d08b5b;
  /* The two green tokens are the only addition to the palette, and they exist
     for one control: Submit answer is the forward move, so it reads as the
     affirmative one beside a muted Dismiss. They are tokens rather than
     literals so the pair stays one decision. */
  --green: #8fd39a;
  --green-bg: #2c4a37;
  /* The corner band. The card's four corner controls occupy its rightmost
     148px within its top 38px: the X at right: 12px, the read-aloud control at
     right: 48px, the jump corner at right: 84px and the info toggle at
     right: 120px, each 28px wide and 28px tall at top: 10px. A card's text
     reserves that band on its own right so it wraps before the controls rather
     than running through them. It mirrors the controls' geometry rather than
     driving it, exactly as each control's own right offset does: moving a
     control cannot move another, and this token cannot move one either. */
  --corner-band: 148px;
}
* { box-sizing: border-box; }
body {
  margin: 0 auto;
  max-width: 760px;
  padding: 24px;
  background: var(--bg);
  color: var(--text);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}
h1 { font-size: 20px; margin: 0 0 16px; }
ul.items { list-style: none; padding: 0; margin: 0; }
/* The board's own view control, above the first card. It is a control on the
   PAGE rather than on a card, which is why it appears in no row of the
   schema's control-set table: it renders no item, writes no field and causes
   no transition — it decides which of the rows the store already returned are
   drawn. */
.board-controls { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin: 0 0 16px; }
/* An answered item stays on the board as a dimmed record rather than
   disappearing: the operator asked to see what they answered, and the record is
   the only place the answer standing in their name is visible. It leaves when
   the item is closed. */
.item.dimmed {
  opacity: .5;
}
.item.dimmed .record-question {
  font-weight: 600;
}
.item.dimmed .record-answer {
  margin-top: .25rem;
}
li.item {
  position: relative;
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 16px;
  margin-bottom: 12px;
}
.producer {
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  letter-spacing: 0.04em;
}
/* The corner X. It is the card's whole skip affordance, so it takes the
   header's right edge and stays legible at a glance rather than reading as
   one more control in the row. Anchored to the card, which is positioned, so
   it lands in the corner of the card however tall the card grows — a tall
   multi-question card puts its Dismiss button far below the fold, and the
   corner is exactly what the operator asked for instead. */
.corner-x {
  position: absolute;
  top: 10px;
  right: 12px;
  width: 28px;
  height: 28px;
  padding: 0;
  background: transparent;
  color: var(--muted);
  border: 1px solid transparent;
  border-radius: 6px;
  font-family: inherit;
  font-size: 15px;
  line-height: 1;
  cursor: pointer;
}
.corner-x:hover { color: var(--text); border-color: var(--border); }
.payload { font-size: 15px; line-height: 1.45; margin: 6px 0 8px; white-space: pre-wrap; }
.provenance {
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  margin: 0 0 6px;
}
/* One separator between spans, so the line reads as one sentence without the
   template emitting trailing separators for values that were omitted. */
.provenance span + span::before { content: " · "; }
.provenance .unroutable { color: var(--warn); }
/* The explanation a row carries when it has no jump control at all. Muted
   rather than warned: a designed absence is not a fault, and colouring it like
   one would put the board back where the operator could not tell the two
   apart — the defect this line exists to remove.

   ⚠️ Its own class rather than the control's, so a row that renders this and a
   row that renders the control stay distinguishable in the DOM as well as on
   screen: the two are the two arms of one condition, and reusing the control's
   class would make "this row has a jump control" unanswerable by class.
   ⚠️ AMENDED 2026-09-27: the two arms are no longer mutually exclusive. A row
   that renders this explanation now also renders the corner control, present
   but disabled, so the operator finds it in the same place on every card —
   [[Attention Item Schema]] silence 20's resolution. The classes stay
   distinct and the distinction still reads the same way in the DOM, but
   "this row has a jump control" is now answered by a :not(:disabled) test on
   the corner control rather than by the presence of the corner at all. */
.jump-reason .no-jump { color: var(--muted); }
.meta { color: var(--muted); font-size: 12px; }
/* The info toggle and its panel. The control sits in the corner cluster's next
   free slot: the corner X is pinned at right: 12px, the read-aloud control at
   right: 48px and the jump corner at right: 84px, all 28px wide with an 8px gap,
   so right: 120px places this control 8px to the jump corner's left and no
   control's position depends on another's. It mirrors that geometry rather than
   sharing a rule with it, so moving one control cannot move another. The
   .info-panel span + span::before rule re-expresses the provenance line's
   separator for the machine spans relocated here. */
.info-toggle {
  position: absolute;
  top: 10px;
  right: 120px;
  width: 28px;
  height: 28px;
  padding: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  color: var(--muted);
  border: 1px solid transparent;
  border-radius: 6px;
  font-family: inherit;
  font-size: 15px;
  line-height: 1;
  cursor: pointer;
}
.info-toggle:hover { color: var(--text); border-color: var(--border); }
.info-toggle[aria-expanded="true"] { color: var(--text); border-color: var(--border); }
.info-panel {
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  margin: 8px 0 0;
}
.info-panel span + span::before { content: " · "; }
.info-panel .unroutable { color: var(--warn); }
.empty { color: var(--muted); font-size: 14px; }
/* The answer card, rendered for message items only. The context line carries
   the background the producer declared, kept separate from the question so the
   ask stays readable on its own at the top of the row. */
.context { color: var(--muted); font-size: 13px; line-height: 1.45; margin: 0 0 8px; white-space: pre-wrap; }
/* One tab per question of a multi-question item. The active tab is outlined
   rather than filled, so the strip reads as a set of labels with one selected
   rather than as a row of buttons competing with Submit answer. */
.tabs { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 20px; }
.tab {
  background: transparent;
  color: var(--muted);
  border: 1px solid transparent;
  border-radius: 9px;
  padding: 7px 15px;
  font-size: 14px;
  font-family: inherit;
  cursor: pointer;
}
.tab:hover { color: var(--text); }
.tab.active { color: var(--text); background: var(--bg); border-color: var(--muted); }
/* A multi-question item's own payload: the card's title rather than a question,
   so it is muted and the tabs carry the questions. */
.card-title { color: var(--muted); font-size: 15px; line-height: 1.45; margin: 0 0 16px; white-space: pre-wrap; }
/* The question line. The cardinality hint rides on it in the producer's own
   wording, so the operator reads what the control will accept before using it. */
.question { font-size: 17px; font-weight: 600; line-height: 1.4; margin: 0 0 20px; }
.question .hint { font-weight: 400; color: var(--muted); }
/* The card's text reserves the corner band on its right, so a block that
   renders in the band wraps before the controls instead of painting through
   them. li.item's own 16px padding already keeps text 16px clear of the
   card's right edge, so a block needs the remaining 132px of the 148px band,
   plus 4px of clearance so the two boxes do not merely touch — written as the
   band minus 12px so the value follows the controls' geometry rather than
   restating it.

   It is scoped to the text blocks rather than to li.item, so the card's box,
   its border, the corner controls and the info panel keep their full width,
   and a block that renders below the band is not narrowed by a rule meant for
   the band. ⚠️ It is margin-right rather than padding-right on purpose: a
   block's bounding box is its border box, so padding-right would leave the box
   spanning the band while only the text moved out of it, and a geometry check
   comparing the ask's box against the controls' would still report an
   intersection the layout does not have. */
.provenance,
.payload,
.context,
.card-title,
.question,
.record {
  margin-right: calc(var(--corner-band) - 12px);
}
.options { display: flex; flex-direction: column; gap: 18px; margin: 0 0 22px; }
.option { display: flex; align-items: flex-start; gap: 12px; cursor: pointer; }
/* The control is aligned to the label's first line rather than to the row, so
   the label and its muted cost line read as one block beside it.
   ⚠️ Direct child, not a descendant: the Other option nests a text field inside
   its own body, and a descendant selector would size that field 16×16 too. */
.option > input { flex: none; width: 16px; height: 16px; margin: 3px 0 0; accent-color: var(--muted); }
.option-body { flex: 1 1 auto; min-width: 0; }
.option-label { display: block; font-size: 15px; line-height: 1.4; }
.option-label .recommended { color: var(--muted); }
.option-desc { display: block; color: var(--muted); font-size: 14px; line-height: 1.45; margin-top: 4px; }
.other {
  width: 100%;
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 9px;
  padding: 13px 15px;
  font-size: 14px;
  font-family: inherit;
  margin: 0 0 20px;
}
.other::placeholder { color: var(--muted); }
/* Inside the group the field is the Other option's own control, not a sibling
   of the group: the label above it supplies the separation, and the group's own
   bottom margin supplies the space below. The rule above is what the
   no-options path still uses, where the field does stand alone. */
.option .other { margin: 8px 0 0; }
.actions { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; }
.actions button {
  font-family: inherit;
  font-size: 14px;
  border: 1px solid var(--border);
  border-radius: 9px;
  padding: 9px 18px;
  cursor: pointer;
}
/* Dismiss is the skip, so it is muted and Submit answer carries the colour:
   the one forward move should be the one that reads as the affirmative. */
.actions .dismiss { background: transparent; color: var(--muted); }
.actions .dismiss:hover { color: var(--text); border-color: var(--muted); }
.actions .next { background: var(--green-bg); color: var(--green); border-color: var(--green-bg); font-weight: 500; }
.actions .next:hover { border-color: var(--green); }
/* Read aloud is a utility, not a decision, so it does not sit in the actions
   row beside the two controls that answer the card — it sits in the card's
   top-right corner beside the X. That leaves the actions row carrying exactly
   the two choices the card offers, which is what the row is for.
   Its geometry mirrors the corner X deliberately rather than sharing a rule:
   the X is pinned at right: 12px with a 28px width, so right: 48px places this
   8px to its left, and neither control's position depends on the other's. */
.speak {
  position: absolute;
  top: 10px;
  right: 48px;
  width: 28px;
  height: 28px;
  padding: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  color: var(--muted);
  border: 1px solid transparent;
  border-radius: 6px;
  cursor: pointer;
}
.speak:hover { color: var(--text); border-color: var(--border); }
/* The control is a toggle now, so its two states have to be told apart at a
   glance. The Style Guide's rule is the reason: "a button that looks live and
   does nothing is the failure the operator cannot debug from the page" — and
   its inverse is the one that bites here, a control that looks idle while it
   is the only thing on the page that can stop the audio. Only the colour and
   the border move: the glyph, the size and the corner placement are untouched,
   so "nothing else about the control changes" still holds. The green is the
   palette's existing affirmative, the same token the actions row uses. */
.speak[data-state="speaking"] { color: var(--green); border-color: var(--green); }
.speak[data-state="speaking"]:hover { color: var(--green); border-color: var(--green); }
/* The speaker glyph is inline SVG, not a font character: the page loads no web
   fonts, so a glyph taken from the system font would vary by platform and
   family. It inherits the button's own muted colour through currentColor, so it
   adds no colour to the palette. The svg is aria-hidden and focusable="false"
   so it contributes no second accessible name — and here that matters more than
   it did beside a text label, because the control is icon-only: its aria-label
   is the only name it has. */
.speak .speak-icon { width: 15px; height: 15px; flex: none; }
/* The jump handover's clickable half, in the corner beside the X and the
   speaker. Icon-only for the same reason the speaker is, and in the corner for
   the reason the X gives: a control anchored to the card lands in the same
   place however tall the card grows, where the text button it replaces sat
   below the payload and moved with the card's content — the operator's ask,
   verbatim, 2026-09-27: "a button at the top next to the speaker symbol and
   the X ... a small icon and it's always at the same place."
   Its geometry mirrors the two controls to its right rather than sharing a
   rule with either: the X is pinned at right: 12px and the speaker at
   right: 48px, both 28px wide with an 8px gap, so right: 84px places this 8px
   to the speaker's left and no control's position depends on another's. */
.jump-corner {
  position: absolute;
  top: 10px;
  right: 84px;
  width: 28px;
  height: 28px;
  padding: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  color: var(--muted);
  border: 1px solid transparent;
  border-radius: 6px;
  cursor: pointer;
}
.jump-corner:hover { color: var(--text); border-color: var(--border); }
/* ⚠️ A disabled control must not look pressable — a button that looks live and
   does nothing is the failure the operator cannot debug from the page. The
   corner's value is that it is the same on every card, so a row with no
   handover renders this control present-but-unavailable rather than absent.
   ⚠️ It is the real disabled attribute and it carries NO data-jump, and both
   halves are load-bearing: the page's jump handler is delegated at the
   document, so a control that still dispatched would be matched by
   closest('button[data-jump]'), fetch a null URL, and reach showJumpNote with
   no .jump container to append into — a throw on a row that renders no
   explanation div at all. */
.jump-corner:disabled { cursor: default; opacity: 0.4; }
.jump-corner:disabled:hover { color: var(--muted); border-color: transparent; }
.jump-corner .jump-icon { width: 15px; height: 15px; flex: none; }
/* The acknowledge control is the only action a report-only card carries, so it
   takes the affirmative colour the message card gives Submit answer: on a card
   that offers no other move, it is the forward one. */
.actions .ack { background: var(--green-bg); color: var(--green); border-color: var(--green-bg); font-weight: 500; }
.actions .ack:hover { border-color: var(--green); }
/* The jump handover's copyable half: a command for the operator who wants to
   paste it.
   ⚠️ The clickable half left this div on 2026-09-27 — the button is the corner
   control now — so the div carries the command alone and is EMPTY on a
   message item, which has no command by design (jumpCommand returns "" for
   MessageAnswerMechanism). It still renders whenever the row carries a
   handover, and that is deliberate rather than incidental: this div is also
   the outcome note's home, and showJumpNote resolves it with
   row.querySelector('.jump') before appending, so a row that rendered the
   corner control without this container would throw on the first click. An
   empty div contributes no spacing. */
.jump { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; color: var(--muted); font-size: 12px; margin: 8px 0 0; }
.jump:empty { margin: 0; }
.jump code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 2px 6px;
  user-select: all;
}
/* The board's filter is a switch, not a button, because that is what it is: a
   control with two states that stays where it is, rather than one that fires an
   action and returns. The track carries the state in colour and the knob
   carries it in position, so "on" is legible from the page itself and not only
   from the label — a toggle whose state lives only in its wording is one the
   operator cannot read back. The knob stays light in both states, as the
   reference the operator supplied does; only the track changes colour, and it
   takes the same affirmative pair the card's Submit answer uses rather than
   adding one to the palette. */
.board-filter {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  background: transparent;
  border: none;
  padding: 0;
  font-family: inherit;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
}
.board-filter .switch { width: 44px; height: 24px; display: block; flex: none; }
.board-filter .switch-track {
  fill: var(--panel);
  stroke: var(--border);
  stroke-width: 2;
  transition: fill 120ms ease, stroke 120ms ease;
}
.board-filter .switch-knob { fill: var(--text); transition: transform 120ms ease; }
.board-filter:hover .switch-track { stroke: var(--muted); }
/* Ordered after :hover deliberately. The two rules have equal specificity, so
   source order is what stops the on-state colour being flattened on hover —
   the state has to outrank the pointer, not the other way round. */
.board-filter[aria-checked="true"] .switch-track {
  fill: var(--green-bg);
  stroke: var(--green);
}
.board-filter[aria-checked="true"] .switch-knob { transform: translateX(20px); }
/* The focus ring outranks the state deliberately — the opposite precedence from
   the hover rule above, and for the opposite reason. Hover is the pointer
   passing over, which must not obscure what the control is set to; focus is the
   keyboard's only affordance, and it has to be visible whatever the state is.
   The fill still carries the state, so nothing is lost by the stroke change. */
.board-filter:focus-visible .switch-track { stroke: var(--text); }
/* The board's own health, beside the filter switch: the switch says which rows
   are drawn, this says whether the rows still track the store. Warn colour and
   no new token — a stream that has stopped is a fault, not a state the board
   is designed to sit in. It is hidden in the served markup and shown only by
   the stream's own onerror, so a healthy board and a quiet board render
   nothing: a board that is merely quiet must still look quiet. */
.stream-stale { color: var(--warn); font-size: 13px; }
/* A control's outcome is shown, never swallowed into a reload — a silent catch
   reports a code fault as a connection problem. The class is "note" rather than
   "failed" because the read-aloud control reports success through it too (the
   tts message id), and a success line rendered in a failure style would read as
   an error. */
.note { color: var(--muted); font-size: 12px; margin: 8px 0 0; }
.note.failed { color: var(--warn); }
/* The board's own provenance. It is deliberately not a card and carries no
   control: it describes the BUILD, not an item, so it sits below the list and
   outside the row template the stream swaps. The separator rule is what makes
   it read as a footer rather than as one more thing the store returned.
   ⚠️ The monospace is on the values, not the line: a sha and a timestamp are
   read character by character when they are read at all, while the labels
   around them are prose. */
.build-identity { color: var(--muted); font-size: 12px; margin: 24px 0 0; padding-top: 12px; border-top: 1px solid var(--border); }
.build-identity .bi-value { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text); }
/* An identity the binary does not carry is a fault in the deploy, not a normal
   state, so it is warned rather than muted, the same treatment the board gives
   a stream that has stopped tracking the store.
   ⚠️ The class is "bi-absent", not "bi-unknown", and that is load-bearing rather
   than a preference. The page carries a standing guard that no provenance field
   renders an invented placeholder, and it asserts the document contains the
   literal string "unknown" nowhere. A class name is part of the document, so
   naming this one "unknown" reddens a guard about provenance from a footer that
   has nothing to do with it — which is exactly what it did, once. */
.build-identity .bi-absent { color: var(--warn); }
`
