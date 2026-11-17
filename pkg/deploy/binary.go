// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"

	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// binaryMode is the mode the installed binary is written with.
const binaryMode = 0o755

// FileBinary is the installed binary and the previous one retained beside it.
type FileBinary struct {
	// Bin is the installed binary's path.
	Bin string
	// Prev is the retained previous binary's path.
	Prev string
}

// Path returns the installed binary's path.
func (b *FileBinary) Path() string {
	return b.Bin
}

// Identity returns the installed binary's own build identity.
func (b *FileBinary) Identity(ctx context.Context) (buildidentity.Identity, error) {
	return readBinaryIdentity(ctx, b.Bin)
}

// HasPrev reports whether a previous binary was retained.
func (b *FileBinary) HasPrev() bool {
	_, err := os.Stat(b.Prev)
	return err == nil
}

// PrevIdentity returns the retained binary's own build identity.
func (b *FileBinary) PrevIdentity(ctx context.Context) (buildidentity.Identity, error) {
	return readBinaryIdentity(ctx, b.Prev)
}

// RestorePrev puts the retained binary back in place.
//
// ⚠️ Written to a sibling path and renamed over the target, never written in
// place. The failed binary's process may still be alive with its image mapped,
// and an in-place write of a running executable is refused or leaves a torn
// file; a rename is atomic and swaps the name launchd resolves on the next
// start.
func (b *FileBinary) RestorePrev(ctx context.Context) error {
	data, err := os.ReadFile(b.Prev)
	if err != nil {
		return errors.Wrapf(ctx, err, "read %s failed", b.Prev)
	}
	// Cleaned so a configured path carrying `..` still yields a sibling, and
	// suppressed because G703's taint analysis cannot see that the value is the
	// deploy's own flag rather than anything an attacker supplies — the same
	// false positive and the same suppression the repo already carries in
	// cmd/import-spawn-ledger.
	// ⚠️ Unique per process, not a fixed `<bin>.restore`: two failed deploys
	// running at once would otherwise rename each other's partial file over the
	// installed binary — the same collision `install-bin`'s staging path fixed.
	tmp := filepath.Clean(fmt.Sprintf("%s.restore.%d", b.Bin, os.Getpid()))
	//nolint:gosec // G703 is a false positive here: b.Bin is the deploy's own -bin flag
	if err := os.WriteFile(tmp, data, binaryMode); err != nil {
		return errors.Wrapf(ctx, err, "write %s failed", tmp)
	}
	if err := os.Chmod(tmp, binaryMode); err != nil {
		return errors.Wrapf(ctx, err, "chmod %s failed", tmp)
	}
	if err := os.Rename(tmp, b.Bin); err != nil {
		return errors.Wrapf(ctx, err, "rename %s to %s failed", tmp, b.Bin)
	}
	return nil
}

// readBinaryIdentity reads a binary's VCS stamp.
func readBinaryIdentity(ctx context.Context, path string) (buildidentity.Identity, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return buildidentity.Identity{}, errors.Wrapf(
			ctx,
			err,
			"read build info of %s failed",
			path,
		)
	}
	return buildidentity.FromSettings(info.Settings), nil
}
