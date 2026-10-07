// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("Always-live session liveness", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// ⚠️ The empty id is asserted deliberately, and it is the assertion that
	// separates this checker from the registry-backed one. `sessionLivenessChecker`
	// answers false for the empty id — "no registry entry carries it" — which is
	// right on a host and wrong here: a cluster backend's whole contract is that
	// nothing is dead, and a producer that recorded no session is exactly the
	// item it must not prune.
	It("reports every id live, including the empty one", func() {
		checker := pkg.NewAlwaysLiveSessionLivenessChecker()

		Expect(checker.IsLive(ctx, "some-session")).To(BeTrue())
		Expect(checker.IsLive(ctx, "a-session-this-host-has-never-seen")).To(BeTrue())
		Expect(checker.IsLive(ctx, "")).To(BeTrue())
	})

	// ⚠️ The counterfactual the mode exists for, asserted rather than described:
	// against a registry that is PRESENT BUT EMPTY the registry-backed checker
	// answers "gone" for the same id, so wiring this one is what stands between
	// a cluster backend and a read that sweeps the store. The directory is real
	// and empty on purpose — that is the input shape, not a missing path, which
	// the registry checker already fails open on.
	It("differs from the registry checker on an empty registry", func() {
		emptyRegistry := pkg.NewSessionLivenessChecker(GinkgoT().TempDir())

		Expect(emptyRegistry.IsLive(ctx, "some-session")).To(BeFalse(),
			"a readable-but-empty registry reads every producer as gone")
		Expect(pkg.NewAlwaysLiveSessionLivenessChecker().IsLive(ctx, "some-session")).To(BeTrue())
	})
})
