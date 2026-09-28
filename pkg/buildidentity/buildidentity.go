// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package buildidentity reports which source a running binary was built from.
//
// It reads the stamp the Go toolchain embeds from git — runtime/debug's
// `vcs.revision`, `vcs.time` and `vcs.modified` — and nothing else: not the
// filesystem, not a subprocess, not an environment variable read at render
// time. That restriction is the whole point. A value read from the checkout
// answers "which commit is the checkout at", which is a different question from
// "which build is running", and it is the wrong answer to it whenever the two
// have diverged — the failure this package exists to remove.
//
// ⚠️ The toolchain emits the stamp only for a PACKAGE build. `go build main.go`
// builds a file list rather than a package and suppresses it entirely, so that
// form produces a binary that cannot identify itself however this package is
// written. See the repo's CLAUDE.md § Deploy for the recipe that keeps it.
package buildidentity

import (
	"runtime/debug"
)

// shortRevisionLength is how much of a revision the board shows. Twelve hex
// characters is unambiguous within a repository and still readable at the
// footer's type size; all forty would be the widest thing on the page and a
// value nobody reads by eye.
const shortRevisionLength = 12

// Identity is what a binary can say about its own provenance.
//
// ⚠️ Every field is empty on a binary built without a VCS stamp, and that zero
// value is a real state rather than an error to paper over: the board renders
// it as an explicit "no build identity" line, never as a placeholder. A build
// that never received a commit must not render a plausible-looking sha.
type Identity struct {
	// Version is the human-facing identifier: the abbreviated revision, with a
	// `-dirty` suffix when the tree had uncommitted changes at build time.
	//
	// ⚠️ It is derived from the stamp rather than from BUILD_GIT_VERSION on
	// purpose. That arg defaults to `dev`, and the launchd recipe passes
	// nothing, so consulting it would render `dev` on every real deploy — a
	// placeholder wearing the shape of an answer, which is the state this
	// package exists to end. The commit names the build; a version the build
	// was not given is better absent than invented.
	Version string
	// Commit is the abbreviated revision the binary was built from.
	Commit string
	// CommitTime is when that revision was authored, in RFC3339.
	//
	// ⚠️ It is the COMMIT's time, not the build's — the toolchain stamps no
	// build time, so a caller must label it as a commit time. Rendering it
	// under a "built" label would put a cheap signal where an expensive
	// question is asked, which is the same mistake as reading the file's mtime
	// and calling it the build's identity.
	CommitTime string
}

// Known reports whether the binary carries a build identity. False means it was
// built without a VCS stamp — a file-list build, `-buildvcs=false`, or a build
// outside a git checkout.
func (i Identity) Known() bool {
	return i.Commit != ""
}

// Read returns the identity this binary carries in its own build info.
//
// ⚠️ Deliberately not named New*: it derives a value from the running process
// rather than constructing one from injected dependencies, and a New prefix
// would promise an argument list it does not take. The constructors this
// repo's naming convention is about live in pkg/factory.
func Read() Identity {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Identity{}
	}
	return FromSettings(info.Settings)
}

// FromSettings derives an Identity from a build's settings, the `vcs.*` entries
// the toolchain writes into runtime/debug's BuildInfo.
//
// It is exported and split out of Read so the derivation is testable against a
// chosen set of settings. A spec that went through Read would be asserting on
// whatever the test binary happened to be built from — a value the spec does
// not control and which changes with the toolchain's stamping behaviour, so it
// would pass or fail for reasons unrelated to this function.
//
// ⚠️ Not named New* for the reason given on Read: it is a derivation, not a
// constructor.
func FromSettings(settings []debug.BuildSetting) Identity {
	var revision, commitTime string
	var modified bool
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			commitTime = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	// A stamp with no revision is no stamp. The other two settings are
	// meaningless without it, so they are dropped rather than rendered beside
	// an empty sha.
	if revision == "" {
		return Identity{}
	}
	short := revision
	if len(short) > shortRevisionLength {
		short = short[:shortRevisionLength]
	}
	version := short
	if modified {
		// The suffix matches the `--dirty` the canonical build args use, so a
		// binary built from a modified tree is recognisable as one whichever
		// path built it — and so a reader is told the sha does not fully
		// describe the source it came from.
		version += "-dirty"
	}
	return Identity{
		Version:    version,
		Commit:     short,
		CommitTime: commitTime,
	}
}
