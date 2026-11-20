// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// managerScope is the state-file scope key for one manager. sha256 of the
// manager's session id, first 8 hex characters (sha256, not sha1 — see
// managerContentKey; the scope is an opaque filename, never a security
// primitive).
//
// ⚠️ The scope is keyed on the TRACKED SET'S IDENTITY — here the manager's
// session id — never a bare `state.json`. Two managers sharing the default
// state directory is the ordinary case, and one shared file makes each read the
// other's previous set and print a bare `CLEARED`.
func managerScope(manager string) string {
	sum := sha256.Sum256([]byte(manager))
	return hex.EncodeToString(sum[:])[:managerScopeLength]
}

// managerScopeLength is how many hex characters of the digest name a scope.
const managerScopeLength = 8

// managerStatePath returns state-<scope>.json under stateDir.
func managerStatePath(stateDir, manager string) string {
	return filepath.Join(stateDir, "state-"+managerScope(manager)+".json")
}

// managerParkPath returns park-<scope>.json under stateDir.
//
// ⚠️ A SIBLING of the state file, never a wider record inside it: the state file
// is a plain session→key map, and widening it would make every existing file
// unreadable — which the load path answers with a NEW GATE burst.
func managerParkPath(stateDir, manager string) string {
	return filepath.Join(stateDir, "park-"+managerScope(manager)+".json")
}

// managerLoadPrevState reads a state file. ok is false when the file is absent
// or unreadable — the caller warns and treats the pass as a first run.
func managerLoadPrevState(path string) (map[string]string, bool) {
	var prev map[string]string
	if !managerLoadJSON(path, &prev) {
		return nil, false
	}
	if prev == nil {
		prev = map[string]string{}
	}
	return prev, true
}

// managerLoadParkAges reads a park-age file. ok is false when the file is
// absent or unreadable, exactly as managerLoadPrevState.
func managerLoadParkAges(path string) (map[string]managerParkAge, bool) {
	var ages map[string]managerParkAge
	if !managerLoadJSON(path, &ages) {
		return nil, false
	}
	if ages == nil {
		ages = map[string]managerParkAge{}
	}
	return ages, true
}

// managerLoadJSON decodes one file into value, reporting whether it was
// readable AND parseable.
//
// ⚠️ An absent file is an ordinary first run and logs quietly; a file that
// exists but cannot be decoded is a CORRUPT state file and warns, because the
// pass then behaves as a first run and re-emits — a fact the operator should be
// able to see.
func managerLoadJSON(path string, value interface{}) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		if stderrors.Is(err, os.ErrNotExist) {
			glog.V(2).Infof("job=manager-gate action=state_absent path=%s", path)
			return false
		}
		glog.Warningf("job=manager-gate action=state_unreadable path=%s error=%v", path, err)
		return false
	}
	if err := json.Unmarshal(body, value); err != nil {
		glog.Warningf("job=manager-gate action=state_corrupt path=%s error=%v", path, err)
		return false
	}
	return true
}

// managerSaveState writes prev atomically: a sibling .tmp file, then os.Rename.
// A bare open-and-write truncates first and a crash leaves a zero-byte file that
// reads as a first run and answers with a NEW GATE burst.
func managerSaveState(path string, prev map[string]string) error {
	return managerWriteAtomic(path, prev)
}

// managerSaveParkAges is the park-age sibling of managerSaveState, atomic for
// the same reason.
func managerSaveParkAges(path string, ages map[string]managerParkAge) error {
	return managerWriteAtomic(path, ages)
}

// managerWriteAtomic encodes value and replaces path in one step.
func managerWriteAtomic(path string, value interface{}) error {
	body, err := json.Marshal(value)
	if err != nil {
		return errors.Wrap(context.Background(), err, "encode state failed")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errors.Wrap(context.Background(), err, "create state dir failed")
	}
	tmp := path + ".tmp"
	// ⚠️ 0600: the file names session ids, and nothing but this process reads it.
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return errors.Wrap(context.Background(), err, "write state tmp failed")
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Wrap(context.Background(), err, "rename state failed")
	}
	return nil
}
