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
func IsDoneClassAnswer(answer *Answer) bool {
	if answer == nil {
		return false
	}
	labels := make([]string, 0, 1+len(answer.Values))
	if answer.Value != "" {
		labels = append(labels, answer.Value)
	}
	labels = append(labels, answer.Values...)
	for _, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		for _, prefix := range DoneClassPrefixes {
			if strings.HasPrefix(normalized, prefix) {
				return true
			}
		}
	}
	return false
}

// pullRequestURL matches a GitHub pull-request URL and captures owner, repo and
// number.
var pullRequestURL = regexp.MustCompile(`https://github\.com/([^/\s]+)/([^/\s]+)/pull/([0-9]+)`)

// absolutePath matches an absolute path token in free text.
var absolutePath = regexp.MustCompile(`(?:^|\s)(/[^\s,;:]+)`)

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

// EvidenceClaimFromText returns the artifact an answer named as proof, or an
// empty string when it named none.
//
// A pull-request URL wins over a path because it is the stronger claim: a path
// can exist for unrelated reasons, while a merged PR is the effect itself.
func EvidenceClaimFromText(text string) string {
	if match := pullRequestURL.FindString(text); match != "" {
		return match
	}
	if match := absolutePath.FindStringSubmatch(text); len(match) == 2 {
		// A bare "/" or a one-segment token is not a usable artifact.
		if strings.Count(match[1], "/") >= 2 {
			return match[1]
		}
	}
	return ""
}

// EvidenceRunner runs one external probe and reports whether it succeeded. It
// exists so the sweep can be tested without shelling out.
type EvidenceRunner func(ctx context.Context, name string, args ...string) (string, error)

// evidenceRunner is the probe the sweep uses. It is a package-level variable so
// a test can inject a fake without threading it through every constructor; the
// production value never changes.
var evidenceRunner EvidenceRunner = DefaultEvidenceRunner

// DefaultEvidenceRunner runs the probe on this machine.
func DefaultEvidenceRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
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
		out, err := run(ctx, "gh", "-R", owner+"/"+repo, "pr", "view", number, "--json", "state", "--jq", ".state")
		if err != nil {
			return false
		}
		state := strings.ToUpper(strings.TrimSpace(out))
		return state == "MERGED" || state == "CLOSED"
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
	i.EvidenceClaim = EvidenceClaimFromText(AnswerText(answer))
	i.EvidenceDeadline = NewEvidenceDeadline(now)
}
