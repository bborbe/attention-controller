// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// This file is the deterministic half of the expiry coverage.
//
// `expiry_test.go` drives a real store, so it can only place deadlines relative
// to the wall clock — which is enough to tell "past" from "future" but cannot
// address the boundary at all. The one instant where the two comparison
// spellings disagree is `deadline == now`, and the whole point of choosing
// `!now.Before(deadline)` over `now.After(deadline)` is that instant. It is
// pinned here, against a `now` this file owns, because a wall-clock spec would
// have to race the microsecond it is testing.
//
// It lives in `package pkg` rather than `package pkg_test` because `isExpired`
// is unexported. Both packages compile into the same test binary, so these
// specs register with the same suite the rest of the package runs under.
var _ = Describe("isExpired", func() {
	now := libtime.DateTimeFromUnixMicro(stdtime.Now().UnixMicro())

	It("treats the boundary instant as expired", func() {
		item := Item{ExpiresAt: &now}

		Expect(isExpired(&item, now)).To(BeTrue(),
			"a deadline of exactly now has passed — this is the instant the two "+
				"comparison spellings disagree on, and the reason isExpired is written "+
				"as !now.Before(deadline) rather than now.After(deadline)")
	})

	It("does not expire a deadline one microsecond ahead", func() {
		ahead := libtime.DateTimeFromUnixMicro(now.Time().Add(stdtime.Microsecond).UnixMicro())
		item := Item{ExpiresAt: &ahead}

		Expect(isExpired(&item, now)).To(BeFalse())
	})

	It("expires a deadline one microsecond behind", func() {
		behind := libtime.DateTimeFromUnixMicro(now.Time().Add(-stdtime.Microsecond).UnixMicro())
		item := Item{ExpiresAt: &behind}

		Expect(isExpired(&item, now)).To(BeTrue())
	})

	It("does not expire an item that declares no deadline", func() {
		// The schema's default, stated as a unit rule rather than inferred from a
		// store round-trip: absent `expires_at` is persistence, never an
		// immediate expiry.
		item := Item{}

		Expect(isExpired(&item, now)).To(BeFalse())
	})
})
