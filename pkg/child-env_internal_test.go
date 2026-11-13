// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("childEnv", func() {
	It("inherits the allowlist and none of this service's credentials", func() {
		GinkgoT().Setenv("PATH", "/usr/bin:/bin")
		GinkgoT().Setenv("HOME", "/Users/example")
		GinkgoT().Setenv("TMPDIR", "/tmp")
		GinkgoT().Setenv("ATTENTION_STORE_TOKEN", "store-secret")
		GinkgoT().Setenv("ATTENTION_UPSTREAM_TOKEN", "upstream-secret")

		env := childEnv("SWEEP_GATE_STATE_DIR=/state")

		Expect(env).To(ContainElement("PATH=/usr/bin:/bin"))
		Expect(env).To(ContainElement("HOME=/Users/example"))
		Expect(env).To(ContainElement("TMPDIR=/tmp"))
		Expect(env).To(ContainElement("SWEEP_GATE_STATE_DIR=/state"))
		for _, pair := range env {
			Expect(pair).NotTo(HavePrefix("ATTENTION_STORE_TOKEN="))
			Expect(pair).NotTo(HavePrefix("ATTENTION_UPSTREAM_TOKEN="))
		}
	})

	It("does not inherit a KUBECONFIG the caller did not pass", func() {
		GinkgoT().Setenv("KUBECONFIG", "/Users/example/.kube/parent")
		env := childEnv()

		for _, pair := range env {
			Expect(pair).NotTo(HavePrefix("KUBECONFIG="))
		}
	})

	It("lets an extra pair override its allowlisted twin instead of duplicating it", func() {
		GinkgoT().Setenv("HOME", "/Users/example")

		env := childEnv("HOME=/elsewhere")

		homes := make([]string, 0, 2)
		for _, pair := range env {
			if strings.HasPrefix(pair, "HOME=") {
				homes = append(homes, pair)
			}
		}
		Expect(homes).To(Equal([]string{"HOME=/elsewhere"}))
	})
})
