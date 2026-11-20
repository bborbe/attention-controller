// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// The tag is a payload PREFIX and nothing else. The negative cases are the
// load-bearing half: a card that merely mentions the probes is an operator
// question and must render, so a substring match would silently swallow real
// cards — the failure docs/production-touching-marker.md records for its own
// marker ("list-item form only, never a bare substring").
var _ = Describe("IsSyntheticProbePayload", func() {
	DescribeTable("decides from the payload's opening characters",
		func(payload pkg.Payload, expected bool) {
			Expect(pkg.IsSyntheticProbePayload(payload)).To(Equal(expected))
		},
		Entry(
			"the 2026-11-17 burst's own payload",
			pkg.Payload("SC2 burst probe #218: synthetic dismissal"),
			true,
		),
		Entry("an indented probe payload", pkg.Payload("  SC2 burst probe #1: x"), true),
		Entry(
			"a card that DISCUSSES the probes",
			pkg.Payload("The SC2 burst probe burst filled the board"),
			false,
		),
		Entry("a lowercased tag", pkg.Payload("sc2 burst probe #1"), false),
		Entry("an ordinary card", pkg.Payload("deploy prod?"), false),
		Entry("an empty payload", pkg.Payload(""), false),
	)
})
