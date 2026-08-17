# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.2.2...HEAD
[0.2.2]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.2.2
[0.1.0-alpha]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.1.0-alpha
