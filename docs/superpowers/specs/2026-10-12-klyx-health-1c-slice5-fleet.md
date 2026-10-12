# Klyx health 1c, slice 5: the fleet on summaries

Status: Codex APPROVE-WITH-CHANGES (2026-10-12); amended below per its four required changes (§1, §2, §4) and its removal and startup notes (§1, §6). Parent spec: `2026-10-11-klyx-health-1c-cluster-summary.md` (§8, §9 slice 5). Slice 4 (`2026-10-12-klyx-health-1c-slice4-summary.md`) is closed: the open cluster's mast, tree and Overview read the cluster's attention model (`nav/clusterAttention.ts`).

## 0. Outcome and limits

The fleet surfaces - the spine lamps, the fleet board's header meters and its cluster cards - read each cluster's Observer summary through the same attention model the open cluster uses, applied per cluster. The fleet board's per-visit health fan-out goes: `bridge/fleetboard.ts` today calls, per connected cluster and per visit, the GitOps summary, the Argo applications list, the all-namespaces workloads list, the Gateways list and one topology read per Gateway. Utilisation (cpu and memory) is not a health fact and stays a per-visit metrics read.

Not in this slice: removing "not covered" and the qualification of the quiet state (slice 6's parity gate).

## 1. Source

- Every cluster's current summary, from app start: `startSummaries()` is called once from the app root (it is idempotent), so the fleet board, the spine and Settings read summaries without a lens or a cluster having been opened (subscribe-before-bootstrap, newest-wins, Final and tombstones are the store's, unchanged). The boot is tested on the fleet and Settings routes.
- Per cluster, the model is `clusterAttention(st, cluster, clusterDTO)` - the slice 4 model, unchanged. For the open cluster this includes the rows its lenses hold (the selector's A4 contract), so its card, its lamp, its mast and its tree show one number. Other clusters have no rows; their parts are the summary's.
- Parts are answered as in slice 4 §1: current-incarnation rows answer their parts before any summary (their counts and faults show on the fleet too); only an unanswered part is "checking…" - never a count, never quiet. A newer incarnation never inherits an older summary's time or namespace count (the store drops the older summary on the newer incarnation).
- Final (`ended`): "observation ended at HH:MM:SS"; no health count stands.
- The connection axis is the fleet DTO's state (Synced, Degraded, Stale, Failed, Unconnected, Connecting), as today - it is not a summary fact, and the two axes are reported apart.

## 2. Spine lamps (review 4)

One lamp per cluster. Its tone is the FIRST matching rule (the Signal Bench rules: only faults glow, blink is spine-only):

1. unreachable (Failed, Unconnected): the unpowered socket (dashed ring);
2. an area needs attention with danger: danger, blinking;
3. an area needs attention, warning only: warning;
4. the connection is Stale or Degraded: warning (the connection's);
5. the connection is Connecting: unverified (no glow) - a transition never implies health, quiet or otherwise;
6. any gap, not-covered part, checking part, or an ended observation: unverified (no glow);
7. otherwise - connection Synced and every expected part read with no gap: the healthy lamp (Signal Bench's dim, unlit lamp).

Whichever rule wins, the title keeps both axes: the connection state and its reason, AND the health reading - the areas needing attention with their lines, or the gaps and not-covered parts named, or "checking…", or "observation ended at …" - each line with its own provenance (§4). A fault never erases a stale or connecting connection's explanation, and a connection problem never erases the last health reading (an unreachable cluster's title names its last reading and when).

## 3. Fleet meters (the board header)

Independent facts, each counted as clusters, never objects or areas summed across clusters:

- clusters, connected, unreachable, stale - from the fleet DTOs (unchanged);
- attention: clusters with at least one area needing attention (`mastReading` state attention), toned danger when any of them has a danger area - replacing today's workloads-only count;
- unknown coverage: connected clusters whose model has a gap, a not-covered part, a checking part or an ended observation;
- nodes and pods totals - from the fleet DTOs (unchanged; unknown pods counts stay named).

## 4. Cluster cards (review 1, 2, 3)

- **Health lines** from the model, each in its area's own vocabulary and never summed across areas or tools: workloads (the workloads line), GitOps (Flux's and Argo's lines side by side - "flux 2 not ready · argo 1 need attention"), Gateway (the gateways and routes lines). Quiet is neutral, never green.
- **Every expected area's coverage**, lit or not: each area with a gap, a checking or unreadable part, an unknown native role, an unknown controller, or a not-covered part is named on the card ("Certificates: could not be read (forbidden)", "Karpenter: NodeClaims 1 unknown", "Argo CD: ApplicationSets not covered by the summary"), and so is an ended observation. A tool reads "not detected" only on confirmed absence (capability absent and no non-absent summary part); a missing expected area or part reads checking or not covered, never "not detected". Every cluster the header counts as unknown coverage names why on its card.
- **Other areas** needing attention (pods, nodes, certificates, external secrets, Karpenter, Helm, a controller not ready) are listed by name; evidence (warning events) is never attention.
- **Provenance per line**: each health line carries its selected part's provenance - lens rows or cluster summary, with that part's own time - and its updating, refreshing, resync and re-read-failure state, through the model's `provenance` and `lineText` (no second count formatter). A row-derived count is never captioned with an older summary's time. The card shows "checking…" or "observation ended at …" instead of counts when those hold.
- **Utilisation**: the cards show none (they never rendered the fleet board's cpu and memory); Overview's utilisation is its own metrics read, unchanged.
- **Ordering** (`clusterDiagnosticScore`): unreachable, then clusters with danger areas, then warning areas, then a stale or degraded connection, then gaps or not covered, then quiet - from the model, not the fan-out; within a band, more areas needing attention first, then name.

## 5. Overview's fleet-board facts

Overview's "argo cd" and "gateway" facts read the model's argo and gateway areas (as its Flux fact already does since slice 4); utilisation stays the metrics read.

## 6. Removals

- `fetchFleetBoard`, `FleetBoardEntry` and the store's `fleetBoard` slice, entirely. Adjusted during implementation with Codex's agreement: no surface read the entry's cpu and memory - Overview's utilisation, sparklines and metrics refresh are its own metrics read and stay - so a utilisation-only entry would be unused fetching and state.
- `boardAttention` and the fleet-board observation helpers in `health/verdict.ts` (`observe`, `describeObservation`, `asOfText`, `VerdictObservation`), with no remaining caller.
- Bound service methods with no remaining caller, with their result envelopes where unused: `WorkloadsService.ListWorkloads`, `PodsService.ListPods`, `NodesService.ListNodes`, `GatewayService.ListGateways`, `GitOpsService.GetGitOpsSummary`, `ArgoService.ListApplications`. The connection-level reads they wrap stay (headroom, node and workload metrics, the upgrade check use them), and so do the detail and verb endpoints, the converter field helpers and their identity, serialization and parity tests. The Events, Helm and Gateway-topology request paths stay (per-fetch lenses, slice 3).
- Found during implementation, with no remaining caller once the above went: the connection-level `GitOpsSummary`/`GitOpsSummaryFlux` and `ListGateways` (the Observer reads Gateways through `healthsrc`), the list methods on the `fleet.Conn` interface (the `ClusterConn` reads stay for the internal users above), the store's `setArgo`/`setArgoLoading` and `setArgoApplicationSets`/`setArgoApplicationSetsLoading` setters with their result types (the Argo rows payload feeds the slice), and the frontend result envelopes `WorkloadsResultDTO`, `PodsResultDTO`, `NodesResultDTO`, `GatewayListDTO`.

## 7. Tests

- Spine: one fixture per first-match rule; cross-axis fixtures - stale plus a fault, stale plus gaps, degraded plus an unknown controller, connecting with a retained quiet reading (never the healthy lamp), unreachable with a last-good attention reading - each title keeping both axes; an open cluster whose rows are newer than its summary (the lamp agrees with the mast).
- Lifecycle on the fleet: optional fault rows before any summary (shown, with the rows' provenance); staggered parts after a Final and a newer incarnation (no inherited time or namespaces).
- A4 on the fleet: the card, the mast and the tree agree on both sides of the deadline - first-second rows (updating), the deadline switch to the summary (refreshing), a failed re-bootstrap (named) and recovery; never a row count with the summary's time.
- Meters: attention counted as clusters with any danger tone named; unknown coverage counted apart from attention; no summary and ended counted as unknown, never quiet; each unknown-coverage cluster's card names why.
- Cards: lines from areas (Flux and Argo never summed; quiet never green); quiet workloads beside unreadable Certificates; unknown native NodeClaims; a missing expected Argo part; mixed Flux absent versus Argo unverified ("not detected" only for the confirmed absence); other lit areas listed; evidence never attention; checking and ended; ordering bands from the model.
- Overview: the argo and gateway facts from the model.
- Boot: summaries started once from the app root, on the fleet and Settings routes.
- No health fan-out: visiting the fleet board calls only the metrics read (spied list endpoints never called); entering a cluster after it calls none either (slice 4's test, kept).
- Go: the removed methods are gone from the bindings; the remaining connection-level reads, detail and verb endpoints keep their tests.
- Mounted tests on the real health store, plus mutation checks.

## 8. Order of work

One reviewed commit each:

1. The per-cluster model on the fleet: summaries from app start, spine lamps and the board meters.
2. Cluster cards, their ordering and Overview's facts; the fleet board's fetch reduced to utilisation.
3. The removals (§6) on both sides, with regenerated bindings.
