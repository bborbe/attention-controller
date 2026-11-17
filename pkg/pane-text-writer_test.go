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

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("WeztermPaneTextWriter", func() {
	It(
		"bounds get-text with paneTextTimeout even when the caller's context carries no deadline",
		func() {
			// ⚠️ This is the only spec that pins paneTextTimeout's call-site wiring.
			// A spec asserting the constant's value would pass on a constant wired to
			// nothing, so this drives a real hung child instead: a fake `wezterm` on
			// PATH that outlasts any sane bound.
			//
			// The writer resolves the binary through exec.LookPath over a fixed
			// candidate list, so PATH is the seam — the same seam pane-lister_test.go
			// uses. A ten-second sleep still fails the ceiling below when the
			// WithTimeout is removed, while the bound ends the call at ~2.5 s.
			dir := GinkgoT().TempDir()
			script := filepath.Join(dir, "wezterm")
			Expect(os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0o700)).To(Succeed())

			original := os.Getenv("PATH")
			Expect(os.Setenv("PATH", dir+string(os.PathListSeparator)+original)).To(Succeed())
			DeferCleanup(func() {
				Expect(os.Setenv("PATH", original)).To(Succeed())
			})

			writer := pkg.NewWeztermPaneTextWriter(&mocks.PaneLister{})

			start := time.Now()
			// context.Background() is the point: the caller's deadline-free context is
			// what made the old call unbounded.
			_, ok := writer.Text(context.Background(), 1)
			elapsed := time.Since(start)

			// The read is refused, not returned: a bound that let the hung child's
			// output through would not be a bound.
			Expect(ok).To(BeFalse())
			// The lower bound is the positive control: if the PATH seam stopped
			// resolving, Text returns "wezterm not found" in ~1 ms and both ceilings
			// pass with the WithTimeout deleted.
			Expect(elapsed).To(BeNumerically(">=", 1500*time.Millisecond),
				"the fake wezterm never ran, so nothing was bounded")
			// paneTextTimeout is 2 s and the wait delay 500 ms, so the deterministic
			// worst case is 2.5 s. Anything approaching the fake's 10 s sleep means the
			// declared bound is not the one in force.
			Expect(elapsed).To(BeNumerically("<=", 6*time.Second),
				"paneTextTimeout is not applied at the get-text call site")
		},
	)
})
