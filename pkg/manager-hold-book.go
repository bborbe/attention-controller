// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"sort"
	"sync"
	"time"
)

// ManagerHold is one gate held off the board while its manager can still
// answer it.
//
// ⚠️ The hold is a DELAY, not a filter: the card is posted when the window
// expires, whether or not the manager engaged. That is the Python watcher's
// contract and it is kept here — a hold that could silently drop a gate would
// trade a slow card for a lost one.
type ManagerHold struct {
	// Record is the event that would otherwise have been pushed.
	Record EventIngestRecord
	// Manager is the session the gate was held for.
	Manager string
	// Reason is why this gate belongs to a manager rather than to the board.
	Reason string
	// HeldAt is when the hold started; the expiry window is measured from it,
	// never from the latest re-read of the same event.
	HeldAt time.Time
}

// ManagerHoldBook holds gates off the board until their window expires.
//
// ⚠️ It is an interface with a mutex-guarded implementation because two jobs
// touch it from different goroutines: the ingest adds a hold, the release arm
// takes one. A map shared without a lock is the shape that loses a gate in the
// window between "expired" and "posted".
type ManagerHoldBook interface {
	// Hold records a gate held for a manager. A repeated hold of the same id is
	// a no-op: the first hold keeps its original clock, so a re-read of the
	// same event cannot extend the window indefinitely.
	Hold(itemID string, hold ManagerHold)
	// Take removes and returns a hold.
	Take(itemID string) (ManagerHold, bool)
	// Expired returns the holds whose window has run out, oldest first.
	Expired(now time.Time, window time.Duration) []ManagerHold
	// Len returns how many gates are currently held.
	Len() int
}

// NewManagerHoldBook creates an empty hold book.
func NewManagerHoldBook() ManagerHoldBook {
	return &managerHoldBook{holds: map[string]ManagerHold{}}
}

type managerHoldBook struct {
	mutex sync.Mutex
	holds map[string]ManagerHold
}

// Hold records a gate held for a manager, keeping the first hold's clock.
func (b *managerHoldBook) Hold(itemID string, hold ManagerHold) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if _, exists := b.holds[itemID]; exists {
		return
	}
	b.holds[itemID] = hold
}

// Take removes and returns a hold.
func (b *managerHoldBook) Take(itemID string) (ManagerHold, bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	hold, exists := b.holds[itemID]
	if !exists {
		return ManagerHold{}, false
	}
	delete(b.holds, itemID)
	return hold, true
}

// Expired returns the holds whose window has run out, oldest first.
//
// ⚠️ It does not remove them: the caller takes each one only after its effect
// landed, so a store failure leaves the hold in the book for the next pass
// rather than dropping the gate.
func (b *managerHoldBook) Expired(now time.Time, window time.Duration) []ManagerHold {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	expired := make([]ManagerHold, 0, len(b.holds))
	for _, hold := range b.holds {
		if now.Sub(hold.HeldAt) >= window {
			expired = append(expired, hold)
		}
	}
	sort.Slice(expired, func(i, j int) bool {
		return expired[i].HeldAt.Before(expired[j].HeldAt)
	})
	return expired
}

// Len returns how many gates are currently held.
func (b *managerHoldBook) Len() int {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return len(b.holds)
}

// Compile-time guard: the book must satisfy its own interface.
var _ ManagerHoldBook = (*managerHoldBook)(nil)
