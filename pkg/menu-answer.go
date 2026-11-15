// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// menuCancel is the menu's ONLY exit when nothing is picked: it offers no
// "submit nothing" row, so a decline is a cancellation.
//
// ⚠️ It is sent ALONE, never as an arrow-key prefix — `ESC [ B` is the down
// arrow, and a lone ESC is what cancels. Sending an arrow would move the cursor
// instead of closing the menu.
const menuCancel = "\x1b"

// menuMark is the footer an open AskUserQuestion menu draws. Its presence in the
// pane is the only proof that the menu this card answers is actually showing —
// without it the arm would type digits into an ordinary prompt.
const menuMark = "Enter to select"

// submitRowPattern finds the review screen's Submit row and the digit that
// submits it. The digit is READ OFF THE SCREEN, never assumed: its value depends
// on how many questions the menu carried.
var submitRowPattern = regexp.MustCompile(`(\d)\.\s+Submit answers`)

// MenuAnswerInterval is how often the menu arm looks for an answered menu card.
// It matches the prose arm's pace, which is the cadence the board's
// answered -> delivered bar is measured at.
const MenuAnswerInterval = 2 * time.Second

// Defaults for the keystroke loop. They are fields on the job rather than
// package constants so a spec can drive the loop with zero sleeps.
const (
	defaultMenuKeySettle   = 250 * time.Millisecond
	defaultMenuSubmitPolls = 12
)

// MenuQuestion is one question unit of an open AskUserQuestion menu: the tab the
// board renders, its option labels in render order, and whether it takes several
// picks.
//
// It is a view of the stored Questions/Options rather than a second model: the
// labels come from AnswerOptions and the cardinality from AnswerCardinality, so
// nothing here can disagree with what the board rendered.
type MenuQuestion struct {
	// Tab is the question's tab label, or "" on a single-question menu.
	Tab string
	// Labels are the option labels in the order the menu renders them.
	Labels []string
	// Multi reports whether each pick toggles a box rather than selecting and
	// advancing.
	Multi bool
}

// MenuQuestionsOf returns the menu's question units for an item, or false when
// the item is not an open AskUserQuestion menu.
//
// ⚠️ Three tests, all load-bearing. A `permission` item is approve-shaped and is
// answered through the wrapper, not by keystrokes. A non-hook item is a card the
// operator answers on the board, which carries options for RENDERING but has no
// menu open in any pane — typing its digit would submit it into whatever prompt
// the session happens to be showing. And an item with neither questions nor
// options was never a menu, so it takes the prose path.
func MenuQuestionsOf(ctx context.Context, item Item) ([]MenuQuestion, bool) {
	if item.AnswerMechanism != MessageAnswerMechanism {
		return nil, false
	}
	if item.ProvenanceClass != HookProvenanceClass {
		return nil, false
	}
	if len(item.Questions) > 0 {
		return questionsToMenu(item.Questions), true
	}
	if len(item.Options) > 0 {
		return []MenuQuestion{{
			Labels: optionLabels(item.Options),
			Multi:  item.AnswerCardinality == MultipleAnswerCardinality,
		}}, true
	}
	return nil, false
}

// questionsToMenu converts stored question units into menu units.
func questionsToMenu(questions Questions) []MenuQuestion {
	menu := make([]MenuQuestion, 0, len(questions))
	for _, question := range questions {
		menu = append(menu, MenuQuestion{
			Tab:    question.Tab,
			Labels: optionLabels(question.Options),
			Multi:  question.Cardinality == MultipleAnswerCardinality,
		})
	}
	return menu
}

// optionLabels projects the labels off an option list.
func optionLabels(options AnswerOptions) []string {
	labels := make([]string, 0, len(options))
	for _, option := range options {
		labels = append(labels, option.Label)
	}
	return labels
}

// MenuDigits returns the key presses that answer the menu, in order, or an error
// naming why the answer cannot be keyed.
//
// Every sequence was measured on live menus:
//
//	option, single   its digit — selects and advances at once
//	option, multiple each digit toggles its box (the cursor stays), then Tab
//	                 leaves the question for the next tab / the review screen
//	text             the "Type something." row's digit (labels + 1) opens the
//	                 input, the text is typed, Enter commits and advances
//	skip             Tab — leaves the question unanswered
//	decline          a lone ESC — EVERY question was skipped, so the whole ask is
//	                 cancelled and the gate is RELEASED rather than left waiting
//
// No ARROW key is ever produced: arrows are `ESC [ B`-style sequences and the
// lone ESC that cancels a menu is their prefix.
func MenuDigits(
	ctx context.Context,
	questions []MenuQuestion,
	answers Answers,
	single *Answer,
) ([]string, error) {
	if len(questions) == 0 {
		return nil, errors.Errorf(ctx, "no questions to answer")
	}
	units := menuUnits(questions, answers, single)
	// A decline must RELEASE the gate, not leave it waiting. Every question
	// answered `skip` — the board's Dismiss — means the operator declined the
	// whole ask, and with nothing picked the menu's only exit is Esc. Measured:
	// the answer was recorded `skip`, the card read `answered`, and the menu was
	// STILL open at T+5min, so the worker waited on a gate the operator had
	// already declined.
	if allExplicitSkip(units) {
		return []string{menuCancel}, nil
	}
	// An absent unit (the board sent no entry for that question) is not the
	// operator declining — it falls through to the per-question refusal below.
	if allAbsentOrSkip(units) {
		return nil, errors.Errorf(
			ctx,
			"every question was skipped or left unanswered — nothing to deliver",
		)
	}
	keys := make([]string, 0, len(questions)+2)
	for i, question := range questions {
		next, err := questionKeys(ctx, question, units[i], len(questions) > 1)
		if err != nil {
			return nil, err
		}
		keys = append(keys, next...)
	}
	return keys, nil
}

// menuUnits pairs each question with the answer that belongs to it.
//
// A single-question menu reads the item's own Answer; a multi-question menu
// pairs by tab, because that is how the store keys per-question answers.
func menuUnits(questions []MenuQuestion, answers Answers, single *Answer) []*Answer {
	if len(questions) == 1 && len(answers) == 0 {
		return []*Answer{single}
	}
	byTab := make(map[string]*Answer, len(answers))
	for i := range answers {
		byTab[answers[i].Question] = &answers[i].Answer
	}
	units := make([]*Answer, 0, len(questions))
	for _, question := range questions {
		units = append(units, byTab[question.Tab])
	}
	return units
}

// allExplicitSkip reports whether every question was answered with an explicit
// skip.
func allExplicitSkip(units []*Answer) bool {
	for _, unit := range units {
		if unit == nil || unit.Kind != SkipAnswerKind {
			return false
		}
	}
	return true
}

// allAbsentOrSkip reports whether no question carries answer content.
func allAbsentOrSkip(units []*Answer) bool {
	for _, unit := range units {
		if unit != nil && unit.Kind != "" && unit.Kind != SkipAnswerKind {
			return false
		}
	}
	return true
}

// questionKeys returns the key presses for one question.
func questionKeys(
	ctx context.Context,
	question MenuQuestion,
	unit *Answer,
	multiQuestion bool,
) ([]string, error) {
	name := questionName(question.Tab)
	kind := AnswerKind("")
	if unit != nil {
		kind = unit.Kind
	}
	// The board sends an entry only for questions the operator answered, so an
	// absent entry on a multi-question menu is a question left alone — Tab past
	// it exactly like an explicit skip. On a single-question menu it stays
	// absent, which the refusal below reports.
	if kind == "" && multiQuestion {
		kind = SkipAnswerKind
	}
	switch kind {
	case SkipAnswerKind:
		return []string{"\t"}, nil
	case TextAnswerKind:
		return textKeys(ctx, name, question, unit)
	case OptionAnswerKind:
		return optionKeys(ctx, name, question, unit)
	default:
		return nil, errors.Errorf(
			ctx,
			"%s was answered with %s — not keyed",
			name,
			kindOrNothing(kind),
		)
	}
}

// textKeys returns the key presses for a free-text answer.
func textKeys(
	ctx context.Context,
	name string,
	question MenuQuestion,
	unit *Answer,
) ([]string, error) {
	text := strings.TrimSpace(unit.Value)
	if text == "" {
		return nil, errors.Errorf(ctx, "%s: free-text answer is empty", name)
	}
	// The "Type something." row sits one past the options, and a row above 9 has
	// no single digit to press.
	row := len(question.Labels) + 1
	if row > 9 {
		return nil, errors.Errorf(ctx, "%s: the free-text row %d has no single digit", name, row)
	}
	typed := strings.NewReplacer("\r", " ", "\n", " / ").Replace(text)
	return []string{strconv.Itoa(row), typed, "\r"}, nil
}

// optionKeys returns the key presses for a pick-one or pick-many answer.
func optionKeys(
	ctx context.Context,
	name string,
	question MenuQuestion,
	unit *Answer,
) ([]string, error) {
	picks := unit.Values
	if len(picks) == 0 {
		picks = []string{unit.Value}
	}
	digits := make([]string, 0, len(picks))
	for _, label := range picks {
		index := indexOfLabel(question.Labels, label)
		if index < 0 {
			return nil, errors.Errorf(ctx, "%s: answer %q is not one of its options", name, label)
		}
		if index+1 > 9 {
			return nil, errors.Errorf(ctx, "%s: option %d has no single digit", name, index+1)
		}
		digits = append(digits, strconv.Itoa(index+1))
	}
	if question.Multi {
		// Tab leaves the question once its boxes are toggled.
		return append(digits, "\t"), nil
	}
	if len(digits) == 1 {
		return digits, nil
	}
	return nil, errors.Errorf(
		ctx,
		"%s is single-select but was answered with %d options",
		name,
		len(digits),
	)
}

// indexOfLabel returns the label's position, or -1.
func indexOfLabel(labels []string, label string) int {
	for i, candidate := range labels {
		if candidate == label {
			return i
		}
	}
	return -1
}

// questionName names a question in an error, so a refusal reads correctly for
// both a tabbed menu and a single-question one.
func questionName(tab string) string {
	if tab == "" {
		return "the question"
	}
	return tab
}

// kindOrNothing renders an absent answer kind for an error message.
func kindOrNothing(kind AnswerKind) string {
	if kind == "" {
		return "nothing"
	}
	return string(kind)
}

// MenuAnswerJob picks the board's options in an open AskUserQuestion menu.
//
// ⚠️ It is LIVE KEYBOARD AUTOMATION: every key press goes into a session that is
// blocked on the operator. That is why the plan is computed in full before a
// single key is sent — a partial sequence would leave the menu half-answered
// with no way to undo it.
type MenuAnswerJob interface {
	// Run answers menu cards until the context is cancelled. It logs and
	// continues on a failed cycle, because a missed delivery costs one interval.
	Run(ctx context.Context) error

	// DeliverOnce runs one menu cycle. It is a seam so a spec can drive a cycle
	// deterministically instead of racing a ticker.
	DeliverOnce(ctx context.Context) error
}

// MenuAnswerOption customises the job at construction.
type MenuAnswerOption func(*menuAnswerJob)

// WithMenuAnswerTimings overrides the keystroke loop's pace.
//
// It exists for specs: the loop's SHAPE is what a spec pins, and a spec that
// waited the real 250 ms settle and 12 submit polls per case would take minutes.
// A zero keySettle is legal — the loop still polls submitPolls times.
func WithMenuAnswerTimings(keySettle time.Duration, submitPolls int) MenuAnswerOption {
	return func(job *menuAnswerJob) {
		job.keySettle = keySettle
		job.submitPolls = submitPolls
	}
}

// NewMenuAnswerJob creates the menu arm.
//
// The locator and the writer are separate because the pane is resolved by the
// same lease logic the prose carrier uses, while the keys are typed by the
// writer — sharing one copy of the resolution rather than a second guess at
// which pane a recycled id now names.
func NewMenuAnswerJob(
	store AttentionStore,
	locator PaneLocator,
	writer PaneTextWriter,
	interval time.Duration,
	options ...MenuAnswerOption,
) MenuAnswerJob {
	job := &menuAnswerJob{
		store:       store,
		locator:     locator,
		writer:      writer,
		interval:    interval,
		keySettle:   defaultMenuKeySettle,
		submitPolls: defaultMenuSubmitPolls,
	}
	for _, option := range options {
		option(job)
	}
	return job
}

type menuAnswerJob struct {
	store       AttentionStore
	locator     PaneLocator
	writer      PaneTextWriter
	interval    time.Duration
	keySettle   time.Duration
	submitPolls int
}

// Run answers menu cards until the context is cancelled.
func (j *menuAnswerJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "menu answer interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("job=attention-watcher action=menu stopped")
			return nil
		case <-ticker.C:
			if err := j.DeliverOnce(ctx); err != nil {
				glog.Warningf("job=attention-watcher action=menu_cycle_error error=%v", err)
			}
		}
	}
}

// DeliverOnce answers every answered menu card that has not yet been delivered.
func (j *menuAnswerJob) DeliverOnce(ctx context.Context) error {
	attempts, err := j.store.DeliveryAttempts(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read delivery attempts failed")
	}
	items, err := j.store.ReadBoard(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read board failed")
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil
		}
		questions, ok := j.menuDeliverable(ctx, item, attempts)
		if !ok {
			continue
		}
		j.deliver(ctx, item, questions)
	}
	return nil
}

// menuDeliverable reports whether the item is an answered menu card this arm
// should answer, returning its question units.
func (j *menuAnswerJob) menuDeliverable(
	ctx context.Context,
	item Item,
	attempts map[ItemID]DeliveryAttempt,
) ([]MenuQuestion, bool) {
	if item.State != AnsweredState || item.AnsweredAt == nil {
		return nil, false
	}
	// The originate arm settled this one; the session has already moved on.
	if item.AnsweredBy == attentionWatcherAnsweredBy {
		return nil, false
	}
	if attempt, ok := attempts[item.ItemID]; ok && attempt.Outcome == DeliveredOutcome {
		return nil, false
	}
	if _, ok := itemSessionID(ctx, item); !ok {
		return nil, false
	}
	return MenuQuestionsOf(ctx, item)
}

// deliver answers one menu card and records what it observed.
func (j *menuAnswerJob) deliver(
	ctx context.Context,
	item Item,
	questions []MenuQuestion,
) {
	keys, err := MenuDigits(ctx, questions, item.Answers, item.Answer)
	if err != nil {
		glog.V(2).Infof(
			"job=attention-watcher action=menu item=%s verdict=%s reason=%q",
			item.ItemID,
			DeliveryRefuse,
			err.Error(),
		)
		j.recordAttempt(ctx, item, FailedOutcome)
		return
	}
	sessionID, ok := itemSessionID(ctx, item)
	if !ok {
		return
	}
	// Pane 0 makes the locator resolve the pane from the session's own tty,
	// which is the correct path for a card whose recorded pane was never ours.
	delivery := &PaneDelivery{SessionID: sessionID, StoreItemID: string(item.ItemID)}
	verdict, pane, reason := j.locator.Locate(ctx, delivery)
	if verdict != DeliveryOK {
		glog.V(2).Infof(
			"job=attention-watcher action=menu item=%s verdict=%s reason=%q",
			item.ItemID,
			verdict,
			reason,
		)
		if verdict == DeliveryRefuse {
			j.recordAttempt(ctx, item, FailedOutcome)
		}
		return
	}
	verdict, reason = j.answer(ctx, pane, keys)
	glog.V(2).Infof(
		"job=attention-watcher action=menu item=%s verdict=%s reason=%q",
		item.ItemID,
		verdict,
		reason,
	)
	switch verdict {
	case DeliveryOK:
		j.recordAttempt(ctx, item, DeliveredOutcome)
	case DeliveryRefuse:
		j.recordAttempt(ctx, item, FailedOutcome)
	default:
		// `retry` records nothing: the answer has not been delivered and will be
		// attempted again, so a record now would date a fact still pending.
	}
}

// answer types the keys into the pane and confirms the menu closed.
func (j *menuAnswerJob) answer(
	ctx context.Context,
	pane int,
	keys []string,
) (DeliveryVerdict, string) {
	text, ok := j.writer.Text(ctx, pane)
	if !ok || !strings.Contains(text, menuMark) {
		// Nothing typed: the menu is not showing (yet). Retry rather than refuse —
		// the pane may still be drawing it, and a refusal would drop the answer.
		return DeliveryRetry, "pane " + strconv.Itoa(pane) + " shows no open menu"
	}
	for _, key := range keys {
		if err := j.writer.SendText(ctx, pane, key); err != nil {
			return DeliveryRetry, "send-text into pane " + strconv.Itoa(pane) + " failed"
		}
		// A multi-character key (a typed answer) needs longer to draw than a
		// single digit, so the settle time scales with the key's length.
		settle := j.keySettle * 2
		if len(key) > 1 {
			settle = j.keySettle * 4
		}
		time.Sleep(settle)
	}
	return j.finishMenu(ctx, pane, keys)
}

// finishMenu submits a multi-question menu's review screen, then confirms the
// menu closed.
//
// The review screen ("Review your answers" / "N. Submit answers") carries no
// `Enter to select`, so the Submit row is looked for FIRST — checking the mark
// first reported "closed" while the review screen sat unsubmitted.
//
// ⚠️ It returns `retry`, not `ok`, when the menu is still open after the submit
// loop. The Python arm returned `ok` there, which recorded the answer
// `delivered` while the session stayed blocked on a gate nobody had answered —
// the exact false-`ok` failure the Go delivery path exists to remove.
func (j *menuAnswerJob) finishMenu(
	ctx context.Context,
	pane int,
	keys []string,
) (DeliveryVerdict, string) {
	cancelled := len(keys) == 1 && keys[0] == menuCancel
	picked := describeKeys(keys)
	for i := 0; i < j.submitPolls; i++ {
		text, ok := j.writer.Text(ctx, pane)
		if !ok {
			text = ""
		}
		if submit := submitRowPattern.FindAllStringSubmatch(text, -1); len(submit) > 0 {
			// The row's own digit, read off the screen — the last match wins, as
			// the review screen renders the Submit row once at its foot.
			if err := j.writer.SendText(ctx, pane, submit[len(submit)-1][1]); err != nil {
				glog.V(2).Infof("send submit into pane %d failed: %v", pane, err)
			}
		} else if !strings.Contains(text, menuMark) {
			if cancelled {
				return DeliveryOK, "cancelled the menu in pane " + strconv.Itoa(
					pane,
				) + " — gate released as a decline"
			}
			return DeliveryOK, "picked " + picked + " in pane " + strconv.Itoa(pane) + "; menu closed"
		}
		time.Sleep(j.keySettle * 2)
	}
	if cancelled {
		return DeliveryRetry, "cancelled the menu in pane " + strconv.Itoa(
			pane,
		) + ", but it is still open"
	}
	return DeliveryRetry, "picked " + picked + " in pane " + strconv.Itoa(
		pane,
	) + ", but the menu is still open"
}

// describeKeys renders a key sequence for a log reason.
func describeKeys(keys []string) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		switch key {
		case "\t":
			parts = append(parts, "Tab")
		case "\r":
			parts = append(parts, "Enter")
		case menuCancel:
			parts = append(parts, "Esc")
		default:
			if len(key) == 1 {
				parts = append(parts, key)
			} else {
				parts = append(parts, "<text>")
			}
		}
	}
	return strings.Join(parts, "/")
}

// recordAttempt writes the trail entry, logging rather than failing: the keys
// have already been sent, and a missing trail entry must not read as a failed
// delivery.
func (j *menuAnswerJob) recordAttempt(
	ctx context.Context,
	item Item,
	outcome DeliveryOutcome,
) {
	if _, err := j.store.RecordAttempt(ctx, item.ItemID, attentionWatcherCarrier, outcome); err != nil {
		glog.Warningf(
			"job=attention-watcher action=menu_attempt_error item=%s outcome=%s error=%v",
			item.ItemID,
			outcome,
			err,
		)
	}
}

// Compile-time guard: the job must satisfy its own interface.
var _ MenuAnswerJob = (*menuAnswerJob)(nil)
