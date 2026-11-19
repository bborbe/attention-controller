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

// managerGateDocHeader is the exact header the gate table must carry.
const managerGateDocHeader = "| # | Condition | Verdict | Reason |"

// managerGateDocRows parses the first markdown table whose header is
// managerGateDocHeader into (condition, verdict, reason) triples.
func managerGateDocRows(body string) [][3]string {
	lines := strings.Split(body, "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == managerGateDocHeader {
			start = index
			break
		}
	}
	if start < 0 {
		return nil
	}
	rows := make([][3]string, 0, len(ManagerGateRows))
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			break
		}
		cells := managerGateDocCells(trimmed)
		if len(cells) != 4 || managerGateDocSeparator(cells) {
			continue
		}
		rows = append(rows, [3]string{cells[1], cells[2], cells[3]})
	}
	return rows
}

// managerGateDocCells splits one markdown table row, trimming pipes and spaces.
func managerGateDocCells(line string) []string {
	parts := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

// managerGateDocSeparator reports whether a row is the table's `---` separator.
func managerGateDocSeparator(cells []string) bool {
	for _, cell := range cells {
		if strings.Trim(cell, "-: ") != "" {
			return false
		}
	}
	return true
}

var _ = Describe("manager gate semantics doc", func() {
	// ⚠️ This is a TEST, not a grep and not `test -f`: a stub doc, a copied
	// fragment or a stale doc must fail it.
	It("TestDocTableMatchesCode", func() {
		body, err := os.ReadFile(filepath.Join("..", "docs", "manager-gate-semantics.md"))
		Expect(err).NotTo(HaveOccurred())
		rows := managerGateDocRows(string(body))
		Expect(rows).To(HaveLen(len(ManagerGateRows)))
		for index, want := range ManagerGateRows {
			Expect(rows[index]).To(
				Equal([3]string{want.Condition, want.Verdict, want.Reason}),
				"gate table row %d",
				index,
			)
		}
	})
})
