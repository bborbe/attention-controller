// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is an internal test package rather than the external `handler_test`
// the rest of the directory uses, and deliberately so: infoMetaLine is
// unexported and its two single-fact branches are unreachable through the
// served page — a real store.Push always sets both State and CreatedAt — so the
// only way to exercise them is to call the pure function directly. The specs
// register into the same ginkgo suite handler_suite_test.go runs.
package handler

import (
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("infoMetaLine", func() {
	It("returns empty when the item carries neither a state nor a timestamp", func() {
		Expect(infoMetaLine(pkg.Item{})).To(Equal(""))
	})

	It("returns the state alone when the timestamp is zero", func() {
		Expect(infoMetaLine(pkg.Item{State: pkg.OpenState})).To(Equal("open"))
	})

	It("returns the timestamp alone when the state is empty", func() {
		created := libtime.NewDateTime(2026, stdtime.March, 1, 12, 0, 0, 0, stdtime.UTC)
		Expect(infoMetaLine(pkg.Item{CreatedAt: created})).
			To(Equal("2026-03-01T12:00:00Z"))
	})

	It("returns the state and timestamp joined by a spaced hyphen when both are set", func() {
		created := libtime.NewDateTime(2026, stdtime.March, 1, 12, 0, 0, 0, stdtime.UTC)
		Expect(infoMetaLine(pkg.Item{State: pkg.OpenState, CreatedAt: created})).
			To(Equal("open - 2026-03-01T12:00:00Z"))
	})
})

// navigationSessionName is a pure function whose whole job is one comparison,
// so every branch is reachable here — and none of them cheaply through the
// served page: the suppression's end-to-end proof against a served row is the
// sibling integration spec's, and this case exists so the new function has
// tests of its own.
var _ = Describe("navigationSessionName", func() {
	It("returns the name unchanged when there is no task title to compare against", func() {
		Expect(navigationSessionName(pkg.Provenance{SessionName: "Board Polish Session"})).
			To(Equal("Board Polish Session"))
	})

	It("suppresses the name when it repeats the task title", func() {
		Expect(navigationSessionName(pkg.Provenance{
			TaskName:    "Fix the board",
			SessionName: "Fix the board",
		})).To(Equal(""))
	})

	It("returns the name when it differs from the task title", func() {
		Expect(navigationSessionName(pkg.Provenance{
			TaskName:    "Fix the board",
			SessionName: "Board Polish Session",
		})).To(Equal("Board Polish Session"))
	})

	It("returns empty for a provenance carrying neither value", func() {
		Expect(navigationSessionName(pkg.Provenance{})).To(Equal(""))
	})
})
