# Local testing

## Prerequisites

- Go 1.26+
- `git` binary on `$PATH`
- A `config.yaml` pointing at one or more repos (see `config.example.yaml`)
- `golangci-lint` for linting (`go install github.com/golangci/golangci-lint/cmd/golangci-lint@v2.11.4`)
- `jq` for `make deps-check`

## Running the components

The three binaries are independent processes. Run each in a separate terminal after `make build`.

```bash
# Terminal 1 — API server (SQLite at /tmp/update-checker.db)
make run-api

# Terminal 2 — Scanner (reads config.yaml, posts to localhost:8080)
make run-scanner

# Terminal 3 — Dashboard (UI at http://localhost:8081, proxies /api/* to API)
make run-dashboard
```

The dashboard is available at [http://localhost:8081](http://localhost:8081) once all three are running.

## Configuring repos

Copy `config.example.yaml` to `config.yaml` and add at least one repo:

```yaml
repos:
  - name: my-gitops
    repo: https://github.com/example/my-gitops-repo
    path: kubernetes/apps   # optional sub-path

updateType: all             # all | major | minor | patch
parallelChecks: 5
```

For private repos, use a token or SSH key — see `config.example.yaml` for all auth options.

## Useful make targets

```bash
make build          # compile all three binaries to bin/
make test           # run tests with race detector
make test-cover     # generate coverage.html
make lint           # run golangci-lint
make fmt            # format all Go source files
make vet            # run go vet
make tidy           # go mod tidy + verify
make deps-check     # list outdated direct dependencies (requires jq)
make clean          # remove bin/, coverage.out, coverage.html
make install-hooks  # register .githooks/ with git
make help           # list all targets with descriptions
```

## Git hooks

The repo ships a `pre-push` hook that:
- Enforces conventional commit messages on pushes to `master`
- Validates `v*` tag format (`v<major>.<minor>.<patch>[-prerelease]`)
- Runs the test suite before each push

Install once:

```bash
make install-hooks
```

## Running a subset of tests

```bash
go test -v -run TestExtractor ./internal/extractor/...
go test -v -run TestVersion   ./internal/version/...
```

## Linting

```bash
make lint
# or with auto-fix where possible:
golangci-lint run --fix ./...
```
