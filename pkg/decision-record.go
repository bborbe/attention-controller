// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// DecisionRecord is the written record [[Decision Table]] row 46 requires for a
// one-way, wide-reach infrastructure decision. It is a page in the vault, not a
// card: the operator acknowledges it with `vault-cli decision ack`, and only
// that ack releases the work.
//
// The page carries the full decision block — Question, Context, Options,
// Premise, Decision, Blast Radius, Related — because a card cannot carry enough
// context to decide a wide-reach question, which is the failure this record
// exists to remove.
type DecisionRecord struct {
	// Title is the human-readable page title, without the date prefix.
	Title string
	// DedupKey is the producer's dedup key, recorded on the page so a re-push
	// of the same decision reuses the page rather than writing a second one.
	DedupKey DedupKey
	// Question is the card's own payload — what the producer was asking.
	Question string
	// Context is the producer's background for the ask, when it supplied one.
	Context string
	// Options are the choices the producer offered, in its own order.
	Options AnswerOptions
	// Premise is the one line stating what breaks today without the change.
	Premise string
	// BlastRadius is what the change touches and why answering again does not
	// undo it.
	BlastRadius string
}

// DecisionRecordWriter writes a Decision Record page into a vault and returns
// its vault-relative path.
//
// It is an interface rather than a function so the mint path can be exercised
// against an in-memory recorder, and so a store with no vault configured simply
// carries no writer rather than guessing a path.
type DecisionRecordWriter interface {
	Write(ctx context.Context, record DecisionRecord) (string, error)
}

// NewDecisionRecord builds the record for a push request. It is the single
// translation from a card's declaration to the page it becomes, so the mint
// path and its tests cannot disagree about what the page says.
func NewDecisionRecord(request PushRequest) DecisionRecord {
	return DecisionRecord{
		Title:       decisionRecordTitle(request.Payload.String(), request.DedupKey.String()),
		DedupKey:    request.DedupKey,
		Question:    request.Payload.String(),
		Context:     request.Context.String(),
		Options:     request.Options,
		Premise:     decisionRecordPremise(request),
		BlastRadius: BlastRadiusFor(RequestConsequence(request)),
	}
}

// decisionRecordTitle derives a page title from the ask, preferring the first
// sentence of the payload over the dedup key: the payload is what the operator
// will read, while the key is a machine handle. A payload that says nothing
// usable falls back to the key.
func decisionRecordTitle(payload string, dedupKey string) string {
	title := firstSentence(payload)
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "Approve ")
	title = strings.TrimPrefix(title, "approve ")
	title = strings.TrimSuffix(title, "?")
	title = strings.TrimSpace(title)
	if title == "" {
		title = humanizeDedupKey(dedupKey)
	}
	if title == "" {
		title = "Untitled decision"
	}
	return truncateTitle(title, 80)
}

// firstSentence returns the text up to the first sentence terminator, so a long
// card body does not become a page title.
func firstSentence(text string) string {
	for i, r := range text {
		switch r {
		case '.', '?', '!', '\n':
			return text[:i]
		}
	}
	return text
}

// humanizeDedupKey turns `sun-k3s-migration-nuke-2026-11-24` into a readable
// title. It is the fallback for a payload that carries no usable sentence.
func humanizeDedupKey(dedupKey string) string {
	title := strings.ReplaceAll(dedupKey, "-", " ")
	title = strings.ReplaceAll(title, "_", " ")
	return strings.TrimSpace(title)
}

// truncateTitle bounds the title to a length that is still a filename, cutting
// on a rune boundary so a multi-byte character is never split.
func truncateTitle(title string, max int) string {
	if utf8.RuneCountInString(title) <= max {
		return title
	}
	runes := []rune(title)
	return strings.TrimSpace(string(runes[:max]))
}

// decisionRecordPremise states what breaks today without the change, in one
// line. It uses the producer's own context when one was supplied, because the
// producer is the only party that knows the condition the card is trying to
// fix; with no context it states the class honestly rather than inventing one.
func decisionRecordPremise(request PushRequest) string {
	if context := strings.TrimSpace(request.Context.String()); context != "" {
		return firstSentence(context)
	}
	return "the operation described on the card does not happen and the current arrangement stands unchanged."
}

// Markdown renders the page body, frontmatter included.
//
// ⚠️ It deliberately writes no `reviewed`, `status: accepted`, `decision_status`
// or `selected_option`: those are the operator's ack fields, and a page that
// pre-filled them would release itself. Only `vault-cli decision ack` writes
// them.
func (r DecisionRecord) Markdown() string {
	var builder strings.Builder
	builder.WriteString("---\n")
	if r.DedupKey != "" {
		builder.WriteString("dedup_key: " + yamlScalar(r.DedupKey.String()) + "\n")
	}
	builder.WriteString("needs_review: true\n")
	builder.WriteString("status: proposed\n")
	builder.WriteString("type: Decision Record\n")
	builder.WriteString("---\n")
	builder.WriteString("Tags: [[Decision Record]]\n\n---\n\n")
	builder.WriteString("# Question\n")
	builder.WriteString(r.Question + "\n\n")
	builder.WriteString("# Context\n")
	if strings.TrimSpace(r.Context) == "" {
		builder.WriteString("(The producer supplied no additional context.)\n\n")
	} else {
		builder.WriteString(r.Context + "\n\n")
	}
	builder.WriteString("# Options\n")
	if len(r.Options) == 0 {
		builder.WriteString("(The producer offered no options; see the Question above.)\n\n")
	} else {
		for index, option := range r.Options {
			fmt.Fprintf(&builder, "## Option %d — %s", index+1, option.Label)
			if option.Recommended {
				builder.WriteString(" (recommended)")
			}
			builder.WriteString("\n")
			if option.Description != "" {
				builder.WriteString(option.Description + "\n")
			}
			builder.WriteString("\n")
		}
	}
	builder.WriteString("# Premise\n")
	builder.WriteString("What breaks today without this change: " + r.Premise + "\n\n")
	builder.WriteString("# Decision\n")
	builder.WriteString("**Proposed:** " + recommendedLabel(r.Options) +
		" — awaiting the operator's `vault-cli decision ack`.\n\n")
	builder.WriteString("# Blast Radius\n")
	builder.WriteString(r.BlastRadius + "\n\n")
	builder.WriteString("# Related\n")
	builder.WriteString("- Card dedup key: `" + r.DedupKey.String() + "`\n")
	return builder.String()
}

// recommendedLabel names the producer's recommendation, or points at the option
// list when it recommended none.
func recommendedLabel(options AnswerOptions) string {
	for _, option := range options {
		if option.Recommended {
			return option.Label
		}
	}
	if len(options) > 0 {
		return "see the options above"
	}
	return "see the question above"
}

// yamlScalarPattern matches a value that is safe as a YAML plain scalar.
var yamlScalarPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// yamlReservedScalar matches a value a YAML reader would coerce to a non-string
// (a bool, null, or a number), so a dedup key of `true` or `123` is quoted
// rather than read back as a boolean or int.
var yamlReservedScalar = regexp.MustCompile(
	`(?i)^(?:true|false|yes|no|on|off|null|~|-?\d+(?:\.\d+)?)$`,
)

// yamlScalar quotes a value when a YAML plain scalar would misread it: a value
// with special characters, or one a reader would coerce to a non-string (a
// bool, null, or a number). Inside the quotes it escapes backslash and the
// other YAML double-quote specials, so a producer-controlled dedup key cannot
// break the frontmatter or change type on read-back.
func yamlScalar(value string) string {
	if yamlScalarPattern.MatchString(value) && !yamlReservedScalar.MatchString(value) {
		return value
	}
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return `"` + replacer.Replace(value) + `"`
}

// NewVaultDecisionRecordWriter returns a writer that puts Decision Record pages
// in `<vaultDir>/50 Knowledge Base/`, the location
// [[One-Way and Two-Way Door Decisions]] § Where the decision is made names.
func NewVaultDecisionRecordWriter(
	vaultDir string,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) DecisionRecordWriter {
	return &vaultDecisionRecordWriter{
		vaultDir: vaultDir,
		clock:    currentDateTimeGetter,
	}
}

type vaultDecisionRecordWriter struct {
	vaultDir string
	clock    libtime.CurrentDateTimeGetter
}

// decisionRecordDir is the vault-relative directory the pages live in.
const decisionRecordDir = "50 Knowledge Base"

// Write writes the page and returns its vault-relative path. A page already
// carrying this decision's dedup key is reused rather than duplicated, so a
// producer re-pushing the same decision points at one page.
func (w *vaultDecisionRecordWriter) Write(
	ctx context.Context,
	record DecisionRecord,
) (string, error) {
	if strings.TrimSpace(w.vaultDir) == "" {
		return "", errors.New(ctx, "decision record writer has no vault dir")
	}
	directory := filepath.Join(w.vaultDir, decisionRecordDir)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", errors.Wrap(ctx, err, "create decision record dir failed")
	}
	if existing, ok := w.findByDedupKey(directory, record.DedupKey); ok {
		return existing, nil
	}
	date := w.clock.Now().Time().Format("2006-01-02")
	base := date + " - " + sanitizeFilename(record.Title)
	relative := filepath.Join(decisionRecordDir, base+".md")
	target := filepath.Join(w.vaultDir, relative)
	for suffix := 2; fileExists(target); suffix++ {
		relative = filepath.Join(decisionRecordDir, fmt.Sprintf("%s (%d).md", base, suffix))
		target = filepath.Join(w.vaultDir, relative)
	}
	if err := os.WriteFile(target, []byte(record.Markdown()), 0o600); err != nil {
		return "", errors.Wrap(ctx, err, "write decision record failed")
	}
	return relative, nil
}

// findByDedupKey returns the vault-relative path of an existing page carrying
// this dedup key, so a re-push reuses it.
func (w *vaultDecisionRecordWriter) findByDedupKey(
	directory string,
	dedupKey DedupKey,
) (string, bool) {
	if dedupKey == "" {
		return "", false
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", false
	}
	needle := "dedup_key: " + yamlScalar(dedupKey.String())
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		body, readErr := os.ReadFile(
			filepath.Join(directory, entry.Name()),
		) //#nosec G304 -- bounded to the configured vault dir
		if readErr != nil {
			continue
		}
		if frontmatterHasLine(frontmatterOf(string(body)), needle) {
			return filepath.Join(decisionRecordDir, entry.Name()), true
		}
	}
	return "", false
}

// frontmatterHasLine reports whether the frontmatter block carries the exact
// line, so a dedup key `sun` never matches a different key `sun-extra` — a
// substring match would link the operator (and their `decision ack`) to the
// wrong page.
func frontmatterHasLine(frontmatter string, line string) bool {
	for _, candidate := range strings.Split(frontmatter, "\n") {
		if strings.TrimRight(candidate, " \r") == line {
			return true
		}
	}
	return false
}

// frontmatterOf returns the frontmatter block of a markdown document, or an
// empty string when it carries none.
func frontmatterOf(document string) string {
	if !strings.HasPrefix(document, "---\n") {
		return ""
	}
	rest := document[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// fileExists reports whether the path names an existing file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// filenameUnsafe matches every character a filename should not carry on the
// platforms this runs on.
var filenameUnsafe = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)

// sanitizeFilename turns a title into a filename stem, collapsing runs of
// separators so a payload full of punctuation does not produce a ragged name.
func sanitizeFilename(title string) string {
	name := filenameUnsafe.ReplaceAllString(title, "-")
	name = strings.Join(strings.Fields(name), " ")
	name = strings.Trim(name, " .-")
	if name == "" {
		return "Untitled decision"
	}
	return name
}
