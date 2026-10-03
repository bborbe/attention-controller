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
		// never exits is what LookPath resolves to, and the store's own copy of the
		// WezTerm CLI is never reached.
		dir := GinkgoT().TempDir()
		script := filepath.Join(dir, "wezterm")
		Expect(os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o700)).To(Succeed())

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
		// The script sleeps 30 s. At or above that, nothing bounded the call — which
		// is what this spec caught on its first run: with the deadline set but no
		// WaitDelay, `sh` was killed at 3 s while `sleep` held the inherited stdout
		// pipe, so Output blocked for the full 30 s. The bound is now the deadline
		// plus the wait delay, so this leaves room for a loaded machine without ever
		// passing on an unbounded call.
		Expect(elapsed).To(BeNumerically("<", 10*time.Second))
	})
})
