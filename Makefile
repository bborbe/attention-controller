include Makefile.variables
include Makefile.precommit
include Makefile.docker
include example.env

SERVICE = bborbe/attention-controller

# Local launchd service, not a cluster deploy. The store runs on this machine as
# the launchd job com.bborbe.attention-controller, built straight to
# ~/.local/bin/attention-controller and serving localhost:18080. There is no
# in-repo k8s/ tree, no cluster manifests, and no image is pushed to a registry —
# see CLAUDE.md § Deploy.
# No sentry flag here, matching notification-controller's run target. The
# skeleton's version resolved -sentry-dsn from teamvault at run time, which
# made a local run depend on a teamvault key this repo never declares
# (SENTRY_DSN_KEY is in neither example.env nor Makefile.variables) and on
# ~/.teamvault.json existing — so `make run` failed at argument parsing before
# the server started. Sentry is error reporting for a deployed stage, and the
# local rung does not need it — this repo has no deployed stage yet, so nothing
# here depends on the flag.
run:
	@go run -mod=mod . \
	-listen="localhost:${ATTENTION_CONTROLLER_PORT}" \
	-datadir="data" \
	-v=2

# Drives the attention board in a real browser against a real binary.
#
# Deliberately NOT part of `test` or `precommit`: the package carries a
# `//go:build e2e` tag, so `go list ./...` does not even see it and neither
# target changes behaviour. E2E is never run automatically — it is triggered
# here, and in the release path by the dark-factory scenario that wraps it.
#
# The suite builds the binary itself and runs it on a random port against a
# temp DATADIR and a temp session registry, so it never touches the launchd
# service on :18080.
.PHONY: e2e
e2e: frontend
	go run $(PLAYWRIGHT_GO_MODULE)/cmd/playwright@$(PLAYWRIGHT_GO_VERSION) install chromium
	@# Both suites always run: a Go failure must not hide the frontend result.
	@# The target still exits non-zero if either suite failed.
	@go_rc=0; fe_rc=0; \
		go test -mod=mod -tags e2e -count=1 -timeout 25m ./e2e/ || go_rc=$$?; \
		(cd $(FRONTEND_DIR) && npx playwright install chromium && npm run e2e) || fe_rc=$$?; \
		echo "e2e: go suite rc=$$go_rc, frontend suite rc=$$fe_rc"; \
		[ $$go_rc -eq 0 ] && [ $$fe_rc -eq 0 ]

# The React frontend (frontend/), built into frontend/dist and embedded with
# go:embed. types first: tsc imports ./types, which tygo generates from pkg.
FRONTEND_DIR ?= frontend

.PHONY: frontend
frontend:
	cd $(FRONTEND_DIR) && npm ci && npm run build

.PHONY: types
types:
	go run github.com/gzuidhof/tygo@$(TYGO_VERSION) generate

.PHONY: test-frontend
test-frontend:
	cd $(FRONTEND_DIR) && npm ci && npm test

.PHONY: check-frontend
check-frontend:
	cd $(FRONTEND_DIR) && npm ci && npm audit --audit-level=high

# Builds the launchd binary and signs it with a stable local identity, then
# restarts the service.
#
# The signature is what keeps macOS privacy control (TCC) working across
# deploys. The service reads the vault under ~/Documents at startup, and TCC
# keys that grant to the binary's designated requirement. A plain `go build`
# binary is ad-hoc signed, so its requirement is its cdhash and every rebuild
# is a new identity: startup then blocks in open() on the vault until access is
# granted again (outage 2026-10-03, board and :1337 down ~38 min). Signing
# with a fixed identity and identifier makes the requirement
# `identifier "de.bborbe.attention-controller" and certificate leaf = H"…"`,
# which survives rebuilds, so one grant holds.
#
# The package (`.`) is built, never `main.go`, so the binary keeps its VCS stamp.
# CODESIGN_IDENTITY must exist in the login keychain
# (`security find-identity -p codesigning`).
CODESIGN_IDENTITY ?= bborbe local codesign
INSTALL_BIN ?= $(HOME)/.local/bin/attention-controller
# The permission-answer hook is a second binary installed from this repo. It is
# not the launchd service, so it carries no .prev rollback: a failed hook start
# cannot take the board down.
INSTALL_HOOK_BIN ?= $(HOME)/.local/bin/attention-controller-permission-answer-hook
# The approve-run helper is a third binary installed from this repo: the client
# that posts a command-approval card and runs the approved command as a child of
# the agent's own Bash call. It has its own target (install-approve-run) so a
# client install never swaps the daemon binary.
INSTALL_APPROVE_RUN_BIN ?= $(HOME)/.local/bin/attention-controller-approve-run

#
# The binary is built and signed at a staging path and only moved into place
# once signed and verified, so a failed codesign never leaves an unsigned binary
# where the plist will load it on the next start.
LAUNCHD_LABEL ?= com.bborbe.attention-controller
LAUNCHD_PLIST ?= $(HOME)/Library/LaunchAgents/$(LAUNCHD_LABEL).plist

# Enforces the launchd baseline on the installed plist, which stays hand-installed
# because it carries secrets: ThrottleInterval 30 and NumberOfFiles 4096. A
# KeepAlive job relaunching an invalidly signed binary at launchd's 10 s default
# preceded the 2026-10-17 kernel panic. Idempotent; touches only these two keys.
.PHONY: launchd-baseline
launchd-baseline:
	@test -f "$(LAUNCHD_PLIST)" || { echo "plist not installed: $(LAUNCHD_PLIST)" >&2; exit 1; }
	/usr/libexec/PlistBuddy -c 'Set :ThrottleInterval 30' "$(LAUNCHD_PLIST)" 2>/dev/null \
		|| /usr/libexec/PlistBuddy -c 'Add :ThrottleInterval integer 30' "$(LAUNCHD_PLIST)"
	/usr/libexec/PlistBuddy -c 'Set :SoftResourceLimits:NumberOfFiles 4096' "$(LAUNCHD_PLIST)" 2>/dev/null \
		|| /usr/libexec/PlistBuddy -c 'Add :SoftResourceLimits:NumberOfFiles integer 4096' "$(LAUNCHD_PLIST)"
	plutil -lint "$(LAUNCHD_PLIST)"

# The deploy gate. Every entry point runs THIS target, so the readiness proof,
# the two failure classes and the rollback are implemented once — in
# `cmd/deploy` — instead of once per caller.
#
# ⚠️ It is a gate, not a restart. The driver logs `t_deploy_start` at the
# launchctl bootstrap, polls /readiness, /healthz and the serving footer every
# 2 s for 90 s, and on failure restores `$(INSTALL_BIN).prev` and restarts
# inside a 30 s budget — so a start plus a rollback can never exceed two
# minutes. It exits non-zero on both a rolled-back deploy and a dark board, and
# posts the operator's card through the board's own push route once the previous
# board serves again.
#
# bootout + bootstrap, never `kickstart -k`: kickstart keeps the plist launchd
# cached at bootstrap, so a baseline edit would not apply. bootout is
# asynchronous and the driver waits for the job to be gone before bootstrapping,
# or bootstrap fails with `5: Input/output error`.
#
# ⚠️ A restart with no rebuild runs the same gate. That is deliberate: the
# 2026-11-17 outage was a start that never bound the port, and `launchd-reload`
# alone can strand the board exactly the same way.
#
# `EXPECT` is the commit the gate must see serving. `make install` sets it to the
# checkout's HEAD, so a stale binary aborts before anything restarts instead of
# re-serving the old build and reporting exit 0. Left empty, the gate falls back
# to the installed binary's own identity — correct for a restart-only reload,
# which is meant to serve whatever is installed.
DEPLOY_MODE ?= reload
EXPECT ?=
.PHONY: launchd-reload
launchd-reload:
	@test -f "$(LAUNCHD_PLIST)" || { echo "plist not installed: $(LAUNCHD_PLIST)" >&2; exit 1; }
	go run -mod=mod ./cmd/deploy \
		-mode=$(DEPLOY_MODE) \
		-bin="$(INSTALL_BIN)" \
		-label="$(LAUNCHD_LABEL)" \
		-plist="$(LAUNCHD_PLIST)" \
		$(if $(EXPECT),-expect="$(EXPECT)")

# Builds, signs and installs the binary WITHOUT restarting the service.
#
# ⚠️ The previous binary is retained as `.prev` before the swap, and only when
# it is a DIFFERENT commit. `make install` overwrites the binary in place with no
# copy, so without this a failed start has nothing to roll back to; and
# reinstalling the SAME commit must not overwrite the last good copy with an
# identical build, which would make the rollback restore the very binary that
# just failed.
#
# ⚠️ The whole build → sign → verify → retain → swap sequence runs in ONE shell
# with a stage path unique to that shell (`$$$$`). A single shared
# `attention-controller.new` is not safe: two installs running at once — a
# re-run of the same approval card, a sibling session deploying the same repo —
# each `mv` the other's staging file, and the loser installs nothing while
# copying a half-built artifact into `.prev`. Measured 2026-11-17: two concurrent
# runs left `.prev` and the installed binary identical, both the staged bad build,
# with the board dark.
#
# ⚠️ The promote is PROVEN, not assumed. After the `mv`, the installed file's
# `vcs.revision` is re-read and must equal the staged build's; a mismatch exits
# non-zero naming both. Measured 2026-11-24: `make install` exited 0 while the
# live binary and `.prev` both stayed at the old commit — the gate could not see
# it because it polled the identity of the stale file. An unstamped build is
# refused for the same reason: it would serve no identity at all.
#
# ⚠️ Orphaned `<bin>.new.<pid>` stages are swept after a successful promote, but
# only those whose pid is no longer alive. A concurrent install's live stage is
# left alone (the collision the per-shell staging path exists to prevent); a
# killed install's corpse is removed so it cannot be mistaken for a live one.
.PHONY: install-bin
install-bin: frontend
	@security find-identity -p codesigning | grep -qF '"$(CODESIGN_IDENTITY)"' \
		|| { echo "codesign identity '$(CODESIGN_IDENTITY)' not in keychain; check: security find-identity -p codesigning" >&2; exit 1; }
	@test -f "$(LAUNCHD_PLIST)" || { echo "plist not installed: $(LAUNCHD_PLIST)" >&2; exit 1; }
	@mkdir -p "$(dir $(INSTALL_BIN))"
	@set -e; \
		stage="$(INSTALL_BIN).new.$$$$"; \
		trap 'rm -f "$$stage"' EXIT INT TERM; \
		go build -o "$$stage" .; \
		built="$$(go version -m "$$stage" | sed -n 's/.*vcs.revision=//p')"; \
		if [ -z "$$built" ]; then \
			echo "refusing to install an unstamped build: $(INSTALL_BIN) would serve no identity" >&2; \
			exit 1; \
		fi; \
		codesign -f -s "$(CODESIGN_IDENTITY)" -i de.bborbe.attention-controller "$$stage"; \
		codesign --verify --strict "$$stage"; \
		codesign -d -r- "$$stage" 2>&1 | grep -q 'identifier "de.bborbe.attention-controller" and certificate leaf = H"'; \
		if [ -f "$(INSTALL_BIN)" ] && [ "$$(go version -m "$(INSTALL_BIN)" | sed -n 's/.*vcs.revision=//p')" != "$$built" ]; then \
			cp -p "$(INSTALL_BIN)" "$(INSTALL_BIN).prev.$$$$"; \
			mv -f "$(INSTALL_BIN).prev.$$$$" "$(INSTALL_BIN).prev"; \
			echo "retained previous binary: $(INSTALL_BIN).prev"; \
		fi; \
		mv -f "$$stage" "$(INSTALL_BIN)"; \
		installed="$$(go version -m "$(INSTALL_BIN)" | sed -n 's/.*vcs.revision=//p')"; \
		if [ "$$installed" != "$$built" ]; then \
			echo "promote failed: built $$built but $(INSTALL_BIN) carries $$installed" >&2; \
			exit 1; \
		fi; \
		for orphan in "$(INSTALL_BIN)".new.*; do \
			[ -e "$$orphan" ] || continue; \
			pid="$${orphan##*.}"; \
			case "$$pid" in ''|*[!0-9]*) continue;; esac; \
			kill -0 "$$pid" 2>/dev/null || { rm -f "$$orphan"; echo "removed orphaned stage: $$orphan"; }; \
		done; \
		echo "installed $(INSTALL_BIN) commit=$$built"
	@set -e; \
		stage="$(INSTALL_HOOK_BIN).new.$$$$"; \
		trap 'rm -f "$$stage"' EXIT INT TERM; \
		go build -o "$$stage" ./cmd/permission-answer-hook; \
		codesign -f -s "$(CODESIGN_IDENTITY)" "$$stage"; \
		codesign --verify --strict "$$stage"; \
		mv -f "$$stage" "$(INSTALL_HOOK_BIN)"

# Builds, signs and installs ONLY the approve-run client, so installing it never
# swaps the daemon binary. Mirrors the permission-answer hook's staging block.
.PHONY: install-approve-run
install-approve-run:
	@security find-identity -p codesigning | grep -qF '"$(CODESIGN_IDENTITY)"' \
		|| { echo "codesign identity '$(CODESIGN_IDENTITY)' not in keychain; check: security find-identity -p codesigning" >&2; exit 1; }
	@mkdir -p "$(dir $(INSTALL_APPROVE_RUN_BIN))"
	@set -e; \
		stage="$(INSTALL_APPROVE_RUN_BIN).new.$$$$"; \
		trap 'rm -f "$$stage"' EXIT INT TERM; \
		go build -o "$$stage" ./cmd/approve-run; \
		codesign -f -s "$(CODESIGN_IDENTITY)" "$$stage"; \
		codesign --verify --strict "$$stage"; \
		mv -f "$$stage" "$(INSTALL_APPROVE_RUN_BIN)"

.PHONY: install
install: install-bin
	$(MAKE) launchd-baseline
	$(MAKE) launchd-reload DEPLOY_MODE=install EXPECT="$(shell git rev-parse HEAD)"

deps:
	go install github.com/bborbe/teamvault-utils/cmd/teamvault-config-parser@latest
	go install github.com/bborbe/teamvault-utils/cmd/teamvault-file@latest
	go install github.com/bborbe/teamvault-utils/cmd/teamvault-url@latest
	go install github.com/bborbe/teamvault-utils/cmd/teamvault-username@latest
	go install github.com/bborbe/teamvault-utils/cmd/teamvault-password@latest
	go install github.com/onsi/ginkgo/v2/ginkgo@v2.25.3
	sudo port install trivy

formatenv:
	cat example.env | sort > c
	mv c example.env

.PHONY: fix
fix:
	@for dir in $$(find `pwd` -type d -name vendor -prune -o -name go.mod -exec dirname "{}" \; | grep -v '^$$'); do \
		cd $${dir}; \
		echo "fix $${dir}"; \
		go get github.com/go-git/go-git/v5@latest; \
		go get github.com/containerd/containerd@latest; \
		go get golang.org/x/crypto@latest; \
		go get golang.org/x/net@latest; \
	done
