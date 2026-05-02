.PHONY: build test vet lint cover kind-up kind-down smoke fixtures clean

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
	go test ./...

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run

cover:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -1

kind-up:
	./deploy/kind/bootstrap.sh

kind-down:
	./deploy/kind/teardown.sh

smoke:
	./tests/smoke/run.sh

clean:
	rm -rf $(BIN_DIR) coverage.out
