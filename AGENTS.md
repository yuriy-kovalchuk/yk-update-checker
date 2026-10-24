# yk-update-checker

## Overview

Kubernetes-native GitOps dependency update checker. Scans git repositories for outdated Helm chart dependencies and FluxCD HelmRelease resources, then surfaces results via a web UI. Deployable as a single Helm chart; runs as one binary in two commands that never talk over HTTP — the scanner writes reports, the dashboard reads them.

## Architecture

Single binary (`update-checker`) with two commands (plus `version`):

| Command | Role |
|---|---|
| `dashboard` | `--mode=kubernetes\|local`: read the reports (`report.Poller`), API + embedded UI, start scans (Job / child process), export `/metrics` while leading |
| `scan` | One-shot: clone repos → extract deps → check versions → write reports per repo (`--store=kubernetes\|file`) or print JSON (`--store=none`) |

**Key packages:**

- `cmd/update-checker/` — entrypoint; wires backend, runner, writer, trigger, poller, server (a 10-line `every()` loop replaces the old scheduler package)
- `internal/report/` — the contract between the two halves: `Backend` (list/put/delete) with `Kube` (dynamic client, create-or-update) and `Files` (JSON under `--data`) impls; `types.go` (RepositoryScan, Dependency, names, labels, `RepoError`); `Writer{Backend, History}.Repo(ctx, repo, results, err)` (changed-only writes, stale deletes, digest drift, `outdated_since`, `latest_seen_at`, history cap, failed scan keeps the repo's dependencies); `Poller` (latest `Snapshot` of a Backend)
- `internal/scan/` — `Runner` (walk + extract + version check, configured by `RunnerOptions`, which takes `OnRepo(repo, results, err)` called once per repository as it finishes) and the `Result`/`Status`/`RepoError` types. It no longer serves HTTP, stores results or knows about versions of anything
- `internal/gitsync/` — the only code that runs `git` or touches a credential: `Syncer.Sync` (clone/fetch with the hardened environment, scoped auth header, ssh command) and `RedactURL`
- `internal/extractor/` — `Extractor` interface over `*File` (path, content, YAML documents decoded once and shared) returning `Ref`s + `HelmChart` (parses `Chart.yaml` deps; skips `file://` and `@alias` repos) + `FluxCD` (PrepareFile collects HelmRepository/OCIRepository, Extract resolves HelmRelease) + `Images` (container images in workload pod specs; skips templated refs) + `Dockerfile` (base images in `FROM` lines, final build stage flagged)
- `internal/registry/` — version resolution: HTTPS index.yaml fetch, OCI chart tag listing, or container image tags (OCI + vendor tag APIs); `scope.go` is the single home of version comparison (`ChartScope`, `ImageScope`, `IsNewerChart`); `vendor.go` one `dialect` per vendor tag API (Docker Hub, Quay, Harbor); `client.go` the fetch `limits` and the one HTTP client constructor; scope filtering (`patch`/`minor`/`all`, with `major` an alias of `all`); `IndexCache` lives for one scan (no TTL — every scan is its own process)
- `internal/errkind/` — the failure vocabulary (`auth`, `not_found`, `unreachable`, `timeout`, `rate_limited`, `server_error`, `host_key`, `invalid_version`, `too_large`, `unsupported`, `other`): `Wrap` where a failure is understood, `Of` anywhere up the chain. Surfaces as `check_error_kind`, `RepositoryScan.status.error.kind`, the `update_checker_dependency_check_failed_info{error_kind}` metric and the dashboard's labels and hints
- `internal/kube/` — `Connect() (Env, error)` (in-cluster config + namespace) and `Env.Leader(ctx, lease)` (Lease election; the returned closure says whether this pod leads)
- `internal/memlimit/` — sets the Go heap limit to 70% of the cgroup memory limit at startup (headroom for git subprocesses); an explicit `GOMEMLIMIT` wins
- `internal/metrics/` — `Handler(snapshot, leading)`: a `Collector` over a `report.Snapshot` and nothing else. Result metrics are exported only by the leader, so N replicas produce one alert per outdated dependency. Seven metrics: `update_checker_dependency_outdated_info` (presence per outdated reference), `update_checker_dependencies_scanned` (coverage, by source+type), `update_checker_dependency_digest_drift` (presence per moved digest), `update_checker_dependency_check_failed_info` (presence per failed check, with `error_kind`), `update_checker_dependency_skipped_tags` (rejected candidates per reference), plus `update_checker_last_scan_time` and `update_checker_repository_scan_failed{source,kind}` from the reports. The full label set is the series identity, so results agreeing on every label collapse — a duplicate would abort the whole scrape
- `internal/trigger/` — `Trigger` interface with two impls: `Kubernetes` (Job from the CronJob template; probes that it can read the CronJob at startup; caches `Running` for 5s) and `Process` (child `scan`, `NewProcess(ctx, args...)`)
- `internal/dashboard/` — `Handler{Snapshot func() report.Snapshot, Trigger trigger.Trigger}`: embedded UI, `GET /api/results`, `GET /api/status`, `POST /api/scan/trigger`, and the `Status` type the UI polls
- `internal/api/` — HTTP server bootstrap (`Run(ctx, routes)`), route registration, middleware chain, `/health` + `/ready` and the optional `/metrics` handler
- `internal/config/` — YAML config loader; repos, update scope, parallel checks, git cache dir, `scan_timeout`, what-to-scan switches (`check_images`, `check_dockerfiles`, `verify_image_tags`)
- `internal/middleware/` — shared HTTP middleware (recovery, logging, headers)
- `internal/version/` — `Version`, `Commit`, `BuildDate` vars injected via ldflags

**Storage:** the two custom resources of `updatechecker.yuriykovalchuk.dev/v1alpha1` — `RepositoryScan` (one per repository: phase, error, counts, last N runs) and `Dependency` (one per checked reference, named after its identity including the pinned version, plus `outdated_since` and `latest_seen_at`). No database, no per-run copies of items, and no TTL for registry fetches. Locally the same two kinds are JSON under `--data` (default `./data`, gitignored): `data/repositoryscans/*.json` and `data/dependencies/*.json`, one file per object, named exactly as the custom resource is.

**Data flow (dashboard mode):**

1. `report.NewPoller(backend)` re-reads the backend every 5s; `Snapshot()` feeds the UI, the API and the metrics collector
2. On manual trigger: `POST /api/scan/trigger` → `Trigger.Trigger()` → a Job copied from the scanner CronJob (kubernetes) or a child `scan` process (local)
3. Scanner (CronJob / Job / child process): `Runner.Run` with `OnRepo` → `report.Writer.Repo` per repository as it finishes. A repo whose scan failed keeps its previous `Dependency` objects and its `RepositoryScan` gets `phase: Failed` + the error kind; a scan that hit `scan_timeout` records every repository it did not reach as failed
4. Dashboard: `GET /api/results` + `GET /api/status` render the UI (also `GET /`, `GET /favicon.svg`, `GET /assets/`, `GET /fonts/`)
5. Metrics: `/metrics` is served by every replica but only the leader exports the result metrics (local: always leader)

## Design Decisions

- **Single binary, two commands, no HTTP between them** — previously three binaries (api/scanner/dashboard), then one binary with a `POST /api/scan/results` push path. Both are gone: the scanner writes reports and the dashboard reads them, so a scan does not depend on reaching a particular replica and the write path is ordinary Kubernetes RBAC.
- **Trigger abstraction** — `--cronjob` selects the Kubernetes trigger, which first checks it can read that CronJob; if it cannot, the trigger reports unavailable (the dashboard hides the button) and the log names the missing permission. There is no in-cluster fallback to an in-process scan: the dashboard is given no repository credentials, so an in-process scan would clone unauthenticated. Locally the trigger is always a child `scan` process.
- **Registry credentials** — `registry_auth_file` names a Docker config file (`.dockerconfigjson`); only its static `auths` entries are read (never `credHelpers`/`credsStore`, which would run a program), on every resolve so a rotated Secret applies without a restart. The chart mounts `scanner.registryAuth.existingSecret` into the scan pod only. Harbor's artifacts API gets the same robot account, and only for its own host. `imagePullSecrets` is for pulling the checker's own image.
- **Credentials are files, never Kubernetes objects, in Go** — `internal/config` reads `token_file` / `password_file` / `ssh_key_path` / `known_hosts_file` and knows nothing about Secrets. The chart (`templates/_auth-secrets.tpl`) is the only place that understands `auth.existingSecret`: it mounts that Secret read-only, one key per repo, at `/etc/update-checker-scanner/secrets/<repo name>/<key>` (mode 0440 — Secret volume files are root-owned, so a non-root pod needs `fsGroup`, which the chart requires when credentials exist) and rewrites the rendered ConfigMap to the matching `*_file`. Only the scan pod gets them — the CronJob and the Jobs copied from it are the only workloads that clone, so the dashboard mounts no Secret, no scanner config and no writable `/tmp`, and a missing credential Secret cannot stop it from starting. Inline `auth.token` / `auth.password`, credentials inside the repo URL, `token`/`basic` over a non-https URL, an ssh repo with no `scanner.ssh.knownHosts`, credentials with no `auth.type`, and two repo names that sanitize to one mount directory all fail at `helm template` — and the first three also fail in `config.Load`, since the binary is used without Helm too. The user creates the Secret; `values.yaml` has no field for a token. Guarded by `scripts/verify-chart-credentials.sh` (`make chart-creds`, also a CI step).
- **git subprocess baseline** — every git call lives in `internal/gitsync` and runs through `gitEnv`: `--` before the URL, `GIT_ALLOW_PROTOCOL` matching exactly the transports `config.Load` accepts (so `ext::` cannot run a command), `GIT_CONFIG_NOSYSTEM=1` (the image's `/etc/gitconfig` gets no vote), `transfer.credentialsInUrl=die`, `GIT_TERMINAL_PROMPT=0`. HTTP credentials go out as `http.<repo URL>.extraheader` — scoped to that URL, never global — and `TestCredentialHeaderStaysWithItsHost` proves with a real redirect that the target host never sees the header. Repo URLs are logged through `gitsync.RedactURL`. A configured `known_hosts_file` that cannot be read fails the clone instead of falling back to trust-on-first-use.
- **Two ServiceAccounts, two privilege sets** — the dashboard's can `create/get/list jobs`, `get cronjobs`, `get/list` the two report kinds and `create` Leases plus `get/update` on its own Lease; the scanner's (`<fullname>-scanner`, the token the scan pod needs to write CRs) can only `get/list/create/update/delete` the two report kinds. That split matters because `jobs: create` means "can read every Secret in the namespace" (a Job spec can mount any Secret), and the pod that holds the credentials is the one parsing hostile repository content: it must not be able to create a Job, read a ConfigMap or touch a Secret. The trigger only ever copies the CronJob's JobTemplate, so no caller-controlled pod spec is reachable through the dashboard's right.
- **Reports as resources, with bounded history** — one `RepositoryScan` per repository and one `Dependency` per reference; `Dependency` names hash the reference's identity (pinned version included), so a rescan updates rather than appends, and references that vanish are deleted. `RepositoryScan.status.history` keeps the last N runs (`scan --history`, default 10, `scanner.history.runs` in the chart) and nothing keeps a copy of every run's items — etcd is not a time-series database. Because the state is in the API, `replicaCount > 1` is allowed and restarting the dashboard loses nothing.
- **Graceful shutdown** — on SIGTERM the dashboard fails `/ready` (liveness `/health` stays up), waits 5s for the endpoints to drop the pod, then drains for up to 20s; the sum stays under the default 30s grace period.
- **No in-app authentication, no egress allowlist** — the binary trusts its network boundary. Anyone who can reach the Service can read results and start scans; the scan fetches whatever hosts the scanned repositories name. `NetworkPolicy`, ingress auth and egress control belong to the cluster operator, and the chart deliberately ships neither. Replacing the reports is not part of that surface — it takes Kubernetes API access, which RBAC controls. See `docs/architecture.md` → "Trust model & exposure".
- **Extractor prepare/extract for FluxCD** — a HelmRelease can name a source declared in another file, so every file goes through `PrepareFile` during the single walk (FluxCD collects sources), and `Extract` runs only after the walk, over the files some extractor matched. Each file is read once and its YAML parsed once (`extractor.File.Documents`). A source written without `metadata.namespace` (Kustomize sets it at apply time) matches any namespace a reference asks for, unless an exact one exists.
- **Repository walk filters by file name** — `extractor.IsScannable` (YAML plus Dockerfile/Containerfile names) decides what is opened at all; content-based `Match` runs only after a file is in memory. A new extractor whose files are not YAML must extend `IsScannable` or it will never see them. Only regular files are read (a committed symlink could point outside the checkout, or at `/dev/zero`), each capped at `RunnerOptions.MaxFileBytes` (10 MiB). `path:` must be relative (`config.Load`), must exist, and must not leave the checkout through a symlinked directory — otherwise the repo fails with `not_found`/`unsupported` instead of scanning nothing.
- **Leader-gated metrics** — every dashboard replica serves `/metrics`, but the result metrics come from a Lease (``--lease``, chart: `<fullname>-leader`) and only the leader exports them, so N replicas produce one alert per outdated dependency. A local dashboard has no Lease and always leads. Chart CRDs (`ServiceMonitor`, `PrometheusRule`) are opt-in; the stale-scan rules' threshold must stay larger than the scan interval, `UpdateCheckerNoSuccessfulScan` (an `absent()` rule) covers "never scanned since install", and `last_scan_time` now comes from the reports, so a dashboard restart no longer hides the age rule.
- **Memory** — the binary sets its own heap limit (`internal/memlimit`, 70% of the cgroup limit) because git children share the container's budget; the chart sets no `GOMEMLIMIT`. Helm indexes are capped at 20 MB and decoded one at a time (~15x in heap), which is why the scanner's default limit is 512Mi.
- **Chart defaults** — CronJob scans daily at 00:00 UTC (no `TZ` set on the pod); only the CronJob mounts the `scanner.config` ConfigMap, and both workloads are built from one container template (`templates/_workload.tpl`). The CRDs live in `crds/`, which Helm installs on install and **never upgrades** — a schema change is a manual `kubectl apply`. One release per namespace (report names come from the repository name); reports carry `app.kubernetes.io/instance`. One scan at a time: the scan holds the `<fullname>-scan` Lease (locally a flock on `<data>/.scan.lock`). Bounds: two repositories in parallel, at most 2,000 references per repository (`report.MaxDependencies`).

## Known Issues

Open (features, not defects):

- extraction misses `Chart.lock`, `sourceRef.kind: GitRepository|Bucket`, `HelmRelease.spec.values.image.*` and `dependencies[].alias`;
- `UpdateCheckerOutdatedDependency*` rules fire for as long as a dependency is outdated: there is no ack/silence concept;
- `/api/status` cannot say "the reports are not current" (planned with the scan-history feature).

Config loading is deliberately asymmetric: an unrecognised key warns with its exact
path (`repos[0].auth.existingSecret`) and still loads, because a config written for a newer image
must not break a CronJob; `update_type` and `auth.type` are hard errors, because a bad enum
silently changes what is reported or which credential is sent. Key sets come from the structs' own
yaml tags and `TestShippedConfigsHaveNoUnknownKeys` checks the shipped examples and `values.yaml`
against them.

Closed **by decision**, so do not "re-fix" them: `update_type: major` is an alias of `all` (pinned
by `TestScopeMajorBehavesAsScopeAll`); the commit convention is `docs/commits.md` with no scopes,
enforced identically by both hooks; API authentication and registry-egress control are
**cluster-operator responsibilities** (`docs/architecture.md` → "Trust model & exposure"); one
release per namespace; a failing repository keeps its last digest-drift alert until it scans again;
audit and review notes (`docs/audit-*.md`) are working files and are never committed.

## Engineering Rules

Each rule exists because breaking it caused a real bug or finding in this repo. Follow them in new code; when a rule and a
change disagree, change the rule here first and say why.

**Credentials and trust**

1. **Credentials are files.** Never pass a credential through an env var from a Secret, argv,
   a URL, a ConfigMap or a values field. The chart mounts the user's Secret per repo, read-only,
   `items:` limited to the one key, mode `0440` with `fsGroup` set (Secret files are root-owned).
   `values.yaml` never gets a field that holds a secret value.
2. **Only the pod that clones holds credentials, and it gets the narrowest identity.** A pod that
   parses repository content may hold rights over the app's own resources only — never
   `jobs: create`, because that means "can read every Secret in the namespace" (a Job spec can
   mount any Secret). The scanner's ServiceAccount token is mounted because writing reports is its job — which is exactly why its Role stops at the two report kinds; the dashboard's token is mounted only with `rbac.create`, since a token without its Role buys nothing.
3. **All git goes through `internal/gitsync`.** No `exec.Command("git", …)` anywhere else. Auth is
   `http.<repo URL>.extraheader` (scoped, never global), Authorization only over `https`, `--`
   before the URL, the `gitEnv` hardening baseline on every call.
4. **Repository content is untrusted input.** Read only regular files, cap their size, keep the
   scan root inside the checkout after resolving symlinks, and bound what it contributes to
   exported labels. (The hosts it names are fetched by design — egress control is the
   operator's, S2.)
5. **Security controls fail closed.** If a control the operator configured cannot be applied (an
   unreadable `known_hosts_file`, a missing key), return an error. Never fall back silently to the
   weaker behaviour.
6. **Nothing secret reaches a log, an error string or `/api/status`.** Log repo URLs through
   `gitsync.RedactURL`; never log a credential path or value.
7. **Every outbound request goes through `registry.policyTransport`** (via `newHTTPClient`, or
   `remoteOptions` for go-containerregistry, which only accepts a transport): User-Agent, at most
   5 redirects, no https→http downgrade.
8. **Supply chain:** keep `go.mod`'s `go` line and the Dockerfile builder image on the same patch
   release, run `govulncheck` clean before building an image, and pin the runtime image by
   digest (it supplies the `git` that reads every credential).

**Correctness**

9. **One source of truth per rule.** Version comparison and scope classification live only in
   `internal/registry/scope.go`; `scan` never parses a version. A second copy is how a date-tag
   bump got reported as "major".
10. **A partial failure must never look like "fixed".** A repository whose scan failed keeps its
    previous `Dependency` objects, and its `RepositoryScan` gets `phase: Failed` with the error
    kind; a scan that hit `scan_timeout` records every repository it did not reach the same way.
    `scan` exits non-zero, the dashboard shows it and `update_checker_repository_scan_failed` alerts.
    Dropping those results silently resolves their alerts.
11. **Untrusted text exported to Prometheus goes through `bound()`**, which makes it valid
    UTF-8 and truncates on a character boundary. One rejected series makes the whole `/metrics`
    return 500.
12. **Skip what no registry can answer, and say why. Never drop what could be answered.**
    `file://`, `@alias` and templated refs (`${…}`, `{{ }}`, `$(…)`) are skipped with a comment.
    A resolvable reference must not disappear because of a lookup quirk (the Flux namespace case).
13. **Errors carry a kind.** Classify a failure where it is understood (`errkind.Wrap` with the HTTP
    status, git's output, the size cap) and read it with `errkind.Of`. Keep the message as the
    detail; never make the UI, a metric or an alert parse an error string. A new kind needs an
    entry in `app.js` `ERROR_KINDS` (`TestDashboardKnowsEveryErrorKind` enforces it).
14. **Validate API input at the boundary.** No endpoint reads a request body today; when one
    starts to, it sets its own `http.MaxBytesReader` limit (a server-wide one can only lower it)
    and answers 413 when exceeded. Enumerations are typed constants shared across packages and
    anything else gets a 400 — nothing stringly-typed crosses package boundaries. Every listener
    keeps `ReadHeader`/`Read`/`Write`/`Idle` timeouts (`api.newServer`).
15. **Don't tie a feature to an unrelated switch.** Both pods always mount their API token (the Role
    is what limits it; `rbac.create` only decides who creates the Role); leader election is its own
    Lease and gates only the result metrics.
16. **Every bug fix ships with a regression test that was revert-checked**: it fails with the fix
    removed.

**Design**

17. **Constructor injection; options structs for wide constructors.** Use a named options struct
    once a constructor takes more than ~4 parameters, or two of the same type. No setters on
    structs or public interfaces — wiring happens in `cmd/update-checker`.
18. **No mutable package-level state for tests.** Limits, clients, keychains and vendor tables are
    struct fields set by options. Test-only options live in `_test.go` files.
19. **Keep functions small.** In production code, `gocognit` ≤ 30 and no `nestif` findings
    (`golangci-lint run --default=none --enable=gocognit,nestif --tests=false ./...`; large
    table-driven tests are exempt). Split by step (walk →
    extract → check), not by arbitrary line count.
20. **Read and parse once.** One walk per repo; each file's YAML is decoded once
    (`extractor.File.Documents`) and released after extraction.
21. **Per-vendor or per-kind behaviour is an interface, not a repeated `switch`** (see
    `registry.dialect`).
22. **Less is more.** Ship the smallest thing that satisfies the requirement, and delete what a
    change makes unused — no features, knobs or history kept "in case", no second way to do the
    same job.
23. **A comment is one line.** Go, templates and YAML alike: keep the first sentence or rewrite it
    shorter. The reasoning that does not fit lives in `docs/architecture.md`.
24. **Names match the domain.** `Ref` (not `ChartRef`) because images are refs too; `Writer`,
    `Poller` and `Backend` (not `Store`) because the reports, not a store, are the contract, and
    "repository" already means a git repo here. Renames stop at the wire: JSON fields and metric
    labels are API and stay.

**Chart**

25. **The chart's rules mirror the Go rules exactly.** `credDir` uses `config.SafeName`'s
    character class, and every check `config.Load` makes is also a render-time `fail`. A
    divergence means one side accepts what the other rejects.
26. **One container template** (`_workload.tpl`) for both workloads, with one `scans` flag deciding
    the mounts: only the scanning container gets the config, `/tmp` and the credential Secrets.
    Compute `repoCreds` once per template and pass it down. A comment inside a `range` is a
    template comment, so it is not repeated in the rendered YAML.
27. **Memory settings must agree.** The heap limit comes from `internal/memlimit` (70% of the
    cgroup limit); never set `GOMEMLIMIT` to the full container limit. Resource limits must cover
    the fetch caps (`limits.indexBytes` × ~15 × `maxConcurrentIndexFetches`).
28. **Every chart behaviour has a line in `scripts/verify-chart-credentials.sh`** — including the
    two RBAC identities, the CRDs and the dashboard's "mounts nothing" property.

**CI and dependencies**

29. **Pin everything CI executes.** Actions by full commit SHA with the version in a comment
    (`uses: actions/checkout@<sha> # v6.1.0`), tools by exact version (`govulncheck@v1.9.0`,
    golangci-lint, Helm via `setup-helm` `version:`). A tag or `@latest` can be re-pointed, and the
    publishing jobs hold `packages: write`.
30. **Dependabot proposes the bumps** (`.github/dependabot.yml`: Go modules, Actions, Docker). Its
    commit prefixes are `build`/`ci` with no scope, as `docs/commits.md` requires. When it bumps
    the Dockerfile's Go builder, bump `go.mod`'s `go` line in the same PR (rule 8).

**Before you call a change done:** `make test lint chart-creds`, `govulncheck ./...`, `helm lint`, the complexity
check from rule 19, `make kind VALUES=examples/test-values.yaml` when anything storage- or
RBAC-shaped changed, and update this file and `docs/architecture.md` when
behaviour changed.

## Development Commands

```bash
make build        # compile → bin/update-checker
make run          # build + local dashboard: examples/config.yaml, reports in ./data, child scans
make kind         # kind cluster + local image + chart (KIND_CLUSTER=, VALUES=)
make test         # go test -race -timeout 120s ./...
make chart-creds  # verify the chart's credentials, RBAC and report wiring (also a CI step)
make lint         # golangci-lint run ./...
make docker-build # multi-arch image build (no push)
make docker-push  # build + push (IMAGE=<registry>/<repo> for another registry, e.g. Harbor)
make install-hooks # configure .githooks/
```

## Key Patterns

- Module: `github.com/yuriy-kovalchuk/yk-update-checker`
- Image: `ghcr.io/yuriy-kovalchuk/yk-update-checker`
- One direction of data flow: `scan.Runner` → `report.Writer` → `report.Backend` → `report.Poller` → `dashboard.Handler` / `metrics.Collector`. Nobody calls anybody's HTTP.
- Constructor injection everywhere; the seams are interfaces the caller implements (`report.Backend`, `trigger.Trigger`, `api.Routes`, `scan.RunnerOptions.OnRepo`). No setters.
- `context.Context` first param on all I/O functions
- `log/slog` with `slog.NewTextHandler(os.Stderr, ...)`, structured fields only
- Config via YAML file + flags; no environment variables are read for configuration at all (credentials reach the process as inline values or file paths in the YAML). Environment is read only for detection: `GOMEMLIMIT` (an explicit heap limit wins over `memlimit`) and, through `rest.InClusterConfig`, `KUBERNETES_SERVICE_HOST`. `config.Load` parses the file once and validates everything, durations included, in both commands
- **Scan deadlines nest**: `scan_timeout` (default 30m) bounds `Runner.Run`, each `git` subprocess has its own limit (`RunnerOptions.GitCommandTimeout`, default `gitsync.DefaultCommandTimeout` 5m, negative disables), and the chart's CronJob adds `activeDeadlineSeconds`/`startingDeadlineSeconds` for the case the Go deadline cannot cover. A run that hits a deadline returns an error naming the knob — never a truncated "success"
- `CGO_ENABLED=0` always; runtime image is `alpine/git` (git is needed at runtime) running as nonroot `65534`
