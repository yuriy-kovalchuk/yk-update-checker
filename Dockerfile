# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ARG VERSION=dev COMMIT=unknown BUILD_DATE=unknown
ARG TARGETOS=linux TARGETARCH=amd64
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w \
      -X github.com/yuriy-kovalchuk/yk-update-checker/internal/version.Version=${VERSION} \
      -X github.com/yuriy-kovalchuk/yk-update-checker/internal/version.Commit=${COMMIT} \
      -X github.com/yuriy-kovalchuk/yk-update-checker/internal/version.BuildDate=${BUILD_DATE}" \
    -o /update-checker ./cmd/update-checker

# Runtime pinned by digest: this image's git reads every credential (refresh: docker buildx imagetools inspect alpine/git:<tag>).
FROM alpine/git:v2.54.0@sha256:e8ceedc73bd80992ae8d58a7d24c58b8322146c354f1d2e81a97db4452068ec0
LABEL org.opencontainers.image.title="yk-update-checker" \
      org.opencontainers.image.description="Scans GitOps repos for outdated Helm chart and FluxCD dependencies" \
      org.opencontainers.image.source="https://github.com/yuriy-kovalchuk/yk-update-checker" \
      org.opencontainers.image.licenses="MIT"
COPY --from=builder /update-checker /update-checker
USER 65534:65534
ENTRYPOINT ["/update-checker"]
