// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
)

// OperatorTierWords is the operator-word list a `pick` payload must match to be
// operator-tier. It mirrors `attention-placement.py`'s `OPERATOR_WORDS` exactly,
// in the same order; `docs/operator-tier.md` carries the table and
// `TestOperatorTierDocMatchesCode` fails when the two drift.
var OperatorTierWords = []string{
	"prod",
	"production",
	"deploy",
	"make apply",
	"make buca",
	"merge",
	"release",
	"delete",
	"force",
	"secret",
	"credential",
	"token",
	"live trade",
	"TDR",
	"approve:",
}

// OperatorArmKind is how an arm value is matched against the operator set.
type OperatorArmKind string

const (
	// OperatorArmEquals matches the whole value.
	OperatorArmEquals OperatorArmKind = "equals"
	// OperatorArmContains matches a substring, mirroring the probe's `in by`.
	OperatorArmContains OperatorArmKind = "contains"
	// OperatorArmPrefix matches a leading substring.
	OperatorArmPrefix OperatorArmKind = "prefix"
)

// OperatorArm is one entry of the operator set.
type OperatorArm struct {
	Kind  OperatorArmKind
	Value string
}

// OperatorTierArms is the set of arms allowed to settle an operator-tier item.
// It mirrors `attention-placement.py`'s `answer_tier` exactly — same exact value,
// same substring, same prefix. `docs/operator-tier.md` carries the table and
// `TestOperatorTierDocMatchesCode` fails when the two drift.
var OperatorTierArms = []OperatorArm{
	{Kind: OperatorArmEquals, Value: "operator"},
	{Kind: OperatorArmContains, Value: "attention-board"},
	{Kind: OperatorArmPrefix, Value: "supervisor:attention-next"},
}

// operatorOnlyWords compiles OperatorTierWords into the case-insensitive pattern
// the probe uses (`re.I`), with one correction the probe needs: a word is bounded
// on the RIGHT only when it ends in a word character. `approve:` ends in a colon,
// so the probe's trailing `\b` never matches the canonical `approve: repair` form
// (a space after the colon is not a word boundary) and the token is a no-op
// there. The store bounds `approve:` on the left only, so it matches.
var operatorOnlyWords = func() *regexp.Regexp {
	closed := make([]string, 0, len(OperatorTierWords))
	open := make([]string, 0, len(OperatorTierWords))
	for _, word := range OperatorTierWords {
		quoted := regexp.QuoteMeta(word)
		if endsWithWordCharacter(word) {
			closed = append(closed, quoted)
		} else {
			open = append(open, quoted)
		}
	}
	parts := make([]string, 0, 2)
	if len(closed) > 0 {
		parts = append(parts, `\b(`+strings.Join(closed, "|")+`)\b`)
	}
	if len(open) > 0 {
		parts = append(parts, `\b(`+strings.Join(open, "|")+`)`)
	}
	return regexp.MustCompile(`(?i)(` + strings.Join(parts, "|") + `)`)
}()

// endsWithWordCharacter reports whether the last byte of word is a regex word
// character, so a trailing `\b` can follow it.
func endsWithWordCharacter(word string) bool {
	if word == "" {
		return false
	}
	last := word[len(word)-1]
	return last == '_' ||
		(last >= 'a' && last <= 'z') ||
		(last >= 'A' && last <= 'Z') ||
		(last >= '0' && last <= '9')
}

// operatorTier reports whether an item may be settled only by the operator.
//
// ⚠️ The `permission` class is excluded: it is tiered by `permission-tier.py`,
// and a manager-tier permission answer must keep landing. The `ack` class is not
// governed either — it is a separate operator-tier class with its own producer
// path, out of this rule's scope.
func operatorTier(item *Item) bool {
	if item.AnswerMechanism == PermissionAnswerMechanism {
		return false
	}
	switch item.InterruptClass {
	case ApproveInterruptClass, InterruptClass("decision"):
		return true
	case PickInterruptClass:
		if item.ProducerKind == ManagerProducerKind {
			return true
		}
		return item.ProducerKind == SessionProducerKind &&
			operatorOnlyWords.MatchString(string(item.Payload))
	}
	return false
}

// operatorArm reports whether arm is one the operator answers through. The empty
// arm is not an operator arm — the callers decide separately whether a no-arm
// write is allowed, because a producer withdrawal and an armless answer mean
// different things.
func operatorArm(arm string) bool {
	for _, candidate := range OperatorTierArms {
		switch candidate.Kind {
		case OperatorArmEquals:
			if arm == candidate.Value {
				return true
			}
		case OperatorArmContains:
			if strings.Contains(arm, candidate.Value) {
				return true
			}
		case OperatorArmPrefix:
			if strings.HasPrefix(arm, candidate.Value) {
				return true
			}
		}
	}
	return false
}

// operatorTierLowerArmRefused reports whether this arm may not cause the
// transition on this item.
//
// allowEmptyArm is true on the close route — a producer withdrawing its own card
// names no arm and answers nothing — and false on the answer route, where an
// armless answer names no operator and is therefore refused.
func operatorTierLowerArmRefused(item *Item, arm string, allowEmptyArm bool) bool {
	if arm == "" {
		return !allowEmptyArm && operatorTier(item)
	}
	return operatorTier(item) && !operatorArm(arm)
}
