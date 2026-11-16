// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strconv"
	"strings"
	"sync"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// AnswerContext is what a headless delivery needs beyond the item itself: the
// workspace the resumed child starts in, and the vault task the session is
// anchored to.
//
// Both are resolved from the item's own provenance — never guessed. An empty
// Cwd is legal (the wrapper inherits its own), and an empty TaskName is the
// normal shape for a session that is anchored to no task, which is why the
// delivery simply omits the closing command rather than inventing a name.
type AnswerContext struct {
	// Cwd is the session's working directory, used as the resumed child's cwd.
	Cwd string
	// TaskName is the vault task title, or "" when nothing resolved.
	TaskName string
}

// AnswerContextResolver resolves the delivery context for one answered item.
type AnswerContextResolver interface {
	// AnswerContext returns the item's delivery context, or a zero value when
	// nothing could be resolved.
	AnswerContext(ctx context.Context, item Item) AnswerContext
}

// NewProvenanceAnswerContext creates a resolver reading the item's cwd and task
// off the board's own provenance resolver.
//
// ⚠️ It reuses the provenance resolver rather than a second lookup of the vault
// index and the session registry: a second source would be a second answer to
// "which task is this session on", and the board would then be able to show one
// task while the delivery named another. A nil resolver resolves nothing, which
// is what a host with no configured vault gets.
func NewProvenanceAnswerContext(resolver ProvenanceResolver) AnswerContextResolver {
	return &provenanceAnswerContext{resolver: resolver}
}

type provenanceAnswerContext struct {
	resolver ProvenanceResolver
}

func (r *provenanceAnswerContext) AnswerContext(
	ctx context.Context,
	item Item,
) AnswerContext {
	if r.resolver == nil {
		return AnswerContext{}
	}
	provenance, ok := r.resolver.Resolve(ctx, Items{item})[item.ItemID]
	if !ok {
		return AnswerContext{}
	}
	return AnswerContext{Cwd: provenance.Cwd, TaskName: provenance.TaskName}
}

// HeadlessAnswerDeliverer carries an answered card into a session that has no
// pane.
//
// ⚠️ It exists because a pane-less session is pane-less BY CONSTRUCTION — every
// headless `sdk-cli` worker — so the pane path does not fail on those cards, it
// has nothing to type into. Treating that as "nothing to do" silently dropped
// the operator's answer, and the worker re-asked the identical question.
type HeadlessAnswerDeliverer interface {
	// DeliverHeadless attempts one delivery of an answered card.
	//
	// It returns true when the card is TERMINAL for this arm — delivered, failed
	// for good, or expired — and false when the answer is still pending, which
	// is what makes the next watcher cycle retry it.
	DeliverHeadless(ctx context.Context, item Item) bool
}

// NewHeadlessAnswerDelivery creates the headless delivery arm.
func NewHeadlessAnswerDelivery(
	store AttentionStore,
	wrapper WrapperMessageDeliverer,
	contexts AnswerContextResolver,
	now libtime.CurrentDateTimeGetter,
) HeadlessAnswerDeliverer {
	return &headlessAnswerDelivery{
		store:     store,
		wrapper:   wrapper,
		contexts:  contexts,
		now:       now,
		delivered: map[ItemID]bool{},
		attempts:  map[ItemID]int{},
		inFlight:  map[ItemID]bool{},
	}
}

type headlessAnswerDelivery struct {
	store    AttentionStore
	wrapper  WrapperMessageDeliverer
	contexts AnswerContextResolver
	now      libtime.CurrentDateTimeGetter
	// mu guards delivered, attempts and inFlight.
	//
	// ⚠️ ONE instance is wired into BOTH the attention-watcher job and the
	// menu-answer job, and those run concurrently on `run.NewConcurrentRunner`.
	// Go treats a concurrent map read/write as an unrecoverable fatal error — not
	// a recoverable panic — so an unsynchronised map here crashes the whole
	// controller the first time a menu card and a non-menu headless card are
	// delivered at the same instant.
	mu sync.Mutex
	// delivered is the at-most-once guard. The store's own `delivered` attempt
	// record is the durable half, but a delivery whose record write failed would
	// otherwise be re-sent on the next cycle — and a re-sent turn is a second
	// instruction to a worker that already acted on the first.
	delivered map[ItemID]bool
	// attempts counts the retries logged for one card, so the log line carries
	// the count the operator reads as "this has been failing for N cycles".
	attempts map[ItemID]int
	// inFlight marks a card a concurrent cycle is already working on, so two jobs
	// racing on one card deliver it once rather than twice.
	inFlight map[ItemID]bool
}

// begin reserves a card for this cycle.
//
// It returns terminal=true when the card is already settled (nothing to do) and
// reserved=false when another cycle holds it — a non-terminal skip, because the
// owning cycle still has to report the outcome.
func (d *headlessAnswerDelivery) begin(id ItemID) (terminal bool, reserved bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.delivered[id] {
		return true, false
	}
	if d.inFlight[id] {
		return false, false
	}
	d.inFlight[id] = true
	return false, true
}

// release clears the in-flight mark for a card this cycle reserved.
func (d *headlessAnswerDelivery) release(id ItemID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.inFlight, id)
}

// DeliverHeadless attempts one delivery, retrying until delivered once or the
// item's own deadline passes.
func (d *headlessAnswerDelivery) DeliverHeadless(ctx context.Context, item Item) bool {
	if ctx.Err() != nil {
		// The cycle was cancelled before it could act — a shutdown, or a runner
		// draining. The card stays pending: nothing was attempted, so nothing may
		// be recorded, and the next cycle owns it.
		return false
	}
	terminal, reserved := d.begin(item.ItemID)
	if terminal {
		return true
	}
	if !reserved {
		// A concurrent cycle owns this card. Not terminal — that cycle still has
		// to report the outcome — so this cycle reports "not done" and moves on.
		return false
	}
	defer d.release(item.ItemID)
	if d.expired(item) {
		d.settle(ctx, item, FailedOutcome,
			"answer expired before it could be delivered ("+d.attemptsOf(item)+")")
		return true
	}
	if d.wrapper == nil {
		// Nothing can deliver on this host. Left pending rather than failed: an
		// unconfigured wrapper is a configuration defect the item's deadline
		// bounds, and recording `failed` here would report a delivery attempt
		// that never happened.
		d.retry(item, "no wrapper configured")
		return false
	}
	sessionID, ok := deliverySessionID(ctx, item)
	if !ok {
		d.settle(ctx, item, FailedOutcome,
			"no wrapper-keyable session id for item "+string(item.ItemID))
		return true
	}
	answerContext := AnswerContext{}
	if d.contexts != nil {
		answerContext = d.contexts.AnswerContext(ctx, item)
	}
	text, ok := AnswerTurnText(ctx, item, answerContext)
	if !ok {
		d.settle(ctx, item, FailedOutcome, "the card carries no answer to deliver")
		return true
	}
	verdict, reason := d.wrapper.DeliverMessage(ctx, sessionID, answerContext.Cwd, text)
	switch verdict {
	case DeliveryOK:
		d.settle(ctx, item, DeliveredOutcome, reason)
		return true
	case DeliveryRefuse:
		d.settle(ctx, item, FailedOutcome, reason)
		return true
	default:
		d.retry(item, reason)
		return false
	}
}

// expired reports whether the producer's own deadline has passed.
//
// An absent deadline never expires — the schema's default — so a card with no
// `expires_at` is retried for as long as the board holds it.
func (d *headlessAnswerDelivery) expired(item Item) bool {
	if item.ExpiresAt == nil || d.now == nil {
		return false
	}
	return !d.now.Now().Time().Before(item.ExpiresAt.Time())
}

// settle records the card's terminal outcome and stops further cycles.
func (d *headlessAnswerDelivery) settle(
	ctx context.Context,
	item Item,
	outcome DeliveryOutcome,
	reason string,
) {
	d.mu.Lock()
	d.delivered[item.ItemID] = true
	attempts := d.attempts[item.ItemID]
	d.mu.Unlock()
	glog.V(2).Infof(
		"job=attention-watcher action=headless item=%s verdict=%s attempts=%d reason=%q",
		item.ItemID,
		outcome,
		attempts,
		reason,
	)
	if _, err := d.store.RecordAttempt(
		ctx,
		item.ItemID,
		attentionWatcherCarrier,
		outcome,
	); err != nil {
		glog.Warningf(
			"job=attention-watcher action=headless_attempt_error item=%s outcome=%s error=%v",
			item.ItemID,
			outcome,
			err,
		)
	}
}

// retry logs a cycle that delivered nothing and leaves the answer pending.
//
// ⚠️ The line is the record. Nothing is written to the attempt trail — a retry
// is not a failure yet — so without it a delivery that keeps failing is
// indistinguishable from one that is quietly not attempted, which is exactly how
// this defect stayed invisible.
func (d *headlessAnswerDelivery) retry(item Item, reason string) {
	d.mu.Lock()
	d.attempts[item.ItemID]++
	attempts := d.attempts[item.ItemID]
	d.mu.Unlock()
	glog.V(2).Infof(
		"job=attention-watcher action=headless_retry item=%s session=%s attempts=%d reason=%q",
		item.ItemID,
		shortID(string(item.ProducerID)),
		attempts,
		reason,
	)
}

// attemptsOf renders the retry count for an expiry reason.
func (d *headlessAnswerDelivery) attemptsOf(item Item) string {
	d.mu.Lock()
	attempts := d.attempts[item.ItemID]
	d.mu.Unlock()
	return "after " + strconv.Itoa(attempts) + " attempts"
}

// deliverySessionID returns the id the wrapper is keyed by.
//
// ⚠️ The producer id is the answer's own target: the item was asked by that
// session, so that is the conversation the answer belongs to. The liveness ref
// is the fallback rather than the first choice — an `owner:` item's ref names
// the owner, which is not the same session, and delivering there would move the
// answer into a conversation that never asked.
func deliverySessionID(ctx context.Context, item Item) (string, bool) {
	if isUUID(string(item.ProducerID)) {
		return string(item.ProducerID), true
	}
	if sessionID, ok := itemSessionID(ctx, item); ok && isUUID(sessionID) {
		return sessionID, true
	}
	return "", false
}

// AnswerTurnText renders the turn that carries an operator's answer into a
// pane-less session.
//
// The turn must stand on its own: the session has just ended a turn and has no
// memory of the card, so the question it answered is carried back with the
// answer. The closing command is appended only when the task name resolved —
// `/vault-cli:work-on-task ""` would name no task, and a resume with no prompt
// is a no-op, so the answer itself is what makes the turn non-empty.
func AnswerTurnText(ctx context.Context, item Item, answerContext AnswerContext) (string, bool) {
	answers := answerLines(item)
	if len(answers) == 0 {
		return "", false
	}
	var builder strings.Builder
	builder.WriteString("The operator answered on the attention board.\n")
	builder.WriteString("\nQuestion: ")
	builder.WriteString(strings.TrimSpace(string(item.Payload)))
	builder.WriteString("\n")
	if itemContext := strings.TrimSpace(string(item.Context)); itemContext != "" {
		builder.WriteString("\nContext: ")
		builder.WriteString(itemContext)
		builder.WriteString("\n")
	}
	if options := menuOptionLines(ctx, item); len(options) > 0 {
		builder.WriteString("\nOptions offered:\n")
		for _, option := range options {
			builder.WriteString(option)
			builder.WriteString("\n")
		}
	}
	builder.WriteString("\nAnswer:\n")
	for _, answer := range answers {
		builder.WriteString("- ")
		builder.WriteString(answer)
		builder.WriteString("\n")
	}
	if task := strings.TrimSpace(answerContext.TaskName); task != "" {
		builder.WriteString("\nThen continue the task: /vault-cli:work-on-task ")
		builder.WriteString(shellQuote(task))
		builder.WriteString("\n")
	}
	return builder.String(), true
}

// shellQuote wraps a value in single quotes for a shell, escaping every embedded
// single quote as '\”.
//
// ⚠️ The task name reaches this function from a vault page any worker can write,
// and vault task names routinely carry apostrophes ("A Worker's Ask …"). An
// unquoted interpolation both breaks the command — the shell ends the argument at
// the apostrophe — and lets a quote in a name arrive at the shell as syntax. Single
// quotes suppress every expansion ($, backtick, glob), which is why the escaping is
// the `'\”` form rather than a double-quoted one.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// answerLines renders the answer content the card carries, or an empty list
// when there is nothing to deliver.
func answerLines(item Item) []string {
	lines := make([]string, 0, len(item.Answers)+1)
	if item.Decision != "" {
		lines = append(lines, string(item.Decision))
	}
	if line, ok := answerLine(item.Answer); ok {
		lines = append(lines, line)
	}
	for _, answer := range item.Answers {
		line, ok := answerLine(&answer.Answer)
		if !ok {
			continue
		}
		if answer.Question != "" {
			line = answer.Question + ": " + line
		}
		lines = append(lines, line)
	}
	return lines
}

// answerLine renders one answer, or false when it carries no content.
//
// A `hidden` answer renders nothing on purpose: it means the operator answered
// the question elsewhere and only wanted the card off the board, so delivering
// it would assert an answer nobody gave here.
func answerLine(answer *Answer) (string, bool) {
	if answer == nil {
		return "", false
	}
	switch answer.Kind {
	case SkipAnswerKind:
		return "skipped", true
	case OptionAnswerKind:
		if len(answer.Values) > 0 {
			return strings.Join(answer.Values, ", "), true
		}
	case HiddenAnswerKind:
		return "", false
	}
	if value := strings.TrimSpace(answer.Value); value != "" {
		return value, true
	}
	return "", false
}

// menuOptionLines renders each question unit's offered options, so the answer's
// label can be read against the choices it was picked from.
func menuOptionLines(ctx context.Context, item Item) []string {
	questions, ok := MenuQuestionsOf(ctx, item)
	if !ok {
		return nil
	}
	lines := make([]string, 0, len(questions))
	for _, question := range questions {
		if len(question.Labels) == 0 {
			continue
		}
		lines = append(lines, "- "+questionName(question.Tab)+": "+
			strings.Join(question.Labels, " / "))
	}
	return lines
}

// Compile-time guards.
var (
	_ HeadlessAnswerDeliverer = (*headlessAnswerDelivery)(nil)
	_ AnswerContextResolver   = (*provenanceAnswerContext)(nil)
)
