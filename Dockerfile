# syntax=docker/dockerfile:1

# ── Build ─────────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG VERSION_PKG=github.com/yuriy-kovalchuk/yk-update-checker/internal/version
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w \
        -X ${VERSION_PKG}.Version=${VERSION} \
        -X ${VERSION_PKG}.Commit=${COMMIT} \
        -X ${VERSION_PKG}.BuildDate=${BUILD_DATE}" \
      -o /update-checker ./cmd/update-checker

# ── Runtime ───────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static:nonroot@sha256:6706c73aae2afaa8201d63cc3dda48753c09bcd6c300762251065c0f7e602b8e

LABEL org.opencontainers.image.title="yk-update-checker" \
      org.opencontainers.image.description="Scans GitOps repos for outdated Helm chart and FluxCD dependencies" \
      org.opencontainers.image.source="https://github.com/yuriy-kovalchuk/yk-update-checker" \
      org.opencontainers.image.licenses="MIT"

COPY --from=builder /update-checker /update-checker

USER nonroot:nonroot

ENTRYPOINT ["/update-checker"]
