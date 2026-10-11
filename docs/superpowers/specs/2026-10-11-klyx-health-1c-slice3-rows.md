# Klyx health 1c - slice 3 design: lenses read Observer rows

Status: proposal for review, 2026-10-11. Parent spec: `2026-10-11-klyx-health-1c-cluster-summary.md` (§4 publication, §9 slice 3). Slices 1-2 are closed: every task's kinds are watched by the per-cluster Observer for the cluster's lifetime.

## 1. What changes

Today no lens reads the Observer. Each reads its own source:

- push lenses on lazy watches with their own push loops: Flux, Certificates, External Secrets;
- push lenses on `OpenLive*` re-list loops: Pods, Workloads;
- fetch lenses: Nodes, Argo CD, Karpenter, Gateways.

Every kind those lenses read is already watched by the Observer, so each open lens duplicates a watch and evaluates its rule a second time.

Slice 3 makes these lenses read their task's rows from the Observer, so the lens and the summary come from one capture: the one-number rule of §4.

The following stay per-fetch, with an as-of label, as the spec says:

- Helm and Events;
- the Gateway topology's data path;
- Argo AppProjects (not an Observer kind);
- Karpenter's controller probe and disruption events.

## 2. Transport

- **Rows are lens DTOs, not domain structs.** Today `HealthSink.Rows` emits raw `observe.Rows`. Their domain types have no JSON tags, and the verdict kind is an integer. A converter registry in `appbridge` keyed by `task/part` turns each part's rows into the lens's existing DTOs with the existing converters:
  - `toCertificateDTO`, `toIssuerDTO`;
  - `toExternalSecretDTO`, `toStoreDTO`;
  - `ToFluxDTO`, `toSourceDTO`;
  - `toArgoAppDTO`, `toArgoAppSetDTO`;
  - `toKarpenterPoolDTO`, `toKarpenterClaimDTO`;
  - `toGatewayRefDTO`;
  - `toPodSummaryDTO`, `toWorkloadDTO`, `toNodeSummaryDTO`.

  The DTO carries the rule that the Observer's capture evaluated (§2 of the parent spec), so the rule a lens shows is the summary's.
- **Payload:** `RowsDTO{cluster, incarnation, task, part: Part (counts, coverage, kinds, Ver, Obs, AsOf), evaluatedAt, rows: []DTO}`.
- **Event:** `health:rows:<cluster>:<task>/<part>`, which is what the code already emits. The parent spec's `health:rows:<cluster>:<part>` is ambiguous, because part IDs are only unique within a task. The spec text is corrected.
- **Bound API on HealthService:**
  - `OpenRows(cluster, task, part, owner) RowsDTO`: subscribes (`SubscribeRows` with `Owner{Kind:"lens", ID: owner}`), then bootstraps with a fresh capture (`RowsNow`). That is subscribe-before-bootstrap: the frontend listens to the event first, and both paths keep only strictly newer `(incarnation, Ver)`.
  - `CloseRows(cluster, task, part, owner)`.
  - `RowsBootstrap(cluster, task, part, requestGen) RowsDTO`: the fenced re-bootstrap of A4.
  - Owner IDs are per lens instance, so two mounted views of one part are two subscribers.
- **Leases:** `HealthOwner` already holds every domain for the cluster's lifetime. A lens subscription adds a rows subscriber, not a watch.

## 3. Time

Rows are pushed when a part's `Ver` advances. Relative fields such as `ageSeconds`, `readySinceSeconds`, `expiresInSeconds` and `lastRefreshAgeSeconds` would therefore freeze between pushes. Today they are recomputed every tick with a 30s heartbeat.

The rows DTOs therefore carry absolute times (unix seconds) alongside or instead of the relative ones, and lenses tick them with the existing `useNow`. Time-dependent rules need no frontend logic:

- certificate expiry;
- ESO refresh overdue;
- the Karpenter launch deadline;
- the Helm pending deadline (still a snapshot, see §1).

The Observer's reclassification timer re-evaluates at each rule's `Next` and advances `Ver`, so the rows and rule are re-pushed at the boundary.

## 4. One number per task (A4), in the frontend

A new `bridge/healthRows.ts` and a store slice hold `rows[cluster][task/part] = {incarnation, Ver, part, rows, evaluatedAt, state}`. The open lens reads its rows and counts only from there. The mast and tree read the summary (slice 4).

The mismatch rule:

- When the summary's part `Ver` is above the rows' `Ver`, a 1s deadline starts at the first unresolved mismatch. Later ticks never reset it.
- Until the deadline, the rows' counts stand, marked `updating`.
- At the deadline:
  - the counts switch to the summary's;
  - the rows are qualified "as of HH:MM:SS - refreshing" with verdict chips dimmed;
  - a fenced `RowsBootstrap` with a request generation is issued.
- A reply applies only if its generation is current and its `Ver` is newer than the rows held.

Tests, per §4:

- continuous churn;
- summary newer than rows, and rows newer than summary;
- a time boundary crossing with an open lens;
- error and recovery with no mutation;
- unrelated parts keeping their `Ver`;
- a dropped rows event and a delayed one;
- a bootstrap reply racing a later event.

## 5. Per lens

Each lens is its own commit, reviewed, in this order.

1. **Certificates** (`certs/certificates`, `certs/issuers`):
   - Coverage and watchError come from `part.kinds`. The lens's `KindCoverageDTO{kind:"Certificate",…}` maps from the Observer's kind names.
   - `GatewayTopology` reads the cert-manager slice (`GatewayTopology.tsx:64`), so it moves to the rows store too. It then works without CertsView mounted.
   - The issuance-chain resolver's issuer lookup reads the lazy store today (`certmanagerchain.go:40`). It moves to a direct GET with the served version, like its certificate fallback, so the lazy store can go.
2. **External Secrets** (`eso/externalsecrets`, `eso/stores`): coverage as for certificates.
3. **Flux** (`flux/reconciliations`, `flux/sources`):
   - The combined `gitops:updated` becomes two parts.
   - `GetResourceDetail` and `ResolveGitLink` read objects from the lazy store today (`GitOpsObject`, `GitOpsSourceObject`). They move to direct GETs at the served version.
4. **Argo CD** (`argo/applications`, `argo/applicationsets`):
   - The broken-first order moves to the frontend sort, which the lens mostly has already.
   - `Available`, `Message` and `Error` come from coverage.
   - AppProjects stay a fetch. Adding an AppProject kind to the Observer is a separate decision.
5. **Karpenter** (`karpenter/nodepools`, `karpenter/nodeclaims`): pools and claims come from rows. `GetKarpenterState` shrinks to the controller probe and the disruption-event feed, both per-fetch with as-of.
6. **Gateways list** (`gateway/gateways`, plus the routes part view of §1):
   - `GatewayAPIServed` comes from absent coverage.
   - The topology stays a fetch. The routes part rows (`observe.Attached`) carry the verdict, not the data path.
7. **Nodes** (`nodes/nodes`).
8. **Workloads** (`workloads/workloads`):
   - `FluxPresent` travels in the payload, as a fact that also sits on the summary.
   - Evidence: `PodsComplete` means the coverage is live.
9. **Pods** (`pods/pods`):
   - The Observer is all-namespaces and uncapped. The namespace filter moves to the frontend. The 5000 cap and its `Truncated` flag go, and the virtualized list holds the full set.
   - Payload size is measured on the kind lab (5000 pods, all rows open) before this lens switches. If a full-rows push under churn is too heavy for the webview, the fallback is per-subscription namespace filtering in the converter, which needs per-owner payloads. It is decided by measurement, not now.

## 6. Removals

These go once their last reader has moved:

- the lazy watches `OpenGitOps`, `OpenCertManager` and `OpenExternalSecrets`, with their push loops and events;
- `OpenLivePods` and `OpenLiveWorkloads`, with `WatchDirty`.

The eager typed node informer goes when the Nodes lens reads rows. The connection's node counts then come from the nodes kind. That needs a count of Ready nodes, which `KindState` lacks: either a per-kind tally hook evaluated under the Observer's lock at notify time, or the nodes part's verdict counts if their semantics match `NodeReadiness`. This is to be decided in that commit.

The request methods `ListWorkloads`, `ListPods`, `ListNodes`, `ListApplications` and `ListGateways` stay while `bridge/attention.ts` and `bridge/fleetboard.ts` call them. Those go in slices 4 and 5.

## 7. Acceptance

For every lens moved:

- the header and filter count, the nav count and the summary part's `Included` are equal at one `Ver`;
- no lens watch is open for its kinds;
- the lab rerun shows the WATCH delta drop accordingly.

A kind lab run with every lens open reports RSS and the rows payload rates.
