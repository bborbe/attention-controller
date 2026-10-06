// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	libtime "github.com/bborbe/time"
	"github.com/fsnotify/fsnotify"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// This file covers the one fail-soft path a spec cannot reach from outside the
// package: a watcher that cannot be created at all. `fsnotify.NewWatcher` only
// fails when the process cannot obtain an inotify instance, which cannot be
// produced on demand — so the seam below stands in for it, and the rule that a
// watcher failure logs a warning and leaves the service serving is pinned rather
// than assumed.
//
// It lives in `package pkg` rather than `package pkg_test` because the seam is
// unexported. Both packages compile into the same test binary, so this spec
// registers with the suite the rest of the package runs under.
var _ = Describe("TaskIndexWatcher construction failure", func() {
	It("keeps serving when the watcher cannot be created", func() {
		original := newTaskIndexFSWatcher
		defer func() { newTaskIndexFSWatcher = original }()
		newTaskIndexFSWatcher = func() (*fsnotify.Watcher, error) {
			return nil, stderrors.New("no inotify instances available")
		}

		index := NewTaskIndex(context.Background(), "", libtime.NewCurrentDateTime())
		// The vaultDir is never reached: the watcher fails to construct before it
		// is joined into a path.
		watcher := NewTaskIndexWatcher(index, "vault")

		Expect(watcher.Run(context.Background())).To(Succeed(),
			"a watcher that cannot be created must not take the process down")
	})
})
