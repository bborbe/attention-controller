// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// runnerRegistrations parses every non-test Go file of this package and
// returns the number of `runner.Add(...)` calls and every literal capacity
// passed to `run.NewConcurrentRunner(...)`.
func runnerRegistrations() (int, []int) {
	entries, err := os.ReadDir(".")
	Expect(err).NotTo(HaveOccurred())
	fset := token.NewFileSet()
	adds := 0
	var capacities []int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		Expect(err).NotTo(HaveOccurred())
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case recv.Name == "runner" && sel.Sel.Name == "Add":
				adds++
			case recv.Name == "run" && sel.Sel.Name == "NewConcurrentRunner" && len(call.Args) == 1:
				lit, ok := call.Args[0].(*ast.BasicLit)
				Expect(
					ok,
				).To(BeTrue(), "NewConcurrentRunner capacity must be a literal so this guard can read it")
				value, err := strconv.Atoi(lit.Value)
				Expect(err).NotTo(HaveOccurred())
				capacities = append(capacities, value)
			}
			return true
		})
	}
	return adds, capacities
}

var _ = Describe("background-job runner capacity", func() {
	// ⚠️ The runner buffers `make(chan Func, maxConcurrent)` and `Add` blocks
	// before `Run` drains it, so one registration too many hangs startup with
	// no log line. A host that enables fewer arms starts fine, which is why
	// this counts the call SITES rather than starting the binary: the live
	// board enables arms a test host does not.
	It("exceeds the number of runner.Add sites in the package", func() {
		adds, capacities := runnerRegistrations()
		Expect(adds).To(BeNumerically(">", 0))
		Expect(capacities).To(HaveLen(1))
		Expect(capacities[0]).To(BeNumerically(">", adds),
			"run.NewConcurrentRunner(%d) must exceed the %d runner.Add sites", capacities[0], adds)
	})
})
