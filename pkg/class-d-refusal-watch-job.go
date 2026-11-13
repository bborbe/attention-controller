// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

const (
	// classDRefusalWatchJobName is the `job=` value on every line this job logs.
	// A probe binds the job's effect to a line by grepping it, so it is a stable
	// identifier rather than a label that happens to read well.
	classDRefusalWatchJobName = "class-d-refusal-watch"

	// ClassDRefusalWatchDefaultInterval is the cadence the launchd job ran at,
	// under `StartInterval 60` in com.bborbe.class-d-refusal-watch.
	ClassDRefusalWatchDefaultInterval = 60 * time.Second

	// classDRefusalWatchProducerID and the two dedup keys are carried over
	// byte-for-byte from the Python job, and must stay that way: the store
	// suppresses a duplicate push on producer id plus dedup key, so a renamed
	// key re-posts a card the operator has already seen and a renamed producer
	// re-opens the whole history.
	classDRefusalWatchProducerID     ProducerID     = "class-d-refusal-watch"
	classDRefusalHitDedupKey         DedupKey       = "class-d-refusal-hit"
	classDRefusalClauseGoneDedupKey  DedupKey       = "class-d-refusal-clause-gone"
	classDRefusalWatchInterruptClass InterruptClass = "pick"

	// The clause sanctions TWO wordings for the same refusal, and the live
	// refusals use the first while the second is what the clause's own example
	// quotes. Watching only one of them missed every real occurrence: measured
	// 2026-10-05, 186 refusals after a bare `y` post-fix, 0 seen by the
	// single-phrase watch. A hit needs either.
	classDRefusalWatchPhraseRefusal   = "not an answer"
	classDRefusalWatchPhraseKeystroke = "the pane needs your own keystroke"

	// classDRefusalHitPayloadLines and classDRefusalHitLineLimit mirror the
	// shell's `head -5 | cut -c1-1500`: one card carries at most five hits, each
	// bounded, so a burst cannot make the card unreadable.
	classDRefusalHitPayloadLines = 5
	classDRefusalHitLineLimit    = 1500

	// classDRefusalHitPayloadPrefix is the payload's opening, unchanged.
	classDRefusalHitPayloadPrefix = "Class-D refusal observed — "

	// The three names inside the state directory the Python job used. The layout
	// is kept rather than redesigned so the offsets file a live watch left behind
	// resumes instead of re-baselining.
	classDRefusalHeartbeatFileName = "heartbeat"
	classDRefusalHitsFileName      = "hits.log"
	classDRefusalOffsetsFileName   = "offsets.json"
)

// classDRefusalWatchInfof and classDRefusalWatchErrorf are package variables so
// a spec can capture the per-cycle job lines.
//
// ⚠️ The line is not decoration: the job's effects are a card in the store and a
// line in a hits log, and `job=class-d-refusal-watch action=scan` /
// `action=create card=<id>` is what binds them to the job. A spec that had to
// read glog's file output would be testing glog rather than the job.
var (
	classDRefusalWatchInfof = func(format string, args ...any) {
		glog.Infof(format, args...)
	}
	classDRefusalWatchErrorf = func(format string, args ...any) {
		glog.Errorf(format, args...)
	}
)

// ClassDRefusalClauseProbe reports whether the shipped clause this watch exists
// to observe is still present in the supervisor plugin.
//
// It is an interface so the job can be driven without a plugin cache on disk:
// the probe's whole subject is another repository's released file, which a spec
// must not depend on.
type ClassDRefusalClauseProbe interface {
	// Shipped reports whether the newest shipped goal-loop.md carries the
	// clause, and the file (or glob) it read to decide.
	Shipped(ctx context.Context) (bool, string, error)
}

// NewClassDRefusalClauseProbe builds the probe over a supervisor cache root.
func NewClassDRefusalClauseProbe(root string) ClassDRefusalClauseProbe {
	return &classDRefusalClauseProbe{root: root}
}

type classDRefusalClauseProbe struct {
	root string
}

// Shipped resolves the newest released version directory and greps its
// goal-loop.md for the wording the live refusals actually use.
//
// ⚠️ It probes `not an answer`, not the clause's own example phrase. The example
// phrase is present in the shipped file while every real refusal uses the other
// half, so a probe keyed on the example returned "present" on a watch that could
// not fire — measured 2026-10-05.
//
// ⚠️ Only version-like directories are considered. The cache also holds
// `builtin/`, which sorts last under a plain ordering and would be probed as if
// it were the loaded version.
func (p *classDRefusalClauseProbe) Shipped(
	ctx context.Context,
) (bool, string, error) {
	pattern := filepath.Join(p.root, "*")
	entries, err := filepath.Glob(pattern)
	if err != nil {
		return false, pattern, errors.Wrapf(ctx, err, "glob clause dirs '%s' failed", pattern)
	}
	newest := newestClassDRefusalVersionDir(entries)
	if newest == "" {
		// No released version directory at all: the shipped file the clause lived
		// in is gone, which reads as absent rather than as healthy.
		return false, filepath.Join(pattern, "commands", "goal-loop.md"), nil
	}
	path := filepath.Join(newest, "commands", "goal-loop.md")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// A version directory that carries no goal-loop.md: the clause is
			// absent, which is the same verdict as a reworded one.
			return false, path, nil
		}
		return false, path, errors.Wrapf(ctx, err, "read clause file '%s' failed", path)
	}
	return strings.Contains(string(content), classDRefusalWatchPhraseRefusal), path, nil
}

// classDRefusalVersionPattern matches a cache directory named after a released
// version.
var classDRefusalVersionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// newestClassDRefusalVersionDir returns the highest version-like directory among
// paths, or "" when none matches.
func newestClassDRefusalVersionDir(paths []string) string {
	newest := ""
	var newestParts [3]int
	for _, path := range paths {
		parts, ok := classDRefusalVersionParts(filepath.Base(path))
		if !ok {
			continue
		}
		if newest != "" && !classDRefusalVersionLess(newestParts, parts) {
			continue
		}
		newest, newestParts = path, parts
	}
	return newest
}

// classDRefusalVersionParts parses a `major.minor.patch` directory name.
func classDRefusalVersionParts(name string) ([3]int, bool) {
	match := classDRefusalVersionPattern.FindStringSubmatch(name)
	if match == nil {
		return [3]int{}, false
	}
	var parts [3]int
	for index := range parts {
		value, err := strconv.Atoi(match[index+1])
		if err != nil {
			return [3]int{}, false
		}
		parts[index] = value
	}
	return parts, true
}

// classDRefusalVersionLess reports whether left sorts before right.
func classDRefusalVersionLess(left, right [3]int) bool {
	for index := range left {
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return false
}

// NewClassDRefusalWatchJob watches Claude Code transcripts for the class-D
// refusal — a manager refusing a bare affirmative against a handover — and
// reports each occurrence as one card, until ctx is cancelled.
//
// ⚠️ The heartbeat file is KEPT, and it is no longer the survival mechanism. The
// Python job this replaces was a short-lived launchd tick: it wrote a heartbeat
// and exited by design, so the file's mtime was the only thing that could tell
// the store its producer was still there. In-process the producer is the service
// itself — long-lived and supervised, so the card's survival now rests on the
// service's own liveness rather than on a separate tick re-touching a file. The
// touch is kept anyway, because the store's `heartbeat:<path>` model reads a
// stale or missing file as a dead producer and would prune the card on its next
// read: the file is the witness that model requires, not a second liveness
// mechanism. It is still written BEFORE any post, for the same ordering reason
// the Python job wrote it first — a push that precedes its own heartbeat leaves
// a window in which the card is already prunable.
//
// ⚠️ Every failure is fail-soft: a missed cycle costs one interval of watching
// and loses nothing, so the loop logs it and continues. Taking the process down
// would drop the board over something the board survives.
//
// The interval is a parameter rather than the constant read directly, so a spec
// can drive the tick without waiting a minute for it.
func NewClassDRefusalWatchJob(
	store AttentionStore,
	tailer TranscriptPhraseTailer,
	clause ClassDRefusalClauseProbe,
	stateDir string,
	interval time.Duration,
	now libtime.CurrentDateTimeGetter,
) run.Func {
	job := &classDRefusalWatchJob{
		store:    store,
		tailer:   tailer,
		clause:   clause,
		stateDir: stateDir,
		interval: interval,
		now:      now,
	}
	return job.run
}

type classDRefusalWatchJob struct {
	store    AttentionStore
	tailer   TranscriptPhraseTailer
	clause   ClassDRefusalClauseProbe
	stateDir string
	interval time.Duration
	now      libtime.CurrentDateTimeGetter
}

// run is the loop shape every in-process job in this repo shares: a ticker and
// ctx.Done, with a failed cycle logged and continued rather than returned.
//
// ⚠️ It scans once before the first tick, matching launchd, which ran the tick
// immediately on load. That first cycle is also what arms transcripts it has
// never seen at their current size, so a restart is quiet.
func (j *classDRefusalWatchJob) run(ctx context.Context) error {
	if ctx.Err() == nil {
		j.cycle(ctx)
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			j.cycle(ctx)
		}
	}
}

// cycle runs one pass: touch the heartbeat, scan, report.
func (j *classDRefusalWatchJob) cycle(ctx context.Context) {
	// ⚠️ A cycle whose heartbeat cannot be written RETURNS BEFORE SCANNING, and
	// that ordering is the point: a scan advances the per-file offsets, so a scan
	// whose hits could not be posted would move past the occurrence and never
	// report it — the offsets are the only record of what has been consumed.
	if err := j.touchHeartbeat(ctx); err != nil {
		classDRefusalWatchErrorf(
			"job=%s action=heartbeat_error error=%v",
			classDRefusalWatchJobName,
			err,
		)
		return
	}

	hits, err := j.tailer.Scan(ctx)
	if err != nil {
		classDRefusalWatchErrorf(
			"job=%s action=scan_error error=%v",
			classDRefusalWatchJobName,
			err,
		)
		return
	}
	// ⚠️ The cycle line is logged even when nothing was found. It is the evidence
	// the job ran, and a quiet cycle is a fact an absent log line would hide — a
	// probe could not tell "nothing new" from "the loop is dead".
	classDRefusalWatchInfof(
		"job=%s action=scan hits=%d",
		classDRefusalWatchJobName,
		len(hits),
	)

	if len(hits) > 0 {
		j.reportHits(ctx, hits)
		return
	}
	j.reportStaleClause(ctx)
}

// reportHits appends the hits to the durable hits log and posts ONE card.
//
// ⚠️ One card per cycle, not one per hit: the card is the operator's signal that
// the observable fired at all, and five of them say the same thing five times.
// The full lines stay in the hits log, which is where SC1's evidence lives.
func (j *classDRefusalWatchJob) reportHits(ctx context.Context, hits TranscriptPhraseHits) {
	lines := make([]string, 0, len(hits))
	for _, hit := range hits {
		lines = append(lines, hit.String())
	}
	if err := j.appendHits(ctx, lines); err != nil {
		classDRefusalWatchErrorf(
			"job=%s action=hits_log_error error=%v",
			classDRefusalWatchJobName,
			err,
		)
	}
	item, err := j.store.Push(ctx, j.request(
		classDRefusalHitDedupKey,
		classDRefusalHitPayload(lines),
	))
	if err != nil {
		classDRefusalWatchErrorf(
			"job=%s action=create_error dedup_key=%s error=%v",
			classDRefusalWatchJobName,
			classDRefusalHitDedupKey,
			err,
		)
		return
	}
	if item == nil {
		classDRefusalWatchErrorf(
			"job=%s action=create_error dedup_key=%s error=store returned no item",
			classDRefusalWatchJobName,
			classDRefusalHitDedupKey,
		)
		return
	}
	classDRefusalWatchInfof(
		"job=%s action=create card=%s dedup_key=%s",
		classDRefusalWatchJobName,
		item.ItemID,
		classDRefusalHitDedupKey,
	)
}

// reportStaleClause posts one card when the clause the watch observes has gone
// from the shipped plugin.
//
// ⚠️ It runs only on a cycle with no hits, matching the shell's `elif`: an
// occurrence is the stronger fact, and a stale-clause card beside a real hit
// would read as "the watch is broken" at the one moment it just proved it works.
func (j *classDRefusalWatchJob) reportStaleClause(ctx context.Context) {
	shipped, path, err := j.clause.Shipped(ctx)
	if err != nil {
		classDRefusalWatchErrorf(
			"job=%s action=clause_error error=%v",
			classDRefusalWatchJobName,
			err,
		)
		return
	}
	if shipped {
		return
	}
	item, err := j.store.Push(ctx, j.request(
		classDRefusalClauseGoneDedupKey,
		classDRefusalClauseGonePayload(path),
	))
	if err != nil {
		classDRefusalWatchErrorf(
			"job=%s action=create_error dedup_key=%s error=%v",
			classDRefusalWatchJobName,
			classDRefusalClauseGoneDedupKey,
			err,
		)
		return
	}
	if item == nil {
		classDRefusalWatchErrorf(
			"job=%s action=create_error dedup_key=%s error=store returned no item",
			classDRefusalWatchJobName,
			classDRefusalClauseGoneDedupKey,
		)
		return
	}
	classDRefusalWatchInfof(
		"job=%s action=create card=%s dedup_key=%s",
		classDRefusalWatchJobName,
		item.ItemID,
		classDRefusalClauseGoneDedupKey,
	)
}

// request builds the push declaration both cards share.
//
// ⚠️ producer_kind, interrupt_class and answer_mechanism are carried over from
// the Python poster's defaults (`session`, `pick`, `message`) rather than
// re-derived. The kind is what the board's kind filter and the per-producer
// misroute count group by, so re-declaring it would move this card out of the
// views it appears in today — a port that changes only what it must.
func (j *classDRefusalWatchJob) request(dedupKey DedupKey, payload string) PushRequest {
	return PushRequest{
		ProducerID:      classDRefusalWatchProducerID,
		ProducerKind:    SessionProducerKind,
		LivenessRef:     LivenessRef("heartbeat:" + j.heartbeatPath()),
		DedupKey:        dedupKey,
		InterruptClass:  classDRefusalWatchInterruptClass,
		Payload:         Payload(payload),
		AnswerMechanism: MessageAnswerMechanism,
	}
}

// touchHeartbeat writes the heartbeat file, creating the state directory if
// needed.
//
// The content mirrors the shell's `<epoch> <rfc3339>` line, but the file's MTIME
// is what the store reads — the content is for a human looking at the file.
func (j *classDRefusalWatchJob) touchHeartbeat(ctx context.Context) error {
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create state dir '%s' failed", j.stateDir)
	}
	instant := j.now.Now().Time().UTC()
	content := strconv.FormatInt(instant.Unix(), 10) + " " +
		instant.Format(time.RFC3339) + "\n"
	path := j.heartbeatPath()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return errors.Wrapf(ctx, err, "write heartbeat '%s' failed", path)
	}
	return nil
}

// appendHits appends the hit lines to the hits log.
//
// ⚠️ A failure here is logged and the card still goes out: the card is the
// notification and the log is the evidence, so losing the second must not cost
// the operator the first.
func (j *classDRefusalWatchJob) appendHits(ctx context.Context, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create state dir '%s' failed", j.stateDir)
	}
	path := j.hitsPath()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrapf(ctx, err, "open hits log '%s' failed", path)
	}
	defer file.Close()
	for _, line := range lines {
		if _, err := file.WriteString(line + "\n"); err != nil {
			return errors.Wrapf(ctx, err, "append to hits log '%s' failed", path)
		}
	}
	return nil
}

// heartbeatPath is the file the card's liveness ref names.
func (j *classDRefusalWatchJob) heartbeatPath() string {
	return filepath.Join(j.stateDir, classDRefusalHeartbeatFileName)
}

// hitsPath is the durable log of every reported hit.
func (j *classDRefusalWatchJob) hitsPath() string {
	return filepath.Join(j.stateDir, classDRefusalHitsFileName)
}

// classDRefusalHitPayload renders the card payload from the hits of one cycle.
func classDRefusalHitPayload(lines []string) string {
	if len(lines) > classDRefusalHitPayloadLines {
		lines = lines[:classDRefusalHitPayloadLines]
	}
	bounded := make([]string, 0, len(lines))
	for _, line := range lines {
		bounded = append(bounded, classDRefusalTruncate(line, classDRefusalHitLineLimit))
	}
	return classDRefusalHitPayloadPrefix + strings.Join(bounded, "\n")
}

// classDRefusalClauseGonePayload renders the stale-clause card's payload,
// naming the file it read so a reader can check the claim.
func classDRefusalClauseGonePayload(goalLoopPath string) string {
	return "Class-D refusal watch: the shipped clause '" +
		classDRefusalWatchPhraseKeystroke +
		"' is ABSENT from " + goalLoopPath +
		" — the observable is stale or the premise is void. The watch cannot fire; " +
		"re-derive the wording or reopen the parent task."
}

// classDRefusalTruncate bounds a line quoted in a card payload, by characters
// rather than bytes so a multi-byte transcript cannot be cut mid-rune.
func classDRefusalTruncate(line string, limit int) string {
	runes := []rune(line)
	if len(runes) <= limit {
		return line
	}
	return string(runes[:limit])
}

// NewDefaultClassDRefusalWatchJob builds the job against the paths the launchd
// job used: the transcript root, the state directory, and the supervisor
// plugin's cache.
//
// ⚠️ Resolving them here rather than in main keeps the job's three paths beside
// the job's semantics, and returns an error instead of a job with an
// unresolvable home directory — a watch armed on a guessed path would scan
// nothing and look exactly like a quiet one.
func NewDefaultClassDRefusalWatchJob(
	ctx context.Context,
	store AttentionStore,
) (run.Func, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.Wrap(ctx, err, "resolve home dir failed")
	}
	stateDir := filepath.Join(home, ".claude", "state", "class-d-refusal-watch")
	now := libtime.NewCurrentDateTime()
	return NewClassDRefusalWatchJob(
		store,
		NewTranscriptPhraseTailer(
			filepath.Join(home, ".claude", "projects"),
			filepath.Join(stateDir, classDRefusalOffsetsFileName),
			[]string{
				classDRefusalWatchPhraseRefusal,
				classDRefusalWatchPhraseKeystroke,
			},
			now,
		),
		NewClassDRefusalClauseProbe(
			filepath.Join(
				home,
				".claude",
				"plugins",
				"cache",
				"claude-supervisor",
				"supervisor",
			),
		),
		stateDir,
		ClassDRefusalWatchDefaultInterval,
		now,
	), nil
}
