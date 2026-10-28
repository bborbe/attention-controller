// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	stderrors "errors"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

var (
	// ErrItemNotFound is returned when no item carries the requested id.
	ErrItemNotFound = stderrors.New("item not found")

	// ErrIllegalTransition is returned when a move is not a row of the schema's
	// transitions table — it is illegal from every state, not merely from the
	// one the item happens to be in.
	ErrIllegalTransition = stderrors.New("illegal transition")

	// ErrAlreadyAnswered is returned when an answer write loses the
	// compare-and-set: the item was no longer open, so another arm answered it
	// first. The loser reads back and reports; it must not retry and must not
	// route its own answer anyway.
	ErrAlreadyAnswered = stderrors.New("item already answered")

	// ErrAlreadyEscalated is returned when an escalation write loses the
	// compare-and-set: another session stamped the item first. The loser reads
	// back EscalatedBy and reports who holds it; it must not stamp over it,
	// which would erase the only record of who is carrying the item.
	//
	// This is NOT returned when the loser is the session that already stamped
	// the item — a manager re-running its own sweep must never be blocked by
	// its own stamp.
	ErrAlreadyEscalated = stderrors.New("item already escalated")

	// ErrItemNotOpen is returned when escalation is attempted on an item that
	// has already left the queue.
	//
	// It is deliberately not ErrIllegalTransition: escalation is not a
	// transition, and the item's state is untouched by it. The schema says a
	// stamp on a closed item is unreachable because the item has left the
	// queue, so the store refuses rather than stamping an item nothing renders.
	ErrItemNotOpen = stderrors.New("item not open")

	// ErrInvalidSessionID is returned when a value the schema requires to be a
	// session id is not one. The store refuses such a write rather than
	// normalizing it: a placeholder such as "session-a" has no UUID to normalize
	// to, so "repair" could only mean inventing an identity.
	//
	// Observed live before this check existed: `escalated_by` carried the literal
	// "session-a" on two items and a bare 8-hex prefix on a third, against
	// § Escalation's own rule. Those three predate the check and are left in
	// place — they sit on closed items and are the schema page's counterexamples.
	ErrInvalidSessionID = stderrors.New("invalid session id")

	// ErrInvalidLivenessRef is returned when a liveness ref is not one of the
	// three models, or carries no value.
	ErrInvalidLivenessRef = stderrors.New("invalid liveness ref")

	// ErrSessionHeartbeatNotFound is returned when no heartbeat row exists for
	// a session id.
	//
	// ⚠️ It is deliberately NOT used for an unreadable store. "No row" is the
	// answer that renders a session's card Resume, so a read failure must never
	// be able to produce it — the read path reports an unreadable store as a
	// 500 and reserves this sentinel for a file that genuinely is not there.
	ErrSessionHeartbeatNotFound = stderrors.New("session heartbeat not found")

	// ErrPeerUnavailable is returned when a federated call to the peer store
	// fails for a reason this store cannot classify: an unreachable peer, a
	// deadline, or a peer answer no case in remoteResponseError recognises.
	//
	// ⚠️ It exists so the failure reaches the HANDLER still classifiable. Before
	// it, the federating client wrapped such a failure in a sentinel-less error,
	// and every handler mapping — wrapItemError, wrapAnswerError and the
	// escalate switch — sends anything that is not one of their own sentinels to
	// 409 VALIDATION_ERROR. A peer outage therefore reached the client as "your
	// request was malformed", which is the one reading that sends the next
	// caller down the wrong path: it cannot tell a missing item from a broken
	// peer.
	//
	// ⚠️ It is deliberately NOT ErrItemNotFound. A failed peer call is not
	// evidence of absence — the item may exist on the peer and be unreadable —
	// so mapping it to 404 would answer a question the read never settled.
	//
	// ⚠️ It names no route. The client is reached from Get, Close, Answer and
	// Escalate alike and has no idea which one is calling it; a sentinel
	// carrying an HTTP verb would be a layering leak, and the handler that DOES
	// know the route is where a route-bearing message belongs.
	ErrPeerUnavailable = stderrors.New("peer attention store unavailable")

	// ErrSessionLedgerEntryNotFound is the read path's "no such row" sentinel.
	//
	// ⚠️ It is reserved for a row that genuinely is not there, exactly as the
	// heartbeat sentinel beside it is: an unreadable store reports a 500 and
	// must never be able to produce this. The distinction carries more weight
	// here than on the heartbeat path, because the row it answers for is what a
	// manager reads to decide whether a worker exists at all.
	ErrSessionLedgerEntryNotFound = stderrors.New("session ledger entry not found")

	// ErrSessionLedgerPatchInvalid is the write path's "this patch cannot be
	// applied" sentinel, and it is a CLIENT error (400), never a 500.
	//
	// ⚠️ It covers three refusals that would each otherwise be a SILENT NO-OP, and
	// silence is what makes them worth a sentinel of their own:
	//
	//   - a patch naming `session_id`, which the path owns — accepting it would
	//     mean either discarding the caller's value or moving the row;
	//   - a patch naming a field the row does not have, where a typo would
	//     otherwise be dropped by the decode and the unchanged row returned as a
	//     successful write;
	//   - a patch that would leave the row failing its own validation, which is
	//     the one way to store a row `Post` would have refused.
	//
	// ⚠️ It is deliberately NOT ErrSessionLedgerEntryNotFound: a patch against a
	// row that does not exist is `absent` (404), which is a different answer from
	// a patch that is malformed. Folding them would tell a caller its payload was
	// bad when the real answer is that the worker was never spawned.
	ErrSessionLedgerPatchInvalid = stderrors.New("session ledger patch invalid")

	// ErrPermissionCloseWithoutDecision is returned when an arm-caused close is
	// attempted on an open `permission` item.
	//
	// A `permission` item is a gate the operator alone may answer, and the close
	// route's arm-caused write records no decision — it is the acknowledgement of
	// an `ack` item, which routes nothing back. Applying that write to a gate
	// does not clear it, it consumes it: `closed` is terminal, so the write takes
	// the gate's only answer path and the asking session stays parked.
	//
	// ⚠️ It is deliberately NOT ErrIllegalTransition. `open -> closed` is a row
	// of the schema's table, so a caller told "illegal transition" would be told
	// the wrong thing — and could not tell "this transition does not exist" from
	// "this close is not an acceptable way to end a gate".
	ErrPermissionCloseWithoutDecision = stderrors.New("permission close without decision")

	// ErrFlagReasonRequired is returned when a flag carries no usable reason —
	// empty, absent or whitespace-only. The store refuses before writing
	// anything: a flag without a reason is indistinguishable from a grumble and
	// cannot be counted, so the record is left exactly as it was.
	ErrFlagReasonRequired = stderrors.New("flag reason required")

	// ErrMisrouteWeekInvalid is returned when the weekly misroute count is asked
	// for a window that is not an ISO-8601 week in the `YYYYWww` form.
	//
	// ⚠️ It exists so a malformed week is a 400 rather than an empty count. A
	// count of zero is a real answer — a week nobody flagged in — so a fallback
	// on an unparseable value would make a caller's typo indistinguishable from
	// a quiet week, which is the one reading a weekly count must never confuse.
	ErrMisrouteWeekInvalid = stderrors.New("misroute week invalid")
)

// isNotFound reports whether a libkv read failed because the thing asked for
// does not exist — either the key is absent, or the bucket itself has never
// been created.
//
// Both cases are one answer to the caller. A store that has never been written
// to has no bucket at all, and libkv reports that as a bucket error rather than
// a key error: `tx.Bucket()` fails before the key is ever consulted. Treating
// only the key case as not-found would make the very first `Get` against an
// empty store surface a 500 instead of a 404.
func isNotFound(err error) bool {
	return errors.Is(err, libkv.ErrKeyNotFound) || errors.Is(err, libkv.ErrBucketNotFound)
}
