# Klyx redesign: the health model

Date: 2026-10-10. Owner authorization: full redesign and rebuild, owner veto. Claude implements; Codex reviews. Companion spec: `2026-10-10-klyx-redesign-layout-nav.md`, which owns presentation and consumes this model. This spec owns classification, aggregation, coverage and freshness. It is ranked first because the live review (seeded kind cluster, 2026-10-10) showed one cluster reporting the same pod faults as 2, 3 and "1 HEALTHY" at the same moment.

## Outcomes

- Every count, lamp, tone and verdict word in the UI derives from one classification per object. No view computes health locally.
- A number shown anywhere names its entity and its verdict set, so two numbers never disagree silently.
- "Healthy" means operationally healthy. Connection state is a separate axis and never borrows health words.
- Missing data is never zero. Unknown, unreadable, not-yet-loaded and stale are visible states.
- Colour follows fault. Zero, capacity, freshness and success-by-default carry no warning or success glow.

## 1. Verdicts

One closed set, used by every domain. The tool's own vocabulary stays as the label; the verdict drives tone, shape, ordering and counting.

| Verdict | Meaning | Counted as attention | Tone / shape |
|---|---|---|---|
| `fault` | Failing now; needs an operator | yes | danger, `▲` prefix, lit lamp |
| `degraded` | Serving partially or drifting; needs a look | yes | warning, lit lamp |
| `progressing` | Moving toward desired state within expectations | no | neutral, unlit lamp, may animate nothing |
| `paused` | Intentionally stopped (suspend, cordon) | no | neutral, outlined lamp |
| `ok` | Healthy | no | dim success lamp, never glows |
| `unknown` | Cannot be determined (unreadable, condition Unknown, controller down) | separate "unknown" count | grey, dashed lamp |

Attention = `fault` + `degraded`. Headers show `N attention` and expand to `x fault · y degraded`. `unknown` is never folded into attention or ok.

Ordering (broken-first everywhere): fault > degraded > unknown > progressing > paused > ok, then by domain rank (e.g. `hardRank`), then name.

## 2. Domain mappings

Implemented once in Go (`internal/health`, pure, table-tested) and applied to every DTO as `verdict` + `verdictLabel` + `verdictReason`. The label is the actionable tool-native word; the underlying phase/state is secondary text.

| Domain | fault | degraded | progressing | paused | unknown | Label rule |
|---|---|---|---|---|---|---|
| Pod | hard reason (`worstPodReason`), phase Failed, Unschedulable, **current terminated non-zero exit with restartPolicy≠Never** | Running but not Ready > 60s | ContainerCreating / PodInitializing / Pending ≤ 60s without hard reason | - | phase Unknown | reason if any, else phase. Inspector uses the same label (fixes ErrImagePull→"Pending") |
| Workload | rank unhealthy | rank degraded | rollout in progress (generation > observedGeneration, or updated < desired) | replicas 0 by intent | - | controller reason |
| Node | NotReady | Memory/Disk/PID pressure | - | cordoned (unschedulable) | Ready=Unknown | condition name |
| Flux (KS/HR/sources) | Ready=False, not suspended | Reconciling longer than 2× interval | Reconciling within budget | suspended | Ready=Unknown / missing | Flux words: ready / reconciling / failed / suspended |
| Argo app | health Degraded or Missing | OutOfSync (health ok) | health Progressing | health Suspended | health Unknown | Argo words: synced / outofsync / degraded / progressing |
| cert-manager Certificate | Ready=False | expires within 14d and not renewing | Issuing | - | Unknown | cert-manager words |
| ESO ExternalSecret | Ready=False | refresh stale (`now-refreshTime > 2×interval`, existing rule) | - | - | Unknown | ESO words |
| Helm release | failed | - | pending-install / pending-upgrade / pending-rollback / uninstalling | - | unknown status | helm status |
| Karpenter NodeClaim/NodePool | existing stuck-chain fault | - | launching | - | Unknown | Karpenter words |
| Controller (capability) | detected but controller not healthy → one `fault` in that domain | - | detection pending | - | unreadable | "controller down" |

Events are never a verdict. Warning events are evidence surfaced in inspectors and the Events lens; they do not light lamps or enter attention counts. The "restarts" rank (recent termination, now running) becomes `ok` with a "restarted Nm ago" note, not attention.

Rules removed by this mapping (each is a current inconsistency): Pods attention including restarts (`PodsView.tsx:281`); Flux not-ready including Reconciling (`internal/fleet/gitops_summary.go:70`); LensNav pods counting unhealthy only (`LensNav.tsx:378`); degraded workloads called "critical" (`ClusterCard.tsx:275`); Suspended as warning (`GitOps.tsx:757`, `ArgoView.tsx:35`); cordon counted in tape but not NodesView (`overview.ts:59` vs `NodesView.tsx:61`).

## 3. Two axes per cluster

**Connection** (from `ConnState`): `connected` (Synced), `connecting`, `stale` (last-good data, with age), `unreachable` (Failed/Unconnected). Words: connected / connecting / stale Nm / unreachable. Never "healthy".

**Health** (from domains): worst verdict across domains plus per-domain counts. Only asserted when connected or stale; stale health is shown dimmed with its as-of time. Unreachable clusters have health `unknown`.

Fleet meters become: `clusters`, `connected`, `attention` (clusters with any fault/degraded), `unknown`. The fleet spine lamp uses the cluster health verdict (blink rule unchanged: fault only). Fixes `FleetView.tsx:59` (Synced counted healthy), `:39` (connecting counted unreachable) and `FleetSpine.tsx:99` (stale shown green).

## 4. The cluster health summary (backend)

New `internal/health` package (pure classification) and `HealthService` in `internal/appbridge`:

```go
type DomainSummary struct {
    Domain    string            // pods, workloads, nodes, flux, argo, certs, eso, helm, karpenter
    Coverage  string            // live | stale | checking | unreadable | absent
    AsOf      time.Time
    Counts    map[Verdict]int   // every verdict present, zeros omitted
    Top       []ObjectRef       // up to 5 worst, for the attention popover
    Error     string            // when unreadable
}
type ClusterHealth struct {
    Cluster    string
    Connection string
    Verdict    Verdict          // worst across live/stale domains; unknown if none readable
    Domains    []DomainSummary
}
func (s *HealthService) ClusterHealth(cluster string) (ClusterHealth, error)
func (s *HealthService) FleetHealth() ([]ClusterHealth, error)
```

- Computed from the informer caches the cluster already holds. For lazily watched domains (Flux, Argo, cert-manager, ESO, Karpenter) the health service starts the watch when the capability is detected, so coverage no longer depends on which lens was opened. `checking` until first sync.
- Pushed, not polled: a debounced (≤1 per 2s per cluster) Wails event `health:changed` carries the new `ClusterHealth`. The frontend stops fetching full pod/event/workload lists to count them (`bridge/tape.ts:24-32`, `bridge/overview.ts:164-173`).
- Controller-down feeds the domain verdict. `internal/fleet/conn.go:441-470` Degraded no longer drives health; it stays a connection-tier note.
- Entity scope is explicit: workloads and pods are separate domains and are never summed. The workloads group attention is the workloads count, with pods shown as its own line.

## 5. Frontend contract

- One module `src/health/` exports `verdictTone`, `verdictLamp`, `verdictOrder`, `attentionCount(summary)`, and a `useClusterHealth(cluster)` hook backed by a store slice keyed by cluster and fed by `health:changed`.
- Delete local derivations: `rankDot`/`rankTone` (PodsView, WorkloadsView), `fluxTone`, Argo `syncColor`/`healthColor` tone use, Helm `statusColor`/`statusDot`, Karpenter `readyColor`, `readyColor` in `primitives/styles.ts`, `nodeNeedsAttention`, `bridge/overview.ts` summarise math, `bridge/fleetboard.ts:55` broken math. `StatusDot` takes a verdict, not a CSS-variable string.
- Lists sort by `verdictOrder`. Broken filters are `attention` (fault+degraded); an `unknown` filter appears only when unknown > 0.
- Numbers: zero renders neutral. Meters that are not verdict counts (capacity, allocatable, freshness, totals) never take warning/success tones. `CapacityCell` (`NodesView.tsx:872-884`) shows allocatable/capacity as a neutral fact; utilization keeps `saturation.ts` thresholds (it is a real pressure signal) but is labelled "used".
- Every attention number has one source: the lens header, LensNav group count, mast attention button, fleet spine and fleet card all read `ClusterHealth`. A lens header may additionally show filter-scoped counts, labelled with the scope ("in shop").

## 6. Bugs fixed by phase 1 (regression tests required)

1. Crash-looping container in `Terminated: Error` shows no reason and renders "Running" (`internal/workloads/reason.go:73-86` only considers OOMKilled terminations). Treat current terminated non-zero exit, restartPolicy≠Never, as `fault` with label `CrashLoop` (or the terminated reason).
2. Pod inspector headline uses `phase || rank` (`PodsView.tsx:849`) while the list uses `reason || phase` (`:529`). Both use `verdictLabel`.
3. Allocatable/capacity bar amber at 100% (`NodesView.tsx:881`): polarity inverted; becomes neutral.
4. Fleet "HEALTHY" = Synced; connecting counted unreachable; stale lamp green (section 3).
5. Pods tab 2 vs filter 3 vs tape 3 (three definitions, namespace-scoped vs all-namespace): one domain count.
6. Flux Reconciling double-counted as attention and info (`GitOps.tsx:53,260-261`).
7. Argo tone drift across LensNav/tape/Overview/ArgoView.
8. Fleet workload bar "3 bad" with no denominator: becomes a count with entity, no pseudo-progress bar; freshness becomes a word + age, not a bar.

## 7. Delivery

Phase 1a (backend): `internal/health` with table tests per domain row above, the reason.go fix, verdict fields on DTOs (additive; old fields kept until consumers migrate), `HealthService` + event push, lazy-watch start on capability detection. Gates: `go test -race`, vet, golangci-lint, govulncheck.

Phase 1b (frontend): `src/health/`, store slice, switch LensNav, FleetSpine, FleetView, ClusterCard, PodsView, NodesView to it; TriageTape reads `ClusterHealth` until the layout migration deletes it (layout spec section 4). Gates: tsc, eslint, vitest (verdict mapping, attentionCount, zero-neutral), vite build.

Acceptance on the seeded kind cluster (`shop` namespace: ErrImagePull, Unschedulable, CrashLoop, 3 healthy): every surface reports the same `3 attention (3 fault)` for pods and `3 attention` for workloads; the crash-looping pod never reads Running; the fleet shows `1 connected · 1 attention`; the node shows no amber; no green zeros. Verified by screenshot of fleet, pods (list + inspector), nodes, and the palette.

## Decision record

- Verdicts are computed in Go so every consumer, including the AI bundle and any future CLI, agrees. The frontend only maps verdict to presentation.
- Events never drive lamps (agreed with Codex, round 2): Warning is evidence, not state.
- Suspended/paused and cordoned are intentional and never attention (extends the 2026-07-02 Argo ruling to every domain).
- "Restarts" is history, not attention.
- Thresholds introduced here (60s not-ready grace, 2× interval reconciling budget, 14d certificate window) are named constants in `internal/health`, each with a test, and adjustable later from config if demand appears.
