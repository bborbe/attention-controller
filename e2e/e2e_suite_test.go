// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build e2e

// Package e2e drives the attention board in a real browser against a real
// binary.
//
// It is a package of its own rather than a build-tagged file inside
// pkg/handler for a mechanical reason: Ginkgo permits exactly one RunSpecs per
// package, and pkg/handler/handler_suite_test.go already calls it. A tagged
// file in that package would collide the moment `go test -tags e2e` compiled
// both.
//
// The suite is deliberately NOT part of `make precommit` and never runs
// automatically — it is triggered by `make e2e` and, in the release path, by the
// dark-factory scenario that wraps it.
package e2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Attention Board E2E Suite")
}
