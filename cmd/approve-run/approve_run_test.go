// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// testItemID is a well-formed 32-hex store item id.
var testItemID = strings.Repeat("a", storeItemIDLen)

// fakeClock is a controllable Clock. Timer fires immediately so the poll loop
// iterates without a real sleep; Now does not advance, so a zero wait reaches
// its deadline on the first pass.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Timer(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// fakeStore is a recording storeClient.
type fakeStore struct {
	getFunc  func(context.Context, pkg.ItemID) (*pkg.Item, error)
	postFunc func(context.Context, pkg.PushRequest) (pkg.Item, error)
	posts    []pkg.PushRequest
	gets     []pkg.ItemID
}

func (s *fakeStore) Get(ctx context.Context, itemID pkg.ItemID) (*pkg.Item, error) {
	s.gets = append(s.gets, itemID)
	if s.getFunc != nil {
		return s.getFunc(ctx, itemID)
	}
	return nil, pkg.ErrItemNotFound
}

func (s *fakeStore) Post(ctx context.Context, request pkg.PushRequest) (pkg.Item, error) {
	s.posts = append(s.posts, request)
	if s.postFunc != nil {
		return s.postFunc(ctx, request)
	}
	return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
}

// fakeRunner records every command it is asked to run.
type fakeRunner struct {
	rc    int
	err   error
	calls []string
	onRun func()
}

func (r *fakeRunner) Run(_ context.Context, cmd, _ string) (int, error) {
	r.calls = append(r.calls, cmd)
	if r.onRun != nil {
		r.onRun()
	}
	return r.rc, r.err
}

// answeredCard builds an item already answered with label.
func answeredCard(cmd, cwd, label string) *pkg.Item {
	return &pkg.Item{
		ItemID:         pkg.ItemID(testItemID),
		ProducerID:     pkg.ProducerID("producer-1"),
		State:          pkg.AnsweredState,
		Payload:        pkg.Payload(renderPayload(cmd, cwd, "")),
		Answer:         &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: label},
		AnsweredBy:     "operator",
		AnsweredClient: &pkg.AnsweredClient{UserAgent: "board", RemoteAddr: "127.0.0.1"},
	}
}

var _ = Describe("approve-run", func() {
	var (
		ctx   context.Context
		dir   string
		store *fakeStore
		run_  *fakeRunner
		clock *fakeClock
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = os.MkdirTemp("", "approve-run-test")
		Expect(err).NotTo(HaveOccurred())
		store = &fakeStore{}
		run_ = &fakeRunner{}
		clock = &fakeClock{now: time.Now()}
	})

	AfterEach(func() {
		_ = os.RemoveAll(dir)
	})

	newOptions := func(cmd string) options {
		return options{
			Cmd:       cmd,
			Cwd:       dir,
			Wait:      0,
			Interval:  minInterval,
			SessionID: "session-1",
			StateDir:  dir,
			Stdout:    &bytes.Buffer{},
			Stderr:    &bytes.Buffer{},
			Store:     store,
			Runner:    run_,
			Clock:     clock,
		}
	}

	stdoutOf := func(opts options) string {
		buffer, ok := opts.Stdout.(*bytes.Buffer)
		Expect(ok).To(BeTrue())
		return buffer.String()
	}

	Describe("output tokens and exit codes", func() {
		It("runs the command and passes its own exit code through", func() {
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) {
				return answeredCard("echo hi", dir, approveLabel), nil
			}
			run_.rc = 7

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(7))
			Expect(stdoutOf(opts)).To(ContainSubstring("CARD: " + testItemID))
			Expect(stdoutOf(opts)).To(ContainSubstring("APPROVED: " + testItemID))
			Expect(stdoutOf(opts)).To(ContainSubstring("EXIT: 7"))
			Expect(run_.calls).To(Equal([]string{"echo hi"}))
		})

		It("declines without running", func() {
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) {
				return answeredCard("echo hi", dir, declineLabel), nil
			}

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitDeclined))
			Expect(stdoutOf(opts)).To(ContainSubstring("DECLINED: " + testItemID))
			Expect(run_.calls).To(BeEmpty())
		})

		It("hides without running", func() {
			item := answeredCard("echo hi", dir, "")
			item.Answer = &pkg.Answer{Kind: pkg.HiddenAnswerKind}
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) { return item, nil }

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitHidden))
			Expect(stdoutOf(opts)).To(ContainSubstring("HIDDEN: " + testItemID))
			Expect(run_.calls).To(BeEmpty())
		})

		It("pends when unanswered past the wait", func() {
			open := answeredCard("echo hi", dir, "")
			open.State = pkg.OpenState
			open.Answer = nil
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) { return open, nil }

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitPending))
			Expect(stdoutOf(opts)).To(ContainSubstring("PENDING: " + testItemID))
			Expect(run_.calls).To(BeEmpty())
		})

		It("fails when the store errors", func() {
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) {
				return nil, errors.New("boom")
			}

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitStoreFailure))
			Expect(stdoutOf(opts)).To(ContainSubstring("FAILED:"))
			Expect(run_.calls).To(BeEmpty())
		})
	})

	Describe("the command is parsed back out of the card", func() {
		It("refuses when the card's command is not the one posted", func() {
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) {
				return answeredCard("make buca", dir, approveLabel), nil
			}

			opts := newOptions("make test")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(stdoutOf(opts)).To(ContainSubstring("REFUSED:"))
			Expect(run_.calls).To(BeEmpty())
		})

		It("refuses a card with no readable command", func() {
			item := answeredCard("echo hi", dir, approveLabel)
			item.Payload = pkg.Payload("no fenced block here")
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) { return item, nil }

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(run_.calls).To(BeEmpty())
		})
	})

	Describe("attribution guards", func() {
		It("refuses an answer with no answered_client", func() {
			item := answeredCard("echo hi", dir, approveLabel)
			item.AnsweredClient = nil
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) { return item, nil }

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(stdoutOf(opts)).To(ContainSubstring("answered_client"))
			Expect(run_.calls).To(BeEmpty())
		})

		It("refuses an automation answer", func() {
			automated := true
			item := answeredCard("echo hi", dir, approveLabel)
			item.AnsweredClient.Automation = &automated
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) { return item, nil }

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(stdoutOf(opts)).To(ContainSubstring("automation: true"))
			Expect(run_.calls).To(BeEmpty())
		})

		It("refuses self-approval by the poster", func() {
			item := answeredCard("echo hi", dir, approveLabel)
			item.AnsweredBy = "producer-1"
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) { return item, nil }

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(stdoutOf(opts)).To(ContainSubstring("self-approval"))
			Expect(run_.calls).To(BeEmpty())
		})
	})

	Describe("the decline cooldown", func() {
		It("does not re-ask inside the cooldown", func() {
			cmd := "echo hi"
			absDir, err := filepath.Abs(dir)
			Expect(err).NotTo(HaveOccurred())
			key := stateKey(cmd, absDir)
			Expect(
				saveState(ctx, dir, key, state{ItemID: testItemID, DeclinedAt: nowISO(time.Now())}),
			).
				To(Succeed())

			opts := newOptions(cmd)
			code := run(ctx, opts)

			Expect(code).To(Equal(exitDeclined))
			Expect(stdoutOf(opts)).To(ContainSubstring("not re-asking inside"))
			Expect(store.posts).To(BeEmpty())
			Expect(run_.calls).To(BeEmpty())
		})
	})

	Describe("single-use used_at", func() {
		It("writes used_at before the command starts", func() {
			store.postFunc = func(context.Context, pkg.PushRequest) (pkg.Item, error) {
				return pkg.Item{ItemID: pkg.ItemID(testItemID)}, nil
			}
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) {
				return answeredCard("echo hi", dir, approveLabel), nil
			}
			var usedAtAtRun string
			run_.onRun = func() {
				absDir, _ := filepath.Abs(dir)
				usedAtAtRun = loadState(dir, stateKey("echo hi", absDir)).UsedAt
			}

			opts := newOptions("echo hi")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitOK))
			Expect(usedAtAtRun).NotTo(BeEmpty(), "used_at must be written before the command runs")
		})
	})

	Describe("resume and expect-option citation", func() {
		It("refuses a malformed item id", func() {
			opts := newOptions("")
			opts.Resume = "not-hex"
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(store.gets).To(BeEmpty())
		})

		It("refuses a citation that resolves to no item", func() {
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) {
				return nil, pkg.ErrItemNotFound
			}
			opts := newOptions("")
			opts.Resume = testItemID
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(stdoutOf(opts)).To(ContainSubstring("no store item"))
		})

		It("refuses a citation answered with a different option", func() {
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) {
				return answeredCard("echo hi", dir, declineLabel), nil
			}
			opts := newOptions("")
			opts.Resume = testItemID
			opts.ExpectOption = approveLabel
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(stdoutOf(opts)).To(ContainSubstring("expected"))
			Expect(run_.calls).To(BeEmpty())
		})

		It("runs when the citation matches", func() {
			store.getFunc = func(context.Context, pkg.ItemID) (*pkg.Item, error) {
				return answeredCard("echo hi", dir, approveLabel), nil
			}
			run_.rc = 0
			opts := newOptions("")
			opts.Resume = testItemID
			opts.ExpectOption = approveLabel
			code := run(ctx, opts)

			Expect(code).To(Equal(exitOK))
			Expect(run_.calls).To(Equal([]string{"echo hi"}))
		})
	})

	Describe("the secret-leak refusal", func() {
		It("refuses a command that prints a secret", func() {
			opts := newOptions("kubectl get secret foo -o yaml")
			code := run(ctx, opts)

			Expect(code).To(Equal(exitRefused))
			Expect(stdoutOf(opts)).To(ContainSubstring("Secret/ConfigMap"))
			Expect(store.posts).To(BeEmpty())
		})
	})
})
