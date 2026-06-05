# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine AS builder
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

FROM gcr.io/distroless/static:nonroot@sha256:dfadf31470f770fcabd48903762dce126958e98d1ce320acf1216bbfaa42d79c
LABEL org.opencontainers.image.title="yk-update-checker" \
      org.opencontainers.image.description="Scans GitOps repos for outdated Helm chart and FluxCD dependencies" \
      org.opencontainers.image.source="https://github.com/yuriy-kovalchuk/yk-update-checker" \
      org.opencontainers.image.licenses="MIT"
COPY --from=builder /update-checker /update-checker
USER nonroot:nonroot
ENTRYPOINT ["/update-checker"]
