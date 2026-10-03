// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"sync"
)

//counterfeiter:generate -o ../mocks/attention-change-notifier.go --fake-name AttentionChangeNotifier . AttentionChangeNotifier

// AttentionChangeNotifier signals that the attention store changed, so a live
// view can re-read rather than poll for the difference.
//
// It exists because neither the store nor libkv can report a change: the store
// declares no watch hook, and github.com/bborbe/kv v1.21.16 exposes no
// subscribe API at all. Change detection is therefore added in process, on the
// write path, rather than subscribed to.
//
// It carries no payload. A subscriber is told only that something changed and
// re-reads the store for what that was — so the notifier cannot disagree with
// the store, and a coalesced or dropped signal costs a read rather than a
// wrong render.
//
// ⚠️ Generation is the one exception, and it is not a payload. It counts the
// calls to Notify rather than describing what changed, so it cannot disagree
// with the store either. It exists so a renderer shared by several subscribers
// can tell "I have already rendered for the change you are waking for" from
// "not yet": two subscribers woken by the same write read the same generation
// and the second reuses the first's render instead of repeating it. Without it
// there is no way to distinguish that case from a wake whose render is still
// owed, and every subscriber would render once per change — which is the cost
// this exists to remove.
type AttentionChangeNotifier interface {
	// Notify signals every current subscriber that the store changed. It never
	// blocks and never fails: a subscriber that has not yet drained its signal
	// is left with the one it already holds, because it re-reads the store on
	// waking and so cannot miss a change by being signalled once for two
	// writes.
	Notify()

	// Subscribe returns the channel this subscriber will be signalled on and a
	// function that unregisters it and closes that channel. The caller must
	// call the returned function when it stops reading; the channel is closed
	// by the notifier, never by the subscriber, so a subscriber that ranges
	// over it sees the close rather than a panic.
	//
	// The channel is buffered, so Notify never waits on a slow subscriber.
	Subscribe() (<-chan struct{}, func())

	// Generation returns the number of Notify calls made so far. It is read by
	// a subscriber after it wakes, to tell a change already rendered for from
	// one still owed — see the type doc. It is monotonic and never resets.
	Generation() uint64
}

// NewAttentionChangeNotifier creates an in-process notifier. It holds no
// state beyond its subscriber set, so it is safe to create one per store.
func NewAttentionChangeNotifier() AttentionChangeNotifier {
	return &attentionChangeNotifier{
		subscribers: map[int]chan struct{}{},
	}
}

type attentionChangeNotifier struct {
	mutex       sync.Mutex
	subscribers map[int]chan struct{}
	nextID      int
	generation  uint64
}

// Notify signals every subscriber without waiting for any of them. The send is
// non-blocking and the channel is buffered, so a subscriber that is mid-render
// coalesces two writes into one wake-up rather than stalling the writer.
//
// The mutex is held across the sends so a concurrent unsubscribe cannot close a
// channel this loop is still sending on.
func (n *attentionChangeNotifier) Notify() {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	// ⚠️ Bumped once per call, before the sends, and deliberately not once per
	// subscriber: the generation identifies the WRITE, and every subscriber
	// woken by this call must read the same value or the shared renderer would
	// see them as separate changes and render once for each.
	n.generation++
	for _, changes := range n.subscribers {
		select {
		case changes <- struct{}{}:
		default:
			// The subscriber already holds an unread signal. It re-reads the
			// store when it wakes, so one signal covers both writes.
		}
	}
}

// Subscribe registers a subscriber. The returned function unregisters it and
// closes its channel; it is idempotent, so a caller that defers it and also
// calls it on an error path does not panic on a double close.
func (n *attentionChangeNotifier) Subscribe() (<-chan struct{}, func()) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	id := n.nextID
	n.nextID++
	changes := make(chan struct{}, 1)
	n.subscribers[id] = changes
	return changes, func() {
		n.mutex.Lock()
		defer n.mutex.Unlock()
		subscribed, ok := n.subscribers[id]
		if !ok {
			return
		}
		delete(n.subscribers, id)
		close(subscribed)
	}
}

// Generation returns the number of Notify calls made so far. It takes the same
// mutex the sends do, so a value read after waking is at least the write that
// woke the reader — the increment happens before the sends, and a subscriber
// cannot receive its signal before that.
func (n *attentionChangeNotifier) Generation() uint64 {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	return n.generation
}
