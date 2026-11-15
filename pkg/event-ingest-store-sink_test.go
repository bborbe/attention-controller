// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// recordingIngestStore is a scripted store that records what the sink asked it
// to do.
type recordingIngestStore struct {
	requests []pkg.PushRequest
	board    pkg.Items
	closed   []pkg.ItemID
	pushErr  error
}

func (s *recordingIngestStore) Push(
	_ context.Context,
	request pkg.PushRequest,
) (*pkg.Item, error) {
	if s.pushErr != nil {
		return nil, s.pushErr
	}
	s.requests = append(s.requests, request)
	return &pkg.Item{ItemID: pkg.ItemID("store-1")}, nil
}

func (s *recordingIngestStore) ReadBoard(_ context.Context) (pkg.Items, error) {
	return s.board, nil
}

func (s *recordingIngestStore) Close(
	_ context.Context,
	itemID pkg.ItemID,
	_ string,
	_ *pkg.AnsweredClient,
) (*pkg.Item, error) {
	s.closed = append(s.closed, itemID)
	return &pkg.Item{ItemID: itemID}, nil
}

var _ = Describe("StoreEventSink", func() {
	var ctx context.Context
	var store *recordingIngestStore

	record := func(kind string, detail string, raw string) pkg.EventIngestRecord {
		ts := float64(1700000000)
		return pkg.EventIngestRecord{
			ItemID:    "iid-1",
			SessionID: "sid-1",
			Kind:      kind,
			TS:        &ts,
			Open:      true,
			Detail:    detail,
			Raw:       []byte(raw),
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		store = &recordingIngestStore{}
	})

	It("maps a permission record onto the store schema", func() {
		sink := pkg.NewStoreEventSink(store, nil)

		Expect(sink.Open(ctx, record("permission", "run rm -rf", `{}`))).To(Succeed())

		Expect(store.requests).To(HaveLen(1))
		request := store.requests[0]
		Expect(request.ProducerID).To(Equal(pkg.ProducerID("sid-1")))
		Expect(request.ProducerKind).To(Equal(pkg.SessionProducerKind))
		Expect(request.ProvenanceClass).To(Equal(pkg.ProvenanceClass("hook")))
		Expect(request.LivenessRef).To(Equal(pkg.LivenessRef("session:sid-1")))
		Expect(request.DedupKey).To(Equal(pkg.DedupKey("iid-1")))
		Expect(request.InterruptClass).To(Equal(pkg.InterruptClass("approve")))
		Expect(request.AnswerMechanism).To(Equal(pkg.AnswerMechanism("permission")))
		Expect(request.Payload).To(Equal(pkg.Payload("run rm -rf")))
		Expect(request.AskedAt).NotTo(BeNil())
	})

	It("maps a question record to the message mechanism", func() {
		sink := pkg.NewStoreEventSink(store, nil)

		Expect(sink.Open(ctx, record("question", "which one?", `{}`))).To(Succeed())

		Expect(store.requests[0].InterruptClass).To(Equal(pkg.InterruptClass("pick")))
		Expect(store.requests[0].AnswerMechanism).To(Equal(pkg.AnswerMechanism("message")))
	})

	It("uses the injected producer-kind resolver", func() {
		resolve := func(_ context.Context, _ string) pkg.ProducerKind {
			return pkg.ManagerProducerKind
		}
		sink := pkg.NewStoreEventSink(store, resolve)

		Expect(sink.Open(ctx, record("permission", "d", `{}`))).To(Succeed())

		Expect(store.requests[0].ProducerKind).To(Equal(pkg.ManagerProducerKind))
	})

	It("truncates an oversized payload", func() {
		sink := pkg.NewStoreEventSink(store, nil)

		Expect(sink.Open(ctx, record("permission", strings.Repeat("x", 2500), `{}`))).To(Succeed())

		Expect([]rune(string(store.requests[0].Payload))).To(HaveLen(2000))
	})

	It("forwards the hook's context and options verbatim", func() {
		raw := `{"context":"allow editing the repo","options":[{"label":"Allow","value":"allow"}]}`
		sink := pkg.NewStoreEventSink(store, nil)

		Expect(sink.Open(ctx, record("permission", "d", raw))).To(Succeed())

		Expect(store.requests[0].Context).To(Equal(pkg.ItemContext("allow editing the repo")))
		Expect(store.requests[0].Options).To(HaveLen(1))
		Expect(store.requests[0].Options[0].Label).To(Equal("Allow"))
	})

	It("closes the open item whose dedup key matches", func() {
		store.board = pkg.Items{
			{ItemID: "store-9", DedupKey: pkg.DedupKey("iid-1")},
		}
		sink := pkg.NewStoreEventSink(store, nil)

		Expect(sink.Close(ctx, record("permission", "d", `{}`))).To(Succeed())

		Expect(store.closed).To(Equal([]pkg.ItemID{"store-9"}))
	})

	It("is a no-op when no open item matches", func() {
		store.board = pkg.Items{
			{ItemID: "store-9", DedupKey: pkg.DedupKey("other")},
		}
		sink := pkg.NewStoreEventSink(store, nil)

		Expect(sink.Close(ctx, record("permission", "d", `{}`))).To(Succeed())

		Expect(store.closed).To(BeEmpty())
	})

	It("returns the push error so the ingest can retry", func() {
		store.pushErr = fmt.Errorf("store down")
		sink := pkg.NewStoreEventSink(store, nil)

		Expect(sink.Open(ctx, record("permission", "d", `{}`))).NotTo(Succeed())
	})
})
