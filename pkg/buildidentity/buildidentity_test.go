// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildidentity_test

import (
	"runtime/debug"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// The revision every stamped case builds from. Forty hex characters, as git
// writes it, so the shortening is exercised rather than assumed.
const fullRevision = "93b999e4f52fd952bb397090d45b19caec6639bf"

var _ = Describe("FromSettings", func() {
	Context("a stamped build", func() {
		It("reports the abbreviated revision and the commit's own time", func() {
			identity := buildidentity.FromSettings([]debug.BuildSetting{
				{Key: "vcs.revision", Value: fullRevision},
				{Key: "vcs.time", Value: "2026-09-28T17:08:55Z"},
				{Key: "vcs.modified", Value: "false"},
			})

			Expect(identity.Known()).To(BeTrue())
			Expect(identity.Commit).To(Equal("93b999e4f52f"))
			Expect(identity.Version).To(Equal("93b999e4f52f"))
			Expect(identity.CommitTime).To(Equal("2026-09-28T17:08:55Z"))
		})

		It("keeps a revision shorter than the display width whole", func() {
			identity := buildidentity.FromSettings([]debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc123"},
				{Key: "vcs.time", Value: "2026-09-28T17:08:55Z"},
			})

			Expect(identity.Commit).To(Equal("abc123"))
		})
	})

	Context("a build from a modified tree", func() {
		It("marks the version dirty and leaves the commit bare", func() {
			// The suffix belongs on the version alone: it qualifies which source
			// the build represents, while the commit stays the sha that source
			// was based on. A dirty commit would name a revision that does not
			// exist.
			identity := buildidentity.FromSettings([]debug.BuildSetting{
				{Key: "vcs.revision", Value: fullRevision},
				{Key: "vcs.time", Value: "2026-09-28T17:08:55Z"},
				{Key: "vcs.modified", Value: "true"},
			})

			Expect(identity.Version).To(Equal("93b999e4f52f-dirty"))
			Expect(identity.Commit).To(Equal("93b999e4f52f"))
		})
	})

	Context("a build carrying no stamp", func() {
		// ⚠️ This is the negative control the board's criterion turns on: a
		// build that never received a commit must not report a plausible-looking
		// sha, so every field must be empty and Known must be false. Asserting
		// only on Known would leave a version populated and still pass.
		It("reports nothing at all when the settings are empty", func() {
			identity := buildidentity.FromSettings(nil)

			Expect(identity.Known()).To(BeFalse())
			Expect(identity).To(Equal(buildidentity.Identity{}))
		})

		It("reports nothing at all when the revision is missing", func() {
			// A time without a revision is not half an identity: rendering the
			// time beside an empty sha would put a real-looking value where the
			// answer is absent.
			identity := buildidentity.FromSettings([]debug.BuildSetting{
				{Key: "vcs.time", Value: "2026-09-28T17:08:55Z"},
				{Key: "vcs.modified", Value: "false"},
			})

			Expect(identity.Known()).To(BeFalse())
			Expect(identity).To(Equal(buildidentity.Identity{}))
		})

		It("ignores settings that are not vcs ones", func() {
			identity := buildidentity.FromSettings([]debug.BuildSetting{
				{Key: "GOARCH", Value: "arm64"},
				{Key: "GOOS", Value: "darwin"},
				{Key: "-buildmode", Value: "exe"},
			})

			Expect(identity.Known()).To(BeFalse())
		})
	})
})

var _ = Describe("Read", func() {
	It("agrees with the settings the running test binary carries", func() {
		// Read is a thin wrapper over FromSettings; the point of this case is
		// that it reads THIS process rather than a file, so the two can only
		// disagree if Read stops consulting the build info at all.
		info, ok := debug.ReadBuildInfo()
		if !ok {
			Skip("no build info in this test binary")
		}

		Expect(buildidentity.Read()).To(Equal(buildidentity.FromSettings(info.Settings)))
	})
})
