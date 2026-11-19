// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"crypto/sha256"
	"encoding/hex"
)

// managerContentKey is the identity of a gate's CONTENT — the exact string a
// NEW GATE carries. Per body hash, never per session and never per
// session+reason: a session already in the set never re-emits, and a body
// change under a stable reason must.
//
// ⚠️ sha256, never SHA-1. gosec runs via golangci-lint inside `make precommit`
// and rejects the SHA-1 import under G505/G401. The truncation is the identity,
// not a security measure — nothing here needs a strong digest, but the lint does
// need one that is not on its deny list.
func managerContentKey(reason, detail string) string {
	sum := sha256.Sum256([]byte(reason + " :: " + detail))
	return hex.EncodeToString(sum[:])[:managerContentKeyLength]
}

// managerContentKeyLength is how many hex characters of the digest make up a
// content key. It is also what distinguishes a key from a legacy entry.
const managerContentKeyLength = 16

// managerIsContentKey reports whether a committed `prev` value is a content key
// rather than a legacy membership-only entry.
func managerIsContentKey(value string) bool {
	if len(value) != managerContentKeyLength {
		return false
	}
	for _, char := range value {
		isDigit := char >= '0' && char <= '9'
		isLowerHex := char >= 'a' && char <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}
	return true
}

// managerContentMatches reports whether the value committed for a session
// already describes that session's current content, so the pass must emit no
// NEW GATE.
//
// ⚠️ A LEGACY entry — the empty string, or the pre-content-key human
// `reason :: detail` string prompt 3 committed — proves nothing about content
// and therefore matches. That is the documented upgrade path: an entry that
// predates content keys is recorded in place instead of re-emitting every
// member once, and the NEXT pass can then tell a change from a repeat.
func managerContentMatches(known string, state ManagerGateState) bool {
	if !managerIsContentKey(known) {
		return true
	}
	return known == managerContentKey(state.Reason, state.Detail)
}
