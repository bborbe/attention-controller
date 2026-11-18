// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlogpanes

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Attnlogpanes Suite")
}

var _ = Describe("lister", func() {
	It("parses a pane listing", func() {
		titles, err := parseListing([]byte(`[{"pane_id":1,"title":"one"},` +
			`{"pane_id":2,"title":"two"}]`))
		Expect(err).To(BeNil())
		Expect(titles).To(Equal(map[int]string{1: "one", 2: "two"}))
	})

	It("rejects a malformed listing", func() {
		_, err := parseListing([]byte(`not json`))
		Expect(err).NotTo(BeNil())
	})

	It("fails when no wezterm binary resolves", func() {
		dir := GinkgoT().TempDir()
		GinkgoT().Setenv("PATH", dir)
		_, err := resolveBinary()
		Expect(err).NotTo(BeNil())
	})

	It("reads the listing through the wezterm binary on PATH", func() {
		dir := GinkgoT().TempDir()
		script := "#!/bin/sh\necho '[{\"pane_id\":7,\"title\":\"mine\"}]'\n"
		Expect(os.WriteFile(filepath.Join(dir, "wezterm"), []byte(script), 0o750)).
			To(Succeed())

		GinkgoT().Setenv("PATH", dir)

		titles, err := New().ListPanes()
		Expect(err).To(BeNil())
		Expect(titles).To(Equal(map[int]string{7: "mine"}))
	})

	It("fails when the listing exits non-zero", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(dir, "wezterm"),
			[]byte("#!/bin/sh\nexit 1\n"),
			0o750,
		)).To(Succeed())
		GinkgoT().Setenv("PATH", dir)

		_, err := New().ListPanes()
		Expect(err).NotTo(BeNil())
	})

	It("fails when the listing is not JSON", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(dir, "wezterm"),
			[]byte("#!/bin/sh\necho not-json\n"),
			0o750,
		)).To(Succeed())
		GinkgoT().Setenv("PATH", dir)

		_, err := New().ListPanes()
		Expect(err).NotTo(BeNil())
	})
})
