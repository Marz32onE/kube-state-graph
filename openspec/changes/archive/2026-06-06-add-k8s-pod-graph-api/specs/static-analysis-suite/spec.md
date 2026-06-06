## ADDED Requirements

### Requirement: Curated golangci-lint configuration

The repository SHALL ship a `.golangci.yml` at its root enabling at minimum the following linters, grouped by intent:

- **Correctness**: `errcheck`, `gosimple`, `govet`, `ineffassign`, `staticcheck`, `unused`, `gocritic`, `exhaustive`.
- **Modern Go idioms**: `copyloopvar`, `intrange`, `revive`.
- **Error handling**: `errorlint`, `nilerr`.
- **Security**: `gosec`.
- **Complexity**: `gocyclo`, `gocognit`, `funlen`.
- **Performance**: `prealloc`, `bodyclose`, `unconvert`.
- **Style**: `misspell`, `gofmt`, `goimports`.
- **Dead code / duplication**: `dupl`, `unparam`.
- **Magic numbers**: `mnd`.
- **Testify usage**: `testifylint` (with `enable-all: true`).

The configuration SHALL apply to all Go source files in the repository, including `cmd/`, `internal/`, and `tests/`. Test files MAY relax `errcheck` and the strictest complexity / duplication rules.

#### Scenario: All linters enabled

- **WHEN** an operator runs `golangci-lint linters` against the repository
- **THEN** every linter named above appears in the "Enabled" list

#### Scenario: Test files relaxed

- **WHEN** a test file legitimately repeats table-driven structure
- **THEN** `golangci-lint run` does NOT flag it for `dupl` or `errcheck`

### Requirement: Complexity caps

The `.golangci.yml` SHALL enforce the following complexity caps:

- `gocyclo`: cyclomatic complexity ≤ 15 per function.
- `gocognit`: cognitive complexity ≤ 20 per function.
- `funlen`: ≤ 100 lines and ≤ 50 statements per function.

Functions exceeding these caps MUST be refactored or carry an explicit `//nolint:<linter>` comment with a one-line rationale.

#### Scenario: Function exceeds gocyclo cap

- **WHEN** a contributor introduces a function with cyclomatic complexity 16
- **THEN** `golangci-lint run` fails with a `gocyclo` finding pointing at that function

### Requirement: golangci-lint runs on every PR

The repository SHALL include a CI workflow that runs `golangci-lint` on every pull request via `golangci/golangci-lint-action@v8` (or newer-major) with `args: --timeout=5m`. The workflow SHALL gate merges on lint success.

#### Scenario: Lint job runs on PR

- **WHEN** a developer opens a pull request
- **THEN** the CI workflow's `lint` job runs and any failure marks the PR as failing required checks

### Requirement: govulncheck on every PR

The repository SHALL include a CI workflow step that runs `golang.org/x/vuln/cmd/govulncheck@latest ./...` on every pull request. Detected vulnerabilities SHALL gate merges; suppressions SHALL be made explicit via comment plus a tracking issue, never via silent ignoring.

#### Scenario: govulncheck flags a known CVE

- **WHEN** a vulnerable transitive dependency reachable from the binary is on the dependency graph
- **THEN** `govulncheck ./...` exits non-zero and the PR's `vuln` job fails

#### Scenario: Suppressions documented

- **WHEN** a contributor needs to suppress a finding (e.g., a vulnerability that does not affect the reachable code path)
- **THEN** the suppression appears as a code comment referencing a tracked issue, not as a removal of the `vuln` job

### Requirement: Lint, vuln, and test jobs run in parallel

The CI workflow SHALL define `lint`, `vuln`, and `test` as separate jobs without `needs` edges between them. PR feedback latency SHALL be the maximum of the three jobs, not the sum.

#### Scenario: Three jobs visible in PR checks

- **WHEN** a developer opens a pull request
- **THEN** the PR check list shows `lint`, `vuln`, and `test` as three independent required checks

### Requirement: Local equivalents

The repository's `Makefile` (or equivalent) SHALL provide developer-facing targets that run the same checks locally:

- `make lint` — runs `golangci-lint run`.
- `make vuln` — runs `govulncheck ./...`.
- `make test` — runs `go test ./...`.
- `make docs` — runs `swag init -g cmd/kube-state-graph/main.go --output docs --parseDependency --parseInternal`.
- `make check-docs` — runs `make docs`, then `git diff --exit-code docs/`. Fails when generated files would change.

Each target SHALL exit non-zero on any failure.

#### Scenario: Local lint matches CI

- **WHEN** a developer runs `make lint` after making a change
- **THEN** the linter set, complexity caps, and per-file exemptions are identical to those applied by the CI workflow's `lint` job

### Requirement: OpenAPI drift gate

The CI workflow SHALL include a job (or step) that runs `swag init` over the source tree and fails the build if the resulting `docs/swagger.json`, `docs/swagger.yaml`, or `docs/docs.go` differ from the versions checked into the repository. The same gate SHALL be reproducible locally via `make check-docs`.

#### Scenario: Annotation drift detected

- **WHEN** a contributor edits a handler's `// @Summary` comment without re-running `swag init`
- **THEN** the CI `docs` job runs `swag init`, observes a `git diff` in `docs/`, and exits non-zero

#### Scenario: Local check-docs reproduces CI gate

- **WHEN** a developer runs `make check-docs`
- **THEN** the command executes the same `swag init` invocation as the CI gate and exits with the same status
