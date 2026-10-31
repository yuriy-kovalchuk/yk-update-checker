# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.3] - 2026-10-31

### Fixed
- **An image published under several names could never be reported**: verification counted one "same digest as …" signal per alias, so a vendor that stamps each build with its own tags (`cloudflared:2026.10.7`, `2002-872b241`, `2002-872b241f9487` are one image) reached the two-signal threshold and every release of it was reported as `skipped`. One shared digest is now one signal, however many names carry it. A tag whose image is the pre-release **of that very version** (`9.2` serving the `9.2.0-rc1` build) is now rejected outright, with the honest reason, instead of relying on two name collisions
- **A scan could do nothing and still look healthy**: a scan pod exited while client-go's Lease release was still in flight, so the scan Lease kept naming a pod that no longer existed. The next scan then had to wait out the full `leaseDuration` recorded in that Lease (client-go measures an abandoned holder from when it first reads the record, not from `renewTime`) and `Env.Lock` gave up after exactly `leaseDuration + retryPeriod` = 17s, so the takeover race was won by luck: manual triggers skipped about half the time, and three nightly CronJob runs wrote no reports while their Jobs reported `Complete`. The release now completes before the process exits, the wait for a holder that died is `2 × leaseDuration`, and a scan that really is refused logs WARN naming the Lease. Side effect: a scan triggered while another is running now waits that budget and starts if the first one finishes inside it

## [0.4.2] - 2026-10-29

### Changed
- **Runtime image pin**: `alpine/git:v2.54.0` was re-pushed upstream, so the digest the Dockerfile pins moved from `sha256:4fac439e…` to `sha256:e8ceedc7…` (verified against the registry index, not just the tag)
- **Toolchain**: Go 1.26.10 → 1.27.3 (`go.mod`, Dockerfile builder `golang:1.27.3-alpine`), which closes `GO-2026-6809` (`crypto/x509`); `k8s.io/{api,apimachinery,client-go}` 0.37.1 → 0.37.2, `controller-gen` v0.20.1 → v0.23.0 (the generated CRDs gain only the version annotation), golangci-lint in CI v2.11.4 → v2.15.0; `k8s.io/kube-openapi` stays at the pseudo-version the 0.37 line requires

### Fixed
- **Another release's reports could be deleted or overwritten**: a second Helm release in the same namespace pruned and rewrote the first one's `RepositoryScan` and `Dependency` objects. Such reports are now left alone, and the scanner fails with the `unsupported` error kind instead — one release per namespace is enforced rather than assumed
- **One duplicate series could abort every scrape**: two repositories failing with the same `source` and `kind` produced two identical `update_checker_repository_scan_failed` series, and a rejected series makes `/metrics` answer 500; the collector now emits one per pair
- **The API could be served from a cache**: `GET /api/results` and `GET /api/status` now answer `Cache-Control: no-store`, so an ingress or sidecar cannot hold an old scan back
- **The footer said "checked" about the wrong thing**: it read the newest `checked_at` among the stored dependencies, which only moves when that reference changes; it now reads the scan's own timestamp
- **A gitconfig inside the image could steer the scan**: git subprocesses also ignore the global config now (`GIT_CONFIG_GLOBAL=/dev/null`), as they already ignored the system one
- **Two references could share one report**: a manifest that pins an image to a digest (`app:1.0.0@sha256:…`) alongside the same tag followed by name is now reported as the two references it is. The pinned digest is recorded as `pinned_digest` and joins the reference's identity, so the `Dependency` objects of digest-pinned references are **named differently once**: the scan after the upgrade recreates them and their `outdated_since`/`latest_seen_at` start over; references that only follow a tag are untouched
- **The published image carried a fixable HIGH**: every `alpine/git` tag freezes its packages at the base image's build date, so all of them (`latest`, `2.54.0`, `v2.54.0`) still shipped `libcurl 8.22.0-r0` (CVE-2026-92392) while Alpine 3.24 had the fix. The runtime stage now resolves that package at our build date (`apk add --no-cache --upgrade libcurl`), which took it to `8.23.0-r0`

## [0.4.1] - 2026-10-25

### Added
- **Filter by repository**: the dashboard UI gained a repository selector that scopes the tiles and the list, and the Grafana dashboard gained a multi-value `Repository` variable (`source` label) on every panel except *Last scan*, which stays global

### Fixed
- **Scan now is inert during a scan**: the button and the `r` shortcut stay disabled while `status.scanning` is true, instead of answering the second click with `trigger failed: HTTP 409`

## [0.4.0] - 2026-10-24

A redesign: the scanner writes reports as Kubernetes resources and the dashboard only reads them. Read **Breaking** and **Upgrading** before moving from 0.3.0.

### Added
- **Reports as custom resources** (`updatechecker.yuriykovalchuk.dev/v1alpha1`): one `RepositoryScan` per repository (phase, error kind, counts, last N runs) and one `Dependency` per checked reference (with `outdated_since` and `latest_seen_at`). Results survive restarts and are readable with `kubectl get updatechecker`. Locally the same objects are JSON files under `./data`
- **Container images**: image tags in workload manifests and `FROM` lines of Dockerfiles/Containerfiles are checked (`check_images`, `check_dockerfiles`). Tags are compared by shape (semver, calver, date, build, floating, opaque), suspicious candidates are skipped with a reason (`verify_image_tags`), and moved digests are reported as drift
- **Private registries**: `registry_auth_file` (a Docker config file; chart: `scanner.registryAuth.existingSecret`)
- **Error kinds** on every failure (`auth`, `not_found`, `unreachable`, `timeout`, …), shown in the UI with a hint and exported as a metric label
- **Multiple dashboard replicas**: leader election, so only one replica exports the result metrics
- **One scan at a time**: a Lease (locally a file lock); a scan that finds one running exits 0
- **Metrics**: `update_checker_dependencies_scanned`, `_dependency_digest_drift`, `_dependency_check_failed_info`, `_dependency_skipped_tags` and `_repository_scan_failed`; `kind`, `path` and `tag_kind` labels on `_dependency_outdated_info`
- **Alerts**: `UpdateCheckerRepositoryScanFailed`, `UpdateCheckerNoSuccessfulScan` and `UpdateCheckerDigestDrift`
- **Grafana dashboard** with everything the UI shows: counts by scope, failures by kind, outdated, failed and drifting references, skipped tags and failing repositories
- **Dashboard UI** redesign: needs-attention lane, grouped dependencies, filters, details drawer, phone layout
- **Config**: `scan_timeout` (default 30m), `auth.known_hosts_file`; unknown keys are logged with their path instead of being ignored silently
- **Helm**: `crds.install`, `imagePullSecrets`, `scanner.history.runs`, `scanner.ssh.knownHosts`, `auth.existingSecretKey`, `activeDeadlineSeconds`/`startingDeadlineSeconds` on the CronJob
- **Build**: `make kind` (local cluster with the local image and chart) and `make generate` (CRDs from the Go types)

### Changed
- **Only the CronJob scans.** The dashboard reads reports and starts scans as Jobs copied from the CronJob (locally: child processes); nothing talks over HTTP between the two
- **Least privilege**: the dashboard and the scanner have separate ServiceAccounts and Roles; only the scan pod gets repository credentials, and it can touch nothing but the reports and its Lease
- **Credentials**: `auth.existingSecret` is mounted per repo as a read-only file (mode `0440`, needs `fsGroup`) and git sends it as a header scoped to the repo URL
- **Hardened git**: only the configured transports, no system gitconfig, no credential in URLs, `--` before every URL
- **Limits**: two repositories scanned in parallel, at most 2,000 references per repository, files over 10 MiB and symlinks skipped
- **Image**: Go 1.26.10, runtime `alpine/git` pinned by digest, Go heap limit derived from the container memory limit
- **CI**: actions pinned by SHA, Dependabot, blocking lint/Trivy/govulncheck gates, pull requests no longer publish images

### Breaking
- `serve` is now `dashboard --mode=kubernetes|local`; `scan --server-url` is replaced by `scan --store=none|file|kubernetes`
- Config: `registry_cache_ttl` is removed; `update_type` and `auth.type` are validated; `token`/`basic` auth needs an `https://` URL; credentials inline in a URL or without `auth.type` are rejected
- Helm: `scanner.trigger` and `scanner.interval` are removed; inline `auth.token`/`auth.password` and ssh repos without `scanner.ssh.knownHosts` fail to render; one release per namespace
- Metrics: `update_checker_scan_duration_seconds` and `update_checker_scan_total` are removed; the new labels on `update_checker_dependency_outdated_info` change its series

### Upgrading from 0.3.0
1. Create the repository Secrets the `auth.existingSecret` fields name, and add `scanner.ssh.knownHosts` for ssh repos.
2. `helm upgrade` installs the CRDs from the chart. If they already exist from an earlier install, label them `app.kubernetes.io/managed-by=Helm` and annotate `meta.helm.sh/release-name` / `meta.helm.sh/release-namespace` first.
3. With several releases in one cluster, set `crds.install: true` on exactly one.

## [0.3.0] - 2026-08-17

### Added
- **Registry**: TTL cache for fetched registry data (`registry_cache_ttl`, default 15m) — `serve` reuses recently fetched Helm indexes and OCI tag lists between scans, so back-to-back scans skip index re-downloads; `0` restores per-scan fetching
- **Dashboard**: new app icon (update arrow + check, served at `/favicon.svg` and used in the app bar); replaces the inline layer-stack icon
- **Helm**: fixed the broken chart `icon:` URL (pointed at the old `yk-helm-update-checker` repo and removed path)
- **Metrics**: Prometheus `/metrics` endpoint exposed when running inside Kubernetes — auto-detected via `rest.InClusterConfig`
- **Metrics**: `update_checker_dependency_outdated_info` gauge — one time series per outdated dependency, labelled by source, chart, dependency, type, protocol, scope, current_version, latest_version
- **Metrics**: `update_checker_last_scan_time`, `update_checker_scan_duration_seconds`, `update_checker_scan_total` operational gauges
- **Helm**: `ServiceMonitor` CRD template (opt-in via `metrics.serviceMonitor.enabled`) for Prometheus Operator discovery
- **Helm**: `PrometheusRule` CRD template with configurable scope→severity rules (opt-in via `metrics.prometheusRule.enabled`), including a default rule for `unknown`-scope (non-semver-pinned) dependencies
- **Dashboard**: pre-built Grafana dashboard JSON (`charts/yk-update-checker/dashboards/update-checker.json`) — stat row with total outdated count plus per-scope breakdown (major/minor/patch/unknown) and time since last scan, and a table of outdated dependencies with a scope filter (major/minor/patch/unknown)
- **Dashboard**: WebUI now has a scope filter dropdown (major/minor/patch/unknown) alongside existing type and status filters
- **Helm**: `UpdateCheckerScanStale` alert rule (opt-in via `metrics.prometheusRule.staleScan.enabled`, threshold via `staleScan.thresholdSeconds`, default 30h to match the daily scan schedule) — fires when no scan has completed successfully within the threshold, so a broken scanner can't silently clear all update alerts
- **Dashboard**: error banner in the WebUI showing the last scan error (`status.last_error`) — a failed or partially failed scan no longer looks identical to "all up to date"

### Fixed
- **Security**: credential-file read errors are no longer logged in clear text (resolves CodeQL `go/clear-text-logging`); neither the error nor the file path is logged, and the auth failure surfaces in the scan result's `check_error`
- **Lint**: `internal/metrics` package now passes golangci-lint (errcheck, noctx, goimports)

## [0.2.2] - 2026-08-04

### Changed
- **Dependencies**: bumped `google/go-containerregistry` v0.21.7 → v0.21.8
- **Dependencies**: bumped `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go` v0.36.2 → v0.36.3

## [0.1.1-alpha] - 2026-06-05

### Changed
- **Helm chart**: added Kyverno-compliant security contexts — `runAsNonRoot`, `seccompProfile: RuntimeDefault` at pod level; `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: ALL` at container level
- **Helm chart**: split security contexts into independent Deployment and CronJob settings for per-workload overrides
- **Helm chart**: added emptyDir volume (`/tmp`) to scanner CronJob for git cache write access, defaulted `git_cache_dir: /tmp/git-cache`
- **Dockerfile**: switched runtime base from `distroless/static` to `alpine/git:v2.52.0` so the `scan` subcommand has `git` available in the container

## [0.1.0-alpha] - 2026-06-05

### Added
- Single binary with two subcommands: `serve` (HTTP server + embedded UI) and `scan` (one-shot dependency check)
- `serve` mode embeds the web UI — no separate dashboard process required
- Internal scheduler (`--interval`) — runs scans on a fixed interval inside the serve process without an external CronJob
- Inline trigger — manual scan button runs a scan in-process when no `--cronjob` flag is provided or when outside Kubernetes
- Kubernetes trigger — `POST /api/scan/trigger` creates a one-off Job from the CronJob template when `--cronjob` is set and in-cluster config is available; falls back to inline automatically
- `update-checker version` subcommand
- Helm `Chart.yaml` dependency scanning for HTTPS and OCI repositories
- FluxCD `HelmRelease` scanning with cross-file source reference resolution (two-pass: collect `HelmRepository`/`OCIRepository`, resolve `HelmRelease` refs)
- Configurable upgrade scope: `patch`, `minor`, `major`, `all`
- Private repository auth: `token`, `basic`, and `ssh` types; credentials loaded from files (Kubernetes Secrets)
- `scan` subcommand can POST results to a running `serve` instance (`--server-url`) or print JSON to stdout
- Single Helm chart with two workloads: one Deployment (`serve`) and one CronJob (`scan`)
- Multi-architecture Docker image (`linux/amd64`, `linux/arm64`) published to GHCR

[Unreleased]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.3.0
[0.2.2]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.2.2
[0.1.0-alpha]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.1.0-alpha
