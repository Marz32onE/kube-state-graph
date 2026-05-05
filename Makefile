.PHONY: build test vet lint vuln cover docs check-docs refresh-docs-ui kind-up kind-down local-up local-down local-smoke smoke clean \
        docker-build docker-push docker-buildx docker-load-kind docker-run

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
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

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

smoke local-smoke:
	./local/kind/smoke.sh

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
