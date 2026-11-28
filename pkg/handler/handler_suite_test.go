// Copyright (c) 2024 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// readBoardByID serves `GET /api/1.0/attention/board` through the given board
// read handler and returns the decoded entries keyed by item id, so a spec
// asserts on one card's render data rather than on the whole board.
func readBoardByID(httpHandler http.Handler) map[pkg.ItemID]pkg.BoardItem {
	req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention/board", nil)
	resp := httptest.NewRecorder()
	httpHandler.ServeHTTP(resp, req)
	Expect(resp.Code).To(Equal(http.StatusOK), resp.Body.String())
	var decoded []pkg.BoardItem
	Expect(json.Unmarshal(resp.Body.Bytes(), &decoded)).To(Succeed())
	result := make(map[pkg.ItemID]pkg.BoardItem, len(decoded))
	for _, entry := range decoded {
		result[entry.ItemID] = entry
	}
	Expect(result).To(HaveLen(len(decoded)), "duplicate item ids on the board")
	return result
}

// boardEntryOf is readBoardByID narrowed to one item, failing when the board
// does not carry it.
func boardEntryOf(httpHandler http.Handler, itemID pkg.ItemID) pkg.BoardItem {
	entry, ok := readBoardByID(httpHandler)[itemID]
	Expect(ok).To(BeTrue(), "board entry for %s not found", itemID)
	return entry
}

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
