# Klyx health slice 1c: the cluster health summary (design addendum, v2.1)

Date: 2026-10-11. Status: APPROVED by Codex (v2.1, confirmed against 1adaa7f). v2.1 applies the four required amendments, A1-A4: part versions in §4, the reflector restart barrier in §3.1, route identity in §1.3, and the mismatch fallback in §4. It also applies the optional clarifications. Slice 1 may start once these are confirmed. Parent: `2026-10-10-klyx-redesign-health-model.md` §4 (binding requirements R1-R6). Consumer: `2026-10-10-klyx-redesign-layout-nav.md` §4 (fault discovery). The step 7e frontend summary is what this replaces.

v1 (e92a9ca) got REWORK with eight required changes (C1-C8). The Observer direction and a budget of about 190 watches were accepted in principle. v2 changes the publication, coverage and acceptance contracts. Each section names the change it answers.

## 0. Outcome and limits

- **Coverage.** Every connected cluster has one backend observation, live from watches. It is not polled and does not depend on which lens is open. Every installed task and task part is covered or explicitly qualified (checking, stale, unreadable).
- **One number per task.** Where a task's rows are on screen, every count for that task comes from the rows' own observation. Where they are not, it comes from the summary. Lenses that still read per fetch (Helm and Events snapshots, the Gateway topology's data-path lanes) keep explicit as-of qualifications. The final acceptance states that limitation; it does not claim "never two numbers" universally (C7).
- **No new domain semantics.** Verdict domains use `internal/health`. The other domains keep their current lens rules, moved into Go with golden parity (§2). Changing a domain's policy belongs to its own health spec §5 table.

## 1. Observation sources: tasks, parts and dependencies (R1, C4, C8)

A **task** is a navigable area (one destination). A **part** is a counted entity set inside it with its own rule, scope and coverage. A **dependency** is a watched kind that a part's rule reads but does not count. Object, attachment and controller identities are separate (§1.3).

| Task | Part (entity, scope) | Kinds counted | Dependencies | Rule |
|---|---|---|---|---|
| Workloads | workloads (Deployments, StatefulSets, DaemonSets; all namespaces) | deployments, statefulsets, daemonsets | pods, replicasets (metadata) | verdict `health.Workload`, UID-chain ownership |
| Pods | pods (all namespaces) | pods | - | verdict `health.Pod` |
| Nodes | nodes | nodes | - | verdict `health.Node` |
| Events | warning events (all namespaces) | events: 60s snapshot, `type=Warning`, `Limit 500` | - | evidence only |
| Helm | releases (all namespaces) | `helm list -A --max 0`: 5m snapshot, after a Klyx Helm operation, and on lens Refresh | - | native (relocated `helmHealth.ts`) |
| Flux | reconciliations (Kustomization, HelmRelease) | kustomizations, helmreleases | - | native (`GitOps.tsx:54` `needsAttention` / `fluxHealth`) |
| Flux | sources | gitrepositories, ocirepositories, buckets, helmrepositories, helmcharts | - | native (`GitOps.tsx:249` `sourceNeedsAttention`) |
| Argo CD | applications | applications | - | native (`App.Broken`) |
| Argo CD | ApplicationSets | applicationsets | - | native (`appSetConditionBroken`) |
| Certificates | certificates | certificates | - | native (`CertsView` `needsAttention` / `certHealth`) |
| Certificates | issuers (Issuer + ClusterIssuer) | issuers, clusterissuers | - | native (`issuerHealth`; inclusion `ready !== Ready`) |
| External secrets | external secrets | externalsecrets | - | native (`ExternalSecretsView` `needsAttention` / `secretHealth`) |
| External secrets | stores (SecretStore + ClusterSecretStore) | secretstores, clustersecretstores | - | native (`storeHealth`; inclusion `state !== Synced`) |
| Gateways | Gateways | gateways | gatewayclasses | native (`gatewayModel.gatewayHealth`) |
| Gateways | route attachments (HTTPRoute × parent Gateway) | httproutes | services (metadata), endpointslices (slim) | native (`gatewayModel.routeIssues` / `routeHealth`, primary backend as today) |
| Karpenter | NodeClaims | nodeclaims | - | native (`claimHealth`) |
| Karpenter | NodePools | nodepools | - | native (`poolHealth`) |

A failed, unused Issuer or store, an ApplicationSet failure, a failing Flux source and a healthy Gateway with a broken route each light a discoverable part. None of them is merely a join dependency.

### 1.1 Snapshots

Helm and Events are never live. Each has a cadence, a single-flight read and a named deadline: `HelmReadTimeout` 60s, `EventsReadTimeout` 30s. A hung read is cancelled through its context and records class `timeout`, so single-flight never leaves a part checking indefinitely. A missing `helm` binary is `unreadable` with class `cliMissing`, never `absent`. Helm output carries no values, and Klyx surfaces no Secret payloads. A capped Events read shows `≥500` and its as-of time. It never yields a quiet claim.

### 1.2 Projections, not field drops (C8)

Each model entry is a pruned copy built by the transform, and the decoded object is never written. The reflector that decoded it is health-owned (§3), so nothing else shares it.

**Pods** keep:

- metadata: name, namespace, UID, labels, ownerReferences, creation and deletion timestamps, deletion grace;
- spec: nodeName, restartPolicy, and per-container name, image, restartPolicy and resources;
- status: complete.

**Workloads** keep:

- metadata: name, namespace, UID, labels, generation;
- spec: replicas, selector, strategy, paused, progressDeadlineSeconds, minReadySeconds, ordinals and updateStrategy;
- the template's container names and images;
- status: complete.

Each workload entry also stores its Reloader targets, `reloadTargets(annotations, podSpec)` (`assemble.go:193`, `reloader.go:89-110`), precomputed from the raw object at transform time.

Workload resources keep today's derivation from matched pods (`assemble.go`), never template totals. The two differ after admission and at zero replicas.

Workload DTO assembly takes the precomputed Reloader targets instead of the raw annotations and pod spec. Selector-matched pod facts keep working: pod labels are retained, and the selector is kept.

**Services** are metadata-only (existence). An **EndpointSlice** keeps its service-name label, address type and, per endpoint, the ready condition and targetRef kind. Addresses are dropped; `routeIssues` reads counts only. **ReplicaSets** and **namespaces** are metadata-only.

**Parity gate.** Raw-versus-projected fixtures run every current consumer on both forms and require identical output:

- pod DTO, workload DTO including Reloader, resources, images and selector-matched pods, Gateway route health;
- `internal/health` evaluation (verdict, label, rule, ReclassifyAt).

This gate passes before 1c-2 evaluates from projections and before 1c-6 serves rows from them. Memory measurements (§7) use fixtures with realistic label sets (15 labels) and long condition messages, so status and label retention is measured, not assumed small.

### 1.3 Identity and deduplication (C4)

**Objects** are counted once per part by UID.

**Routes (A3).** Attachments are internal identities: (route UID, parent Gateway namespace/name). An attachment exists for each parentRef whose Gateway is in the model, as the topology builds lanes today. The counted entity is the route object, once per UID:

- its Role is the worst role across its attachments;
- it is Included if any of its attachments is included;
- Total and the role partition count routes, not attachments.

The Top ref carries the route plus its worst attachment's parent (ties broken by parent namespace/name), and the lens row lists every attachment. A route with one healthy and two failing parents counts once, with the worse failing role, and names that parent.

Removing a parent Gateway drops that attachment and re-derives the route. A route whose only parent is gone is not counted, which is today's behaviour, carried verbatim; dangling parentRefs belong to the Gateway domain table. Recreating the parent restores the attachment under the same identity, with progressing or unknown roles until its controller writes status.

The Gateways lens gains a Routes part view whose header and filter count equal the part's `Included`. Today the lens header counts Gateways only (`GatewayApiView.tsx:74`).

**Controller issues** are a task-level field, `Controller{State, Reason}`, never added to a part count. Today they come from the capability tiers; after 1c the 5m re-probe keeps them current. A task can be lit by its controller alone: "controller not ready", with no object number.

Parts are never summed into one task number. The mast counts lit tasks.

## 2. Relocated native rules, verbatim, with golden parity (C3)

Each lens currently has two separate functions: an **inclusion predicate** (what its attention filter and header count) and a **presentation role** (fault, degraded, progressing, paused, unknown, ok, plus a label). They disagree on purpose. For example, a cert-manager Certificate that is Issuing is included by `needsAttention` but has the progressing role. v2 keeps both, separately, per object:

```go
type NativeEval struct {
	Included bool     // the lens's inclusion predicate, verbatim
	Role     string   // the lens's presentation role, verbatim
	Label    string
	Next     *time.Time // next time boundary the rule depends on, if any
}
```

The rules move into the domain's pure Go package (`internal/certmanager`, `internal/externalsecrets`, `internal/gwapi`, `internal/karpenter`, `internal/helmcli`, `internal/gitops/flux`; Argo's rules are already Go). The lens DTO carries `included`, `role` and `label`, and the lens deletes its local copy. The time boundaries are:

- certificate NotAfter;
- ESO refreshTime + 2×refreshInterval;
- NodeClaim creation + 15m;
- Helm pending deadline.

**Golden parity.** One fixture file per rule (`testdata/<domain>-rules.json`: input DTO, expected `Included`, `Role`, `Label`, `Next`) is executed by the Go test and by a vitest harness against the current TypeScript function before the lens switches. After the switch, the TypeScript copy is deleted. The fixtures stay as the Go regression set, and a lens test asserts that the header count, the filter count and the summary part's `Included` count are equal at one watermark.

Per part, the summary carries:

```go
type NativeCounts struct {
	Total    int
	Roles    Counts // complete partition of Total by role: fault, degraded, unknown, progressing, paused, ok
	Included int    // the lens's legacy inclusion count (may include progressing/unknown roles)
	Word     string // the lens's own wording: "not ready", "not synced or refresh overdue", ...
}
```

The area text uses `Included` with `Word`, as the lens does. The tone is the worst role among included objects. `Roles.Unknown > 0` is a coverage gap ("n unknown") and blocks cluster ok (§5), so native Unknown can never hide inside a quiet summary.

## 3. Watches, leases and generations (R2, C2, C5)

### 3.1 Health-owned reflectors with an exact re-list barrier

A shared informer exposes no signal that a re-list has completed. `conn.go:413-426` recovers on any refresh once the initial HasSynced holds. `watchErrTracker` recovers on any successful list call or watch establishment (`gitopswatch.go:58-104`). Neither proves a replacement list was applied, so v2 does not use them for health coverage.

Every kind in §1 runs as a client-go `Reflector` (paging, backoff, bookmarks) into a Klyx-owned `cache.Store`, the kind's **model**. The reflector calls the model as follows:

- `Replace(items, rv)` once per complete list, all pages, initial or re-list;
- `Add`, `Update` and `Delete` per watch event.

The ListWatch is wrapped. A list or watch-establish error, a `watch.Error` event and an unexpected channel close are all recorded with their class. A kind's freshness is a per-kind state machine:

| From | Event | To |
|---|---|---|
| checking | `Replace` applied | live |
| checking | error of class forbidden / unauthorized | unreadable (no evidence) |
| checking | `InitialSyncTimeout` (60s) without `Replace` | unreadable (class timeout) |
| live | any recorded error | stale (retained evidence; `LastGoodAt` = the error time) |
| stale | `Replace` applied after the error | live |
| any | lease released / generation ended | (model discarded) |

`live` therefore means that a complete replacement list was applied after the last error, and no error has been recorded since. A stale kind whose re-list is still paging stays stale until `Replace` runs.

**Restart barrier (A2).** The pinned client-go v0.36.2 Reflector retries a failed watch establishment from `LastSyncResourceVersion` without listing again (`tools/cache/reflector.go:561-668`). This covers connection refused, 429 and internal errors. A clean watch timeout also re-watches without a list. So `Replace` alone would never arrive after those errors.

Therefore any error Klyx records for a kind (a list error, a watch-establish error, or a `watch.Error` event) moves the kind to `stale` and ends that kind's **reflector instance**. Its context is cancelled, stopping its internal retry. A new reflector instance then starts after an exponential backoff (1s doubling to 30s, with jitter). A new reflector begins with an empty resource version, so it must list completely and call `Replace` before the kind is `live` again.

Each instance writes through its own store adapter, stamped with the instance id. The model accepts calls only from the current instance, so late events from a cancelled instance are dropped.

The model's contents survive the restart as retained evidence until the new `Replace` lands.

A watch that ends cleanly, by server timeout or rotation, records no error and stays `live`. The reflector resumes from `LastSyncResourceVersion`, and the watch protocol makes that continuous: the server replays from that version or answers 410 Expired, which the reflector turns into a full list and `Replace`. This is not "watch establishment equals catch-up". It is the protocol's version continuity on a path where Klyx observed no error.

Tests cover each path:

- a failed establishment followed by a successful watch with no list (stays `stale`; the restart's `Replace` makes it `live`);
- a quiet resumed stream (stays `live`);
- a normal timeout (no error, no fault);
- an expired version (the reflector's own re-list and `Replace`);
- a late event from a cancelled instance (dropped).

Kinds are independent: one domain can recover while another remains failed. The connection state (health spec §2) is a separate axis, and it never sets kind freshness. Connection Degraded is "connected · notes" and leaves kinds live. A Stale or unreachable connection reaches each kind through that kind's own recorded errors.

This replaces the parent spec's §2 Stale row for every surface on the Observer. The parent spec carries a matching amendment, so the two documents do not contradict each other.

### 3.2 Leases, owners and generations

- **Owner token.** `Acquire(owner Owner)`, where `Owner{Kind: "health"|"lens", ID string}` (for example `lens:certs`). The refcount is the number of distinct owners. A repeated Acquire by the same owner is idempotent and returns the same `Release`. `Release` is idempotent.
- **Lenses.** Lens `Open`/`Close` (`GitOpsService`, `CertManagerService`, `ExternalSecretsService`, and the new Argo, Gateway and Karpenter lens subscriptions) become Acquire/Release by a lens owner. A second Open is a no-op, and a Close releases only that owner.
- **Subscriptions** belong to an owner: `Subscribe(owner, fn)` is removed on that owner's Release. Lens push loops are driven by these notifications (§4), replacing the 1s ticker and DeepEqual.
- **Generations.** Each start of a domain's watches creates a generation: a fresh context, fresh reflectors and fresh models. Informers and reflectors are never restarted after stopping. The last Release, a CRD disappearing, or a version change ends the generation: its context is cancelled and its models are detached. A later Acquire or reappearance starts a new generation. Late callbacks from an ended generation write into detached models and are ignored. Late startup completions check the generation's fence before installing anything.
- **Release during start.** The start is asynchronous: served-version discovery, then reflectors. A Release that drops the count to zero cancels the in-flight start, and its completion finds the fence moved and discards itself.
- **The Observer** holds a health owner lease on every applicable task for the lifetime of the connection context. It starts domains staggered (0-3s jitter) after the core kinds go live.

### 3.3 Capability appear and disappear

- **Re-probe.** Discovery is re-probed every 5m through the existing 60s TTL cache, and immediately on a NotFound list.
- **Discovery failure** confirms nothing. The last known served set is kept, and a capability gap is recorded (class `discovery`).
- **A version-specific 404** triggers a re-probe of the served versions. If the resource is served at another version, a new generation starts at that version. Only a successful discovery that lacks the resource makes the part `absent`.
- **Controller tiers** come from the same re-probe. This fixes the connect-time-only tiers for Network, cert-manager, ESO, Karpenter and Reloader.

### 3.4 Observer identity and Final

- **Incarnation.** Each Observer has an `Incarnation` from a process-wide monotonic counter. Connection recovery never changes it; a new Observer for the same cluster gets a higher one.
- **Final.** Cancelling the Observer's context publishes once with `Final: true`, bypassing the throttle, and removes the observer from `Summaries()`.
- **Tombstones.** The frontend records `finalizedThrough[cluster] = max(finalizedThrough[cluster], incarnation)` and drops any summary or rows at or below it, so a delayed bootstrap cannot resurrect the observer.
- **Final from an older incarnation** never deletes a newer observer's stored summary. A `Final` removes the stored entry only when that entry's incarnation is the same or older.
- `Final` removes the summary entry, never the configured fleet cluster: the fleet list still comes from `fleet:updated`.

## 4. Publication: sequence, watermarks and atomic capture (R3, C1, C7)

Two different counters:

- **`Seq`**, the publication sequence, is per Observer and monotonic. It advances on every observable change: a model mutation, a timer reclassification, a kind freshness transition (error, recovery, timeout), a capability or controller change, and a successful snapshot even when its content is unchanged (freshness advanced). The frontend orders by `(Incarnation, Seq)` and keeps strictly newer, so no observable change can be discarded.
- **`Obs`**, the per-part data watermark, is informational. It advances when that part's counted data changes: a model mutation in its counted kinds or dependencies, or a snapshot with changed content.
- **`Ver`**, the per-part version (A1), is monotonic. It orders a part's counts and rows. It advances on every part-visible change:
  - `Obs` advanced;
  - a re-evaluation changed any classification, label or `Next` (a time boundary crossed with no data change);
  - the part's coverage or a kind's freshness, class, AsOf or LastGoodAt changed (error, recovery, timeout);
  - a successful unchanged snapshot advanced AsOf.

  Unrelated parts are not re-evaluated, so their `Ver` does not move: pod churn never touches Certificates.

**Atomic capture.** A part's model mutations and its `Obs` increment happen under the part's lock. Evaluation takes the lock, captures the model contents and `Obs` together, releases it, and evaluates the captured set at one `EvaluatedAt`. Counts, Top refs and rows from one capture share its `Obs`, `Ver` and `EvaluatedAt`. Nothing is stamped with a value read after assembly.

**Throttle.** Any change marks the Observer dirty. A publish is scheduled at `max(now, lastPublish + 1s)`, so the maximum delivery delay is 1s. One publish tick emits:

1. the summary at its `Seq`, with every part's `Obs` and `Ver`;
2. for each part whose `Ver` advanced and which has an open lens subscription, its rows with the counts from the same capture (atomic count + rows).

**One number per task (C7, both directions).** For a task whose rows are on screen, the lens header, the nav tree and the mast's task state all take that task's counts from the rows payload. For other tasks they come from the summary. Both are produced by one capture per tick, so they normally carry the same `Ver` and `EvaluatedAt`.

- **Rows newer than the summary:** their counts are used at once.
- **Summary newer than the rows (A4).** A part's `Ver` in the summary above the rows' `Ver` opens a mismatch, and its deadline is fixed at the first unresolved mismatch: **1s**. Later summary ticks never reset it.
  - Until the deadline, the counts stay the rows' counts, marked `updating`.
  - Any row payload with `Ver` at or above the summary's part `Ver` resolves the mismatch. It need not equal an obsolete watermark.
  - At the deadline, the counts switch to the summary. The rows are qualified "as of HH:MM:SS - refreshing", with their verdict chips dimmed, so an old row's verdict is never presented as current evidence. A fenced `Rows(cluster, part)` re-bootstrap is also issued.
- **Fencing.** Each re-bootstrap carries a request generation. A reply is applied only if its generation is current and its `Ver` is newer than the rows held. So a delayed or dropped rows event cannot leave a quiet part `updating` forever, and a stale reply cannot win a race with a later event.

Under continuous churn every tick delivers rows and counts at one `Ver`, so `updating` never persists.

Tests:

- continuous churn;
- summary newer than rows, and rows newer than summary;
- a time boundary crossing with an open lens (fake clock): rows and counts update with no data change;
- error and recovery, and an unchanged successful snapshot, each with no model mutation;
- unrelated parts keep their `Ver`;
- a dropped rows event and a delayed rows event (the deadline, then re-bootstrap);
- a bootstrap reply racing a later event.

**Transport.**

- The `health:summary` event carries the full summary; `health:rows:<cluster>:<part>` carries rows per subscribed part.
- The summary is bootstrapped with `HealthService.Summaries()`, and rows with `HealthService.Rows(cluster, part)`. Each subscribes before it bootstraps and keeps only strictly newer by `(Incarnation, Seq)` for summaries and `(Incarnation, Ver)` for rows.
- Details (inspector) carry UID and generation.

## 5. Time, staleness names and coverage (R4, R5, C6)

**Reclassification.** One timer per Observer is armed at the earliest boundary across all parts. The candidates are `ReclassifyAt`, `NativeEval.Next`, snapshot freshness deadlines (2× cadence), `InitialSyncTimeout` and snapshot read deadlines. On fire, it re-evaluates captures (no API call) and publishes with a new `Seq`. It uses the injected `internal/clock`. `EvaluatedAt` is separate from every observation time.

**Three stale meanings, three names:**

- `coverage: stale` means the cache or snapshot is last-good;
- `generationLag` is the ratified "Syncing…";
- `refreshOverdue` is an overdue ESO refresh.

The summary never uses a bare "stale" for the latter two.

```go
type Coverage string // live | stale | checking | unreadable | absent

type KindCoverage struct {
	Kind, GVR  string        // GVR "" for the Helm CLI
	Role       string        // counted | dependency
	Scope      string        // "all namespaces" (Klyx reads cluster-wide), or "cluster-scoped"
	Coverage   Coverage
	Class      string        // "", forbidden, unauthorized, notFound, discovery, transport, timeout, cliMissing, other
	Error      string        // verbatim
	Objects    int
	Truncated  bool          // snapshots only
	AsOf       time.Time     // data known current up to: EvaluatedAt while live; LastGoodAt when stale; read time for snapshots
	LastGoodAt *time.Time    // when it left live (stale only)
	Cadence    time.Duration // snapshots only
}

type Part struct {
	ID, Entity, Scope string     // e.g. "issuers", "Issuers and ClusterIssuers", "all namespaces + cluster-scoped"
	Classifier        string     // verdict | native | evidence
	Coverage          Coverage   // worst of its counted kinds and dependencies: unreadable > stale > checking > live; absent only if all absent
	Kinds             []KindCoverage
	Counts            *Counts    // verdict parts
	Native            *NativeCounts
	Evidence          *int
	Top               []Ref      // worst first, max 5; route parts: one ref per route UID, naming its worst parent
	Obs               uint64     // data watermark (informational)
	Ver               uint64     // part version: orders counts and rows (§4)
	AsOf              time.Time  // MIN of its kinds' AsOf (the oldest freshness, conservative)
	Next              *time.Time
}

type Task struct {
	ID         string       // registry destination id: workloads pods nodes events helm flux argo certs eso gateway karpenter
	Parts      []Part
	Controller *ControllerIssue // separate identity, never in a part count
	Coverage   Coverage     // worst of its parts
	AsOf       time.Time    // min of its parts
}

type Ref struct {
	Cluster, GVR, Namespace, Name, UID string
	Parent                             string // attachment parent ("gateway infra/eg"), else ""
	Role, Label                        string
	Generation                         int64
	Route                              string // nav registry destination id
}

type Summary struct {
	Cluster     string
	Incarnation uint64
	Seq         uint64
	EvaluatedAt time.Time
	Connection  string // health spec §2 connection word ("connected · notes" for Degraded)
	Namespaces  *int
	Tasks       []Task // applicable + absent (absent is an explicit "not installed")
	Final       bool
}
```

**Coverage rules.**

- **Zeros** are stated ("none need attention") only for a `live` part. A `stale` part reads "none at HH:MM (last good; latest read <class>)". A part that is `unreadable` with no evidence reads "could not be read (<class>)", for example "Klyx cannot list certificates (forbidden)".
- **A forbidden first read** is `unreadable` with no counts. **RBAC revoked after a sync** is `stale` with class forbidden and retained, labelled counts. Retained zeros are never presented as current absence.
- **Cluster ok** requires every applicable part (counted kinds and dependencies) `live`, verdict Unknown 0, native `Roles.Unknown` 0, and no controller issue. A failed dependency degrades its part. Example: failing EndpointSlices make route backends unverified, so the route part is `stale` or `unreadable` and the cluster is not ok.
- **Events** never contribute to verdicts. A truncated Events read is shown as capped and does not block cluster ok.
- **A fault is reportable despite gaps.**

## 6. Watch budget (R6, C4)

**Watches per fully equipped cluster** (Flux, Argo, cert-manager, ESO, Gateway API, Karpenter):

| Group | Watches |
|---|---|
| core: nodes, pods, namespaces, deployments, statefulsets, daemonsets, replicasets | 7 |
| Flux | 7 |
| cert-manager | 3 |
| ESO | 3 |
| Argo | 2 |
| Gateway: gatewayclasses, gateways, httproutes, services, endpointslices | 5 |
| Karpenter | 2 |
| controller-health workload watches, existing | about 4-6 |
| **Per cluster** | **about 33-35** |
| **Six clusters** | **about 200-210** |

Snapshots add Events every 60s and Helm every 5m per cluster. 1c-6 removes the duplicate per-lens raw watches (`OpenLive*`) and the lens lazy informers, because lenses read the Observer's models.

Klyx states no transport assumption, such as one HTTP/2 connection per cluster; stream and connection counts are measured. EndpointSlices are the highest-churn added kind, which is why §7 measures sustained update CPU.

## 7. Measurement with budgets set before the run (R6)

These are proposed acceptance budgets for review. If a measurement misses its budget, the design changes (incremental evaluation, lazier dependencies), not the budget.

**Go load test (`internal/observe`, fake clients; not an API-server cost measurement).** Six clusters; the large one has 5000 pods (15 labels each, long condition messages), 1200 workloads, 1500 EndpointSlices, 50 nodes, 200 Flux objects, 300 certificates, 400 ExternalSecrets, 20 Gateways, 300 HTTPRoutes and 50 NodeClaims. Budgets:

- first complete summary ≤ 5s per cluster;
- heap above baseline ≤ 96 MB for the large cluster;
- full evaluation of the large cluster ≤ 50 ms;
- under 100 updates/s for 30s: publishes ≤ 1/s per cluster, p99 delivery lag ≤ 1.2s, sustained evaluation CPU ≤ 10% of one core;
- recovery storm (all six re-list at once): peak heap ≤ 1.5× steady state, every kind live again ≤ 15s after the API recovers.

**Real API server.** An isolated, removable lab fixture on the local kind cluster: namespaces `klyx-load-*`, created and deleted by one script, with 5000 Pending pods (an unsatisfiable node selector) and 1000 Deployments. Budgets:

- initial sync of all kinds ≤ 20s;
- Klyx RSS increase ≤ 200 MB;
- idle CPU ≤ 2% of one core; ≤ 15% under 50 pod updates/s;
- one logical list cycle per kind per reflector instance; paginated LIST requests are counted separately, from the API server's `apiserver_request_total` delta;
- connection and stream counts recorded;
- steady and peak memory measured with every task's row subscription open.

The fixture is deleted after the run, and the numbers go in the PR. The six AKS clusters are the owner's acceptance run.

## 8. Frontend

- **Bridge.** `bridge/health.ts` uses subscribe-before-bootstrap, newest-wins and tombstones (§3.4, §4) for summaries of all clusters, plus rows for open lenses. The per-fetch fan-out and the 60s poll in `bridge/attention.ts` go away.
- **Areas.** `nav/attention.ts` maps a Task to an area, and its Parts to the area's wording lines. `UNCOVERED` and "not covered" go away only when the final gate (§9, slice 6) passes. Until then, incremental slices keep the transitional qualification for any task not yet on the Observer.
- **Wording.** The step 7e rules are kept: count areas, never objects; gaps are named; stale and unreadable wording comes from §5. Overview rows may list up to three Top refs as direct links.

## 9. Slices (C7 order) and acceptance

Each slice is reviewed before the next starts, and the app stays runnable with transitional qualifications until slice 6. Gates throughout: `go test -race ./...`, vet, golangci-lint, gofmt, govulncheck; tsc, eslint, vitest, vite build.

### Slice 1. Observer core

`internal/observe` with no real domains: owners and leases, generations and fences, reflector-into-model with the freshness machine (§3.1), `Seq`/`Obs`/`Ver` and atomic capture, the throttle, reclassification, Final and tombstones, the clock.

Tests:

- two simultaneous holders; repeated opens and closes; release during discovery and during start; late callbacks from an ended generation;
- disappearance and reappearance (a new generation); a version 404 that re-probes; a discovery error that confirms nothing;
- error and recovery without object changes; a re-list still paging stays stale; one kind recovering while another stays failed;
- the restart barrier paths (§3.1): a failed establishment then a successful watch with no list; a quiet resumed stream; a normal timeout; an expired version; a late event from a cancelled instance;
- a time-only threshold crossing; an unchanged successful snapshot that advances freshness;
- concurrent mutation during evaluation (race detector);
- the §4 publication tests;
- a route with one healthy and two failing parents, with parent removal and recreation;
- Final; a delayed bootstrap after Final; a late Final from an older incarnation; handler and goroutine baselines.

### Slice 2. Sources

Verdict tasks on projections, with the projection parity gate (§1.2). Native rules relocated with golden parity (§2), leases for all §1 kinds, the Helm and Events snapshots, controller issues from the re-probe. The load test and the lab measurement against the §7 budgets.

### Slice 3. Shared rows

Lenses read their task's rows from the Observer: Pods, Workloads, Nodes, Flux, Certificates, External secrets, Argo, Karpenter, and the Gateways list with route health. Lens lazy watches and `OpenLive*` for those kinds are removed, and the one-number rule (§4) applies. Helm and Events lens rows stay per-fetch snapshots with as-of, as does the Gateway topology's data-path rendering.

### Slice 4. Frontend summary

§8 for the open cluster's mast, tree and Overview.

### Slice 5. Fleet on summaries

Spine lamps and fleet meters (clusters, connected, attention, unknown coverage) come from summaries. The fleet board's per-visit health fan-out goes away.

### Slice 6. Final coverage-parity gate

The layout spec §4 deletion condition. It is the last gate, run on the kind fixture with one fixture per case:

- every step 7e area is present with equal numbers;
- a failed unused Issuer, a failed unused store, an ApplicationSet failure, a failing Flux source and a healthy Gateway with a broken route (backend not found, and no ready endpoints) each light their part;
- a controller down with no object fault lights its task with no object count;
- a forbidden kind reads as a named gap, and RBAC revoked after a sync reads as last-good;
- cluster ok appears only with everything live and no unknowns.

Only then is "not covered" removed. The acceptance text names the per-fetch limitation of the Helm, Events and topology lenses.

## 10. Decisions taken from the review

- **Events** stay a 60s bounded snapshot with truncation and as-of. An informer is optional later.
- **Flux sources** are their own part, with per-kind coverage.
- **cert-manager Issuing** keeps its current inclusion and progressing role. Policy changes belong only to the reviewed cert-manager domain table.
- **The budget** is a hypothesis until §7 is met.
