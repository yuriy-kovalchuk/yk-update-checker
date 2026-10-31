# Architecture

yk-update-checker is a single binary with two commands deployed via a single Helm chart.

## Commands

| Subcommand | Typical deployment | Role |
|---|---|---|
| `dashboard` | Kubernetes Deployment (1..N replicas) or a local process | Reads the reports (`report.Poller`), serves the API + embedded UI, starts scans on demand (and on an interval locally), exports `/metrics` while it holds the leader Lease |
| `scan` | Kubernetes CronJob / Job, or a child process of a local dashboard | One-shot: clone repos → extract deps → check versions → write the reports per repository (`--store=kubernetes\|file`), or print JSON (`--store=none`, the default) |

There is no HTTP between the two: the scanner writes, the dashboard reads.

## Component diagram

```mermaid
flowchart LR
    browser(["browser"])

    subgraph dash["update-checker dashboard — 1..N replicas"]
        handler["API + embedded UI<br/>GET / · /api/results · /api/status · /assets/ · /fonts/<br/>POST /api/scan/trigger · GET /health · /ready · /metrics"]
        poller["report.Poller<br/>re-reads the backend every 5s"]
        trigger["trigger.Trigger<br/>kubernetes: Job from the scanner CronJob<br/>local: child scan process"]
        poller -->|"report.Snapshot"| handler
    end

    store[("report.Backend<br/>cluster: RepositoryScan + Dependency custom resources<br/>local: ./data/repositoryscans/*.json + ./data/dependencies/*.json")]

    scan["update-checker scan<br/>clone → walk + extract → resolve versions → write<br/>(--store=none prints JSON instead)"]

    browser --> handler
    handler -->|"start a scan"| trigger
    trigger -->|"Job / child process"| scan
    scan -->|"writes each repository as it finishes"| store
    store --> poller
```

`/metrics` is served by every replica but carries the result metrics only on the one that holds
the leader Lease. Locally there is one process, so it always leads.

## Scan triggering

Three ways a scan runs:

1. **CronJob schedule** (`scanner.schedule`) — the Kubernetes scan, no dashboard involved. Its Job is never retried (`backoffLimit: 0`): a retry would re-clone every repository and append a second history entry to each `RepositoryScan`.
2. **Manual trigger** — `POST /api/scan/trigger`. In a cluster it creates a one-off `Job` copied from the scanner CronJob's JobTemplate, so the scan gets the scanner ServiceAccount and the credential mounts, and `activeDeadlineSeconds` applies to it too. Locally it starts `update-checker scan --store=file` as a child process. Either way one scan at a time: a second click answers `409 already_running`, and `GET /api/status` reports the running state from an answer cached for 5 seconds so polling does not hammer the API server - the UI keeps **Scan now** (and its `r` shortcut) inert while that state says a scan is running.
3. **Local interval** (`dashboard --mode=local --interval=…`) — fires immediately and then on every tick through the same Process trigger.

With `--cronjob` set, `Kubernetes` checks once that it can read that CronJob; if it cannot, the trigger reports unavailable, the dashboard hides the button and the log names the missing permission. There is no in-cluster fallback to an in-process scan: the dashboard is deliberately given no repository credentials, so an in-process scan would clone unauthenticated. Outside a cluster the Process trigger is always available.

Any number of dashboard replicas can run: the reports are cluster state, not process state. On SIGTERM a replica fails `/ready` (liveness `/health` stays up), waits 5 seconds for the Service to stop routing to it, then drains for up to 20 seconds.

One scan, start to finish (cluster shape; locally the dashboard simply starts `scan --store=file` as a child and the API is a directory instead of the Kubernetes API):

```mermaid
sequenceDiagram
    actor U as cron schedule, user click or --interval
    participant D as dashboard
    participant A as Kubernetes API
    participant S as scan (Job pod)
    participant R as report.Backend (CRs)

    U->>D: POST /api/scan/trigger
    D->>A: create Job from the scanner CronJob's JobTemplate
    A->>S: pod starts with the scanner ServiceAccount + credential mounts
    loop once per repository, as soon as that repository finishes
        S->>R: put RepositoryScan, put changed Dependencies, delete vanished ones
    end
    S-->>A: exit non-zero if any repository failed (Job is not retried)
    D->>R: re-read every 5s
    D-->>U: /api/results · /api/status · /metrics (leader only)
```

## Storage

Reports are objects, not memory, and they are the only channel between the two halves (`internal/report`):

- **`RepositoryScan`** (one per repository): `phase` (Succeeded/Failed), `last_scan_time`, `last_success_time`, the classified `error`, `counts` (checked/outdated/failed) and `history` — the last N runs (`scan --history`, default 10, newest first). Named after the repository name plus a hash of it.
- **`Dependency`** (one per checked reference): the scan result itself plus `outdated_since` (first scan that found it behind) and `latest_seen_at` (first scan that saw the current `latest_version`). Its name is a hash of the reference's identity — source, type, path, chart, dependency, pinned version and the digest the manifest pins — so a rescan updates the same object, and a new pin is a new object. References that disappear from the repository are deleted; only references that changed are written.
- Both kinds are `updatechecker.yuriykovalchuk.dev/v1alpha1`, `Namespaced`, with a typed, bounded `status` schema generated from the Go types by `make generate` (a field the schema does not declare is pruned, so a new field needs a regenerate; CI fails on a stale schema) and no status subresource (the writer sends the whole object). `kubectl get repscan` / `kubectl get dep` show the useful columns, `kubectl get updatechecker` both kinds.
- Locally the same two kinds are JSON files under `--data` — see "Where the reports live" below.

There is no per-run copy of the items: etcd is not a time-series database. A repository whose scan failed keeps its previous dependencies (so its outdated alerts do not silently resolve) and gains `phase: Failed` plus the error kind — and the same for a scan that hit `scan_timeout`: every repository that had not finished is recorded as failed with that error rather than left looking unchanged.

### Where the reports live

**In a cluster** the two resources sit in the release's namespace, so `kubectl` reads them without the app:

```bash
kubectl -n yk-update-checker get repositoryscans
kubectl -n yk-update-checker get dependencies -l updatechecker.yuriykovalchuk.dev/outdated=true
kubectl -n yk-update-checker get dependency homelab-fluxcd-d93bcd36fcb6fe67 -o yaml
```

**Locally** they are plain JSON under `--data` (default `./data`, which `.gitignore` excludes) — one file per object, same names and same `metadata`/`status` as the custom resources without the Kubernetes boilerplate. A scan of `examples/config.yaml` with `--store=file` writes:

```text
data/
├── repositoryscans/
│   ├── homelab-8be0b5445fca6eb1.json
│   └── update-checker-0161c41e887a0801.json
└── dependencies/                                    # 68 files, ~270 KB
    ├── homelab-fluxcd-d93bcd36fcb6fe67.json          # kyverno 3.9.1 → 3.9.2, outdated_since set
    ├── homelab-image-019fe22466d6960d.json
    └── update-checker-dockerfile-5d619e3861556a2a.json
```

The file name is the object name, so a rescan rewrites the same file instead of adding one, and
`--data=<dir>` moves the whole tree. `rm -rf data` is how you start from an empty dashboard; nothing
expires on its own. A local dashboard polls that directory every 5 seconds, so the child `scan` it
starts shows up in the UI without a restart.

A report belongs to the release that wrote it: `app.kubernetes.io/instance`, passed as `--instance`
by the CronJob. Its name comes only from the repository name, so a second release in the same
namespace would address the same objects — `Writer` refuses rather than competes: `Prune` leaves a
foreign report alone, and scanning a repository whose report names another release fails as
`unsupported` naming it, instead of overwriting it and silently resolving its alerts.

Because writes are changed-only, `Dependency.status.checked_at` is the scan in which that reference
last *changed*, not the last scan of its repository; "when did the scan run" comes only from
`RepositoryScan.status.last_scan_time`, which is also what `/api/status` reports as `last_scan_at`.

### Error kinds

Every failure is classified into one of a small set of kinds (`internal/errkind`): `auth`, `not_found`, `unreachable`, `timeout`, `rate_limited`, `server_error`, `host_key`, `invalid_version`, `too_large`, `unsupported`, `other`. The classification happens where the failure is understood: the registry client maps HTTP status codes (401/403 → `auth`, 404 → `not_found`, 429 → `rate_limited`, 5xx → `server_error`) and its own checks (size caps, a chart missing from its index, a version that matches nothing); `gitsync` maps git's and ssh's output (an unreadable `known_hosts_file` is `host_key`); network errors and expired deadlines are recognised anywhere. The message stays as the detail.

The kind travels as `check_error_kind` on each result, and as `status.error.kind` on the failed `RepositoryScan`, which the dashboard turns into `repo_errors[]` (`repo`, `kind`, `message`) and `last_error_kind` on `/api/status`; `update_checker_dependency_check_failed_info{…,error_kind}` marks each reference the scan could not check. The dashboard turns it into a specific label ("auth failed", "not found", …) with a "what to check" hint, in the result row, the details drawer and the failed-scan banner. `app.js` `ERROR_KINDS` and `TestDashboardKnowsEveryErrorKind` keep the UI and the vocabulary aligned.

Fetched registry data (Helm `index.yaml` files, OCI tag lists) is cached in a `registry.IndexCache`, and every scan deduplicates lookups per unique registry URL (singleflight + memoized errors). The cache lives for one scan: each scan is a process of its own, so a TTL would only pin memory between runs.

## Monitoring & alerting

The dashboard mounts a Prometheus `/metrics` endpoint. Label values that come from repository content pass through `bound()`: invalid UTF-8 is replaced and long values are cut on a character boundary, because a single label value Prometheus rejects turns the whole scrape into a 500.

With `--mode=kubernetes` the dashboard campaigns for a Lease (`--lease`, the chart passes `<fullname>-leader`) and only the leader emits the result metrics — followers serve the UI but export nothing, so an alert fires once per outdated dependency rather than once per replica. A local dashboard is always the leader. Leader election needs `leases` rights (see the chart's Role); it is not tied to anything else.

Key metrics (the shipped Grafana dashboard charts all of them, mirroring the UI's tiles and lists):

- `update_checker_dependency_outdated_info` — value `1`, one time series per outdated reference. Labels split into a **stable identity** half (`source`, `chart`, `dependency`, `type`, `kind`, `protocol`, `path`, `tag_kind`) and the **volatile change** half (`scope`, `current_version`, `latest_version`). `kind` is the manifest kind the reference lives in (`Deployment`, `CronJob`, `HelmRelease`, …) and `tag_kind` its tag shape (`semver`, `calver`, `date`, `build`, `floating`, `opaque`) - both bounded vocabularies, so grouping by them is cheap; `path` is the repo-relative file. Values copied out of the repository (image names, paths, chart names) are truncated at 200 bytes: the scanned repo is untrusted input and must not mint oversized labels. `current_version`/`latest_version` do start a fresh series on every bump; they stay because the shipped alert rules and the Grafana table render them from `$labels`, and an alert can only annotate labels of its own series. Those label values *are* the series identity, so the collector collapses results that agree on all of them: two identical label sets would make the registry reject the second one and abort the scrape entirely, taking every other metric with it.
- `update_checker_dependencies_scanned{source,type}` — references the latest scan examined, up-to-date ones included. The info metric above is presence-only (a dependency with no update has no series at all), so coverage and outdated-ratio questions are answered by this gauge, not by the absence of series.
- `update_checker_dependency_digest_drift` — value `1`, one series per reference whose image digest moved since the previous scan: a floating tag re-pushed under the same name, or a digest pin that no longer matches what its tag serves. Presence is the signal, and it is reported whether or not a version change was found; the series lives until the next scan.
- `update_checker_dependency_check_failed_info` — value `1`, one series per reference the latest scan could not check, with the identity labels plus `error_kind`.
- `update_checker_dependency_skipped_tags` — per reference, how many candidate tags were rejected as strays; no series when none were.
- `update_checker_last_scan_time` — unix time of the newest successful repository scan over all repositories, read from the `RepositoryScan` resources, so it survives a dashboard restart. The staleness alert relies on it; it is deliberately label-free (see below).
- `update_checker_repository_scan_failed{source,kind}` — `1` while a repository's latest scan failed, else `0`. Its dependencies keep their previous values, so the outdated metrics look unchanged without this.

Both dashboards filter by repository: the UI's repository selector scopes its tiles and list, and the Grafana `repo` variable (`label_values(update_checker_dependencies_scanned, source)`, multi-value with All) goes into every panel query. `update_checker_last_scan_time` has no `source` label, so Grafana's *Last scan* panel stays global: it answers "is the scanner alive at all", and labelling it per repository would turn the staleness alert into one alert per repository.

The chart ships opt-in CRDs (both disabled by default):

- `metrics.serviceMonitor.enabled` — `ServiceMonitor` for Prometheus Operator discovery.
- `metrics.prometheusRule.enabled` — `PrometheusRule` with one `OutdatedDependency<Scope>` rule per configured scope (default: major=warning, minor/patch=info, unknown=warning), `UpdateCheckerScanStale` and `UpdateCheckerNoSuccessfulScan` (`metrics.prometheusRule.staleScan`, default threshold 30h), `UpdateCheckerRepositoryScanFailed` (`metrics.prometheusRule.scanFailed`) and `UpdateCheckerDigestDrift` (`metrics.prometheusRule.digestDrift`). Keep the stale threshold comfortably larger than the scan interval in `scanner.schedule` (default schedule: daily at 00:00 UTC), and the drift rule's `for` well below it, because a drift series only exists for the scan that detected the move.

## Repository walk

Each repository is walked once. Repository content is untrusted, so:

- only regular files are read: a committed symlink could otherwise point the scan at a file outside the checkout, or at `/dev/zero`;
- a file over 10 MiB (`RunnerOptions.MaxFileBytes`) is skipped, not truncated;
- `path:` must be relative (`config.Load`), must exist, and must not leave the checkout through a symlinked directory. Otherwise the repo fails with `not_found` / `unsupported` instead of reporting an empty, "successful" scan.

Skipped files are logged with the reason.

Bounds: two repositories are scanned at a time (two git children fit the memory the 512Mi scan pod
leaves outside the Go heap), each with `parallel_checks` registry lookups, so at most
2 × `parallel_checks` requests run at once. A repository may store at most 2,000 references
(`report.MaxDependencies`); past that it fails as `too_large` and keeps its previous reports.
Measured cost per reference: ~1.3 KB in etcd and ~2 KB of dashboard heap, plus ~5 KB of
short-lived allocation per poll, so the default 128Mi dashboard is comfortable up to ~10,000.

Only one scan runs at a time, whatever started it: the scan holds a Lease (`<fullname>-scan`) in
the cluster, or an exclusive lock on `<data>/.scan.lock` locally; a second scan logs that and exits 0.

## Package layout

```
cmd/update-checker/     entrypoint; dashboard and scan wiring, flag parsing, signal handling
internal/
  api/                  HTTP server bootstrap, route registration, middleware chain, /health + /ready
  report/               the contract between the two halves:
    types.go            RepositoryScan, Dependency, names, labels, phases; kubebuilder markers for make generate
    backend.go          Backend interface (list/put/delete)
    kube.go             Kube: dynamic client, create-or-update, one namespace
    file.go             Files: JSON under ./data, written atomically
    writer.go           Writer.Repo: changed-only writes, stale deletes, outdated_since, history cap; Writer.Prune
    poller.go           Poller: latest Snapshot of a Backend
  scan/
    runner.go           RunnerOptions; walk once, run extractors, check versions concurrently, OnRepo per repository
    types.go            Result
  gitsync/              the only code that runs git or reads a credential: Syncer.Sync, RedactURL
  memlimit/             Go heap limit = 70% of the cgroup memory limit
  kube/                 Connect() (in-cluster config + namespace), Env.Leader (Lease election), Env.Lock (scan Lease)
  errkind/              failure kinds: Wrap where understood, Of anywhere
  dashboard/            GET /, GET /favicon.svg, GET /api/results, GET /api/status, POST /api/scan/trigger, GET /metrics, GET /assets/, GET /fonts/; embeds ui/
  extractor/            Extractor interface over *File (YAML parsed once) + HelmChart, FluxCD, Images and Dockerfile
  registry/             version resolution: HTTPS index.yaml, OCI tag listing, container image tags; scope.go (all
                        version comparison), vendor.go (one dialect per vendor tag API), client.go (limits, HTTP client)
  metrics/              Collector over a report.Snapshot (leader-gated) mounted at /metrics
  trigger/              Trigger interface; Kubernetes (Job from the CronJob) and Process (child `scan`)
  config/               YAML config loader
  middleware/           Recovery, Headers, Logger
  version/              Version, Commit, BuildDate vars (ldflags)
```

## Version detection

### Helm `Chart.yaml` dependencies

For each entry in `dependencies[]`:

- **HTTPS** (`repository: https://...`): fetches `index.yaml`, picks the highest version within the configured `updateType` scope (`patch`, `minor`, `all`; `major` is accepted as an alias of `all` — there is no major-only filter).
- **OCI** (`repository: oci://...`): lists tags via `go-containerregistry`, applies the same scope filter.

### FluxCD `HelmRelease`

`HelmRelease` resources cross-reference source objects in other files, so the repository is walked once with two phases:

1. **Prepare** — during the walk every file goes through `PrepareFile`; FluxCD collects all `HelmRepository` and `OCIRepository` resources, keyed by `namespace/name`. A source written without `metadata.namespace` (Kustomize or a Flux Kustomization sets it at apply time) is keyed `/name` and matches a reference from any namespace unless an exact match exists. An `OCIRepository` is pinned by `spec.ref.semver` (a range, resolved against the registry) or else `spec.ref.tag`; a digest-only ref has no version to compare.
2. **Extract** — after the walk, each `HelmRelease` is resolved against the map (via `spec.sourceRef`, `spec.chartRef`, or inline `repoURL`), then the same version lookup as above runs.

Chart dependencies on `file://` subcharts or `@alias` / `alias:` repositories are skipped: there is no registry to ask. Image references that are still templates (`${VAR}`, `{{ … }}`, `$(…)`) are skipped for the same reason.

### Bump size (scope)

`internal/registry/scope.go` is the only place a bump is classified. Charts are semver (`ChartScope`). Images use the same tag shapes as the image look-up (`ImageScope`): only semver tags with at least `major.minor` have a major/minor/patch boundary; a date, calendar version or single number (build counter, `valkey:9`) reports `unknown`, which the default alert rules treat as a warning, rather than a misleading `major`.

### Container images in workloads

Enabled by `check_images` (default `true`). `Images` contributes one result per distinct
image reference in a YAML document. It deliberately does not enumerate workload kinds: it
looks for `containers`, `initContainers` or `ephemeralContainers` keys whose value is a
sequence, so any custom resource that embeds a PodSpec is covered too. Each result is
labelled `<Kind>/<metadata.name>` and carries `protocol: image`.

Not covered: `HelmRelease.spec.values.image.*`, the usual Flux way to override an image
without writing a pod template. There is no pod spec to walk there, so such pins are
invisible to the scan (open, see AGENTS.md → Known Issues).

### Container image tag comparison

The machinery the `Images` and `Dockerfile` extractors share, in `internal/registry`:

- **Tag classification** (`tag_kind` in the API) decomposes a tag into prefix / numeric core
  / suffix and classifies it: `semver` (`1.24.2`, `v1.12`), `calver` (`2026.10.3`, `202506`),
  `date` (`20260927`), `build` (`server-cuda12-b10689` — version is the last numeric group),
  `floating` (`latest`, `edge`, `main`, `nightly`, …), `opaque` (git SHAs, bare variant names),
  or empty for unclassifiable. Only tags with a numeric core are version-comparable, and a
  candidate counts as an update only if it classifies the same way *and* has the same prefix and
  suffix as the current tag (`comparableShapes`), so `1.27-alpine` never suggests `1.28`.
- **Tag listing** uses OCI `tags/list` with paging, plus the vendor tag API of the registry
  host (Docker Hub, Quay, and Harbor v2.0) for push dates and ordering. Requests are bounded
  per lookup (`imagePageBudget`, `imageMaxPages`, page sizes) so a repository with hundreds of
  thousands of tags cannot stall a scan. Hosts that are not recognised public registries are
  probed with the Harbor v2.0 API at their own address — that guessing is why egress control
  matters (see "Trust model & exposure").
- **Candidate verification** (`verify_image_tags`, default `true`) re-checks the handful of
  winning tags (`imageVerifyCandidates`, default 3) before reporting one, because registries
  list stray tags: a backport build, a fork's rebuild, an arch-specific leftover. A candidate
  is rejected on one hard signal (its own image metadata declares a different version, or the
  same digest is also served under a pre-release *of that very version* — a `9.2` tag holding
  the `9.2.0-rc1` build is not a release) or two soft ones (pushed *before* the current tag; no
  sibling tags; skips a version range that the registry does not list; narrower platform
  coverage than the current tag; an identical digest to other tags, counted once however many
  names share it, because vendors such as Cloudflare publish every release under build-counter
  aliases too; unsigned where the current tag is signed). Rejections are never silent — they
  land in `skipped` with reasons and in the `note` the UI shows.
- **Floating and digest-pinned pins** have no version to compare, so they are reported
  differently: `current_resolved` shows the version the tag actually points at today (from the
  OCI `org.opencontainers.image.version` annotation), `image_digest` records what it points at
  now, `pinned_digest` records the digest the manifest itself pins (empty when the reference
  follows a tag, and part of the reference's identity, so `app:1.0.0` and
  `app:1.0.0@sha256:…` are two reports), and `digest_changed` is set when that digest moved since
  the previous scan — for a mutable tag that is the only signal that an update landed. A reference
  pinned to a digest that no longer matches what the tag serves is flagged on the very first scan.

Results of both extractors flow into everything downstream unchanged: the `type` label is
`image` or `dockerfile` in the API and in Prometheus, and the per-scope alert rules and the
Grafana dashboard need no image-specific handling.

### Dockerfile `FROM` lines

Files named `Dockerfile`/`Containerfile`, with or without a prefix/suffix (`Dockerfile.dev`, `api.Dockerfile`), contribute one result per `FROM` that names a real base image — enabled by `check_dockerfiles` (default `true`, independent of `check_images`) and checked with the same tag comparison, tag verification and digest-drift machinery as workload images.

- Multi-stage builds report **every** base image; the stage that produces the published image is flagged (`final_stage` in the API, a "Stage: final" fact in the UI drawer).
- Skipped: `FROM scratch`, references to an earlier stage of the same build, and build-arg bases (`FROM ${BASE}`) — none of them are registry lookups.
- Each result is labelled `<repo-relative file>/<stage name>`, or `<file>/stage<N>` for unnamed stages — e.g. `apps/api/Dockerfile/build`. The directory is part of the label because a bare file name is not unique in one repository: two modules can each carry a `Dockerfile` whose stages share aliases, and that collision is what made two results indistinguishable to Prometheus.

## Trust model & exposure

The binary has **no authentication or authorization of its own**, and that is
intentional: it is an in-cluster utility whose access control belongs to the
platform, not to the app. What that means in practice:

- **Anything that can reach the Service can use it fully.** `GET /api/results` and
  `GET /api/status` disclose internal repo names, file paths and the image
  inventory, and `POST /api/scan/trigger` starts a scan (a Job copied from the
  scanner CronJob — never a caller-supplied spec). Both `GET` endpoints answer
  `Cache-Control: no-store`, so no proxy in front of the dashboard may reply to a
  poll from cache. `/metrics` is likewise open to
  whoever can reach it. The chart ships a plain `ClusterIP` Service and **no
  `NetworkPolicy`**, so every pod in the cluster qualifies as "can reach it".
  Replacing the reports is *not* part of that surface: writing them needs
  Kubernetes API access, which RBAC controls.
- **Outbound requests are named by the repositories being scanned.** Git remotes,
  `index.yaml` URLs from `Chart.yaml`/`HelmRelease`, and image registries all
  become fetches from inside the cluster. That is the product; there is no
  allowlist to configure. If egress needs bounding, do it at the network layer.

The usual two levers, both outside this chart:

```yaml
# Restrict who may talk to the dashboard/API. Apply in the namespace you install
# into; keep the Prometheus namespace on the ingress allow-list if you scrape.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: update-checker-ingress
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: yk-update-checker
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels: kubernetes.io/metadata.name: <your-dashboard-or-proxy>
      ports:
        - port: 8080
    # metrics scraping, if you use it
    - from:
        - namespaceSelector:
            matchLabels: kubernetes.io/metadata.name: <prometheus-namespace>
      ports:
        - port: 8080
---
# Optional: bound what the scanner may reach. It needs git hosts, chart
# repositories and image registries on outbound 443/22, so enumerate those
# (a default-deny egress policy that also allows DNS + kube-dns to work).
```

If you need real authentication in front of the UI, terminate it at an ingress
or sidecar (`oauth2-proxy`, Basic auth, mTLS) — the app trusts the network
boundary it is given.

## Private registries

Registry lookups are anonymous unless `registry_auth_file` names a Docker config file (the `.dockerconfigjson` of a `kubernetes.io/dockerconfigjson` Secret). The chart mounts `scanner.registryAuth.existingSecret` read-only, and only into the scan pod; a read-only robot account is enough.

- Only the static `auths` entries are read. `credHelpers` / `credsStore` name programs to execute, and a mounted Secret must not be able to make the scanner run one.
- The file is read on every resolve, so a rotated Secret applies to the next scan without a restart. Registry clients, and the tokens they hold, are rebuilt for every scan.
- A configured file that cannot be read fails `config.Load`.
- For Harbor the same account is sent, as basic auth, to its artifacts API, and only to that registry's own host. Without it a private project answers 401 and its images get no push dates.

`imagePullSecrets` is a separate value: it lets Kubernetes pull the checker's own image from a private registry.

Every registry request, go-containerregistry's included, goes through one transport (`policyTransport`):
- it sends a `yk-update-checker` User-Agent;
- it follows at most 5 redirects;
- it never follows a redirect from https to plain http.

## Authentication

Private Git repository auth is configured per-repo in `config.yaml`:

| Type | Mechanism |
|---|---|
| `token` | `Authorization: Basic base64(git:<token>)`, as `http.<repo URL>.extraheader` |
| `basic` | `Authorization: Basic base64(<username>:<password>)`, same mechanism |
| `ssh` | key file via `GIT_SSH_COMMAND`, host key verified against `known_hosts_file` |

Two rules are enforced in `config.Load` **and** at `helm template`, because both
failures are silent otherwise:

- **No credentials in the repo URL.** `https://x-access-token:ghp_…@github.com/…`
  is a credential in the config file, in the git argv (visible in `ps`), in the
  cached clone's `.git/config`, in anything that logs the repo, and it defeats the
  inline-credential guard below. A login without a password is still fine where it
  means the ssh user (`git@github.com:org/repo`).
- **`token` and `basic` require `https://`.** Both send an `Authorization` header,
  and `http://`/`git://` would put it on the wire in plaintext. `http://` stays
  legal for public repos, and ssh is unaffected.

Nothing is injected into the clone URL. Credentials are handed to git per
invocation through `GIT_CONFIG_*`, so they appear neither in the process argv nor
in the clone's `.git/config`.

The scanner only ever reads credential **files** (`token_file`, `password_file`,
`ssh_key_path`, plus inline `token`/`username`/`password`) and knows nothing about
Kubernetes. Everything Kubernetes-shaped lives in the chart
(`templates/_auth-secrets.tpl`):

- `auth.existingSecret` (optional `existingSecretKey`) names a Secret **the user
  created**. The chart never creates or updates a Secret, and `values.yaml` has no
  field for a token.
- Per repo that declares one: a Secret volume with `items` for that single key,
  `defaultMode: 0440` (root-owned file + pod `fsGroup`, so the non-root scanner can
  read it; `0400` would be root-only), mounted read-only at
  `/etc/update-checker-scanner/secrets/<repo name>/<key>`. The file is on the
  Secret's tmpfs for the pod's lifetime — never in argv, never in `.git/config`.
- **Only the scan pod gets them.** The CronJob and the Jobs created from it are
  the only workloads that clone, so the dashboard holds no credentials, no scanner
  config and no writable `/tmp` — and a missing or renamed Secret key cannot stop
  the dashboard from starting.
- The rendered ConfigMap swaps `existingSecret`/`existingSecretKey` for the
  matching `*_file` path, so the scanner never receives a key it does not
  implement. That swap exists because the reference once lived in
  four documents, nothing consumed it, and private repos were cloned
  unauthenticated. `scripts/verify-chart-credentials.sh` (CI) and
  `TestShippedConfigsHaveNoUnknownKeys` keep chart output and Go input aligned.
- `basic` takes its password from the Secret and its username from the config; a
  username is not a secret. Defaults for the Secret key: `token` / `password` /
  `id_ed25519`.
- Two repo names that sanitize to the same credentials directory (`x` and `x-`)
  would give one pod two mounts at the same path, which the API server rejects
  after a clean `helm template`; the chart now fails at render naming both repos.

### What each git subprocess is allowed to do

Every git subprocess runs with a baseline that limits what a hostile repo URL or a
tampered config can do:

| Setting | Why |
|---|---|
| `--` before the URL | a URL starting with `-` is otherwise an option (`-cprotocol.ext.allow=always`) rather than a repository |
| `GIT_ALLOW_PROTOCOL=https:ssh:file:http:git` | exactly the transports `config.Load` accepts; `ext::` (an arbitrary command) dies here too, verified in `TestGitRefusesExtTransport` |
| `GIT_CONFIG_NOSYSTEM=1` | `/etc/gitconfig` belongs to the image, not to this config; it must not be able to change git's behaviour underneath a scan |
| `GIT_CONFIG_GLOBAL=/dev/null` | `GIT_CONFIG_NOSYSTEM` covers only `/etc/gitconfig`; this also excludes `~/.gitconfig` and `$XDG_CONFIG_HOME/git/config`, verified in `TestGlobalGitconfigGetsNoVote` |
| `transfer.credentialsInUrl=die` | git refuses a URL with a plaintext password: `fatal: URL 'http://u:<redacted>@…' uses plaintext credentials` — the belt behind the config rule |
| `GIT_TERMINAL_PROMPT=0` | a scan is never interactive; a missing credential fails fast instead of hanging on a prompt |

The header is scoped to the repo URL (`http.<url>.extraheader`) rather than global,
and measured rather than assumed: `TestCredentialHeaderStaysWithItsHost` runs a
real `git ls-remote` against a server that 301s to a second server and asserts the
second server never receives an `Authorization` header. On git 2.54 (the git in
this image) git re-authenticates for the redirect target instead of replaying the
header, so it fails closed; the scoped key means the credential is not even
eligible for a request to another host, whatever git does in a future version.
That measurement also corrects an earlier assumption: the
"header is replayed to every redirect target" claim does not hold on the git
version this project pins. The part that *was* real is the plaintext transport, and
that is now refused at load time.

Log output goes through `gitsync.RedactURL` (`syncing repo url=…`), because the repo URL is
logged on every scan.

### SSH host keys

`GIT_SSH_COMMAND` always carries `-o IdentitiesOnly=yes -o BatchMode=yes`. Host key
policy is decided by `auth.known_hosts_file`:

- set and readable → `-o UserKnownHostsFile=… -o StrictHostKeyChecking=yes`;
- set but unreadable (a broken mount, a typo) → the clone fails. A verification
  the operator asked for is never silently downgraded to trust-on-first-use;
- unset → `accept-new`, plus a warning naming the repo.

`accept-new` is worth naming precisely: a scan pod is short-lived, its rootfs is
read-only and the pod has no writable `HOME`, so there is no known_hosts to carry
anything between runs. In practice that means *every scan accepts whatever key the
host offers*. A man in the middle still cannot steal a deploy key (ssh signatures
are bound to the session), but it can serve forged repository content, and forged
content means forged scan results. So the chart defaults to
`scanner.ssh.strictHostKeyChecking: true` and fails the render for an ssh repo when
`scanner.ssh.knownHosts` is empty; the rendered `known_hosts` lands in the config
ConfigMap and `known_hosts_file` is set automatically for every ssh repo. Setting
it `false` is the explicit opt-out.

The OpenSSH detail that trips people up, quoted from `authfile.c`:

```c
/* if a key owned by the user is accessed, then we check the permissions of the
   file. if the key owned by a different user, then we don't care. */
if ((st.st_uid == getuid()) && (st.st_mode & 077) != 0)  /* "This private key will be ignored." */
```

The check applies **only to keys the running uid owns**, and then *any* group/other
bit fails, not just write bits. A mounted key is root-owned, so `0440` is accepted
in the pod — but a key file of your own at `0440` is rejected as "too open". (An
earlier version of these docs claimed ssh objects to keys group/other can *write*;
that is simply not how it works.)

### Credential handling

Files, not environment variables — but not for the reason usually given. A
`valueFrom.secretKeyRef` puts only a **reference** in the Pod spec: `kubectl get
pod -o yaml` shows the Secret name and key, never the value. At the API boundary
the two forms are equally safe, and RBAC on `secrets` is the control for both.
The difference is inside the container and across time:

| | Secret as an env var | Secret as a file (what this chart does) |
|---|---|---|
| Pod spec, `get pods` | reference only | reference only — **no difference** |
| inside the container | the value sits in the process environment for the container's whole life: `kubectl exec … env`, `/proc/<pid>/environ` for any same-uid process, inherited by every child process, and applications routinely log their environment — which is the actual argument behind CIS Kubernetes Benchmark 5.4.1 ("Prefer using secrets as files over secrets as environment variables") | one path read when needed; a path-only disclosure still has to be combined with a read to yield a credential |
| rotation | *"If a container already consumes a Secret in an environment variable, a Secret update will not be seen by the container unless it is restarted"* | the kubelet propagates updates (sync period + cache delay) — which is why the mount is a directory mount and not `subPath`: subPath mounts do not receive updates |
| shape | no file mode at all; `envFrom` exposes **every** key in the Secret | `items` narrows to the single key, `defaultMode: 0440`, `readOnly: true` |
| `ssh` | impossible — OpenSSH needs a key file and checks its permissions | the only shape that covers token, basic and ssh uniformly |

Equal at the API boundary, strictly better inside the container, and the only
option that works for SSH deploy keys in the first place. One honest exception:
the credential *does* reach an environment — the short-lived `git` child, via
`GIT_CONFIG_KEY_n`. That adds nothing an attacker with code execution in the pod
did not already have (the same uid can read the mounted file directly), and the
alternatives are worse: `git -c http.extraheader=…` puts it in argv where `ps`
sees it. A `git-credential` helper would remove the env step as well, and is the
natural next change if the trade-off is unacceptable.

What each level of cluster access buys — the first three lines are why the design
looks the way it does:

| Access | What it reveals |
|---|---|
| `get pods` / `get deployments` in the namespace | Secret **names** only (`secretName: homelab-github-token`) — which repos are private and where to look. True for env-var references too; the value is never in the Pod spec |
| `create jobs` (what the dashboard's Role grants) | **every Secret in the namespace**: creating a Job means choosing its pod spec, and that spec can mount any Secret in it. The trigger only ever copies the scanner CronJob's JobTemplate, so no caller-controlled pod spec is reachable through it, and the pod that parses hostile repository content runs as a *different* ServiceAccount, which cannot create Jobs at all |
| `get configmaps` | the whole `scanner.config`. This is why inline `auth.token` / `auth.password` are refused at render: a ConfigMap is a separate object, but read access to it is granted far more widely than `get secrets` |
| `list`/`watch` on secrets | everything in the namespace, this chart's included — upstream notes that list/watch grants read access to *all* Secret data, not just what a workload references. Nothing here can help; RBAC is the control |
| the scan pod itself (e.g. SSRF with file read) | every repo's credential, since one process scans all repos. Single-container blast radius is deliberately the whole configured set |
| root on the node | the tmpfs mount, plus anything in the pod's memory. Kubernetes documents that node compromise exposes Secrets of pods on it |

The chart has two identities and two privilege sets. The dashboard ServiceAccount
gets `batch/jobs` (`create`, `get`, `list`), `batch/cronjobs` (`get`), `get`/`list`
on the two report kinds, and `create` on `leases` plus `get`/`update` on its own
Lease — **no `secrets` verbs**. The scanner ServiceAccount gets `get`, `list`,
`create`, `update`, `delete` on the two report kinds, plus `create` on `leases`
and `get`/`update` on the scan Lease (one scan at a time), and nothing else; that pod is the one holding the credentials and reading
untrusted repository content, so it must not be able to create a Job, read a
ConfigMap or touch a Secret. As the table row above says, `jobs: create` reaches
Secrets transitively, so that right stays on the dashboard, which parses no
repository content. Credentials are never placed in a git URL, in a process's
argv (visible in `ps` and in git's own error messages) or in the clone's
`.git/config`.

Three things only the cluster operator can do, and the chart does not pretend
otherwise:

- **Encrypt Secrets at rest.** Secrets are base64, not encrypted, unless
  `encryptionAtRest` is configured — `kubectl get secret x -o jsonpath='{.data.token}' | base64 -d`
  is the entire attack, and etcd backups inherit the problem.
- **Isolate the namespace.** Anyone who can *create a pod* in a namespace can
  mount any Secret in it, regardless of what this chart declares. Do not run this
  workload next to namespaces holding something important.
- **Bound egress** (see "Trust model & exposure"). A scan fetches whatever the
  configured repos name, and the scan pod also holds the credentials for all of
  them, plus rights to write the reports.
