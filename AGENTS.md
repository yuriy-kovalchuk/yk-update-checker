# yk-update-checker

## Overview

Kubernetes-native GitOps dependency update checker. Scans git repositories for outdated Helm chart dependencies and FluxCD HelmRelease resources, then surfaces results via a web UI. Deployable as a single Helm chart; runs as one binary in two modes.

## Architecture

Single binary (`update-checker`) with two subcommands:

| Subcommand | Role |
|---|---|
| `serve` | HTTP server: API + embedded dashboard UI, optional internal scheduler, optional K8s CronJob trigger |
| `scan` | One-shot: clone repos → extract deps → check versions → print JSON or POST to a serve instance |

**Key packages:**

- `cmd/update-checker/` — entrypoint; wires config, runner, service, trigger, scheduler, server
- `internal/scan/` — vertical slice: `Handler` (HTTP), `Service` (orchestration), `Repository` (in-memory), `Runner` (clone + extract + version check), `Result`/`Status` types
- `internal/extractor/` — `Extractor` interface + `HelmChart` (parses `Chart.yaml` deps) + `FluxCD` (two-pass: collect HelmRepository/OCIRepository, resolve HelmRelease)
- `internal/registry/` — version resolution: HTTPS index.yaml fetch or OCI tag listing; scope filtering (patch/minor/major/all)
- `internal/trigger/` — `Trigger` interface with two impls: `KubernetesTrigger` (creates K8s Job from CronJob template) and `InlineTrigger` (calls `RunScan` in-process)
- `internal/scheduler/` — interval-based internal scheduler; runs `RunScan` on a ticker
- `internal/dashboard/` — serves embedded web UI
- `internal/api/` — HTTP server bootstrap, route registration, middleware chain
- `internal/config/` — YAML config loader; repos, update scope, parallel checks, git cache dir
- `internal/middleware/` — shared HTTP middleware (recovery, logging, headers)
- `internal/version/` — `Version`, `Commit`, `BuildDate` vars injected via ldflags

**Storage:** in-memory only (`scan.Repository` uses a `sync.RWMutex`-guarded slice). Results are lost on restart.

**Data flow (serve mode):**

1. On startup: optionally start internal scheduler (ticker → `RunScan`)
2. On manual trigger: `POST /api/scan/trigger` → `Service.Trigger()` → K8s Job or inline `RunScan`
3. External scanner (K8s CronJob): `POST /api/scan/results` → `Service.StoreResults()`
4. Dashboard: `GET /api/scan/results` + `GET /api/scan/status` to render UI

## Design Decisions

- **Single binary, two modes** — previously three separate binaries (api/scanner/dashboard). Collapsed to simplify deployment and eliminate the need for inter-service HTTP in small installations.
- **Trigger abstraction** — `KubernetesTrigger` is preferred when a CronJob name is supplied and in-cluster config is available; falls back to `InlineTrigger` automatically. This lets the same binary work both inside and outside Kubernetes.
- **In-memory storage** — no database dependency. Results survive as long as the pod is running. Acceptable given the use case (periodic scans, no history required).
- **Extractor two-pass for FluxCD** — FluxCD files must be walked twice: first pass collects repository sources, second resolves HelmRelease chart refs. This is encoded in the `Extractor` interface (`PrepareFile` + `Extract`).

## Development Commands

```bash
make build        # compile → bin/update-checker
make run          # build + serve (uses examples/config.yaml)
make run-scan     # build + one-shot scan, print JSON to stdout
make test         # go test -race -timeout 120s ./...
make test-cover   # coverage report → coverage.html
make lint         # golangci-lint run ./...
make tidy         # go mod tidy && go mod verify
make deps-check   # list outdated direct deps
make vuln         # govulncheck ./...
make docker-build # multi-arch image build (no push)
make docker-push  # build + push to GHCR
make install-hooks # configure .githooks/
```

## Key Patterns

- Module: `github.com/yuriy-kovalchuk/yk-update-checker`
- Image: `ghcr.io/yuriy-kovalchuk/yk-update-checker`
- Clean architecture, feature folders: handler → service → repository, all in `internal/scan/`
- Constructor injection everywhere; exported interfaces, unexported impls
- `context.Context` first param on all I/O functions
- `log/slog` with `slog.NewTextHandler(os.Stderr, ...)`, structured fields only
- Config via YAML file + flags; env vars for credential paths only
- `CGO_ENABLED=0` always; distroless nonroot runtime image
