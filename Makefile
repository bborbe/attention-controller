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
	@go run -mod=mod main.go \
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
e2e:
	go run $(PLAYWRIGHT_GO_MODULE)/cmd/playwright@$(PLAYWRIGHT_GO_VERSION) install chromium
	go test -mod=mod -tags e2e -count=1 -timeout 15m ./e2e/

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

#
# The binary is built and signed at a staging path and only moved into place
# once signed and verified, so a failed codesign never leaves an unsigned binary
# where the plist will load it on the next start.
LAUNCHD_LABEL ?= com.bborbe.attention-controller

.PHONY: install
install:
	@security find-identity -p codesigning | grep -qF '"$(CODESIGN_IDENTITY)"' \
		|| { echo "codesign identity '$(CODESIGN_IDENTITY)' not in keychain; check: security find-identity -p codesigning" >&2; exit 1; }
	mkdir -p "$(dir $(INSTALL_BIN))"
	go build -o "$(INSTALL_BIN).new" .
	codesign -f -s "$(CODESIGN_IDENTITY)" -i de.bborbe.attention-controller "$(INSTALL_BIN).new"
	codesign --verify --strict "$(INSTALL_BIN).new"
	codesign -d -r- "$(INSTALL_BIN).new" 2>&1 | grep -q 'identifier "de.bborbe.attention-controller" and certificate leaf = H"'
	mv -f "$(INSTALL_BIN).new" "$(INSTALL_BIN)"
	launchctl kickstart -k gui/$$(id -u)/$(LAUNCHD_LABEL)

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
