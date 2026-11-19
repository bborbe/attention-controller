// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command approve-run executes a shell command the operator approved on the
// attention board. It is the Go replacement for ~/.claude/scripts/approve-run.py.
//
// THE CONTRACT: WHAT THE OPERATOR SEES IS WHAT RUNS
// The command is written into the card payload, the operator approves that
// payload, and the run step parses the command back OUT OF THE CARD. Nothing is
// executed from an argument the caller supplies at run time. A card that says
// `make buca` cannot run something else because the two strings are the same
// object: the one the operator read. The run step also re-checks that the parsed
// command matches the one this invocation posted, and refuses on any mismatch.
//
// WHY THIS IS A CLIENT, NOT A DAEMON FEATURE
// Execution stays a child of the agent's own Bash call, so the output lands in
// the transcript and cwd/env are the session's. A launchd-run process would have
// a launchd env, write to a service log, and mean a daemon running arbitrary
// shell. The Python's `refresh_poll_window` and its `posted_at` / `wait` state
// fields existed only for the watcher's `approve_run_polling`, retired with
// attention-watcher on 2026-11-16 (bborbe/claude@ef1cf52); they are not ported.
//
// FAIL CLOSED, ALWAYS
//   - Only an answer whose label is `run it` releases execution.
//   - DECLINED, HIDDEN, an unattributed move, an unreachable store and a
//     timeout all run nothing.
//   - A card is single-use: `used_at` is written before the command starts, so a
//     replayed approval cannot re-run anything.
//   - A declined command is not re-offered for the decline cooldown (2 h).
//
// OUTPUT TOKENS (read the token; an approved command's exit status is passed
// through unchanged, so `EXIT: 3` is the command's own code)
//
//	CARD: <item_id>        the card posted, or resumed
//	OPEN: ...              still waiting (printed when the state changes)
//	APPROVED: <item_id>    the operator said run it; the command follows
//	PENDING: <item_id>     timed out unanswered -- the card is still on the board
//	DECLINED: <item_id>    the operator said no; nothing ran
//	HIDDEN: <item_id>      the operator filed it elsewhere; nothing ran
//	FAILED: <reason>       store unreachable, or the card is unreadable
//	REFUSED: <reason>      bad arguments, or the card and the request disagree
//
// Exit codes: 0 approved and the command succeeded, the command's own rc when it
// failed, 4 pending, 3 declined, 5 hidden, 1 store failure, 2 refused.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	berrors "github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg"
)

const (
	// defaultWait bounds one foreground poll so the call returns cleanly before
	// the harness's 600 s Bash cap, leaving the card open on the board.
	defaultWait = 300 * time.Second
	// maxWait caps --wait at 540 s.
	maxWait = 540 * time.Second
	// defaultInterval paces the poll.
	defaultInterval = 3 * time.Second
	// minInterval floors --interval.
	minInterval = 500 * time.Millisecond
	// httpTimeout bounds one store request.
	httpTimeout = 5 * time.Second
	// cardTTL is how long a posted card lives on the board.
	cardTTL = 2 * time.Hour
	// declineCooldown is how long a declined command is not re-offered.
	declineCooldown = 2 * time.Hour
	// approveLabel is the only answer label that releases execution.
	approveLabel = "run it"
	// declineLabel is the label that declines.
	declineLabel = "no"
	// storeItemIDLen is the hex length of a store item id (128-bit, hex).
	storeItemIDLen = 32
)

// Exit codes, read by callers and by the specs.
const (
	exitOK           = 0
	exitStoreFailure = 1
	exitRefused      = 2
	exitDeclined     = 3
	exitPending      = 4
	exitHidden       = 5
)

var (
	// fenceRe captures the command from a fenced code block in the card payload.
	fenceRe = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\n(.*?)\n```")
	// cwdLineRe captures the `cwd: `...`` line of the payload.
	cwdLineRe = regexp.MustCompile("(?m)^cwd: `([^`]+)`$")
	// secretLeakRe mirrors the credential rules deny-k8s-secret-read.sh refuses,
	// narrowly enough that a command merely naming a secret still passes.
	secretLeakRe = regexp.MustCompile(
		`(?i)kubectl[a-z]*\b[^|;&]*\b(secret|configmap)\b[^|;&]*` +
			`(-o\s*(yaml|json)\b|--raw\b|\.data\b|stringData\b|\.spec\.env\b)` +
			`|kubectl[a-z]*\b[^|;&]*\bedit\b[^|;&]*\b(secret|configmap)\b`,
	)
)

// Clock is the poll's time source. It is injected so a spec advances time
// instead of blocking.
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

// parsedPayload is the command and cwd as they appear on the card.
type parsedPayload struct {
	Cmd string
	Cwd string
}

// storeClient is the client's view of the attention store. A spec injects a
// recording fake, which is how "no store traffic" is proven rather than
// asserted.
type storeClient interface {
	Post(ctx context.Context, request pkg.PushRequest) (pkg.Item, error)
	Get(ctx context.Context, itemID pkg.ItemID) (*pkg.Item, error)
}

// httpStoreClient is the loopback HTTP implementation of storeClient.
type httpStoreClient struct {
	baseURL string
	token   string
	client  *http.Client
}

// newHTTPStoreClient creates the store client.
func newHTTPStoreClient(baseURL, token string) storeClient {
	return &httpStoreClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		client:  &http.Client{Timeout: httpTimeout},
	}
}

// headers returns the request headers, adding the bearer token when set.
func (c *httpStoreClient) headers() map[string]string {
	headers := map[string]string{"Accept": "application/json"}
	if c.token != "" {
		headers["Authorization"] = "Bearer " + c.token
	}
	return headers
}

// Post pushes one card and returns the stored item.
func (c *httpStoreClient) Post(ctx context.Context, request pkg.PushRequest) (pkg.Item, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return pkg.Item{}, berrors.Wrap(ctx, err, "encode push request failed")
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/api/1.0/attention",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return pkg.Item{}, berrors.Wrap(ctx, err, "build push request failed")
	}
	for key, value := range c.headers() {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		glog.V(2).Infof("attention store POST failed: %v", err)
		return pkg.Item{}, berrors.Wrap(ctx, err, "push to attention store failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pkg.Item{}, berrors.Wrapf(
			ctx,
			errors.New("unexpected status"),
			"push returned %d",
			resp.StatusCode,
		)
	}
	var item pkg.Item
	if err := decode(ctx, resp.Body, &item); err != nil {
		return pkg.Item{}, berrors.Wrap(ctx, err, "decode pushed item failed")
	}
	return item, nil
}

// Get reads one item by id. A 404 returns pkg.ErrItemNotFound unwrapped, so a
// caller's errors.Is is exact.
func (c *httpStoreClient) Get(ctx context.Context, itemID pkg.ItemID) (*pkg.Item, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/api/1.0/attention/"+url.PathEscape(itemID.String()),
		nil,
	)
	if err != nil {
		return nil, berrors.Wrap(ctx, err, "build get request failed")
	}
	for key, value := range c.headers() {
		req.Header.Set(key, value)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		glog.V(2).Infof("attention store GET failed: %v", err)
		return nil, berrors.Wrap(ctx, err, "get from attention store failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, pkg.ErrItemNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, berrors.Wrapf(
			ctx,
			errors.New("unexpected status"),
			"get returned %d",
			resp.StatusCode,
		)
	}
	var item pkg.Item
	if err := decode(ctx, resp.Body, &item); err != nil {
		return nil, berrors.Wrap(ctx, err, "decode item failed")
	}
	return &item, nil
}

// decode reads a bounded response body into target.
func decode(ctx context.Context, body io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return berrors.Wrap(ctx, err, "read body failed")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return berrors.Wrap(ctx, err, "unmarshal body failed")
	}
	return nil
}

// commandRunner executes the approved command as a child process.
type commandRunner interface {
	// Run executes cmd in cwd and returns the process's exit code.
	Run(ctx context.Context, cmd, cwd string) (int, error)
}

// execRunner runs the command through /bin/sh -c, inheriting the session's
// stdout and stderr so output lands in the transcript.
type execRunner struct {
	stdout io.Writer
	stderr io.Writer
}

// Run executes cmd via the shell.
//
// The command is the operator-approved card payload, parsed back out of the
// card, so exec-from-a-variable is the whole point of this binary.
func (r execRunner) Run(ctx context.Context, cmd, cwd string) (int, error) {
	fmt.Fprintf(r.stdout, "$ %s\n", cmd)
	// #nosec G204 -- cmd is the operator-approved payload parsed from the card.
	process := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	process.Dir = cwd
	process.Stdout = r.stdout
	process.Stderr = r.stderr
	if err := process.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return exitStoreFailure, berrors.Wrap(ctx, err, "run command failed")
	}
	return exitOK, nil
}

// options is everything one invocation needs, injected so a spec drives the
// whole binary without a process, a socket or a wall clock.
type options struct {
	Cmd          string
	Reason       string
	Cwd          string
	Wait         time.Duration
	Interval     time.Duration
	Resume       string
	ExpectOption string
	SessionID    string
	StateDir     string
	Stdout       io.Writer
	Stderr       io.Writer
	Store        storeClient
	Runner       commandRunner
	Clock        Clock
}

// state is the per-key record kept between invocations.
type state struct {
	ItemID     string `json:"item_id,omitempty"`
	Cmd        string `json:"cmd,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	UsedAt     string `json:"used_at,omitempty"`
	DeclinedAt string `json:"declined_at,omitempty"`
}

// stateKey is stable across invocations, so a retry resumes the same card.
func stateKey(cmd, cwd string) string {
	sum := sha256.Sum256([]byte(cwd + "\x00" + cmd))
	return hex.EncodeToString(sum[:])[:40]
}

// statePath is the on-disk record path for one key.
func statePath(dir, key string) string {
	return filepath.Join(dir, key+".json")
}

// loadState reads a key's record, or an empty one when absent or unreadable.
func loadState(dir, key string) state {
	raw, err := os.ReadFile(statePath(dir, key))
	if err != nil {
		return state{}
	}
	var record state
	if err := json.Unmarshal(raw, &record); err != nil {
		return state{}
	}
	return record
}

// saveState writes a key's record atomically.
func saveState(ctx context.Context, dir, key string, record state) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return berrors.Wrap(ctx, err, "create state dir failed")
	}
	raw, err := json.MarshalIndent(record, "", " ")
	if err != nil {
		return berrors.Wrap(ctx, err, "encode state failed")
	}
	tmp := statePath(dir, key) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return berrors.Wrap(ctx, err, "write state temp failed")
	}
	if err := os.Rename(tmp, statePath(dir, key)); err != nil {
		return berrors.Wrap(ctx, err, "rename state failed")
	}
	return nil
}

// markUsed records that the approved command is starting, BEFORE it runs, so a
// long-running command is never mistaken for an unconsumed card.
func markUsed(ctx context.Context, dir, key, itemID string) {
	record := loadState(dir, key)
	record.ItemID = itemID
	record.UsedAt = nowISO(time.Now())
	if err := saveState(ctx, dir, key, record); err != nil {
		glog.Warningf("approve-run: mark used for %s failed: %v", itemID, err)
	}
}

// stateKeyForItem scans the state dir for the key whose item_id matches.
func stateKeyForItem(dir, itemID string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		key := strings.TrimSuffix(name, ".json")
		if loadState(dir, key).ItemID == itemID {
			return key
		}
	}
	return ""
}

// nowISO renders a time as an RFC3339 UTC second string, matching the Python.
func nowISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// parseISO parses an RFC3339 string, returning ok=false when it is absent or
// malformed.
func parseISO(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// renderPayload builds the operator-facing card payload.
func renderPayload(cmd, cwd, reason string) string {
	head := ""
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		head = trimmed + "\n\n"
	}
	return head + "```\n" + cmd + "\n```\n\ncwd: `" + cwd + "`"
}

// parsePayload returns the command and cwd as they appear on the card.
func parsePayload(payload string) (parsedPayload, bool) {
	match := fenceRe.FindStringSubmatch(payload)
	if match == nil {
		return parsedPayload{}, false
	}
	cmd := strings.TrimSpace(match[1])
	if cmd == "" {
		return parsedPayload{}, false
	}
	cwd := ""
	if found := cwdLineRe.FindStringSubmatch(payload); found != nil {
		cwd = strings.TrimSpace(found[1])
	}
	return parsedPayload{Cmd: cmd, Cwd: cwd}, true
}

// buildPushRequest constructs the card declaration from the store's real types.
func buildPushRequest(cmd, cwd, reason, key, producerID string, clock Clock) pkg.PushRequest {
	expires := libtime.DateTime(clock.Now().Add(cardTTL))
	return pkg.PushRequest{
		ProducerID:      pkg.ProducerID(producerID),
		ProducerKind:    pkg.SessionProducerKind,
		LivenessRef:     pkg.LivenessRef("owner:" + producerID),
		DedupKey:        pkg.DedupKey("command-approval:" + key),
		InterruptClass:  pkg.InterruptClass("approve"),
		Risk:            pkg.ElevatedRisk,
		Payload:         pkg.Payload(renderPayload(cmd, cwd, reason)),
		AnswerMechanism: pkg.MessageAnswerMechanism,
		Options: pkg.AnswerOptions{
			{Label: approveLabel, Recommended: true},
			{Label: declineLabel},
		},
		ExpiresAt: &expires,
	}
}

// isStoreItemID reports whether candidate has the shape the store mints. Shape
// only: a well-formed unknown id still reaches the store to be told it is gone.
func isStoreItemID(candidate string) bool {
	if len(candidate) != storeItemIDLen {
		return false
	}
	for _, char := range candidate {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// answerText is the operator's chosen label, or "" when the item carries none.
func answerText(item *pkg.Item) string {
	if item == nil || item.Answer == nil {
		return ""
	}
	if len(item.Answer.Values) > 0 {
		return strings.Join(item.Answer.Values, ", ")
	}
	return item.Answer.Value
}

// verdict classifies an item as open / approved / declined / hidden /
// unanswered, plus the operator's text.
func verdict(item *pkg.Item) (string, string) {
	text := strings.TrimSpace(answerText(item))
	lowered := strings.ToLower(text)
	if strings.HasPrefix(lowered, approveLabel) {
		return "approved", text
	}
	if item.Answer != nil {
		switch item.Answer.Kind {
		case pkg.HiddenAnswerKind:
			return "hidden", text
		case pkg.SkipAnswerKind:
			return "declined", text
		}
	}
	if strings.HasPrefix(lowered, declineLabel) || lowered == "skip" {
		return "declined", text
	}
	if item.State == pkg.OpenState {
		return "open", text
	}
	// A reap or an acknowledgement moved the record without the operator
	// speaking. Not a release.
	return "unanswered", text
}

// attribution reports (ok, why-not): was this answered by a client that is not
// the asker? Three guards: a real client answered, that client is not automated,
// and the answer is not self-approval.
func attribution(item *pkg.Item, sessionID string) (bool, string) {
	if item.AnsweredClient == nil {
		return false, "the record carries no answered_client, so the answering client is unknown — the gate is not released"
	}
	if item.AnsweredClient.Automation != nil && *item.AnsweredClient.Automation {
		return false, "the answering client reported automation: true, so a script drove this answer — the gate is not released"
	}
	answerers := map[string]struct{}{}
	for _, value := range []string{item.AnsweredBy, item.ResolvedBy} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			answerers[trimmed] = struct{}{}
		}
	}
	askers := map[string]struct{}{}
	for _, value := range []string{item.ProducerID.String(), strings.TrimSpace(sessionID)} {
		if value != "" {
			askers[value] = struct{}{}
		}
	}
	for answerer := range answerers {
		if _, self := askers[answerer]; self {
			return false, "the card was answered by the session that posted it — self-approval is not an approval"
		}
	}
	return true, ""
}

// waitAndRun polls until answered, then runs the command the CARD carries.
func waitAndRun(
	ctx context.Context,
	opts options,
	itemID string,
	expect string,
	expectSet bool,
	wait, interval time.Duration,
	sessionID string,
	onApprove func(),
) int {
	deadline := opts.Clock.Now().Add(wait)
	last := ""
	for {
		item, err := opts.Store.Get(ctx, pkg.ItemID(itemID))
		if err != nil {
			if errors.Is(err, pkg.ErrItemNotFound) {
				fmt.Fprintf(opts.Stdout, "FAILED: no such item %s; nothing ran\n", itemID)
				return exitStoreFailure
			}
			fmt.Fprintf(opts.Stdout, "FAILED: attention store unreachable (%v); nothing ran\n", err)
			return exitStoreFailure
		}

		outcome, text := verdict(item)
		switch outcome {
		case "approved":
			return handleApproved(
				ctx,
				opts,
				item,
				itemID,
				text,
				expect,
				expectSet,
				sessionID,
				onApprove,
			)
		case "declined":
			fmt.Fprintf(opts.Stdout, "DECLINED: %s -- nothing ran\n", itemID)
			return exitDeclined
		case "hidden":
			fmt.Fprintf(opts.Stdout, "HIDDEN: %s -- nothing ran\n", itemID)
			return exitHidden
		}

		if outcome != last {
			fmt.Fprintf(opts.Stdout, "OPEN: %s -- waiting for the operator\n", itemID)
			last = outcome
		}
		if !opts.Clock.Now().Before(deadline) {
			fmt.Fprintf(
				opts.Stdout,
				"PENDING: %s -- unanswered after %gs; the card is still on the board and re-running this exact command resumes it\n",
				itemID,
				wait.Seconds(),
			)
			return exitPending
		}
		select {
		case <-opts.Clock.Timer(interval):
		case <-ctx.Done():
			return exitStoreFailure
		}
	}
}

// handleApproved validates an approved card and runs the command it carries.
//
// ⚠️ The command is parsed back OUT OF THE CARD, never from the invocation's own
// argument, and a parsed command that differs from the one this invocation
// posted is refused. That is the whole contract: what the operator approved is
// what runs.
func handleApproved(
	ctx context.Context,
	opts options,
	item *pkg.Item,
	itemID, text, expect string,
	expectSet bool,
	sessionID string,
	onApprove func(),
) int {
	ok, why := attribution(item, sessionID)
	if !ok {
		fmt.Fprintf(opts.Stdout, "REFUSED: %s; nothing ran\n", why)
		return exitRefused
	}
	parsed, parsedOK := parsePayload(string(item.Payload))
	if !parsedOK {
		fmt.Fprintf(
			opts.Stdout,
			"REFUSED: card %s carries no readable command; nothing ran\n",
			itemID,
		)
		return exitRefused
	}
	if expectSet && parsed.Cmd != expect {
		fmt.Fprintln(
			opts.Stdout,
			"REFUSED: the card's command is not the command this invocation posted; nothing ran",
		)
		return exitRefused
	}
	if parsed.Cwd == "" || !isDir(parsed.Cwd) {
		fmt.Fprintf(
			opts.Stdout,
			"REFUSED: card cwd is not a directory: %q; nothing ran\n",
			parsed.Cwd,
		)
		return exitRefused
	}
	fmt.Fprintf(opts.Stdout, "APPROVED: %s -- %s\n", itemID, text)
	if onApprove != nil {
		onApprove()
	}
	rc, runErr := opts.Runner.Run(ctx, parsed.Cmd, parsed.Cwd)
	if runErr != nil {
		fmt.Fprintf(opts.Stdout, "FAILED: %v\n", runErr)
		return exitStoreFailure
	}
	fmt.Fprintf(opts.Stdout, "EXIT: %d\n", rc)
	return rc
}

// isDir reports whether path is an existing directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// clamp bounds value into [low, high].
func clamp(value, low, high time.Duration) time.Duration {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// run executes one invocation and returns its exit code.
func run(ctx context.Context, opts options) int {
	if opts.ExpectOption != "" && opts.Resume == "" {
		fmt.Fprintln(opts.Stdout, "REFUSED: --expect-option requires --resume")
		return exitRefused
	}
	wait := clamp(opts.Wait, 0, maxWait)
	interval := opts.Interval
	if interval < minInterval {
		interval = minInterval
	}

	if opts.Resume != "" {
		return runResume(ctx, opts, wait, interval)
	}

	cmd := strings.TrimSpace(opts.Cmd)
	if cmd == "" || strings.ContainsRune(cmd, 0) {
		fmt.Fprintln(opts.Stdout, "REFUSED: --cmd must be a non-empty shell command")
		return exitRefused
	}
	if secretLeakRe.MatchString(cmd) {
		fmt.Fprintln(
			opts.Stdout,
			"REFUSED: this command prints Secret/ConfigMap values. That is not an approval gate — the output would land in the transcript — so the helper will not carry it. Read one non-secret field with -o jsonpath, or compare hashes.",
		)
		return exitRefused
	}
	cwd, err := filepath.Abs(opts.Cwd)
	if err != nil || !isDir(cwd) {
		fmt.Fprintf(opts.Stdout, "REFUSED: --cwd is not a directory: %s\n", opts.Cwd)
		return exitRefused
	}

	return runCommand(ctx, opts, cmd, cwd, wait, interval)
}

// runCommand posts the card (or resumes one), polls it, and runs the approved
// command. It is split out of run so each function stays within the repo's
// statement limit.
func runCommand(
	ctx context.Context,
	opts options,
	cmd, cwd string,
	wait, interval time.Duration,
) int {
	key := stateKey(cmd, cwd)
	record := loadState(opts.StateDir, key)

	if declinedAt, ok := withinDeclineCooldown(record); ok {
		fmt.Fprintf(
			opts.Stdout,
			"DECLINED: %s -- the operator declined this exact command at %s; not re-asking inside %dh\n",
			firstOr(record.ItemID, "?"),
			declinedAt,
			int(declineCooldown.Hours()),
		)
		return exitDeclined
	}

	if record.UsedAt == "" && record.ItemID != "" {
		itemID := record.ItemID
		fmt.Fprintf(opts.Stdout, "CARD: %s (resumed)\n", itemID)
		return waitAndRun(ctx, opts, itemID, cmd, true, wait, interval, opts.SessionID, func() {
			markUsed(ctx, opts.StateDir, key, itemID)
		})
	}

	producerID := opts.SessionID
	if producerID == "" {
		fmt.Fprintln(
			opts.Stdout,
			"REFUSED: CLAUDE_CODE_SESSION_ID is unset, so the card could never be polled back by this session",
		)
		return exitRefused
	}

	item, err := opts.Store.Post(
		ctx,
		buildPushRequest(cmd, cwd, opts.Reason, key, producerID, opts.Clock),
	)
	if err != nil {
		fmt.Fprintf(opts.Stdout, "FAILED: attention store unreachable (%v); nothing ran\n", err)
		return exitStoreFailure
	}
	itemID := item.ItemID.String()
	if itemID == "" {
		fmt.Fprintf(opts.Stdout, "FAILED: store returned no item id: %+v; nothing ran\n", item)
		return exitStoreFailure
	}
	if err := saveState(ctx, opts.StateDir, key, state{ItemID: itemID, Cmd: cmd, Cwd: cwd}); err != nil {
		glog.Warningf("approve-run: save state for %s failed: %v", itemID, err)
	}
	fmt.Fprintf(opts.Stdout, "CARD: %s\n", itemID)

	rc := waitAndRun(ctx, opts, itemID, cmd, true, wait, interval, opts.SessionID, func() {
		markUsed(ctx, opts.StateDir, key, itemID)
	})
	if rc == exitDeclined {
		recordDecline(ctx, opts.StateDir, key, itemID)
	}
	return rc
}

// withinDeclineCooldown reports whether a key's last decline is still inside the
// cooldown, and when it happened.
func withinDeclineCooldown(record state) (string, bool) {
	declinedAt, ok := parseISO(record.DeclinedAt)
	if !ok || time.Since(declinedAt) >= declineCooldown {
		return "", false
	}
	return record.DeclinedAt, true
}

// recordDecline records that the operator declined this exact command.
func recordDecline(ctx context.Context, dir, key, itemID string) {
	updated := loadState(dir, key)
	updated.ItemID = itemID
	updated.DeclinedAt = nowISO(time.Now())
	if err := saveState(ctx, dir, key, updated); err != nil {
		glog.Warningf("approve-run: record decline for %s failed: %v", itemID, err)
	}
}

// runResume waits on a card already posted, validating any citation before
// anything runs.
func runResume(ctx context.Context, opts options, wait, interval time.Duration) int {
	itemID := opts.Resume
	if !isStoreItemID(itemID) {
		fmt.Fprintf(
			opts.Stdout,
			"REFUSED: %q is not a store item id — one is %d hex characters, and this is %d.\n",
			itemID,
			storeItemIDLen,
			len(itemID),
		)
		return exitRefused
	}
	cited, err := opts.Store.Get(ctx, pkg.ItemID(itemID))
	if err != nil {
		if errors.Is(err, pkg.ErrItemNotFound) {
			fmt.Fprintf(
				opts.Stdout,
				"REFUSED: citation %s resolves to no store item; nothing ran\n",
				itemID,
			)
			return exitRefused
		}
		fmt.Fprintf(opts.Stdout, "FAILED: store returned an error for %s; nothing ran\n", itemID)
		return exitStoreFailure
	}
	if opts.ExpectOption != "" {
		outcome, text := verdict(cited)
		if outcome != "approved" && outcome != "declined" {
			fmt.Fprintf(
				opts.Stdout,
				"REFUSED: citation %s is %s, not ANSWERED; nothing ran\n",
				itemID,
				outcome,
			)
			return exitRefused
		}
		if text != opts.ExpectOption {
			fmt.Fprintf(
				opts.Stdout,
				"REFUSED: citation %s answered %q, expected %q; nothing ran\n",
				itemID,
				text,
				opts.ExpectOption,
			)
			return exitRefused
		}
	}
	key := stateKeyForItem(opts.StateDir, itemID)
	var onApprove func()
	if key != "" {
		onApprove = func() { markUsed(ctx, opts.StateDir, key, itemID) }
	}
	return waitAndRun(ctx, opts, itemID, "", false, wait, interval, opts.SessionID, onApprove)
}

// firstOr returns value, or fallback when value is empty.
func firstOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// storeURL resolves the store base URL, honouring the Python's env names first.
func storeURL() string {
	for _, name := range []string{"ATTENTION_STORE_URL", "ATTENTION_BOARD_URL"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return "http://localhost:18080"
}

// stateDir resolves the state directory, defaulting under ~/.claude/state.
func stateDir() string {
	if value := strings.TrimSpace(os.Getenv("APPROVE_RUN_STATE_DIR")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".command-approval"
	}
	return filepath.Join(home, ".claude", "state", "command-approval")
}

// main parses flags and runs one invocation.
func main() {
	flags := flag.NewFlagSet("approve-run", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	cmd := flags.String("cmd", "", "the exact shell command to run")
	reason := flags.String("reason", "", "why it is gated; shown above the command")
	cwd := flags.String("cwd", "", "working directory (default: current)")
	wait := flags.Duration("wait", defaultWait, "seconds to wait for the answer")
	interval := flags.Duration("interval", defaultInterval, "poll interval")
	resume := flags.String("resume", "", "wait on a card already posted")
	expect := flags.String(
		"expect-option",
		"",
		"the option the cited card must already have been answered with (--resume only)",
	)
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(exitRefused)
	}
	workingDir := *cwd
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}
	opts := options{
		Cmd:          *cmd,
		Reason:       *reason,
		Cwd:          workingDir,
		Wait:         *wait,
		Interval:     *interval,
		Resume:       *resume,
		ExpectOption: *expect,
		SessionID:    strings.TrimSpace(os.Getenv("CLAUDE_CODE_SESSION_ID")),
		StateDir:     stateDir(),
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		Store:        newHTTPStoreClient(storeURL(), os.Getenv("ATTENTION_STORE_TOKEN")),
		Runner:       execRunner{stdout: os.Stdout, stderr: os.Stderr},
		Clock:        realClock{},
	}
	os.Exit(run(context.Background(), opts))
}

// Compile-time guards: the concrete types must satisfy their interfaces.
var (
	_ Clock       = realClock{}
	_ storeClient = (*httpStoreClient)(nil)
)
