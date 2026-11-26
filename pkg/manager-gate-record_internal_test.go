// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("manager gate record line", func() {
	const manager = "64d8cab1-37ac-489a-87a8-d3849c05ab8b"

	It("TestSelfRecordNeverTyped — a manager is never woken about its own gate", func() {
		// The AGED record for the manager's OWN session is what produced the
		// repeating `attention gate: 64d8cab1 (64d8cab1-…): 15 minutes` wake: a
		// session prefix the consumer cannot resolve as an item id, typed back into
		// the pane that owns it.
		line, ok := managerGateRecordLine(manager, ManagerGateRecord{
			Kind:    ManagerGateRecordAged,
			Session: manager,
			Label:   manager,
			Detail:  "15 minutes",
		})
		Expect(ok).To(BeFalse())
		Expect(line).To(BeEmpty())
	})

	It("TestOtherSessionRecordTyped — a worker's gate is still announced", func() {
		line, ok := managerGateRecordLine(manager, ManagerGateRecord{
			Kind:    ManagerGateRecordAged,
			Session: "a1b2c3d4-0000-0000-0000-000000000000",
			Label:   "Vuln Tasks Route to the Dedicated Vuln-Fix Lane",
			Detail:  "15 minutes",
		})
		Expect(ok).To(BeTrue())
		Expect(line).To(Equal(
			"attention gate: a1b2c3d4 (Vuln Tasks Route to the Dedicated Vuln-Fix Lane): 15 minutes",
		))
	})

	It("TestClearedRecordTyped — a worker leaving the set still clears", func() {
		line, ok := managerGateRecordLine(manager, ManagerGateRecord{
			Kind:    ManagerGateRecordCleared,
			Session: "a1b2c3d4-0000-0000-0000-000000000000",
			Label:   "worker",
		})
		Expect(ok).To(BeTrue())
		Expect(line).To(Equal(managerClearedLinePrefix + " a1b2c3d4"))
	})
})
