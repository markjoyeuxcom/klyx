# The Line, slice 7: new data, part 2

Design note for slice 7 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §8.3 (last recorded managed-field updates) and §8.5 (route metrics, per rule). The parent is the contract; this note records what exists, how the slice is cut and the points the parent leaves open.
- **Scope:** Go and bindings. Slice 8 draws what this slice collects (the dependency graph reads 7a; the data path reads 7b and 7c), so no screen changes here.

## 0. What exists (surveyed 2026-10-15)

- **Managed fields are dropped everywhere.** The fallback node and pod informers strip them (`internal/fleet/conn.go:465-475`); every Observer kind projects them away (`internal/healthsrc/native.go:95-115`, `project.go:21-32`); detail YAML removes them (`internal/crd/detail.go:340-362`). Nothing reads them.
- **A metadata client exists** per cluster (`ClusterConn.meta`), used only for lists. There is no single-object metadata GET, and no general kind-to-resource resolver (resolvers exist for Flux and Gateway kinds only).
- **The observation log** (`internal/obslog`) is memory only, 20,000 entries and 24h per cluster. It records health-role transitions keyed by object UID, and the line's own predicate batches (`internal/appbridge/line_history.go`), which are the pattern for a new predicate partition. No fingerprint exists anywhere.
- **Gateway API:** the Observer holds HTTPRoutes as full objects, but its rows carry no rules, backends or weights. The live topology (`GetGatewayTopology`) flattens matches across rules and carries no UID. The only per-rule logic is the backend-weight verdict (`internal/gwapi/rules.go`).
- **Route metrics** are route-level only (`internal/routemetrics`): five instant queries at a 5-minute rate, the rule index discarded, no scoping to a Gateway's proxies, no history. `metrics.Client.RangeSeries` reads one series only.
- **Nothing reads** Envoy Gateway's proxy pods or the `EnvoyProxy` resource.
- **Envoy Gateway (read from its v1.9.2 API and translator):**
  - A rule's cluster is named `httproute/<ns>/<name>/rule/<n>` by default (`EnvoyProxy.spec.telemetry.metrics.clusterStatName` changes it).
  - A rule's backends are endpoint groups inside that one cluster, so Envoy Gateway has no per-backend cluster per rule.
  - `spec.mergeGateways` puts several Gateways on one proxy fleet; `spec.mergeBackends` gives one cluster per backend, shared across routes.
  - Proxy pods carry `gateway.envoyproxy.io/owning-gateway-name` and `owning-gateway-namespace`.
- **The kind lab** has Gateway API objects but no Envoy or Prometheus, so 7c rests on captured-metric fixtures, as the parent requires.

## 1. Sub-slices

| | Builds |
|---|---|
| **7a** | The last recorded managed-field update (§8.3): a pure reader, a UID-pinned single-object metadata GET, a bound batch read |
| **7b** | Rule fingerprints and their proven intervals (§8.5 "attributed history"): per-rule fingerprints on the Observer's route rows, a `routes` predicate partition in the observation log, an interval query |
| **7c** | Route metrics per rule (§8.5): the rule index kept, the Gateway's proxies scoped, unattributable cases, the per-rule range queries, per-metric availability, attribution from 7b, the captured-metric fixtures |

Each is Codex-reviewed on its own, in this order.

## 2. 7a: the last recorded managed-field update

- **The reader** (`internal/writes`, pure): from `metadata.managedFields`, the entry with the latest `time`, as `{time, manager, operation, subresource}`.
  - An entry with no `time` is skipped.
  - A `status` subresource entry is labelled "status update"; the reader also returns the latest entry that is not one, which is what change proximity may use. Status updates are never used for it.
  - No entries (absent, or reset) read "no managed-field record". Klyx never says which field changed, and never calls a manager an authenticated writer.
- **The read** (`ClusterConn.ObjectWrites`): for each reference `{group, version, kind, namespace, name, uid}`, one metadata GET (`PartialObjectMetadata`), metadata only.
  - The resource comes from the cached discovery: the served resource for the group, version and kind.
  - **UID-pinned:** a different UID reads "replaced"; NotFound reads "gone"; any other failure is that reference's error, never "no managed-field record".
  - **Bounds:** at most 200 references per call, 8 at a time, 10s for the whole call.
- **The binding:** a bound batch method returning one result per reference. Slice 8's dependency graph calls it for its nodes and their supply objects.

### 2.1 As built in 7a

- **The reader** is `internal/lastwrite` (`Read` gives `Last` and `LastSpec`).
- **The read** is `ClusterConn.ObjectWrites`, on the fleet `Conn` interface. The resource comes from the cached discovery's group-version list: a top-level resource of that kind, never a subresource. An empty version takes the group's preferred one.
- **The binding** is `ObjectWritesService.ObjectWrites(cluster, refs)`, with `bridge/writes.ts` for slice 8. A refused batch (over 200) is an error for the whole call; every other failure is per reference.
- **Live:** `TestLabObjectWrites` reads the kind lab, read-only.

## 3. 7b: rule fingerprints and proven intervals

- **The fingerprint** of a rule index is a hash (sha256, hex) of its matches and its backends with their weights, in rule order:
  - matches: path type and value, headers, query parameters, method;
  - backends: group, kind, namespace (defaulted to the route's), name, port, weight (defaulted to 1).
  - Filters, timeouts and session settings are not in it, as the parent defines it.
- **Where it is computed:** the Observer's gateway domain already holds each HTTPRoute whole. Its route rows gain `rules: [{index, fingerprint}]`, computed when the route is evaluated.
- **The partition:** an appbridge writer, modelled on `line_history.go`, writes predicate batches into the observation log under task `routes`, part `rules`:
  - one key per rule, `route/<uid>#<index>`, whose value is its fingerprint;
  - a baseline at first sight, a change when a fingerprint changes, a loss and an after-gap across a coverage break, an end when the route goes.
- **The interval query** (`RuleInterval(cluster, uid, index)`): the start of the current proven interval, the later of:
  - the last fingerprint change (observed);
  - the first sighting (Klyx saw no earlier history);
  - the start of the partition's current live span, after a coverage gap.

  It returns no interval when the key is unknown, suspended by a loss, or past the log's trim.
- **A recreated route** has a new UID and so a new key. A reordered route changes the fingerprints at each index. Neither joins history to today's backends.

## 4. 7c: route metrics per rule

- **Parsing** keeps the rule index: `httproute/<ns>/<name>/rule/<n>` reads as (route, rule). Every other shape is skipped, never guessed.
- **Scoping to the Gateway's proxies:**
  - The proxy pods are listed by `gateway.envoyproxy.io/owning-gateway-name` and `owning-gateway-namespace`, in any namespace.
  - The series are filtered by the labels the scrape gives them, detected per query set, and always as a pair that identifies one pod: the owning-gateway name and namespace labels themselves when the series carry them; else `pod` with `namespace`; else `kubernetes_pod_name` with `kubernetes_namespace`. A pod name alone is never used: names are unique only within a namespace (design review of 5cdcaa4).
  - With no such pair, or any ambiguity, per-rule metrics read "unavailable (the series cannot be scoped to this Gateway's proxies)". Scoping fails closed.
- **Unattributable,** shown "unavailable (shared or custom cluster names)", when:
  - the `EnvoyProxy` that configures the Gateway (its `infrastructure.parametersRef`, else its GatewayClass's `parametersRef`) sets `telemetry.metrics.clusterStatName`, `mergeGateways` or `mergeBackends`;
  - the route attaches to the same Gateway more than once (one cluster per rule serves every listener it names);
  - the `EnvoyProxy` cannot be read when one is referenced.
- **The queries, per rule,** as range queries over 30 minutes at a 30-second step, with a 1-minute rate:
  - the request rate (`envoy_cluster_upstream_rq_total`);
  - the response-class shares (`envoy_cluster_upstream_rq_xx`);
  - the 503 share (`envoy_cluster_upstream_rq`, `envoy_response_code="503"`);
  - retries (`envoy_cluster_upstream_rq_retry`).

  `metrics.Client` gains a multi-series range reader. Every value is per rule; nothing is summed across rules.
- **Availability is per metric:** each of the four is available, absent ("no series"), or failed, apart.
- **The per-backend split** is unavailable for Envoy Gateway: a rule's backends share its one cluster, and `mergeBackends` clusters are shared across routes. The DTO keeps the field for gateways that expose one.
- **Attribution (7b):** two distinct times are reported, the interval start (the proof) and the attribution cut-off (that start plus one rate window, 1 minute).
  - The points before the cut-off are marked "unattributable before <time>", and are not this rule's.
  - With no interval, the whole series is unattributable.
- **Direct and local responses** (a `directResponse`, a redirect, a 404 with no route) never reach an upstream cluster, so they are absent from these series, and the result says so.
- **The binding:** a bound per-Gateway method returning, for each of its routes' rules, the four series with their availability and attribution. The route-level metrics the topology shows today stay as they are until slice 8.
- **Captured-metric fixtures:** Prometheus JSON responses for dedicated clusters, merged backends (shared), a custom stat name, several parents, retries, and direct and local responses.

## 5. Testing

- **7a:** the reader over real `managedFields` (apply, update, status subresource, missing time, empty); the read against a fake metadata client (found, replaced, gone, forbidden, bounds); the kind lab, read-only.
- **7b:** fingerprints (stable across key order and defaults; a weight, a match or a backend change moves only that rule's; a reorder moves both); the partition through baseline, change, gap and end; the interval for each case.
- **7c:** the parser; scoping by each label form and its absence; each unattributable case; the four queries' PromQL; per-metric availability; attribution cut-offs; every fixture.

## 6. Open points (ruled in design review of 5cdcaa4: all six accepted, point 2 with the namespace-paired scoping above, point 6 with the cut-off kept distinct from the proof start)

1. **7a's consumer is slice 8:** 7a ships the reader, the read and the binding, with no screen.
2. **Pod scoping** detects the series' labels per query set, always a pair that names one pod (owning-gateway name and namespace, `pod` and `namespace`, or `kubernetes_pod_name` and `kubernetes_namespace`), and fails closed to "unavailable".
3. **Several parents:** a route attached to the same Gateway more than once is unattributable. Attached to different Gateways, each Gateway's proxies scope its own series.
4. **No per-backend split for Envoy Gateway** (one cluster per rule; `mergeBackends` clusters are shared across routes).
5. **The fingerprint** covers matches, backends and weights only, as the parent defines it.
6. **Two times, kept apart:** the interval start (the proof) is the latest of the last change, the first sighting and the start of the post-gap live span; the attribution cut-off is that start plus the 1-minute rate window.
