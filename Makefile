.PHONY: build test vet lint vuln cover docs check-docs refresh-docs-ui kind-up kind-down kind-redeploy kind-restart local-up local-down local-redeploy local-restart local-smoke smoke clean \
        docker-build docker-push docker-buildx docker-load-kind docker-run docker-docs docker-docs-stop \
        init init-go init-tools init-hooks doctor mocks verify-mocks tools-versions \
        helm-lint helm-template helm-install-kind helm-uninstall-kind

BIN_DIR := bin
BIN     := $(BIN_DIR)/kube-state-graph
PKG     := github.com/marz32one/kube-state-graph
LDFLAGS := -s -w

# Container image settings. Override on the command line, e.g.:
#   make docker-push REGISTRY=docker.io IMAGE_REPO=marz32one/kube-state-graph VERSION=v0.2.0
REGISTRY      ?= docker.io
IMAGE_REPO    ?= marz32one/kube-state-graph
IMAGE         ?= $(REGISTRY)/$(IMAGE_REPO)
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
DOCKERFILE    := deploy/docker/server.Dockerfile
PLATFORMS     ?= linux/amd64,linux/arm64
LOCAL_TAG     := localhost/kube-state-graph/server:dev
DOCKER_BUILD_ARGS := \
    --build-arg VERSION=$(VERSION) \
    --build-arg COMMIT=$(COMMIT) \
    --build-arg BUILD_DATE=$(BUILD_DATE)

## ---------------------------------------------------------------------------
## Local-dev bootstrap.
##
##   make init          # one-shot: go mod download + dev tools + (optional) hooks
##   make init-go       # only: go mod download / tidy verify
##   make init-tools    # only: install host-level dev binaries (golangci-lint, govulncheck)
##   make init-hooks    # optional: install pre-commit hook (gofmt + vet + lint)
##   make doctor        # report toolchain versions & missing pieces
##
## Go-based tools tracked via go.mod `tool` directive (Go 1.24+) are invoked
## with `go tool <name>` and need no install step. They are excluded from the
## production binary by the Go toolchain.
##
## Host-level tools (golangci-lint, govulncheck) live in $(GOBIN). They are
## NOT pulled into go.mod and NOT required by CI consumers of the module.
GOBIN ?= $(shell go env GOPATH)/bin
GOLANGCI_LINT_VERSION ?= v2.11.4
GOVULNCHECK_VERSION   ?= latest

init: init-go init-tools
	@echo ""
	@echo "Local dev environment ready."
	@echo "  make doctor         # verify toolchain"
	@echo "  make init-hooks     # (optional) install pre-commit hook"

init-go:
	@echo ">> go mod download"
	go mod download
	@echo ">> verifying go.mod is tidy (read-only check)"
	@cp go.mod go.mod.bak && cp go.sum go.sum.bak; \
	    go mod tidy >/dev/null 2>&1; \
	    if ! diff -q go.mod go.mod.bak >/dev/null || ! diff -q go.sum go.sum.bak >/dev/null; then \
	        mv go.mod.bak go.mod && mv go.sum.bak go.sum; \
	        echo "WARN: go.mod / go.sum not tidy. Run 'go mod tidy' and commit."; \
	    else \
	        rm -f go.mod.bak go.sum.bak; \
	        echo "  ok"; \
	    fi

## Install host-level dev binaries into $(GOBIN). Skipped if already present
## at the pinned version. Safe to re-run.
init-tools:
	@echo ">> ensure $(GOBIN) is on PATH"
	@case ":$$PATH:" in *":$(GOBIN):"*) ;; *) echo "  WARN: $(GOBIN) not on PATH. Add: export PATH=\"$(GOBIN):\$$PATH\"";; esac
	@echo ">> golangci-lint $(GOLANGCI_LINT_VERSION)"
	@if ! command -v golangci-lint >/dev/null 2>&1 || ! golangci-lint --version 2>/dev/null | grep -q "$$(echo $(GOLANGCI_LINT_VERSION) | sed 's/^v//')"; then \
	    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
	        | sh -s -- -b $(GOBIN) $(GOLANGCI_LINT_VERSION); \
	else \
	    echo "  already installed: $$(golangci-lint --version)"; \
	fi
	@echo ">> govulncheck $(GOVULNCHECK_VERSION)"
	@if ! command -v govulncheck >/dev/null 2>&1; then \
	    GOBIN=$(GOBIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION); \
	else \
	    echo "  already installed at $$(command -v govulncheck)"; \
	fi
	@echo ">> mockery (via go tool — no install needed)"
	@go tool mockery --version 2>/dev/null | tail -1 | awk '{print "  " $$0}' || echo "  WARN: 'go tool mockery' failed; check go.mod tool directive."

## Install a project-local pre-commit hook running gofmt + vet + lint on the
## staged Go files. Idempotent: overwrites existing hook with our managed one.
init-hooks:
	@if [ ! -d .git ]; then echo "not a git repo; skipping"; exit 0; fi
	@mkdir -p .git/hooks
	@printf '%s\n' \
	    '#!/usr/bin/env bash' \
	    'set -euo pipefail' \
	    'files=$$(git diff --cached --name-only --diff-filter=ACM | grep "\\.go$$" || true)' \
	    '[ -z "$$files" ] && exit 0' \
	    'unformatted=$$(gofmt -l $$files || true)' \
	    'if [ -n "$$unformatted" ]; then echo "gofmt: $$unformatted"; exit 1; fi' \
	    'go vet ./... || exit 1' \
	    > .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "installed .git/hooks/pre-commit (gofmt + vet)"

doctor:
	@echo "go         : $$(go version 2>/dev/null || echo MISSING)"
	@echo "GOBIN      : $(GOBIN)"
	@echo "golangci   : $$(golangci-lint --version 2>/dev/null | head -1 || echo MISSING)"
	@echo "govulncheck: $$(command -v govulncheck >/dev/null && echo present || echo MISSING)"
	@echo "swag (tool): $$(go tool swag -v 2>/dev/null | head -1 || echo MISSING)"
	@echo "mockery    : $$(go tool mockery --version 2>/dev/null | tail -1 || echo MISSING)"
	@echo "docker     : $$(docker --version 2>/dev/null || echo MISSING)"
	@echo "kind       : $$(kind --version 2>/dev/null || echo MISSING)"

tools-versions:
	@echo "Pinned dev tools (override via env):"
	@echo "  GOLANGCI_LINT_VERSION = $(GOLANGCI_LINT_VERSION)"
	@echo "  GOVULNCHECK_VERSION   = $(GOVULNCHECK_VERSION)"

## Generate testify-style mocks. Re-run whenever an interface in a configured
## package changes. Mocks are committed to git so CI does not need mockery.
mocks:
	@if [ ! -f .mockery.yaml ]; then \
	    echo "no .mockery.yaml; nothing to generate"; \
	else \
	    go tool mockery; \
	fi

## Verify committed mocks match the current interfaces. Used by CI.
verify-mocks: mocks
	@if ! git diff --quiet -- '**/mocks/' 2>/dev/null; then \
	    echo "FAIL: mocks out of sync. Run 'make mocks' and commit."; \
	    git --no-pager diff -- '**/mocks/'; \
	    exit 1; \
	fi
	@echo "mocks up-to-date"

## ---------------------------------------------------------------------------

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/kube-state-graph

test:
	go test ./... -count=1 -race -shuffle=on

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run --timeout=5m

vuln:
	@if command -v govulncheck >/dev/null 2>&1; then \
	    govulncheck ./...; \
	else \
	    go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...; \
	fi

cover:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -1

docs:
	go tool swag init -g cmd/kube-state-graph/main.go --output docs --parseDependency --parseInternal --v3.1=true
	go run ./tools/openapi-postprocess docs/swagger.json docs/swagger.yaml
	@cp docs/swagger.yaml internal/api/static/openapi/openapi.yaml
	@cp docs/swagger.json internal/api/static/openapi/openapi.json

check-docs: docs
	@if ! git diff --quiet -- docs/ internal/api/static/openapi/; then \
		echo "FAIL: docs are out of sync. Run 'make docs' and commit."; \
		git --no-pager diff -- docs/ internal/api/static/openapi/; \
		exit 1; \
	fi

refresh-docs-ui:
	./scripts/refresh-docs-ui.sh

## Local kind rig (NOT run by CI; see local/kind/).

kind-up local-up:
	./local/kind/bootstrap.sh

kind-down local-down:
	./local/kind/teardown.sh

## Re-apply manifests + rebuild image + bounce pods on the existing Kind
## cluster. Skip cluster create/destroy. Use after editing Go code, manifest
## YAML, ConfigMaps, Grafana datasources/dashboards, or Alloy/Tempo config.
kind-redeploy local-redeploy:
	./local/kind/redeploy.sh

## Bounce kube-state-graph + observability pods only (no rebuild, no
## manifest re-apply). Use after a ConfigMap edit when you only need pods
## to pick up fresh config.
kind-restart local-restart:
	kubectl -n kube-state-graph rollout restart \
		deploy/kube-state-graph deploy/grafana deploy/alloy deploy/tempo deploy/victoria-metrics
	kubectl -n kube-state-graph rollout status \
		deploy/kube-state-graph deploy/grafana deploy/alloy deploy/tempo deploy/victoria-metrics --timeout=120s

smoke local-smoke:
	./local/kind/smoke.sh

## ---------------------------------------------------------------------------
## Helm chart (charts/kube-state-graph/).
##
## The chart is a deployable artefact; tests do not depend on it.
## CI should run `make helm-lint` (and ideally `helm-template`) on changes
## under charts/.

CHART_DIR := charts/kube-state-graph

helm-lint:
	helm lint $(CHART_DIR)
	helm lint $(CHART_DIR) --values local/kind/values-kind.yaml

## Render the chart to stdout for both default and kind overlays. Useful
## as a smoke test before committing template changes.
helm-template:
	@echo "==> default values"
	helm template kube-state-graph $(CHART_DIR) --namespace kube-state-graph \
		--set config.promURL=http://victoria-metrics:8428 >/dev/null
	@echo "==> local/kind overlay"
	helm template kube-state-graph $(CHART_DIR) --namespace kube-state-graph \
		--values local/kind/values-kind.yaml >/dev/null
	@echo "OK"

## Install / upgrade kube-state-graph into the existing kind cluster using
## the in-repo chart. Assumes `make kind-up` already created the supporting
## resources (namespace, Secret, VictoriaMetrics, Alloy, ...). Re-runnable.
helm-install-kind:
	./local/kind/helm-install.sh

helm-uninstall-kind:
	helm uninstall kube-state-graph --namespace kube-state-graph

clean:
	rm -rf $(BIN_DIR) coverage.out

## Container image build / push.
##
## Login to your registry first:  docker login $(REGISTRY)
## Single-arch local build (host arch). Tags both VERSION and the
## :dev tag the local kind rig pins via imagePullPolicy=Never.
docker-build:
	docker build $(DOCKER_BUILD_ARGS) \
		-f $(DOCKERFILE) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		-t $(LOCAL_TAG) \
		.

## Push the single-arch image built by docker-build to the registry.
## Run `docker login` (or `make docker-build && docker login`) first.
docker-push: docker-build
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

## Multi-arch build + push using buildx. Requires `docker buildx` and a
## builder that supports the target platforms (run once:
##   docker buildx create --name ksg --use --bootstrap).
docker-buildx:
	docker buildx build $(DOCKER_BUILD_ARGS) \
		--platform $(PLATFORMS) \
		-f $(DOCKERFILE) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		--push \
		.

## Load the local-built image into the kind cluster used by `make kind-up`.
## Useful when iterating on the API without rebuilding the whole rig.
docker-load-kind: docker-build
	kind load docker-image $(LOCAL_TAG) --name kube-state-graph

## Run the freshly built image locally against a Prom URL.
##   make docker-run PROM_URL=http://host.docker.internal:8428
PROM_URL ?= http://host.docker.internal:8428
docker-run: docker-build
	docker run --rm -p 8080:8080 $(LOCAL_TAG) \
		--prom-url=$(PROM_URL) --listen-addr=:8080

## Docker-only docs preview. Runs the API server in Docker with a placeholder
## upstream so the static OpenAPI / Scalar UI routes are reachable. /v1/* and
## /readyz will return upstream errors (no VictoriaMetrics is started); only the
## docs surfaces are intended for verification here.
##
##   make docker-docs            # build + run, foreground (Ctrl-C to stop)
##   make docker-docs DETACH=1   # run detached; stop with `make docker-docs-stop`
##
## Then visit:
##   http://localhost:8080/docs           — Scalar UI
##   http://localhost:8080/openapi.json   — OpenAPI JSON
##   http://localhost:8080/openapi.yaml   — OpenAPI YAML
DOCS_PORT ?= 8080
DOCS_NAME ?= kube-state-graph-docs
DETACH    ?=
docker-docs: docker-build
	@echo "Starting $(DOCS_NAME) on http://localhost:$(DOCS_PORT)/docs"
	@echo "  Scalar UI : http://localhost:$(DOCS_PORT)/docs"
	@echo "  OpenAPI   : http://localhost:$(DOCS_PORT)/openapi.json"
	@echo "              http://localhost:$(DOCS_PORT)/openapi.yaml"
	docker run --rm $(if $(DETACH),-d,) --name $(DOCS_NAME) \
		-p $(DOCS_PORT):8080 \
		$(LOCAL_TAG) \
		--prom-url=http://127.0.0.1:8428 \
		--listen-addr=:8080 \
		--log-level=info

docker-docs-stop:
	-docker rm -f $(DOCS_NAME) 2>/dev/null || true
