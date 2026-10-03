// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("WeztermPaneLister", func() {
	It("bounds the listing even when the caller's context carries no deadline", func() {
		// ⚠️ paneListingTimeout is the headline half of the 2026-10-03 fix, and this
		// is the only spec that pins it. Every other spec injects a PaneLister fake,
		// so dropping the WithTimeout, moving it above resolveWezterm, or raising the
		// constant would compile and pass CI while reintroducing the outage.
		//
		// The lister resolves a real binary through exec.LookPath over a fixed
		// candidate list, so PATH is the seam: a temp dir holding a `wezterm` that
		// outlasts any sane bound is what LookPath resolves to, and the store's own
		// copy of the WezTerm CLI is never reached.
		dir := GinkgoT().TempDir()
		script := filepath.Join(dir, "wezterm")
		Expect(os.WriteFile(script, []byte("#!/bin/sh\nsleep 20\n"), 0o700)).To(Succeed())

		original := os.Getenv("PATH")
		Expect(os.Setenv("PATH", dir+string(os.PathListSeparator)+original)).To(Succeed())
		DeferCleanup(func() {
			Expect(os.Setenv("PATH", original)).To(Succeed())
		})

		start := time.Now()
		// ⚠️ context.Background() is the point of the spec, not an oversight: it is
		// the deadline-free caller context that made the old call unbounded, so a
		// bound that only holds when the caller supplies its own deadline is not the
		// bound this fix exists to provide.
		_, err := pkg.NewWeztermPaneLister().List(context.Background())
		elapsed := time.Since(start)

		Expect(err).To(HaveOccurred())
		// ⚠️ Two assertions, because they pin different things, and the loose one goes
		// first so a catastrophic regression reports the clearer message.
		//
		// The loose ceiling is what caught this spec's first-run failure: with the
		// deadline set but no WaitDelay, `sh` was killed at 3 s while the `sleep` it
		// spawned held the inherited stdout pipe, and Output blocked for the script's
		// full run. The tight one pins the CONSTANT rather than mere boundedness — the
		// bound is 3 s and the wait delay 500 ms, so anything approaching 5 s means
		// the declared bound is not the one in force, and raising paneListingTimeout
		// to 9 s fails here rather than passing on a loose ceiling.
		// ⚠️ The lower bound is the positive control, and without it this spec can
		// pass vacuously. If the PATH seam ever stops resolving — a change in
		// LookPath, a temp file written without the exec bit, a platform without
		// `#!/bin/sh` — List returns "wezterm not found" in about a millisecond and
		// BOTH upper bounds below pass with the WithTimeout deleted. Requiring the
		// call to have actually waited is what proves the subprocess ran and that the
		// deadline, not the fake failing to start, is what ended it.
		Expect(elapsed).
			To(BeNumerically(">=", 2*time.Second), "the fake lister never ran, so nothing was bounded")
		Expect(elapsed).To(BeNumerically("<", 10*time.Second), "the listing was not bounded at all")
		Expect(elapsed).
			To(BeNumerically("<", 5*time.Second), "the bound in force is larger than the declared one")
	})
})
