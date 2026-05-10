# Architecture

yk-update-checker is a three-component system deployed via a single Helm chart. Each component is independently scaled and has a clearly bounded responsibility.

## Components

| Component | Kind | Port | Role |
|---|---|---|---|
| `update-checker-api` | Deployment | 8080 | Owns the SQLite database, exposes the REST API, manages K8s Job triggers |
| `update-checker-scanner` | CronJob | — | Clones repos, checks versions against registries, posts results to the API |
| `update-checker-dashboard` | Deployment | 8081 | Serves the web UI, reverse-proxies `/api/*` to the API |

## Data flow

```
[CronJob / manual trigger]
        │
        ▼
  update-checker-scanner
  ┌─────────────────────────────────────────────────┐
  │ 1. Clone/fetch each configured Git repo         │
  │ 2. Walk YAML files (Chart.yaml + HelmRelease)   │
  │ 3. Resolve FluxCD cross-file references         │
  │ 4. Look up latest versions in Helm / OCI reg.   │
  │ 5. POST results to API                          │
  └────────────────────┬────────────────────────────┘
                       │ REST API
                       ▼
             update-checker-api
             ┌────────────────┐
             │  SQLite (WAL)  │
             └────────────────┘
                       ▲
                       │ reverse proxy
             update-checker-dashboard
             ┌────────────────┐
             │  Web UI        │
             └────────────────┘
                       ▲
                  [browser]
```

## Storage

The API owns a single SQLite database file stored on a `PersistentVolumeClaim`. WAL mode is enabled so the scanner can write results while the dashboard reads previous scan data concurrently. The scanner never touches the database directly — all writes go through the API.

The database schema has two tables:
- `scans` — scan metadata (status, timing, result count, trigger source)
- `results` — one row per detected dependency / HelmRelease

## Version detection

### Helm `Chart.yaml` dependencies

The scanner walks every `Chart.yaml` in the repo. For each entry in `dependencies[]`:

- **HTTPS** (`repository: https://...`): fetches `index.yaml` from the registry, parses all available versions, and picks the highest version that falls within the configured `updateType` scope.
- **OCI** (`repository: oci://...`): lists tags via the OCI distribution API using `go-containerregistry`, then applies the same scope filter.

### FluxCD `HelmRelease`

The scanner uses a two-pass approach because `HelmRelease` resources often reference a `HelmRepository` or `OCIRepository` that lives in a different file:

1. **Prepare pass** — collects all `HelmRepository` and `OCIRepository` resources across every YAML file, keyed by `namespace/name`.
2. **Extract pass** — resolves each `HelmRelease.spec.sourceRef` or `spec.chartRef` against the prepared map to obtain the concrete registry URL, then performs the same version lookup as above.

Inline `repoURL` (without a separate source resource) is also supported.

## Authentication

The scanner supports three auth types for private Git repositories:

| Type | Mechanism |
|---|---|
| `token` | Bearer token injected into the HTTPS clone URL |
| `basic` | Username + password injected into the HTTPS clone URL |
| `ssh` | SSH key file passed via `GIT_SSH_COMMAND` |

Credentials are mounted into the scanner pod from Kubernetes Secrets. The scanner never writes credentials to disk.

## Manual triggers

When `api.enableTrigger` is true, the API can create a one-off Kubernetes `Job` from the scanner `CronJob` spec on demand. This lets the dashboard button initiate an immediate scan without waiting for the next scheduled run. The API requires in-cluster RBAC permissions to create Jobs in its own namespace.
