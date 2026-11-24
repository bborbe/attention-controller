// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
)

// Consequence is the class of downstream effect an approval unleashes — what
// the card's answer actually sets in motion, read from the operation the card
// describes rather than from the card's own declared wording.
//
// It exists because [[Attention Cards Carry a Risk Level]] derived the rendered
// risk from the card's own text: a production-touching marker, an irreversible
// verb, an operator-only gate. That derivation has nothing to key on when the
// destructive work lives one step downstream, in what the approval unleashes —
// the defect measured on 2026-11-24, when a card reading "approve — migrate to
// nuke" was answered fifteen seconds after it was minted.
//
// The consequence is derived at RENDER time and at MINT time from the same
// predicate, so the board and the mint path cannot disagree about what class a
// payload belongs to.
type Consequence string

const (
	// NoConsequence is the absence of a consequence class — a routine card.
	NoConsequence Consequence = ""
	// IrreversibleInfraConsequence is the class [[Decision Table]] row 46 names:
	// moving or migrating a VM, rebuilding a cluster or host, moving disks or
	// data between hosts, or changing which host carries a workload. It is never
	// a card and never an `approve:` line; it is a Decision Record page released
	// only by the operator's own `vault-cli decision ack`.
	IrreversibleInfraConsequence Consequence = "irreversible-infra"
)

// String returns the consequence as a string.
func (c Consequence) String() string {
	return string(c)
}

// irreversibleInfraPatterns match the four forms [[Decision Table]] row 46
// names, each as an ACTION near an OBJECT rather than as a single verb.
//
// ⚠️ Requiring both halves is what keeps a routine card from being swept up: a
// task approval says "approve" and a self-contained one-way card may say
// "delete", and neither names a VM, a host, a cluster or a disk. A single-verb
// match would classify "Delete the stale branch" as infrastructure, which it is
// not — the branch is one target and one action, judgeable from the card alone,
// which is exactly what makes it a legal card.
//
// The window is bounded by sentence punctuation as well as by length so a card
// naming an action in one sentence and an unrelated object in another does not
// match across the boundary.
var irreversibleInfraPatterns = []*regexp.Regexp{
	// Move or migrate a VM / cluster / host / workload.
	regexp.MustCompile(
		`(?i)\b(?:migrat|mov|relocat|transfer)\w*\b[^.\n]{0,100}\b(?:vm|vms|virtual machine|virtual machines|cluster|clusters|host|hosts|node|nodes|k3s|kubernetes|k8s|workload|workloads)\b`,
	),
	// Rebuild or recreate a cluster or host.
	regexp.MustCompile(
		`(?i)\b(?:rebuild|recreat|reinstall|reprovision|drain|evict|evacuat)\w*\b[^.\n]{0,100}\b(?:cluster|clusters|host|hosts|node|nodes|k3s|kubernetes)\b`,
	),
	// Move, resize or shrink disks or volumes.
	regexp.MustCompile(
		`(?i)\b(?:mov|migrat|relocat|resiz|shrink|grow|extend)\w*\b[^.\n]{0,100}\b(?:disk|disks|volume|volumes|vg\d*|lv\d*|logical volume|volume group|datastore|datastores)\b`,
	),
	// Move data between hosts.
	regexp.MustCompile(
		`(?i)\b(?:mov|migrat|transfer)\w*\b[^.\n]{0,100}\bdata\b[^.\n]{0,100}\bfrom\b`,
	),
	// Change which host carries a workload.
	regexp.MustCompile(
		`(?i)\bchang\w*\b[^.\n]{0,100}\bhost\b[^.\n]{0,100}\bworkload\b`,
	),
}

// ⚠️ **What this predicate cannot see.** It reads the operation a card
// DESCRIBES, so a card that states the consequence only obliquely — "make sun
// redundant", "free up the hypervisor", or a phrasing in another language —
// matches nothing and falls back to the declared level and the verb rule. It is
// a backstop for the day a card is minted anyway, not a proof that no such card
// can be minted: the mint-path rule is the real fix, and the render rule only
// catches the wordings it can name.
//
// ConsequenceOf returns the consequence class of the given texts, read as one
// body. Empty texts are skipped, so a caller can pass the fields it holds
// without filtering them first.
func ConsequenceOf(texts ...string) Consequence {
	body := strings.Join(texts, "\n")
	for _, pattern := range irreversibleInfraPatterns {
		if pattern.MatchString(body) {
			return IrreversibleInfraConsequence
		}
	}
	return NoConsequence
}

// RequestConsequence returns the consequence class a push request describes.
//
// It reads every operator-facing field the request carries — the payload, the
// context, the option labels and the dedup key — because a producer may put the
// operation in any of them. The dedup key is included because it is often the
// most explicit statement of the operation (`sun-k3s-migration-nuke-2026-11-24`),
// and a card that names a migration in its key is a migration card whatever its
// prose says.
func RequestConsequence(request PushRequest) Consequence {
	return ConsequenceOf(requestTexts(
		request.Payload.String(),
		request.Context.String(),
		request.DedupKey.String(),
		consequenceOptionLabels(request.Options),
	)...)
}

// ItemConsequence returns the consequence class a stored item describes.
func ItemConsequence(item Item) Consequence {
	return ConsequenceOf(requestTexts(
		item.Payload.String(),
		item.Context.String(),
		item.DedupKey.String(),
		consequenceOptionLabels(item.Options),
	)...)
}

// requestTexts filters empty strings, so ConsequenceOf sees only what the item
// actually says.
func requestTexts(texts ...string) []string {
	kept := make([]string, 0, len(texts))
	for _, text := range texts {
		if strings.TrimSpace(text) != "" {
			kept = append(kept, text)
		}
	}
	return kept
}

// consequenceOptionLabels returns the labels of the given options, one per
// line, so a consequence named only in an option label is still seen.
func consequenceOptionLabels(options AnswerOptions) string {
	labels := make([]string, 0, len(options))
	for _, option := range options {
		labels = append(labels, option.Label)
	}
	return strings.Join(labels, "\n")
}

// blastRadiusStatement is the blast radius the board states for an
// irreversible-infrastructure card that did not state one itself.
//
// ⚠️ It is deliberately generic. The card cannot know which hosts, volumes or
// clusters a given operation touches, and inventing specifics would be a
// fabricated blast radius presented as a measured one — the failure the
// attention stack's plain-sentence contract exists to prevent. What it can say
// honestly is the class: an infrastructure change touches everything it names
// and is not undone by answering the card again.
const blastRadiusStatement = "Blast radius: irreversible infrastructure change — every host, cluster and volume it touches is affected until it is undone by hand; answering this card does not undo it."

// BlastRadiusFor returns the blast radius the board renders for a consequence
// class. It is empty for NoConsequence, so a routine card renders none.
func BlastRadiusFor(consequence Consequence) string {
	if consequence != IrreversibleInfraConsequence {
		return ""
	}
	return blastRadiusStatement
}

// BlastRadiusOf returns the blast radius the board renders for an item.
func BlastRadiusOf(item Item) string {
	return BlastRadiusFor(ItemConsequence(item))
}
