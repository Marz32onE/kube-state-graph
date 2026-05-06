#!/usr/bin/env bash
# Run kube-state-graph in Docker for OpenAPI / Scalar UI verification.
#
# Builds the server image (using deploy/docker/server.Dockerfile) and launches
# it on port 8080 with a placeholder upstream URL — the docs surfaces (`/docs`,
# `/openapi.json`, `/openapi.yaml`) do not require VictoriaMetrics and will
# render fine; `/v1/*` and `/readyz` will fail because no VM is reachable.
#
# Usage:
#   ./local/docker/docs.sh                    # build + run, foreground
#   DETACH=1 ./local/docker/docs.sh           # run detached
#   DOCS_PORT=9090 ./local/docker/docs.sh     # override host port
#   ./local/docker/docs.sh stop               # stop a detached container
#
# Equivalent Makefile targets: `make docker-docs`, `make docker-docs-stop`.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

IMAGE_TAG="${IMAGE_TAG:-localhost/kube-state-graph/server:dev}"
DOCKERFILE="${DOCKERFILE:-deploy/docker/server.Dockerfile}"
DOCS_NAME="${DOCS_NAME:-kube-state-graph-docs}"
DOCS_PORT="${DOCS_PORT:-8080}"
DETACH="${DETACH:-}"

cmd="${1:-run}"

case "$cmd" in
    stop)
        docker rm -f "$DOCS_NAME" >/dev/null 2>&1 || true
        echo "stopped $DOCS_NAME"
        exit 0
        ;;
    run)
        ;;
    *)
        echo "usage: $0 [run|stop]" >&2
        exit 2
        ;;
esac

echo ">> building image $IMAGE_TAG"
docker build \
    --build-arg "VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" \
    --build-arg "COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" \
    --build-arg "BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -f "$DOCKERFILE" \
    -t "$IMAGE_TAG" \
    .

# Replace any prior container with the same name.
docker rm -f "$DOCS_NAME" >/dev/null 2>&1 || true

echo
echo ">> starting $DOCS_NAME on http://localhost:$DOCS_PORT"
echo "   Scalar UI : http://localhost:$DOCS_PORT/docs"
echo "   OpenAPI   : http://localhost:$DOCS_PORT/openapi.json"
echo "               http://localhost:$DOCS_PORT/openapi.yaml"
echo "   livez     : http://localhost:$DOCS_PORT/livez"
echo

run_args=(--rm --name "$DOCS_NAME" -p "${DOCS_PORT}:8080")
if [[ -n "$DETACH" ]]; then
    run_args+=(-d)
fi

docker run "${run_args[@]}" "$IMAGE_TAG" \
    --prom-url=http://127.0.0.1:8428 \
    --listen-addr=:8080 \
    --enable-debug \
    --log-level=info

if [[ -n "$DETACH" ]]; then
    echo
    echo "container running detached. stop with: $0 stop"
fi
