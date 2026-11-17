// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// stubWhoNeedsMeReader answers a fixed feed or error, so the handler's contract
// is tested without the join.
type stubWhoNeedsMeReader struct {
	feed pkg.WhoNeedsMeFeed
	err  error
}

func (s stubWhoNeedsMeReader) Read(_ context.Context) (pkg.WhoNeedsMeFeed, error) {
	return s.feed, s.err
}

var _ = Describe("WhoNeedsMeHandler", func() {
	serve := func(target http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/who-needs-me", nil)
		rec := httptest.NewRecorder()
		target.ServeHTTP(rec, req)
		return rec
	}

	It("answers joined JSON with 200 and application/json (AC1)", func() {
		target := handler.NewWhoNeedsMeHandler(stubWhoNeedsMeReader{feed: pkg.WhoNeedsMeFeed{
			Source:  "store",
			Sources: pkg.WhoNeedsMeSources{Store: "ok", Registry: "ok", Panes: "ok"},
			Items: []pkg.WhoNeedsMeRow{
				{ItemID: "store-1", DedupKey: "dedup-1", Kind: "permission"},
			},
		}})
		rec := serve(target)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("application/json"))
		Expect(rec.Body.String()).To(ContainSubstring(`"item_id":"store-1"`))
		Expect(rec.Body.String()).To(ContainSubstring(`"source":"store"`))
	})

	It("answers 500 when the reader fails (AC1)", func() {
		target := handler.NewWhoNeedsMeHandler(stubWhoNeedsMeReader{
			err: errors.New(context.Background(), "read failed"),
		})
		rec := serve(target)
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})
})
