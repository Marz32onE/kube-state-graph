#!/usr/bin/env python3
"""Fetch /v1/graph (Cytoscape format) and deserialise into Pydantic models.

Run:
    uv run --with httpx --with pydantic --with rich examples/python/get_graph.py \
        --base-url http://localhost:8080 \
        --start 2026-05-01T12:00:00Z \
        --end   2026-05-01T12:05:00Z

Or with pip:
    pip install 'httpx>=0.27' 'pydantic>=2.7' 'rich>=13'
    python examples/python/get_graph.py
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


# Re-usable adapter (faster than re-parsing a model on every call).
GraphAdapter: TypeAdapter[GraphResponse] = TypeAdapter(GraphResponse)


Direction = Literal["in", "out", "both"]


def fetch_graph(
    base_url: str,
    *,
    start: datetime,
    end: datetime,
    api_key: str | None = None,
    cluster: list[str] | None = None,
    namespace: list[str] | None = None,
    edge_type: list[EdgeType] | None = None,
    pod: list[str] | None = None,
    root: str | None = None,
    depth: int | None = None,
    direction: Direction | None = None,
    if_none_match: str | None = None,
    timeout: float = 30.0,
) -> tuple[GraphResponse | None, str | None, int]:
    """Call GET /v1/graph and return (graph, etag, status).

    On 304 Not Modified the graph is None — the caller already has the body
    matching the supplied If-None-Match etag.
    """
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

    headers = {"Accept": "application/json"}
    if api_key:
        headers["X-API-Key"] = api_key
    if if_none_match:
        headers["If-None-Match"] = if_none_match

    with httpx.Client(base_url=base_url, timeout=timeout) as client:
        resp = client.get("/v1/graph", params=params, headers=headers)
        if resp.status_code == 304:
            return None, resp.headers.get("etag"), 304
        resp.raise_for_status()
        return (
            GraphAdapter.validate_json(resp.content),
            resp.headers.get("etag"),
            resp.status_code,
        )


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
        g, etag, status = fetch_graph(
            args.base_url,
            start=datetime.fromisoformat(args.start.replace("Z", "+00:00")),
            end=datetime.fromisoformat(args.end.replace("Z", "+00:00")),
            api_key=args.api_key,
            cluster=args.cluster or None,
            namespace=args.namespace or None,
            edge_type=args.edge_type or None,
            pod=args.pod or None,
            root=args.root,
            depth=args.depth,
            direction=args.direction,
            if_none_match=args.if_none_match,
        )
    except httpx.HTTPStatusError as e:
        console.print(f"[red]HTTP {e.response.status_code}[/]: {e.response.text}")
        return 1
    except httpx.HTTPError as e:
        console.print(f"[red]request failed[/]: {e}")
        return 1

    if status == 304:
        console.print(f"[green]304 Not Modified[/] etag={etag}")
        return 0

    assert g is not None
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
