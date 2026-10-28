# Local Testing

## Prerequisites

- Go 1.27.3+ (the version in the `go` directive of `go.mod`)
- `git` binary on `$PATH`
- `golangci-lint` v2.x (`brew install golangci-lint`)
- for `make kind`: `kind`, `docker`, `helm`, `kubectl`
- for `make generate`: nothing extra (`go run` fetches the pinned `controller-gen`)

## Quick start

```bash
cp examples/config.example.yaml examples/config.yaml
# edit examples/config.yaml — add at least one repo
make run
```

`make run` builds the binary and starts the **local dashboard**: reports are JSON files
under `./data`, and the scan button (and `--interval`) starts `scan` as a child process.
The UI is at [http://localhost:8080](http://localhost:8080).

## Config

`make run` reads the tracked `examples/config.yaml`; pass `--config=<path>` to scan
anything else. `examples/config.example.yaml` is the fully documented reference (every
auth type) — copy it somewhere outside the repo rather than over the tracked one.
Minimum config:

```yaml
update_type: all        # all | minor | patch  ("major" is accepted and behaves as all)
parallel_checks: 5

repos:
  - name: my-gitops
    repo: https://github.com/example/my-gitops-repo
    path: kubernetes/apps   # optional sub-path
```

Every key the scanner understands (default in parentheses):

| Key | Effect |
|---|---|
| `update_type` (`all`) | highest upgrade level to report: `patch`, `minor`, `all`. `major` is accepted and behaves exactly like `all` — there is no major-only filter, so a major request still reports minor and patch bumps |
| `parallel_checks` (`5`) | concurrent registry lookups per repository (two repositories are scanned at a time) |
| `scan_timeout` (`30m`) | wall-clock budget for one scan — clones, fetches and every registry lookup. On expiry the run is recorded as failed with the timeout in the message, every repository that had not finished included, instead of leaving the dashboard permanently "scanning". `0` removes it; each individual `git` call has its own fixed 5-minute limit |
| `check_images` (`true`) | container images referenced by Kubernetes workloads |
| `check_dockerfiles` (`true`) | base images pinned in `Dockerfile`/`Containerfile` `FROM` lines |
| `verify_image_tags` (`true`) | re-check the winning candidate image tags before reporting them, so backport builds, fork rebuilds and arch-specific leftovers are not offered as updates; `false` reports the highest version-looking tag exactly as the registry lists it |
| `registry_auth_file` (unset) | a Docker config file (`.dockerconfigjson`) whose static `auths` entries authenticate image and OCI chart lookups |
| `git_cache_dir` (unset) | clone into a persistent directory instead of a temporary one deleted after the scan. Unset is right locally; in Kubernetes the scan pod has a read-only rootfs, so the chart gives it an `emptyDir` at `/tmp` |

Keys this build does not read are listed in a startup warning with their exact path, and
an `update_type` or `auth.type` outside its allowed set stops the run naming the key —
worth checking with `bin/update-checker scan --config your.yaml` after editing.

For private repos see `examples/config.example.yaml` — it covers token, basic, and SSH auth.

## Running modes

### Local dashboard (UI + API, no cluster)

```bash
make run
# or with options:
./bin/update-checker dashboard --mode=local \
  --config examples/config.yaml \
  --data ./data \
  --interval 6h \
  --port 8080 \
  --verbose
```

The UI polls `/api/results` and `/api/status`. The scan button starts
`update-checker scan --config … --store=file --data ./data` as a child process; `--interval`
fires the same trigger on a timer (0 = only on demand). Reports land in
`./data/{repositoryscans,dependencies}/*.json`, which is what the dashboard polls every
5 seconds — the same two kinds the cluster version keeps as custom resources.

### One-shot scan (print JSON, write nothing)

```bash
./bin/update-checker scan --config examples/config.yaml --verbose
```

### One-shot scan (write reports)

```bash
./bin/update-checker scan --config examples/config.yaml --store=file --data ./data --history 10
# in a cluster, from a pod with the scanner ServiceAccount:
./bin/update-checker scan --config /etc/update-checker/config.yaml --store=kubernetes
```

`scan` exits non-zero when a repository failed, and every repository it did not reach (a
`scan_timeout` or a cancellation) is recorded as failed rather than left looking unchanged.

### Where local runs save their reports

Nothing is kept in memory and no database is involved: a local scan **writes files**, and the local
dashboard reads those same files back (re-polled every 5 seconds). The directory is `--data`, default
`./data`, which `.gitignore` excludes.

```bash
find data -type f | head -4; du -sh data
```

```text
data/
├── repositoryscans/                                    # one file per configured repository
│   ├── homelab-8be0b5445fca6eb1.json
│   └── update-checker-0161c41e887a0801.json
└── dependencies/                                       # one file per checked reference
    ├── homelab-fluxcd-d93bcd36fcb6fe67.json
    ├── homelab-image-019fe22466d6960d.json
    └── update-checker-dockerfile-5d619e3861556a2a.json

280K	data            # 2 repositoryscans + 68 dependencies from examples/config.yaml
```

The file *is* the report object — `metadata.name` (the same name the Kubernetes custom resource
uses) plus `status`. A rescan rewrites the file it already has and only touches references whose
content changed; references that disappear from the repository are deleted from disk too.

```jsonc
// data/dependencies/homelab-fluxcd-d93bcd36fcb6fe67.json
{
  "metadata": {
    "name": "homelab-fluxcd-d93bcd36fcb6fe67",
    "labels": { "updatechecker.yuriykovalchuk.dev/outdated": "true", "…": "…" }
  },
  "status": {
    "source": "homelab", "type": "fluxcd", "kind": "HelmRelease",
    "path": "kubernetes/core/kyverno/base/helm-release.yaml",
    "chart": "kyverno", "dependency": "kyverno",
    "current_version": "3.9.1", "latest_version": "3.9.2", "scope": "patch",
    "update_available": true,
    "outdated_since": "2026-10-24T18:33:42Z",   // first scan that found it behind
    "latest_seen_at":  "2026-10-24T18:33:42Z"   // first scan that saw 3.9.2
  }
}
```

```bash
rm -rf data                     # reset: the dashboard comes up empty
./bin/update-checker dashboard --mode=local --config examples/config.yaml --data /tmp/uc   # anywhere else
jq . data/repositoryscans/*.json | less    # phases, counts and the last runs per repository
```

### In a cluster

```bash
make kind                                   # kind cluster + image + chart (VALUES defaults to examples/test-values.yaml)
make kind VALUES=examples/credentials.values.yaml   # an overlay with private repos (create its Secrets first)

kubectl --context kind-update-checker -n yk-update-checker get repositoryscans,dependencies
kubectl --context kind-update-checker -n yk-update-checker create job scan-now \
  --from=cronjob/yk-update-checker-scanner    # don't wait for the schedule
```

`make kind` builds `$(IMAGE):dev` with a plain `docker build`, loads it into the cluster and
installs the chart with `image.pullPolicy=Never`; `KIND_CLUSTER=` and `VALUES=` override it.
The CronJob scans at 00:00 UTC, so `create job --from=` is how you get a report now. What to
look at afterwards:

```bash
kubectl --context kind-update-checker -n yk-update-checker get repositoryscans   # phase, outdated, last scan
kubectl --context kind-update-checker -n yk-update-checker get lease            # which replica leads the metrics
kubectl --context kind-update-checker -n yk-update-checker auth can-i list secrets \
  --as=system:serviceaccount:yk-update-checker:yk-update-checker-scanner        # must be "no"
```

A scan of a repository that does not exist is worth adding to the overlay once: the Job fails,
its `RepositoryScan` gets `phase: Failed` with the error kind, and every other repository keeps
the reports it already had.

## Make targets

```bash
make build          # compile → bin/update-checker
make run            # build + local dashboard (examples/config.yaml, ./data)
make kind           # kind cluster + local image + chart (KIND_CLUSTER=, VALUES=)
make generate       # regenerate the CRDs from internal/report after changing the report types
make test           # go test -race -timeout 120s ./...
make lint           # golangci-lint run ./...
make chart-creds    # verify the chart's credential mounts and RBAC/report wiring
make docker-build   # multi-arch image build (no push); docker-push to push
make install-hooks  # register .githooks/ with git
```

## Git hooks

`make install-hooks` registers `.githooks/`:

- **commit-msg** — enforces `type: description` commit format
- **pre-push** — validates conventional commits on `master`, validates `v*` tag format, runs `go test ./...`

## Linting

```bash
make lint
# auto-fix where possible:
golangci-lint run --fix ./...
```
