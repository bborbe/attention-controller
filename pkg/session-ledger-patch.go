// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/bborbe/errors"
)

// sessionIDField is the one key a patch may not carry.
//
// ⚠️ **The identity is path-owned, and a body copy could only ever disagree with
// it.** `PATCH /api/1.0/session-ledger/{sessionID}` names the row in the URL, so a
// body that also named it would make "which row?" ambiguous the moment the two
// differ — and the safe resolution (trust the path) silently discards a caller's
// input. Refusing it outright is the answer that cannot hide a bug.
const sessionIDField = "session_id"

// SessionLedgerPatch is a partial update to one ledger row: the fields to change,
// and nothing else.
//
// ⚠️ **A map of raw fields rather than a `SessionLedgerEntry`, and that difference
// is the whole feature.** A struct cannot tell a field the caller OMITTED from one
// the caller sent as its zero value, so decoding a patch into a
// `SessionLedgerEntry` would blank every field the patch did not name — which is
// precisely the failure the single-writer guarantee exists to prevent,
// reintroduced one layer down. The three cases the merge has to distinguish are:
//
//	absent  -> the field is left exactly as the owner wrote it
//	`null`  -> the field is cleared
//	a value -> the field is set
//
// `json.RawMessage` is what preserves that: it carries the raw bytes (the `null`
// included) rather than a decoded zero, so the distinction survives decoding.
type SessionLedgerPatch map[string]json.RawMessage

// Apply merges the patch into one row and returns the result.
//
// ⚠️ **It round-trips through the wire shape rather than assigning fields by
// hand.** Twenty-odd hand-written assignments would have to be kept in step with
// the struct by every future field, and whichever one was forgotten would silently
// stop being patchable — a defect that reads as "that field is not supported"
// rather than as a bug. Marshalling the row, overlaying the patch keys, and
// unmarshalling back defines the merge over the same shape the endpoint speaks.
//
// ⚠️ **A `null` DELETES the key rather than storing a null.** Every optional field
// on the row is `omitempty` or a pointer, so dropping the key is what "cleared"
// means on the wire; storing a literal `null` would instead leave a value that
// reads back as present-and-null to a caller checking only for the key's presence.
// The sweep's own `stop_state` reads the two the same way, but a row is served to
// more than that one reader.
func (p SessionLedgerPatch) Apply(
	ctx context.Context,
	current SessionLedgerEntry,
) (SessionLedgerEntry, error) {
	if _, ok := p[sessionIDField]; ok {
		return SessionLedgerEntry{}, errors.Wrapf(
			ctx,
			ErrSessionLedgerPatchInvalid,
			"patch must not carry %q — the row is named by the path",
			sessionIDField,
		)
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return SessionLedgerEntry{}, errors.Wrap(
			ctx,
			err,
			"marshal current session ledger entry failed",
		)
	}
	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return SessionLedgerEntry{}, errors.Wrap(
			ctx,
			err,
			"unmarshal current session ledger entry failed",
		)
	}
	for key, value := range p {
		if isJSONNull(value) {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	encoded, err = json.Marshal(merged)
	if err != nil {
		return SessionLedgerEntry{}, errors.Wrap(
			ctx,
			err,
			"marshal merged session ledger entry failed",
		)
	}
	// ⚠️ `DisallowUnknownFields` is what stops a TYPO from being a silent no-op.
	// The merge above is defined over the wire shape, so a patch key that matches
	// no field on the row would otherwise be copied into the map, discarded by the
	// decode, and reported to the caller as a successful write of a row that did
	// not change. The merged map's other keys all came from the marshalled row and
	// are therefore known, so every unknown key here is the caller's.
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var next SessionLedgerEntry
	if err := decoder.Decode(&next); err != nil {
		return SessionLedgerEntry{}, errors.Wrapf(
			ctx,
			ErrSessionLedgerPatchInvalid,
			"merged row is not a valid session ledger entry: %v",
			err,
		)
	}
	return next, nil
}

// isJSONNull reports whether a raw patch value is the JSON literal `null`.
func isJSONNull(value json.RawMessage) bool {
	return string(bytes.TrimSpace(value)) == "null"
}
