// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// redaction patterns, ported from the Python writer's module constants.
//
// ⚠️ redact() and scrubDetail() are deliberately two functions, not one with a
// flag. redact() feeds EventItemID, so widening it would move every dedup key
// ever written; only scrubDetail() may apply the bare assignment form, because
// that is the one place a detail reaches a state file.
var (
	// authHeaderPattern matches `Authorization: <value>` and
	// `Authorization: Bearer <value>`, replacing the whole value.
	authHeaderPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*)(?:bearer\s+)?\S+`)
	// bareBearerPattern matches a bare `Bearer <value>` outside a header.
	bareBearerPattern = regexp.MustCompile(`(?i)\b(bearer)\s+\S+`)
	// assignSecretPattern matches a bare `NAME=value` assignment whose name is
	// credential-shaped. It is applied by scrubDetail only.
	assignSecretPattern = regexp.MustCompile(
		`(?i)\b([A-Za-z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|APIKEY|API_KEY|ACCESS_KEY|PRIVATE_KEY)[A-Za-z0-9_]*\s*=\s*)([^\s"'&;]+)`,
	)
)

// redact replaces the header and bare-bearer credential forms. It is the
// ceiling on what EventItemID hashes and on what a detail may carry.
func redact(text string) string {
	text = authHeaderPattern.ReplaceAllString(text, `${1}[REDACTED]`)
	text = bareBearerPattern.ReplaceAllString(text, `${1} [REDACTED]`)
	return text
}

// scrubDetail applies redact and then the bare `NAME=value` assignment form.
func scrubDetail(text string) string {
	text = redact(text)
	return assignSecretPattern.ReplaceAllString(text, `${1}<redacted>`)
}

// EventItemID is the event log's dedup key:
// sha256(sessionID + ":" + kind + ":" + redact(detail)), hex, over the whole
// untruncated detail.
//
// ⚠️ It is NOT the store's ItemIDGenerator (pkg/item-id-generator.go, a random
// 128-bit store id). This id is the join key the readers use.
func EventItemID(sessionID, kind, detail string) string {
	sum := sha256.Sum256([]byte(sessionID + ":" + kind + ":" + redact(detail)))
	return hex.EncodeToString(sum[:])
}
