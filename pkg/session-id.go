// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"regexp"

	"github.com/bborbe/errors"
)

// sessionIDPattern is the shape § Escalation requires of a session id: a dashed
// UUID, lowercase.
//
// It is deliberately the same pattern the latency reader uses to sort a value
// into the operator rung or into the malformed-value class beside it. A store
// that accepted a shape the reader calls malformed would let the write through
// and then count its own accepted value as a defect — so the two rules are one
// rule, and this is where it is enforced.
var sessionIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// SessionID names the session that escalated an item to the operator.
//
// The schema says `escalated_by` holds "a session id, never a pane id and never
// a boolean". A pane id is recycled across tab moves and restarts, so a stale
// one resolves to another session's pane rather than failing; a boolean cannot
// answer "is it me", which § Escalation's self-stamp rule requires.
type SessionID string

// Validate reports whether the value is a well-formed session id.
//
// A malformed value is refused rather than repaired: a placeholder such as
// "session-a" has no UUID to normalize to, so "repair" could only mean
// inventing an identity. Refusing surfaces the caller's bug at the call;
// storing it hands the same bug to whoever buckets by level later.
func (s SessionID) Validate(ctx context.Context) error {
	if !sessionIDPattern.MatchString(string(s)) {
		return errors.Wrapf(
			ctx,
			ErrInvalidSessionID,
			"sessionID '%s' is not a well-formed session id",
			s,
		)
	}
	return nil
}

// String returns the session id as a plain string.
func (s SessionID) String() string {
	return string(s)
}
