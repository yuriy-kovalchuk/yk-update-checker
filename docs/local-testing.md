# Local Testing

## Prerequisites

- Go 1.26.2+
- `git` binary on `$PATH`
- `golangci-lint` v2.x (`brew install golangci-lint`)

## Quick start

```bash
cp examples/config.example.yaml examples/config.yaml
# edit examples/config.yaml — add at least one repo
make run
```

`make run` builds the binary and starts `serve` mode. The UI is at [http://localhost:8080](http://localhost:8080).

## Config

`examples/config.yaml` is gitignored. Minimum config:

```yaml
update_type: all        # all | major | minor | patch
parallel_checks: 5

repos:
  - name: my-gitops
    repo: https://github.com/example/my-gitops-repo
    path: kubernetes/apps   # optional sub-path
```

For private repos see `examples/config.example.yaml` — it covers token, basic, and SSH auth.

## Running modes

### Serve mode (API + UI + optional scheduler)

```bash
make run
# or with options:
./bin/update-checker serve \
  --config examples/config.yaml \
  --port 8080 \
  --interval 6h \
  --verbose
```

The UI polls `/api/results` and `/api/status`. A manual scan button calls `POST /api/scan/trigger` which runs a scan in-process (inline trigger) when there is no `--cronjob` flag.

### One-shot scan (print to stdout)

```bash
make run-scan
# or:
./bin/update-checker scan --config examples/config.yaml --verbose
```

### One-shot scan (post to a running serve instance)

```bash
./bin/update-checker scan \
  --config examples/config.yaml \
  --server-url http://localhost:8080
```

## Make targets

```bash
make build          # compile → bin/update-checker
make run            # build + serve (uses examples/config.yaml)
make run-scan       # build + one-shot scan, print JSON to stdout
make test           # go test -race -timeout 120s ./...
make test-cover     # coverage report → coverage.html
make lint           # golangci-lint run ./...
make fmt            # go fmt ./...
make vet            # go vet ./...
make tidy           # go mod tidy + verify
make deps-check     # list outdated direct dependencies
make vuln           # govulncheck ./...
make clean          # remove bin/, coverage.out, coverage.html
make install-hooks  # register .githooks/ with git
make help           # list all targets
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
