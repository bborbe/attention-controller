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
})
