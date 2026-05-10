.PHONY: build run run-api run-scanner run-dashboard lint fmt vet tidy deps-check install-hooks test test-cover docker-build docker-push clean help

CONFIG     ?= examples/config.yaml
IMAGE      ?= ghcr.io/yuriy-kovalchuk
VERSION    ?= dev
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE       := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
PKG        := github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/config
PLATFORMS  ?= linux/amd64,linux/arm64

LDFLAGS := -ldflags "-s -w \
  -X $(PKG).Version=$(VERSION) \
  -X $(PKG).Commit=$(COMMIT) \
  -X $(PKG).BuildDate=$(DATE)"

## build: compile all binaries for the current platform
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o bin/update-checker-api ./cmd/update-checker-api
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o bin/update-checker-scanner ./cmd/update-checker-scanner
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o bin/update-checker-dashboard ./cmd/update-checker-dashboard

## run-api: run the API server
run-api: build
	./bin/update-checker-api -db /tmp/update-checker.db

## run-scanner: run the scanner (requires API to be running)
run-scanner: build
	./bin/update-checker-scanner -api-url http://localhost:8080 -config $(CONFIG)

## run-dashboard: run the dashboard (requires API to be running)
run-dashboard: build
	./bin/update-checker-dashboard -api-url http://localhost:8080

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## fmt: format all Go source files
fmt:
	gofmt -w -s .

## vet: run go vet
vet:
	go vet ./...

## tidy: tidy and verify go modules
tidy:
	go mod tidy
	go mod verify

## deps-check: list outdated direct dependencies (requires jq)
deps-check:
	go list -u -m -json all 2>/dev/null | jq -r 'select(.Update) | "\(.Path): \(.Version) → \(.Update.Version)"'

## install-hooks: configure git to use .githooks/
install-hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/*
	@echo "Git hooks installed."

## test: run all tests
test:
	go test -race -timeout 120s ./...

## test-cover: run tests with coverage report
test-cover:
	go test -race -timeout 120s -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## docker-build: build all Docker images
docker-build:
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t $(IMAGE)/update-checker-api:$(VERSION) -t $(IMAGE)/update-checker-api:latest -f Dockerfile.api .
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t $(IMAGE)/update-checker-scanner:$(VERSION) -t $(IMAGE)/update-checker-scanner:latest -f Dockerfile.scanner .
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t $(IMAGE)/update-checker-dashboard:$(VERSION) -t $(IMAGE)/update-checker-dashboard:latest -f Dockerfile.dashboard .

## docker-push: build and push all Docker images
docker-push:
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t $(IMAGE)/update-checker-api:$(VERSION) -t $(IMAGE)/update-checker-api:latest -f Dockerfile.api . --push
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t $(IMAGE)/update-checker-scanner:$(VERSION) -t $(IMAGE)/update-checker-scanner:latest -f Dockerfile.scanner . --push
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) -t $(IMAGE)/update-checker-dashboard:$(VERSION) -t $(IMAGE)/update-checker-dashboard:latest -f Dockerfile.dashboard . --push

## clean: remove build artefacts and coverage reports
clean:
	rm -rf bin/ coverage.out coverage.html

## help: list available targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //' | column -t -s ':'
