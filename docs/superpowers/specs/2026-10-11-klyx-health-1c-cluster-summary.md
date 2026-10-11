# Klyx health slice 1c: the cluster health summary (design addendum, v2)

Date: 2026-10-11. Status: v2 for Codex design review; no code before approval. Parent: `2026-10-10-klyx-redesign-health-model.md` §4 (binding requirements R1-R6). Consumer: `2026-10-10-klyx-redesign-layout-nav.md` §4 (fault discovery). The step 7e frontend summary is what this replaces.

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

Each workload entry also stores two precomputed facts:

- its Reloader targets, `reloadTargets(annotations, podSpec)` (`assemble.go:193`, `reloader.go:89-110`), computed from the raw object at transform time;
- its template resource totals.

Workload DTO assembly takes the precomputed facts instead of the raw annotations and pod spec. Selector-matched pod facts keep working: pod labels are retained, and the selector is kept.

**Services** are metadata-only (existence). An **EndpointSlice** keeps its service-name label, address type and, per endpoint, the ready condition and targetRef kind. Addresses are dropped; `routeIssues` reads counts only. **ReplicaSets** and **namespaces** are metadata-only.

**Parity gate.** Raw-versus-projected fixtures run every current consumer on both forms and require identical output:

- pod DTO, workload DTO including Reloader, resources, images and selector-matched pods, Gateway route health;
- `internal/health` evaluation (verdict, label, rule, ReclassifyAt).

This gate passes before 1c-2 evaluates from projections and before 1c-6 serves rows from them. Memory measurements (§7) use fixtures with realistic label sets (15 labels) and long condition messages, so status and label retention is measured, not assumed small.

### 1.3 Identity and deduplication (C4)

**Objects** are counted once per part by UID.

**Attachments** are counted per route × parent Gateway. The part's count names its entity: "routes with a failing attachment" counts route objects. The Top refs list the attachments.

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

`live` therefore means that a complete replacement list was applied after the last error, and no error has been recorded since. A stale kind whose re-list is still paging stays stale until `Replace` runs. Kinds are independent: one domain can recover while another remains failed. The connection state (health spec §2) is a separate axis, and it never sets kind freshness. Connection Degraded is "connected · notes" and leaves kinds live. A Stale or unreachable connection reaches each kind through its own watch errors.

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
- **Tombstones.** The frontend records `finalizedThrough[cluster] = incarnation` and drops any summary or rows at or below it, so a delayed bootstrap cannot resurrect the observer. `Final` removes the summary entry, never the configured fleet cluster: the fleet list still comes from `fleet:updated`.

## 4. Publication: sequence, watermarks and atomic capture (R3, C1, C7)

Two different counters:

- **`Seq`**, the publication sequence, is per Observer and monotonic. It advances on every observable change: a model mutation, a timer reclassification, a kind freshness transition (error, recovery, timeout), a capability or controller change, and a successful snapshot even when its content is unchanged (freshness advanced). The frontend orders by `(Incarnation, Seq)` and keeps strictly newer, so no observable change can be discarded.
- **`Obs`**, the per-part observation watermark, is monotonic. It advances only when that part's counted data changes: a model mutation in its counted kinds or dependencies, or a snapshot with changed content. Unrelated parts never advance it, so pod churn does not invalidate Certificates.

**Atomic capture.** A part's model mutations and its `Obs` increment happen under the part's lock. Evaluation takes the lock, captures the model contents and `Obs` together, releases it, and evaluates the captured set. Counts, Top refs and rows are always stamped with the `Obs` of exactly the contents they were computed from, never a value read after assembly.

**Throttle.** Any change marks the Observer dirty. A publish is scheduled at `max(now, lastPublish + 1s)`, so the maximum delivery delay is 1s. One publish tick emits:

1. the summary at its `Seq`, with every part's `Obs`;
2. for each part whose `Obs` advanced and which has an open lens subscription, its rows with the counts computed from the same capture (atomic count + rows).

**One number per task (C7, both directions).** For a task whose rows are on screen, the lens header, the nav tree and the mast's task state all take that task's counts from the rows payload. For other tasks they come from the summary. Both are produced by one capture per tick, so they normally carry the same `Obs`.

- If a summary arrives with a part `Obs` newer than the rows on screen, the task's counts keep the rows' counts and mark `updating` until the matching rows arrive. After 1s they switch to the summary's count, and the rows themselves show `updating`.
- If rows arrive newer than the summary, the rows' counts are used immediately.

Under continuous churn every tick delivers rows and counts at one `Obs`, so `updating` never persists. Tests cover continuous updates, summary-newer-than-rows and rows-newer-than-summary.

**Transport.**

- The `health:summary` event carries the full summary; `health:rows:<cluster>:<part>` carries rows per subscribed part.
- The summary is bootstrapped with `HealthService.Summaries()`, and rows with `HealthService.Rows(cluster, part)`. Each subscribes before it bootstraps and keeps only strictly newer by `(Incarnation, Seq)` for summaries and `(Incarnation, Obs)` for rows.
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
	Top               []Ref      // worst first, max 5; attachments for route parts
	Obs               uint64
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
- one paged initial list per kind per generation, checked from the API server's `apiserver_request_total` delta;
- connection and stream counts recorded.

The fixture is deleted after the run, and the numbers go in the PR. The six AKS clusters are the owner's acceptance run.

## 8. Frontend

- **Bridge.** `bridge/health.ts` uses subscribe-before-bootstrap, newest-wins and tombstones (§3.4, §4) for summaries of all clusters, plus rows for open lenses. The per-fetch fan-out and the 60s poll in `bridge/attention.ts` go away.
- **Areas.** `nav/attention.ts` maps a Task to an area, and its Parts to the area's wording lines. `UNCOVERED` and "not covered" go away only when the final gate (§9, slice 6) passes. Until then, incremental slices keep the transitional qualification for any task not yet on the Observer.
- **Wording.** The step 7e rules are kept: count areas, never objects; gaps are named; stale and unreadable wording comes from §5. Overview rows may list up to three Top refs as direct links.

## 9. Slices (C7 order) and acceptance

Each slice is reviewed before the next starts, and the app stays runnable with transitional qualifications until slice 6. Gates throughout: `go test -race ./...`, vet, golangci-lint, gofmt, govulncheck; tsc, eslint, vitest, vite build.

### Slice 1. Observer core

`internal/observe` with no real domains: owners and leases, generations and fences, reflector-into-model with the freshness machine (§3.1), `Seq`/`Obs` and atomic capture, the throttle, reclassification, Final and tombstones, the clock.

Tests:

- two simultaneous holders; repeated opens and closes; release during discovery and during start; late callbacks from an ended generation;
- disappearance and reappearance (a new generation); a version 404 that re-probes; a discovery error that confirms nothing;
- error and recovery without object changes; a re-list still paging stays stale; one kind recovering while another stays failed;
- a time-only threshold crossing; an unchanged successful snapshot that advances freshness;
- concurrent mutation during evaluation (race detector);
- continuous updates; summary-newer-than-rows; rows-newer-than-summary;
- Final; a delayed bootstrap after Final; handler and goroutine baselines.

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
