# attention-controller

[![Go Reference](https://pkg.go.dev/badge/github.com/bborbe/attention-controller.svg)](https://pkg.go.dev/github.com/bborbe/attention-controller)
[![CI](https://github.com/bborbe/attention-controller/actions/workflows/ci.yml/badge.svg)](https://github.com/bborbe/attention-controller/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/bborbe/attention-controller)](https://goreportcard.com/report/github.com/bborbe/attention-controller)

**A template/attention-controller project for creating new Go microservices.** This project demonstrates patterns and integration of common technologies, serving as a copy-paste starting point for new services rather than a complete production implementation.

---

## Table of Contents

* [Requirements](#requirements)
* [Technologies Demonstrated](#technologies-demonstrated)
* [Installation](#installation)
* [Quick Start](#quick-start)
* [Standard Endpoints](#standard-endpoints)
* [Development Commands](#development-commands)
* [Configuration](#configuration)
* [Deployment](#deployment)
  * [Kubernetes](#kubernetes)
  * [Docker](#docker)
* [Architecture](#architecture)
* [API Documentation](#api-documentation)
* [Testing](#testing)
* [Usage as Template](#usage-as-template)
* [License](#license)

---

## Requirements

- **Go 1.27+** (tested with Go 1.27.2 or later)
- **Docker** (for containerized builds)
- **Make** (for development commands)
- **Kafka** (optional, for message broker features)

---

## Technologies Demonstrated

- **Go**: Modern Go service with proper project structure
- **Docker**: Containerization with multi-stage builds
- **Kubernetes**: Complete deployment manifests (Deployment/StatefulSet options)
- **Kafka**: Message broker integration with producers/consumers
- **BoltDB**: Local embedded database for persistent storage
- **HTTP Services**: RESTful API with standard endpoints
- **Monitoring**: Prometheus metrics and Sentry error tracking
- **Security**: Comprehensive vulnerability scanning and license management

---

## Installation

As a template project, you typically copy this repository rather than installing it as a dependency. However, if you need to reference it:

```bash
go get github.com/bborbe/attention-controller
```

## Quick Start

```bash
# Install dependencies
make deps

# Run the service locally
make run

# Run full development workflow
make precommit
```

## Standard Endpoints

| Endpoint | Description |
|----------|-------------|
| `/healthz` | Health check |
| `/readiness` | Readiness check |
| `/metrics` | Prometheus metrics |
| `/resetdb` | Database reset |
| `/setloglevel/{level}` | Dynamic log level adjustment |
| `/testloglevel` | Test logging at all levels |
| `/sentryalert` | Test Sentry integration |
| `/debug/pprof/` | Go runtime profiles — CPU (`?seconds=N`), heap, goroutine, cmdline, trace. ⚠️ Unauthenticated, and mounted **only when the listen address is loopback**: `/cmdline` publishes the process argv, which can carry `-attention-store-token`. A non-loopback bind logs and skips the registration. |

## Development Commands

```bash
make ensure    # Dependency management (go mod tidy, verify)
make format    # Code formatting (gofmt, goimports-reviser, golines)
make generate  # Code generation
make test      # Run tests with coverage
make check     # Security and quality checks (vet, errcheck, vulncheck, osv-scanner, trivy)
make precommit # Complete development workflow
```

## Configuration

Configuration is managed through environment variables. See `example.env` for development defaults.

### Runtime Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `KAFKA_BROKERS` | Kafka broker addresses | `localhost:9092` |
| `LISTEN` | HTTP server listen address | `:8080` |
| `SENTRY_DSN` | Sentry error tracking DSN | (required for production) |
| `SENTRY_PROXY` | Sentry proxy URL | (optional) |
| `DATADIR` | Database storage directory | (required) |
| `BATCH_SIZE` | Kafka batch consume size | `1` |

### Build/Deployment Configuration

| Variable | Description |
|----------|-------------|
| `DOCKER_REGISTRY` | Docker registry for image publishing |
| `IMAGE` | Docker image name |
| `CLUSTER_CONTEXT` | Kubernetes cluster context |
| `BUILD_GIT_VERSION` | Build version (auto-set in CI/Docker) |
| `BUILD_GIT_COMMIT` | Build commit hash (auto-set in CI/Docker) |
| `BUILD_DATE` | Build timestamp (auto-set in CI/Docker) |

## Deployment

### Local (launchd)

There is no Kubernetes deploy. The store runs on this machine as the launchd job
`com.bborbe.attention-controller`, serving `http://127.0.0.1:18080`:

```bash
make install   # go build . → codesign with a stable identity → launchctl kickstart
```

⚠️ **Sign every deploy — never install a plain `go build` binary.** The service
reads the vault under `~/Documents` at startup, and macOS privacy control (TCC)
keys that grant to the binary's signature. A plain `go build` binary is ad-hoc
signed, so every rebuild is a new identity and startup blocks in `open()` on the
vault until access is granted again — the board and the `:1337` jump listener
stay down meanwhile (2026-10-03, ~38 min). `make install` signs with
`CODESIGN_IDENTITY` (default `bborbe local codesign`) and the fixed identifier
`de.bborbe.attention-controller`, so one Documents / Full Disk Access grant
survives rebuilds. The identity must be in the login keychain:
`security find-identity -p codesigning`.

⚠️ **Build the package (`.`), never the file list (`main.go`).** The two forms
differ in one way that matters here: `go build main.go` builds a list of files
rather than a package, and that **suppresses Go's automatic VCS stamping**. A
binary built that way carries no `vcs.revision`, so it cannot say which source it
came from — and the board's footer renders an explicit "no build identity" line
instead of the version and sha. The recipe above is what keeps the stamp.

Confirm the deployed artifact's own identity with:

```bash
go version -m ~/.local/bin/attention-controller | grep vcs.revision
```

That reads the running binary, not the checkout: it answers *which build is
deployed*, which is the question the footer exists to answer. The binary's mtime
does not — it proves a build happened, not what was built.

The plist lives at `~/Library/LaunchAgents/com.bborbe.attention-controller.plist`.

### Docker

Multi-stage Docker build included. See `Dockerfile` and `Makefile.docker`.

## Architecture

Standard Go microservice architecture:

- `main.go` - Application entry point with dependency injection
- `pkg/factory/` - Factory functions for handler creation
- `pkg/handler/` - HTTP handlers for various endpoints
- Uses proven libraries for service framework, HTTP handling, Kafka, database, and monitoring

## API Documentation

For complete API documentation of the handlers and packages, visit [pkg.go.dev](https://pkg.go.dev/github.com/bborbe/attention-controller).

---

## Testing

Uses Ginkgo BDD framework with Gomega matchers. Run tests with `make test` or `ginkgo run ./...`.

Example tests are included to demonstrate testing patterns - copy and adapt these when building your service.

## Usage as Template

1. Copy this repository as a starting point for your new service
2. Update service name throughout (replace "attention-controller")
3. Modify handlers and add your business logic
4. Update configuration in `example.env` and K8s manifests
5. Implement your specific Kafka consumers/producers and database schema

---

## License

BSD-style license. See [LICENSE](LICENSE) file for details.
