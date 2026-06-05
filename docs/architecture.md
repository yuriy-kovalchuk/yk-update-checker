# Architecture

yk-update-checker is a single binary with two subcommands deployed via a single Helm chart.

## Binary modes

| Subcommand | Typical deployment | Role |
|---|---|---|
| `serve` | Kubernetes Deployment | HTTP server: API + embedded web UI, optional internal scheduler, optional K8s CronJob trigger |
| `scan` | Kubernetes CronJob / local CLI | One-shot: clone repos → extract deps → check versions → POST results to a `serve` instance or print JSON to stdout |

## Component diagram

```
                  ┌─────────────────────────────────────────┐
                  │          update-checker serve           │
                  │                                         │
  [browser] ────► │  GET /             (embedded index.html)│
                  │  GET /api/results  (latest scan results)│
                  │  GET /api/status   (scan state + meta)  │
                  │  POST /api/scan/trigger   (manual scan) │
                  │  POST /api/scan/results   (push results)│
                  │  GET /health  GET /ready                │
                  │                                         │
                  │  ┌─────────┐   in-memory Repository     │
                  │  │ Service │◄──(sync.RWMutex slice)     │
                  │  └────┬────┘                            │
                  │       │ Trigger interface               │
                  │  ┌────▼──────────────────────┐          │
                  │  │ KubernetesTrigger          │         │
                  │  │  creates K8s Job from      │         │
                  │  │  CronJob template          │         │
                  │  │ — or —                     │         │
                  │  │ InlineTrigger              │         │
                  │  │  calls RunScan in-process  │         │
                  │  └────────────────────────────┘         │
                  └─────────────────────────────────────────┘
                                    ▲
                                    │ POST /api/scan/results
                  ┌─────────────────┴───────────────────────┐
                  │         update-checker scan             │
                  │                                         │
                  │  1. Clone/fetch each configured Git repo│
                  │  2. Walk YAML files (two-pass)          │
                  │  3. Resolve versions in Helm / OCI reg. │
                  │  4. POST results  — or — print JSON     │
                  └─────────────────────────────────────────┘
```

## Scan triggering

Three ways a scan can run:

1. **Internal scheduler** (`--interval`) — fires immediately on startup then on every tick. Runs `RunScan` in-process inside the `serve` process.
2. **K8s CronJob trigger** — `POST /api/scan/trigger` creates a one-off K8s `Job` from the CronJob spec. The resulting `scan` pod posts results back via `POST /api/scan/results`. Requires in-cluster RBAC and `--cronjob` flag.
3. **Inline trigger** — `POST /api/scan/trigger` calls `RunScan` in-process in a goroutine. Used when no CronJob name is configured or when running outside Kubernetes.

The trigger implementation is selected at startup: `KubernetesTrigger` if `--cronjob` is set and in-cluster config is available; `InlineTrigger` otherwise.

## Storage

Results are held in memory (`scan.Repository`, a `sync.RWMutex`-guarded slice). There is no database. Results are lost on pod restart; the next scheduled or manual scan repopulates them.

## Package layout

```
cmd/update-checker/     entrypoint; flag parsing, wiring, signal handling
internal/
  api/                  HTTP server bootstrap, route registration, middleware chain
  scan/
    handler.go          POST /api/scan/trigger, POST /api/scan/results
    service.go          orchestration: RunScan, StoreResults, GetResults, GetStatus, Trigger
    repository.go       in-memory storage
    runner.go           clone repos, run extractors, check versions concurrently
    types.go            Result, Status
  dashboard/            GET /, GET /api/results, GET /api/status; embeds ui/index.html
  extractor/            Extractor interface + HelmChart + FluxCD implementations
  registry/             version resolution: HTTPS index.yaml or OCI tag listing; scope filter
  trigger/              Trigger interface; KubernetesTrigger + InlineTrigger
  scheduler/            interval ticker; fires RunScan immediately then on each tick
  config/               YAML config loader
  middleware/           Recovery, Headers, Logger
  version/              Version, Commit, BuildDate vars (ldflags)
```

## Version detection

### Helm `Chart.yaml` dependencies

For each entry in `dependencies[]`:

- **HTTPS** (`repository: https://...`): fetches `index.yaml`, picks the highest version within the configured `updateType` scope (patch / minor / major / all).
- **OCI** (`repository: oci://...`): lists tags via `go-containerregistry`, applies the same scope filter.

### FluxCD `HelmRelease`

Two-pass walk because `HelmRelease` resources cross-reference source objects in other files:

1. **Prepare pass** — collects all `HelmRepository` and `OCIRepository` resources, keyed by `namespace/name`.
2. **Extract pass** — resolves each `HelmRelease` against the map (via `spec.sourceRef`, `spec.chartRef`, or inline `repoURL`), then performs the same version lookup as above.

## Authentication

Private Git repository auth is configured per-repo in `config.yaml`:

| Type | Mechanism |
|---|---|
| `token` | Bearer token injected into the HTTPS clone URL |
| `basic` | Username + password injected into the HTTPS clone URL |
| `ssh` | SSH key file passed via `GIT_SSH_COMMAND` |

Credentials are mounted from Kubernetes Secrets. Nothing is written to disk.
