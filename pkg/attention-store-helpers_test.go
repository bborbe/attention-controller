// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	libkv "github.com/bborbe/kv"
)

// Session ids for the actors these specs name. § Escalation requires a session
// id to be a dashed UUID — the store refuses anything else rather than
// normalizing it — so a readable fixture such as "session-manager" cannot be
// handed to Escalate. The actor names live in the constant names instead, and
// the values are shaped like the ids the live store actually carries.
//
// Only the escalation path enforces this. `resolved_by` is the same kind of
// value but § Resolution states no rejection rule for it, so those specs keep
// their plain names — the asymmetry is the schema's, not an oversight here.
const (
	manager1SessionID     = "00000000-0000-4000-8000-000000000001"
	manager2SessionID     = "00000000-0000-4000-8000-000000000002"
	managerSessionID      = "00000000-0000-4000-8000-000000000003"
	fleetManagerSessionID = "00000000-0000-4000-8000-000000000004"
	topicManagerSessionID = "00000000-0000-4000-8000-000000000005"
)

// countingDB counts the write transactions opened against a real libkv.DB, so a
// spec can assert the read path's transaction shape rather than its timing. The
// embedded DB supplies every other method.
type countingDB struct {
	libkv.DB
	updates int
}

func (c *countingDB) Update(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	c.updates++
	return c.DB.Update(ctx, fn)
}
