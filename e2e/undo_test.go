// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mxschmitt/playwright-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// pushPermission seeds one permission card and returns the item id the store
// assigned. It is a second pusher rather than a parameter on push because the
// hold only exists on a permission card's Allow / Deny pair — a message card
// renders a form and no decision control — so every case here needs the
// permission mechanism, and a parameter would put a branch in the path every
// other case takes.
func pushPermission(payload, dedupKey string) string {
	request := pkg.PushRequest{
		ProducerID:      pkg.ProducerID(e2eProducerID),
		ProducerKind:    pkg.SessionProducerKind,
		LivenessRef:     pkg.LivenessRef("session:" + e2eSessionID),
		DedupKey:        pkg.DedupKey(dedupKey),
		InterruptClass:  pkg.InterruptClass("pick"),
		Payload:         pkg.Payload(payload),
		AnswerMechanism: pkg.PermissionAnswerMechanism,
	}
	raw, err := json.Marshal(request)
	Expect(err).NotTo(HaveOccurred())
	resp, err := http.Post(baseURL+"api/1.0/attention", "application/json", bytes.NewReader(raw))
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	content, _ := io.ReadAll(resp.Body)
	Expect(resp.StatusCode).To(BeNumerically("<", 300), "push failed: %s", string(content))

	var decoded struct {
		ItemID string `json:"item_id"`
	}
	Expect(json.Unmarshal(content, &decoded)).To(Succeed())
	Expect(decoded.ItemID).NotTo(BeEmpty(), "push returned no item_id: %s", string(content))
	return decoded.ItemID
}

// The held-answer bar. A permission card's Allow / Deny is held for a few
// seconds before the answer reaches the store, and Undo (or U) cancels it. The
// hold is client-side — the schema carries no reopen endpoint — so the only
// proof that Undo delivers nothing is the store's own state plus the absence of
// an answer request. These cases are the behavioural half the Go source specs
// cannot supply: they click the real controls in a real browser.
var _ = Describe("the held answer", func() {
	BeforeEach(func() {
		tts.reset()
	})

	// SC2 and SC1. The bar occupies its fixed height whether or not it is
	// active, so the top card never moves; and Undo within the hold cancels the
	// request before it is sent.
	It("keeps the top card's offsetTop at a 0 px delta and delivers nothing on Undo", func() {
		clicked := pushPermission("e2e: undo case", "e2e-undo")
		// A second card so the board still has a first row to measure once the
		// clicked one leaves it.
		push("e2e: undo anchor", "e2e-undo-anchor")

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, clicked) }).Should(Equal(1))

		var answers int32
		page.OnRequest(func(request playwright.Request) {
			if request.Method() == http.MethodPost && strings.HasSuffix(request.URL(), "/answer") {
				atomic.AddInt32(&answers, 1)
			}
		})

		// Measured with the bar inactive but present: its space is already
		// reserved, so this is the "before it appears" reading the criterion names.
		before := firstRowOffsetTop(page)

		Expect(
			page.Locator(rowSelector(clicked) + " button[data-decision=deny]").Click(),
		).To(Succeed())
		Eventually(func() int { return rowCount(page, clicked) }).Should(Equal(0))
		Eventually(func() bool { return undoBarActive(page) }).Should(BeTrue())

		// 0 px while the bar is showing.
		Expect(firstRowOffsetTop(page)).To(Equal(before))

		// ⚠️ Force, because this click is racing a ~5 s timer. Playwright's normal
		// click waits for actionability (visible, enabled, stable) and retries for
		// up to 30 s; under host load that wait alone outlasted the hold, the bar
		// hid, and the click then failed on an invisible element. The bar is
		// already active and at a fixed position when this line runs, so the
		// actionability wait buys nothing here.
		Expect(page.Locator("[data-undo]").Click(playwright.LocatorClickOptions{
			Force: playwright.Bool(true),
		})).To(Succeed())
		Eventually(func() int { return rowCount(page, clicked) }).Should(Equal(1))
		Eventually(func() bool { return undoBarActive(page) }).Should(BeFalse())

		// 0 px again once it hides.
		Expect(firstRowOffsetTop(page)).To(Equal(before))

		// The card came back PENDING, not merely present: its Allow control is
		// rendered and enabled, so a restored card that had been disabled or
		// dimmed would fail here rather than pass on a bare row count.
		Expect(page.Locator(rowSelector(clicked) + " button[data-decision=allow]").IsEnabled()).
			To(BeTrue())

		// Undo cancels the request, so no answer ever reaches the store — and the
		// item stays open, not merely unrecorded.
		Consistently(func() int32 { return atomic.LoadInt32(&answers) }, "6s", "500ms").
			Should(Equal(int32(0)))
		Expect(getItem(clicked).State).To(Equal(pkg.OpenState))
	})

	// SC4: the `U` shortcut is the keyboard half of Undo. The case above clicks
	// the button; this one proves the keydown handler restores the card and
	// delivers nothing, so a build that wired only the button fails here.
	It("restores the card when U is pressed, not only the Undo button", func() {
		itemID := pushPermission("e2e: undo key case", "e2e-undo-key")
		push("e2e: undo key anchor", "e2e-undo-key-anchor")

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		var answers int32
		page.OnRequest(func(request playwright.Request) {
			if request.Method() == http.MethodPost && strings.HasSuffix(request.URL(), "/answer") {
				atomic.AddInt32(&answers, 1)
			}
		})

		Expect(
			page.Locator(rowSelector(itemID) + " button[data-decision=deny]").Click(),
		).To(Succeed())
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(0))
		Eventually(func() bool { return undoBarActive(page) }).Should(BeTrue())

		Expect(page.Keyboard().Press("u")).To(Succeed())
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))
		Eventually(func() bool { return undoBarActive(page) }).Should(BeFalse())

		Expect(getItem(itemID).State).To(Equal(pkg.OpenState))
		// Observed across the whole hold window, not a slice of it: a delivery
		// that slipped through after the first second would fail here.
		Consistently(func() int32 { return atomic.LoadInt32(&answers) }, "5s", "500ms").
			Should(Equal(int32(0)))
	})

	// SC3 and the expiry half of SC5: the answer is delivered exactly once, and
	// the click-to-delivery delta stays within five seconds.
	It("delivers exactly one answer when the hold expires, within five seconds", func() {
		itemID := pushPermission("e2e: expiry case", "e2e-expiry")

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		var answers int32
		page.OnRequest(func(request playwright.Request) {
			if request.Method() == http.MethodPost && strings.HasSuffix(request.URL(), "/answer") {
				atomic.AddInt32(&answers, 1)
			}
		})

		// Both timestamps are recorded in the page's own clock, at the click and
		// at the fetch, so the delta is the hold itself rather than a poll's
		// granularity. The click listener runs in the capture phase, before the
		// board's own handler.
		_, err := page.Evaluate(`() => {
			window.__undoClickTs = 0;
			window.__undoDeliveryTs = 0;
			document.addEventListener('click', function (event) {
				var target = event.target;
				if (target && target.closest && target.closest('button[data-decision]')) {
					window.__undoClickTs = Date.now();
				}
			}, true);
			var original = window.fetch;
			window.fetch = function (url, options) {
				if (String(url).indexOf('/answer') >= 0) { window.__undoDeliveryTs = Date.now(); }
				return original.apply(this, arguments);
			};
		}`)
		Expect(err).NotTo(HaveOccurred())

		Expect(
			page.Locator(rowSelector(itemID) + " button[data-decision=deny]").Click(),
		).To(Succeed())

		Eventually(func() int32 { return atomic.LoadInt32(&answers) }, "6s", "100ms").
			Should(Equal(int32(1)))

		delta := evalFloat(page, `() => window.__undoDeliveryTs - window.__undoClickTs`)
		// The hold actually held (not delivered on click) and stayed within the
		// five-second budget the criterion sets.
		Expect(delta).To(BeNumerically(">=", 4000))
		Expect(delta).To(BeNumerically("<=", 5000))

		Eventually(func() pkg.State { return getItem(itemID).State }, "5s").
			Should(Equal(pkg.AnsweredState))
		Expect(getItem(itemID).Decision).To(Equal(pkg.DenyDecision))

		// Exactly one: a second delivery would be a double-answer defect.
		Consistently(func() int32 { return atomic.LoadInt32(&answers) }, "1s", "200ms").
			Should(Equal(int32(1)))
	})

	// The flush half of SC5: answering the next card delivers the held answer
	// immediately, so the total delivery count is 2 — not one, and not three.
	It("flushes the held answer when the next card is answered, delivering both", func() {
		first := pushPermission("e2e: flush first", "e2e-flush-1")
		second := pushPermission("e2e: flush second", "e2e-flush-2")

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, first) }).Should(Equal(1))
		Eventually(func() int { return rowCount(page, second) }).Should(Equal(1))

		var answers int32
		page.OnRequest(func(request playwright.Request) {
			if request.Method() == http.MethodPost && strings.HasSuffix(request.URL(), "/answer") {
				atomic.AddInt32(&answers, 1)
			}
		})

		// Hold the first answer.
		Expect(
			page.Locator(rowSelector(first) + " button[data-decision=deny]").Click(),
		).To(Succeed())
		Eventually(func() int { return rowCount(page, first) }).Should(Equal(0))
		Expect(atomic.LoadInt32(&answers)).To(Equal(int32(0)))

		// ⚠️ Wait out the 400 ms decision-suppression window the first click opens.
		// It exists so the next card's pair cannot be answered by the second half
		// of a double-click; a click inside it is swallowed on purpose, so a spec
		// that answered the second card immediately would race the debounce and
		// fail on a fast host rather than test the flush.
		time.Sleep(600 * time.Millisecond)

		// Answering the second card flushes the first immediately, well before the
		// first hold's timer could have expired.
		Expect(
			page.Locator(rowSelector(second) + " button[data-decision=deny]").Click(),
		).To(Succeed())
		Eventually(func() int32 { return atomic.LoadInt32(&answers) }, "2s", "100ms").
			Should(Equal(int32(1)))
		Eventually(func() pkg.State { return getItem(first).State }, "5s").
			Should(Equal(pkg.AnsweredState))

		// The second is still held at this point — exactly one delivered.
		Expect(atomic.LoadInt32(&answers)).To(Equal(int32(1)))

		// Its own expiry delivers it, for a delivery count of 2.
		Eventually(func() int32 { return atomic.LoadInt32(&answers) }, "6s", "100ms").
			Should(Equal(int32(2)))
		Eventually(func() pkg.State { return getItem(second).State }, "5s").
			Should(Equal(pkg.AnsweredState))
	})

	// The hold must not turn a click into silence. Leaving the page inside the
	// window delivers the answer through the pagehide path instead of dropping
	// it, so the asking session is never left waiting on a reply the operator
	// already gave.
	// ⚠️ Navigation rather than page.Close(): Playwright closes a page at the
	// protocol level and skips the unload events, so a close-based case would
	// assert the harness rather than the page. A navigation fires pagehide for
	// the same reason a tab close does.
	It("delivers the held answer when the page is left inside the window", func() {
		itemID := pushPermission("e2e: unload case", "e2e-unload")

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		Expect(
			page.Locator(rowSelector(itemID) + " button[data-decision=deny]").Click(),
		).To(Succeed())
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(0))
		Expect(getItem(itemID).State).To(Equal(pkg.OpenState))

		// Nothing has been delivered yet: the answer is held, not sent.
		_, err := page.Goto("about:blank")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() pkg.State { return getItem(itemID).State }, "6s", "100ms").
			Should(Equal(pkg.AnsweredState))
		Expect(getItem(itemID).Decision).To(Equal(pkg.DenyDecision))
	})

	// The other direction: a stream frame that shows the held item ANSWERED means
	// the store moved on under the hold, so the hold is cancelled rather than left
	// offering an Undo that would restore a card the store no longer has open.
	It("cancels the hold when the store answers the held item from another arm", func() {
		itemID := pushPermission("e2e: answered under hold", "e2e-answered-under-hold")

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		Expect(
			page.Locator(rowSelector(itemID) + " button[data-decision=deny]").Click(),
		).To(Succeed())
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(0))
		Eventually(func() bool { return undoBarActive(page) }).Should(BeTrue())

		// Another arm answers it through the API while the board is holding.
		answer(itemID)

		Eventually(func() bool { return undoBarActive(page) }, "5s").Should(BeFalse())
		Expect(getItem(itemID).State).To(Equal(pkg.AnsweredState))
	})
})

// firstRowOffsetTop reads the offsetTop of the board's first card. It is the
// card the criterion names, and its value is fixed by the board's top edge —
// which is what the bar must not move.
func firstRowOffsetTop(page playwright.Page) float64 {
	return evalFloat(page, `() => {
		var row = document.querySelector('li.item');
		return row ? row.offsetTop : -1;
	}`)
}

// undoBarActive reports whether the held-answer bar is showing. The bar is
// always in the document; only its is-active class says it is visible.
func undoBarActive(page playwright.Page) bool {
	value, err := page.Evaluate(`() => !!document.querySelector('[data-undo-bar].is-active')`)
	Expect(err).NotTo(HaveOccurred())
	active, ok := value.(bool)
	Expect(ok).To(BeTrue(), "the bar's active reading was not a bool: %v", value)
	return active
}

// evalFloat runs a no-argument expression that returns a number.
//
// ⚠️ playwright-go decodes an INTEGRAL JS number to Go int, not float64: its
// value decoder returns `int` whenever math.Ceil(v) == v. Both readings here
// are integral (offsetTop, and the click-to-fetch delta), so a bare
// `value.(float64)` assertion would fail on every call and abort the spec at
// its first measurement — the shape this helper shipped with until the review
// caught it.
func evalFloat(page playwright.Page, expression string) float64 {
	value, err := page.Evaluate(expression)
	Expect(err).NotTo(HaveOccurred())
	switch number := value.(type) {
	case float64:
		return number
	case int:
		return float64(number)
	case int64:
		return float64(number)
	}
	Fail(fmt.Sprintf("the expression did not return a number: %v", value))
	return 0
}
