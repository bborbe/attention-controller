// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/golang/glog"
	libtime "github.com/bborbe/time"
)

// EvidenceDeadline is how long a `done`-class claim may wait unobserved before
// the item returns to the board.
//
// It is deliberately short: the claim is about work the operator says already
// happened, so an artifact that is not visible within half an hour is either
// not there or not where the answer said it would be — and in both cases the
// operator needs to see the card again rather than trust a `done`.
const EvidenceDeadline = 30 * 60 * 1000 * 1000 * 1000 // 30m in nanoseconds

// EvidenceProbeTimeout bounds ONE probe, so a hung probe costs its own claim
// rather than the sweep's whole budget.
const EvidenceProbeTimeout = 10 * 1000 * 1000 * 1000 // 10s

// MaxEvidenceProbesPerSweep bounds how many parked items one sweep probes.
//
// ⚠️ The sweep runs under its caller's deadline (30 s), and a probe can cost
// EvidenceProbeTimeout, so probing every parked item in one tick can outrun that
// budget and fail the apply pass — which would strand a parked card with its
// deadline reopen never firing. A small batch per tick drains a backlog over a
// few minutes and keeps every tick inside its bound.
const MaxEvidenceProbesPerSweep = 4

// DoneClassPrefixes are the answer labels that assert the work is finished.
//
// ⚠️ The match is a case-insensitive PREFIX, not equality: operators answer
// "done — reviewed and flipped both", "merged", "deployed to prod". An exact
// match would have let the 2026-11-13 answer through as an ordinary answer and
// left the false `done` invisible, which is the defect this whole path exists
// to remove.
var DoneClassPrefixes = []string{"done", "merged", "deployed", "shipped", "fixed"}

// IsDoneClassAnswer reports whether the operator's answer claims the work is
// finished.
//
// ⚠️ ONE rule for every answer shape, and it is the whole-word one: the done
// word must stand alone or be followed by a separator, or by the artifact the
// answer names (`done https://…`, `done /srv/app/build.log`). A bare prefix
// would treat `done for now`, `deployed to staging` and `fixed pending review`
// as finished claims — such an answer usually names no artifact, so it would
// park, never verify, and come back to the board 30 minutes later for nothing.
func IsDoneClassAnswer(answer *Answer) bool {
	if answer == nil {
		return false
	}
	// ⚠️ ONE rule for both shapes, and it is the whole-word one. A bare prefix
	// match on free text treats `done for now`, `fixed pending review` and
	// `done? no, still working` as finished claims; such an answer usually names
	// no artifact, so it parks, can never verify, and comes back 30 minutes later
	// for no reason. The motivating answer still matches — `done — reviewed and
	// flipped both` continues with a dash, which is a separator, not a word.
	for _, label := range append([]string{answer.Value}, answer.Values...) {
		if isDoneClassOptionLabel(label) {
			return true
		}
	}
	return false
}

// isDoneClassOptionLabel reports whether an option label names a done word as a
// whole word: `done`, `done — reviewed and flipped both`, `merged: #77` yes;
// `done for now`, `fixed pending review` no.
func isDoneClassOptionLabel(label string) bool {
	normalized := strings.ToLower(strings.TrimSpace(label))
	for _, prefix := range DoneClassPrefixes {
		if normalized == prefix {
			return true
		}
		if !strings.HasPrefix(normalized, prefix) {
			continue
		}
		rest := normalized[len(prefix):]
		// A separator, not a letter: the next rune decides whether the word
		// ended there or the label merely starts with it.
		if rest == "" {
			return true
		}
		// Spaces are skipped, not decisive: `done — reviewed` and `done —
		// reviewed` are the same claim, while `done for now` continues with a
		// word and claims nothing finished.
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return true
		}
		// An artifact may follow the word directly: `done https://github.com/…`
		// and `done /srv/app/build.log` are claims, and the URL or path IS the
		// proof the answer names.
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") || strings.HasPrefix(rest, "/") {
			return true
		}
		next, _ := utf8.DecodeRuneInString(rest)
		switch next {
		case '-', '—', '–', ':', ',', '.', '!', '(':
			return true
		}
	}
	return false
}

// pullRequestURL matches a GitHub pull-request URL and captures owner, repo and
// number.
var pullRequestURL = regexp.MustCompile(`https://github\.com/([^/\s]+)/([^/\s]+)/pull/([0-9]+)`)

// absolutePath matches an absolute path token in free text.
//
// ⚠️ It accepts a delimiter before the slash (`(`, a quote, a bracket) so a
// markdown link like `[build](/srv/app/build.log)` yields its path, and it
// stops at closing delimiters so the captured token is the path itself. The
// trailing punctuation of prose (`done, see /srv/app/build.log.`) is stripped
// by trimming after the match — a regex alone cannot tell a full stop from a
// dot inside a filename, and the stat is what settles it.
var absolutePath = regexp.MustCompile(`(?:^|[\s(\["'<=])(/[^\s,;:)\]}"'<>]+)`)

// pathTrailingPunctuation is what a sentence may append to a path.
const pathTrailingPunctuation = ".,;:!?"

// AnswerText is the operator's own words on an answer, whichever kind carried
// them.
func AnswerText(answer *Answer) string {
	if answer == nil {
		return ""
	}
	parts := make([]string, 0, 1+len(answer.Values))
	if answer.Value != "" {
		parts = append(parts, answer.Value)
	}
	parts = append(parts, answer.Values...)
	return strings.Join(parts, " ")
}

// EvidenceClaimsFromText returns every artifact an answer named as proof, or
// an empty slice when it named none.
//
// ⚠️ ALL matches are returned, never just the first. The answer this feature
// exists for named two PRs, and a first-match rule would close the card as soon
// as one merged while the other claim was untouched.
//
// Pull-request URLs win over paths when the text carries both: a path can exist
// for unrelated reasons, while a merged PR is the effect itself.
func EvidenceClaimsFromText(text string) []string {
	if matches := pullRequestURL.FindAllString(text, -1); len(matches) > 0 {
		return uniqueStrings(matches)
	}
	claims := make([]string, 0)
	for _, match := range absolutePath.FindAllStringSubmatch(text, -1) {
		claim := strings.TrimRight(match[1], pathTrailingPunctuation)
		// A bare "/" or a one-segment token is not a usable artifact.
		if strings.Count(claim, "/") >= 2 {
			claims = append(claims, claim)
		}
	}
	return uniqueStrings(claims)
}

// uniqueStrings drops duplicates while keeping the first occurrence's order.
func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// EvidenceRunner runs one external probe and reports whether it succeeded. It
// exists so the sweep can be tested without shelling out.
type EvidenceRunner func(ctx context.Context, name string, args ...string) (string, error)

// DefaultEvidenceRunner runs the probe on this machine.
func DefaultEvidenceRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// ⚠️ WaitDelay bounds the CALL, not just the process: if `gh` leaves a
	// descendant holding the inherited stdout pipe, Output() blocks until that
	// descendant exits even after the context killed `gh` — and this probe runs
	// inside the store's write transaction. Same shape and same fix as
	// pane-lister.go.
	cmd.WaitDelay = EvidenceProbeTimeout
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// EvidenceObserved reports whether the claimed artifact is visible.
//
// A claim that is neither a pull-request URL nor an absolute path is never
// observed: the store does not guess what the operator meant, because a guess
// that happens to succeed would close the card on evidence the store invented.
func EvidenceObserved(ctx context.Context, claim string, run EvidenceRunner) bool {
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return false
	}
	if match := pullRequestURL.FindStringSubmatch(claim); len(match) == 4 {
		owner, repo, number := match[1], match[2], match[3]
		if run == nil {
			run = DefaultEvidenceRunner
		}
		// ⚠️ Bounded per probe, not only by the sweep's own deadline: the probe
		// runs inside the store's write transaction, so one hung `gh` would hold
		// the lock for the whole sweep budget instead of costing this claim alone.
		ctx, cancel := context.WithTimeout(ctx, EvidenceProbeTimeout)
		defer cancel()
		out, err := run(ctx, "gh", "-R", owner+"/"+repo, "pr", "view", number, "--json", "state", "--jq", ".state")
		if err != nil {
			glog.V(2).Infof("evidence probe failed for %s: %v", claim, err)
			return false
		}
		state := strings.ToUpper(strings.TrimSpace(out))
		// ⚠️ `MERGED` only. `CLOSED` means closed WITHOUT merging, so a PR that
		// was abandoned or superseded would close the item as satisfied while the
		// work it claimed never landed — the exact false `done` this state exists
		// to prevent, arriving through the probe instead of the answer.
		glog.V(2).Infof("evidence probe for %s answered state=%s", claim, state)
		return state == "MERGED"
	}
	if strings.HasPrefix(claim, "/") {
		info, err := os.Stat(claim)
		return err == nil && !info.IsDir()
	}
	return false
}

// NewEvidenceDeadline returns the deadline for an item parked now.
func NewEvidenceDeadline(now libtime.DateTime) *libtime.DateTime {
	deadline := now.Add(libtime.Duration(EvidenceDeadline))
	return &deadline
}

// parkForEvidence moves the item to `awaiting_evidence` when the answer claims
// the work is done, recording what would prove it and when the claim stops
// waiting. It is a no-op for every other answer.
//
// It lives here rather than inline in the store's Answer method so that method's
// cognitive complexity stays inside the lint bound — the rule this path exists
// to satisfy is the claim, and it reads better named.
func (i *Item) parkForEvidence(answer *Answer, now libtime.DateTime) {
	if !IsDoneClassAnswer(answer) {
		return
	}
	i.State = AwaitingEvidenceState
	i.EvidenceClaims = EvidenceClaimsFromText(AnswerText(answer))
	i.EvidenceDeadline = NewEvidenceDeadline(now)
}

// EvidenceAllObserved reports whether EVERY claim is visible. An empty claim set
// is never observed: the store does not treat "the answer named nothing" as
// satisfied, because that is the case it cannot check.
func EvidenceAllObserved(ctx context.Context, claims []string, run EvidenceRunner) bool {
	if len(claims) == 0 {
		return false
	}
	for _, claim := range claims {
		if !EvidenceObserved(ctx, claim, run) {
			return false
		}
	}
	return true
}
