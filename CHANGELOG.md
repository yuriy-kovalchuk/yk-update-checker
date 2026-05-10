# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0-alpha] - 2026-04-27

### Added
- CronJob-based manual scan triggers via Kubernetes Jobs (`api.enableTrigger`)
- Private repository support: token, HTTP basic, and SSH key authentication
- Auth credentials sourced from Kubernetes Secrets (`existingSecret` / `existingSecretKey`)

## [0.3.0] - 2026-04-25

### Changed
- Replaced `go-git` library with the `git` binary for all clone and fetch operations, eliminating peak memory spikes on large monorepos

## [0.2.2] - 2026-04-25

### Fixed
- Reduced memory footprint during Helm registry index fetches and repository scans

## [0.2.1] - 2026-04-25

### Changed
- Optimised in-process memory allocation across the scan pipeline

## [0.2.0] - 2026-04-25

### Added
- Multi-architecture Docker images (`linux/amd64`, `linux/arm64`)
- Initial `README.md`

### Changed
- Upgraded golangci-lint to latest version in CI

## [0.1.0-beta] - 2026-04-25

### Added
- Initial release
- FluxCD `HelmRelease` manifest scanning with cross-file reference resolution
- Helm `Chart.yaml` dependency scanning for HTTPS and OCI repositories
- Three-component architecture: API (Deployment), Scanner (CronJob), Dashboard (Deployment)
- SQLite-backed scan history with pagination
- Configurable upgrade scope: `all`, `major`, `minor`, `patch`
- Multi-arch Docker images and Helm chart published to GHCR

[Unreleased]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v1.0.0-alpha...HEAD
[1.0.0-alpha]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.3.0...v1.0.0-alpha
[0.3.0]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.1.0-beta...v0.2.0
[0.1.0-beta]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.1.0-beta
