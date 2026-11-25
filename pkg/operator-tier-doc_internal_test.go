// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The exact headers the two operator-tier tables must carry.
const (
	operatorTierWordHeader = "| # | Word |"
	operatorTierArmHeader  = "| # | Match | Value |"
)

// operatorTierDocRows parses the markdown table whose header is header and
// returns its data rows as cell slices (the leading `#` column dropped).
func operatorTierDocRows(body string, header string) [][]string {
	lines := strings.Split(body, "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == header {
			start = index
			break
		}
	}
	if start < 0 {
		return nil
	}
	rows := make([][]string, 0)
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			break
		}
		cells := operatorTierDocCells(trimmed)
		if len(cells) < 2 || operatorTierDocSeparator(cells) {
			continue
		}
		rows = append(rows, cells[1:])
	}
	return rows
}

// operatorTierDocCells splits one markdown table row, trimming pipes and spaces.
func operatorTierDocCells(line string) []string {
	parts := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

// operatorTierDocSeparator reports whether a row is the table's `---` separator.
func operatorTierDocSeparator(cells []string) bool {
	for _, cell := range cells {
		if strings.Trim(cell, "-: ") != "" {
			return false
		}
	}
	return true
}

var _ = Describe("operator tier doc", func() {
	// ⚠️ This is a TEST, not a grep and not `test -f`: a stub doc, a copied
	// fragment or a stale doc must fail it.
	It("TestOperatorTierDocMatchesCode", func() {
		body, err := os.ReadFile(filepath.Join("..", "docs", "operator-tier.md"))
		Expect(err).NotTo(HaveOccurred())

		words := operatorTierDocRows(string(body), operatorTierWordHeader)
		Expect(words).To(HaveLen(len(OperatorTierWords)))
		for index, want := range OperatorTierWords {
			Expect(words[index]).To(Equal([]string{want}), "word row %d", index)
		}

		arms := operatorTierDocRows(string(body), operatorTierArmHeader)
		Expect(arms).To(HaveLen(len(OperatorTierArms)))
		for index, want := range OperatorTierArms {
			Expect(arms[index]).To(
				Equal([]string{string(want.Kind), want.Value}),
				"arm row %d",
				index,
			)
		}
	})
})
