// Copyright (c) 2024 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"

	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// testBuildIdentity is the provenance every page spec renders. It is a fixed
// value rather than buildidentity.Read() so the footer is exercised in the
// page's ordinary shape — a page whose footer only ever renders its absent
// state would leave the rendering path untested — and so a spec that asserts on
// it is asserting on a value it chose.
//
// ⚠️ Version and Commit are the same string on purpose: that is what a build
// given no release tag actually reports, so a spec written against a fixture
// where they differ would not describe the deploy this board runs on.
var testBuildIdentity = buildidentity.Identity{
	Version:    "1a2b3c4d5e6f",
	Commit:     "1a2b3c4d5e6f",
	CommitTime: "2026-01-02T03:04:05Z",
}

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate
func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	RunSpecs(t, "Test Suite", suiteConfig, reporterConfig)
}
