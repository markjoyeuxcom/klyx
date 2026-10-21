# Klyx M5 design (Gateway API topology)

Date: 2026-06-05
Status: approved design, ready for plan
Scope: M5. The marquee feature - render the Gateway API data path as a graph, not
a CRD list (design principle 3). A per-Gateway columnar topology
(Gateway → HTTPRoute → Service → Pods) with policies attached at the correct
node and a route detail panel. Reference: mockup 3 (`docs/design/mockups-2026-06-04.html` #m3) and
the approved brainstorm mockup. Architecture: Approach A (server-side topology
build in Go, snapshot, no watch). Layout: deterministic columnar lanes (no graph
library).

ClusterMesh edges (principle 4) and inline observability (principle 5) are
explicitly deferred (M5-c / M7).

## 1. Scope, navigation, decomposition

The **Network** section of a cluster (currently a placeholder) renders when the
cluster's network capability is present (`networkTier != "Absent"`). It lists the
cluster's **Gateways**; an empty list yields a clear empty state ("No Gateway API
/ no Gateways on this cluster"), so no separate Gateway-API capability field is
needed. Clicking a Gateway opens its **columnar topology**:
the Gateway pinned left (listeners + status + attached ClientTrafficPolicy), one
lane per attached **HTTPRoute** (path matches + status + attached
BackendTrafficPolicy/SecurityPolicy), each route's backend **Service**, and the
backing **Pods** (ready/total, with best-effort CiliumNetworkPolicy). Clicking a
route node opens a **detail panel** below (match / backend / attached policies /
status). Snapshot on open, refresh button, no watch.

Breadcrumb: `Fleet / <cluster> / Network / <gateway>`; the route detail is a
selection within the topology, not a route-level route.

In scope (M5): Gateways list, per-Gateway columnar lane (Gateway/HTTPRoute/
Service/Pods with status), policy attachment (CTP/BTP/SecurityPolicy by
`targetRef`, CNP best-effort by label), route detail panel, capability gate,
cluster-scoped handling, refresh.

Out of scope (deferred): GRPCRoute/TCPRoute (HTTPRoute only for v1), ClusterMesh
cross-cluster edges (M5-c), inline observability metrics (M7), editing, live
watch.

**Plan decomposition** (one spec, likely two plans for native-verify checkpoints):
- **M5-a**: the structural lane - Network section, Gateways list, capability gate,
  the columnar Gateway→HTTPRoute→Service→Pods topology with status. No policies.
- **M5-b**: policy attachment (CTP/BTP/SecurityPolicy/CNP at the right node) + the
  route detail panel.

## 2. Data model + resolution (`internal/gwapi`, new pure package)

The cross-referencing - the fiddly part - lives here, fully unit-tested from
unstructured fixtures.

```go
type Topology struct {
    Gateway  GatewayNode
    Routes   []RouteNode // one lane each, ordered by name
    Warnings []string    // soft, non-fatal issues surfaced to the user (see §3)
}
type GatewayNode struct {
    Namespace, Name, ClassName string
    Listeners  []Listener   // Name, Protocol, Port, Hostname
    Accepted   bool
    Programmed bool
    Policies   []PolicyRef  // targetRef → this Gateway (e.g. ClientTrafficPolicy)
}
type RouteNode struct {
    Namespace, Name string
    Hostnames    []string
    Matches      []Match     // Path{Type,Value}, Method
    Accepted     bool          // status.parents[] Accepted, scoped to THIS Gateway's parentRef
    ResolvedRefs bool          // status.parents[] ResolvedRefs, scoped to THIS Gateway's parentRef
    Backends     []Backend     // all backendRefs: Kind, Name, Port, Weight
    Policies     []PolicyRef   // targetRef → this HTTPRoute (BTP, SecurityPolicy)
    Services     []ServiceNode // one per resolved Service backend (≥0); the lane renders the
                               // first/primary + "+N more", the detail panel lists all
    Pods         PodCount      // ready/total for the primary Service backend
}
type ServiceNode struct {
    Namespace, Name, Type string // ClusterIP/LoadBalancer/...
    Port int32
    CNPs []PolicyRef // CiliumNetworkPolicies whose endpointSelector matches (best-effort)
}
type Listener struct { Name, Protocol, Hostname string; Port int32 }
type Match     struct { PathType, PathValue, Method string }
type Backend   struct { Kind, Name string; Port, Weight int32 }
type PodCount  struct { Ready, Total int }
type PolicyRef struct {
    Kind, Name, Summary string // Kind is the real kind (ClientTrafficPolicy /
                               // BackendTrafficPolicy / SecurityPolicy /
                               // CiliumNetworkPolicy / CiliumClusterwideNetworkPolicy)
                               // so the chip can render "CCNP" distinctly; Summary is
                               // conservative (a few obvious fields per kind, else the
                               // policy name - never a guessed intent)
    Inferred bool             // true for CNP/CCNP attached by the label heuristic, not a targetRef
}
type GatewayRef struct { Namespace, Name, ClassName string; Accepted, Programmed bool }
```

Pure functions:
- `ParseGateway(u) GatewayNode` - `spec.gatewayClassName`, `spec.listeners[]`,
  `status.conditions` (Accepted/Programmed).
- `ParseHTTPRoute(u) RouteNode` - `spec.hostnames`, `spec.rules[].matches[]`
  (path type/value, method), `spec.rules[].backendRefs[]`, and
  `status.parents[].conditions` (Accepted/ResolvedRefs).
- `RouteParents(u) []ParentRef` - `spec.parentRefs[]` (group/kind/name, namespace
  defaulting to the route's), used to link routes to a Gateway.
- `PolicyTargets(u) []TargetRef` - `spec.targetRefs[]` (and legacy singular
  `spec.targetRef`) - `{group, kind, name, sectionName}`. Used to attach a parsed
  policy to the matching Gateway/HTTPRoute/Service node.
- `PolicySummary(kind string, u) string` - a short chip label per policy kind
  (e.g. ClientTrafficPolicy HTTP2 + a connection limit → "h2 16mb"); falls back to
  the policy name.
- `CNPSelector(u) map[string]string` - a CiliumNetworkPolicy's
  `spec.endpointSelector.matchLabels`, for best-effort service matching.
- Wiring: `linkRoutes(gw, routes)` keeps routes whose `parentRefs` reference the
  Gateway; `attachPolicies(topology, policies)` places each policy on the node its
  `targetRef` names; `attachCNPs(service, cnps)` attaches a CNP when its
  `endpointSelector.matchLabels` is a subset of the Service's `spec.selector`.

`gwapi` is given already-parsed pieces + the Service/Pod data; it does the
resolution. No client-go dependency beyond `unstructured`.

## 3. Data layer (`internal/fleet`)

Two methods on `ClusterConn` (added to the `Conn` interface + `fakeConn`):

```go
func (c *ClusterConn) ListGateways(ctx) ([]gwapi.GatewayRef, error)
func (c *ClusterConn) GetGatewayTopology(ctx, namespace, name string) (gwapi.Topology, error)
```

GVRs (preferred version resolved via the existing `preferredVersion` helper):
- `gateway.networking.k8s.io`: `gateways`, `httproutes` (v1).
- `gateway.envoyproxy.io`: `clienttrafficpolicies`, `backendtrafficpolicies`,
  `securitypolicies` (v1alpha1).
- `cilium.io`: `ciliumnetworkpolicies` (namespaced) + `ciliumclusterwidenetworkpolicies`
  (cluster-scoped) (v2).

`ListGateways` lists Gateways (dynamic), parses ref + status.

`GetGatewayTopology(ns, name)`:
1. Dynamic Get the Gateway; `gwapi.ParseGateway`.
2. Dynamic list HTTPRoutes (all namespaces); keep those whose `parentRefs`
   reference this Gateway (`gwapi.linkRoutes`); `gwapi.ParseHTTPRoute` each.
3. For each route's primary backend Service: typed Get the `Service` (name/type/
   port) and list `EndpointSlices` (discovery.k8s.io/v1, label
   `kubernetes.io/service-name=<svc>`) → ready/total pod counts.
4. Dynamic list CTP/BTP/SecurityPolicy; `gwapi.attachPolicies` by `targetRef`.
5. Dynamic list CiliumNetworkPolicy (+ clusterwide); `gwapi.attachCNPs` by
   label-subset against each Service's selector (best-effort).
6. Return the assembled `gwapi.Topology`.

Snapshot; no watch. Bounded context.

**Error vs warning (no silent zero-state).** `GetGatewayTopology` returns an error
only for a **core** failure - the Gateway itself can't be read - which the UI must
show as an error, never a fake-empty topology. Everything softer accumulates in
`Topology.Warnings` and the topology still renders:
- Missing Envoy policy CRDs (no CTP/BTP/SecurityPolicy group served).
- Missing Cilium CRDs (no CNP/CCNP).
- A route's `backendRef` Service not found (the lane renders the route with an
  "unresolved backend" marker).
- EndpointSlices unavailable for a backend (pod counts shown as unknown).
- A backendRef that isn't a Service kind (skipped, noted).
- A route with multiple backends collapsed to the primary in the lane.
Each is a human-readable line; the route also carries its own resolution flags so
the affected lane can mark itself rather than relying only on the global list.

## 4. appbridge (`GatewayService`)

A new bound service (registered in `main.go`), request/response (no push):

```go
type TopologyDTO struct {
    Gateway  GatewayNodeDTO `json:"gateway"`
    Routes   []RouteNodeDTO `json:"routes"`
    Warnings []string       `json:"warnings,omitempty"` // soft issues (see §3)
    Error    string         `json:"error,omitempty"`    // a core failure - UI shows it, not a fake-empty topology
}
// ...NodeDTOs mirror the gwapi model with json tags; status as booleans;
// RouteNodeDTO has `services []ServiceNodeDTO`; PolicyRefDTO{kind,name,summary,inferred};
// PodCountDTO{ready,total}.
type GatewayRefDTO struct { Namespace, Name, ClassName string; Accepted, Programmed bool }

func (s *GatewayService) ListGateways(cluster string) GatewayListDTO          // gateways + a served flag (see §5)
func (s *GatewayService) GetGatewayTopology(cluster, namespace, name string) TopologyDTO
```

`GatewayConn` interface (`ListGateways` + `GetGatewayTopology`) satisfied by the
fleet `Conn`; appbridge fake stubs it. On a **cluster miss** or a **core** topology
error, the DTO carries a non-empty `Error` (never a silently-empty graph); soft
issues ride in `Warnings`. `ListGateways` returns `{ gatewayAPIServed bool;
gateways []GatewayRefDTO }` so the frontend can tell "Gateway API not installed"
from "no Gateways". Tested with a fake conn (mapping incl. warnings/error and the
served flag).

## 5. Frontend (`NetworkTopology` + wiring)

- **Nav + three distinct states**: the `network` section renders `NetworkView`
  when `networkTier != "Absent"`. `NetworkView` calls `ListGateways` and shows the
  right empty state for each genuinely different operational situation (do NOT
  conflate them):
  1. Gateway API CRDs not served (`gatewayAPIServed == false`) → "Gateway API is
     not installed on this cluster."
  2. Served but zero Gateways → "No Gateways found."
  3. A selected Gateway with no attached HTTPRoutes → "No HTTPRoutes attached to
     this Gateway."
  Selecting a Gateway sets a route-carried `gateway` ref and renders
  `NetworkTopology`.
- **`NetworkTopology.tsx`**: deterministic CSS-grid columnar lanes - Gateway
  pinned left (listeners, Accepted/Programmed badges, CTP chip), one row per
  HTTPRoute (accepted/resolvedRefs dot scoped to this Gateway, path, BTP/
  SecurityPolicy chips), the route's **primary backend Service** (with a `+N more`
  affordance when a route has multiple weighted backends), Pods (ready/total, or
  "unknown" when EndpointSlices were unavailable), and policy chips. Chevrons
  between columns. Policy chips colour-coded by kind; **CNP/CCNP chips are marked
  `inferred`** (distinct `CCNP` label for cluster-wide, tooltip "matched by Service
  selector against the policy's endpointSelector - not a Gateway API attachment").
  An unresolved backend renders the route lane with an "unresolved backend"
  marker rather than dropping it. A **warnings banner** surfaces `topology.warnings`
  (and a prominent error block if `topology.error` is set). A muted ClusterMesh
  placeholder row. Clicking a route node selects it → detail panel below (match /
  all backends with weights / attached policies / status + a "view YAML" link that
  opens the route via the M4-c instance detail). Refresh re-fetches. Loading state.
- **Route**: the cluster route gains an optional `gateway?: { namespace, name }`
  (sibling to `resource`/`instance`); `setSection`/section changes drop it (same
  pattern as M4). A topology slice holds `gatewayList`, `selectedGateway`,
  `topology`, `selectedRoute`, `loading`, with a ref guard.
- **Bridge**: `listGateways(cluster)`, `getGatewayTopology(cluster, ns, name)`.
- Breadcrumb: `… / Network / <gateway>`; the `Network` crumb returns to the
  Gateways list.

## 6. Testing

- **`internal/gwapi`** (pure, fixtures) - Gateway API status is fiddly, so cover
  the namespace/status edge cases explicitly:
  - `ParseGateway`/`ParseHTTPRoute` (listeners, matches, backends, status conditions).
  - parentRef matching: HTTPRoute in ns A → Gateway in ns B (cross-namespace);
    parentRef with `sectionName`; a route accepted by one parent but rejected by
    another (the per-this-Gateway status must reflect THIS parent only).
  - backendRef: namespace omitted (defaults to the route's) vs namespace set;
    multiple weighted backends → `Services` has all, primary is first; a
    non-Service-kind backendRef skipped with a warning.
  - `PolicyTargets` + `attachPolicies`: CTP→gateway, BTP→route, both `targetRefs`
    and legacy singular `targetRef`, and a `sectionName`-scoped targetRef.
  - `PolicySummary` (a known field → short chip; unknown → name fallback, never a
    guess); `CNPSelector` + `attachCNPs` (label-subset match, non-match, and the
    cluster-wide CCNP marked distinctly + `Inferred`).
- **`internal/fleet`**: `GetGatewayTopology` against the dynamic fake (gateway +
  two httproutes + CTP/BTP) and the typed fake (Services + EndpointSlices →
  ready/total); a missing-policy-group path degrading to empty; the cluster-scoped
  CNP path; `ListGateways`.
- **appbridge**: `TopologyDTO`/`GatewayRefDTO` mapping via a fake conn.
- **frontend (vitest)**: lane render (gateway + routes + services + pods) from a
  mocked bridge; policy chips on the right nodes; route-click → detail panel;
  capability-gated empty state; the Gateways list → select flow.
- **Native handoff**: `homelab-nelli`'s Envoy Gateway - confirm the lane renders
  the real Gateway/routes/services/pods with correct status, the CTP/BTP chips
  land on the right nodes, and the route detail panel shows match/backend/policies.

## 7. Decisions log

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Deterministic columnar lanes, no graph library | The data path has a fixed shape (Gateway→Route→Service→Pods); deterministic CSS layout matches the mockup and removes graph-lib risk |
| 2 | Server-side topology build in `internal/gwapi` (pure) | parentRef/backendRef/targetRef/label resolution is real logic that deserves Go unit tests; the frontend stays a dumb renderer |
| 3 | Snapshot + refresh, no watch | Topology changes rarely; consistent with M4; avoids standing watch over Gateway API objects |
| 4 | Per-Gateway scope as the entry | A Gateway is the ingress entrypoint; showing everything flowing through it is the natural data-path story |
| 5 | HTTPRoute only for v1 | The owner's Envoy Gateway setup is HTTPRoute; GRPCRoute/TCPRoute are a clean later add |
| 6 | CTP/BTP/SecurityPolicy by `targetRef` (precise); CNP best-effort by label | Envoy policies use Gateway API policy-attachment (clean); Cilium CNP attaches to pods by `endpointSelector`, inherently a heuristic |
| 7 | EndpointSlices (not Endpoints) for pod counts | The modern, non-deprecated source on k8s 1.36 |
| 8 | ClusterMesh + observability deferred (M5-c / M7) | ClusterMesh is single-cluster-undogfoodable on the homelab; observability is its own milestone |
| 9 | Two plans under one spec (lane, then policies+detail) | Isolates the structural topology for a native-verify checkpoint before the fiddly policy attachment |
| 10 | Surface `Warnings []` + `Error` in the topology; never a silent zero-state | A blank graph on an error is a debugging swamp; optional-CRD-missing/unresolved-backend are warnings, core failures are a visible error (review) |
| 11 | Model `Services []ServiceNode` per route even though the lane renders the primary | HTTPRoute backendRefs is a weighted list; modelling one Service would hide half a split route - `+N more` in the lane, all in the detail (review) |
| 12 | Three distinct empty states (API not installed / no Gateways / no routes) | They are different operational situations; conflating them annoys the operator (review) |
| 13 | CNP/CCNP chips marked `Inferred`; CCNP labelled distinctly; conservative policy summaries | CNP attaches by label heuristic, not a Gateway API targetRef - the UI must not imply a precise attachment; a guessed summary is worse than the policy name (review) |
| 14 | Route Accepted/ResolvedRefs scoped to THIS Gateway's parentRef | A route can attach to multiple Gateways with different acceptance; the per-Gateway view must show this Gateway's parent status, not a global OR |
