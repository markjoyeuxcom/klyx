# Klyx health 1c, slice 5: the fleet on summaries

Status: draft for Codex review (2026-10-12). Parent spec: `2026-10-11-klyx-health-1c-cluster-summary.md` (§8, §9 slice 5). Slice 4 (`2026-10-12-klyx-health-1c-slice4-summary.md`) is closed: the open cluster's mast, tree and Overview read the cluster's attention model (`nav/clusterAttention.ts`).

## 0. Outcome and limits

The fleet surfaces - the spine lamps, the fleet board's header meters and its cluster cards - read each cluster's Observer summary through the same attention model the open cluster uses, applied per cluster. The fleet board's per-visit health fan-out goes: `bridge/fleetboard.ts` today calls, per connected cluster and per visit, the GitOps summary, the Argo applications list, the all-namespaces workloads list, the Gateways list and one topology read per Gateway. Utilisation (cpu and memory) is not a health fact and stays a per-visit metrics read.

Not in this slice: removing "not covered" and the qualification of the quiet state (slice 6's parity gate).

## 1. Source

- Every cluster's current summary, from app start: `startSummaries()` moves to the app shell, so the fleet board and the spine read summaries without a lens or a cluster having been opened (subscribe-before-bootstrap, newest-wins, Final and tombstones are the store's, unchanged).
- Per cluster, the model is `clusterAttention(st, cluster, clusterDTO)` - the slice 4 model, unchanged. For the open cluster this includes the rows its lenses hold (the selector's A4 contract), so its card, its lamp, its mast and its tree show one number. Other clusters have no rows; their parts are the summary's.
- No summary yet: the cluster's health is "checking…"; never a count, never quiet.
- Final (`ended`): "observation ended at HH:MM:SS"; no health count stands.
- The connection axis is the fleet DTO's state (Synced, Degraded, Stale, Failed, Unconnected, Connecting), as today - it is not a summary fact, and the two axes are reported apart.

## 2. Spine lamps

One lamp per cluster, two axes kept honest (the Signal Bench rules: only faults glow, blink is spine-only):

| state | lamp | title |
|---|---|---|
| unreachable (Failed, Unconnected) | danger | the connection state and reason |
| areas need attention, any danger | danger, blinking | "N areas need attention: <area lines>" - areas, never objects |
| areas need attention, warning only | warning | as above |
| gaps, not covered, checking, ended | unverified (no glow) | what could not be established, named |
| connection stale or degraded, nothing lit | warning (connection) | the connection reason |
| quiet: read, no gaps, nothing not covered | neutral (no green) | "no attention in N areas read · summary as of HH:MM:SS" |

A cluster whose summary is checking or ended is never shown quiet. The title always names the summary's evaluation time, and for the open cluster each line's own provenance (slice 4 §3).

## 3. Fleet meters (the board header)

Independent facts, each counted as clusters, never objects or areas summed across clusters:

- clusters, connected, unreachable, stale - from the fleet DTOs (unchanged);
- attention: clusters with at least one area needing attention (`mastReading` state attention), toned danger when any of them has a danger area - replacing today's workloads-only count;
- unknown coverage: connected clusters whose model has a gap, a not-covered part, a checking part or an ended observation;
- nodes and pods totals - from the fleet DTOs (unchanged; unknown pods counts stay named).

## 4. Cluster cards

- **Health lines** from the model, each in its area's own vocabulary and never summed across areas or tools: workloads (the workloads line), GitOps (Flux's and Argo's lines side by side - "flux 2 not ready · argo 1 need attention", or "not detected" when neither is expected), Gateway (the gateways and routes lines). Each names its gaps; quiet is neutral, never green.
- **Other areas** needing attention (pods, nodes, certificates, external secrets, Karpenter, Helm, a controller not ready) are listed by name on one line ("also: Certificates - issuers 1 not ready · Nodes - 1 need attention"); evidence (warning events) is never attention.
- **Utilisation** (cpu, memory) stays the metrics read's.
- **Provenance**: the card names the summary's evaluation time ("summary as of HH:MM:SS"), and "checking…" or "observation ended at …" instead of counts when those hold.
- **Ordering** (`clusterDiagnosticScore`): unreachable, then clusters with danger areas, then warning areas, then a stale or degraded connection, then gaps or not covered, then quiet - from the model, not the fan-out; within a band, more areas needing attention first, then name.

## 5. Overview's fleet-board facts

Overview's "argo cd" and "gateway" facts read the model's argo and gateway areas (as its Flux fact already does since slice 4); utilisation stays the metrics read.

## 6. Removals

- `fetchFleetBoard`'s health reads (GitOps summary, Argo applications, workloads, Gateways and topologies) and the `FleetBoardEntry` health fields; the entry keeps cpu and memory only (renamed to what it is, the utilisation read).
- `boardAttention` and the other `fleetBoard`-health helpers in `health/verdict.ts` with no remaining caller.
- Bound service methods with no remaining caller, with their result DTOs where unused: `WorkloadsService.ListWorkloads`, `PodsService.ListPods`, `NodesService.ListNodes`, `GatewayService.ListGateways`, `GitOpsService.GetGitOpsSummary`, `ArgoService.ListApplications`. The connection-level reads they wrap stay (headroom, node and workload metrics, the upgrade check use them). The Events, Helm and Gateway-topology request paths stay (per-fetch lenses, slice 3).

## 7. Tests

- Spine: one fixture per row of the §2 table, plus an open cluster whose rows are newer than its summary (the lamp agrees with the mast).
- Meters: attention counted as clusters with any danger tone named; unknown coverage counted apart from attention; no summary and ended counted as unknown, never quiet.
- Cards: lines from areas (Flux and Argo never summed; gaps named; quiet never green); other lit areas listed; evidence never attention; provenance; checking and ended; ordering bands from the model.
- Overview: the argo and gateway facts from the model.
- No health fan-out: visiting the fleet board calls only the metrics read (spied list endpoints never called); entering a cluster after it calls none either (slice 4's test, kept).
- Go: the removed methods are gone from the bindings; the remaining connection-level reads keep their tests.
- Mounted tests on the real health store, plus mutation checks.

## 8. Order of work

One reviewed commit each:

1. The per-cluster model on the fleet: summaries from app start, spine lamps and the board meters.
2. Cluster cards, their ordering and Overview's facts; the fleet board's fetch reduced to utilisation.
3. The removals (§6) on both sides, with regenerated bindings.
