# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Reports are Kubernetes resources**: two CRDs in the `updatechecker.yuriykovalchuk.dev/v1alpha1` group — `RepositoryScan` (one per repository: phase, error + kind, counts, the last N runs) and `Dependency` (one per checked reference, with `outdated_since` and `latest_seen_at`). `kubectl get repositoryscans` / `kubectl get dependencies -l updatechecker.yuriykovalchuk.dev/outdated=true` work without the UI, results survive a restart, and a scan no longer depends on reaching a particular replica. Locally the same two kinds are JSON under `--data` (default `./data`)
- **Scan**: `--store=kubernetes|file|none` — the backend a scan writes to (in-cluster default `kubernetes`, outside a cluster `none` so a stray kubeconfig cannot be used). Results are written **as each repository finishes**, so a scan that hits `scan_timeout`, a eviction or a crash leaves a complete record; every repository it did not reach is recorded failed. A repository that failed to sync keeps its previous `Dependency` objects and gets `phase: Failed` + the error kind, and `scan` exits non-zero. `--history` keeps the last N runs per repository (chart: `scanner.history.runs`)
- **Dashboard**: `dashboard --mode=kubernetes|local` replaces `serve`. Both read the reports through one `report.Backend` (`report.Poller`, every 5s) and serve the same UI/API; `--interval` rescans on a timer (default `0`: on demand only), a manual scan starts a Job copied from the scanner CronJob in the cluster or an `update-checker scan` child process locally, and `--config`/`--data` are required in local mode
- **Leader election**: dashboard replicas campaign for a Lease (`--lease`) and only the leader exports the result metrics, so `replicaCount > 1` is allowed and produces one alert per outdated dependency instead of one per replica; followers still serve the UI. `/metrics` is mounted whenever the binary runs as `dashboard`
- **Metrics**: `update_checker_repository_scan_failed{source,kind}` (presence per repository whose latest scan failed) — the alert that catches "the scan broke", which the presence-only result metrics cannot show. `update_checker_last_scan_time{source}` now comes from the reports, so its series no longer resets when the dashboard restarts
- **Helm**: `crds/` ships both schemas — `status` preserves unknown fields, everything else has `additionalProperties: false`, and the printer columns show phase/outdated/counts/age. Helm installs them on `helm install` and **never upgrades** them, so a schema change is a `kubectl apply -s`
- **Helm**: `UpdateCheckerRepositoryScanFailed` alert rule (any repository whose latest scan failed, labelled with the repo and the error kind)
- **Deploy**: `make kind` creates (if missing) a kind cluster, builds `$(IMAGE):dev`, loads it and installs the chart with `image.pullPolicy=Never` — `KIND_CLUSTER=` and `VALUES=` override it, and the default overlay is `examples/test-values.yaml`
- **Private registries**: `registry_auth_file` (a Docker config file) and the chart's `scanner.registryAuth.existingSecret` authenticate image and OCI chart lookups; Harbor's artifacts API gets the same robot account, so private projects have push dates. Only static `auths` entries are read — never credential helpers
- **Helm**: `imagePullSecrets` for pulling the checker's own image from a private registry
- **Helm**: `UpdateCheckerNoSuccessfulScan` alert (`absent(update_checker_last_scan_time)`), which fires when the scanner has never succeeded — the case `UpdateCheckerScanStale` cannot see. It no longer fires after a dashboard restart, because that timestamp now lives in the reports
- **Dashboard**: `--interval` (default `0`) rescans on a timer without waiting for the CronJob
- **Errors carry a kind** (`auth`, `not_found`, `unreachable`, `timeout`, `rate_limited`, `server_error`, `host_key`, `invalid_version`, `too_large`, `unsupported`, `other`): `check_error_kind` on results, `status.error` + `kind` on the `RepositoryScan`, `last_error_kind` and per-repo `repo_errors` on `/api/status`, the `update_checker_dependency_check_failed_info{…,error_kind,type,kind}` metric, and a specific label plus a "what to check" hint in the dashboard instead of "lookup failed"
- **CI**: every GitHub Action pinned to a commit SHA, `govulncheck` and Helm pinned to exact versions, and Dependabot for Go modules, Actions and Docker base images
- **Helm**: two ServiceAccounts and two Roles instead of one of each. The dashboard's may `create/get/list jobs`, `get cronjobs`, `get/list` the two report kinds, and elect a leader (`create leases`, `get/update/patch/delete` on its own Lease by name). The scan pod's (`<fullname>-scanner`) may only `get/list/create/update/delete` the two report kinds — it holds the repository credentials and parses hostile content, so it must not be able to create a Job (a Job spec can mount any Secret in the namespace) or read anything else. The dashboard mounts no credential Secret at all, so a missing or renamed Secret cannot stop it from starting
- **Helm**: `scanner.ssh.knownHosts` (paste `ssh-keyscan` output) plus `scanner.ssh.strictHostKeyChecking` (default `true`). The content is rendered into the config ConfigMap at `/etc/update-checker/known_hosts` and `auth.known_hosts_file` is set for every ssh repo automatically — which is what turns ssh from "accept any key" into verification
- **Config**: `auth.known_hosts_file` — with it, git runs ssh with `-o UserKnownHostsFile=… -o StrictHostKeyChecking=yes`; without it, `accept-new` plus a warning naming the repo. `IdentitiesOnly=yes` is now always set
- **Security hardening for git subprocesses**: `GIT_ALLOW_PROTOCOL` restricted to the transports `config.Load` accepts (`ext::` cannot run a command — `TestGitRefusesExtTransport`), `GIT_CONFIG_NOSYSTEM=1` (the image's `/etc/gitconfig` gets no vote), `transfer.credentialsInUrl=die` and `GIT_TERMINAL_PROMPT=0` on every call. The `Authorization` header is now scoped to the repo URL (`http.<url>.extraheader`) instead of global, and `TestCredentialHeaderStaysWithItsHost` runs a real cross-host redirect and asserts the second server never sees the credential
- **Deploy**: the scan pod's ServiceAccount (`<fullname>-scanner`) is separate from the dashboard's and holds only the two report kinds (`get/list/create/update/delete`) — it needs the token to write reports, so it is mounted, but the pod that parses hostile repository content can touch nothing else in the namespace. It is also the only workload with repository credentials, and it is never given a writable `Secret` path: a Job spec can mount any Secret in the namespace, so `jobs: create` is granted to the dashboard only, whose pod has no credentials at all
- **Images**: the runtime base is pinned by digest as well as tag (`alpine/git:v2.54.0@sha256:4fac439e…`) — that image provides `git`, the binary that is handed every repository credential
- **Helm**: credential mounts are mode `0440`, not `0400` — Secret volume files are owned by root and `fsGroup` only changes the group, so `0400` leaves them unreadable to a non-root pod. The chart now refuses to render a credential mount when `fsGroup` is unset (`scanner.podSecurityContext`)
- **Helm**: inline `auth.token` / `auth.password` are rejected at render time. They would be written into the ConfigMap, and ConfigMap read access (`get configmaps`) is granted far more widely than Secret read access; use `auth.existingSecret` or an `auth.*_file` path. `auth.username` stays inline on purpose — a username is not a secret
- **Helm**: `auth.existingSecretKey` selects which Secret key a repo uses (defaults `token` / `password` / `id_ed25519`), and a bad credential config fails at `helm template` rather than during a scan — `existingSecret` with no `auth.type`, an unknown `auth.type`, or a Secret reference next to an explicit `token_file`. `scripts/verify-chart-credentials.sh` (new; runs in the CI `helm` job and as `make chart-creds`) asserts the whole chain: Secret in values → read-only volume in both workloads → file path in the config the scanner reads, plus the failure cases
- **Examples**: `examples/credentials.values.yaml` (two private repos: token, basic, and an SSH deploy key with a non-default key name) and `examples/private-repo-secrets.yaml` (the matching Secrets you create); `examples/github-token-secret.yaml` folds into the latter
- **Images**: container images referenced by Kubernetes workloads are now checked for newer tags (`check_images`, default `true`) — the scanner walks any YAML document for `containers`, `initContainers` and `ephemeralContainers` arrays rather than enumerating workload kinds, so custom resources that embed a PodSpec are covered too; results are labelled `<Kind>/<metadata.name>`
- **Images**: tags are compared by *shape*, not blindly by semver (`tag_kind` in the API): `semver`, `calver` (`2026.10.3`), bare `date` (`20260927`), `build` counters (`server-cuda12-b10689`), `floating` (`latest`, `edge`, `nightly`, …) and `opaque` (git SHAs, variant names). A candidate must match the current tag's prefix and variant, so `1.27-alpine` is never told to move to plain `1.28`
- **Images**: `verify_image_tags` (default `true`) re-checks the winning candidate before offering it, so stray tags — backport builds, fork rebuilds, arch-specific leftovers, unsigned aliases of a nightly — are skipped rather than reported, and every rejection is stated with its reason in the new `note` and `skipped` fields
- **Images**: push dates and digests are read from the Docker Hub, Quay and Harbor tag APIs; GHCR tag listings are paged in 1000s so a busy repository's newest tags are actually reached, and re-pushed older tags are tolerated
- **Images**: digest drift — `digest_changed` marks a floating tag that moved since the previous scan, and a reference pinned to a digest reports a mismatch with what its tag now serves on the very first scan. Bumping a tag in Git is deliberately *not* drift
- **API**: results carry the file the reference was found in (`path`), what a floating tag currently resolves to (`current_resolved`), its digest (`image_digest`), and the push dates of both tags (`current_pushed_at`, `latest_pushed_at`)
- **Dashboard**: redesigned — needs-attention lane, one group per dependency, filter tiles, version diff highlighting, a linkable details drawer, project logos per category and a phone layout
- **Dockerfiles**: base images pinned in `FROM` lines of `Dockerfile`/`Containerfile` (including `Dockerfile.dev`, `api.Dockerfile`) are now checked for newer tags, reusing the container-image machinery — tag classification, candidate verification, digest pinning and drift all behave as they do for workload images
- **Dockerfiles**: multi-stage builds report every real base image and flag the stage whose image the build publishes (`final_stage` in the API, shown as a "Stage: final" fact in the UI); `FROM scratch`, references to an earlier stage of the same build and build-arg bases (`FROM ${BASE}`) are skipped as non-registry lookups
- **Dashboard**: "Dockerfiles" category — filter chip, category lane and the Docker logo alongside the Kubernetes, Helm and FluxCD ones; results are labelled `<file>/<stage>` (`Dockerfile/build`, `api.Dockerfile/stage1`)
- **Helm / config**: `check_dockerfiles` (default `true`) turns Dockerfile scanning off independently of `check_images`
- **Metrics**: `update_checker_dependency_outdated_info` now says *what* is outdated and *where* — new `kind` (manifest kind: `Deployment`, `CronJob`, `HelmRelease`, …), `path` (repo-relative file the reference lives in) and `tag_kind` (bounded tag-shape vocabulary); `ChartRef` and `Result` carry the manifest kind end to end
- **Metrics**: `update_checker_dependencies_scanned{source,type}` counts references examined per scan, up-to-date included — the denominator the presence-only info metric has no series for
- **Metrics**: `update_checker_dependency_digest_drift` (presence per reference whose image digest moved since the previous scan) plus a default-on `UpdateCheckerDigestDrift` alert rule, which makes the UI’s “moved digest” signal alertable
- **Dashboard**: “Digest drift” and “Scanned references” stat panels; the results table gains Kind, File and Tag shape columns
- **Config**: `scan_timeout` (default `30m`, `0` disables) bounds a whole scan; a hung clone can no longer hold the `scanning` flag forever and block every later CronJob run

### Changed
- **Scan**: only regular files are read from a repository (a committed symlink could point outside the checkout or at `/dev/zero`), files over 10 MiB are skipped, and a repo `path:` must be relative, exist and stay inside the checkout — a typo now fails the repo instead of scanning nothing
- **API**: `/ready` fails during shutdown, which waits 5s before draining; panic recovery no longer writes into a started response; every listener keeps `ReadHeader`/`Read`/`Write`/`Idle` timeouts
- **Registry**: failed lookups are remembered for 1 minute instead of the whole cache TTL; a cancelled caller no longer fails other callers of the same fetch; expired cache entries and registry clients (with their tokens) are dropped before each scan; the redirect and User-Agent policy also covers go-containerregistry's own requests
- **Trigger**: the Kubernetes trigger checks at startup that it can read its CronJob; inside a cluster an unusable trigger hides the scan button instead of failing with 500 or scanning in-process without credentials; "is a scan running" is cached for 5s instead of two API calls per dashboard poll
- **Helm**: the CronJob name is capped at 52 characters (long release names rendered an invalid one)
- **Build**: `make docker-push` tags `:latest` only with `TAG_LATEST=true`
- **Build**: Go 1.26.10 and `golang.org/x/net` v0.60.0 — `govulncheck` reported 16 reachable standard-library vulnerabilities on 1.26.5
- **Scan**: a repository that fails to sync keeps its previous results instead of disappearing (its alerts no longer resolve and re-fire); `last_error` is set in CronJob mode too
- **Scan**: image bump size uses the same tag rules as the image look-up — date, calendar-version and single-number tags report `unknown` instead of `major`
- **Scan**: Flux sources without `metadata.namespace` (set by Kustomize at apply time) now resolve; `OCIRepository` `spec.ref.semver` is supported; `file://` / `@alias` chart dependencies and templated image references are skipped instead of reported as errors
- **Scan**: a configured `known_hosts_file` that cannot be read fails the clone instead of falling back to trust-on-first-use
- **Metrics**: the `kind` label is populated (it was always empty); label values are made valid UTF-8 and truncated on a character boundary (a cut multi-byte character made `/metrics` return 500); `/metrics` no longer depends on a mounted ServiceAccount token
- **API**: `POST /api/scan/trigger` answers 409 while a scan is being started, and 503 when the trigger is unavailable
- **Config**: `scan_timeout` is validated at load time in both commands
- **Memory**: the binary sets its Go heap limit to 70% of the container memory limit (git shares the budget); the chart no longer sets `GOMEMLIMIT`; the scanner's default memory limit is 512Mi and Helm indexes are capped at 20 MB, decoded one at a time
- **Registry**: every HTTP client sends a User-Agent, follows at most 5 redirects and refuses https→http downgrades
- **Helm**: credential mount directories use exactly `config.SafeName`'s rule (non-ASCII letters kept, trailing `-` kept); both workloads share one container template
- **Breaking (config)**: `auth.type: token` and `auth.type: basic` now require an `https://` repo URL. Both send an `Authorization` header, and over `http://` or `git://` that header is plaintext on the wire. `http://` remains valid for public repos with no credential. Enforced in `config.Load` and at `helm template`
- **Breaking (config)**: a repo URL that embeds credentials (`https://x-access-token:ghp_…@github.com/…`) is rejected. Such a URL puts the credential in the config file, in the git argv where `ps` sees it, in the cached clone's `.git/config` and in the scan log, and it got around the inline-credential guard. Move it to `auth` (`existingSecret`). A login without a password still works where it means the ssh user
- **Breaking (config)**: credentials configured with no `auth.type` are rejected — the type is what `scan.authEnv` switches on, so without it the token is read, never used, and the repo fails as an unauthenticated permission error
- **Breaking (Helm)**: a repo with `auth.type: ssh` and no `scanner.ssh.knownHosts` fails to render, because a short-lived pod on a read-only rootfs cannot remember a host key between runs and `accept-new` therefore means "believe the first key offered" — forged repo content means forged scan results. `scanner.ssh.strictHostKeyChecking: false` opts out explicitly
- **Helm**: two repo names that sanitize to the same credentials directory (`x` and `x-`) now fail at `helm template`. They used to render cleanly and then be rejected by the API server for a duplicate `mountPath`
- **Config**: `update_type` and `auth.type` are validated at load time instead of silently falling back — `update_type: pathc` used to report major-version jumps you never asked for, and `auth.type: tokne` used to clone private repos with no credential at all. Both now fail with the key named. Keys this build does not read emit a startup warning with their exact path (`repos[0].auth.existingSecret`, `git_cache_dirr`, …) and keep working, so a config written for a newer image never breaks a scan
- **Docs**: the trust model is now stated rather than implied. `docs/architecture.md` gained "Trust model & exposure" — what an unauthenticated caller can do (read the inventory, trigger a scan; writing reports takes Kubernetes RBAC), that the chart ships no `NetworkPolicy`, and that outbound fetches are named by the repositories being scanned — plus a worked `NetworkPolicy` example. `README.md` carries the warning next to the `helm install` command and `values.yaml` says it in the `service:` block; access control and egress bounding stay cluster-operator concerns
- **Commit conventions**: the two git hooks now enforce `docs/commits.md` exactly — `type: description`, no scopes, no `revert` type, plus the 72-character subject limit the docs already stated but nothing checked. `Merge …` and `Revert "…"` (subjects git writes itself) are exempt in both hooks; `commit-msg` previously rejected them, so `git merge` could not create a commit without bypassing the hook
- **Metrics**: the label set *is* the series identity of `update_checker_dependency_outdated_info`, so adding `kind`/`path`/`tag_kind` changes it — queries or alerts that matched the old set literally should be re-checked. Label values copied out of repository content (image names, paths, chart names) are truncated at 200 bytes
- **Docs/config**: `update_type: major` is documented as an accepted **alias of `all`** — the checker reports the highest newer version it finds and has no major-only filter. No behaviour change; the wording in `README.md`, `values.yaml`, `examples/config.example.yaml`, `docs/local-testing.md` and `docs/architecture.md` now matches the code, and a test pins it
- **Helm**: the scan CronJob now sets `activeDeadlineSeconds` (default 40m) and `startingDeadlineSeconds` (default 10m), so a wedged Job releases the `Forbid` slot and a missed schedule is noticed instead of silently skipped; the values follow `scanner.scan_timeout` server-side
- **CI**: pull request builds no longer publish. `cd-dev.yaml` granted `packages: write` (and
  `pull-requests: write`) at workflow level and pushed `pr-<n>-<sha>` images from PR runs, which
  execute untrusted Dockerfile content with that run's token. Now only the master-push job holds
  `packages: write`; PR runs compile both platforms with nothing exported. The "dev image for this
  PR" comment is gone with it, since there is no PR image to pull
- **CI**: quality gates bite. `golangci-lint` is no longer `continue-on-error`; both Trivy scans
  keep their full-severity SARIF upload and gained a blocking gate on fixable CRITICAL/HIGH
  findings (`exit-code: 1`, `ignore-unfixed: true`); a `govulncheck ./...` job runs on every push
  and pull request. The coverage upload stays non-blocking on purpose
- **Internal**: the registry HTTP plumbing, YAML decoding, git helpers, JSON responses and the scan results payload are shared between the chart and image code paths instead of being implemented twice
- **Helm**: chart versioning is no longer rewritten in CI. `Chart.yaml` carried `version`/`appVersion` `0.1.0` while `v0.3.0` was the last release, and since `deployment.yaml` falls back to `.Chart.AppVersion` for the image tag, `helm install ./charts` from a checkout asked for `0.1.0` — a tag that was never built. `cd.yaml` now packages what the file says and fails the release unless both fields match the tag; bumping them is part of a release-prep commit (README "Releasing"), and the file is set to `0.3.0`, the last release
- **Docs**: the architecture and README diagrams are **mermaid** (`flowchart` for the component graph, `sequenceDiagram` for one scan end to end) instead of box-drawing ASCII, and the local report layout is written down where a local user reads it: `./data/repositoryscans/*.json` + `./data/dependencies/*.json` (default `--data`, gitignored), one file per report object, named exactly like the custom resource

### Removed
- **Breaking (CLI)**: `serve` is `dashboard` (`--mode=kubernetes|local`); `scan` no longer takes `--server-url`, and a scan in a cluster writes reports instead of POSTing them. `--cronjob` moved to the dashboard, where it selects the Job trigger
- **Breaking (config)**: `registry_cache_ttl` is gone — every scan is its own process with a fresh `registry.IndexCache`, nothing is reused between scans
- **Breaking (metrics)**: `update_checker_scan_duration_seconds` is gone, since nothing aggregates scan runs any more. `update_checker_last_scan_time` stays but is read from the reports, so it survives a dashboard restart
- **Breaking (Helm)**: `scanner.trigger` (including the `inline` mode where serve scanned in-process) and `scanner.interval`. The CronJob is the scanner and the dashboard only starts Jobs; `replicaCount > 1` is allowed because the state lives in the API. The scan CronJob no longer sets `automountServiceAccountToken: false` — writing reports is its job — which is why its ServiceAccount is restricted to the two report kinds
- **Internals**: `scan.Service`/`Coordinator`/`Store`, `internal/scheduler`, `POST /api/scan/results` and `ResultsPayload`, and the TTL registry cache. A scan run has no state to coordinate: it writes and exits
- **Build**: `make test-cover` and `make deps-check`

### Fixed
- **Config**: `auth.type` was trimmed into a loop **copy**, so a value like ` basic ` passed validation while the stored config kept the spaces — which `scan.authEnv`'s switch does not match, i.e. a credential silently dropped. Normalization now writes through to the config; case is still not forgiven (`Basic` errors rather than disabling the credential quietly)
- **Logging**: `syncing repo url=…` runs through `redactURL`, so a URL that carries a login can never reach the log
- **Docs**: four claims corrected after review, each against the source rather than memory. (1) ssh does **not** object to keys group/other can read or write in general — `authfile.c` checks key permissions *only when the key is owned by the uid running ssh*, and then rejects any group/other bit; a root-owned `0440` mount passes, a key of your own at `0440` does not. (2) A `valueFrom.secretKeyRef` env var does **not** put a Secret value in the Pod spec — the value is only in the Secret object either way, and the real difference is process lifetime, logging, rotation and file mode. (3) The chart's narrow Role **is** a path to other Secrets: `jobs: create` lets a Job mount any Secret in the namespace, so the old "cannot be coaxed into reading unrelated Secrets" sentence was only true of `get secrets`. (4) Audit S4's "git replays the credential header to every redirect target" does not hold on git 2.54 — measured: git re-authenticates for the redirect host and never presents the header there; the genuine defect was the plaintext-transport case, which is now refused
- **Private repositories**: `auth.existingSecret` finally does what `README.md`, `values.yaml` and the examples promised. The Secret stays yours to create — the chart only mounts it: one Secret key per repo, read-only, mode `0440`, at `/etc/update-checker-scanner/secrets/<repo name>/<key>`, in both the scan CronJob and the serve pod. The rendered ConfigMap replaces `existingSecret`/`existingSecretKey` with the matching `auth.token_file` / `password_file` / `ssh_key_path`, so the scanner never sees a key it does not implement. Before this the reference was dropped on the floor and private repos were cloned **unauthenticated**
- **Metrics**: two results agreeing on every metric label made `/metrics` answer **500** and dropped *every* series in the response, including the staleness one: the registry rejects a series whose label set was already collected, which aborts the whole scrape. The collector now emits one series per label set and logs how many it collapsed
- **API / Dashboard**: a Dockerfile result's `chart` label now includes the repo-relative directory (`apps/api/Dockerfile/build`, not `Dockerfile/build`). Two modules each with a `Dockerfile` were one identity before, which is how those duplicate label sets were produced
- **Config**: a repo `name` that mapped to `.` or `..` cloned one directory *above* the workspace; such names are now rejected, as are names whose derived directory is over-long
- **Config**: a repo `repo:` URL shaped like a git option (`-c…`) or the `ext::` transport reached the git argv as an option; URLs are now restricted to the `https`, `http`, `ssh`, `git` and `file` transports plus scp-style SSH and local paths, and every git invocation ends option parsing with `--` before the URL
- **Registry**: OCI tag listings, vendor tag APIs and referrer lookups decoded response bodies with no size limit; every JSON response is now capped at 10 MB, matching the existing `index.yaml` cap
- **Registry**: the eight version caches shared one singleflight key space, so a digest-shaped *tag* could make a tag lookup receive a referrer lookup's value and panic the process; each store's keys are now qualified by store name
- **Scan**: digest-drift detection identified a dependency without its file path, so one Dockerfile's moved digest could report drift for an identically-pinned sibling file
- **Server**: only `ReadHeaderTimeout` was set; whole-request read, write and keep-alive idle timeouts are now set
- **Server**: responses now carry a default-deny `Content-Security-Policy` and `Referrer-Policy: no-referrer`; the dashboard uses no inline handlers and no external origins, so it needs no exemptions
- **Helm**: `Chart.yaml` `home`/`sources` still pointed at the retired `yk-helm-update-checker` repository, and its `description` claimed a Material Design UI
- **Docs**: `AGENTS.md` documented `GET /api/scan/results` / `GET /api/scan/status`, routes that do not exist; the auth section of `docs/architecture.md` described credentials injected into the clone URL, which is not how git is invoked; `docs/local-testing.md` claimed `examples/config.yaml` is gitignored (it is tracked) and named the wrong Go version; `values.yaml` was missing `nameOverride`, `fullnameOverride` and `scanner.extraEnv`, all three of which the templates read

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

[Unreleased]: https://github.com/yuriy-kovalchuk/yk-update-checker/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.3.0
[0.2.2]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.2.2
[0.1.0-alpha]: https://github.com/yuriy-kovalchuk/yk-update-checker/releases/tag/v0.1.0-alpha
