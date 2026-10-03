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
	"strconv"
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
	// infoToggleSelector is the card's info affordance — the `i` button at the
	// card's top right that reveals the panel below.
	infoToggleSelector = "button[data-info-toggle]"
	// infoPanelSelector is the panel the affordance reveals. It ships hidden in
	// the served markup, so only the browser's rendered state tells an open card
	// from a closed one.
	infoPanelSelector = "[data-info-panel]"
	// cornerXSelector is the card's whole skip affordance, pinned in the card's
	// top-right corner.
	cornerXSelector = "button[data-corner-x]"
	// jumpCornerSelector is the jump control in the corner. It is addressed by
	// class rather than by `data-jump`, because the disabled arm — the one a
	// fixture card renders, since no pane resolves in this harness — carries no
	// `data-jump` attribute.
	jumpCornerSelector = "button.jump-corner"
	// provenanceSelector is the card's navigation line. It renders only when a
	// navigation value resolved, so the harness's registry name is what makes it
	// present on a fixture card.
	provenanceSelector = ".provenance"
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
	spawnDir    string
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
	// ⚠️ The `nameSource` is load-bearing, not decoration. Only a source of
	// `user` renders a session name (pkg/provenance.go's sessionNameSourceUser),
	// and the navigation line is the only place the session name appears — so
	// without it every fixture card renders no `.provenance` element and the
	// navigation case below has nothing to assert on.
	//
	// ⚠️ This changes the shared fixture for EVERY case in the suite, and that
	// is intended rather than incidental: the fixture cards gain a session-name
	// span in a navigation line, and no existing case asserts the absence of
	// one. The store, the row count, the panel contents, the tab strip, the
	// note cases and the answer shapes are all unaffected.
	entry := map[string]string{
		"sessionId":  e2eSessionID,
		"name":       "e2e",
		"nameSource": "user",
	}
	content, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "e2e.json"), content, 0o644)
}

// writeSpawnLedger creates a hermetic spawn ledger holding one headless
// record for the fixture session, and returns its directory.
//
// It is deliberately a temp directory rather than the operator's real
// ~/.local/state/claude-supervisor/sessions: the ledger is what decides
// whether a permission card renders its Allow / Deny pair, and a fixture
// session absent from the real ledger would lose the control the permission
// answer-shape cases click. The record must be `headless` — a fixture that
// read as a tab worker would turn scenario 002's "renders the two verdict
// buttons" red.
func writeSpawnLedger(dir, sessionID, mode string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(
		filepath.Join(dir, sessionID+".json"),
		[]byte(`{"session_id":"`+sessionID+`","mode":"`+mode+`"}`),
		0o644,
	)
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

// startBinary launches the real binary against the temp datadir, the hermetic
// registry and the hermetic spawn ledger, and waits for /healthz.
func startBinary(port int) error {
	proc = exec.Command(binPath,
		"-listen", fmt.Sprintf("127.0.0.1:%d", port),
		"-datadir", filepath.Join(tmpRoot, "data"),
		"-sessions-dir", sessionsDir,
		"-spawn-state-dir", spawnDir,
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
	spawnDir = filepath.Join(tmpRoot, "spawn")
	Expect(writeSpawnLedger(spawnDir, e2eSessionID, "headless")).To(Succeed())

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

// openItemIDs returns the ids the store still counts as open, read through the
// list API an arm reads rather than through the board.
//
// The suite starts one binary against one datadir for the whole run, so items
// pushed by earlier specs are still in the store. A case that needs a board
// with nothing open therefore establishes that precondition itself instead of
// assuming a fresh store.
func openItemIDs() []string {
	resp, err := http.Get(baseURL + "api/1.0/attention")
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	content, _ := io.ReadAll(resp.Body)
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "list failed: %s", string(content))
	var items []struct {
		ItemID string `json:"item_id"`
	}
	Expect(json.Unmarshal(content, &items)).To(Succeed())
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ItemID)
	}
	return ids
}

// renderedCount returns how many item rows the board renders, whichever items
// they belong to. Unlike rowCount it names no item, which is what makes it
// usable as the positive half of an assertion about a whole board.
func renderedCount(page playwright.Page) int {
	count, err := page.Locator("li.item").Count()
	Expect(err).NotTo(HaveOccurred())
	return count
}

// emptyStateCount returns how many empty-state statements the board renders.
// The statement is a rendering fact: the served markup carries it only when the
// server rendered no rows at all, and the client re-creates it only from
// collapseIfEmpty. So a count of zero on a board that also renders no rows is
// the blank region itself.
func emptyStateCount(page playwright.Page) int {
	count, err := page.Locator("p.empty").Count()
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

// clickInfoToggle presses one item's info affordance, so the page performs the
// reveal itself rather than the suite setting the panel's hidden attribute.
func clickInfoToggle(page playwright.Page, itemID string) {
	Expect(page.Locator(rowSelector(itemID) + " " + infoToggleSelector).Click()).To(Succeed())
}

// infoPanelVisible reports whether one item's info panel is rendered. The
// panel ships hidden in every response, so this is a rendering fact read from
// the live DOM — the served markup is identical either way and cannot answer
// it.
func infoPanelVisible(page playwright.Page, itemID string) bool {
	visible, err := page.Locator(rowSelector(itemID) + " " + infoPanelSelector).IsVisible()
	Expect(err).NotTo(HaveOccurred())
	return visible
}

// computedStyle reads one CSS property off one element, as the browser
// resolves it. It is the only way to assert a control's slot: the served
// stylesheet states the rule, but only the browser can say what the element
// actually computes.
func computedStyle(page playwright.Page, selector, property string) string {
	value, err := page.Locator(selector).Evaluate(
		"el => getComputedStyle(el).getPropertyValue("+strconv.Quote(property)+")",
		nil,
	)
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

// textBox returns the bounding box of an element's TEXT, not of its border
// box. The two differ here and the difference is load-bearing: the card's text
// blocks reserve the corner band with a right margin, so the text ends where
// the reservation begins while the element's box stops at the same place — but
// a Range over the text is what the acceptance criterion names, and it is the
// only measurement that answers "does the text run through the controls".
func textBox(page playwright.Page, selector string) playwright.Rect {
	box, err := page.Locator(selector).Evaluate(`el => {
		const range = document.createRange();
		range.selectNodeContents(el);
		const r = range.getBoundingClientRect();
		return {x: r.x, y: r.y, width: r.width, height: r.height};
	}`, nil)
	Expect(err).NotTo(HaveOccurred())
	decoded, err := json.Marshal(box)
	Expect(err).NotTo(HaveOccurred())
	var rect playwright.Rect
	Expect(json.Unmarshal(decoded, &rect)).To(Succeed())
	return rect
}

// overlaps reports whether two rectangles share any area. Touching edges do
// not count: the reserved band is sized to clear the controls' leftmost edge,
// so a strict comparison is what distinguishes "wraps before the control" from
// "touches it".
func overlaps(a, b playwright.Rect) bool {
	return a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height
}

// infoExpanded reads one item's affordance state, the attribute the control
// reports to assistive technology.
func infoExpanded(page playwright.Page, itemID string) string {
	value, err := page.Locator(rowSelector(itemID) + " " + infoToggleSelector).
		GetAttribute("aria-expanded")
	Expect(err).NotTo(HaveOccurred())
	return value
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

// binaryVCSRevision reads the commit the built binary carries in its OWN build
// info, via `go version -m` — the artifact's own stamp, never the checkout's
// HEAD. That distinction is the whole point of the footer: a repo read answers
// "which commit is the checkout at", which agrees with the binary only while the
// two are in step, and is the wrong answer exactly when they diverge.
//
// It returns "" when the binary carries no stamp, which is a real state rather
// than an error: a file-list build (`go build main.go`) suppresses Go's VCS
// stamping entirely, and the caller asserts on the emptiness rather than
// papering over it.
func binaryVCSRevision(binaryPath string) string {
	out, err := exec.Command("go", "version", "-m", binaryPath).Output()
	Expect(err).NotTo(HaveOccurred(), "go version -m failed on %s", binaryPath)
	for _, field := range strings.Fields(string(out)) {
		if value, found := strings.CutPrefix(field, "vcs.revision="); found {
			return value
		}
	}
	return ""
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

	// ⚠️ The operator's report stated as the invariant it actually is: "if
	// everything is done ... we should show a something". A board that renders
	// neither a card nor the empty-state statement is the failure — the region
	// below the control row goes blank and certifies nothing.
	//
	// The store is shared across the suite, so the case makes its own
	// precondition: it answers every item the board still counts as open. What
	// is left are rows that exist only as dimmed records, which the default view
	// parks — the state the operator's board was in when they reported it.
	It(
		"says nothing needs the operator rather than rendering a blank region when every item is answered",
		func() {
			for _, itemID := range openItemIDs() {
				answer(itemID)
			}
			Eventually(openItemIDs).Should(BeEmpty())

			page := newPage("")
			defer func() { _ = page.Close() }()

			// The empty statement is what the default view owes once every record
			// is parked, and it is pinned by identity, count and text: exactly one
			// `p.empty`, zero item rows, and the sentence the zero-item path
			// renders. A board that rendered nothing at all cannot satisfy the
			// first of these, which is what the earlier `renderedCount + empty`
			// `> 0` probe could not tell apart from a blank region.
			Eventually(func() int { return emptyStateCount(page) }).
				WithTimeout(5 * time.Second).Should(Equal(1))
			Consistently(func() int { return renderedCount(page) }).Should(Equal(0))

			text, err := page.Locator("p.empty").TextContent()
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(text)).To(Equal("Nothing needs attention."))
		},
	)

	// ⚠️ The absence half, and the load-bearing one: a board that printed the
	// statement unconditionally would satisfy the all-answered case above and
	// only fail here. The store is shared, so the case makes its own
	// precondition — answer every open id — then pushes exactly one fresh card,
	// which the default view renders.
	//
	// The dedup key is named rather than left to choice: a collision with a key
	// another case already uses yields a suppressed or twinned row, which is a
	// failure for the wrong reason.
	//
	// ⚠️ The second paragraph closes the LIVE half, and it is the half no other
	// case in this suite observes. Every other case loads a fresh page *after*
	// the store changed, so a fix wired only into the load path would pass them
	// all while the live board — the surface the operator actually reported —
	// stayed blank. The frame is an upsert, not a remove: the stream reads
	// ReadBoard (pkg/handler/attention-stream.go), so an answered item stays in
	// the rendered map with changed HTML and is sent as an upsert carrying the
	// dimmed row. The client parks it and renders the statement.
	It(
		"does not claim nothing needs the operator while an open card is rendered",
		func() {
			for _, itemID := range openItemIDs() {
				answer(itemID)
			}
			Eventually(openItemIDs).Should(BeEmpty())

			openID := push("e2e: the open card", "e2e-absence-guard")

			page := newPage("")
			defer func() { _ = page.Close() }()

			// The leading positive is load-bearing: without it the absence below
			// would also be satisfied by a card that never rendered at all.
			Eventually(func() int { return rowCount(page, openID) }).Should(Equal(1))
			// Exactly one row, because every other record was answered and the
			// default view parks answered records.
			Consistently(func() int { return renderedCount(page) }).Should(Equal(1))
			// The statement is absent while an open card stands. The explicit
			// WithTimeout is deliberate: Consistently's default window is 100 ms,
			// which is too short to mean anything on a browser absence assertion.
			Consistently(func() int { return emptyStateCount(page) }).
				WithTimeout(5 * time.Second).Should(Equal(0))

			// The live half: answering the last open card on an already-open
			// board must render the statement without a reload.
			answer(openID)
			Eventually(func() int { return emptyStateCount(page) }).
				WithTimeout(5 * time.Second).Should(Equal(1))
			Consistently(func() int { return renderedCount(page) }).
				WithTimeout(5 * time.Second).Should(Equal(0))
		},
	)

	// ⚠️ The toggle round-trip, and the guard for the frozen constraint: an
	// implementation that discharged the parked set to reach the empty state
	// would satisfy the all-answered case and fail here, because the record
	// could no longer be restored. The store is shared, so the case establishes
	// its own all-answered precondition, then pushes and answers one fresh card
	// so the toggle has exactly one parked record to restore.
	It(
		"replaces the empty statement with the parked record when the Hide answered switch is clicked",
		func() {
			for _, itemID := range openItemIDs() {
				answer(itemID)
			}
			Eventually(openItemIDs).Should(BeEmpty())

			itemID := push("e2e: the toggle round-trip", "e2e-toggle-roundtrip")
			answer(itemID)
			Eventually(openItemIDs).Should(BeEmpty())

			page := newPage("")
			defer func() { _ = page.Close() }()

			// The answered card is parked, not rendered, and the statement stands
			// in its place.
			Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(0))
			Eventually(func() int { return emptyStateCount(page) }).
				WithTimeout(5 * time.Second).Should(Equal(1))

			Expect(page.Locator(switchSelector).Click()).To(Succeed())

			// The record returns and the statement is gone.
			Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))
			Eventually(func() int { return emptyStateCount(page) }).Should(Equal(0))
		},
	)

	It("renders the answered card too when the view is hide=none", func() {
		openID := push("e2e: the open card", "e2e-none-open")
		answeredID := push("e2e: the answered card", "e2e-none-answered")
		answer(answeredID)

		page := newPage("?hide=none")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, openID) }).Should(Equal(1))
		Eventually(func() int { return rowCount(page, answeredID) }).Should(Equal(1))
	})

	// ⚠️ The footer's identity is asserted against the BINARY'S OWN stamp, never
	// against the checkout — which makes this case the regression guard for the
	// deploy recipe as well. The suite builds with `go build -o out .`, the
	// package form; a binary built as a file list (`go build main.go`) carries no
	// vcs.revision, so the footer renders its explicit "No build identity" line
	// and this case reddens, instead of the board shipping unable to say which
	// build it is. The expected value is read from the artifact itself, so a page
	// that resolved the sha from the repo at render time cannot satisfy it.
	//
	// The abbreviation's exact width is asserted in the handler specs; what this
	// case adds is that the value reaching the footer is the artifact's own.
	It("renders the running binary's own commit in the footer", func() {
		revision := binaryVCSRevision(binPath)
		Expect(revision).NotTo(BeEmpty(), "the suite's own build carries no VCS stamp")

		page := newPage("")
		defer func() { _ = page.Close() }()

		// The footer is server-rendered with the document, so it is present once
		// the load event has fired and needs no Eventually to wait for it.
		text, err := page.Locator("footer.build-identity").TextContent()
		Expect(err).NotTo(HaveOccurred())

		// Shortened the way buildidentity shortens it — only when it is longer —
		// so a revision already shorter than the display width is compared whole
		// rather than sliced past its end.
		short := revision
		if len(short) > 12 {
			short = short[:12]
		}
		Expect(text).To(ContainSubstring(short))
		Expect(text).To(ContainSubstring("committed"))
		Expect(text).NotTo(ContainSubstring("No build identity"))
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

	// ⚠️ The suite's package total is asserted in BOTH scenario files, not only
	// scenario 001: `scenarios/002-board-answer-shapes.md` carries the same hard
	// `N of N Specs` line, and it is the package total rather than that file's
	// own case count, so these two board cases move it too. The spec named only
	// scenario 001, so the second edit is a finding rather than an instruction it
	// anticipated — leaving it stale would turn the board's pre-release gate into
	// a check that fails on a correct build.
	//
	// ⚠️ The same finding holds for the two corner-band and navigation cases
	// added below: they move the package total again (25 → 27), so BOTH scenario
	// files move with them. Recorded here beside the previous instance of the
	// finding rather than only in the changelog, because the next case added to
	// this file will hit it a third time.
	//
	// ⚠️ The card's info affordance is driven here in a real browser because the
	// panel ships hidden in EVERY response: the served markup is identical
	// whether a card is open or closed, so only the browser's rendered state can
	// tell the two apart. The served-HTML criteria are a sibling prompt's.
	It("reveals a card's metadata from the info affordance and reports its state", func() {
		itemID := push("e2e: the card that reveals its metadata", "e2e-info-reveal")

		page := newPage("")
		defer func() { _ = page.Close() }()

		// The leading positive, so every absence below is a withheld thing rather
		// than a dropped row.
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		// Exactly one affordance and one panel on this row. The count is scoped to
		// the row because the fixture store is shared across the whole run and a
		// page-wide count could be satisfied by another case's row.
		toggleCount, err := page.Locator(rowSelector(itemID) + " " + infoToggleSelector).Count()
		Expect(err).NotTo(HaveOccurred())
		Expect(toggleCount).To(Equal(1))
		panelCount, err := page.Locator(rowSelector(itemID) + " " + infoPanelSelector).Count()
		Expect(err).NotTo(HaveOccurred())
		Expect(panelCount).To(Equal(1))

		// A visible glyph and an accessible name, and BOTH halves are required.
		// v0.18.0 rejected an icon-only read-aloud control on this board because
		// the control's meaning lived in a hover-only `title` plus an `aria-label`
		// only assistive tech sees. The visible `i` is the meaning here; the
		// `aria-label` is the accessible name.
		glyph, err := page.Locator(rowSelector(itemID) + " " + infoToggleSelector).TextContent()
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(glyph)).To(Equal("i"))
		label, err := page.Locator(rowSelector(itemID) + " " + infoToggleSelector).
			GetAttribute("aria-label")
		Expect(err).NotTo(HaveOccurred())
		Expect(label).NotTo(BeEmpty())

		// The one placement assertion: the affordance sits at the row's top right.
		Expect(page.Locator(rowSelector(itemID)).ScrollIntoViewIfNeeded()).To(Succeed())
		rowBox, err := page.Locator(rowSelector(itemID)).BoundingBox()
		Expect(err).NotTo(HaveOccurred())
		toggleBox, err := page.Locator(rowSelector(itemID) + " " + infoToggleSelector).BoundingBox()
		Expect(err).NotTo(HaveOccurred())
		Expect(rowBox).NotTo(BeNil())
		Expect(toggleBox).NotTo(BeNil())
		Expect(toggleBox.X).To(BeNumerically(">", rowBox.X+rowBox.Width/2))
		Expect(toggleBox.Y).To(BeNumerically("<", rowBox.Y+40))

		// Closed first: the panel ships hidden and the control reports so.
		Expect(infoPanelVisible(page, itemID)).To(BeFalse())
		Expect(infoExpanded(page, itemID)).To(Equal("false"))

		// A real click reveals it. ⚠️ No Reload is issued anywhere after this
		// click — "without a page reload" is the criterion, so the reveal must
		// come from the page's own handler.
		clickInfoToggle(page, itemID)
		Eventually(func() bool { return infoPanelVisible(page, itemID) }).
			WithTimeout(5 * time.Second).Should(BeTrue())
		Eventually(func() string { return infoExpanded(page, itemID) }).Should(Equal("true"))

		// The revealed text is the card's own metadata. The producer line and the
		// `state - createdAt` footer both live inside the panel, so this is the
		// observable form of "the reveal changes the row's metadata from absent to
		// present".
		panelText, err := page.Locator(rowSelector(itemID) + " " + infoPanelSelector).TextContent()
		Expect(err).NotTo(HaveOccurred())
		Expect(panelText).To(ContainSubstring(e2eProducerID))

		// A two-way toggle, not a one-way control — the defect this half exists to
		// catch.
		clickInfoToggle(page, itemID)
		Eventually(func() bool { return infoPanelVisible(page, itemID) }).Should(BeFalse())
		Eventually(func() string { return infoExpanded(page, itemID) }).Should(Equal("false"))
	})

	It("keeps the info affordance working when a stream event replaces its row", func() {
		itemID := push("e2e: the card that survives a swap", "e2e-info-survives")

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		// Reveal the panel first, so the swap has an open panel to lose.
		clickInfoToggle(page, itemID)
		Eventually(func() bool { return infoPanelVisible(page, itemID) }).Should(BeTrue())

		// Force the stream to replace the row: a differing payload on the same
		// producer and dedup key updates the item in place, so the store's render
		// changes and the stream sends an upsert. The payload MUST differ — a
		// byte-identical re-push renders identically and the stream sends nothing,
		// so no swap would happen. This is the technique the existing
		// `keeps a control working when a stream event replaces its row` case uses.
		repushedID := push("e2e: the card that survives a swap, re-rendered", "e2e-info-survives")
		Expect(repushedID).To(Equal(itemID), "the re-push must update in place, not add a row")

		// Wait for the swap to land before asserting on the node the stream put
		// there: reading straight after the push reads the old node, because the
		// frame has not arrived yet.
		Eventually(func() string {
			text, err := page.Locator(rowSelector(itemID)).TextContent()
			Expect(err).NotTo(HaveOccurred())
			return text
		}).WithTimeout(10 * time.Second).Should(ContainSubstring("re-rendered"))

		// The row came back closed. The acceptance criterion allows either outcome
		// — "still showing its metadata, or closed with the affordance still
		// operable" — and this design chose CLOSED, because the panel's state is
		// the served markup's rather than a page-local map's: the panel is
		// server-rendered `hidden` and the client keeps no open/closed state of its
		// own, so a swapped row is closed by construction and can never be stale.
		Eventually(func() bool { return infoPanelVisible(page, itemID) }).Should(BeFalse())
		Expect(infoExpanded(page, itemID)).To(Equal("false"))

		// ⚠️ The load-bearing half. A per-button listener dies with the node
		// upsertRow replaces, leaving the control rendered and inert — the
		// rendered-and-inert failure this board shipped once at v0.19.0 with the
		// read-aloud control. The delegated listener is what keeps the swapped
		// node's affordance operable.
		clickInfoToggle(page, itemID)
		Eventually(func() bool { return infoPanelVisible(page, itemID) }).Should(BeTrue())
		Eventually(func() string { return infoExpanded(page, itemID) }).Should(Equal("true"))
	})

	// ⚠️ AC1 and AC4, and both halves need a browser rather than served markup.
	// The reserved corner band is a stylesheet rule, so the served HTML states
	// it; only the browser can say what the ask's text and the four controls
	// actually compute. The payload MUST be long enough to reach the band — a
	// short body would satisfy the non-overlap assertion while the reported
	// defect persists, which the acceptance criterion explicitly forbids.
	//
	// ⚠️ A screenshot cannot be asserted from Go, and the acceptance criterion
	// names one as the OPERATOR's evidence. It is deliberately not produced
	// here: the geometry assertion below is the mechanism, and the operator's
	// own click-through is the evidence.
	It("wraps a long ask clear of the card's corner controls", func() {
		itemID := push(
			"e2e: an ask long enough to run past the card's corner band and wrap "+
				"before the read-aloud control, the corner X, the jump control and "+
				"the i, so it occupies several rendered lines inside the board's column",
			"e2e-corner-band",
		)

		page := newPage("")
		defer func() { _ = page.Close() }()

		// The leading positive, so every assertion below is about a drawn row.
		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		// A single-question `message` card renders its ask as `.question`.
		askSelector := rowSelector(itemID) + " .question"
		Expect(page.Locator(askSelector).Count()).To(Equal(1))

		Expect(page.Locator(rowSelector(itemID)).ScrollIntoViewIfNeeded()).To(Succeed())

		// Each control, and the slot it settles in. The identifiers and the
		// positions are frozen: this case asserts them rather than accepting a
		// change to them.
		controls := []struct {
			name     string
			selector string
			right    string
		}{
			{"read-aloud control", speakSelector, "48px"},
			{"corner X", cornerXSelector, "12px"},
			{"jump control", jumpCornerSelector, "84px"},
			{"info control", infoToggleSelector, "120px"},
		}

		// The ask's TEXT box, not its border box — the measurement the
		// acceptance criterion names.
		askBox := textBox(page, askSelector)
		for _, control := range controls {
			selector := rowSelector(itemID) + " " + control.selector

			// ⚠️ The existence check comes first: an absent control would
			// satisfy the non-overlap assertion vacuously.
			Expect(page.Locator(selector).Count()).To(Equal(1), "%s is missing", control.name)

			controlBox, err := page.Locator(selector).BoundingBox()
			Expect(err).NotTo(HaveOccurred())
			Expect(controlBox).NotTo(BeNil())
			Expect(overlaps(askBox, *controlBox)).To(BeFalse(),
				"the ask's text overlaps the %s", control.name)

			// AC4's static half — no control moved to escape the text.
			Expect(computedStyle(page, selector, "top")).To(Equal("10px"),
				"the %s did not keep its settled top", control.name)
			Expect(computedStyle(page, selector, "right")).To(Equal(control.right),
				"the %s did not keep its settled right", control.name)
		}

		// ⚠️ The band is cleared by WRAPPING, not by shrinking the text. The
		// computed size is the `.question` size, unchanged, and the ask still
		// occupies more than one rendered line.
		Expect(computedStyle(page, askSelector, "font-size")).To(Equal("17px"))
		metrics, err := page.Locator(askSelector).Evaluate(`el => {
			const style = getComputedStyle(el);
			return {
				height: el.getBoundingClientRect().height,
				lineHeight: style.lineHeight,
			};
		}`, nil)
		Expect(err).NotTo(HaveOccurred())
		decoded, err := json.Marshal(metrics)
		Expect(err).NotTo(HaveOccurred())
		var measured struct {
			Height     float64 `json:"height"`
			LineHeight string  `json:"lineHeight"`
		}
		Expect(json.Unmarshal(decoded, &measured)).To(Succeed())
		lineHeight, err := strconv.ParseFloat(strings.TrimSuffix(measured.LineHeight, "px"), 64)
		Expect(err).NotTo(HaveOccurred(), "line-height was not a pixel value")
		Expect(measured.Height).To(BeNumerically(">", lineHeight),
			"the ask occupies one line, so the band was not cleared by wrapping")

		// AC4's corner-X half, last, so it disposes of the fixture: the corner
		// X still closes its row.
		//
		// ⚠️ AC4's "every control still works" is already covered elsewhere and
		// is deliberately NOT duplicated here: the read-aloud click by
		// `forwards the utterance to the tts server when the read-aloud control
		// is clicked`, and the info-toggle click by `reveals a card's metadata
		// from the info affordance and reports its state`. This case asserts the
		// corner X because it is the one control those two do not click.
		Expect(page.Locator(rowSelector(itemID) + " " + cornerXSelector).Click()).To(Succeed())
		Eventually(func() int { return rowCount(page, itemID) }).
			WithTimeout(5 * time.Second).Should(Equal(0))
	})

	// ⚠️ AC2's rendered half (b). Its markup half — the provenance div's index
	// being less than the ask's in the served row — belongs to the handler-level
	// prompt; this case is not the whole criterion. ⚠️ A stale stylesheet cannot
	// satisfy this one, which is exactly why the criterion requires both halves:
	// the markup half can be true while the line is painted below the ask by a
	// stylesheet that still places it there.
	It("leads with the navigation line above the ask", func() {
		itemID := push("e2e: the card whose navigation line leads", "e2e-nav-above")

		page := newPage("")
		defer func() { _ = page.Close() }()

		Eventually(func() int { return rowCount(page, itemID) }).Should(Equal(1))

		// The line is rendered, with a value on it: the harness's registry name,
		// which requirement 1 made renderable. Without this positive the offset
		// comparison below could pass on an element that is not the navigation
		// line.
		provenanceLocator := page.Locator(rowSelector(itemID) + " " + provenanceSelector)
		Expect(provenanceLocator.Count()).To(Equal(1))
		provenanceText, err := provenanceLocator.TextContent()
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(provenanceText)).NotTo(BeEmpty())

		Expect(page.Locator(rowSelector(itemID)).ScrollIntoViewIfNeeded()).To(Succeed())

		// The ask is the same selector requirement 4 uses: `push` seeds a
		// single-question `message` card.
		askLocator := page.Locator(rowSelector(itemID) + " .question")
		Expect(askLocator.Count()).To(Equal(1))

		provBox, err := provenanceLocator.BoundingBox()
		Expect(err).NotTo(HaveOccurred())
		askBox, err := askLocator.BoundingBox()
		Expect(err).NotTo(HaveOccurred())
		Expect(provBox).NotTo(BeNil())
		Expect(askBox).NotTo(BeNil())

		// The rendered top offset, in the same row.
		Expect(provBox.Y).To(BeNumerically("<", askBox.Y))
	})
})
