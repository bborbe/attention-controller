// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

const (
	reaperUID     = 501
	reaperSelfPID = 4242
	frameworkPy   = "/Library/Frameworks/Python.framework/Versions/3.14/Resources/Python.app/Contents/MacOS/Python"
	cacheWatch    = "/Users/u/.claude/plugins/cache/claude-supervisor/supervisor/0.357.0/scripts/manager-attention-watch.py"
	marketWatch   = "/Users/u/.claude/plugins/marketplaces/claude-supervisor/scripts/manager-attention-watch.py"
	cachePoll     = "/Users/u/.claude/plugins/cache/claude-supervisor/supervisor/0.352.7/scripts/permission-answer-poll.py"
	cacheFilter   = "/Users/u/.claude/plugins/cache/claude-supervisor/supervisor/0.358.1/scripts/gate-owner-filter.py"
)

func watcherProcess(pid int, args ...string) pkg.Process {
	return pkg.Process{PID: pid, PPID: 100, UID: reaperUID, Args: args}
}

var _ = Describe("ParsePsOutput", func() {
	It("parses pid, ppid, uid and the split command line", func() {
		processes := pkg.ParsePsOutput("  123   1  501 /usr/bin/python3 /a/b.py --x 1\n")
		Expect(processes).To(HaveLen(1))
		Expect(processes[0]).To(Equal(pkg.Process{
			PID: 123, PPID: 1, UID: 501, Args: []string{"/usr/bin/python3", "/a/b.py", "--x", "1"},
		}))
	})

	It("skips blank, short and non-numeric lines instead of failing the read", func() {
		processes := pkg.ParsePsOutput("\nabc 1 501 /bin/sh\n12 1\n7 1 501 /bin/zsh\n")
		Expect(processes).To(HaveLen(1))
		Expect(processes[0].PID).To(Equal(7))
	})
})

const testRetiredWatchers = "manager-attention-watch,permission-answer-poll"

var _ = Describe("ParseRetiredWatchers", func() {
	It("trims blanks and a trailing .py", func() {
		Expect(
			pkg.ParseRetiredWatchers(" manager-attention-watch.py , ,permission-answer-poll"),
		).To(Equal(map[string]bool{
			"manager-attention-watch": true,
			"permission-answer-poll":  true,
		}))
	})

	It("returns an empty set for an empty list", func() {
		Expect(pkg.ParseRetiredWatchers("")).To(BeEmpty())
	})

	It("parses the default list into both retired scripts", func() {
		Expect(pkg.ParseRetiredWatchers(testRetiredWatchers)).To(HaveLen(2))
	})
})

var _ = Describe("RetiredWatcherMatcher", func() {
	var retired map[string]bool

	BeforeEach(func() {
		retired = pkg.ParseRetiredWatchers(testRetiredWatchers)
	})

	match := func(p pkg.Process) (pkg.RetiredWatcher, bool) {
		return pkg.NewRetiredWatcherMatcher(retired, reaperUID, reaperSelfPID).Match(p)
	}

	It("matches nothing with an empty retired list", func() {
		retired = map[string]bool{}
		_, ok := match(watcherProcess(21, frameworkPy, cacheWatch))
		Expect(ok).To(BeFalse())
	})

	It("matches a retired script from a plugin cache version", func() {
		got, ok := match(watcherProcess(10, frameworkPy, cacheWatch, "--tracked", "x"))
		Expect(ok).To(BeTrue())
		Expect(got.Script).To(Equal("manager-attention-watch"))
		Expect(got.Version).To(Equal("0.357.0"))
	})

	It("matches a retired script from the marketplace clone", func() {
		got, ok := match(watcherProcess(11, "python3", marketWatch))
		Expect(ok).To(BeTrue())
		Expect(got.Version).To(Equal("marketplace"))
	})

	It("skips interpreter flags before the script path", func() {
		got, ok := match(watcherProcess(12, "/opt/homebrew/bin/python3.15", "-I", "-u", cachePoll))
		Expect(ok).To(BeTrue())
		Expect(got.Script).To(Equal("permission-answer-poll"))
	})

	It("refuses a script that is not on the retired list", func() {
		_, ok := match(watcherProcess(13, frameworkPy, cacheFilter, "--feed"))
		Expect(ok).To(BeFalse())
	})

	It("refuses another user's process", func() {
		p := watcherProcess(14, frameworkPy, cacheWatch)
		p.UID = 0
		_, ok := match(p)
		Expect(ok).To(BeFalse())
	})

	It("refuses its own pid", func() {
		_, ok := match(watcherProcess(reaperSelfPID, frameworkPy, cacheWatch))
		Expect(ok).To(BeFalse())
	})

	It("refuses pid 1", func() {
		_, ok := match(watcherProcess(1, frameworkPy, cacheWatch))
		Expect(ok).To(BeFalse())
	})

	It("refuses a non-Python process whose command line names the script", func() {
		_, ok := match(watcherProcess(15, "/bin/zsh", "-c", "eval 'python3 "+cacheWatch+"'"))
		Expect(ok).To(BeFalse())
		_, ok = match(watcherProcess(16, "/usr/bin/tail", cacheWatch))
		Expect(ok).To(BeFalse())
	})

	It("refuses a retired name outside the claude-supervisor plugin layout", func() {
		_, ok := match(watcherProcess(17, "python3", "/tmp/manager-attention-watch.py"))
		Expect(ok).To(BeFalse())
		_, ok = match(
			watcherProcess(
				18,
				"python3",
				"/Users/u/.claude/plugins/cache/other/x/1.0/scripts/manager-attention-watch.py",
			),
		)
		Expect(ok).To(BeFalse())
	})

	It("refuses a process with no script argument", func() {
		_, ok := match(watcherProcess(19, "python3"))
		Expect(ok).To(BeFalse())
		_, ok = match(watcherProcess(20, "python3", "-I"))
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("WatcherReaperJob", func() {
	var (
		ctx       context.Context
		lister    *mocks.ProcessLister
		signaller *mocks.ProcessSignaller
		job       pkg.WatcherReaperJob
		retired   map[string]bool
	)

	BeforeEach(func() {
		ctx = context.Background()
		lister = &mocks.ProcessLister{}
		signaller = &mocks.ProcessSignaller{}
		retired = pkg.ParseRetiredWatchers(testRetiredWatchers)
		lister.ListReturns([]pkg.Process{
			watcherProcess(10, frameworkPy, cacheWatch),
			watcherProcess(11, frameworkPy, cacheFilter, "--feed"),
			watcherProcess(12, "python3", cachePoll),
			watcherProcess(13, "/bin/zsh", "-c", cacheWatch),
		}, nil)
	})

	JustBeforeEach(func() {
		job = pkg.NewWatcherReaperJob(
			lister,
			pkg.NewRetiredWatcherMatcher(retired, reaperUID, reaperSelfPID),
			signaller,
			time.Minute,
		)
	})

	terminatedPIDs := func() []int {
		var pids []int
		for i := 0; i < signaller.TerminateCallCount(); i++ {
			_, pid := signaller.TerminateArgsForCall(i)
			pids = append(pids, pid)
		}
		return pids
	}

	It("terminates every retired watcher and nothing else", func() {
		reaped, err := job.ReapOnce(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(reaped).To(Equal(2))
		Expect(terminatedPIDs()).To(Equal([]int{10, 12}))
	})

	It("does not count a process that exited before the signal", func() {
		signaller.TerminateReturnsOnCall(0, pkg.ErrProcessGone)
		reaped, err := job.ReapOnce(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(reaped).To(Equal(1))
	})

	It("keeps going after a refused signal", func() {
		signaller.TerminateReturnsOnCall(0, stderrors.New("operation not permitted"))
		reaped, err := job.ReapOnce(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(reaped).To(Equal(1))
		Expect(terminatedPIDs()).To(Equal([]int{10, 12}))
	})

	It("returns the list failure without signalling anything", func() {
		lister.ListReturns(nil, stderrors.New("ps failed"))
		_, err := job.ReapOnce(ctx)
		Expect(err).To(HaveOccurred())
		Expect(signaller.TerminateCallCount()).To(Equal(0))
	})

	It("runs one pass, then stops when the context is cancelled", func() {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		Expect(job.Run(cancelled)).To(Succeed())
		Expect(lister.ListCallCount()).To(Equal(1))
	})

	It("refuses a non-positive interval", func() {
		job = pkg.NewWatcherReaperJob(
			lister,
			pkg.NewRetiredWatcherMatcher(retired, reaperUID, reaperSelfPID),
			signaller,
			0,
		)
		Expect(job.Run(ctx)).NotTo(Succeed())
	})
})
