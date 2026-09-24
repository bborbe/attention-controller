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
