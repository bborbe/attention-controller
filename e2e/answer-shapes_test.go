// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/mxschmitt/playwright-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// The answer each card type records when it is answered on the RENDERED board.
//
// These cases exist because the chain from a board click to a Claude session
// broke at every layer on 2026-09-29, and the board layer was the one nothing
// tested: a two-tab card answered on the live board was stored as skip/skip, a
// permission card had no control at all. The attention watcher keys its
// delivery on exactly these stored shapes — `value` vs `values`, `question` vs
// `tab`, `decision` — so a board that writes the wrong shape reaches no session,
// silently. Each case clicks the page's own controls and then reads the stored
// item back through the API; nothing here calls the answer endpoint directly.

// pushCard pushes a card built from a request the caller shapes, filling in the
// fixture producer and liveness every case shares.
func pushCard(request pkg.PushRequest) string {
	request.ProducerID = pkg.ProducerID(e2eProducerID)
	request.ProducerKind = pkg.SessionProducerKind
	request.LivenessRef = pkg.LivenessRef("session:" + e2eSessionID)
	if request.InterruptClass == "" {
		request.InterruptClass = pkg.InterruptClass("pick")
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
	Expect(decoded.ItemID).NotTo(BeEmpty())
	return decoded.ItemID
}

// storedItem reads an item back by id, whatever its state.
func storedItem(itemID string) pkg.Item {
	resp, err := http.Get(baseURL + "api/1.0/attention/" + itemID)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	var item pkg.Item
	Expect(json.NewDecoder(resp.Body).Decode(&item)).To(Succeed())
	return item
}

// answeredItem waits until the store records the item as answered and returns it.
func answeredItem(itemID string) pkg.Item {
	var item pkg.Item
	Eventually(func() pkg.State {
		item = storedItem(itemID)
		return item.State
	}).Should(Equal(pkg.AnsweredState))
	return item
}

func options(labels ...string) pkg.AnswerOptions {
	result := make(pkg.AnswerOptions, 0, len(labels))
	for _, label := range labels {
		result = append(result, pkg.AnswerOption{Label: label})
	}
	return result
}

// pick clicks the option with this label inside one row.
func pick(page playwright.Page, itemID, label string) {
	Expect(
		page.Locator(rowSelector(itemID) + ` input[data-option="` + label + `"]:visible`).Check(),
	).
		To(Succeed())
}

func click(page playwright.Page, itemID, selector string) {
	Expect(page.Locator(rowSelector(itemID) + " " + selector).Click()).To(Succeed())
}

var _ = Describe("an answer given on the rendered board", func() {
	var page playwright.Page

	BeforeEach(func() {
		page = newPage("")
	})

	AfterEach(func() {
		_ = page.Close()
	})

	It("stores a single-question radio pick as option + value", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-radio",
			Payload:         "e2e: pick a pet",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Cat", "Dog"),
		})
		page = newPage("")
		pick(page, itemID, "Dog")
		click(page, itemID, "button.next")

		item := answeredItem(itemID)
		Expect(item.Answer).NotTo(BeNil())
		Expect(item.Answer.Kind).To(Equal(pkg.OptionAnswerKind))
		Expect(string(item.Answer.Value)).To(Equal("Dog"))
	})

	It("stores a single-question multi-select as option + values", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:          "shape-multi",
			Payload:           "e2e: pick toppings",
			AnswerMechanism:   pkg.MessageAnswerMechanism,
			AnswerCardinality: pkg.MultipleAnswerCardinality,
			Options:           options("Cheese", "Ham", "Olives"),
		})
		page = newPage("")
		pick(page, itemID, "Cheese")
		pick(page, itemID, "Olives")
		click(page, itemID, "button.next")

		item := answeredItem(itemID)
		Expect(item.Answer).NotTo(BeNil())
		Expect(item.Answer.Kind).To(Equal(pkg.OptionAnswerKind))
		Expect(item.Answer.Values).To(ConsistOf("Cheese", "Olives"))
	})

	It("stores free text typed in Other as text", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-text",
			Payload:         "e2e: a name",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Anna", "Bob"),
		})
		page = newPage("")
		Expect(
			page.Locator(rowSelector(itemID) + " input[name=text]:visible").Fill("Zoe"),
		).To(Succeed())
		click(page, itemID, "button.next")

		item := answeredItem(itemID)
		Expect(item.Answer).NotTo(BeNil())
		Expect(item.Answer.Kind).To(Equal(pkg.TextAnswerKind))
		Expect(string(item.Answer.Value)).To(Equal("Zoe"))
	})

	// The pick-then-type sequence, which had no coverage at all: the case above
	// types text WITHOUT picking first, so it passes on a board that drops the
	// pick.
	//
	// ⚠️ The stored answer is `text` on BOTH the broken and the fixed build, so a
	// case asserting only the stored answer is green on the unfixed board. The
	// discriminating assertions are the rendered ones, and they come first.
	//
	// ⚠️ Line citations in this file are against MASTER, not against this branch.
	// On master collectAnswers discards the pick at attention-page.go:701-703. On
	// this branch the function has MOVED — it sits below the two delegated
	// listeners, which this work grows — so it is named rather than numbered here:
	// the `data-other` skip and the text-first early-out inside collectAnswers.
	// ⚠️ A branch line number in this file is stale the moment a listener above it
	// gains a line, which is exactly what happened once already.
	It("checks Other and clears the pick when text is typed after one", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-pick-then-type",
			Payload:         "e2e: a name",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Anna", "Bob"),
		})
		page = newPage("")

		pick(page, itemID, "Anna")
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Anna"]:visible`).IsChecked(),
		).To(BeTrue())

		Expect(
			page.Locator(rowSelector(itemID) + " input[name=text]:visible").Fill("Zoe"),
		).To(Succeed())

		// Other is a member of the group, not a field beside it.
		Expect(
			page.Locator(rowSelector(itemID) + " .options input[data-other]:visible").Count(),
		).To(Equal(1))
		// The pick made first is released, and Other carries the selection.
		//
		// ⚠️ Plain Expect, never Eventually, for all three: the `input` listener
		// runs in the same task as the Fill above, so the state has already
		// settled and Eventually buys no tolerance. It costs something though —
		// around a NEGATIVE assertion it passes on a control that was briefly
		// checked and then released, which is exactly the defect being asserted
		// against.
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Anna"]:visible`).IsChecked(),
		).To(BeFalse())
		Expect(
			page.Locator(rowSelector(itemID) + " input[data-other]:visible").IsChecked(),
		).To(BeTrue())

		click(page, itemID, "button.next")

		item := answeredItem(itemID)
		Expect(item.Answer).NotTo(BeNil())
		Expect(item.Answer.Kind).To(Equal(pkg.TextAnswerKind))
		Expect(string(item.Answer.Value)).To(Equal("Zoe"))
	})

	// The multi-pick twin of the case above, and the one path a click reaches
	// that typing never does. On a multi-pick card the Other control is a
	// checkbox, so the operator can check it WITHOUT firing the `input` listener
	// — a click on a checkbox fires `input`, but its target is the control, not
	// the text field, so the listener's class guard returns early. collectAnswers
	// skips the control's own value, so a checked Other with an empty field would
	// read as a pick on screen and be dropped by the submission: the same defect
	// this change removes, one layer down. The control is click-inert instead —
	// the click is prevented and the field takes focus — which also matters on a
	// single-pick card, where the click would otherwise release the earlier pick
	// with nothing to restore it.
	It("refuses a click on Other with no text and keeps the picks already made", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:          "shape-multi-other-click",
			Payload:           "e2e: pick toppings",
			AnswerMechanism:   pkg.MessageAnswerMechanism,
			AnswerCardinality: pkg.MultipleAnswerCardinality,
			Options:           options("Cheese", "Ham", "Olives"),
		})
		page = newPage("")

		pick(page, itemID, "Cheese")
		pick(page, itemID, "Olives")
		// Both targets, because they are not the same test: the caption is the
		// wide one an operator actually aims at, and a click there is a click on
		// the enclosing label rather than on the control.
		// ⚠️ NOT because the two guards differ — an earlier version of this
		// comment claimed a control-keyed guard would let the caption through, and
		// the caption case below records the opposite as measured: label
		// activation forwards a click whose target IS the control, so the narrow
		// guard caught it too. Clicking both is about covering the target the
		// operator reaches for, not about two guards.
		click(page, itemID, "input[data-other]")
		click(page, itemID, ".option-other .option-label")

		// The click neither checks Other nor releases the picks already made.
		Expect(
			page.Locator(rowSelector(itemID) + " input[data-other]:visible").IsChecked(),
		).To(BeFalse())
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Cheese"]:visible`).IsChecked(),
		).To(BeTrue())

		click(page, itemID, "button.next")

		item := answeredItem(itemID)
		Expect(item.Answer).NotTo(BeNil())
		Expect(item.Answer.Kind).To(Equal(pkg.OptionAnswerKind))
		Expect(item.Answer.Values).To(ConsistOf("Cheese", "Olives"))
	})

	// The caption on a single-pick card, where a missed guard is destructive
	// rather than merely wrong: a radio group releases the previous selection the
	// moment another is checked, and nothing restores it.
	//
	// ⚠️ What this case does NOT do is discriminate against a control-keyed guard.
	// It was written believing it would, and running it against that guard showed
	// otherwise: label activation dispatches a SECOND, forwarded click whose
	// target is the control, so the older guard caught the caption path too. What
	// it does discriminate against is a guard that is absent or wrong — with the
	// listener made inert this case fails, and that is the regression it pins.
	It("refuses a click on the Other caption without releasing the earlier pick", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-caption-single",
			Payload:         "e2e: a name",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Anna", "Bob"),
		})
		page = newPage("")

		pick(page, itemID, "Anna")
		click(page, itemID, ".option-other .option-label")

		// The caption click neither checks Other nor releases the pick already
		// made.
		Expect(
			page.Locator(rowSelector(itemID) + " input[data-other]:visible").IsChecked(),
		).To(BeFalse())
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Anna"]:visible`).IsChecked(),
		).To(BeTrue())
	})

	// The keyboard path to the same control, which a review raised as a possible
	// hole and explicitly did not verify. Arrow-key navigation inside a radio
	// group moves the selection, and whether the browser also fires a `click` for
	// it decides whether a click-only guard covers this at all — so the question
	// is asked here rather than assumed. On a single-pick card a move onto Other
	// would release the earlier pick with nothing to restore it.
	It("refuses a keyboard move onto the Other control on a single-pick card", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-keyboard-single",
			Payload:         "e2e: a name",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Anna", "Bob"),
		})
		page = newPage("")

		pick(page, itemID, "Anna")
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Anna"]:visible`).Focus(),
		).To(Succeed())
		// Anna -> Bob -> Other, the Other control being last in the group.
		Expect(page.Keyboard().Press("ArrowRight")).To(Succeed())
		Expect(page.Keyboard().Press("ArrowRight")).To(Succeed())

		Expect(
			page.Locator(rowSelector(itemID) + " input[data-other]:visible").IsChecked(),
		).To(BeFalse())
		// ⚠️ Anna is asserted UNCHECKED, and that is the point rather than a
		// regression: the first press legitimately moves the selection to Bob, so
		// Anna releasing is the group working. What must not happen is the
		// selection landing on Other — a control whose empty field sends nothing.
		// The second press is refused there, and the selection rests on Bob rather
		// than vanishing, which is why Bob is the positive assertion.
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Anna"]:visible`).IsChecked(),
		).To(BeFalse())
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Bob"]:visible`).IsChecked(),
		).To(BeTrue())
	})

	// The mirror order, which a review found uncovered: type FIRST, then pick a
	// named option. Typing checks Other and clears the other picks; picking one
	// back releases Other but does not touch the field, and collectAnswers reads
	// text ahead of any pick — so unless the text is cleared with the control, the
	// board draws the option just clicked while the submission carries the text
	// the operator had moved on from. Same defect, reached from the other side.
	It("clears the typed text when a named option is picked after typing", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-type-then-pick",
			Payload:         "e2e: a name",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Anna", "Bob"),
		})
		page = newPage("")

		Expect(
			page.Locator(rowSelector(itemID) + " input[name=text]:visible").Fill("Zoe"),
		).To(Succeed())
		Expect(
			page.Locator(rowSelector(itemID) + " input[data-other]:visible").IsChecked(),
		).To(BeTrue())

		pick(page, itemID, "Anna")

		// The pick stands, and the text it displaced is gone — so what the board
		// draws is what the submission will carry.
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Anna"]:visible`).IsChecked(),
		).To(BeTrue())
		Expect(
			page.Locator(rowSelector(itemID) + " input[data-other]:visible").IsChecked(),
		).To(BeFalse())
		Expect(
			page.Locator(rowSelector(itemID) + " input[name=text]:visible").InputValue(),
		).To(BeEmpty())

		click(page, itemID, "button.next")

		item := answeredItem(itemID)
		Expect(item.Answer).NotTo(BeNil())
		Expect(item.Answer.Kind).To(Equal(pkg.OptionAnswerKind))
		Expect(string(item.Answer.Value)).To(Equal("Anna"))
	})

	// The one path where the answer REVERTS rather than switching: emptying the
	// field releases the Other pick, which is the only way back to an unanswered
	// card. Every other case types or types-then-picks; none of them empties.
	It("releases the Other pick when the field is emptied", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-empty-field",
			Payload:         "e2e: a name",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         options("Anna", "Bob"),
		})
		page = newPage("")

		Expect(
			page.Locator(rowSelector(itemID) + " input[name=text]:visible").Fill("Zoe"),
		).To(Succeed())
		Expect(
			page.Locator(rowSelector(itemID) + " input[data-other]:visible").IsChecked(),
		).To(BeTrue())

		Expect(
			page.Locator(rowSelector(itemID) + " input[name=text]:visible").Fill(""),
		).To(Succeed())

		// Released, and nothing takes its place — the card is unanswered again.
		Expect(
			page.Locator(rowSelector(itemID) + " input[data-other]:visible").IsChecked(),
		).To(BeFalse())
		Expect(
			page.Locator(rowSelector(itemID) + ` input[data-option="Anna"]:visible`).IsChecked(),
		).To(BeFalse())

		// And the submission refuses rather than sending an empty answer: no
		// request goes out, so the item never leaves the open state.
		click(page, itemID, "button.next")
		Expect(storedItem(itemID).State).To(Equal(pkg.OpenState))
	})

	It("stores a multi-tab card as one entry per answered question, keyed by question", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-tabs",
			Payload:         "e2e: two questions",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Questions: pkg.Questions{
				{Tab: "Fruit", Payload: "e2e: which fruit", Options: options("Apple", "Cherry")},
				{
					Tab:         "Toppings",
					Payload:     "e2e: which toppings",
					Cardinality: pkg.MultipleAnswerCardinality,
					Options:     options("Cheese", "Ham"),
				},
			},
		})
		page = newPage("")
		pick(page, itemID, "Cherry")
		clickTab(page, itemID, "Toppings")
		pick(page, itemID, "Cheese")
		pick(page, itemID, "Ham")
		click(page, itemID, "button.next")

		item := answeredItem(itemID)
		Expect(item.Answers).To(HaveLen(2))
		byQuestion := map[string]pkg.QuestionAnswer{}
		for _, answer := range item.Answers {
			byQuestion[answer.Question] = answer
		}
		Expect(byQuestion["Fruit"].Kind).To(Equal(pkg.OptionAnswerKind))
		Expect(string(byQuestion["Fruit"].Value)).To(Equal("Cherry"))
		Expect(byQuestion["Toppings"].Values).To(ConsistOf("Cheese", "Ham"))
	})

	It("stores Dismiss as skip on every question, never as an option", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-dismiss",
			Payload:         "e2e: dismiss me",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Questions: pkg.Questions{
				{Tab: "A", Payload: "e2e: a", Options: options("a1", "a2")},
				{Tab: "B", Payload: "e2e: b", Options: options("b1", "b2")},
			},
		})
		page = newPage("")
		click(page, itemID, "button.dismiss")

		item := answeredItem(itemID)
		Expect(item.Answers).To(HaveLen(2))
		for _, answer := range item.Answers {
			Expect(answer.Kind).To(Equal(pkg.SkipAnswerKind))
		}
	})

	It("stores Allow on a permission card as decision allow", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-allow",
			Payload:         "Write: /tmp/e2e-allow",
			InterruptClass:  pkg.InterruptClass("approve"),
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})
		page = newPage("")
		click(page, itemID, `button[data-decision="allow"]`)

		Expect(answeredItem(itemID).Decision).To(Equal(pkg.Decision("allow")))
	})

	It("stores Deny on a permission card as decision deny", func() {
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "shape-deny",
			Payload:         "Write: /tmp/e2e-deny",
			InterruptClass:  pkg.InterruptClass("approve"),
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})
		page = newPage("")
		click(page, itemID, `button[data-decision="deny"]`)

		Expect(answeredItem(itemID).Decision).To(Equal(pkg.Decision("deny")))
	})

	It("renders the store's failure on a lost-race Allow, never the answerFailure object", func() {
		// ⚠️ The pair only survives on a card the store still holds open, so the
		// click has to OUTLIVE the item. The board renders no closed item at all,
		// and an answered one loses its Allow / Deny to the dimmed record, so
		// there is no live page on which this failure can be reached. Blocking
		// the stream is what holds the drawn row in place — the state the board
		// itself names "Not tracking the store - showing the last known state." —
		// and the route must be installed BEFORE the first paint, because the
		// page subscribes to the stream on load.
		itemID := pushCard(pkg.PushRequest{
			DedupKey:        "answer-failure-render",
			Payload:         "e2e: a lost-race Allow renders the store's own message",
			InterruptClass:  pkg.InterruptClass("approve"),
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})

		page, err := browser.NewPage()
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = page.Close() }()
		Expect(page.Route(streamPattern, func(route playwright.Route) {
			_ = route.Abort("failed")
		})).To(Succeed())
		// ?hide=none for the reason the stream cases use it: this case ends on the
		// reload the stale arm schedules, and that reload re-renders the answered
		// card. Under the default filter that row is parked out of the DOM, so
		// the return-to-queue assertion below would find nothing however well the
		// reload had worked.
		_, err = page.Goto(baseURL+"?hide=none", playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateLoad,
		})
		Expect(err).NotTo(HaveOccurred())

		// Positive control: the pair IS on the page, so a missing note below is a
		// render failure rather than a card that never drew a control.
		Eventually(func() int {
			return noteCount(page, itemID, `button[data-decision="allow"]`)
		}).WithTimeout(5 * time.Second).Should(Equal(1))

		// Another arm answers first. This is the lost race the ALREADY_ANSWERED
		// classifier exists for, and it is driven through the API rather than
		// fabricated, so the envelope is one the store really emits.
		post("api/1.0/attention/"+itemID+"/answer", map[string]any{
			"answered_by": "e2e-other-arm",
			"decision":    "allow",
		})

		// The operator's own click now loses that race.
		click(page, itemID, `button[data-decision="allow"]`)

		// ⚠️ Both halves, because either alone passes on the broken build. The
		// ABSENCE is the defect: the decision path handed showCloseNote the
		// answerFailure OBJECT, whose textContent coercion rendered the literal
		// [object Object] for every code and dropped the store's own message with
		// it. The PRESENCE is the fix: the human line that code earns.
		// ⚠️ Captured INSIDE the probe. The stale arm schedules a reload 2.5s
		// after the click — deliberately, because a rejected answer publishes no
		// stream delta so the row would otherwise never be swapped — which makes
		// the note transient by design. A read after the reload would find the
		// reloaded page instead of the thing under test.
		var seen string
		Eventually(func() string {
			seen = noteText(page, itemID)
			return seen
		}).WithTimeout(5 * time.Second).Should(ContainSubstring("This item was already answered."))
		Expect(seen).NotTo(ContainSubstring("[object Object]"))

		// And the return the line promises actually arrives: the reload replaces
		// the stale card with the record of the answer that landed. Asserted
		// separately because the two halves fail independently — a card that
		// renders the line and then keeps a dead Allow / Deny pair under it is
		// the defect the reload exists to prevent.
		Eventually(func() int {
			return noteCount(page, itemID, ".record")
		}).WithTimeout(10 * time.Second).Should(Equal(1))
	})
})

// noteText returns the text of the failure note rendered into an item's row, or
// "" when no note is there yet — which is what makes it usable as an Eventually
// probe. Read as TEXT rather than matched by selector, because the defect under
// test rendered a coerced object: the assertion has to see the characters the
// operator saw.
func noteText(page playwright.Page, itemID string) string {
	text, err := page.Locator(rowSelector(itemID) + " " + failedNoteSelector).First().TextContent()
	if err != nil {
		return ""
	}
	return text
}
