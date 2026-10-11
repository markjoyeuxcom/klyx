# Klyx redesign: the health model (v2)

Date: 2026-10-10. Owner authorization: full redesign and rebuild, owner veto. Claude implements; Codex reviews. Companion: `2026-10-10-klyx-redesign-layout-nav.md` (presentation; consumes this model). This spec owns classification, aggregation, coverage and freshness.

v2 responds to Codex's review of v1 (REWORK, 12 required changes). v1 assumed caches that do not exist and used overlapping shorthand mappings. v2 narrows phase 1a to domains whose observation paths already exist (Pods, Workloads, Nodes), gives them total first-match decision tables, and records the remaining requirements as binding acceptance criteria for later slices, rather than claiming them.

## Outcomes

- Every verdict, lamp, tone and attention count derives from one Go classification per object. No migrated surface computes health locally or falls back to legacy rank silently.
- A shown number names its entity, scope and verdict set. Equal entity + scope + observation yields an equal number.
- Healthy means operationally healthy. Connection is a separate axis.
- Missing data is never zero: unknown, unreadable, checking and stale are visible.
- Colour follows fault. Zero, capacity, totals and freshness are neutral.

## 1. Verdicts

| Verdict | Meaning | Attention | Presentation |
|---|---|---|---|
| `fault` | Failing now; needs an operator | yes | danger, `▲`, lit lamp |
| `degraded` | Partially serving, overdue or drifting | yes | warning, lit lamp |
| `progressing` | Moving toward desired state within its allowance | no | neutral |
| `paused` | Intentionally stopped (suspend, cordon, explicit scale-to-zero) | no | neutral, outlined lamp |
| `ok` | Healthy | no | dim success lamp, never glows |
| `unknown` | Cannot be determined | no; own count, shown only when > 0 | grey, dashed lamp |

Attention = fault + degraded, shown as `N attention` expanding to `x fault · y degraded`. Unknown is visibility, not severity: a cluster aggregate reports its worst known verdict plus coverage completeness, never folds uncertainty into ok.

Order: fault, degraded, unknown, progressing, paused, ok; then domain severity rank; then name.

Each classified object carries:

```go
type Verdict struct {
    Verdict    string    // enum above
    Label      string    // actionable tool-native headline ("ImagePullBackOff", "Exited 1", "NotReady")
    Reason     string    // machine reason code (stable, testable)
    Message    string    // diagnostic text, full, never truncated by the backend
    Native     string    // underlying native state shown secondarily ("phase Pending")
    Note       string    // history, never headline ("restarted 4m ago", "last OOMKilled 2h ago")
    Rule       string    // rule ID that fired, e.g. "pod.hard-waiting" (explain/debug payload)
    ReclassifyAt *time.Time // next time-threshold crossing that could change this verdict; nil if none
}
```

Thresholds are named constants with tests, clock-injected (`internal/clock`). They are product heuristics, stated as such in the UI tooltip of any verdict they produce.

## 2. Phase 1a decision tables (first match wins)

### Pod

| # | Condition | Verdict | Label |
|---|---|---|---|
| P1 | `deletionTimestamp` set, now ≤ deletion + grace + `stuckTerminating` (5m) | progressing | Terminating |
| P2 | `deletionTimestamp` set, beyond that | degraded | Stuck terminating |
| P3 | phase Succeeded | ok | Completed |
| P4 | phase Failed | fault | `status.reason`, else worst terminated reason, else `Failed` |
| P5 | Unschedulable (PodScheduled=False reason Unschedulable) | fault | Unschedulable |
| P6 | any init/main container Waiting with a non-benign reason | fault | that reason (highest `hardRank`) |
| P7 | any container currently Terminated with exit ≠ 0 that will be restarted (pod restartPolicy Always/OnFailure, or a restartable init sidecar) | fault | terminated reason if set (incl. OOMKilled), else `Exited <code>`. `CrashLoopBackOff` appears only when the Waiting reason says so (P6) |
| P8 | phase Unknown, or phase empty | unknown | Unknown |
| P9 | phase Pending, now − creation ≤ `podStartGrace` (120s) | progressing | benign reason (ContainerCreating, PodInitializing) else Pending |
| P10 | phase Pending beyond grace | degraded | benign reason else Pending, with duration |
| P11 | Running, Ready condition False, now − Ready.lastTransitionTime ≤ `readyGrace` (60s); missing Ready uses status.startTime | progressing | Not ready |
| P12 | Running, Ready False beyond grace | degraded | Not ready, with duration |
| P13 | otherwise | ok | Running |

Notes, never headline: recent termination within 1h ("restarted 4m ago"), historical OOMKilled in lastState. The old `restarts` rank becomes ok + note. Bug fixed: P7 covers the `Terminated: Error` window that `internal/workloads/reason.go:73-86` misses (it only considers OOMKilled), which made a crash-looping pod render "Running". The same evaluator feeds pod DTOs and workload classification; no view-local patch.

### Workload (Deployment, StatefulSet, DaemonSet)

Inputs: the controller object plus its owned pods (already joined in `internal/workloads/assemble.go`).

| # | Condition | Verdict | Label |
|---|---|---|---|
| W1 | any owned pod is P4–P7 (current hard failure) | fault | that pod's label. Generation lag or an active rollout never conceals a current hard failure |
| W2 | Deployment `ReplicaFailure=True`, or `Progressing=False` (incl. ProgressDeadlineExceeded) | fault | condition reason |
| W3 | Deployment/StatefulSet `spec.replicas` explicitly 0 | paused | Scaled to 0 |
| W4 | DaemonSet `desiredNumberScheduled` 0 | ok | 0 eligible nodes (stated, not attention; the selector may be deliberate, e.g. a GPU plugin on a GPU-less cluster) |
| W5 | rollout active within allowance: `generation > observedGeneration`, or updated < desired. Allowance: Deployment until its own progress deadline (W2 takes over); StatefulSet/DaemonSet until the oldest unready owned pod has been unready > `rolloutGrace` (10m) | progressing | Rolling out · n/N updated |
| W6 | StatefulSet `OnDelete` strategy with revisions differing | ok | Update pending (OnDelete), note only |
| W7 | StatefulSet RollingUpdate with `partition` p > 0 and updated ≥ desired − p | ok | Partitioned at p |
| W8 | ready = 0 with desired > 0 | fault | 0/N ready |
| W9 | ready < desired (incl. W5 beyond allowance) | degraded | n/N ready |
| W10 | otherwise | ok | Available (Deployment) / Ready |

Precedence answers Codex review item 4: current hard failure > explicit controller failure > intentional zero > bounded rollout > native strategy states > serving partial > ok. `rankOf` (`assemble.go:202-216`) stays only for legacy consumers until they migrate.

### Node

| # | Condition | Verdict | Label |
|---|---|---|---|
| N1 | Ready condition missing | unknown | Ready not reported |
| N2 | Ready=Unknown | unknown | Kubelet not reporting (sorted directly under faults) |
| N3 | Ready=False | fault | NotReady (+ reason) |
| N4 | Memory/Disk/PID pressure True | degraded | the pressure condition(s) |
| N5 | cordoned (`spec.unschedulable`) | paused | Cordoned |
| N6 | otherwise | ok | Ready |

Cordon is an independent operation fact carried on the DTO; a cordoned NotReady node stays fault, a cordoned pressured node stays degraded. Fixes the legacy rule that treats a missing Ready condition as NotReady (`internal/workloads/nodesummary.go:130-133`).

### Connection axis (no new infrastructure)

| ConnState (`internal/fleet/state.go`) | Connection word | Health asserted? |
|---|---|---|
| Synced | connected | yes |
| Degraded | connected · notes (subsystem notes from `conn.go:441-470`, shown as text) | yes |
| Stale | stale Nm (last-good age) | last-good, dimmed, with as-of; never counted as live incidents |
| Connecting | connecting | no (checking) |
| Unconnected, Failed | unreachable | no (unknown) |
| any other value | unknown | no |

**Amendment (2026-10-11, slice 1c v2.1).** For every surface served by the 1c Observer, the Stale row's "last-good, dimmed" applies per kind, not per cluster. Each watched kind's freshness is its own state machine (1c addendum §3.1). The connection word stays this table's, but it never sets kind freshness. A Stale or unreachable connection reaches each kind through that kind's own recorded errors, and a Degraded connection leaves kinds live. Surfaces not yet on the Observer keep the row as written.

Fleet meters: `clusters`, `connected`, `attention` (clusters with ≥1 known fault/degraded), `unknown coverage` (clusters with gaps). Buckets are allowed to overlap and are labelled as independent facts, not a partition. Fixes FleetView "HEALTHY = Synced" (`fleet/FleetView.tsx:59`), connecting-as-unreachable (`:39`) and the green stale spine lamp (`chrome/FleetSpine.tsx:99`).

### Amendments from Codex's v2 review (approve-with-changes, binding on 1a)

Implemented in `internal/health` (commit a304e88 and the wiring after it); the tables above read with these corrections:

1. **Pod totality.** P13 requires Running and Ready=True. Ready=Unknown is `unknown`. Ready missing is `progressing` ("Starting") within `podStartGrace` from status.startTime (creation as fallback), then `unknown`. Unsupported/empty phase is `unknown`. ReclassifyAt is always a future boundary; a future anchor (clock skew) counts as elapsed zero; a terminal verdict schedules nothing.
2. **Strategy-aware rollouts.** Rollout active = not paused, not OnDelete, and (generation > observedGeneration or updated < update target). The update target counts the ordinals in [ordinals.start, ordinals.start + desired) at or above the partition, clamped to 0..desired. OnDelete, satisfied partitions and paused Deployments never excuse unready replicas: they fall through to the serving rules (0 ready = fault, short = degraded). Strategy states are notes on an ok verdict only when fully serving. DaemonSet OnDelete is handled like StatefulSet OnDelete.
3. **Rollout anchors.** Deployments trust their own Progressing condition only as positive evidence of the current rollout: Status True, an in-flight reason (NewReplicaSetCreated, FoundNewReplicaSet, ReplicaSetUpdated), a timestamp, and observedGeneration ≥ generation. Then within progressDeadline + `RolloutStallGrace` (10m) of its lastUpdateTime = progressing, beyond = degraded "Rollout stalled". A completed (NewReplicaSetAvailable), Unknown or pre-generation condition never starts a stall clock: that path is unanchored (serving = progressing, short = `unknown`). ReplicaFailure/Progressing=False count only when observedGeneration ≥ generation. A Deployment headlines "Available" only when availableReplicas ≥ desired; ready-but-not-yet-available (minReadySeconds) is progressing. StatefulSet/DaemonSet have no anchor in 1a: fully serving = progressing, short = degraded (it is partially serving; pod churn cannot reset anything). A never-observed generation (observedGeneration 0) is `unknown`. A zero DaemonSet is `ok` only when status is current, else `unknown`.
4. **Ownership, not selectors (amended after the 1a code review).** Pods are attributed by controller UID chain only: StatefulSet/DaemonSet pods by the controller's UID; Deployment pods via pod → ReplicaSet UID → the ReplicaSet's controlling Deployment UID (`fleet.ListWorkloads` now also lists ReplicaSets). Names never attribute: an orphaned or independently created `<deployment>-<hash>` ReplicaSet does not attach, an adopted ReplicaSet with any name does. Evidence completeness is explicit (`workloads.Evidence`): a truncated pod list, or a missing/truncated ReplicaSet list for a Deployment, turns an otherwise-ok workload into `unknown` "Unverified" (controller counts stay in Native); known faults still report.
5. **Termination.** deletionTimestamp is the deadline and already includes grace; P1 is deletionTimestamp + `StuckTerminating` (5m), never + grace again.
6. **Deterministic headlines.** P5-P7 are one ranked current-failure rule: every Waiting failure and every current non-zero exit that will restart (pod restartPolicy, or a restartable init sidecar) competes by rank, then init before main, then container name. CrashLoopBackOff (70) outranks an active OOMKilled (60), which outranks image problems (50); a generic exit is `Exited <code>` (35). Workloads pick the worst faulting pod by rank, then namespace, name, UID. `internal/health` imports no Klyx package. Notes require known termination timestamps.
7b. **Fail closed.** An unsupported verdict kind serialises as `unknown`, never `ok`.
7. **Phase honesty.** 1a returns ReclassifyAt but nothing schedules it yet; until 1c, every surface is per-fetch and labels its as-of time. Connection state alone never lights a healthy lamp. Unmigrated domains keep their legacy counts, labelled separately, never summed into new verdict totals. 1b needs its own review.

## 3. Phase 1a/1b delivery

**1a backend.** New pure package `internal/health` (pod, workload, node evaluators; constants; clock). `internal/workloads` calls it for both pod summaries and workload rows. Additive DTO field on pods, workloads and nodes: one nested `health` object (`kind`, `label`, `reason`, `message`, `native`, `note`, `rule`, `rank`, `reclassifyAtUnix`); node cordon stays the existing `unschedulable` field. Legacy fields stay until every consumer migrates. Tests: one table test per rule row above plus Codex's named fixtures: current Error exit; recovered restart; Never-policy failed pod; completed Job pod; init failure; restartable sidecar failure; active deletion and stuck deletion; long startup; Ready transition on an old pod; new healthy rollout; stuck rollout (Deployment deadline; STS beyond grace); explicit zero-scaled Deployment; zero-eligible DaemonSet; generation lag with a current hard pod failure; OnDelete; partition; cordoned pressured node; cordoned NotReady node; Ready missing; Ready Unknown; fake-clock advance across each threshold with no object change. Gates: `go test -race ./internal/...`, vet, golangci-lint, gofmt, govulncheck.

**1b frontend.** `src/health/` maps verdict to tone/lamp/order and computes `attentionCount` from verdicts. Migrated in this phase: Pods, Workloads, Nodes lenses and inspectors; LensNav pods/workloads counts; fleet board workload counts and ClusterCard; FleetView meters and FleetSpine lamp (connection axis). Capacity (`NodesView.tsx:872-884`) becomes neutral; utilization keeps `saturation.ts` labelled "used". Delete in migrated surfaces: `rankDot`/`rankTone` copies, `nodeNeedsAttention`, the workloads/pods/nodes math in `bridge/overview.ts` and `bridge/fleetboard.ts:55`. A migrated surface given a DTO without `verdict` shows `unknown`, never legacy rank. Tests: verdict mapping, attentionCount, zero-neutral, scope labelling. Gates: tsc, eslint, vitest, vite build.

Scope honesty during migration: until slice 1c lands, counts are per-fetch (existing list paths), labelled with their scope; the tape keeps running for unmigrated domains and reads migrated domains from verdicts. No surface claims full cluster health.

Acceptance on the seeded kind cluster (namespace `shop`, each failure owned by a different Deployment: `api` ErrImagePull, `batch` Unschedulable on its new ReplicaSet, `worker` crash-looping; `web` healthy ×3): pods report `3 fault`; workloads report `api` fault, `worker` fault, `batch` per W1 (fault: its new pod is Unschedulable) — 3 workloads, not double counted with pods; the worker pod never reads Running; the inspector headline equals the list label; the node shows no amber; fleet shows `1 connected · 1 attention`; no green zero anywhere. Screenshots of fleet, pods list + inspector, workloads, nodes.

## 4. Slice 1c: cluster health summary (requirements, not yet designed)

Binding requirements from Codex review items 1, 2, 3, 9, 11, to be designed in a 1c addendum and reviewed before code:

- Observation sources named per domain. Eager pods are metadata-only (`internal/fleet/conn.go:305-306`); typed Pod/Deployment/StatefulSet/DaemonSet caches, Argo and Karpenter informers are new infrastructure. Helm is a bounded CLI snapshot with explicit cadence, never live, never Secret payloads.
- Health owns cluster-lifetime watch leases independent of lens subscriptions (today `GitOpsService.Close` tears down caches, `appbridge/gitops_service.go:108-117`). Reconnect generation, removal cleanup, capability appear/disappear, idempotent start, no leaked callbacks.
- Revisioned publication: subscribe-before-bootstrap, monotonic per-cluster revision + connection generation, stale-event rejection, bounded throttle with a maximum delivery delay and guaranteed terminal publish. Rows, details and summaries carry observation revision; differing revisions display "updating", not conflicting numbers. Details carry UID/generation.
- Local time-driven reclassification from `ReclassifyAt` (no API polling). `evaluatedAt` separate from last-good observation; three distinct "stale" meanings named separately (condition-generation lag, cache staleness, ESO refresh overdue).
- Coverage schema: per-domain and per-kind subcoverage (live/stale/checking/unreadable/absent), totals, scope, truncation, timestamps, revision; zeros omitted only under complete coverage; Top refs carry cluster/GVR/namespace/name/UID/verdict/route. Cluster "ok" requires every applicable domain covered with no unknown verdicts; a fault is reportable despite gaps.
- Watch truth and budget: per-informer sync/error/recovery; authorization failure vs absent CRD; no "controller down" from client permission errors. Documented watch counts (≈13 dynamic per fully equipped cluster before Argo/Karpenter/workload caches, ≈78+ for six clusters), staggered start, pagination, managedFields stripped, indexed pod→workload joins. Six-cluster fake-client load test and a 5000-pod measurement before claiming cost solved.

TriageTape is deleted only after 1c demonstrates coverage parity (layout spec section 4).

## 5. Domain slices after 1c (requirements recorded)

Each gets its own decision table, reviewed before code, with the acceptance fixtures listed:

- **Flux** (review item 6): no "2× interval" budget. Use per-kind timeout/retryInterval/Stalled/observedGeneration; Ready=False dominance justified; suspended exposes last result secondarily; never time Reconciling from ReadySince. Parser fields to add are specified there. Sources map to a Sources task scope and never inflate Reconciliation counts.
- **cert-manager** (item 7): first issuance vs failed Request/Order/Challenge; expired is fault even while renewing; warning from actual renewalTime/validity, not a fixed 14d; missing NotAfter is unknown.
- **ESO** (item 7): overdue only for periodic enabled refresh (respect refreshPolicy CreatedOnce/OnChange, interval 0, never-refreshed); Ready=False dominates age; store failures are separate scoped facts.
- **Argo** (item 8): full sync × health × operation table incl. OutOfSync+Suspended, Synced+Unknown, operation Error/Failed, ApplicationSets or explicit exclusion. Any change from `App.Broken` documented.
- **Karpenter, Helm** (item 8): launch vs failed timers and NodePool conditions; Helm deployed/superseded/uninstalled/pending deadlines/missing CLI or RBAC; historical releases never fault.
- **Controllers and task scopes** (item 10): controller fault is its own issue identity with its own entity label, never +1 on an object count; network controller health keeps its current coverage; explicit task scopes (Sources, Issuers/Stores, Gateway, generic resource views) with header count == same-scope filter count.

## Decision record

- Verdicts computed in Go so every consumer (UI, AI bundle) agrees; the frontend only presents.
- Events never drive lamps: Warning is evidence.
- Paused states (suspend, cordon, explicit zero) are never attention; a paused object still surfaces an independent current failure where its table says so.
- Restarts and historical OOM are notes, not attention.
- v1 → v2: narrowed phase 1 to observed domains; summary infrastructure and other domains are designed and reviewed separately before code.
