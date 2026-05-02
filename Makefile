.PHONY: build test vet lint vuln cover docs check-docs refresh-docs-ui kind-up kind-down local-up local-down local-smoke smoke fixtures clean

BIN_DIR := bin
BIN     := $(BIN_DIR)/kube-state-graph
PKG     := github.com/marz32one/kube-state-graph
LDFLAGS := -s -w

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/kube-state-graph

fixtures:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/vm-fixtures ./tests/harness/vm-fixtures

test:
	go test ./... -count=1 -race -shuffle=on

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run --timeout=5m

vuln:
	@command -v govulncheck >/dev/null 2>&1 || { echo "installing govulncheck..."; go install golang.org/x/vuln/cmd/govulncheck@latest; }
	govulncheck ./...

cover:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -1

docs:
	@command -v swag >/dev/null 2>&1 || { echo "installing swag v2..."; go install github.com/swaggo/swag/v2/cmd/swag@latest; }
	swag init -g cmd/kube-state-graph/main.go --output docs --parseDependency --parseInternal --v3.1=false

check-docs: docs
	@if ! git diff --quiet -- docs/; then \
		echo "FAIL: docs/ is out of sync. Run 'make docs' and commit."; \
		git --no-pager diff -- docs/; \
		exit 1; \
	fi

refresh-docs-ui:
	./scripts/refresh-docs-ui.sh

## Manual Grafana visual rig (NOT run by CI; see local/grafana/).

kind-up local-up:
	./local/grafana/bootstrap.sh

kind-down local-down:
	./local/grafana/teardown.sh

smoke local-smoke:
	./local/grafana/smoke.sh

clean:
	rm -rf $(BIN_DIR) coverage.out
