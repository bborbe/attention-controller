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
		"-jump-url", "",
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
})
