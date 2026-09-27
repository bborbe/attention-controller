// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"fmt"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// This file is SC1's probe, and it exists to be RED on the pre-fix revision.
//
// It is deliberately NOT a shape assertion. The suite already carries one of
// those (`countingDB`, above), and the parent task's first fix shipped past a
// green shape-spec because counting transactions says nothing about how much
// work the read actually does. This probe counts the thing the defect is
// about: **how many stored items the read decodes**.
//
// The count is taken by wrapping the DB → Tx → Bucket → Iterator → Item chain,
// because `storeTx.Map` calls `item.Value(...)` exactly once per item
// (kv_store-tx.go:172) and that callback is where `json.Unmarshal` runs
// (kv_store-tx.go:174). One `Value()` call is therefore one decode, measured on
// the real path rather than inferred from a bucket size the test also computed.

// decodeCounter counts `Item.Value` invocations reached through a DB.
//
// It is a pointer so one counter can be shared by every wrapper in the chain
// and read from the spec without threading a return value back out.
type decodeCounter struct {
	values int64
}

// decodeCountingDB wraps a real DB so every transaction hands the store a
// wrapped Tx. Both View and Update are wrapped: the read path opens a View for
// the scan and an Update for the prune, and a count that missed the prune's
// decodes would understate the read's cost.
type decodeCountingDB struct {
	libkv.DB
	counter *decodeCounter
}

func (d *decodeCountingDB) View(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	return d.DB.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return fn(ctx, &decodeCountingTx{Tx: tx, counter: d.counter})
	})
}

func (d *decodeCountingDB) Update(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	return d.DB.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return fn(ctx, &decodeCountingTx{Tx: tx, counter: d.counter})
	})
}

type decodeCountingTx struct {
	libkv.Tx
	counter *decodeCounter
}

// The three bucket-returning methods are wrapped; DeleteBucket and
// ListBucketNames return no Bucket and are supplied by the embedded Tx.

func (t *decodeCountingTx) Bucket(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	bucket, err := t.Tx.Bucket(ctx, name)
	if err != nil {
		return nil, err
	}
	return &decodeCountingBucket{Bucket: bucket, counter: t.counter}, nil
}

func (t *decodeCountingTx) CreateBucket(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	bucket, err := t.Tx.CreateBucket(ctx, name)
	if err != nil {
		return nil, err
	}
	return &decodeCountingBucket{Bucket: bucket, counter: t.counter}, nil
}

func (t *decodeCountingTx) CreateBucketIfNotExists(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	bucket, err := t.Tx.CreateBucketIfNotExists(ctx, name)
	if err != nil {
		return nil, err
	}
	return &decodeCountingBucket{Bucket: bucket, counter: t.counter}, nil
}

type decodeCountingBucket struct {
	libkv.Bucket
	counter *decodeCounter
}

func (b *decodeCountingBucket) Get(ctx context.Context, key []byte) (libkv.Item, error) {
	item, err := b.Bucket.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return &decodeCountingItem{Item: item, counter: b.counter}, nil
}

func (b *decodeCountingBucket) Iterator() libkv.Iterator {
	return &decodeCountingIterator{Iterator: b.Bucket.Iterator(), counter: b.counter}
}

func (b *decodeCountingBucket) IteratorReverse() libkv.Iterator {
	return &decodeCountingIterator{Iterator: b.Bucket.IteratorReverse(), counter: b.counter}
}

type decodeCountingIterator struct {
	libkv.Iterator
	counter *decodeCounter
}

func (i *decodeCountingIterator) Item() libkv.Item {
	return &decodeCountingItem{Item: i.Iterator.Item(), counter: i.counter}
}

type decodeCountingItem struct {
	libkv.Item
	counter *decodeCounter
}

// Value is the counted call. `storeTx.Map` reaches it once per item in the
// bucket it scans, and the `json.Unmarshal` inside it is the cost this task
// exists to remove — so this increment IS the decode count.
func (i *decodeCountingItem) Value(fn func(val []byte) error) error {
	i.counter.values++
	return i.Item.Value(fn)
}

var _ = Describe("Read decode cost", func() {
	var ctx context.Context
	var counter *decodeCounter
	var db *decodeCountingDB
	var store pkg.AttentionStore

	// Both buckets hold the SAME open set; only the closed population differs.
	// Declared here rather than beside their first use because a function-body
	// const is scoped from its declaration point, and `decodeCountOf` below
	// needs them.
	const (
		openItemCount = 50
		smallClosed   = 1000
		largeClosed   = 20000
	)

	BeforeEach(func() {
		ctx = context.Background()
		raw, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		counter = &decodeCounter{}
		db = &decodeCountingDB{DB: raw, counter: counter}

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// seedOpen pushes `count` genuinely open items through the real write path,
	// so the read has something to keep and nothing to prune. Every producer is
	// live (the checker returns true unconditionally), which is what makes the
	// dead-item prune a no-op here and keeps this probe about the scan alone.
	seedOpen := func(count int) {
		for i := 0; i < count; i++ {
			_, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      pkg.ProducerID(fmt.Sprintf("session-%d", i)),
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef(fmt.Sprintf("session:session-%d", i)),
				DedupKey:        pkg.DedupKey(fmt.Sprintf("gate-%d", i)),
				InterruptClass:  "approve",
				Payload:         "deploy prod?",
				AnswerMechanism: pkg.MessageAnswerMechanism,
			})
			Expect(err).To(BeNil())
		}
	}

	// seedClosed writes `count` closed items straight into the bucket in ONE
	// transaction. Going through Push/Close would cost two transactions per
	// item, which at 20,000 items is slow enough to make the probe unusable —
	// and it would prove nothing extra, because a closed item is inert to the
	// read: `classifyForRead` returns neither keep nor remove for any state that
	// is not `OpenState` (attention-store-impl.go:196). These items exist only
	// to make the bucket big, which is the whole point of the probe.
	seedClosed := func(offset, count int) {
		err := db.DB.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			bucket, err := tx.CreateBucketIfNotExists(ctx, pkg.AttentionStoreBucketName)
			if err != nil {
				return err
			}
			for i := 0; i < count; i++ {
				id := pkg.ItemID(fmt.Sprintf("closed-%06d", offset+i))
				payload, err := json.Marshal(pkg.Item{
					ItemID: id,
					State:  pkg.ClosedState,
				})
				if err != nil {
					return err
				}
				if err := bucket.Put(ctx, []byte(id.String()), payload); err != nil {
					return err
				}
			}
			return nil
		})
		Expect(err).To(BeNil())
	}

	// decodeCountOf runs one read and returns how many items it decoded.
	decodeCountOf := func() int64 {
		counter.values = 0
		items, err := store.Read(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(openItemCount))
		return counter.values
	}

	// The probe's own validity guard, and it must hold on BOTH revisions.
	//
	// SC6's hole is a probe that is green on both the pre-fix and the fixed
	// revision, which proves nothing. A counter that never fires would be
	// exactly that: every count would read 0, the scaling assertion below would
	// compare 0 to 0, and the suite would pass while measuring nothing. So the
	// count is required to be at least the open set the read returns — the read
	// must decode at least the items it hands back, whatever else it does.
	It("counts decodes at all — the counter cannot silently read zero", func() {
		seedOpen(openItemCount)
		seedClosed(0, smallClosed)

		decodes := decodeCountOf()

		Expect(decodes).To(BeNumerically(">=", openItemCount),
			"the read returned the open items, so it must have decoded at least that many")
	})

	// SC1, and SC6's A/B in one spec. ⚠️ RED on the pre-fix revision: the read
	// decodes the whole bucket, so the two counts differ by roughly the closed
	// delta. GREEN once the cost stops tracking store size.
	It("SC1 — the decode count does not track the closed-item count", func() {
		seedOpen(openItemCount)

		seedClosed(0, smallClosed)
		smallDecodes := decodeCountOf()

		seedClosed(smallClosed, largeClosed-smallClosed)
		largeDecodes := decodeCountOf()

		delta := int64(largeClosed - smallClosed)
		tolerance := float64(delta) * 0.05

		GinkgoWriter.Printf(
			"SC1 raw: open=%d closed=%d decodes=%d | closed=%d decodes=%d | delta=%d tolerance=%.0f\n",
			openItemCount, smallClosed, smallDecodes,
			largeClosed, largeDecodes,
			delta, tolerance,
		)

		Expect(largeDecodes - smallDecodes).To(BeNumerically("<", tolerance),
			"decode count scales with the closed-item count — the read is decoding inert items")
	})
})
