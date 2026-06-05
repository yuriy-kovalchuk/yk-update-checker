# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.1.0-alpha...HEAD
[0.1.0-alpha]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.1.0-alpha
