#!/usr/bin/env python3
"""Wrapped httpx client for the kube-state-graph API server.

Exposes `KubeStateGraphClient`, a thin Pydantic-typed wrapper around
`httpx.Client` covering `/v1/graph` (Cytoscape), `/v1/graph/nodegraph`
(Grafana Node Graph), `/v1/clusters`, `/v1/edge-types`, and the health
probes. Supports `X-API-Key` auth, `If-None-Match` revalidation, and
context-manager use.

Run as a script:
    uv run --with httpx --with pydantic --with rich examples/python/get_graph.py \
        --base-url http://localhost:8080 \
        --start 2026-05-01T12:00:00Z \
        --end   2026-05-01T12:05:00Z

Or with pip:
    pip install 'httpx>=0.27' 'pydantic>=2.7' 'rich>=13'
    python examples/python/get_graph.py

As a library:
    from get_graph import KubeStateGraphClient
    with KubeStateGraphClient("http://localhost:8080", api_key="...") as c:
        clusters = c.list_clusters()
        g, etag = c.get_graph(start=..., end=..., cluster=["prod-eu"])
"""

from __future__ import annotations

import argparse
import os
import sys
from datetime import datetime, timedelta, timezone
from typing import Literal

import httpx
from pydantic import BaseModel, ConfigDict, Field, TypeAdapter
from rich.console import Console
from rich.table import Table

# Body shape (v1): { apiVersion, clusters, elements: { nodes, edges } }.
# `start` / `end` are query parameters only; the response does NOT echo them
# (no `start_actual` / `end_actual` / `bucket_seconds` / `built_at`). The body
# is minimal so identical inputs against the same upstream state produce a
# byte-identical body and ETag.


NodeType = Literal["pod", "node", "pvc", "external"]
EdgeType = Literal["pod-calls-pod", "pod-runs-on-node", "pod-mounts-pvc"]


class _Frozen(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid")


class NodeData(_Frozen):
    id: str
    name: str
    type: NodeType
    labels: dict[str, str] = Field(default_factory=dict)


class EdgeData(_Frozen):
    id: str
    type: EdgeType
    source: str
    target: str
    labels: dict[str, str] = Field(default_factory=dict)


class CyNode(_Frozen):
    data: NodeData


class CyEdge(_Frozen):
    data: EdgeData


class Elements(_Frozen):
    nodes: list[CyNode] = Field(default_factory=list)
    edges: list[CyEdge] = Field(default_factory=list)


class GraphResponse(_Frozen):
    """Top-level Cytoscape envelope returned by /v1/graph."""

    api_version: str = Field(alias="apiVersion")
    clusters: list[str]
    elements: Elements

    model_config = ConfigDict(frozen=True, extra="forbid", populate_by_name=True)

    # ---- ergonomic helpers ----------------------------------------------------

    @property
    def nodes(self) -> list[NodeData]:
        return [n.data for n in self.elements.nodes]

    @property
    def edges(self) -> list[EdgeData]:
        return [e.data for e in self.elements.edges]

    def nodes_by_id(self) -> dict[str, NodeData]:
        return {n.id: n for n in self.nodes}

    def cross_cluster_edges(self) -> list[EdgeData]:
        idx = self.nodes_by_id()
        out: list[EdgeData] = []
        for e in self.edges:
            if e.type != "pod-calls-pod":
                continue
            src = idx.get(e.source)
            tgt = idx.get(e.target)
            if not src or not tgt:
                continue
            sc = src.labels.get("cluster")
            tc = tgt.labels.get("cluster")
            if sc and tc and sc != tc:
                out.append(e)
        return out


class ClusterInfo(_Frozen):
    name: str


class ClustersResponse(_Frozen):
    api_version: str = Field(alias="apiVersion")
    clusters: list[ClusterInfo]
    model_config = ConfigDict(frozen=True, extra="forbid", populate_by_name=True)


class EdgeTypeLabel(_Frozen):
    name: str
    description: str | None = None
    value_type: str | None = None


class EdgeTypeDefinition(_Frozen):
    type: str
    description: str | None = None
    directed: bool
    source_type: list[NodeType]
    target_type: list[NodeType]
    may_cross_cluster: bool
    labels: list[EdgeTypeLabel] = Field(default_factory=list)


class EdgeTypesResponse(_Frozen):
    api_version: str = Field(alias="apiVersion")
    edge_types: list[EdgeTypeDefinition]
    model_config = ConfigDict(frozen=True, extra="forbid", populate_by_name=True)


# Re-usable adapters (faster than re-parsing a model on every call).
_GraphAdapter: TypeAdapter[GraphResponse] = TypeAdapter(GraphResponse)
_ClustersAdapter: TypeAdapter[ClustersResponse] = TypeAdapter(ClustersResponse)
_EdgeTypesAdapter: TypeAdapter[EdgeTypesResponse] = TypeAdapter(EdgeTypesResponse)


Direction = Literal["in", "out", "both"]


# ----- Client ---------------------------------------------------------------


class KubeStateGraphError(RuntimeError):
    """Raised on non-2xx / non-304 responses."""

    def __init__(self, status: int, reason: str, body: str):
        super().__init__(f"HTTP {status} {reason}: {body[:200]}")
        self.status = status
        self.reason = reason
        self.body = body


class KubeStateGraphClient:
    """Thin httpx wrapper around the kube-state-graph HTTP API.

    Lifecycle: call `.close()` or use as a context manager.
    Threading: backed by a single `httpx.Client`; share across threads only
    if you'd share a plain httpx.Client (httpx itself is thread-safe for
    simple GETs).
    """

    USER_AGENT = "kube-state-graph-py/1"

    def __init__(
        self,
        base_url: str,
        *,
        api_key: str | None = None,
        timeout: float = 30.0,
        verify: bool | str = True,
        client: httpx.Client | None = None,
    ):
        self._base_url = base_url.rstrip("/")
        self._api_key = api_key
        self._owns_client = client is None
        if client is None:
            client = httpx.Client(
                base_url=self._base_url,
                timeout=timeout,
                verify=verify,
                headers={"Accept": "application/json", "User-Agent": self.USER_AGENT},
            )
        self._client = client

    # ---- context manager / lifecycle -----------------------------------------

    def __enter__(self) -> "KubeStateGraphClient":
        return self

    def __exit__(self, *_exc) -> None:
        self.close()

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    # ---- low-level GET --------------------------------------------------------

    def _get(
        self,
        path: str,
        *,
        params: list[tuple[str, str]] | None = None,
        if_none_match: str | None = None,
    ) -> httpx.Response:
        headers: dict[str, str] = {}
        if self._api_key:
            headers["X-API-Key"] = self._api_key
        if if_none_match:
            headers["If-None-Match"] = if_none_match
        resp = self._client.get(path, params=params, headers=headers)
        if resp.status_code in (200, 304):
            return resp
        # Try to surface the structured `reason` field if present.
        reason = ""
        try:
            payload = resp.json()
            if isinstance(payload, dict):
                reason = str(payload.get("reason", ""))
        except ValueError:
            pass
        raise KubeStateGraphError(resp.status_code, reason, resp.text)

    # ---- /v1/graph ------------------------------------------------------------

    def get_graph(
        self,
        *,
        start: datetime,
        end: datetime,
        cluster: list[str] | None = None,
        namespace: list[str] | None = None,
        edge_type: list[EdgeType] | None = None,
        pod: list[str] | None = None,
        root: str | None = None,
        depth: int | None = None,
        direction: Direction | None = None,
        if_none_match: str | None = None,
    ) -> tuple[GraphResponse | None, str | None]:
        """GET /v1/graph (Cytoscape).

        Returns (graph, etag). On 304 Not Modified, graph is None and the
        caller's cached body matches the returned etag.
        """
        params = self._graph_params(
            start, end, cluster, namespace, edge_type, pod, root, depth, direction
        )
        resp = self._get("/v1/graph", params=params, if_none_match=if_none_match)
        etag = resp.headers.get("etag")
        if resp.status_code == 304:
            return None, etag
        return _GraphAdapter.validate_json(resp.content), etag

    def get_node_graph(
        self,
        *,
        start: datetime,
        end: datetime,
        cluster: list[str] | None = None,
        namespace: list[str] | None = None,
        edge_type: list[EdgeType] | None = None,
        pod: list[str] | None = None,
        root: str | None = None,
        depth: int | None = None,
        direction: Direction | None = None,
        if_none_match: str | None = None,
    ) -> tuple[dict | None, str | None]:
        """GET /v1/graph/nodegraph (Grafana Node Graph datasource shape).

        Returned as raw dict — the Grafana shape (`nodes_fields`, `nodes`,
        `edges_fields`, `edges`) is not modelled in this client.
        """
        params = self._graph_params(
            start, end, cluster, namespace, edge_type, pod, root, depth, direction
        )
        resp = self._get("/v1/graph/nodegraph", params=params, if_none_match=if_none_match)
        etag = resp.headers.get("etag")
        if resp.status_code == 304:
            return None, etag
        return resp.json(), etag

    @staticmethod
    def _graph_params(
        start: datetime,
        end: datetime,
        cluster: list[str] | None,
        namespace: list[str] | None,
        edge_type: list[EdgeType] | None,
        pod: list[str] | None,
        root: str | None,
        depth: int | None,
        direction: Direction | None,
    ) -> list[tuple[str, str]]:
        params: list[tuple[str, str]] = [
            ("start", start.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")),
            ("end", end.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")),
        ]
        for c in cluster or []:
            params.append(("cluster", c))
        for n in namespace or []:
            params.append(("namespace", n))
        for t in edge_type or []:
            params.append(("edge_type", t))
        for p in pod or []:
            params.append(("pod", p))
        if root:
            params.append(("root", root))
        if depth is not None:
            params.append(("depth", str(depth)))
        if direction:
            params.append(("direction", direction))
        return params

    # ---- /v1/clusters, /v1/edge-types ----------------------------------------

    def list_clusters(self, *, if_none_match: str | None = None) -> tuple[ClustersResponse | None, str | None]:
        resp = self._get("/v1/clusters", if_none_match=if_none_match)
        etag = resp.headers.get("etag")
        if resp.status_code == 304:
            return None, etag
        return _ClustersAdapter.validate_json(resp.content), etag

    def list_edge_types(self, *, if_none_match: str | None = None) -> tuple[EdgeTypesResponse | None, str | None]:
        resp = self._get("/v1/edge-types", if_none_match=if_none_match)
        etag = resp.headers.get("etag")
        if resp.status_code == 304:
            return None, etag
        return _EdgeTypesAdapter.validate_json(resp.content), etag

    # ---- health probes --------------------------------------------------------

    def livez(self) -> bool:
        try:
            self._get("/livez")
            return True
        except KubeStateGraphError:
            return False

    def readyz(self) -> bool:
        try:
            self._get("/readyz")
            return True
        except KubeStateGraphError:
            return False


def render(g: GraphResponse, console: Console) -> None:
    console.rule(f"[bold cyan]kube-state-graph {g.api_version}")
    console.print(f"clusters: [yellow]{g.clusters}[/]  nodes={len(g.nodes)}  edges={len(g.edges)}")

    nt = Table(title="nodes", show_lines=False)
    for col in ("id", "name", "type", "labels"):
        nt.add_column(col, overflow="fold")
    for n in g.nodes:
        nt.add_row(n.id, n.name, n.type, ", ".join(f"{k}={v}" for k, v in n.labels.items()))
    console.print(nt)

    et = Table(title="edges", show_lines=False)
    for col in ("type", "source", "target", "labels"):
        et.add_column(col, overflow="fold")
    for e in g.edges:
        et.add_row(e.type, e.source, e.target, ", ".join(f"{k}={v}" for k, v in e.labels.items()))
    console.print(et)

    xc = g.cross_cluster_edges()
    if xc:
        console.print(f"[bold magenta]cross-cluster edges:[/] {len(xc)}")


def parse_args() -> argparse.Namespace:
    now = datetime.now(timezone.utc).replace(microsecond=0)
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--base-url", default=os.getenv("KSG_BASE_URL", "http://localhost:8080"))
    p.add_argument("--api-key", default=os.getenv("KSG_API_KEY"))
    p.add_argument("--start", default=(now - timedelta(minutes=5)).strftime("%Y-%m-%dT%H:%M:%SZ"))
    p.add_argument("--end", default=now.strftime("%Y-%m-%dT%H:%M:%SZ"))
    p.add_argument("--cluster", action="append", default=[])
    p.add_argument("--namespace", action="append", default=[])
    p.add_argument("--edge-type", action="append", default=[], choices=list(EdgeType.__args__))
    p.add_argument("--pod", action="append", default=[])
    p.add_argument("--root", default=None, help="cluster-scoped node id anchoring traversal")
    p.add_argument("--depth", type=int, default=None, help="BFS traversal depth 0..6")
    p.add_argument("--direction", choices=list(Direction.__args__), default=None)
    p.add_argument("--if-none-match", default=None, help="ETag value for conditional GET")
    p.add_argument("--json", action="store_true", help="dump validated model as JSON")
    return p.parse_args()


def main() -> int:
    args = parse_args()
    console = Console()
    try:
        with KubeStateGraphClient(args.base_url, api_key=args.api_key) as client:
            g, etag = client.get_graph(
                start=datetime.fromisoformat(args.start.replace("Z", "+00:00")),
                end=datetime.fromisoformat(args.end.replace("Z", "+00:00")),
                cluster=args.cluster or None,
                namespace=args.namespace or None,
                edge_type=args.edge_type or None,
                pod=args.pod or None,
                root=args.root,
                depth=args.depth,
                direction=args.direction,
                if_none_match=args.if_none_match,
            )
    except KubeStateGraphError as e:
        console.print(f"[red]HTTP {e.status}[/] reason={e.reason!r}: {e.body[:300]}")
        return 1
    except httpx.HTTPError as e:
        console.print(f"[red]request failed[/]: {e}")
        return 1

    if g is None:
        console.print(f"[green]304 Not Modified[/] etag={etag}")
        return 0

    if args.json:
        sys.stdout.write(g.model_dump_json(by_alias=True, indent=2))
        sys.stdout.write("\n")
    else:
        render(g, console)
        if etag:
            console.print(f"[dim]etag: {etag}[/]")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
