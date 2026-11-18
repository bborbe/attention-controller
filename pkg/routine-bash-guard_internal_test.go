// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Bash credential guard", func() {
	// Review round 3: the `$` scan must stay linear in the command length.
	It("scans a 200 KiB command of `$` in under 200 ms", func() {
		command := strings.Repeat("$", 200*1024)
		cwd := GinkgoT().TempDir()
		done := make(chan time.Duration, 1)
		go func() {
			started := time.Now()
			bashNamesDeniedPath(context.Background(), command, cwd)
			done <- time.Since(started)
		}()
		var elapsed time.Duration
		Eventually(done).WithTimeout(2 * time.Second).Should(Receive(&elapsed))
		Expect(elapsed).To(BeNumerically("<", 200*time.Millisecond))
	})

	It("fails closed when the context is cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		Expect(bashNamesDeniedPath(ctx, "ls -la", GinkgoT().TempDir())).To(BeTrue())
	})
})
