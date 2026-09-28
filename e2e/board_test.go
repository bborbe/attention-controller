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
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

const (
	// e2eSessionID is the session the fixture's registry entry carries. The
	// pushed items name it in their liveness_ref, so the store resolves them
	// live and renders them — see writeSessionRegistry for why this must be a
	// temp directory rather than the host's real registry.
	e2eSessionID = "e2e-session"
	// e2eProducerID is the producer every fixture item is pushed under.
	e2eProducerID = "e2e-producer"
	// switchSelector is the board's Hide-answered control. It is a
	// role="switch" button, not a checkbox.
	switchSelector = "button[data-board-filter]"
	// speakSelector is the read-aloud control, present only when -tts-url is set.
	speakSelector = "button[data-speak]"
	// rowSelectorFmt addresses one board row by the item id the store assigned.
	rowSelectorFmt = `li.item[data-item-id=%q]`
	// staleSelector addresses the board's not-tracking state: the span the
	// stream's own onerror unhides. It ships hidden in every response, so a
	// healthy board and a dead one serve the same markup — only the browser's
	// rendered state tells them apart, which is why the case below reads it
	// with IsVisible rather than out of the served source.
	staleSelector = "[data-stream-stale]"
	// streamPattern is the board's live channel, as a route glob. Aborting it
	// is how a case makes the stream fail on purpose; removing the route is how
	// it lets the stream recover.
	streamPattern = "**/api/1.0/attention/stream"
	// speakPattern is one item's read-aloud endpoint, as a route glob.
	// Fulfilling it with an error is how the note cases produce a failure the
	// page has to render, and removing the route is how one of them lets the
	// retry succeed.
	//
	// Read-aloud is the action both note cases fail on purpose, and the choice
	// is load-bearing rather than arbitrary: it writes no item state, so a
	// successful retry produces no stream event and therefore no row swap. An
	// answer would, and the swap it causes races the fetch that reports the
	// outcome — which is the very race the second case asserts against, so it
	// must not be the case's own setup.
	speakPattern = "**/api/1.0/attention/*/speak"
	// failedNoteSelector is the class the page gives a note reporting a
	// failure, whichever of its helpers rendered it.
	failedNoteSelector = ".note.failed"
)

var (
	tmpRoot     string
	sessionsDir string
	binPath     string
	baseURL     string
	proc        *exec.Cmd
	tts         *ttsStub

	pw      *playwright.Playwright
	browser playwright.Browser
)

// ttsStub stands in for the tts server. The suite never calls it directly: the
// board's own speak control POSTs to the board, and the board proxies to /say.
// Recording here is what proves the click reached the server.
type ttsStub struct {
	server *httptest.Server

	mu   sync.Mutex
	says []string
}

func newTTSStub() *ttsStub {
	stub := &ttsStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/say" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		stub.mu.Lock()
		stub.says = append(stub.says, body.Text)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message_id": "stub-message-1"})
	}))
	return stub
}

// utterances returns the texts the board forwarded to /say.
func (s *ttsStub) utterances() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.says...)
}

// reset clears the recorded utterances between specs.
func (s *ttsStub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.says = nil
}

// writeSessionRegistry creates a hermetic session registry holding one live
// entry, and returns its directory.
//
// It is deliberately a temp directory rather than ~/.claude/sessions. The
// registry is the only thing that decides whether a pushed item renders, and an
// *unreadable* registry fails open — every session reads as live, every card
// renders, and the fixture would pass for a reason unrelated to what it claims
// to test. Seeding our own entry keeps the read exercised and the fixture
// independent of whichever host runs it.
func writeSessionRegistry(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entry := map[string]string{"sessionId": e2eSessionID, "name": "e2e"}
	content, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "e2e.json"), content, 0o644)
}

// freePort reserves a port by binding it and closing the listener. The gap
// between close and the binary's own bind is small and the start path retries.
func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// buildBinary compiles the real binary. The suite drives what ships, not a
// re-implementation of it.
func buildBinary(repoRoot, out string) error {
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = repoRoot
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// startBinary launches the real binary against the temp datadir and the
// hermetic registry, and waits for /healthz.
func startBinary(port int) error {
	proc = exec.Command(binPath,
		"-listen", fmt.Sprintf("127.0.0.1:%d", port),
		"-datadir", filepath.Join(tmpRoot, "data"),
		"-sessions-dir", sessionsDir,
		"-tts-url", tts.server.URL,
		"-attention-state-dir", filepath.Join(tmpRoot, "state"),
		// Empty disables the legacy pane-addressed jump listener. It is passed
		// explicitly because its default is a real address (127.0.0.1:1337),
		// which the suite must not bind.
		"-jump-listen", "",
		"-v", "2",
	)
	proc.Stdout, proc.Stderr = os.Stderr, os.Stderr
	if err := proc.Start(); err != nil {
		return err
	}
	baseURL = fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("binary did not become healthy at %s", baseURL)
}

var _ = BeforeSuite(func() {
	var err error

	repoRoot, err := filepath.Abs("..")
	Expect(err).NotTo(HaveOccurred())

	tmpRoot, err = os.MkdirTemp("", "attention-e2e-")
	Expect(err).NotTo(HaveOccurred())
	sessionsDir = filepath.Join(tmpRoot, "sessions")
	Expect(writeSessionRegistry(sessionsDir)).To(Succeed())

	tts = newTTSStub()

	binPath = filepath.Join(tmpRoot, "attention-controller")
	Expect(buildBinary(repoRoot, binPath)).To(Succeed())

	port, err := freePort()
	Expect(err).NotTo(HaveOccurred())
	Expect(startBinary(port)).To(Succeed())

	pw, err = playwright.Run()
	Expect(err).NotTo(HaveOccurred())
	browser, err = pw.Chromium.Launch()
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if browser != nil {
		_ = browser.Close()
	}
	if pw != nil {
		_ = pw.Stop()
	}
	if proc != nil && proc.Process != nil {
		_ = proc.Process.Kill()
		_, _ = proc.Process.Wait()
	}
	if tts != nil && tts.server != nil {
		tts.server.Close()
	}
	if tmpRoot != "" {
		_ = os.RemoveAll(tmpRoot)
	}
})

// --- helpers ---------------------------------------------------------------

// post sends a JSON body and returns the decoded response, failing the spec on
// any non-2xx.
func post(path string, body any) map[string]any {
	raw, err := json.Marshal(body)
	Expect(err).NotTo(HaveOccurred())
	resp, err := http.Post(
		baseURL+strings.TrimPrefix(path, "/"),
		"application/json",
		bytes.NewReader(raw),
	)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	content, _ := io.ReadAll(resp.Body)
	Expect(resp.StatusCode).To(BeNumerically(">=", 200), "POST %s: %s", path, string(content))
	Expect(resp.StatusCode).To(BeNumerically("<", 300), "POST %s: %s", path, string(content))
	var decoded map[string]any
	_ = json.Unmarshal(content, &decoded)
	return decoded
}

// push seeds one card and returns the item id the store assigned.
func push(payload, dedupKey string) string {
	request := pkg.PushRequest{
		ProducerID:      pkg.ProducerID(e2eProducerID),
		ProducerKind:    pkg.SessionProducerKind,
		LivenessRef:     pkg.LivenessRef("session:" + e2eSessionID),
		DedupKey:        pkg.DedupKey(dedupKey),
		InterruptClass:  pkg.InterruptClass("pick"),
		Payload:         pkg.Payload(payload),
		AnswerMechanism: pkg.MessageAnswerMechanism,
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

// pushQuestions pushes a two-question card, so the row renders a tab strip. The
// tab case needs one: a single-question item renders no tabs and cannot fail the
// way that case asserts.
//
// It is a second pusher rather than a parameter on push because every other case
// pushes a single-question card, and an option argument would put a branch in
// the path all of them take to serve one of them.
func pushQuestions(payload, dedupKey string) string {
	request := pkg.PushRequest{
		ProducerID:      pkg.ProducerID(e2eProducerID),
		ProducerKind:    pkg.SessionProducerKind,
		LivenessRef:     pkg.LivenessRef("session:" + e2eSessionID),
		DedupKey:        pkg.DedupKey(dedupKey),
		InterruptClass:  pkg.InterruptClass("pick"),
		Payload:         pkg.Payload(payload),
		AnswerMechanism: pkg.MessageAnswerMechanism,
		Questions: pkg.Questions{
			{
				Tab:         "Alpha",
				Payload:     pkg.Payload("e2e: the first question"),
				Cardinality: pkg.SingleAnswerCardinality,
				Options:     pkg.AnswerOptions{{Label: "a1"}, {Label: "a2"}},
			},
			{
				Tab:         "Beta",
				Payload:     pkg.Payload("e2e: the second question"),
				Cardinality: pkg.SingleAnswerCardinality,
				Options:     pkg.AnswerOptions{{Label: "b1"}, {Label: "b2"}},
			},
		},
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

// answer answers a card through the API, so the board learns about it over the
// SSE stream rather than from its own form handler.
func answer(itemID string) {
	post("api/1.0/attention/"+itemID+"/answer", map[string]any{
		"answered_by": "e2e",
		"answer":      map[string]string{"kind": "text", "value": "e2e answer"},
	})
}

// rowCount returns how many rows the board currently renders for an item. Zero
// means absent, one means present.
func rowCount(page playwright.Page, itemID string) int {
	count, err := page.Locator(fmt.Sprintf(rowSelectorFmt, itemID)).Count()
	Expect(err).NotTo(HaveOccurred())
	return count
}

// newPage opens a fresh page on the board. A page per spec keeps the parked-set
// and filter state from leaking between cases.
func newPage(path string) playwright.Page {
	page, err := browser.NewPage()
	Expect(err).NotTo(HaveOccurred())
	_, err = page.Goto(baseURL+strings.TrimPrefix(path, "/"), playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateLoad,
	})
	Expect(err).NotTo(HaveOccurred())
	return page
}

// rowSelector addresses one item's row, the form the existing cases build
// inline.
func rowSelector(itemID string) string {
	return fmt.Sprintf(rowSelectorFmt, itemID)
}

// staleVisible reports whether the board's not-tracking state is rendered.
//
// The span carries the hidden attribute in every response, so this is false on
// a board whose stream is healthy and true only once the stream's own onerror
// has shown it. It is a rendering fact read from the live DOM — the served
// markup is identical either way and cannot answer it.
func staleVisible(page playwright.Page) bool {
	visible, err := page.Locator(staleSelector).IsVisible()
	Expect(err).NotTo(HaveOccurred())
	return visible
}

// noteCount returns how many notes matching selector the board renders inside
// one item's row. Zero means the note is absent, one means it is rendered.
func noteCount(page playwright.Page, itemID, selector string) int {
	count, err := page.Locator(rowSelector(itemID) + " " + selector).Count()
	Expect(err).NotTo(HaveOccurred())
	return count
}

// failSpeak routes one item's read-aloud endpoint to a 500 for as long as the
// route stays registered, so a click that would otherwise succeed fails inside
// the browser and the page has to render its failure note.
//
// It is the precondition of the two note cases, never their assertion: both
// assert on what the DOM shows afterwards, and the second removes the route so
// the same action can succeed. The handler ignores Fulfill's error rather than
// asserting on it because it runs on playwright's dispatch goroutine, where a
// failed expectation would be reported against the wrong one — a route that
// never fulfilled shows up as the note never appearing.
func failSpeak(page playwright.Page) {
	Expect(page.Route(speakPattern, func(route playwright.Route) {
		status := http.StatusInternalServerError
		_ = route.Fulfill(playwright.RouteFulfillOptions{
			Status:      &status,
			ContentType: playwright.String("application/json"),
			Body:        `{"error":{"code":"INTERNAL","message":"e2e: speak forced to fail"}}`,
		})
	})).To(Succeed())
}

// clickSpeak presses one item's read-aloud control, so the page performs the
// action itself rather than the suite calling the endpoint directly.
func clickSpeak(page playwright.Page, itemID string) {
	Expect(page.Locator(rowSelector(itemID) + " " + speakSelector).Click()).To(Succeed())
}

// activeTab returns the question whose panel the board is currently showing.
//
// It reads the visible panel rather than the strip's own active class on
// purpose: the click handler sets the class and the panels in one pass, so a
// strip whose listener died with its node still carries "active" on the tab that
// was active when the node was replaced. Asserting on the class would therefore
// pass on exactly the wedged card this case exists to catch.
func activeTab(page playwright.Page, itemID string) string {
	value, err := page.Locator(rowSelector(itemID) + " .panel:not([hidden])").First().
		GetAttribute("data-question")
	Expect(err).NotTo(HaveOccurred())
	return value
}

// clickTab presses one tab on an item's card, so the page performs the switch
// rather than the suite setting the panel's hidden attribute directly.
func clickTab(page playwright.Page, itemID, tab string) {
	Expect(
		page.Locator(fmt.Sprintf("%s button[data-tab=%q]", rowSelector(itemID), tab)).Click(),
	).To(Succeed())
}

// --- cases -----------------------------------------------------------------

var _ = Describe("the attention board", func() {
	BeforeEach(func() {
		tts.reset()
	})

	It(
		"returns a parked answered card to the DOM when the Hide answered switch is clicked",
		func() {
			itemID := push("e2e: the switch case", "e2e-switch")
			answer(itemID)

			page := newPage("")
			defer func() { _ = page.Close() }()

			// The card is answered, so the default view parks it. Asserting the
			// click *handler* is the point: the switch itself is rendered
			// unconditionally by the server, so asserting its presence would pass
			// with every line of inline JS deleted.
			Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(0))

			Expect(page.Locator(switchSelector).Click()).To(Succeed())

			Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))
		},
	)

	It("renders the open card and not the answered one on a fresh load", func() {
		openID := push("e2e: the open card", "e2e-default-open")
		answeredID := push("e2e: the answered card", "e2e-default-answered")
		answer(answeredID)

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, openID) }).Should(Equal(1))
		Consistently(func() int { return rowCount(page, answeredID) }).Should(Equal(0))
	})

	It("renders the answered card too when the view is hide=none", func() {
		openID := push("e2e: the open card", "e2e-none-open")
		answeredID := push("e2e: the answered card", "e2e-none-answered")
		answer(answeredID)

		page := newPage("?hide=none")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, openID) }).Should(Equal(1))
		Eventually(func() int { return rowCount(page, answeredID) }).Should(Equal(1))
	})

	// ⚠️ The operator's report, end to end in a real browser: answer one card and
	// the producer's next push of the SAME dedup_key writes a new row, which used
	// to render as a fresh prompt — one ask, two answers.
	//
	// The precondition assertion is load-bearing and is NOT decoration: the store's
	// suppression is open-scoped, so a second row is what the store is supposed to
	// write here. If it ever stops writing one, this case must fail loudly rather
	// than pass because there is no twin left to suppress — the rule that owns that
	// behaviour is Attention Item Schema's and is a separate change.
	It("does not offer a re-pushed ask as a prompt when its sibling is answered", func() {
		answeredID := push("e2e: the re-pushed ask", "e2e-repush")
		answer(answeredID)

		repushedID := push("e2e: the re-pushed ask", "e2e-repush")
		Expect(repushedID).NotTo(Equal(answeredID))

		// ?hide=none so the answered record is not parked by the filter — the
		// assertion below is then about the row being withheld, not about a filter
		// removing it. The leading positive is what keeps the absence meaningful:
		// without it, a board that rendered nothing at all would also pass.
		page := newPage("?hide=none")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, answeredID) }).Should(Equal(1))
		Consistently(func() int { return rowCount(page, repushedID) }).Should(Equal(0))
	})

	It("removes a row from the DOM when the item is answered over the SSE stream", func() {
		itemID := push("e2e: the streamed answer", "e2e-sse")

		// The default view, not ?hide=none: the row must be one the filter parks
		// once it is answered, or the absence below could never happen. The
		// answer goes through the API rather than the page's own form, so the
		// page can only learn of it over the stream — which is what makes this a
		// test of the stream rather than of a local click handler.
		page := newPage("")
		defer func() { _ = page.Close() }()

		// The leading positive is load-bearing: without it the absence below
		// would also be satisfied by a card that never rendered at all.
		Eventually(func() int { return rowCount(page, itemID) }).
			WithTimeout(5 * time.Second).Should(Equal(1))

		answer(itemID)

		Eventually(func() int { return rowCount(page, itemID) }).
			WithTimeout(5 * time.Second).Should(Equal(0))
	})

	It("forwards the utterance to the tts server when the read-aloud control is clicked", func() {
		itemID := push("e2e: read this aloud", "e2e-speak")

		page := newPage("")
		defer func() { _ = page.Close() }()

		// Click the control inside the seeded row, so the assertion is about
		// this item's utterance rather than any row's.
		rowSpeak := page.Locator(
			fmt.Sprintf("%s %s", fmt.Sprintf(rowSelectorFmt, itemID), speakSelector),
		)
		Expect(rowSpeak.Count()).To(Equal(1))
		Expect(rowSpeak.Click()).To(Succeed())

		Eventually(tts.utterances).Should(ContainElement("e2e: read this aloud"))
	})

	It("logs a malformed stream frame and still applies the frame that follows it", func() {
		// The store has no path that emits a frame the page cannot read, so the
		// only way to drive one through the browser is to serve the channel
		// ourselves: the route fulfills it with a truncated frame followed by a
		// well-formed one, which is what makes both assertions below about the
		// page's handler rather than about the store.
		//
		// ⚠️ LIMITATION: this is as close as the harness gets. A frame from a
		// newer store version — the other case the handler's catch exists for —
		// is not producible here either, for the same reason: nothing in this
		// repo can be made to serve one.
		const survivorID = "e2e-frame-survivor"
		malformed := `{"type":"upsert","item_id":"e2e-truncated`
		wellFormed, err := json.Marshal(map[string]string{
			"type":    "upsert",
			"item_id": survivorID,
			"html": `<li class="item" data-item-id="` + survivorID +
				`">read past the malformed frame</li>`,
		})
		Expect(err).NotTo(HaveOccurred())

		var mu sync.Mutex
		var logged []string
		var thrown []error

		page := newPage("")
		defer func() { _ = page.Close() }()

		page.OnConsole(func(message playwright.ConsoleMessage) {
			mu.Lock()
			defer mu.Unlock()
			logged = append(logged, message.Text())
		})
		page.OnPageError(func(err error) {
			mu.Lock()
			defer mu.Unlock()
			thrown = append(thrown, err)
		})
		// Registered before the navigation that opens the channel: newPage has
		// already loaded once, so the reload is what puts this page's stream
		// behind the route.
		Expect(page.Route(streamPattern, func(route playwright.Route) {
			_ = route.Fulfill(playwright.RouteFulfillOptions{
				ContentType: playwright.String("text/event-stream"),
				Body:        "data: " + malformed + "\n\ndata: " + string(wellFormed) + "\n\n",
			})
		})).To(Succeed())
		_, err = page.Reload(
			playwright.PageReloadOptions{WaitUntil: playwright.WaitUntilStateLoad},
		)
		Expect(err).NotTo(HaveOccurred())

		// The row the second frame carried is rendered, so the handler read past
		// the frame it could not parse instead of aborting on it.
		Eventually(func() int { return rowCount(page, survivorID) }).
			WithTimeout(10 * time.Second).Should(Equal(1))

		mu.Lock()
		defer mu.Unlock()
		// And the frame it could not parse was reported rather than swallowed.
		Expect(logged).To(ContainElement(ContainSubstring("unreadable stream frame")))
		// Caught, not thrown: the page's own window.onerror never saw it.
		Expect(thrown).To(BeEmpty())
	})

	It("shows the not-tracking state when the stream dies and clears it when it returns", func() {
		page := newPage("")
		defer func() { _ = page.Close() }()

		// Abort, not an error status. EventSource re-establishes the connection
		// after a network error but fails it for good on a non-200 response or a
		// wrong content type — so a route fulfilling 500 would prove the first
		// half of this case and make the second half impossible.
		Expect(page.Route(streamPattern, func(route playwright.Route) {
			_ = route.Abort()
		})).To(Succeed())
		_, err := page.Reload(
			playwright.PageReloadOptions{WaitUntil: playwright.WaitUntilStateLoad},
		)
		Expect(err).NotTo(HaveOccurred())

		// The board's own onerror unhides it. Read from the rendered page rather
		// than from the served markup: the span ships hidden in every response,
		// so the source cannot tell a dead stream from a healthy one.
		Eventually(func() bool { return staleVisible(page) }).
			WithTimeout(10 * time.Second).Should(BeTrue())

		// Letting the request through is what restores the stream. Nothing here
		// re-opens it — EventSource reconnects on its own — and the state clears
		// on the next frame rather than on the reconnect, so each attempt makes a
		// real change for the channel to carry.
		Expect(page.Unroute(streamPattern)).To(Succeed())

		attempt := 0
		Eventually(func() bool {
			attempt++
			push(fmt.Sprintf("e2e: recovery %d", attempt), fmt.Sprintf("e2e-recover-%d", attempt))
			return !staleVisible(page)
		}).WithTimeout(45 * time.Second).WithPolling(500 * time.Millisecond).Should(BeTrue())

		// And it stays cleared, so the recovery is not a single repaint.
		Consistently(func() bool { return staleVisible(page) }).
			WithTimeout(2 * time.Second).Should(BeFalse())
	})

	It("keeps a failure note when a stream event replaces its row, so the note survives", func() {
		itemID := push("e2e: the note that must survive", "e2e-note-survives")

		// hide=none, so answering the item replaces its row in place with the
		// dimmed record instead of removing it. A row swap is what destroys a
		// note, and a removed row would let the assertion below pass for a
		// reason unrelated to what it claims.
		page := newPage("?hide=none")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		failSpeak(page)
		clickSpeak(page, itemID)

		Eventually(func() int { return noteCount(page, itemID, failedNoteSelector) }).
			WithTimeout(5 * time.Second).Should(Equal(1))

		// The answer goes through the API rather than through the page, so the
		// page can only learn of it over the stream — the technique the existing
		// stream case uses, and what makes this a test of the stream rather than
		// of a local click handler.
		answer(itemID)

		// The row really was replaced: it is the dimmed record now.
		Eventually(func() int { return noteCount(page, itemID, ".record") }).
			WithTimeout(5 * time.Second).Should(Equal(1))

		// And the note that swap would have destroyed is still rendered.
		Consistently(func() int { return noteCount(page, itemID, failedNoteSelector) }).
			WithTimeout(2 * time.Second).Should(Equal(1))
	})

	It(
		"applies a frame atomically, so a throw inside the update never leaves a half-updated row",
		func() {
			// ⚠️ FAULT-INJECTION PROBE, and the justification is load bearing
			// rather than a convenience. A probe is evidence only if the case it
			// constructs can actually occur, and this one can: attention-page.go
			// records a shipped instance of exactly this class — a null
			// querySelector result threw a TypeError on every X-click of a
			// permission card until 2026-09-27, and the file notes that throw was
			// invisible because "the operator saw the right outcome and only the
			// console took the error" (§ showCloseNote). The store emits no frame
			// that throws, so a DOM call the update path itself makes is the only
			// way to drive one through the browser.
			//
			// The fault is aimed at the note replay, which is the step upsertRow
			// used to run AFTER committing the swap. That is what makes the probe
			// discriminating: it lands inside the update on both revisions, but
			// only the swap-then-repair order loses the note.
			itemID := push("e2e: the row that must not half-update", "e2e-atomic-update")

			page := newPage("")
			defer func() { _ = page.Close() }()

			Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

			// A failure note is what gives the update something to lose, put there
			// by failing the read-aloud POST — the technique the two note cases
			// above already use. Without it replayFailure returns early and the
			// update has no post-swap step left to throw in.
			failSpeak(page)
			clickSpeak(page, itemID)
			Eventually(func() int { return noteCount(page, itemID, failedNoteSelector) }).
				WithTimeout(5 * time.Second).Should(Equal(1))

			var mu sync.Mutex
			var logged []string
			page.OnConsole(func(message playwright.ConsoleMessage) {
				mu.Lock()
				defer mu.Unlock()
				logged = append(logged, message.Text())
			})

			// querySelector('form.answer') is the call replayFailure makes to place
			// a note: on a DETACHED node under prepare-then-swap, and on the LIVE
			// row under swap-then-repair. One patch therefore reaches the update
			// from both sides. It disarms on first use, so the page is not left
			// broken for the assertions that follow.
			_, err := page.Evaluate(`() => {
			const original = Element.prototype.querySelector;
			let armed = true;
			Element.prototype.querySelector = function (selector) {
				if (armed && selector === 'form.answer') {
					armed = false;
					throw new Error('e2e: injected fault inside the row update');
				}
				return original.call(this, selector);
			};
		}`)
			Expect(err).NotTo(HaveOccurred())

			// A differing payload on the same dedup key updates the item in place,
			// so the store's render changes, the stream sends an upsert and the page
			// runs its update path. A byte-identical re-push renders identically and
			// the stream sends nothing.
			repushedID := push(
				"e2e: the row that must not half-update, re-rendered",
				"e2e-atomic-update",
			)
			Expect(repushedID).To(Equal(itemID), "the re-push must update in place, not add a row")

			// The frame was handled and the failure reported rather than swallowed.
			// This is also what proves the update path ran at all — without it the
			// assertion below could pass on a frame that never arrived.
			Eventually(func() []string {
				mu.Lock()
				defer mu.Unlock()
				return append([]string(nil), logged...)
			}).WithTimeout(10 * time.Second).Should(ContainElement(
				ContainSubstring("could not apply stream frame for " + itemID),
			))

			// Atomicity. BOTH legal outcomes keep the note — untouched leaves the
			// one already rendered, fully applied re-renders it — so only the
			// half-applied state loses it. That is what makes the note the
			// assertion rather than a proxy for one.
			Consistently(func() int { return noteCount(page, itemID, failedNoteSelector) }).
				WithTimeout(2*time.Second).Should(Equal(1),
				"the row lost its note, so the frame was applied part-way")
		},
	)

	It(
		"keeps a control working when a stream event replaces its row, so a re-rendered card still answers",
		func() {
			// A row swap is what destroys a per-node listener, and this case
			// covers the two binding styles a message card renders: the tab
			// strip and the answer form the Dismiss button submits. Both were
			// bound per node at page load and both went inert on a row the
			// stream had re-rendered — measured 2026-09-28, with a reload the
			// only way back. The read-aloud and Jump controls were converted to
			// delegation for this same defect; the assertion here is what was
			// missing when the other two rotted.
			itemID := pushQuestions(
				"e2e: the control that must survive a swap",
				"e2e-control-survives",
			)

			page := newPage("")
			defer func() { _ = page.Close() }()

			Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

			// Before the swap the strip answers, so a failure below is about the
			// swap and not about a strip that never worked.
			Expect(activeTab(page, itemID)).To(Equal("Alpha"))
			clickTab(page, itemID, "Beta")
			Expect(activeTab(page, itemID)).To(Equal("Beta"))

			// Re-push the same producer and dedup key: the store updates the
			// item in place, the stream sees the row's rendered HTML change and
			// sends an upsert, and the page swaps the node. The payload differs
			// because a byte-identical re-push renders identically and the
			// stream sends nothing — the swap would not happen.
			repushedID := pushQuestions(
				"e2e: the control that must survive a swap, re-rendered",
				"e2e-control-survives",
			)
			Expect(repushedID).To(Equal(itemID), "the re-push must update in place, not add a row")

			// The swap really happened: the stream puts a freshly rendered row in
			// place, which resets the strip to the server's own active question.
			// Waiting for that is what makes the clicks below about the swapped
			// node rather than about the pre-swap one — asserting straight after
			// the push reads the old node, because the frame has not landed yet.
			Eventually(func() string { return activeTab(page, itemID) }).
				WithTimeout(10 * time.Second).Should(Equal("Alpha"))

			// The control still works on the node the stream put there.
			clickTab(page, itemID, "Beta")
			Expect(activeTab(page, itemID)).To(Equal("Beta"))

			// And the second binding style on the same row still works: Dismiss
			// is a submit, so this is the form handler rather than the strip.
			Expect(page.Locator(rowSelector(itemID) + " button[value=skip]").Click()).To(Succeed())
			Eventually(func() int { return rowCount(page, itemID) }).
				WithTimeout(5 * time.Second).Should(Equal(0))
		},
	)

	It("clears the note when the retry succeeds, so no stale failure outlives its cause", func() {
		itemID := push("e2e: the note that must clear", "e2e-note-clears")

		// hide=none for the same reason as the case above: the row has to
		// survive the answer below, or its absence would answer this for us.
		page := newPage("?hide=none")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		failSpeak(page)
		clickSpeak(page, itemID)

		Eventually(func() int { return noteCount(page, itemID, failedNoteSelector) }).
			WithTimeout(5 * time.Second).Should(Equal(1))

		// The route is removed, so the retry reaches the board and succeeds.
		Expect(page.Unroute(speakPattern)).To(Succeed())
		clickSpeak(page, itemID)

		// The note is cleared rather than merely moved: the success replaces it
		// in the same card.
		Eventually(func() int { return noteCount(page, itemID, failedNoteSelector) }).
			WithTimeout(5 * time.Second).Should(Equal(0))

		// And it stays cleared across the row swap the answer below causes. A
		// stale failure must not outlive its cause: had the page kept the
		// failure it was shown, the swap would have re-rendered it here.
		answer(itemID)
		Eventually(func() int { return noteCount(page, itemID, ".record") }).
			WithTimeout(5 * time.Second).Should(Equal(1))
		Consistently(func() int { return noteCount(page, itemID, failedNoteSelector) }).
			WithTimeout(2 * time.Second).Should(Equal(0))
	})
})
