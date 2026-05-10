# yk-update-checker

Scans one or more GitOps repositories for outdated Helm chart dependencies and FluxCD HelmRelease resources, then presents results in a sortable/filterable web UI.

## Architecture

Three components deployed via a single Helm chart:

| Component | Kind | Role |
|---|---|---|
| `update-checker-api` | Deployment | Owns the SQLite database, exposes REST API, triggers scan Jobs |
| `update-checker-scanner` | CronJob | Clones repos, checks versions, posts results to the API |
| `update-checker-dashboard` | Deployment | Serves the web UI, proxies `/api/*` to the API |

## What it detects

- Helm `Chart.yaml` dependencies (HTTPS and OCI repositories)
- FluxCD `HelmRelease` manifests — inline `repoURL`, `sourceRef → HelmRepository`, `chartRef → OCIRepository`
- Cross-file FluxCD references fully resolved

Upgrade scope is configurable: `patch`, `minor`, `major`, or `all`.

## Installation

```bash
helm install yk-update-checker \
  oci://ghcr.io/yuriy-kovalchuk/charts/yk-update-checker \
  --version <version> \
  -f values.yaml
```

Minimum `values.yaml`:

```yaml
scanner:
  config:
    repos:
      - name: homelab
        repo: https://github.com/example/my-gitops-repo
        path: kubernetes/apps   # optional sub-path
```

## Private repositories

Store credentials in a Kubernetes Secret and reference it — do not put tokens or passwords directly in values.

```bash
kubectl create secret generic github-token \
  --from-literal=token=ghp_xxxxxxxxxxxxxxxxxxxx
```

```yaml
scanner:
  config:
    repos:
      - name: private-repo
        repo: https://github.com/example/private-repo
        auth:
          type: token
          existingSecret: github-token
          existingSecretKey: token  # default: token
```

Supported auth types: `token`, `basic`, `ssh`.

## Key chart values

```yaml
scanner:
  schedule: "0 */6 * * *"      # CronJob schedule
  config:
    updateType: all             # all | major | minor | patch
    parallelChecks: 5

  # Persistent git cache — avoids re-cloning on every run.
  # Mount the backing volume via scanner.extraVolumes + extraVolumeMounts.
  gitCacheDir: ""

api:
  persistence:
    size: 1Gi
  enableTrigger: true           # allow manual scans from the UI

dashboard:
  ingress:
    enabled: false

# Per-component environment variables
api:
  extraEnv: []
scanner:
  extraEnv: []
dashboard:
  extraEnv: []

# Applied to all components
extraEnv: []
```

## Development

```bash
make build          # compile all three binaries to bin/
make test           # run tests with race detector
make help           # list all available targets
```

See [docs/local-testing.md](docs/local-testing.md) for a full local setup guide and [docs/architecture.md](docs/architecture.md) for component internals.

Releases are triggered by pushing a `v*` tag. CI builds and pushes all three Docker images and the Helm chart to GHCR.

## License

MIT
