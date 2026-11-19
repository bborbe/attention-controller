// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command permission-answer-hook is the Go replacement for
// permission-answer-poll.py: it carries a parked PermissionRequest gate's answer
// from the attention store back to the waiting harness.
//
// ⚠️ The Python script re-read the store every two seconds; this hook holds ONE
// stream subscription instead, so an operator's answer reaches the gate on the
// store's write rather than up to a poll interval later. A PermissionRequest
// hook is invoked per call by the harness, so the process itself stays — only
// the polling loop goes.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg"
)

// HookDeadline bounds one hook invocation end to end.
const HookDeadline = 540 * time.Second

// ResolveGrace bounds the pre-subscription wait for the session's card to exist.
const ResolveGrace = 10 * time.Second

// FeedGrace bounds how long the hook waits to see the producer's open item
// before deciding the gate is not feed-tracked.
const FeedGrace = 10 * time.Second

const (
	// attemptCarrier names this arm in the delivery-attempt record the task's
	// SC2 reads as received_at. Declared here even though the POST itself lands
	// in a later step.
	attemptCarrier = "supervisor:permission-answer-poll"
	// notBeforeTolerance widens the hook's start time so a card created a moment
	// before it is not mistaken for a stale one.
	notBeforeTolerance = 5 * time.Second
	// resolveRetryInterval paces the bounded resolve window.
	resolveRetryInterval = time.Second
	// stateCheckInterval paces the producer state-file re-read.
	stateCheckInterval = time.Second

	// boardURLEnv names the board URL the hook reads from.
	boardURLEnv = "ATTENTION_BOARD_URL"
	// defaultBoardURL is the local board the hook talks to.
	defaultBoardURL = "http://127.0.0.1:18080"
	// requestTimeout bounds one board request.
	requestTimeout = 10 * time.Second
	// maxBodyBytes caps how much of any body is read.
	maxBodyBytes = 1 << 20
	// maxExpectRunes caps the "<tool>: <detail>" prefix.
	maxExpectRunes = 200
	// maxDetailRunes caps the compact-JSON fallback detail.
	maxDetailRunes = 120

	// streamPath is the board's change feed.
	streamPath = "/api/1.0/attention/stream"
	// dataPrefix frames one event payload line in the feed.
	dataPrefix = "data:"
	// openItemSuffix is the producer's open-gate marker file name suffix.
	openItemSuffix = ".open.json"
	// stateDirEnvName names the directory the producer writes its state into.
	stateDirEnvName = "ATTENTION_STATE_DIR"
	// defaultStateDirSuffix is the state directory under the home directory,
	// matching pkg/attnlog/store.go's default.
	defaultStateDirSuffix = ".claude/state/attention"

	// localCloseReason names the in-pane answer the hook must not race.
	localCloseReason = "gate closed locally"
	// untrackedReason names a gate the producer never recorded as open.
	untrackedReason = "gate is not feed-tracked: no open item seen within FeedGrace"
	// streamClosedReason names a stream that dropped mid-run.
	streamClosedReason = "stream closed"
)

// Clock is the hook's time source. It is injected so a spec advances time
// instead of blocking: a blocking sleep and a repeating ticker are banned in
// this package.
type Clock interface {
	Now() time.Time
	// Timer returns a channel that receives once d has elapsed on this clock.
	Timer(d time.Duration) <-chan time.Time
}

// realClock is the process clock.
type realClock struct{}

// Now returns the process time.
func (realClock) Now() time.Time { return time.Now() }

// Timer returns a one-shot process timer's channel.
func (realClock) Timer(d time.Duration) <-chan time.Time { return time.NewTimer(d).C }

// storeClient is the hook's view of the attention store. A spec injects a
// recording fake, which is how "no store traffic" is proven rather than
// asserted.
type storeClient interface {
	List(ctx context.Context) (pkg.Items, error)
	Get(ctx context.Context, itemID pkg.ItemID) (*pkg.Item, error)
	RecordAttempt(
		ctx context.Context,
		itemID pkg.ItemID,
		carrier string,
		outcome pkg.DeliveryOutcome,
	) error
	Subscribe(ctx context.Context) (Subscription, error)
}

// StreamEvent is one change the board's stream reported. The stream is a
// change signal, not the item, so the hook re-reads the item by id.
type StreamEvent struct {
	Type   string `json:"type"`
	ItemID string `json:"item_id"`
}

// Subscription is one open stream subscription.
type Subscription interface {
	// Events delivers change events until the stream ends or ctx is done.
	Events() <-chan StreamEvent
	// Close releases the connection.
	Close()
}

// httpStoreClient is the loopback HTTP implementation of storeClient.
type httpStoreClient struct {
	baseURL string
	client  *http.Client
	// stream carries no client timeout: a stream is long-lived by design, so
	// its lifetime is the context's, not a fixed wall-clock bound.
	stream *http.Client
}

// newHTTPStoreClient creates the board client.
//
// ⚠️ A base URL that is not loopback disables the client: the hook talks only
// to the local board, so a value pointing anywhere else is refused rather than
// dialled.
func newHTTPStoreClient(baseURL string) storeClient {
	return &httpStoreClient{
		baseURL: normaliseBoardURL(baseURL),
		client:  &http.Client{Timeout: requestTimeout},
		stream:  &http.Client{},
	}
}

// normaliseBoardURL returns the loopback base URL, or "" when the value is not
// one. It mirrors normaliseWrapperURL rather than inventing a second policy.
func normaliseBoardURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	host := parsed.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		return ""
	}
	return raw
}

// List reads every open item.
func (c *httpStoreClient) List(ctx context.Context) (pkg.Items, error) {
	var items pkg.Items
	if err := c.do(ctx, http.MethodGet, "/api/1.0/attention", nil, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Get reads one item by id. A 404 wraps pkg.ErrItemNotFound.
func (c *httpStoreClient) Get(ctx context.Context, itemID pkg.ItemID) (*pkg.Item, error) {
	var item pkg.Item
	path := "/api/1.0/attention/" + url.PathEscape(itemID.String())
	if err := c.do(ctx, http.MethodGet, path, nil, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// RecordAttempt writes the delivery-attempt record for one item.
func (c *httpStoreClient) RecordAttempt(
	ctx context.Context,
	itemID pkg.ItemID,
	carrier string,
	outcome pkg.DeliveryOutcome,
) error {
	payload, err := json.Marshal(map[string]string{
		"carrier": carrier,
		"outcome": outcome.String(),
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "encode attempt for %s failed", itemID)
	}
	path := "/api/1.0/attention/" + url.PathEscape(itemID.String()) + "/attempt"
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(payload), nil)
}

// Subscribe opens the board's change feed.
//
// ⚠️ It returns only once the response headers have been received, which is
// what "established" means: the server writes its baseline and flushes the
// headers before the first byte of the body (pkg/handler/attention-stream.go
// takes the snapshot and then flushes), so a read issued after Subscribe returns
// is outside the baseline gap. Returning before Do completes would put the
// catch-up read back inside it.
func (c *httpStoreClient) Subscribe(ctx context.Context) (Subscription, error) {
	if c.baseURL == "" {
		return nil, errors.New(ctx, "no board configured (board URL is not loopback)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+streamPath, nil)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "build stream request failed")
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		glog.V(2).Infof("board stream open failed: %v", err)
		return nil, errors.Wrapf(ctx, err, "open attention stream failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, errors.Wrapf(
			ctx,
			errors.New(ctx, "unexpected status"),
			"attention stream returned %d",
			resp.StatusCode,
		)
	}
	subscription := &httpSubscription{resp: resp, events: make(chan StreamEvent)}
	go subscription.read(ctx)
	return subscription, nil
}

// httpSubscription is one open stream: a body scanner feeding an event channel.
type httpSubscription struct {
	resp   *http.Response
	events chan StreamEvent
	once   sync.Once
}

// Events delivers change events until the body ends or ctx is done.
func (s *httpSubscription) Events() <-chan StreamEvent { return s.events }

// Close releases the connection. It is idempotent because both the reader and
// the caller close it.
func (s *httpSubscription) Close() { s.once.Do(func() { _ = s.resp.Body.Close() }) }

// read frames the body into events and closes the channel when it ends.
func (s *httpSubscription) read(ctx context.Context) {
	defer close(s.events)
	defer s.Close()
	scanner := bufio.NewScanner(s.resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxBodyBytes)
	payload := make([]byte, 0, 1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if len(payload) == 0 {
				continue
			}
			event := StreamEvent{}
			if err := json.Unmarshal(payload, &event); err != nil {
				glog.V(2).Infof("stream event decode failed: %v", err)
				payload = payload[:0]
				continue
			}
			payload = payload[:0]
			select {
			case s.events <- event:
			case <-ctx.Done():
				return
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, dataPrefix); ok {
			payload = append(payload, strings.TrimPrefix(rest, " ")...)
		}
	}
	if err := scanner.Err(); err != nil {
		glog.V(2).Infof("attention stream read ended: %v", err)
	}
}

// do performs one board call and decodes the response into target when set.
func (c *httpStoreClient) do(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
	target any,
) error {
	if c.baseURL == "" {
		return errors.New(ctx, "no board configured (board URL is not loopback)")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return errors.Wrapf(ctx, err, "build %s %s request failed", method, path)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		glog.V(2).Infof("board %s %s failed: %v", method, path, err)
		return errors.Wrapf(ctx, err, "%s %s failed", method, path)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.Wrapf(ctx, pkg.ErrItemNotFound, "%s %s returned 404", method, path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.Wrapf(
			ctx,
			errors.New(ctx, "unexpected status"),
			"%s %s returned %d",
			method,
			path,
			resp.StatusCode,
		)
	}
	return decodeResponse(ctx, method, path, resp.Body, target)
}

// decodeResponse reads and decodes a response body into target when set.
func decodeResponse(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
	target any,
) error {
	if target == nil {
		_, _ = io.Copy(io.Discard, body)
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxBodyBytes))
	if err != nil {
		return errors.Wrapf(ctx, err, "read %s %s body failed", method, path)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errors.Wrapf(ctx, err, "decode %s %s body failed", method, path)
	}
	return nil
}

// hookInput is what one hook invocation resolved to.
type hookInput struct {
	// SessionID is the producer whose card this hook answers.
	SessionID string
	// Expect is the "<tool>: <detail>" prefix the card's payload must start
	// with, or "" when no stdin payload carried a tool name.
	Expect string
}

// stdinPayload is the harness event payload the hook reads from stdin.
type stdinPayload struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// parseHookInput reads the harness payload from stdin and the manual overrides
// from argv. ok is false when neither names a session.
func parseHookInput(stdin io.Reader, args []string) (hookInput, bool, string) {
	flags := flag.NewFlagSet("permission-answer-hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionID := flags.String("session-id", "", "manual override: the session whose card to answer")
	itemID := flags.String("item-id", "", "manual override: answer this item id directly")
	// --timeout is parsed here and consumed by a later step, so the surface is
	// stable; the value is not read yet.
	flags.Duration("timeout", HookDeadline, "manual override: the hook deadline")
	if err := flags.Parse(args); err != nil {
		return hookInput{}, false, "parse flags failed: " + err.Error()
	}
	if *sessionID != "" {
		return hookInput{SessionID: *sessionID}, true, ""
	}
	if *itemID == "" && !isTerminal(stdin) {
		return parseStdinPayload(stdin)
	}
	return hookInput{}, false, "no session id on stdin or --session-id"
}

// parseStdinPayload decodes the harness payload and derives the expect prefix.
func parseStdinPayload(stdin io.Reader) (hookInput, bool, string) {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxBodyBytes))
	if err != nil {
		return hookInput{}, false, "read stdin failed: " + err.Error()
	}
	var payload stdinPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return hookInput{}, false, "no session id on stdin or --session-id"
	}
	if payload.SessionID == "" {
		return hookInput{}, false, "no session id on stdin or --session-id"
	}
	return hookInput{
		SessionID: payload.SessionID,
		Expect:    expectPrefix(payload.ToolName, payload.ToolInput),
	}, true, ""
}

// isTerminal reports whether the reader is a character-device terminal. An
// injected reader is never one, so it is always read.
func isTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// expectPrefix returns "<tool>: <detail>", truncated to 200 runes, or "" when
// toolName is empty.
func expectPrefix(toolName string, toolInput json.RawMessage) string {
	if toolName == "" {
		return ""
	}
	return truncateRunes(toolName+": "+toolDetail(toolInput), maxExpectRunes)
}

// toolDetail returns the first present detail field, or compacted JSON.
func toolDetail(toolInput json.RawMessage) string {
	if len(toolInput) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(toolInput, &object); err != nil {
		return compactJSON(toolInput)
	}
	for _, key := range []string{"command", "description", "file_path"} {
		if text, ok := object[key].(string); ok && text != "" {
			return text
		}
	}
	return compactJSON(toolInput)
}

// compactJSON compacts raw JSON and truncates it to 120 runes.
func compactJSON(raw json.RawMessage) string {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		return truncateRunes(string(raw), maxDetailRunes)
	}
	return truncateRunes(buffer.String(), maxDetailRunes)
}

// truncateRunes truncates text to at most limit runes.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// permissionVerdict returns the decision an item carries, or "" and a reason
// naming the condition that failed.
//
// ⚠️ deny is never inferred: an absent or unknown decision yields no decision,
// never pkg.DenyDecision — a default would turn "no verdict recorded" into a
// refusal of the operator's tool call.
func permissionVerdict(item *pkg.Item) (pkg.Decision, string) {
	if item == nil {
		return "", "item is nil: no state"
	}
	if item.State != pkg.AnsweredState {
		return "", "item state is " + item.State.String() + ", not answered"
	}
	if item.Decision == "" || !pkg.AvailableDecisions.Contains(item.Decision) {
		return "", "item decision is " + item.Decision.String() + ", not one of allow/deny"
	}
	if item.ResolvedBy == "" {
		return "", "item resolved_by is empty"
	}
	return item.Decision, ""
}

// selectItem returns the one card this hook invocation answers, or a zero item
// and a reason naming why no single card could be chosen.
//
// notBefore is when this hook invocation started; a card created before
// notBefore minus notBeforeTolerance is from an earlier gate and is refused.
// expect is the "<tool>: <detail>" prefix from the stdin payload, or "" when
// the invocation carried none.
//
// ⚠️ More than one survivor settles nothing: an in-pane approval can leave its
// store card open, and answering THIS gate under that card's subject is the
// failure the ambiguity refusal exists to prevent. The newest card is never
// evidence that it is the right one.
func selectItem(
	items pkg.Items,
	producerID string,
	expect string,
	notBefore time.Time,
) (pkg.Item, string) {
	earliest := notBefore.Add(-notBeforeTolerance)
	candidates := make([]pkg.Item, 0, len(items))
	for _, item := range items {
		if isSelectionCandidate(item, pkg.ProducerID(producerID), expect, earliest) {
			candidates = append(candidates, item)
		}
	}
	switch len(candidates) {
	case 1:
		return candidates[0], ""
	case 0:
		return pkg.Item{}, "no matching permission item found for producer " + producerID
	default:
		return pkg.Item{}, fmt.Sprintf(
			"ambiguous: %d permission items match producer %s",
			len(candidates),
			producerID,
		)
	}
}

// isSelectionCandidate reports whether the item survives all four selection
// guards, applied in order: permission class, producer, the expect prefix and
// the not-before instant.
//
// An empty expect leaves the prefix guard inactive — that is the --session-id
// re-run path, which carries no prefix, and it must not become a refuse-all.
func isSelectionCandidate(
	item pkg.Item,
	producerID pkg.ProducerID,
	expect string,
	earliest time.Time,
) bool {
	if item.AnswerMechanism != pkg.PermissionAnswerMechanism {
		return false
	}
	if item.ProducerID != producerID {
		return false
	}
	if expect != "" && !strings.HasPrefix(string(item.Payload), expect) {
		return false
	}
	return !item.CreatedAt.Time().Before(earliest)
}

// resolveItem waits up to ResolveGrace for the session's card, re-reading the
// list on a one-shot timer. It returns the item, or a reason when the window
// expires with no card.
//
// ⚠️ This is the only place the list is read. Once the subscription is
// established the hook reads the list zero times; a re-resolve after subscribing
// would be the poll under another name.
func resolveItem(
	ctx context.Context,
	client storeClient,
	clock Clock,
	producerID string,
	expect string,
	notBefore time.Time,
) (pkg.Item, string) {
	deadline := notBefore.Add(ResolveGrace)
	// Assigned on every pass before it is read, so it is declared without a
	// value: a seeded one would be dead on the first pass.
	var lastReason string
	for {
		items, err := client.List(ctx)
		if err != nil {
			lastReason = errors.Wrapf(ctx, err, "list attention items failed").Error()
		} else if item, reason := selectItem(items, producerID, expect, notBefore); reason == "" {
			return item, ""
		} else {
			lastReason = reason
		}
		if !clock.Now().Before(deadline) {
			return pkg.Item{}, lastReason
		}
		select {
		case <-clock.Timer(resolveRetryInterval):
		case <-ctx.Done():
			return pkg.Item{}, lastReason
		}
	}
}

// gateState is what the producer's open-item file says.
type gateState struct {
	// Present is true when the file exists and decodes.
	Present bool
	// Ok is true when the file could be read at all; false means "unknown",
	// which fails OPEN — the hook keeps waiting rather than reading an
	// unreadable file as a closed gate.
	Ok bool
}

// readGateState reads <stateDir>/<sessionID>.open.json.
//
// It follows pkg/attnlog/store.go's resolution and its fail-open reading: the
// file is opened through a confined root, so a session id that would escape the
// directory reads nothing rather than another file.
func readGateState(stateDir, sessionID string) gateState {
	if stateDir == "" || sessionID == "" {
		return gateState{}
	}
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return gateState{}
	}
	defer root.Close()
	file, err := root.Open(sessionID + openItemSuffix)
	if err != nil {
		if os.IsNotExist(err) {
			return gateState{Ok: true}
		}
		return gateState{}
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBodyBytes))
	if err != nil {
		return gateState{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return gateState{}
	}
	return gateState{Present: true, Ok: true}
}

// stateDirFrom resolves ATTENTION_STATE_DIR, falling back to
// ~/.claude/state/attention exactly as pkg/attnlog/store.go's stateDir does.
func stateDirFrom(getenv func(string) string) string {
	if dir := getenv(stateDirEnvName); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, defaultStateDirSuffix)
}

// skipBeforeConnect reports whether the hook must exit before opening any
// connection, and the reason to print.
func skipBeforeConnect(getenv func(string) string) (bool, string) {
	if strings.EqualFold(getenv("SUPERVISOR_PERMISSION_POLL"), "off") {
		return true, "SUPERVISOR_PERMISSION_POLL=off"
	}
	if getenv("SUPERVISOR_WORKER_MODE") == "headless" {
		return true, "SUPERVISOR_WORKER_MODE=headless"
	}
	return false, ""
}

// hookOptions is everything one invocation needs, injected so a spec drives
// the whole hook without a process, a socket or a wall clock.
type hookOptions struct {
	Args     []string
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Getenv   func(string) string
	Client   storeClient
	Clock    Clock
	Deadline time.Duration
}

// settleResult is what one settle loop concluded.
type settleResult struct {
	Decision pkg.Decision
	ItemID   pkg.ItemID
	Reason   string
	// Deadline is set when the loop stopped because the hook deadline expired,
	// so the terminal line can say so without inferring it from Reason.
	// ⚠️ Without this field there is no way to tell a deadline from any other
	// reason, and the terminal-line path becomes unassertable — the implementer
	// would have to invent a sentinel string, which drifts.
	Deadline bool
}

// settle holds the subscription and decides from the store. started is when the
// invocation began, and is the FeedGrace anchor.
//
// ⚠️ The deadline is a REAL context.WithTimeout, not opts.Clock. A fake clock
// advancing cannot trip a context deadline, so a spec that needs it sets a short
// real opts.Deadline and waits. It is the one place this package uses real time
// besides the process clock itself.
func settle(ctx context.Context, opts hookOptions, item pkg.Item, started time.Time) settleResult {
	deadlineCtx, cancel := context.WithTimeout(ctx, opts.Deadline)
	defer cancel()

	sub, err := opts.Client.Subscribe(deadlineCtx)
	if err != nil {
		return settleResult{
			ItemID: item.ItemID,
			Reason: errors.Wrapf(deadlineCtx, err, "subscribe failed").Error(),
		}
	}
	defer sub.Close()

	run := &settleRun{
		opts:     opts,
		item:     item,
		started:  started,
		stateDir: stateDirFrom(opts.Getenv),
		events:   sub.Events(),
	}
	if result, done := run.catchUp(deadlineCtx); done {
		return result
	}
	return run.wait(deadlineCtx)
}

// settleRun is one settle loop's mutable state, split out of settle so each step
// stays inside the repo's function-length limits.
type settleRun struct {
	opts       hookOptions
	item       pkg.Item
	started    time.Time
	stateDir   string
	seenOpen   bool
	lastReason string
	events     <-chan StreamEvent
}

// catchUp performs the one read taken immediately after the subscription is
// established and decides from it.
//
// ⚠️ That read IS the decision when the item is already answered: an answer that
// landed between the server's baseline and the subscription's establishment
// emits no event, so this read is the only thing that catches it. Discarding its
// result would leave the baseline gap exactly as open as no read at all.
func (r *settleRun) catchUp(ctx context.Context) (settleResult, bool) {
	read, err := r.opts.Client.Get(ctx, r.item.ItemID)
	if err != nil {
		r.lastReason = errors.Wrapf(ctx, err, "catch-up read of %s failed", r.item.ItemID).
			Error()
		return settleResult{}, false
	}
	return r.verdict(read)
}

// wait is the loop: one select over the events, the state re-check and the
// deadline.
func (r *settleRun) wait(ctx context.Context) settleResult {
	for {
		select {
		case event, open := <-r.events:
			if !open {
				// ⚠️ A closed channel is receive-ready forever: leaving it in the
				// select busy-spins until the deadline. A nil channel blocks
				// forever, so the loop waits on the timer and the deadline instead.
				r.events = nil
				r.lastReason = streamClosedReason
				continue
			}
			if result, done := r.handleEvent(ctx, event); done {
				return result
			}
		case <-r.opts.Clock.Timer(stateCheckInterval):
			if result, done := r.handleTick(); done {
				return result
			}
		case <-ctx.Done():
			return settleResult{
				ItemID:   r.item.ItemID,
				Reason:   r.lastReason,
				Deadline: true,
			}
		}
	}
}

// handleEvent reads one matching item by id and decides from it. An event for
// another item is ignored without a read.
func (r *settleRun) handleEvent(ctx context.Context, event StreamEvent) (settleResult, bool) {
	if event.ItemID != r.item.ItemID.String() {
		return settleResult{}, false
	}
	read, err := r.opts.Client.Get(ctx, r.item.ItemID)
	if err != nil {
		r.lastReason = errors.Wrapf(ctx, err, "read item %s failed", r.item.ItemID).Error()
		return settleResult{}, false
	}
	return r.verdict(read)
}

// verdict runs the permission verdict and decides, or records the reason and
// keeps waiting.
func (r *settleRun) verdict(read *pkg.Item) (settleResult, bool) {
	decision, reason := permissionVerdict(read)
	if decision == "" {
		r.lastReason = reason
		return settleResult{}, false
	}
	return r.decide(decision), true
}

// decide suppresses a store decision when the gate was seen open locally and has
// since gone: an in-pane answer wins even when the store event lands just behind
// it.
func (r *settleRun) decide(decision pkg.Decision) settleResult {
	state := readGateState(r.stateDir, r.item.ProducerID.String())
	if state.Ok && !state.Present && r.seenOpen {
		return settleResult{ItemID: r.item.ItemID, Reason: localCloseReason}
	}
	return settleResult{Decision: decision, ItemID: r.item.ItemID}
}

// handleTick re-reads the producer's open-item file and reports a local close or
// an untracked gate.
func (r *settleRun) handleTick() (settleResult, bool) {
	state := readGateState(r.stateDir, r.item.ProducerID.String())
	if !state.Ok {
		// Fail open: an unreadable file is not a close.
		return settleResult{}, false
	}
	if state.Present {
		r.seenOpen = true
		return settleResult{}, false
	}
	if r.seenOpen {
		r.lastReason = localCloseReason
		return settleResult{ItemID: r.item.ItemID, Reason: localCloseReason}, true
	}
	if !r.opts.Clock.Now().Before(r.started.Add(FeedGrace)) {
		r.lastReason = untrackedReason
		return settleResult{ItemID: r.item.ItemID, Reason: untrackedReason}, true
	}
	return settleResult{}, false
}

// deadlineLine renders the terminal line a reached deadline prints.
//
// ⚠️ The deadline prints in SECONDS: time.Duration.String() renders 540s as
// `9m0s`, and the required line is `deadline after 540s`.
func deadlineLine(deadline time.Duration, lastReason string) string {
	return fmt.Sprintf(
		"permission-answer-hook: deadline after %.0fs — last: %s",
		deadline.Seconds(),
		lastReason,
	)
}

// permissionDecision is the decision member of the harness's response.
type permissionDecision struct {
	Behavior string `json:"behavior"`
}

// permissionHookOutput is the harness's response envelope. The field order is
// the wire order, so the marshalled bytes are stable.
type permissionHookOutput struct {
	HookSpecificOutput struct {
		HookEventName string             `json:"hookEventName"`
		Decision      permissionDecision `json:"decision"`
	} `json:"hookSpecificOutput"`
}

// hookEventName is the literal event the harness matches the response to.
const hookEventName = "PermissionRequest"

// writeDecision writes the harness response for a settled verdict, and writes
// nothing at all for a verdict the harness would not understand.
//
// ⚠️ The bytes are the contract: no extra field, no reordering, no
// pretty-printing and no omitted newline. An unknown verdict prints no JSON at
// all — a half-understood verdict is worse than no answer, because the harness
// would read it as a decision it never received.
func writeDecision(w io.Writer, decision pkg.Decision) error {
	if decision != pkg.AllowDecision && decision != pkg.DenyDecision {
		return nil
	}
	output := permissionHookOutput{}
	output.HookSpecificOutput.HookEventName = hookEventName
	output.HookSpecificOutput.Decision = permissionDecision{Behavior: decision.String()}
	encoded, err := json.Marshal(output)
	if err != nil {
		return err
	}
	_, err = w.Write(append(encoded, '\n'))
	return err
}

// recordDeliveryAttempt posts the delivery record for a settled answer.
//
// ⚠️ It is fail-open in the same sense as the verdict path: a rejected POST is
// logged with the item id and the decision still stands. A missing record is a
// measurable defect, not a benign one — the tab path's received_at reads it.
func recordDeliveryAttempt(
	ctx context.Context,
	client storeClient,
	itemID pkg.ItemID,
) error {
	if err := client.RecordAttempt(ctx, itemID, attemptCarrier, pkg.DeliveredOutcome); err != nil {
		return errors.Wrapf(ctx, err, "record delivery attempt for %s failed", itemID)
	}
	return nil
}

// emitDecision writes the settled verdict to stdout and then records the
// delivery attempt.
//
// ⚠️ The ordering is load-bearing. The answer goes out first and the record is
// posted after it, so a slow or failing store write can never delay, gate or
// alter the verdict — and no record is posted on a path that wrote no answer.
func emitDecision(ctx context.Context, opts hookOptions, result settleResult) {
	if err := writeDecision(opts.Stdout, result.Decision); err != nil {
		glog.Warningf(
			"permission-answer-hook: write decision for %s failed: %v",
			result.ItemID,
			err,
		)
		return
	}
	if err := recordDeliveryAttempt(ctx, opts.Client, result.ItemID); err != nil {
		glog.Warningf(
			"permission-answer-hook: record delivery attempt for %s failed: %v",
			result.ItemID,
			err,
		)
	}
}

// run executes one hook invocation.
//
//nolint:unparam // the error return is the fail-open contract later steps use.
func run(ctx context.Context, opts hookOptions) error {
	if skip, reason := skipBeforeConnect(opts.Getenv); skip {
		fmt.Fprintln(opts.Stderr, "permission-answer-hook: "+reason)
		return nil
	}
	input, ok, reason := parseHookInput(opts.Stdin, opts.Args)
	if !ok || input.SessionID == "" {
		fmt.Fprintln(opts.Stderr, "permission-answer-hook: "+reason)
		return nil
	}
	started := opts.Clock.Now()
	item, resolveReason := resolveItem(
		ctx,
		opts.Client,
		opts.Clock,
		input.SessionID,
		input.Expect,
		started,
	)
	if resolveReason != "" {
		fmt.Fprintln(opts.Stderr, "permission-answer-hook: "+resolveReason)
		return nil
	}
	result := settle(ctx, opts, item, started)
	if result.Decision != "" {
		emitDecision(ctx, opts, result)
		return nil
	}
	writeSettleReason(opts, result)
	return nil
}

// writeSettleReason prints why no decision was produced: the terminal deadline
// line when the deadline was reached, the plain reason otherwise.
func writeSettleReason(opts hookOptions, result settleResult) {
	if result.Deadline {
		fmt.Fprintln(opts.Stderr, deadlineLine(opts.Deadline, result.Reason))
		return
	}
	fmt.Fprintln(opts.Stderr, "permission-answer-hook: "+result.Reason)
}

// main runs the hook and always exits 0: a non-zero exit would block the tool
// call the gate is holding.
func main() {
	baseURL := os.Getenv(boardURLEnv)
	if baseURL == "" {
		baseURL = defaultBoardURL
	}
	opts := hookOptions{
		Args:     os.Args[1:],
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Getenv:   os.Getenv,
		Client:   newHTTPStoreClient(baseURL),
		Clock:    realClock{},
		Deadline: HookDeadline,
	}
	if err := run(context.Background(), opts); err != nil {
		fmt.Fprintf(os.Stderr, "permission-answer-hook: %v\n", err)
	}
}

// Compile-time guards: the concrete types must satisfy their interfaces.
var (
	_ Clock       = realClock{}
	_ storeClient = (*httpStoreClient)(nil)
)
