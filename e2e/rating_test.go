// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build e2e

package e2e

import (
	"time"

	"github.com/mxschmitt/playwright-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// A 👍/👎 click on a card the operator is part-way through answering.
//
// The rating posts to /rate and the store echoes the change over the SSE
// stream, where upsertRow replaces the row's whole outerHTML with the server's
// rendering. The server knows nothing about a pick the operator has not sent, so
// before the fix that echo wiped the radio, the boxes, the typed Other text, the
// flag reason and the chosen tab. Each case marks the live row first and waits
// for the mark to be GONE before reading the controls: that is the proof the
// stream's swap actually ran, so no case can pass on the optimistic paint alone.

const swapMarker = "data-e2e-before-swap"

// markRow tags the live row node, so rowSwapped can tell whether the stream has
// replaced it since.
func markRow(page playwright.Page, itemID string) {
	_, err := page.Evaluate(`(selector) => {
		document.querySelector(selector).setAttribute('`+swapMarker+`', '1');
	}`, rowSelector(itemID))
	Expect(err).NotTo(HaveOccurred())
}

// rowSwapped reports whether the row present now is a different node from the
// one markRow tagged — the swap carries the server's markup, which has no mark.
func rowSwapped(page playwright.Page, itemID string) bool {
	if rowCount(page, itemID) != 1 {
		return false
	}
	count, err := page.Locator(rowSelector(itemID) + "[" + swapMarker + "]").Count()
	Expect(err).NotTo(HaveOccurred())
	return count == 0
}

// awaitSwap waits for the stream to replace the marked row.
func awaitSwap(page playwright.Page, itemID string) {
	Eventually(func() bool { return rowSwapped(page, itemID) }).
		WithTimeout(5 * time.Second).Should(BeTrue())
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

	It("keeps the radio pick and the flag reason across the rating's stream echo", func() {
		itemID := petCard("rate-keeps-radio")
		page = newPage("")
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		pick(page, itemID, "Dog")
		Expect(
			page.Locator(rowSelector(itemID) + " input.flag-reason-input").
				Fill("half-typed reason"),
		).
			To(Succeed())
		markRow(page, itemID)

		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))
		awaitSwap(page, itemID)

		Expect(checked(page, itemID, `input[data-option="Dog"]`)).To(BeTrue())
		Expect(checked(page, itemID, `input[data-option="Cat"]`)).To(BeFalse())
		Expect(inputValue(page, itemID, "input.flag-reason-input")).To(Equal("half-typed reason"))
		Expect(ratedButton(page, itemID)).To(Equal("up"))
	})

	It("keeps text typed in Other, and Other's own pick, across the rating's stream echo", func() {
		itemID := petCard("rate-keeps-other")
		page = newPage("")
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		Expect(page.Locator(rowSelector(itemID) + " input.other").Fill("a parrot")).To(Succeed())
		Expect(checked(page, itemID, "input[data-other]")).To(BeTrue())
		markRow(page, itemID)

		rate(page, itemID, pkg.DownRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.DownRating))
		awaitSwap(page, itemID)

		Expect(inputValue(page, itemID, "input.other")).To(Equal("a parrot"))
		Expect(checked(page, itemID, "input[data-other]")).To(BeTrue())
	})

	It("keeps every checked box on a multi-pick card across the rating's stream echo", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:          "rate-keeps-multi",
			Payload:           "e2e: pick toppings, then rate",
			AnswerMechanism:   pkg.MessageAnswerMechanism,
			AnswerCardinality: pkg.MultipleAnswerCardinality,
			Options:           options("Cheese", "Ham", "Olives"),
		})
		page = newPage("")
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		pick(page, itemID, "Cheese")
		pick(page, itemID, "Olives")
		markRow(page, itemID)

		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))
		awaitSwap(page, itemID)

		Expect(checked(page, itemID, `input[data-option="Cheese"]`)).To(BeTrue())
		Expect(checked(page, itemID, `input[data-option="Ham"]`)).To(BeFalse())
		Expect(checked(page, itemID, `input[data-option="Olives"]`)).To(BeTrue())
	})

	It("keeps the open tab and its pick on a two-tab card across the rating's stream echo", func() {
		itemID := pushQuestions("e2e: two questions, then rate", "rate-keeps-tab")
		page = newPage("")
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))
		Expect(activeTab(page, itemID)).To(Equal("Alpha"))

		clickTab(page, itemID, "Beta")
		Expect(activeTab(page, itemID)).To(Equal("Beta"))
		pick(page, itemID, "b2")
		markRow(page, itemID)

		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))
		awaitSwap(page, itemID)

		Expect(activeTab(page, itemID)).To(Equal("Beta"))
		Expect(checked(page, itemID, `input[data-option="b2"]`)).To(BeTrue())

		// Close the card: the suite shares one store, and the empty-board cases
		// answer every open item through the single-answer path, which a
		// question-carrying item refuses.
		click(page, itemID, "button[value=skip]")
		Eventually(func() int { return rowCount(page, itemID) }).
			WithTimeout(5 * time.Second).Should(Equal(0))
	})

	It("still applies a later stream update to a card the operator has touched", func() {
		itemID := petCard("rate-touched-row-updates")
		page = newPage("")
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		pick(page, itemID, "Cat")
		rate(page, itemID, pkg.UpRating)
		Eventually(func() pkg.Rating { return storedRating(itemID) }).Should(Equal(pkg.UpRating))

		// A second client switches the rating; the page learns of it only by stream.
		markRow(page, itemID)
		rateThroughAPI(itemID, pkg.DownRating)
		awaitSwap(page, itemID)

		Eventually(func() string { return ratedButton(page, itemID) }).Should(Equal("down"))
		Expect(checked(page, itemID, `input[data-option="Cat"]`)).To(BeTrue())
	})

	It("still applies a stream update to a card the operator has not touched", func() {
		touched := petCard("rate-untouched-a")
		untouched := pushCard(pkg.PushRequest{
			DedupKey:        "rate-untouched-b",
			Payload:         "e2e: the card nobody touches",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Yes", "No"),
		})
		page = newPage("")
		Eventually(func() int { return rowCount(page, untouched) }).Should(Equal(1))
		pick(page, touched, "Dog")

		markRow(page, untouched)
		rateThroughAPI(untouched, pkg.UpRating)
		awaitSwap(page, untouched)

		Eventually(func() string { return ratedButton(page, untouched) }).Should(Equal("up"))
		Expect(checked(page, untouched, `input[data-option="Yes"]`)).To(BeFalse())
		Expect(checked(page, untouched, `input[data-option="No"]`)).To(BeFalse())
		Expect(checked(page, touched, `input[data-option="Dog"]`)).To(BeTrue())
	})

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
		markRow(page, itemID)

		request.Payload = "e2e: the reworded question"
		Expect(pushCard(request)).To(Equal(itemID), "the re-push must update in place")
		awaitSwap(page, itemID)

		Expect(checked(page, itemID, `input[data-option="Dog"]`)).To(BeFalse())
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
