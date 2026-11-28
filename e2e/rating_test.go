// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build e2e

package e2e

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/mxschmitt/playwright-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// A 👍/👎 click on a card the operator is part-way through answering.
//
// The rating posts to /rate and the store echoes the change over the SSE
// stream. The server knows nothing about a pick the operator has not sent, so an
// update that rebuilt the card from the server's data alone would wipe the
// radio, the boxes, the typed Other text, the flag reason and the chosen tab.
//
// ⚠️ The vanilla page swapped the row's DOM node for server HTML carried in the
// stream frame, so these cases used to wait for a marked node to disappear.
// The React board reads no HTML from the frame: each stream event makes it
// re-read GET /api/1.0/attention/board and re-render, and whether React reuses
// or remounts the node is its own implementation detail — so a node marker is
// no signal at all. The proof the update ran is now the update itself: the
// board re-reads that follow the action (for a click, its own refresh plus the
// stream echo's), then the changed content rendering, then a short settle so
// any re-render still in flight has landed before the state is read.

// boardFetchPath is the board read the React page issues on load, after its own
// writes, and on every stream event.
const boardFetchPath = "/api/1.0/attention/board"

// boardFetches counts the page's completed GET /api/1.0/attention/board
// requests, so a case can wait for the refresh a stream event triggers rather
// than for a DOM node swap the React board never performs.
type boardFetches struct {
	n int64
}

// countBoardFetches starts counting one page's completed board reads.
func countBoardFetches(page playwright.Page) *boardFetches {
	fetches := &boardFetches{}
	page.OnRequestFinished(func(request playwright.Request) {
		if request.Method() == "GET" && strings.HasSuffix(
			strings.SplitN(request.URL(), "?", 2)[0], boardFetchPath,
		) {
			atomic.AddInt64(&fetches.n, 1)
		}
	})
	return fetches
}

// count returns how many board reads have completed so far.
func (b *boardFetches) count() int64 {
	return atomic.LoadInt64(&b.n)
}

// await waits until at least n further board reads have completed since a
// count of since.
func (b *boardFetches) await(since int64, n int64) {
	Eventually(b.count).WithTimeout(10*time.Second).
		Should(BeNumerically(">=", since+n), "the board was not re-read after the update")
}

// settle holds a set of assertions true for long enough that every board
// re-render already in flight has landed: the re-reads are counted when their
// response finishes, a moment before React commits them.
func settle(assertions func(g Gomega)) {
	Consistently(assertions).WithTimeout(time.Second).
		WithPolling(100 * time.Millisecond).Should(Succeed())
}

// rate clicks one of the card's own rating buttons.
func rate(page playwright.Page, itemID string, rating pkg.Rating) {
	click(page, itemID, `button[data-rate="`+string(rating)+`"]`)
}

// rateThroughAPI changes a card's rating as a second client would, so the page
// learns of it over the stream only.
func rateThroughAPI(itemID string, rating pkg.Rating) {
	post("api/1.0/attention/"+itemID+"/rate", map[string]string{"rating": string(rating)})
}

// storedRating reads the rating the store holds for an item.
func storedRating(itemID string) pkg.Rating {
	return storedItem(itemID).Rating
}

// ratedButton returns which rating button the row shows pressed, or "".
func ratedButton(page playwright.Page, itemID string) string {
	loc := page.Locator(rowSelector(itemID) + ` button[data-rate][aria-pressed="true"]`)
	count, err := loc.Count()
	Expect(err).NotTo(HaveOccurred())
	if count == 0 {
		return ""
	}
	value, err := loc.First().GetAttribute("data-rate")
	Expect(err).NotTo(HaveOccurred())
	return value
}

// checked reads one input's live checked property.
func checked(page playwright.Page, itemID, selector string) bool {
	value, err := page.Locator(rowSelector(itemID) + " " + selector).IsChecked()
	Expect(err).NotTo(HaveOccurred())
	return value
}

// inputValue reads one input's live value property.
func inputValue(page playwright.Page, itemID, selector string) string {
	value, err := page.Locator(rowSelector(itemID) + " " + selector).InputValue()
	Expect(err).NotTo(HaveOccurred())
	return value
}

func petCard(dedupKey string) string {
	return pushCard(pkg.PushRequest{
		DedupKey:        pkg.DedupKey(dedupKey),
		Payload:         "e2e: pick a pet, then rate",
		AnswerMechanism: pkg.MessageAnswerMechanism,
		Options:         options("Cat", "Dog"),
	})
}

var _ = Describe("a rating click on a card with an answer in progress", func() {
	var page playwright.Page

	AfterEach(func() {
		if page != nil {
			_ = page.Close()
		}
	})

	// Protects: a radio pick and a half-typed flag reason survive the rating's
	// stream echo. The update is proven by the two board re-reads (the click's
	// own refresh and the echo's) and the rendered 👍, not by a node swap.
	It("keeps the radio pick and the flag reason across the rating's stream echo", func() {
		itemID := petCard("rate-keeps-radio")
		page = newPage("")
		fetches := countBoardFetches(page)
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		pick(page, itemID, "Dog")
		Expect(
			page.Locator(rowSelector(itemID) + " input.flag-reason-input").
				Fill("half-typed reason"),
		).
			To(Succeed())

		since := fetches.count()
		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))
		fetches.await(since, 2)
		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal("up"))

		settle(func(g Gomega) {
			g.Expect(checked(page, itemID, `input[data-option="Dog"]`)).To(BeTrue())
			g.Expect(checked(page, itemID, `input[data-option="Cat"]`)).To(BeFalse())
			g.Expect(inputValue(page, itemID, "input.flag-reason-input")).
				To(Equal("half-typed reason"))
			g.Expect(ratedButton(page, itemID)).To(Equal("up"))
		})
	})

	// Protects: text typed in Other, and Other's own pick, survive the rating's
	// stream echo. Same landed-update signal as above; no node swap under React.
	It("keeps text typed in Other, and Other's own pick, across the rating's stream echo", func() {
		itemID := petCard("rate-keeps-other")
		page = newPage("")
		fetches := countBoardFetches(page)
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		Expect(page.Locator(rowSelector(itemID) + " input.other").Fill("a parrot")).To(Succeed())
		Expect(checked(page, itemID, "input[data-other]")).To(BeTrue())

		since := fetches.count()
		rate(page, itemID, pkg.DownRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.DownRating))
		fetches.await(since, 2)
		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal("down"))

		settle(func(g Gomega) {
			g.Expect(inputValue(page, itemID, "input.other")).To(Equal("a parrot"))
			g.Expect(checked(page, itemID, "input[data-other]")).To(BeTrue())
		})
	})

	// Protects: every checked box on a multi-pick card survives the rating's
	// stream echo. The vanilla case waited for the stream to replace the row
	// node; React re-renders from GET /board instead, so the case waits for the
	// two board re-reads and the rendered 👍.
	It("keeps every checked box on a multi-pick card across the rating's stream echo", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:          "rate-keeps-multi",
			Payload:           "e2e: pick toppings, then rate",
			AnswerMechanism:   pkg.MessageAnswerMechanism,
			AnswerCardinality: pkg.MultipleAnswerCardinality,
			Options:           options("Cheese", "Ham", "Olives"),
		})
		page = newPage("")
		fetches := countBoardFetches(page)
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		pick(page, itemID, "Cheese")
		pick(page, itemID, "Olives")

		since := fetches.count()
		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))
		fetches.await(since, 2)
		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal("up"))

		settle(func(g Gomega) {
			g.Expect(checked(page, itemID, `input[data-option="Cheese"]`)).To(BeTrue())
			g.Expect(checked(page, itemID, `input[data-option="Ham"]`)).To(BeFalse())
			g.Expect(checked(page, itemID, `input[data-option="Olives"]`)).To(BeTrue())
		})
	})

	// Protects: the operator's open tab and the pick on it survive the rating's
	// stream echo. React swaps in no server HTML, so the case waits for the two
	// board re-reads and the rendered 👍 rather than a swap marker.
	It("keeps the open tab and its pick on a two-tab card across the rating's stream echo", func() {
		itemID := pushQuestions("e2e: two questions, then rate", "rate-keeps-tab")
		page = newPage("")
		fetches := countBoardFetches(page)
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))
		Expect(activeTab(page, itemID)).To(Equal("Alpha"))

		clickTab(page, itemID, "Beta")
		Expect(activeTab(page, itemID)).To(Equal("Beta"))
		pick(page, itemID, "b2")

		since := fetches.count()
		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))
		fetches.await(since, 2)
		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal("up"))

		settle(func(g Gomega) {
			g.Expect(activeTab(page, itemID)).To(Equal("Beta"))
			g.Expect(checked(page, itemID, `input[data-option="b2"]`)).To(BeTrue())
		})

		// Close the card: the suite shares one store, and the empty-board cases
		// answer every open item through the single-answer path, which a
		// question-carrying item refuses.
		click(page, itemID, "button[value=skip]")
		Eventually(func() int { return rowCount(page, itemID) }).
			WithTimeout(5 * time.Second).Should(Equal(0))
	})

	// Protects: a card the operator has touched still takes a later server
	// update. A second client re-rates through the API, so the page can learn
	// of it only over the stream; the 👎 the board paints can come only from the
	// re-read the stream event triggers, which is the landed-update signal now
	// that React swaps in no server HTML.
	It("still applies a later stream update to a card the operator has touched", func() {
		itemID := petCard("rate-touched-row-updates")
		page = newPage("")
		fetches := countBoardFetches(page)
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		pick(page, itemID, "Cat")
		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))
		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal("up"))

		// A second client switches the rating; the page learns of it only by stream.
		since := fetches.count()
		rateThroughAPI(itemID, pkg.DownRating)
		fetches.await(since, 1)

		Eventually(func() string { return ratedButton(page, itemID) }).
			WithTimeout(5 * time.Second).Should(Equal("down"))
		settle(func(g Gomega) {
			g.Expect(ratedButton(page, itemID)).To(Equal("down"))
			g.Expect(checked(page, itemID, `input[data-option="Cat"]`)).To(BeTrue())
		})
	})

	// Protects: a stream update lands on an untouched card, and leaves both that
	// card's empty answer and a touched sibling's pick alone. The 👍 on the
	// untouched card can come only from the stream-triggered re-read, so it is
	// the landed-update signal; React swaps in no server HTML.
	It("still applies a stream update to a card the operator has not touched", func() {
		touched := petCard("rate-untouched-a")
		untouched := pushCard(pkg.PushRequest{
			DedupKey:        "rate-untouched-b",
			Payload:         "e2e: the card nobody touches",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Yes", "No"),
		})
		page = newPage("")
		fetches := countBoardFetches(page)
		Eventually(func() int { return rowCount(page, untouched) }).Should(Equal(1))
		pick(page, touched, "Dog")
		Expect(ratedButton(page, untouched)).To(Equal(""))

		since := fetches.count()
		rateThroughAPI(untouched, pkg.UpRating)
		fetches.await(since, 1)

		Eventually(func() string { return ratedButton(page, untouched) }).
			WithTimeout(5 * time.Second).Should(Equal("up"))
		settle(func(g Gomega) {
			g.Expect(checked(page, untouched, `input[data-option="Yes"]`)).To(BeFalse())
			g.Expect(checked(page, untouched, `input[data-option="No"]`)).To(BeFalse())
			g.Expect(checked(page, touched, `input[data-option="Dog"]`)).To(BeTrue())
		})
	})

	// Protects: a pick made against the old wording is not silently carried onto
	// a reworded question. The update is proven by the reworded text rendering
	// in the row; React swaps in no server HTML, so a swap marker is no signal.
	It("does not carry a pick across a re-push that rewords the question", func() {
		request := pkg.PushRequest{
			DedupKey:        "rate-reworded-question",
			Payload:         "e2e: the original wording",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Cat", "Dog"),
		}
		itemID := pushCard(request)
		page = newPage("")
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		pick(page, itemID, "Dog")
		Expect(checked(page, itemID, `input[data-option="Dog"]`)).To(BeTrue())

		request.Payload = "e2e: the reworded question"
		Expect(pushCard(request)).To(Equal(itemID), "the re-push must update in place")
		Eventually(func() string { return rowText(page, itemID) }).
			WithTimeout(10 * time.Second).Should(ContainSubstring("the reworded question"))

		settle(func(g Gomega) {
			g.Expect(checked(page, itemID, `input[data-option="Dog"]`)).To(BeFalse())
		})
	})

	It("leaves no sent flag reason behind in the card once the flag is sent", func() {
		itemID := petCard("rate-flag-reason-cleared")
		page = newPage("")
		flagForm := rowSelector(itemID) + " form[data-flag-control]"
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		Expect(
			page.Locator(flagForm + " input[name=reason]").Fill("asked the wrong team"),
		).To(Succeed())
		Expect(page.Locator(flagForm + " button[type=submit]").Click()).To(Succeed())
		Eventually(
			func() string { return getItem(itemID).FlagReason },
		).Should(Equal("asked the wrong team"))
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(0))

		// Reveal the parked card: its flag field must be empty, so a second
		// Flag click cannot re-send the old reason.
		Expect(page.Locator("[data-flag-filter]").Click()).To(Succeed())
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))
		Expect(inputValue(page, itemID, "form[data-flag-control] input[name=reason]")).To(BeEmpty())

		// Leave the shared store as found: unflag, so the card is an ordinary
		// open card the empty-board cases can answer.
		click(page, itemID, "button[data-unflag]")
		Eventually(func() string { return getItem(itemID).FlagReason }).Should(BeEmpty())
	})

	It("rates, survives a reload, clears on a second click and switches on the other", func() {
		itemID := petCard("rate-round-trip")
		page = newPage("")
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))

		_ = page.Close()
		page = newPage("")
		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal("up"))

		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.NoRating))
		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal(""))

		rate(page, itemID, pkg.DownRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.DownRating))
		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))
		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal("up"))
	})
})
