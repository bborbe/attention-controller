// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"bytes"
	"context"
	"encoding/json"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("SessionLedgerStore ListJSON cache", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.SessionLedgerStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		store = pkg.NewSessionLedgerStore(db)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// encoded is what libhttp.SendJSONResponse writes for the same rows.
	encoded := func() []byte {
		entries, err := store.List(ctx)
		Expect(err).To(BeNil())
		var buf bytes.Buffer
		Expect(json.NewEncoder(&buf).Encode(entries)).To(Succeed())
		return buf.Bytes()
	}

	It("answers the same bytes SendJSONResponse would", func() {
		Expect(store.Post(ctx, ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z"))).To(Succeed())
		Expect(store.Post(ctx, ledgerEntry(ledgerSessionB, "2026-10-13T11:00:00Z"))).To(Succeed())

		body, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())
		Expect(body).To(Equal(encoded()))
	})

	It("answers an empty array, never null, for an empty store", func() {
		body, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())
		Expect(string(body)).To(MatchJSON("[]"))
	})

	It("serves a repeat read from the cache", func() {
		Expect(store.Post(ctx, ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z"))).To(Succeed())

		first, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())
		second, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())
		// Same backing array: the second read did not re-list or re-encode.
		Expect(&second[0]).To(BeIdenticalTo(&first[0]))
	})

	It("drops the cache on Post", func() {
		Expect(store.Post(ctx, ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z"))).To(Succeed())
		_, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())

		Expect(store.Post(ctx, ledgerEntry(ledgerSessionB, "2026-10-13T11:00:00Z"))).To(Succeed())
		body, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())
		Expect(string(body)).To(ContainSubstring(ledgerSessionB))
		Expect(body).To(Equal(encoded()))
	})

	It("drops the cache on Patch", func() {
		Expect(store.Post(ctx, ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z"))).To(Succeed())
		_, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())

		_, found, err := store.Patch(ctx, ledgerSessionA, pkg.SessionLedgerPatch{
			"label": json.RawMessage(`"a patched worker"`),
		})
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		body, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())
		Expect(string(body)).To(ContainSubstring("a patched worker"))
	})

	It("drops the cache on Import", func() {
		_, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())

		added, err := store.Import(ctx, pkg.SessionLedgerEntries{
			ledgerEntry(ledgerSessionC, "2026-10-13T12:00:00Z"),
		})
		Expect(err).To(BeNil())
		Expect(added).To(Equal(1))
		body, err := store.ListJSON(ctx)
		Expect(err).To(BeNil())
		Expect(string(body)).To(ContainSubstring(ledgerSessionC))
	})
})
