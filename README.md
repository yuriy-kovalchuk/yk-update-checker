# yk-update-checker

Scans one or more GitOps repositories for outdated Helm chart dependencies and FluxCD HelmRelease resources, then surfaces results in a web UI.

## Installation

```bash
helm install yk-update-checker \
  oci://ghcr.io/yuriy-kovalchuk/charts/yk-update-checker \
  --version <version> \
  -f values.yaml
```

## Quickstart

Minimum `values.yaml` — add at least one repo:

```yaml
scanner:
  config:
    repos:
      - name: homelab
        repo: https://github.com/example/my-gitops-repo
        path: kubernetes/apps   # optional sub-path
```

The chart deploys two workloads:

| Workload | Kind | Role |
|---|---|---|
| `update-checker` | Deployment | Serves the UI + API on port 8080, handles manual scan triggers |
| `update-checker-scanner` | CronJob | One-shot scan pod: clones repos, checks versions, posts results |

The UI is available at the configured Ingress host or via `kubectl port-forward`.

## Configuration

### Repos

```yaml
scanner:
  config:
    update_type: all        # all | major | minor | patch
    parallel_checks: 5

    repos:
      - name: my-gitops
        repo: https://github.com/example/my-gitops-repo
        path: kubernetes/apps
```

### Private repositories

Store credentials in a Kubernetes Secret, then reference it — do not put tokens inline in values:

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
          existingSecretKey: token   # default: token
```

Supported auth types: `token`, `basic`, `ssh`. See `examples/config.example.yaml` for all options.

### Key values

```yaml
scanner:
  schedule: "0 */6 * * *"   # CronJob schedule (default: every 6h)
  suspend: false             # suspend the CronJob without deleting it

ingress:
  enabled: false
  hosts:
    - host: update-checker.example.com
      paths:
        - path: /
          pathType: Prefix

rbac:
  create: true               # required for the manual scan trigger button
```

Full reference: [`charts/yk-update-checker/values.yaml`](charts/yk-update-checker/values.yaml).

## Development

```bash
make build      # compile → bin/update-checker
make run        # build + serve locally (uses examples/config.yaml)
make run-scan   # one-shot scan, prints JSON to stdout
make test       # run tests with race detector
make help       # list all targets
```

See [docs/local-testing.md](docs/local-testing.md) for a full local setup guide and [docs/architecture.md](docs/architecture.md) for internals.

Releases are triggered by pushing a `v*` tag — CI builds the Docker image and Helm chart and publishes both to GHCR.

## License

MIT
