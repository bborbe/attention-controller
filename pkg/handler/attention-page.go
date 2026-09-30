// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"html/template"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// attentionPageTemplate is the whole page: one document, inline styles, no
// external stylesheet, no framework, no build step. It lives here as a string
// constant so the page ships inside the binary rather than as an asset.
//
// The document renders answer controls for `message` items, and a jump handover
// for every item whose session resolves to a pane. ⚠️ Both reverse the decision
// this page was built with, which was that the document is inert — "no <form>,
// no submitting <button>, no <script> and no fetch/XHR: the page only reads, so
// an answer or close control would be a defect rather than a missing feature".
//
// The reversal is bounded by the schema's who-answers-what ruling rather than
// by taste. A `message` item is `pick`-shaped, so a control that gives a `pick`
// answer is a legal way to answer it. A `permission` item is `approve`-shaped
// and **only the operator may answer it, in the session that raised it** — an
// answer control there would be the permission laundering the schema forbids —
// so a `permission` item renders no answer controls at all. The schema's
// § Answer routing records the board as a second arm
// (`answered_by: attention-board`) and states this reversal; silence 12 is the
// field change that made it possible.
//
// The card is shaped by the declared cardinality rather than by taste: a
// question that allows several picks renders checkboxes and one that allows a
// single pick renders radio buttons, both read from the producer's
// AnswerCardinality and never inferred from the option count — a one-option
// question and a many-option single-pick question carry lists of different
// lengths and ask for different things, so a board that read the length would
// render a checkbox for a question admitting one answer. An item carrying
// `questions` renders one tab per question, each with its own payload, options
// and control; a single-question item renders one card with no tab strip.
//
// ⚠️ The jump handover is a button as well as a copyable command, and that
// reverses a second decision recorded here. It was a command only, on the
// reasoning that "a browser cannot activate a WezTerm tab, so an anchor here
// would present a value as resolved that is not". That premise is false against
// the fleet-jump server, which activates a pane from a plain HTTP GET — proven
// end-to-end on 2026-09-24 — so a link does resolve. The command stays, because
// an operator may still want to paste it, and the button is added beside it.
//
// The button's target is a path on *this* board, never the fleet-jump URL: that
// URL carries the shared token as a query parameter, and the token must not
// reach the browser. The path is called in the background, and the board
// performs the jump server-side, so the token stays on the server and out of
// the served document — and the click leaves the page where it was, which is
// the behaviour the operator asked for.
//
// Rendering goes through html/template, which escapes every interpolated value
// for the context it lands in. ProducerID is a producer-supplied free string
// (validated only by NotEmptyString) and the provenance values are read from
// the producer's own event log, so that escaping is what keeps a producer from
// injecting markup into the reader's browser.
//
// The provenance line renders one span per resolved value and omits the rest.
// ⚠️ An unresolved value is *omitted*, never filled with a placeholder — a
// stand-in like `unknown` or `n/a` would be an unresolvable value presented as
// resolved, which [[Attention Item Schema]] § Silence 7 forbids. A row with no
// resolvable provenance at all renders no provenance line, which is exactly
// what this page rendered before the change.
//
// ⚠️ The task name leads the line, drawn as a link that opens the task in
// Obsidian — the one fact the card had always lacked, since the host, cwd, tool
// and pane all say *where* a session ran and none says *what* it was working on.
// It is wrapped in its own span rather than inserted as a bare anchor, and the
// wrapper is required rather than cosmetic: the line's separators come from a
// rule matching only *adjacent* spans (`.provenance span + span::before`), so a
// bare `<a>` among the spans would suppress the separator beside it and the line
// would render as `Fix the boardburn · /w/x`. It is navigation, so it adds no
// control and changes nothing any card offers.
//
// ⚠️ The goal and the topic follow the task as two more spans of the same shape,
// each gated on its own resolved link: the goal this item's task names first, and
// the topic page that lists that goal under its `## Goals` heading. They are
// gated independently rather than by one condition over both, because a task
// carrying a goal no topic lists is the dominant live case — a single gate would
// drop that goal link along with the absent topic. An unresolved one renders
// absent rather than as a placeholder, the rule the rest of the line already
// follows: a goal no topic lists draws its goal span and no topic span, and a
// task naming no goal draws neither. They are navigation too, adding no control
// and changing nothing any card offers.
const attentionPageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Attention</title>
<style>
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
.options { display: flex; flex-direction: column; gap: 18px; margin: 0 0 22px; }
.option { display: flex; align-items: flex-start; gap: 12px; cursor: pointer; }
/* The control is aligned to the label's first line rather than to the row, so
   the label and its muted cost line read as one block beside it. */
.option input { flex: none; width: 16px; height: 16px; margin: 3px 0 0; accent-color: var(--muted); }
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
</style>
</head>
<body>
<h1>Attention</h1>
{{/* Rendered whether or not the board has rows. A control that appears and
     disappears as the store changes would move under the operator's cursor,
     and an empty board is a state the board is in often. With no rows to
     filter it is inert rather than absent.
     The label names the set it hides rather than the set it leaves, because
     that is the half the ruling settled: it hides the dimmed answered records
     and keeps every permission card, which is actionable but carries no
     answering control. "Only answerable" would read as excluding them.
     ⚠️ The label stays VISIBLE beside the switch. v0.18.0 rejected an
     icon-only read-aloud control because it would have moved the control's
     meaning into a hover-only title plus an aria-label only assistive tech
     sees, and this board's governing complaint was cards whose controls were
     not discoverable at all. A bare switch here would repeat that mistake —
     the operator would have to remember what it does. The switch carries the
     STATE and the label carries the MEANING; neither substitutes for the
     other. role="switch" with aria-checked is the shape a two-state control
     is meant to have, and it replaces the aria-pressed this control shipped
     with in v0.20.0.
     ⚠️ The row also carries the board's stream health: a span saying the board
     has stopped tracking the store and is showing the last state it received.
     It sits here rather than on a card because it describes the BOARD, not an
     item — the same reason the switch sits here. It ships hidden and is shown
     only by the stream's own onerror, so a healthy board, a quiet board and an
     empty board all render exactly what they rendered before. */}}
<div class="board-controls"><button type="button" class="board-filter" data-board-filter role="switch" aria-checked="{{if .HideAnswered}}true{{else}}false{{end}}"><svg class="switch" viewBox="0 0 44 24" aria-hidden="true" focusable="false"><rect class="switch-track" x="1" y="1" width="42" height="22" rx="11"/><circle class="switch-knob" cx="12" cy="12" r="8"/></svg><span class="switch-label">Hide answered</span></button><span class="stream-stale" data-stream-stale hidden>Not tracking the store - showing the last known state.</span></div>
{{if .Items}}<ul class="items">
{{range .Items}}{{template "attention-row" .}}{{end}}</ul>
{{else}}<p class="empty">Nothing needs attention.</p>
{{end}}
{{/* The board's own provenance: the answer to "which build am I looking at",
     on the surface a human is already reading. It exists because that question
     was answered twice in one turn from the file's mtime and the two readings
     disagreed — a filesystem fact standing in for an artifact identity, which
     is the wrong answer to it whenever the checkout and the binary have
     diverged.
     ⚠️ Every value is the BINARY's own VCS stamp, read at startup. A value read
     from the repo at render time would answer "which commit is the checkout
     at" — a different question, and one that agrees with this footer right up
     until the day the two disagree, which is the day the answer matters.
     ⚠️ The absent state renders EXPLICITLY, never as a placeholder. A build
     carrying no stamp must say so: a dev default, an empty span and a
     plausible-looking sha are all answers a reader would take at face value.
     The three fields are
     labelled rather than bare so that version and commit reading identically —
     true of any build given no release tag — reads as the fact it is, not as a
     rendering fault. See pkg/buildidentity. */}}
<footer class="build-identity">{{if .BuildIdentity.Known}}version <span class="bi-value">{{.BuildIdentity.Version}}</span> · commit <span class="bi-value">{{.BuildIdentity.Commit}}</span> · committed <span class="bi-value">{{.BuildIdentity.CommitTime}}</span>{{else}}<span class="bi-absent">No build identity: this binary carries no VCS stamp, so it cannot say which source it was built from.</span>{{end}}</footer>
<script>
/* A throw outside the four handled paths — a listener that dereferences a node
   the stream replaced, a promise chain someone adds later without a catch —
   reaches the console only if devtools happens to be open when it fires. These
   two handlers write it there unconditionally, so a card that misbehaves leaves
   a trail the operator can read back afterwards.
   They log and nothing else. An uncaught error has no form and no row to host a
   note, so surfacing one on the page would be a new visible element on a path
   that renders none today. Registered at the top level rather than inside a
   handler, so a throw before the first click is caught too.
   window.onerror is given (message, source, lineno, colno, error); the error
   object is preferred where the browser supplies one and the message is the
   fallback, because the object carries the stack the message alone loses. */
window.onerror = function (message, source, lineno, colno, error) {
  console.error(error || message);
};
/* Every promise chain in this script carries its own .catch, so nothing escapes
   to this handler today. It is registered for parity with window.onerror rather
   than because a rejection currently reaches it — code added later may reject
   without one, and a silent rejection is the failure this pair exists to end. */
window.addEventListener('unhandledrejection', function (event) {
  console.error(event.reason);
});
/* Tabs switch which question panel is visible. Nothing reloads and no panel is
   re-rendered: every panel ships in the document and only its visibility
   changes, so a half-typed Other field survives a look at the other question.
   ⚠️ Delegated on the document, not bound per button — the defect and the fix
   are the read-aloud control's below; this strip was left on the old binding and
   kept rendering while doing nothing after one row re-render. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var tab = event.target.closest('button[data-tab]');
  if (!tab) { return; }
  var tabs = tab.closest('.tabs');
  var row = tabs && tabs.closest('li.item');
  if (!row) { return; }
  tabs.querySelectorAll('button[data-tab]').forEach(function (other) {
    other.classList.toggle('active', other === tab);
  });
  row.querySelectorAll('.panel').forEach(function (panel) {
    panel.hidden = panel.getAttribute('data-question') !== tab.getAttribute('data-tab');
  });
});
/* Answer controls exist for message items only, and the form is intercepted so
   a failed answer is shown rather than swallowed into a reload: a bare catch
   that reloads anyway reports a code fault as a connection problem.
   ⚠️ Delegated on the document for the tab strip's reason, and measured too: on
   a re-rendered row Dismiss left the item open while the card still drew its
   buttons. A submit event bubbles, so the document is a valid host for it. */
document.addEventListener('submit', function (event) {
  var form = event.target && event.target.closest ? event.target.closest('form.answer') : null;
  if (!form) { return; }
  event.preventDefault();
  var button = event.submitter;
  var kind = button ? button.value : 'send';
  var multi = form.getAttribute('data-multi') === 'true';
  /* automation carries the page's own navigator.webdriver reading, which only
     this script can read. It is the one answered-client member the body may
     carry and explicitly the weaker one; user_agent and remote_addr are read
     by the store from the request and a body cannot set either. It is omitted
     rather than sent as false when the browser does not report it. */
  var request = { answered_by: 'attention-board', automation: navigator.webdriver };
  if (kind === 'skip') {
    /* Dismiss declines the whole card, so a multi-question item records a
       declined answer on every tab rather than on none. */
    if (multi) { request.answers = skipAll(form); } else { request.answer = { kind: 'skip' }; }
    sendAnswer(form, request);
    return;
  }
  var entries = collectAnswers(form);
  if (!entries.length) {
    showNote(form, 'Pick an option or write an answer first.', true);
    return;
  }
  if (multi) { request.answers = entries; } else { request.answer = singleAnswer(entries[0]); }
  sendAnswer(form, request);
});
/* singleAnswer maps one collected entry onto the item-level answer shape.
   It carries values when the question took several picks: a single-question item
   declared multiple has no other field for them, so sending only value would
   drop every pick but the first, and the store rejects the resulting body. */
function singleAnswer(entry) {
  var answer = { kind: entry.kind };
  if (entry.values) { answer.values = entry.values; }
  else if (entry.value) { answer.value = entry.value; }
  return answer;
}
/* collectAnswers reads one entry per question the operator actually answered. A
   question left alone contributes nothing: an entry there would read back as a
   value where the operator gave none. */
function collectAnswers(form) {
  var entries = [];
  form.querySelectorAll('.panel').forEach(function (panel) {
    var question = panel.getAttribute('data-question') || '';
    var picks = [];
    panel.querySelectorAll('input:checked').forEach(function (input) {
      picks.push(input.getAttribute('data-option') || '');
    });
    var other = panel.querySelector('input[name=text]');
    var text = other ? other.value.trim() : '';
    if (text) {
      entries.push({ question: question, kind: 'text', value: text });
      return;
    }
    if (!picks.length) { return; }
    /* The carrier is fixed by the question's declared cardinality, not chosen
       here: a single-pick question carries value, a multi-pick one carries
       values. Joining the picks into one string would be lossy — a label
       containing ", " would read back as two picks — and the store rejects an
       entry using the field its question's cardinality does not name. */
    if (panel.getAttribute('data-multi-pick') === 'true') {
      entries.push({ question: question, kind: 'option', values: picks });
    } else {
      entries.push({ question: question, kind: 'option', value: picks[0] });
    }
  });
  return entries;
}
/* A permission card's Allow / Deny. It writes the verdict as decision, the
   field the schema gives a permission answer; the row updates over the stream
   like every other answer, and a failure is shown on the card rather than
   swallowed. Delegated on the document for the tab strip's reason: a row the
   stream re-renders keeps working. */
document.addEventListener('click', function (event) {
  var button = event.target && event.target.closest ? event.target.closest('button[data-decision]') : null;
  if (!button) { return; }
  var row = button.closest('li.item');
  if (!row) { return; }
  button.disabled = true;
  fetch('/api/1.0/attention/' + encodeURIComponent(row.getAttribute('data-item-id')) + '/answer', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ answered_by: 'attention-board', automation: navigator.webdriver, decision: button.getAttribute('data-decision') })
  }).then(function (response) {
    if (response.ok) { return; }
    return response.text().then(function (body) {
      button.disabled = false;
      showCloseNote(row, answerFailure(body), true);
    });
  }).catch(function (error) {
    button.disabled = false;
    showCloseNote(row, 'Could not reach the store: ' + error, true);
  });
});
function skipAll(form) {
  var entries = [];
  form.querySelectorAll('.panel').forEach(function (panel) {
    entries.push({ question: panel.getAttribute('data-question') || '', kind: 'skip' });
  });
  return entries;
}
function sendAnswer(form, request) {
  fetch('/api/1.0/attention/' + encodeURIComponent(form.closest('li.item').getAttribute('data-item-id')) + '/answer', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request)
  }).then(function (response) {
    if (response.ok) { showNote(form, 'Answer sent.', false); return; }
    return response.text().then(function (body) {
      var failure = answerFailure(body);
      if (failure.leftQueue) {
        /* The item left the queue between this page being drawn and this answer
           arriving — a lost race against the producer's exit, not a malformed
           request. The line is shown AND the page returns to the queue, in that
           order and with a beat between them. Reloading first would swallow the
           outcome into a reload, which the rule above forbids; staying put would
           leave a card in front of the operator for an item that no longer
           exists, which is the defect this branch exists to fix. Both halves
           are the point, so neither is dropped. */
        showNote(form, failure.message, true);
        console.error('attention board: answer failed - HTTP ' + response.status, body);
        window.setTimeout(function () { window.location.reload(); }, 2500);
        return;
      }
      showNote(form, 'Answer failed - HTTP ' + response.status + ' - ' + body, true);
      console.error('attention board: answer failed - HTTP ' + response.status, body);
    });
  }).catch(function (error) {
    showNote(form, 'Answer failed - ' + String(error), true);
    console.error('attention board: answer failed - ' + String(error));
  });
}
/* answerFailure reads the store's error envelope and reports whether the item
   had already left the queue. Only that one code is special-cased: every other
   failure keeps the raw body, because the body is what a reader needs to tell a
   code fault from a connection problem. A body that is not JSON at all — a
   proxy's error page, a truncated response — reads as "not this case" rather
   than throwing, so the caller still reaches its raw-body line. */
function answerFailure(body) {
  var envelope;
  try { envelope = JSON.parse(body); } catch (error) { return { leftQueue: false }; }
  var failure = envelope && envelope.error;
  if (!failure || failure.code !== 'ITEM_CLOSED') { return { leftQueue: false }; }
  var closedAt = (failure.details && failure.details.closed_at) || '';
  return {
    leftQueue: true,
    message: closedAt
      ? 'This item was already closed at ' + closedAt + ' - it left the queue before this answer arrived. Returning to the queue.'
      : 'This item had already left the queue before this answer arrived. Returning to the queue.'
  };
}
function showNote(form, message, isError) {
  var previous = form.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('div');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  form.appendChild(note);
  rememberFailure(form, 'form', message, isError);
}
/* The read-aloud control is a TOGGLE: a click while its own item is playing
   stops that playback, and a click when nothing of its own is playing starts a
   reading. It is type="button" so it never submits the answer form.
   Both the item id and the note's home are found from the ROW, never from a
   form ancestor: the control sits in the card's top-right corner, outside the
   answer form, so a form-ancestor lookup here returns null and this handler
   throws before it ever fetches. The corner X reads the row the same way, for
   the same reason.

   ⚠️ Delegated on the document, NOT bound per button — for the same reason the
   Jump control is, and the reason is a defect rather than a preference. The
   stream replaces a row's whole outerHTML on every event (upsertRow), and a
   listener attached to the old node dies with it. A per-button binding
   therefore leaves every re-rendered card's read-aloud control inert while
   still rendering it correctly: the button is there, the click does nothing,
   no request is made, and no note appears. That is the failure this repo
   already shipped once at v0.19.0, and the Jump control was converted to
   delegation to fix its half of it — the read-aloud control was left behind
   until 2026-09-27, when the permission-card ruling made the inert case
   routine rather than occasional.

   ⚠️ What this page is playing lives HERE, in the script's own scope, and NOT
   on the button. upsertRow replaces a row's whole outerHTML, so a message id
   written onto the control dies with the node it was written to — the same
   defect one layer down, and the reason the repaint is re-applied from the map
   rather than trusted to survive. Two consequences worth stating:

   The click handler decides from the MAP, never from the button's data-state,
   so what the control DOES is right even in the window before the repaint
   lands. And the map is declared in this outer scope rather than inside the
   stream's IIFE because both halves need it: the handler below reads it, and
   upsertRow re-applies it after a swap. It does not survive a full page
   reload — that limit, and why it was accepted, is recorded in the task's
   # Results. */
var speaking = {};
/* The read-aloud control for an item, found by comparing data-item-id rather
   than by building a selector — the same reason the stream's own findRow
   gives: a generated id is not the place to rely on. It is a second lookup
   rather than a call to findRow because findRow lives inside the stream's IIFE
   below, and this scope cannot reach it. */
function speakControl(itemID) {
  var rows = document.querySelectorAll('li.item');
  for (var i = 0; i < rows.length; i++) {
    if (rows[i].getAttribute('data-item-id') === itemID) {
      return rows[i].querySelector('button[data-speak]');
    }
  }
  return null;
}
function speakButtonState(button, isSpeaking) {
  if (!button) { return; }
  if (isSpeaking) {
    button.setAttribute('data-state', 'speaking');
    button.setAttribute('aria-label', 'Stop reading');
    button.setAttribute('title', 'Stop reading');
    return;
  }
  button.removeAttribute('data-state');
  button.setAttribute('aria-label', 'Read aloud');
  button.setAttribute('title', 'Read aloud');
}
function startReading(itemID, noteHost) {
  fetch('/api/1.0/attention/' + encodeURIComponent(itemID) + '/speak', { method: 'POST' })
    .then(function (response) {
      return response.text().then(function (body) {
        if (!response.ok) {
          showNote(noteHost, 'Read aloud failed - HTTP ' + response.status + ' - ' + body, true);
          console.error('attention board: read aloud failed - HTTP ' + response.status, body);
          return;
        }
        /* The id is what makes the next click a stop, so a response that
           carries none leaves the control a start button rather than marking
           it as playing and stranding the operator. */
        var messageID = '';
        try { messageID = (JSON.parse(body) || {}).message_id || ''; } catch (error) { messageID = ''; }
        if (messageID) {
          speaking[itemID] = messageID;
          speakButtonState(speakControl(itemID), true);
        }
        showNote(noteHost, 'Reading aloud (' + body + ')', false);
      });
    }).catch(function (error) {
      showNote(noteHost, 'Read aloud failed - ' + String(error), true);
      console.error('attention board: read aloud failed - ' + String(error));
    });
}
function stopReading(itemID, messageID, noteHost) {
  fetch('/api/1.0/attention/' + encodeURIComponent(itemID) + '/cancel', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message_id: messageID })
  })
    .then(function (response) {
      return response.text().then(function (body) {
        /* Cleared on a 404 as well as on success: that status means the id
           aged out of the tts server's status table, so there is nothing left
           to stop and the reading is over either way. A control left showing
           "speaking" with nothing playing is the one-way control this toggle
           replaces. Only a genuine failure keeps the state, so a retry is
           still possible. */
        if (response.ok || response.status === 404) {
          delete speaking[itemID];
          speakButtonState(speakControl(itemID), false);
        }
        if (response.ok) {
          showNote(noteHost, 'Stopped reading', false);
          return;
        }
        if (response.status === 404) {
          showNote(noteHost, 'Stopped reading - it had already finished', false);
          return;
        }
        showNote(noteHost, 'Stop reading failed - HTTP ' + response.status + ' - ' + body, true);
      });
    }).catch(function (error) {
      showNote(noteHost, 'Stop reading failed - ' + String(error), true);
    });
}
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-speak]');
  if (!button) { return; }
  var row = button.closest('li.item');
  var itemID = row.getAttribute('data-item-id');
  var noteHost = row.querySelector('form.answer') || row;
  var messageID = speaking[itemID];
  if (messageID) {
    stopReading(itemID, messageID, noteHost);
    return;
  }
  startReading(itemID, noteHost);
});
/* ⚠️ The Jump control is a button, not a link, and the difference is the
   operator's ask: "so we dont switch the screen". A link navigates the browser
   to the fleet-jump server, taking the board out of view. This calls the
   board's own endpoint, which performs the jump server-side and answers 204 —
   so a click switches WezTerm and leaves this page exactly where it was. */
/* ⚠️ Delegated on the document, NOT bound per button — and the difference is a
   defect, not a preference. The stream below replaces a row's whole outerHTML
   on every event (upsertRow), and a listener attached to the old node dies
   with it. A per-button binding therefore leaves every re-rendered card's
   control inert while still rendering it correctly — the button is there, the
   click does nothing, no request is made, and no note appears. That is the
   same failure this repo already shipped once: v0.19.0's answer handler looked
   up a node that was not there, threw, and fired no request at all while the
   control still looked right.
   The document rather than ul.items because ensureList() creates that
   container lazily — a listener bound to it at load would miss the first row,
   and binding it inside ensureList would rebind on every empty->non-empty
   transition. The document covers both cases once. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-jump]');
  if (!button) { return; }
  var row = button.closest('li.item');
  fetch(button.getAttribute('data-jump'), { method: 'GET' })
    .then(function (response) {
      if (response.ok) { showJumpNote(row, 'Jumped.', false); return; }
      return response.text().then(function (body) {
        showJumpNote(row, 'Jump failed - HTTP ' + response.status + ' - ' + body, true);
        console.error('attention board: jump failed - HTTP ' + response.status, body);
      });
    }).catch(function (error) {
      showJumpNote(row, 'Jump failed - ' + String(error), true);
      console.error('attention board: jump failed - ' + String(error));
    });
});
function showJumpNote(row, message, isError) {
  var container = row.querySelector('.jump');
  var previous = container.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('span');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  container.appendChild(note);
  rememberFailure(row, 'jump', message, isError);
}
/* ⚠️ The acknowledge path closes the item — open -> closed — and on a
   report-only card it is the only move that card offers. It posts the board as
   the arm that caused the close, which is what lets a reader tell a board
   acknowledgement from a producer withdrawing its own item or from the store's
   producer-exit sweep: neither of those records an answered_by at all.
   answered_at stays unset, because an ack item routes nothing back to its
   producer.
   It is a named function rather than an inline listener because two controls
   reach it — the acknowledge button and the corner X — and both must perform
   the same write, not two writes that happen to agree.
   ⚠️ RENAMED 2026-09-27 from closeAck. It serves a permission card's X as
   well as an ack card's Acknowledge, so a name claiming one mechanism would
   misdescribe the other — the same reason the note's verb is read off the row
   rather than hardcoded. */
function closeCard(row) {
  /* The verb is per-mechanism: an ack card is acknowledged, a permission card
     is cleared. The note is the only feedback this handler gives on a row whose
     removal comes from the stream rather than from here. */
  var acknowledged = !!row.querySelector('[data-ack]');
  fetch('/api/1.0/attention/' + encodeURIComponent(row.getAttribute('data-item-id')) + '/close', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    /* automation is the page's own navigator.webdriver reading — the one
       answered-client member the body may carry; the store reads user_agent
       and remote_addr from the request itself. */
    body: JSON.stringify({ answered_by: 'attention-board', automation: navigator.webdriver })
  }).then(function (response) {
    if (response.ok) { showCloseNote(row, acknowledged ? 'Acknowledged.' : 'Cleared.', false); return; }
    return response.text().then(function (body) {
      showCloseNote(row, (acknowledged ? 'Acknowledge' : 'Clear') + ' failed - HTTP ' + response.status + ' - ' + body, true);
      /* The action word is fixed at "close" rather than read off the row the way
         the note's verb is. The note addresses the operator and names the
         control they pressed; a console line is grepped, so a stable word is
         what makes it findable. The two surfaces are allowed to differ, and the
         note is the one that must not change. */
      console.error('attention board: close failed - HTTP ' + response.status, body);
    });
  }).catch(function (error) {
    showCloseNote(row, (acknowledged ? 'Acknowledge' : 'Clear') + ' failed - ' + String(error), true);
    console.error('attention board: close failed - ' + String(error));
  });
}
/* ⚠️ Delegated on the document for the tab strip's reason: a re-rendered row's
   node takes its per-button listener with it, so Acknowledge stops acting. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-ack]');
  if (!button) { return; }
  var row = button.closest('li.item');
  if (!row) { return; }
  closeCard(row);
});
/* The corner X is one affordance whose act is the mechanism's own dominant act,
   which is why this handler dispatches rather than posting: on a message card
   it is the Dismiss the card already carries (the skip answer, open -> answered,
   answer.kind: skip), on an ack card it is the Acknowledge it already carries
   (the close, open -> closed, answered_at unset), and on a permission card it is
   the clear (that same close — open -> closed, answered_at and decision both
   unset). It adds no transition and no field — see [[Attention Item Schema]]
   § Answer routing, § The corner X.
   A permission card DOES render the X, from 2026-09-27. It is not the permission
   laundering the schema forbids, and the earlier reading here conflated two
   different acts: the schema forbids an arm ANSWERING a permission item —
   causing open -> answered on the operator's behalf — while the X only CLEARS
   the card. No answer is routed, so the asking session stays frozen exactly as
   it was, and the gate is still answered only by the operator, in the session
   that raised it. Clearing is not deciding.
   There is deliberately no third branch below: a permission row carries no
   form.answer, so it falls through to closeCard exactly as an ack row does. The
   three mechanisms share one dispatch because they share one act, and it is
   delegated on the document for the tab strip's reason. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-corner-x]');
  if (!button) { return; }
  var row = button.closest('li.item');
  if (!row) { return; }
  var form = row.querySelector('form.answer');
  /* A message card's X is the Dismiss submit, dispatched rather than
     re-implemented: the form's own submit handler owns the skip payload, the
     ITEM_CLOSED branch and the note placement, and a second copy here would
     be a second thing to keep in step. */
  if (form) {
    var dismiss = form.querySelector('button[value=skip]');
    if (dismiss) { dismiss.click(); return; }
  }
  closeCard(row);
});
/* ⚠️ The container is OPTIONAL, and that is not defensive coding — it is the
   defect this function shipped with until 2026-09-27. A permission row renders
   no .actions div (only message and ack rows do), so row.querySelector returned
   null and the next call threw a TypeError on EVERY X-click on a permission
   card. The close itself succeeded and the row still left the page, because the
   stream removes it rather than this handler — which is exactly why the throw
   was invisible: the operator saw the right outcome and only the console took
   the error. Falling back to the row keeps the note on a card that has no
   actions row to hold it. */
function showCloseNote(row, message, isError) {
  var container = row.querySelector('.actions') || row;
  var previous = container.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('span');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  container.appendChild(note);
  rememberFailure(row, 'close', message, isError);
}
/* The most recent failure shown for an item, keyed the way speaking is and
   for the same reason: a note is a child of the row it was rendered into, so
   upsertRow's outerHTML swap destroys it and the operator loses the line they
   were reading mid-sentence. Held here and re-rendered after the swap, because
   the map lives in this page: the server keeps no record of what this page was
   shown.

   ⚠️ A FAILURE only. A success note is not persisted, and each helper clears
   the entry for its item when it renders one: a stale failure note that
   outlives its cause is a worse defect than the one this fixes, because it
   reports a failure that is no longer true. */
var failures = {};
/* rememberFailure records what a note helper just rendered. The row is read
   from the node the note landed in rather than passed in, so the three helpers
   share one recorder: showNote's host is a form or a row, the other two pass
   the row itself, and closest('li.item') is the same row for all of them. */
function rememberFailure(host, kind, message, isError) {
  var row = host.closest('li.item');
  if (!row) { return; }
  var itemID = row.getAttribute('data-item-id');
  if (!itemID) { return; }
  if (!isError) { delete failures[itemID]; return; }
  failures[itemID] = { kind: kind, message: message };
}
/* replayFailure re-renders the failure last shown for a row, through the SAME
   helper that rendered it — so the replayed note's text, placement and class
   are the helper's own and cannot drift from them, which a second renderer
   here could. The kind picks the helper, and the host is resolved exactly as the
   original call site resolved it: showNote's host is the row's answer form
   where the row carries one and the row itself where it does not, which is the
   same reading the read-aloud handler makes. */
function replayFailure(row) {
  if (!row) { return; }
  var failure = failures[row.getAttribute('data-item-id')];
  if (!failure) { return; }
  if (failure.kind === 'jump') { showJumpNote(row, failure.message, true); return; }
  if (failure.kind === 'close') { showCloseNote(row, failure.message, true); return; }
  showNote(row.querySelector('form.answer') || row, failure.message, true);
}
/* The live channel. The page subscribes once and swaps rows in place as the
   store changes, so an open board tracks the store instead of freezing at load.
   The stream carries a RENDERED ROW rather than an item: the markup comes from
   the server's own template, so there is one renderer and a row arriving here
   cannot drift from the same row on a fresh load — which is also why this stays
   plain DOM work with no framework and no build step.

   Nothing reloads, here or after an action. An earlier version reloaded the
   page once an answer was accepted; that is exactly the manual step this board
   exists to remove, and it would also defeat the channel's own negative control
   — with the stream blocked, a reload would make the row change anyway, and the
   control could not then tell the channel from a coincidence. */
(function () {
  /* The board's own view filter. It hides the dimmed record cards — the
     answered items the board keeps as evidence — so the page answers "what is
     left" rather than showing records beside open prompts. Two properties are
     load bearing and neither is cosmetic.

     A filtered row is REMOVED, not hidden. A display:none rule leaves the node
     in the document, so a selector, a script or the operator's own assistive
     tooling still finds a card the board claims not to be showing.

     And the filter runs on the stream path too, not only at first paint. The
     channel sends a rendered row and this page swaps it in, so a filter that
     ran once at load would leak every row that arrived afterwards — including
     a card that becomes answered while the board is open, which is exactly
     the row the filter exists to hide. */
  var button = document.querySelector('button[data-board-filter]');
  /* The board's stream health, the other control in this row. It ships hidden
     and only the stream's own handlers move it, so nothing about a board whose
     stream is healthy changes. */
  var stale = document.querySelector('[data-stream-stale]');
  /* Mirrors boardHideParam / boardHideAnswered / boardHideNone in Go — the one
     thing in this file that exists in two languages. The server renders the
     switch's initial position from the same parameter; this reads it to apply
     the filter and writes it back on every change, so the URL and the page
     cannot disagree about what is being shown. */
  var HIDE_PARAM = 'hide';
  var HIDE_ANSWERED = 'answered';
  var HIDE_NONE = 'none';
  /* The default is ON: only an explicit hide=none turns the filter off, and
     absence (or any unrecognised value) leaves it on. Read as "not none"
     rather than "is answered" so an unrecognised value falls back to the
     default instead of silently disabling the filter. */
  var on = new URLSearchParams(window.location.search).get(HIDE_PARAM) !== HIDE_NONE;
  /* The rows the filter is hiding, in board order. Each entry carries the id
     of the row that followed it, so a restore returns it to its own place
     rather than to the foot of the board. It holds rendered HTML rather than
     a detached node: a dimmed row carries no form and no typed state, so a
     round trip through markup loses nothing, and the markup is the server's
     own renderer output either way. */
  var parked = [];

  function findRow(itemID) {
    /* Compared rather than selected: the id lands in a selector string
       otherwise, and a generated id is not the place to rely on. */
    var rows = document.querySelectorAll('li.item');
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].getAttribute('data-item-id') === itemID) { return rows[i]; }
    }
    return null;
  }
  /* The list, created in place of the empty-state paragraph when the board
     rendered one. The stream needed this already; the filter needs it too,
     because a board the filter emptied is not an empty board — its rows are
     parked, not gone. */
  function ensureList() {
    var list = document.querySelector('ul.items');
    if (list) { return list; }
    var empty = document.querySelector('p.empty');
    if (!empty) { return null; }
    list = document.createElement('ul');
    list.className = 'items';
    empty.parentNode.replaceChild(list, empty);
    return list;
  }
  function nextRowID(row) {
    var next = row.nextElementSibling;
    while (next && !next.classList.contains('item')) { next = next.nextElementSibling; }
    return next ? next.getAttribute('data-item-id') : null;
  }
  function park(row) {
    var id = row.getAttribute('data-item-id');
    /* Re-parking replaces rather than appends: a row that streams an update
       while hidden arrives here a second time, and two entries for one id
       would restore it twice. */
    parked = parked.filter(function (entry) { return entry.id !== id; });
    parked.push({ id: id, html: row.outerHTML, next: nextRowID(row) });
    row.remove();
  }
  function unpark() {
    var list = ensureList();
    if (!list) { return; }
    /* Restored back to front, so an entry whose anchor is itself still parked
       is placed after that anchor rather than appended past it — parking a run
       of adjacent dimmed rows records each one's anchor as the next, and a
       forward walk would not find any of them yet. */
    for (var i = parked.length - 1; i >= 0; i--) {
      var entry = parked[i];
      var anchor = entry.next ? findRow(entry.next) : null;
      if (anchor) { anchor.insertAdjacentHTML('beforebegin', entry.html); continue; }
      list.insertAdjacentHTML('beforeend', entry.html);
    }
    parked = [];
  }
  function applyFilter() {
    if (!on) { unpark(); return; }
    var rows = document.querySelectorAll('li.item.dimmed');
    for (var i = 0; i < rows.length; i++) { park(rows[i]); }
  }
  function forget(itemID) {
    parked = parked.filter(function (entry) { return entry.id !== itemID; });
  }
  if (button) {
    button.addEventListener('click', function () {
      on = !on;
      button.setAttribute('aria-checked', on ? 'true' : 'false');
      /* replaceState, not pushState. The filter is a view, and a view that
         filled the Back stack would make Back mean "undo my filter" on some
         presses and "leave the page" on others — and with no popstate handler
         a pushState entry would also leave the URL and the page disagreeing
         after a Back. This changes the URL without a navigation, which is what
         keeps the no-reload property intact. */
      var url = new URL(window.location.href);
      /* The URL carries the parameter only when the view departs from the
         default, which is now the filtered view: turning the filter off writes
         the off-spelling, and turning it back on returns the URL to the
         parameter-free default. A deleted parameter can no longer mean "off",
         so absence and HIDE_NONE must not be confused. */
      if (on) {
        url.searchParams.delete(HIDE_PARAM);
      } else {
        url.searchParams.set(HIDE_PARAM, HIDE_NONE);
      }
      window.history.replaceState(null, '', url);
      applyFilter();
    });
  }
  /* Applied at load as well as on click, so a URL that arrived carrying the
     filter renders the filtered view instead of flashing the full one. The
     script runs while the parser is still inside the body, so this lands before
     the first paint rather than as a correction to it. */
  if (on) { applyFilter(); }
  function collapseIfEmpty() {
    var list = document.querySelector('ul.items');
    if (!list || list.querySelector('li.item')) { return; }
    /* A board the filter emptied is not an empty board: the store still holds
       the parked records, and collapsing here would take away the list they
       restore into. */
    if (parked.length > 0) { return; }
    var empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = 'Nothing needs attention.';
    list.parentNode.replaceChild(empty, list);
  }
  /* A row's update is PREPARED on a detached node and only then committed.
     ⚠️ Load bearing: the live path used to swap first and repair after, so a
     throw between the two left the row swapped with its state missing — worst
     of all the filter, which left a card that became answered standing OPEN on
     a board whose own switch said it was hidden. Preparing first makes the swap
     the LAST fallible step, and is also how a PARKED row's note is replayed. */
  function preparedHTML(html, itemID) {
    if (!failures[itemID] && !speaking[itemID]) { return html; }
    var detached = document.createElement('div');
    detached.innerHTML = html;
    var row = detached.firstElementChild;
    if (!row) { return html; }
    if (failures[itemID]) { replayFailure(row); }
    if (speaking[itemID]) { speakButtonState(speakButtonIn(row), true); }
    return row.outerHTML;
  }
  /* The read-aloud control within a row not yet in the document — speakControl
     searches the document, which an uncommitted node is not part of. */
  function speakButtonIn(row) {
    return row ? row.querySelector('button[data-speak]') : null;
  }
  function upsertRow(itemID, html) {
    /* Prepared first: a throw in here leaves the board untouched. */
    var prepared = preparedHTML(html, itemID);
    var hidden = -1;
    for (var i = 0; i < parked.length; i++) {
      if (parked[i].id === itemID) { hidden = i; }
    }
    if (hidden >= 0) {
      /* The row is hidden by the filter, so it is not in the document and
         findRow cannot see it. Updating the parked entry in place keeps the
         place it restores to: appending the row to the list and letting
         applyFilter re-park it would read a fresh anchor at the foot of the
         board, which is null, and the record would come back in the wrong
         position — the exact failure the anchor exists to prevent. The row
         stays hidden because a parked row is answered, and an answered item
         never returns to open (§ Lifecycle), so its update is dimmed too. */
      parked[hidden].html = prepared;
      return;
    }
    var row = findRow(itemID);
    if (row) {
      row.outerHTML = prepared;
    } else {
      var list = ensureList();
      if (!list) { return; }
      list.insertAdjacentHTML('beforeend', prepared);
    }
    /* The row is complete as committed, so nothing is re-applied to it here.
       The filter still runs after: it is a board-level view over every row. */
    applyFilter();
  }
  /* The filter is wired above this guard rather than below it. It is plain DOM
     work, and a browser with no EventSource still renders the board and still
     has to be able to ask what is left. */
  if (typeof EventSource === 'undefined') { return; }
  var source = new EventSource('/api/1.0/attention/stream');
  /* The not-tracking state, shown in the board's control row. It is a state on
     the page rather than a console line because the operator has to be able to
     read it without opening devtools — that is the whole point: a stream that
     has stopped for good (a 404 after a redeploy, a persistent 500) left the
     board frozen at its load-time state with no signal at all, so a stale board
     read as a current one. Tracking is the healthy case, so the callers read
     as what the event means rather than as which way the attribute goes. */
  function setTracking(tracking) {
    if (!stale) { return; }
    stale.hidden = tracking;
  }
  /* No reconnect handler: EventSource reconnects on its own, which is the
     property that lets the board survive a restart of the store.
     ⚠️ onerror renders the state and returns. It does NOT reconnect and must
     not: it closes nothing, retries nothing and leaves reconnection to
     EventSource exactly as the line above records. It fires on each failed
     attempt, so a brief restart of the store shows the state only for the gap,
     and the next message clears it. */
  source.onerror = function () {
    setTracking(false);
  };
  source.onmessage = function (event) {
    /* Cleared before the frame is read: a message arrived, so the board is
       tracking the store again whatever that frame turns out to say — a frame
       this page cannot parse is logged and skipped below, and skipping it is
       not the same as the stream being down. */
    setTracking(true);
    var change;
    /* A frame the page cannot read is logged and skipped rather than thrown.
       The rest of this handler reads change.type and change.item_id, so a throw
       here aborted the row swap with nothing said; a truncated frame or one from
       a newer store version is not a reason to stop listening, and the stream
       reconnects on its own. */
    try {
      change = JSON.parse(event.data);
    } catch (error) {
      console.error('unreadable stream frame: ' + String(error), event.data);
      return;
    }
    /* ⚠️ The whole application is guarded, not only the parse above it: a throw
       in the swap, the note replay, the repaint, the filter or the remove path
       used to escape as an uncaught error. It carries the item id, because a
       row that failed to update renders like a row that needed none. */
    try {
      if (change.type === 'remove') {
        /* Dropped from the parked set as well as from the board: a row the
           store removed must not come back when the filter is switched off. */
        forget(change.item_id);
        var row = findRow(change.item_id);
        if (row) { row.remove(); }
        collapseIfEmpty();
        return;
      }
      upsertRow(change.item_id, change.html);
    } catch (error) {
      /* ⚠️ No 'attention board: ' prefix: that is reserved for the nine ACTION
         lines, and a spec pins its count at nine. This is stream handling, so
         it logs unprefixed exactly as the parse guard above it does.
         ⚠️ The id is read through a guard, not off 'change' directly. The parse
         above succeeds for any JSON document, and the literal 'null' is one:
         'change.type' then throws, this catch runs, and 'change.item_id' would
         throw AGAIN from inside the error path — escaping source.onmessage
         anyway, which is the exact failure this catch exists to close. */
      console.error(
        'could not apply stream frame for ' +
          (change && change.item_id) + ' - ' + String(error),
        change
      );
    }
  };
})();
</script>
</body>
</html>
{{/* One item row, as a sub-template rather than inline in the list. The live
     stream sends a changed row to the open page as rendered HTML and the page
     swaps that node in place, so the row markup has to be renderable on its own
     — and it must be the SAME markup, not a second renderer that would drift
     from this one. The dollar sign inside a sub-template is the value passed to
     it, which is why Speak is carried on the row and read as dot-Speak here. */}}
{{define "attention-row"}}<li class="item{{if .Dimmed}} dimmed{{end}}" data-item-id="{{ .Item.ItemID }}">
<div class="producer">{{ .Item.ProducerID }} ({{ .Item.ProducerKind }})</div>
{{/* The corner X renders on EVERY row, deliberately ungated, from 2026-09-27.
     It was gated on Message-or-Ack until then, which excepted permission rows —
     an exception the operator disowned (it had been read off a yes on a menu
     they did not write). A permission row carries the X because clearing a card
     is not answering a gate: the X closes it, routes no answer, and leaves the
     asking session frozen and the gate operator-only. Gating on a mechanism
     here would re-create the exception. See [[Attention Item Schema]] § The
     corner X, and the dispatch comment on the data-corner-x handler below. */}}<button type="button" class="corner-x" data-corner-x aria-label="Skip this item">✕</button>
{{/* The read-aloud control renders on every row whose Speak is set, from
     2026-09-27 — except the dimmed record card, from 2026-09-27. It was gated on
     'and .Message .Speak', which excepted permission rows on the reasoning that
     read-aloud is an answer control — it is not; it is a utility that records no
     answer, and the operator ruled that permission cards carry it too. ⚠️ The
     two gates reconciled here are the X task's (the X becomes ungated) and this
     one's (the Message conjunct is dropped); they sat on adjacent lines and were
     merged on 2026-09-27. See [[Attention Item Schema]] § Answer routing.
     ⚠️ The dimmed record card is a record, not a prompt, so it carries no
     control that offers an answer — this one included: it asks the tts server to
     read the QUESTION aloud, which is the act of a prompt on a row whose whole
     purpose is to be a record. The (not .Dimmed) conjunct renders that rule, and
     it is the same idiom the ack control below already uses. .Dimmed is exactly
     'item.State == AnsweredState', so the one conjunct covers every mechanism a
     dimmed card can reach — the dimmed message and dimmed permission cards alike
     — while leaving the control on every open row. ⚠️ The '.Speak' conjunct must
     stay: with no tts server configured, no row renders a speaker. */}}{{if and .Speak (not .Dimmed)}}<button type="button" class="speak" data-speak aria-label="Read aloud" title="Read aloud"><svg class="speak-icon" viewBox="0 0 16 16" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M9 2.75 5.25 5.5H2.75v5h2.5L9 13.25z"/><path d="M11.5 5.75a3.25 3.25 0 0 1 0 4.5"/><path d="M13.5 3.75a6 6 0 0 1 0 8.5"/></svg></button>
{{end}}{{if .JumpURL}}<button type="button" class="jump-corner" data-jump="{{ .JumpURL }}" aria-label="Jump to session" title="Jump to session"><svg class="jump-icon" viewBox="0 0 16 16" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.75 3.25h8.5a1.5 1.5 0 0 1 1.5 1.5v6.5a1.5 1.5 0 0 1-1.5 1.5h-8.5a1.5 1.5 0 0 1-1.5-1.5v-6.5a1.5 1.5 0 0 1 1.5-1.5z"/><path d="M5.75 6.5 7.5 8.25 5.75 10"/><path d="M9 10h1.75"/></svg></button>
{{else if .NoJump}}<button type="button" class="jump-corner" disabled aria-label="Jump to session" title="Jump to session"><svg class="jump-icon" viewBox="0 0 16 16" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.75 3.25h8.5a1.5 1.5 0 0 1 1.5 1.5v6.5a1.5 1.5 0 0 1-1.5 1.5h-8.5a1.5 1.5 0 0 1-1.5-1.5v-6.5a1.5 1.5 0 0 1 1.5-1.5z"/><path d="M5.75 6.5 7.5 8.25 5.75 10"/><path d="M9 10h1.75"/></svg></button>
{{end}}{{if not .Message}}<div class="payload">{{ .Item.Payload }}</div>
{{end}}{{if .Item.Context}}<div class="context">{{ .Item.Context }}</div>
{{end}}{{if .Provenance.Resolved}}<div class="provenance">{{if .TaskURL}}<span class="task"><a href="{{ .TaskURL }}">{{ .Provenance.TaskName }}</a></span>{{end}}{{if .GoalURL}}<span class="goal"><a href="{{ .GoalURL }}">{{ .Provenance.GoalName }}</a></span>{{end}}{{if .TopicURL}}<span class="topic"><a href="{{ .TopicURL }}">{{ .Provenance.TopicName }}</a></span>{{end}}{{if .Provenance.Host}}<span class="host">{{ .Provenance.Host }}</span>{{end}}{{if .Provenance.Cwd}}<span class="cwd">{{ .Provenance.Cwd }}</span>{{end}}{{if .Provenance.Tool}}<span class="tool">{{ .Provenance.Tool }}</span>{{end}}{{if .Provenance.Pane}}<span class="pane">pane {{ .Provenance.Pane }}</span>{{else if .Provenance.PaneRecorded}}<span class="unroutable">unroutable</span>{{end}}</div>
{{end}}{{if .Dimmed}}<div class="record"><div class="record-question">{{ .Item.Payload }}</div><div class="record-answer">answered: {{ .Record }}</div></div>
{{else if .Message}}<form class="answer" data-multi="{{ .Tabs }}">
{{if .Tabs}}<div class="tabs">{{range .Questions}}<button type="button" class="tab{{if .Active}} active{{end}}" data-tab="{{ .Tab }}">{{ .Tab }}</button>{{end}}</div>
<div class="card-title">{{ .Item.Payload }}</div>
{{end}}{{range .Questions}}{{$question := .}}<div class="panel" data-question="{{ $question.Tab }}" data-multi-pick="{{ $question.Multi }}"{{if not $question.Active}} hidden{{end}}>
<div class="question">{{ $question.Payload }}{{if $question.Hint}} <span class="hint">({{ $question.Hint }})</span>{{end}}</div>
{{if $question.Options}}<div class="options">
{{range $question.Options}}<label class="option"><input type="{{ if $question.Multi }}checkbox{{ else }}radio{{ end }}" name="{{ $question.Name }}" value="{{ .Label }}" data-option="{{ .Label }}"><span class="option-body"><span class="option-label">{{ .Label }}{{if .Recommended}} <span class="recommended">(Recommended)</span>{{end}}</span>{{if .Description}}<span class="option-desc">{{ .Description }}</span>{{end}}</span></label>
{{end}}</div>
{{end}}<input class="other" type="text" name="text" placeholder="Other...">
</div>
{{end}}<div class="actions"><button type="submit" name="kind" value="skip" class="dismiss">✕ Dismiss</button><button type="submit" name="kind" value="send" class="next">✓ Submit answer</button></div>
</form>
{{end}}{{if and .Ack (not .Dimmed)}}<div class="actions"><button type="button" class="ack" data-ack>Acknowledge</button></div>
{{end}}{{if and .Decide (not .Dimmed)}}<div class="actions"><button type="button" class="dismiss" data-decision="deny">✕ Deny</button><button type="button" class="next" data-decision="allow">✓ Allow</button></div>
{{end}}{{if or .Jump .JumpURL}}<div class="jump">{{if .Jump}}<span>Approve in the session that asked: <code>{{ .Jump }}</code></span>{{end}}</div>
{{else if .NoJump}}<div class="jump-reason"><span class="no-jump">{{ .NoJump }}</span></div>
{{end}}<div class="meta">{{ .Item.State }} - {{ .Item.CreatedAt }}</div>
</li>{{end}}
`

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
	// Decide reports whether this row renders the Allow / Deny controls. True
	// for `permission` items only.
	//
	// ⚠️ REVERSES the "no control on a permission row" ruling above, on the
	// operator's decision 2026-09-29: the board is where the operator answers,
	// and a permission card with no control left every tab-session prompt
	// unanswerable from it. The click is the operator's own verdict, written as
	// `decision`; the attention watcher delivers it by pressing that prompt's
	// own Yes / No row in the session's pane, re-proving the pane is the
	// session's first. A headless worker's park is settled by the supervisor's
	// poll from the same `decision`, as before.
	Decide bool
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
	// vaultFileURL escapes both halves before building it.
	TaskURL template.URL
	// GoalURL is the link that opens the goal this item's task names first in its
	// `goals:` list, drawn beside the task span. Empty when no goal resolved — an
	// unresolvable value renders absent rather than as a stand-in, the same rule
	// TaskURL follows.
	//
	// ⚠️ It is a template.URL rather than a string for the same load-bearing
	// reason TaskURL is: html/template's URL filter admits only `http`, `https`,
	// `mailto` and relative URLs, so a plain-string `href="{{ .GoalURL }}"` renders
	// `href="#ZgotmplZ"` — the link dead in the browser while every test that
	// asserts on the row field still passes. `obsidian://` is exactly the scheme
	// that filter refuses, so the conversion is what makes the href emit at all.
	GoalURL template.URL
	// TopicURL is the link that opens the topic page that lists this item's goal
	// under its `## Goals` heading, drawn beside the goal span. Empty when no topic
	// lists that goal, which is the common case — an unresolvable value renders
	// absent rather than as a stand-in, the same rule TaskURL follows.
	//
	// ⚠️ It is a template.URL rather than a string for the same load-bearing
	// reason TaskURL is: html/template's URL filter admits only `http`, `https`,
	// `mailto` and relative URLs, so a plain-string `href="{{ .TopicURL }}"`
	// renders `href="#ZgotmplZ"` — the link dead in the browser while every test
	// that asserts on the row field still passes. `obsidian://` is exactly the
	// scheme that filter refuses, so the conversion is what makes the href emit at
	// all.
	TopicURL template.URL
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
