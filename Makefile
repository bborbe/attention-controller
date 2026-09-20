include Makefile.variables
include Makefile.precommit
include Makefile.docker
include example.env

SERVICE = bborbe/attention-controller

# Deploy-only repo, like notification-controller: the image is published to
# Docker Hub as an immutable semver tag, then mirrored into the quant registry
# and applied from the nuke manifests. There is no in-repo k8s/ tree — the
# deploy lives in nuke/attention-controller/, per [[Deploy Mirrored Agent Service]].
run:
	@go run -mod=mod main.go \
	-sentry-dsn="$(shell teamvault-url --teamvault-config ~/.teamvault.json --teamvault-key=${SENTRY_DSN_KEY})" \
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
