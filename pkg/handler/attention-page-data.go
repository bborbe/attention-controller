// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package-level note: this file holds the page's render model — the row a card
// becomes, the data the template renders, and the one store read that decorates
// a row — rather than attention-page.go, which crossed revive's 2000-line
// file-length limit when the flag filter and the producer-kind filter landed on
// the same page. The extraction is verbatim: no behaviour changed.
package handler

import (
	"context"
	"html/template"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

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
	// Risk is the level the board RENDERS for this row — the higher of the
	// item's declared risk and the rule its own content matches, computed by
	// pkg.RiskOf at render time.
	//
	// ⚠️ It is deliberately not item.Risk. The declared value is stored
	// unchanged and this field is the render-time escalation above it, so a
	// producer that under-declares cannot make a destructive card render green.
	// The two are separate values and must stay separate: reading item.Risk
	// here would silently drop the derivation, and the row would colour itself
	// by whatever the producer claimed.
	Risk pkg.Risk
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
	// Flagged reports whether this row carries a flag and is therefore parked by
	// the flag filter's default position. The template reads it for the row's
	// data-flagged mark and for the flag-reason line; the page's script reads the
	// rendered mark, never this field.
	Flagged bool
	// DeliveryStatus is what the store's Delivery report says happened to this
	// card's answer — `delivered`, `failed`, `never_attempted` or
	// `pre_trail_unknown` — and is what the template reads to mark an answer
	// that never reached the asking session.
	//
	// ⚠️ It is the DERIVED status, never the raw DeliveryAttempt record, for the
	// reason pkg.DeliveryReport carries the derivation: an absent record means
	// `never_attempted` or `pre_trail_unknown` depending on the item's own
	// timestamps, and a row that had to know the epoch rule to tell the two apart
	// would hold a second copy of it. That is also why it is read at RENDER time
	// rather than carried on the item: an attempt is written by a different arm
	// after the answer, so the value the store holds changes without the item
	// changing.
	//
	// ⚠️ Empty means the read failed, and empty renders no marker — the same
	// fail-soft rule the provenance read follows. It is deliberately not one of
	// the four statuses, so a failed read can never be mistaken for
	// `never_attempted`, which is a real answer about the item rather than about
	// this render.
	DeliveryStatus pkg.DeliveryStatus
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
	// Info reports whether this row carries machine identity — a producer id or
	// kind, a machine provenance value, or a state/timestamp — and therefore
	// renders the info-toggle and its panel. A row carrying none of them renders
	// no control at all, never a control that opens onto nothing.
	Info bool
	// Meta is the panel's state-and-timestamp line. Empty when the item carries
	// neither, so the panel draws no element for an absent value.
	Meta string
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

// Undeliverable reports whether this card's answer failed to reach the asking
// session, and so renders the marker that says so.
//
// ⚠️ It is a method rather than a second boolean field on purpose. The template
// and the row would then carry two values that could disagree about one fact,
// and the constant it compares against stays in Go rather than being spelled as
// a string literal in the template — the same reason the row carries no
// mechanism comparison. A `delivered`, `never_attempted` or `pre_trail_unknown`
// answer renders nothing: only a recorded failure is something the operator can
// act on, and the other three would put a marker on every card.
func (r attentionPageRow) Undeliverable() bool {
	return r.DeliveryStatus == pkg.FailedStatus
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
	// FlagFilter is the flag filter's initial position, read from the request's
	// `flags` parameter alone, exactly as § Two filters defines: each parameter
	// governs its own set, so `hide=none` leaves the flag filter on rather than
	// disabling it. It is a rendering input, never a store input — the page
	// still renders every row it was given.
	FlagFilter bool
	// AllKinds reports whether the kind filter is off. Rendered so the switch's
	// initial position and the URL it writes agree with the request — the same
	// contract HideAnswered keeps, and for the same reason. The active kind
	// itself is not carried here: the switch's link is the whole state, and a
	// second copy on the page would be a value that could disagree with it.
	AllKinds bool
	// KindHref is where the kind switch points: this request with only
	// `producer_kind` replaced. Computed here rather than assembled in the
	// template so every other view parameter — `hide` above all — survives the
	// toggle instead of being dropped by a hand-built query string.
	KindHref string
	// HiddenCount is how many of the rows the store returned this filter is not
	// showing, on the view this request rendered. ⚠️ It is computed from the full
	// row set the page was built from, not read back from the store, so it costs
	// no second read and cannot disagree with the rows it describes.
	HiddenCount int
	// HiddenOpen and HiddenAll are the same count on the two positions of the
	// answered filter: HiddenOpen excludes the answered records (the `hide` ON
	// view, where that filter already withholds them), HiddenAll includes them
	// (the `hide=none` view, where they are on screen and the kind filter is what
	// hides them).
	//
	// ⚠️ Both are rendered so the script can keep the count true after load.
	// Neither is derivable from the other on the client: the rows the server
	// withheld were never sent, so only the server knows how many of them are
	// answered — and the answered toggle and the stream both move which position
	// applies.
	HiddenOpen int
	HiddenAll  int
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

// setKindCount picks the hidden count the view being rendered is showing.
//
// It lives here rather than in the template so the rule is stated once: the
// count follows the answered filter's position, and a template condition would
// be a second copy of it — the kind of duplicate that agrees until one side is
// changed.
func (d *attentionPageData) setKindCount(hideAnswered bool) {
	d.HiddenCount = d.HiddenAll
	if hideAnswered {
		d.HiddenCount = d.HiddenOpen
	}
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
// back off. That is why the test is `!= boardHideNone` rather than a test for
// the word `answered`: an unrecognised value falls back to the default
// instead of silently disabling the filter, and "off" needs a spelling the
// script can write back, which a deleted parameter can no longer be.
//
// ⚠️ The parameter name and the off-value are mirrored as HIDE_PARAM and
// HIDE_NONE in the page's script, because the server renders the switch's
// initial position while the script applies the filter and writes the
// parameter back. That mirror is the only thing in this file that exists in two
// languages, so a spec asserts the script carries the same literals: drift
// there would leave the server rendering from one parameter while the script
// writes another, and the reload-reproduces-the-view property would stop
// working with every other test still green. The script never writes the on
// spelling — absence is how it is written.
const (
	boardHideParam = "hide"
	boardHideNone  = "none"

	// boardFlagsParam carries the flag filter's view state, on the same
	// convention as boardHideParam: the value names the SET that is hidden,
	// so `none` is the only value that turns it off. boardFlagsFlagged is the
	// default spelling the script writes; it is declared here so the Go and
	// script copies are greppable together.
	boardFlagsParam   = "flags"
	boardFlagsFlagged = "flagged"
	boardFlagsNone    = "none"
)

// deliveryStatusOf reads one card's derived delivery status for its row, and
// answers the zero value when the read fails.
//
// ⚠️ The failure is swallowed deliberately, and it is the same fail-soft rule
// provenance.Resolve follows one function up: the status DECORATES a card that
// is already on the board, so a read that fails — a bbolt View error, or an item
// removed between ReadBoard and this call — must not turn the whole page into a
// 500. The card itself is still readable, and the marker is an addition to it,
// never a precondition for rendering it.
//
// ⚠️ The zero value is what makes that safe rather than merely quiet: it is no
// status the store ever returns, so the row renders no marker and degrades to
// exactly what it rendered before the marker existed. Returning
// NeverAttemptedStatus here instead would assert something about the item that
// this call did not learn.
func deliveryStatusOf(
	ctx context.Context,
	store pkg.AttentionStore,
	itemID pkg.ItemID,
) pkg.DeliveryStatus {
	report, err := store.Delivery(ctx, itemID)
	// ⚠️ The nil check is part of the same fail-soft rule rather than
	// defensiveness for its own sake: a store that answers no report and no
	// error has told this row nothing, and dereferencing it would take the whole
	// page down for one card. The mock store in the identity spec is exactly
	// such a caller, and it is right to be — the read it is faking is not the
	// subject of that spec.
	if err != nil || report == nil {
		return ""
	}
	return report.Status
}
