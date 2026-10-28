.PHONY: build generate run test lint chart-creds kind docker-build docker-push install-hooks

CONFIG       ?= examples/config.yaml
IMAGE        ?= ghcr.io/yuriy-kovalchuk/yk-update-checker
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PLATFORMS    ?= linux/amd64,linux/arm64
TAG_LATEST   ?= false
KIND_CLUSTER ?= update-checker
CONTROLLER_GEN ?= v0.23.0
VALUES       ?= examples/test-values.yaml
GIT_COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE   := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG  := github.com/yuriy-kovalchuk/yk-update-checker/internal/version
LDFLAGS      := -s -w -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(GIT_COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

build: ## compile bin/update-checker
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/update-checker ./cmd/update-checker

generate: ## CRDs from the report types into the chart
	rm -rf charts/yk-update-checker/files/crds
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN) crd paths=./internal/report/... output:crd:dir=charts/yk-update-checker/files/crds

run: build ## local dashboard: reports in ./data, scans as child processes
	bin/update-checker dashboard --mode=local --config $(CONFIG) --verbose

test: ## all tests with the race detector
	go test -race -timeout 120s ./...

lint: ## golangci-lint
	golangci-lint run ./...

chart-creds: ## render-time checks of the Helm chart
	scripts/verify-chart-credentials.sh

kind: ## kind cluster (created if missing) + local image + chart with VALUES
	kind get clusters | grep -qx $(KIND_CLUSTER) || kind create cluster --name $(KIND_CLUSTER)
	docker build -t $(IMAGE):dev .
	kind load docker-image $(IMAGE):dev --name $(KIND_CLUSTER)
	helm upgrade --install yk-update-checker charts/yk-update-checker \
	  --kube-context kind-$(KIND_CLUSTER) -n yk-update-checker --create-namespace \
	  -f $(VALUES) --set image.repository=$(IMAGE) --set image.tag=dev --set image.pullPolicy=Never

DOCKER_BUILD = docker buildx create --name multiplatform --driver docker-container --use >/dev/null 2>&1 || true; \
	docker buildx build --builder multiplatform --platform $(PLATFORMS) \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(GIT_COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) $(if $(filter true,$(TAG_LATEST)),-t $(IMAGE):latest)

docker-build: ## multi-arch image, not pushed
	$(DOCKER_BUILD) .

docker-push: ## multi-arch image pushed to IMAGE
	$(DOCKER_BUILD) --push .

install-hooks: ## use .githooks/ for commit-message and pre-push checks
	git config core.hooksPath .githooks && chmod +x .githooks/*
