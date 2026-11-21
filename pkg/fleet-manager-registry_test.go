// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("fleet manager registry", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	write := func(name, body string) {
		Expect(os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)).To(Succeed())
	}

	It("TestFleetManagerRegistryReadsTheBranchAndTheBaseName", func() {
		write("fleet-1.json", `{"branch":"fleet","resolved_at":"2026-11-21T10:00:00+01:00"}`)
		write("mgr-1.json", `{"branch":"goal","subject":"Goal A"}`)

		registrations := pkg.NewFleetManagerRegistry(dir).Registrations(context.Background())
		Expect(registrations).To(HaveLen(1))
		// ⚠️ The session id is the file's BASE NAME. The record does not repeat
		// it, so a reader that took it from the JSON would resolve "" and make
		// every registration unusable.
		Expect(registrations[0].SessionID).To(Equal("fleet-1"))
		Expect(registrations[0].ResolvedAt).To(Equal("2026-11-21T10:00:00+01:00"))
	})

	It("TestFleetManagerRegistryMissingDirectoryIsEmpty", func() {
		Expect(pkg.NewFleetManagerRegistry(filepath.Join(dir, "nope")).
			Registrations(context.Background())).To(BeEmpty())
		Expect(pkg.NewFleetManagerRegistry("").Registrations(context.Background())).To(BeEmpty())
	})

	It("TestFleetManagerRegistrySkipsMalformedAndNonFleet", func() {
		write("bad.json", "{not json")
		write("worker.json", `{"branch":"goal"}`)
		Expect(pkg.NewFleetManagerRegistry(dir).Registrations(context.Background())).To(BeEmpty())
	})
})
