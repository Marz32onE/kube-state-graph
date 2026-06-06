# `pod-calls-service` Edge Type + `others` Node Removal — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Type the service-graph call edge as `pod-calls-service` whenever its target resolves to a `service` node, route unresolvable `"://"` connection strings to `external` instead of `others`, and remove the now-unused `OthersNode` type entirely.

**Architecture:** The service-graph resolver (`pkg/build/servicegraph.go`) already resolves `"://"` server endpoints to `service` nodes; we (1) pick the edge type by target-node kind at edge-construction time, (2) flip the unresolvable-`"://"` fallback from `othersNode()` to `external()`, and (3) delete the `OthersNode` sealed type and all references. Registry, contract docs, golden snapshots, and OpenAPI are updated to match.

**Tech Stack:** Go 1.26, Gin, testify, mockery, swaggo/swag, testcontainers-go (integration).

**Background reading before starting:**
- Design doc: `docs/superpowers/specs/2026-06-03-pod-calls-service-edge-design.md`
- `pkg/build/servicegraph.go` — the resolver (edge loop ~L128-142, `resolveConnString` ~L225-237, `othersNode` ~L300-310).
- `pkg/graph/registry.go` — the `EdgeTypes` catalogue served by `/v1/edge-types`.
- `pkg/graph/node.go` — sealed node types (`OthersNode` L100-114, `NodeTypeOthers` L13, `OthersID` L157).
- CLAUDE.md sections on D27 / D29 (connection-string resolution, missing-UID fallback).

**Conventions:**
- TDD: change the test to the new expectation (RED), run it, then change impl (GREEN).
- Commit after each task. Edge IDs are UUIDv5 over `<type>|<source>|<target>`, so changing an edge's type changes its ID — golden snapshots are refreshed with `-update`.
- `pkg/` MUST NOT import `internal/*`. Serialisation goes through `GraphNode` methods, never type switches.

---

### Task 1: Add the `pod-calls-service` edge type constant

**Files:**
- Modify: `pkg/graph/edge.go:12-18`

- [ ] **Step 1: Add the constant**

In `pkg/graph/edge.go`, add `EdgeTypePodCallsService` to the const block:

```go
const (
	EdgeTypePodMountsPVC      EdgeType = "pod-mounts-pvc"
	EdgeTypePodCallsPod       EdgeType = "pod-calls-pod"
	EdgeTypePodCallsService   EdgeType = "pod-calls-service"
	EdgeTypeServiceSelectsPod EdgeType = "service-selects-pod"
	EdgeTypeSwitchToSwitch    EdgeType = "switch-to-switch"
	EdgeTypeNodeToSwitch      EdgeType = "node-to-switch"
)
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./pkg/graph/`
Expected: success, no output.

- [ ] **Step 3: Commit**

```bash
git add pkg/graph/edge.go
git commit -m "feat(graph): add pod-calls-service edge type constant"
```

---

### Task 2: Register `pod-calls-service` and re-scope `pod-calls-pod` in the catalogue

**Files:**
- Modify: `pkg/graph/registry.go:37-47` (the `pod-calls-pod` entry)
- Test: `pkg/graph/service_test.go:52-88`

- [ ] **Step 1: Update the registry test to the new contract (RED)**

In `pkg/graph/service_test.go`, replace the `pod-calls-pod` assertions (L82-87) and add `pod-calls-service` coverage. Replace the whole `TestEdgeTypeServiceSelectsPod_Registered` tail (from `if podCallsPod == nil {` onward) and extend the switch to capture the new type:

```go
func TestEdgeTypeServiceSelectsPod_Registered(t *testing.T) {
	var serviceSelectsPod *EdgeTypeDefinition
	var podCallsPod *EdgeTypeDefinition
	var podCallsService *EdgeTypeDefinition
	for i := range EdgeTypes {
		switch EdgeTypes[i].Type {
		case EdgeTypeServiceSelectsPod:
			serviceSelectsPod = &EdgeTypes[i]
		case EdgeTypePodCallsPod:
			podCallsPod = &EdgeTypes[i]
		case EdgeTypePodCallsService:
			podCallsService = &EdgeTypes[i]
		default:
			// other edge types are not under test here
		}
	}

	if serviceSelectsPod == nil {
		t.Fatal("EdgeTypeServiceSelectsPod is not registered in EdgeTypes")
	}
	if serviceSelectsPod.MayCrossCluster {
		t.Error("service-selects-pod must be intra-cluster (may_cross_cluster=false)")
	}
	if !containsNodeType(serviceSelectsPod.SourceType, NodeTypeService) {
		t.Errorf("service-selects-pod source_type = %v, want to contain service", serviceSelectsPod.SourceType)
	}
	if !containsNodeType(serviceSelectsPod.TargetType, NodeTypePod) {
		t.Errorf("service-selects-pod target_type = %v, want to contain pod", serviceSelectsPod.TargetType)
	}

	if podCallsPod == nil {
		t.Fatal("EdgeTypePodCallsPod is not registered")
	}
	if containsNodeType(podCallsPod.TargetType, NodeTypeService) {
		t.Errorf("pod-calls-pod target_type = %v, must NOT include service (service targets use pod-calls-service)", podCallsPod.TargetType)
	}
	if containsNodeType(podCallsPod.TargetType, NodeTypeOthers) {
		t.Errorf("pod-calls-pod target_type = %v, must NOT include others (others removed)", podCallsPod.TargetType)
	}

	if podCallsService == nil {
		t.Fatal("EdgeTypePodCallsService is not registered")
	}
	if podCallsService.MayCrossCluster {
		t.Error("pod-calls-service must be intra-cluster (may_cross_cluster=false)")
	}
	if !containsNodeType(podCallsService.TargetType, NodeTypeService) {
		t.Errorf("pod-calls-service target_type = %v, want to contain service", podCallsService.TargetType)
	}
	if len(podCallsService.TargetType) != 1 {
		t.Errorf("pod-calls-service target_type = %v, want exactly [service]", podCallsService.TargetType)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails (RED)**

Run: `go test ./pkg/graph/ -run TestEdgeTypeServiceSelectsPod_Registered -v`
Expected: FAIL — `pod-calls-pod target_type ... must NOT include service` and `EdgeTypePodCallsService is not registered`.

- [ ] **Step 3: Update the registry (GREEN)**

In `pkg/graph/registry.go`, replace the `pod-calls-pod` entry (L37-47) with a re-scoped entry plus a new `pod-calls-service` entry:

```go
	{
		Type:            EdgeTypePodCallsPod,
		Description:     "Pod-UID-resolved RPC edge from service-graph metrics. May cross clusters when the resolved source and target pods live in different clusters (recovered from the topology pod-UID index since the metric only carries the trace-source cluster). An endpoint whose client/server label is a '://' connection string resolving to an in-cluster Kubernetes Service produces a 'pod-calls-service' edge instead (see that type); a '://' string that does NOT resolve to a known service falls back to an 'external' node, and endpoints with a missing pod UID and a non-URL label become 'external' nodes via the human-label fallback (D27).",
		SourceType:      []NodeType{NodeTypePod, NodeTypeService, NodeTypeExternal},
		TargetType:      []NodeType{NodeTypePod, NodeTypeExternal},
		Directed:        true,
		MayCrossCluster: true,
		Labels: []EdgeTypeLabel{
			{Name: "cluster", ValueType: "string"},
		},
	},
	{
		Type:            EdgeTypePodCallsService,
		Description:     "Service-graph call edge whose target resolves to an in-cluster Kubernetes Service node (from a '://' connection string per D29). The Service fans out service-selects-pod edges to its backing pods. Always intra-cluster — the service is resolved in the trace-source (client) cluster. Carries labels.cluster when the client side is a pod (D9).",
		SourceType:      []NodeType{NodeTypePod, NodeTypeService, NodeTypeExternal},
		TargetType:      []NodeType{NodeTypeService},
		Directed:        true,
		MayCrossCluster: false,
		Labels: []EdgeTypeLabel{
			{Name: "cluster", ValueType: "string"},
		},
	},
```

- [ ] **Step 4: Run the test to verify it passes (GREEN)**

Run: `go test ./pkg/graph/ -run TestEdgeTypeServiceSelectsPod_Registered -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/graph/registry.go pkg/graph/service_test.go
git commit -m "feat(graph): register pod-calls-service, re-scope pod-calls-pod target types"
```

---

### Task 3: Emit `pod-calls-service` when the call target is a service node

**Files:**
- Modify: `pkg/build/servicegraph.go:128-142` (the edge-construction loop)
- Test: `pkg/build/servicegraph_test.go` (the existing `"://"` server-resolution test, ~L148-185)

The existing test (around L148) drives a `checkout → https://payments.shop.svc.cluster.local/api` series whose server resolves to the `payments` service node and asserts a `pod-calls-pod` edge whose target is the service. We change that expectation to `pod-calls-service`.

- [ ] **Step 1: Update the service-target assertion to the new type (RED)**

In `pkg/build/servicegraph_test.go`, in the test that resolves a server `"://"` string to the `payments` service (the one asserting `pcp[0].Target == "cluster-alpha/shop/payments"`, near L172-179), change the edge-type expectation. Locate where it filters/asserts `EdgeTypePodCallsPod` for the service-target edge and assert `EdgeTypePodCallsService` instead. Concretely, the edge to the service node must now be found by type `graph.EdgeTypePodCallsService`:

```go
	// The call edge to a resolved service node is now typed pod-calls-service.
	var pcs []*graph.Edge
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePodCallsService {
			pcs = append(pcs, e)
		}
	}
	require.Len(t, pcs, 1, "one pod-calls-service edge to the service node")
	assert.Equal(t, "cluster-alpha/shop/payments", pcs[0].Target, "target is the service node")
	assert.Equal(t, "cluster-alpha", pcs[0].Labels["cluster"], "client pod cluster present (D9)")
```

Apply the same `pod-calls-pod` → `pod-calls-service` change to the other service-target cases in this file: the headless `mongodb://mongo-0.mongo.db.svc...` case (~L191-213, target `cluster-alpha/db/mongo`) and the zero-endpoint `redis://...` case (~L223-239). For those, switch the edge lookup/type assertion to `EdgeTypePodCallsService`. Leave `pod-calls-pod` assertions that target a pod/external untouched.

- [ ] **Step 2: Run the tests to verify they fail (RED)**

Run: `go test ./pkg/build/ -run TestParseServiceGraph -v`
Expected: FAIL — the service-target edges are still typed `pod-calls-pod`.

- [ ] **Step 3: Implement target-driven edge typing (GREEN)**

In `pkg/build/servicegraph.go`, in `parseServiceGraph`, replace the edge-construction loop (currently L129-139) so the type depends on whether the target resolved to a service node:

```go
	edges := make([]*graph.Edge, 0, len(pairs)+len(res.svcEdges))
	for k, agg := range pairs {
		// Edge `cluster` label is the trace-source / client-side cluster, but
		// only when the client side is a pod (per design D9). A client "://"
		// label resolves to a service or external node (never a pod), so such
		// an edge never carries cluster.
		labels := map[string]string{}
		if agg.srcIsPod {
			labels["cluster"] = agg.srcCluster
		}
		// Edge type is target-driven: a target that resolved to a service node
		// (via the D29 "://" connection-string rule) yields pod-calls-service;
		// every other target (pod, synth-pod, external) stays pod-calls-pod.
		edgeType := graph.EdgeTypePodCallsPod
		if _, isSvc := res.services[k.tgt]; isSvc {
			edgeType = graph.EdgeTypePodCallsService
		}
		edges = append(edges, graph.NewEdge(edgeType, k.src, k.tgt, labels))
	}
	for _, e := range res.svcEdges {
		edges = append(edges, e)
	}
```

- [ ] **Step 4: Run the tests to verify they pass (GREEN)**

Run: `go test ./pkg/build/ -run TestParseServiceGraph -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/build/servicegraph.go pkg/build/servicegraph_test.go
git commit -m "feat(build): emit pod-calls-service when the call target is a service node"
```

---

### Task 4: Route unresolvable `"://"` connection strings to `external`

**Files:**
- Modify: `pkg/build/servicegraph.go:225-237` (`resolveConnString`)
- Test: `pkg/build/servicegraph_test.go` (all unresolvable-`"://"` cases asserting `OthersNodes`)

This task flips behaviour but still leaves the `OthersNode` type/method in place (removed in Task 5). After this task, `othersNode()` and the `others` map become unused — that is expected and resolved in Task 5; do not delete them here (the file must still compile, and `res.others` is still referenced by `parseServiceGraph`'s result assembly until Task 5).

- [ ] **Step 1: Flip the unresolvable assertions from others to external (RED)**

In `pkg/build/servicegraph_test.go`, update every case that today asserts an `others` node from an unresolvable `"://"` string. These are at approximately:
- L282-291 (`https://payments.partner.example/api` → was `others/...`)
- L311 (`OthersNodes, 2`)
- L332-333, L350-351 (host not a 2/3-label `.svc` name; `ghost-svc.ghost-ns...`)
- L555-570 (connection-string resolution wins over missing-UID fallback)
- L575-599 (the "others/external disjoint" case)

For each, replace `res.OthersNodes` with `res.ExternalNodes` and `graph.OthersID(...)` / `"others/..."` with `graph.ExternalID(...)` / `"external/..."`. For example, the `https://payments.partner.example/api` case becomes:

```go
	require.Len(t, res.ExternalNodes, 1)
	ext := res.ExternalNodes[0]
	assert.Equal(t, "external/https://payments.partner.example/api", ext.IDValue)
	assert.Equal(t, "https://payments.partner.example/api", ext.NameValue)
	assert.Empty(t, ext.LabelsValue)
	// the call edge targets the external node and stays pod-calls-pod
	assert.Equal(t, "external/https://payments.partner.example/api", pcp[0].Target)
```

For the former "others/external disjoint" test (L575-599): both an unresolvable `"://"` string and a non-URL missing-UID label now produce `external` nodes. Rewrite it to assert two **distinct** external nodes keyed by their verbatim labels (the `"://"` label vs the bare label), and that `OthersNodes` is gone (assert `res.ExternalNodes` length 2 with the two expected IDs). Rename the test from its `*OthersAndExternalDisjoint*` name to e.g. `TestParseServiceGraph_ConnStringAndMissingUIDBothExternal`.

For the connection-string-wins case (L555-570), the server `http://api.example.com` with empty UID now yields `external/http://api.example.com` (not `others/...`); update the ID and drop the `assert.Empty(t, res.ExternalNodes ...)` line (external is now expected).

- [ ] **Step 2: Run the tests to verify they fail (RED)**

Run: `go test ./pkg/build/ -run TestParseServiceGraph -v`
Expected: FAIL — resolver still returns `others` nodes.

- [ ] **Step 3: Flip the fallback to external (GREEN)**

In `pkg/build/servicegraph.go`, in `resolveConnString` (L225-237), change the fallback return and its comment:

```go
func (r *sgResolver) resolveConnString(label, traceCluster string) string {
	if host := connStringHost(label); host != "" {
		if svc, ns, ok := classifyK8sDNS(host); ok {
			if id, ok := r.resolveServiceLevel(traceCluster, ns, svc); ok {
				return id
			}
		}
	}
	// Unresolvable: not a parseable host, not a 2/3-label k8s .svc name, or the
	// service is absent from the trace cluster's topology → external node
	// (labels={}, verbatim label as name). Keeps truly-external URLs and unknown
	// in-cluster names visible.
	return r.external(label)
}
```

- [ ] **Step 4: Run the tests to verify they pass (GREEN)**

Run: `go test ./pkg/build/ -run TestParseServiceGraph -v`
Expected: PASS.

- [ ] **Step 5: Update doc comments referencing the others fallback**

In `pkg/build/servicegraph.go`, update the comments that still say "falls back to an others node":
- The `ReadServiceGraph` doc comment (L25-33): change "falling back to an others node" → "falling back to an external node".
- `resolveEmptyUID` doc comment (L166-183): change "Stage 0: service / others" → "Stage 0: service / external".

In `pkg/build/topology.go`, update the stale comments at L109 ("fall back to `others/<label>`") and L380 ("falls back to others/<label> downstream") to say `external/<label>`.

- [ ] **Step 6: Commit**

```bash
git add pkg/build/servicegraph.go pkg/build/servicegraph_test.go pkg/build/topology.go
git commit -m "feat(build): resolve unresolvable connection strings to external, not others"
```

---

### Task 5: Remove the `OthersNode` type and all references

**Files:**
- Modify: `pkg/graph/node.go` (remove `NodeTypeOthers` L13, `OthersNode` L100-114, `OthersID` L156-157)
- Modify: `pkg/build/servicegraph.go` (remove `others` map field L61, init L82, `othersNode()` L300-310, `ServiceGraphResult.OthersNodes` L20 + its assembly L154-156, L147-148)
- Modify: `pkg/build/build.go:161-186` (`assemble` — drop `OthersNodes` from the total and the append loop)
- Test: `internal/api/golden_test.go` (remove `buildWithOthers`), `internal/api/testdata/golden/with-others-cytoscape.json` (delete)

- [ ] **Step 1: Delete the `with-others` golden and its builder (RED via compile)**

Delete the golden file and remove its registration + builder from the golden test:

```bash
git rm internal/api/testdata/golden/with-others-cytoscape.json
```

In `internal/api/golden_test.go`: remove the `"with-others": buildWithOthers(),` entry from the cases map (L27) and delete the entire `buildWithOthers()` function (the block around L85-95 that constructs `&graph.OthersNode{...}`). Verify no other reference to `buildWithOthers` remains:

Run: `grep -rn "buildWithOthers" internal/`
Expected: no matches.

- [ ] **Step 2: Remove `OthersNodes` from the build result and assembly**

In `pkg/build/servicegraph.go`:
- Remove the `OthersNodes []*graph.OthersNode` field from `ServiceGraphResult` (L21).
- Remove the `others map[string]*graph.OthersNode` field from `sgResolver` (L61).
- Remove the `others: map[string]*graph.OthersNode{},` initialiser (L82).
- Remove the `OthersNodes: make([...]...)` initialiser in the result (L147) and the `for _, o := range res.others { ... }` loop that fills it (L154-156).
- Delete the `othersNode()` method entirely (L300-310).

In `pkg/build/build.go`, update `assemble` (L161-186):

```go
func assemble(topology Topology, sg ServiceGraphResult) ([]graph.GraphNode, []*graph.Edge) {
	// Nodes: pods + k8s nodes + pvcs + synthesised pods + services + externals.
	total := len(topology.Pods) + len(topology.Nodes) + len(topology.PVCs) +
		len(sg.SynthPods) + len(sg.ServiceNodes) + len(sg.ExternalNodes)
	nodes := make([]graph.GraphNode, 0, total)
	for _, p := range topology.Pods {
		nodes = append(nodes, p)
	}
	for _, n := range topology.Nodes {
		nodes = append(nodes, n)
	}
	for _, pv := range topology.PVCs {
		nodes = append(nodes, pv)
	}
	for _, p := range sg.SynthPods {
		nodes = append(nodes, p)
	}
	for _, sv := range sg.ServiceNodes {
		nodes = append(nodes, sv)
	}
	for _, e := range sg.ExternalNodes {
		nodes = append(nodes, e)
	}

	edges := make([]*graph.Edge, 0,
		len(sg.Edges)+len(topology.Pods)+len(topology.PodPVCs))
	edges = append(edges, TopologyEdges(topology)...)
	edges = append(edges, sg.Edges...)
	return nodes, edges
}
```

- [ ] **Step 3: Delete the `OthersNode` type from `pkg/graph/node.go`**

Remove:
- `NodeTypeOthers   NodeType = "others"` from the const block (L13).
- The entire `OthersNode` struct + its five methods + `isGraphNode()` (L100-114), including the leading doc comment (L100-102).
- `OthersID` and its doc comment (L155-157).

- [ ] **Step 4: Verify the whole module compiles and no `Others` references remain**

Run: `go build ./... && grep -rn "Others\|NodeTypeOthers\|OthersID\|othersNode" pkg/ internal/ --include='*.go'`
Expected: build succeeds; grep returns no matches (apart from unrelated words — confirm none reference the removed symbols).

- [ ] **Step 5: Run graph + build + api unit tests**

Run: `go test ./pkg/graph/ ./pkg/build/ ./internal/api/ -count=1`
Expected: PASS (golden tests for service-target snapshots refresh in Task 7; if a service-target golden fails here due to the edge-type rename, that is expected and fixed in Task 7 — note it and continue).

- [ ] **Step 6: Commit**

```bash
git add pkg/graph/node.go pkg/build/servicegraph.go pkg/build/build.go internal/api/golden_test.go
git rm internal/api/testdata/golden/with-others-cytoscape.json
git commit -m "refactor(graph): remove OthersNode type; externals subsume the others fallback"
```

---

### Task 6: Update integration tests for the external fallback and the new edge type

**Files:**
- Modify: `internal/integration/graph_e2e_test.go:175-182`, `:311-314`, `:329-337`

- [ ] **Step 1: Flip the unresolvable-connstring integration test to external**

Rename and rewrite `TestConnStringUnresolvableProducesOthersNode` (L175-182):

```go
func (s *GraphSuite) TestConnStringUnresolvableProducesExternalNode() {
	srv := s.StartAPIServer(func(cfg *config.Config) {})
	resp := s.httpGet(s.graphURL(srv.URL, func(q url.Values) { q.Set("edge_type", "pod-calls-pod") }))
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	s.Contains(string(body), `"type":"external"`)
	s.Contains(string(body), `"name":"https://payments.partner.example/api"`)
	s.NotContains(string(body), `"type":"others"`)
}
```

- [ ] **Step 2: Update the `http://user/api` comment (L311-314)**

The assertion at L313 (`"name":"http://user/api"`) still holds — the node is now `external`. Update only the comment (L311-312):

```go
	// The anchored matcher does NOT catch a host that merely contains "user":
	// http://user/api survives and resolves to an external node.
```

- [ ] **Step 3: Add pod-calls-service to the edge-types catalogue assertion (L329-337)**

```go
	for _, et := range []string{"pod-mounts-pvc", "pod-calls-pod", "pod-calls-service"} {
		s.Contains(string(body), et)
	}
```

- [ ] **Step 4: Run the integration suite (Docker required)**

Run: `go test ./internal/integration/ -count=1`
Expected: PASS (requires Docker; skips cleanly if unavailable — if skipped locally, it will run on CI).

- [ ] **Step 5: Commit**

```bash
git add internal/integration/graph_e2e_test.go
git commit -m "test(integration): expect external node for unresolvable conn strings; assert pod-calls-service"
```

---

### Task 7: Regenerate golden snapshots and OpenAPI docs

**Files:**
- Modify (regenerated): `internal/api/testdata/golden/*.json`, `docs/swagger.json`, `docs/swagger.yaml`

- [ ] **Step 1: Refresh golden snapshots**

Run: `go test ./internal/api/ -update -run Golden`
Expected: PASS; `git status` shows updated goldens (at minimum `with-service-cytoscape.json` flips its edge to `"type":"pod-calls-service"` with a new UUIDv5 `id`, plus `edge-types.json` gains the `pod-calls-service` entry and loses `service` from `pod-calls-pod` target types).

- [ ] **Step 2: Review the golden diff**

Run: `git --no-pager diff internal/api/testdata/golden/`
Expected: only edge-type relabels (`pod-calls-pod` → `pod-calls-service` for service-target edges), changed edge `id`s for those edges, `edge-types.json` catalogue changes, and no `"type":"others"` anywhere. Confirm no unintended node/edge churn.

- [ ] **Step 3: Regenerate OpenAPI docs**

Run: `make docs`
Then: `git --no-pager diff docs/`
Expected: `NodeType` enum drops `others` / `NodeTypeOthers`; `EdgeType` enum adds `pod-calls-service` / `EdgeTypePodCallsService`.

- [ ] **Step 4: Run the docs-drift and full unit suite**

Run: `make check-docs` (after staging is irrelevant — it diffs the regenerated docs against the working tree which now matches) and `go test ./... -count=1`
Expected: docs in sync; all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api/testdata/golden/ docs/
git commit -m "chore: regenerate goldens and OpenAPI for pod-calls-service / others removal"
```

---

### Task 8: Update contract documentation

**Files:**
- Modify: `CLAUDE.md` (D27 / D29 connection-string + missing-UID sections, edge-types list)
- Modify: `openspec/changes/add-k8s-pod-graph-api/design.md` and `design.zh-tw.md` (D27 / D29)
- Modify: `openspec/changes/add-k8s-pod-graph-api/specs/pod-service-graph/spec.md` (and `graph-api`, `container-integration` specs where they enumerate node/edge types)
- Modify: `README.md`, `README.zh-tw.md` (edge-type / node-type lists)

- [ ] **Step 1: Update CLAUDE.md**

In `CLAUDE.md`:
- D29 connection-string rule: change every "→ an `others` node" outcome to "→ an `external` node"; remove the "the `pattern` key is GONE" parentheticals tied to others; change the statement "edge `type` stays `pod-calls-pod`" to "edge `type` is `pod-calls-service` when the target resolves to a service node, otherwise `pod-calls-pod`".
- D27 missing-UID rule: remove the "`external/<label>` ID space is **disjoint** from the `others/<label>` ID space" paragraph and the "two distinct nodes (intentional...)" note — both fallbacks now produce `external`. Keep the per-endpoint resolution order but replace step (1)'s "→ `service` ... or `others`" with "→ `service` ... or `external`", and drop references to a separate others dedupe map.
- The `/v1/edge-types` bullet: add `pod-calls-service` alongside `pod-calls-pod` and `service-selects-pod` in the "Current edge types include..." sentence.
- Any remaining mention of an `others` node type (e.g. the sealed-types list "`OthersNode`") — remove it; the sealed concrete types are now `PodNode, K8sNode, PVCNode, ServiceNode, ExternalNode` (plus `SwitchNode` if listed).

- [ ] **Step 2: Update the openspec design docs**

Apply the same D27 / D29 edits to `openspec/changes/add-k8s-pod-graph-api/design.md` and its `design.zh-tw.md` counterpart. In the specs under `openspec/changes/add-k8s-pod-graph-api/specs/`, update any enumerated node-type list to drop `others` and any edge-type list to add `pod-calls-service`, and adjust connection-string fallback wording to `external`.

- [ ] **Step 3: Update README files**

In `README.md` and `README.zh-tw.md`, add `pod-calls-service` to the edge-type list and remove `others` from the node-type list. Adjust any prose describing the `"://"` unresolvable fallback to say `external`.

- [ ] **Step 4: Verify no stale `others` node references linger in docs**

Run: `grep -rin "others node\|OthersNode\|others/<\|type=\"others\"\|\"others\"" CLAUDE.md README.md README.zh-tw.md openspec/`
Expected: no matches describing an `others` *node type* (matches inside the archived/unrelated prose should be reviewed; the goal is zero references to a producible `others` node).

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md README.md README.zh-tw.md openspec/
git commit -m "docs: pod-calls-service edge + external fallback; remove others node from contracts"
```

---

### Task 9: Downstream gateway check + full CI mirror

**Files:**
- Inspect (separate repo): `graph-api-gateway` (wired via local-path `replace` in its `go.mod`)

- [ ] **Step 1: Check the gateway for removed-symbol references**

Locate the gateway repo (sibling of this one; confirm its path from this module's `replace` directive consumer — it embeds `pkg/cytoscape` / `pkg/graph`). From the gateway repo root:

Run: `grep -rn "NodeTypeOthers\|OthersNode\|OthersID\|\"others\"" --include='*.go' .`
Expected: ideally no matches. If any exist, update them (remove `others` branches / constants) so the gateway builds against the new engine.

- [ ] **Step 2: Build the gateway against the updated engine (if it has uncommitted local replace)**

In the gateway repo: `go build ./... && go test ./... -count=1`
Expected: PASS. If the gateway pins a tagged version rather than a local path, note that this engine change requires a new tag + `go.mod` bump in the gateway as a follow-up (out of scope for this plan's repo).

- [ ] **Step 3: Run the full local CI mirror in kube-state-graph**

Run: `make vet && make lint && make vuln && make verify-mocks && make check-docs && make test`
Expected: all green. (`make test` includes `-race -shuffle=on` and the Docker integration suite.)

- [ ] **Step 4: Push**

```bash
git push
```

Expected: the pre-push hook re-runs the CI mirror and reports `ci: all checks passed`; PR #1 CI turns green.

---

## Self-Review Notes (author)

- **Spec coverage:** A→Task 1+3; B→Task 4; C→Task 5; D(registry)→Task 2; E(contract docs)→Task 8; F(tests/artifacts)→Tasks 3,4,6,7; risks(gateway)→Task 9. All design sections mapped.
- **Type consistency:** `EdgeTypePodCallsService` defined in Task 1, used in Tasks 2,3,6,7; `res.services` map membership is the service-target signal (matches `materializeService` which populates it); `external()` already exists (`servicegraph.go:288`) and is reused in Task 4.
- **Compile ordering:** Task 4 leaves `othersNode`/`others` map temporarily unused (still referenced by result assembly until Task 5) — the file compiles because the symbols still exist; Task 5 removes them together with the result field and assembly in one commit, keeping every commit buildable.
- **cross-cluster counts:** `build.go:124` and `graph.go:84` only special-case `EdgeTypePodCallsPod`; `pod-calls-service` (always intra-cluster) is correctly excluded with no code change — verified, no task needed.
