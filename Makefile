.PHONY: all build run run-scan test test-cover fmt vet lint tidy deps-check vuln \
        docker-build docker-push buildx-setup \
        clean install-hooks help

# ── Variables ─────────────────────────────────────────────────────────────────

CONFIG     ?= examples/config.yaml
LOCALBIN   ?= $(shell pwd)/bin
IMAGE      ?= ghcr.io/yuriy-kovalchuk/yk-update-checker
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PLATFORMS  ?= linux/amd64,linux/arm64
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG := github.com/yuriy-kovalchuk/yk-update-checker/internal/version
LDFLAGS    := -s -w \
  -X $(VERSION_PKG).Version=$(VERSION) \
  -X $(VERSION_PKG).Commit=$(GIT_COMMIT) \
  -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

# ── Default ───────────────────────────────────────────────────────────────────

all: tidy fmt vet lint build

# ── Code quality ──────────────────────────────────────────────────────────────

## fmt: format all Go source files
fmt:
	go fmt ./...

## vet: run go vet
vet:
	go vet ./...

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## tidy: tidy and verify go modules
tidy:
	go mod tidy
	go mod verify

## deps-check: list outdated direct dependencies
deps-check:
	@go list -u -m -f '{{if and (not .Indirect) .Update}}{{.Path}}  {{.Version}} → {{.Update.Version}}{{end}}' all \
	  | grep -v "^$$" \
	  || echo "All direct dependencies are up to date."

## vuln: check for known CVEs in the dependency tree
vuln:
	govulncheck ./...

# ── Test ──────────────────────────────────────────────────────────────────────

## test: run all tests with race detector
test:
	go test -race -timeout 120s ./...

## test-cover: run tests with coverage report
test-cover:
	go test -race -timeout 120s -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# ── Build ─────────────────────────────────────────────────────────────────────

## build: compile the update-checker binary
build:
	mkdir -p $(LOCALBIN)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(LOCALBIN)/update-checker ./cmd/update-checker

# ── Run ───────────────────────────────────────────────────────────────────────

## run: build and run the dashboard (serve mode) with verbose logging
run: build
	$(LOCALBIN)/update-checker serve --config $(CONFIG) --verbose

## run-scan: build and run a single one-shot scan, print results to stdout
run-scan: build
	$(LOCALBIN)/update-checker scan --config $(CONFIG) --verbose

# ── Docker ────────────────────────────────────────────────────────────────────

## buildx-setup: create or start the multi-platform buildx builder
buildx-setup:
	docker buildx create --name multiplatform --driver docker-container --bootstrap --use 2>/dev/null || \
	  docker buildx inspect --bootstrap multiplatform

## docker-build: build multi-arch image (does not push)
docker-build: buildx-setup
	docker buildx build \
	  --builder multiplatform \
	  --platform $(PLATFORMS) \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(GIT_COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) \
	  -t $(IMAGE):latest \
	  .

## docker-push: build and push multi-arch image to GHCR
docker-push: buildx-setup
	docker buildx build \
	  --builder multiplatform \
	  --platform $(PLATFORMS) \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(GIT_COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) \
	  -t $(IMAGE):latest \
	  --push \
	  .

# ── Misc ──────────────────────────────────────────────────────────────────────

## clean: remove build artefacts and coverage reports
clean:
	rm -rf bin/ coverage.out coverage.html

## install-hooks: configure git to use .githooks/
install-hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/*
	@echo "Git hooks installed."

## help: list available make targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //' | column -t -s ':'
