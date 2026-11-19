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
	"strings"
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
	//nolint:unused // consumed by the settle loop in a later step.
	resolveRetryInterval = time.Second
	// stateCheckInterval paces the producer state-file re-read.
	//nolint:unused // consumed by the settle loop in a later step.
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
}

// httpStoreClient is the loopback HTTP implementation of storeClient.
type httpStoreClient struct {
	baseURL string
	client  *http.Client
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
	// One list read, before anything else touches the store: the bounded retry
	// and the subscription belong to a later step.
	notBefore := opts.Clock.Now()
	items, listErr := opts.Client.List(ctx)
	if listErr != nil {
		// Fail-open: a non-zero exit or a blocked gate is worse than no answer.
		wrapped := errors.Wrapf(ctx, listErr, "list attention items failed")
		fmt.Fprintln(opts.Stderr, "permission-answer-hook: "+wrapped.Error())
		return nil
	}
	item, selectReason := selectItem(items, input.SessionID, input.Expect, notBefore)
	if selectReason != "" {
		fmt.Fprintln(opts.Stderr, "permission-answer-hook: "+selectReason)
		return nil
	}
	// The decision and its stdout contract land in a later step; the selected
	// item is carried no further than this variable for now.
	glog.V(2).Infof("permission-answer-hook selected item %s", item.ItemID)
	return nil
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
