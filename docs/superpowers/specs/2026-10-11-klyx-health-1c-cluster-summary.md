# Klyx health slice 1c: the cluster health summary (design addendum)

Date: 2026-10-11. Status: draft for Codex design review; no code before approval. Parent: `2026-10-10-klyx-redesign-health-model.md` §4 (binding requirements, quoted by number below as R1-R6). Consumer: `2026-10-10-klyx-redesign-layout-nav.md` §4 (fault discovery; the transitional frontend summary from step 7e is what this replaces).

## 0. Why, and what changes for the operator

Today the attention summary is a frontend fan-out: seven per-fetch reads every 60s for the open cluster only (`frontend/src/bridge/attention.ts`), with cert-manager, External Secrets, Gateway API and Karpenter named "not covered". After 1c:

- Every connected cluster has one backend summary, live from watches. It is not polled and does not depend on which lens is open.
- Every installed domain is covered or explicitly qualified (checking, stale, unreadable). Once coverage parity is shown, the mast, tree and Overview drop "not covered", and so does the fleet.
- A summary count and the list it opens read the same observation, or say "updating". They never show two different numbers.

Non-goals: new domain decision tables. Health spec §5 owns those, each reviewed separately. 1c carries the current lens rules unchanged: verdict domains use `internal/health`, and the other domains use their existing lens predicates, moved into Go verbatim.

## 1. Observation sources per domain (R1)

| Domain | Source | Kinds (GVR resource) | Classifier | Today |
|---|---|---|---|---|
| Pods | typed informer, slim transform | pods | verdict (`health.Pod`) | metadata-only eager informer (count) + per-fetch list |
| Workloads | typed informers, slim transform; ReplicaSets metadata-only | deployments, statefulsets, daemonsets, replicasets | verdict (`health.Workload`, UID-chain ownership) | per-fetch list ×5 |
| Nodes | existing eager typed informer | nodes | verdict (`health.Node`) | same |
| Namespaces | metadata-only informer | namespaces | count only | per-fetch |
| Events | snapshot every 60s: `type=Warning` field selector, `Limit 500` | events | evidence (never a lamp) | per-fetch |
| Helm | snapshot every 5m via `helm list -A --max 0`, single-flight, 60s timeout; also after a Klyx Helm operation and on lens Refresh | releases (CLI) | native (relocated `helmHealth.ts`) | per-fetch CLI |
| Flux | informer leases (existing lazy watch) | kustomizations, helmreleases (count); sources stay lens-only kinds | native (`GitOpsSummary` rule) | live list per fetch |
| Argo CD | new dynamic informers | applications, applicationsets | native (`App.Broken`, `appSetConditionBroken`) | live list per fetch |
| cert-manager | informer leases (existing lazy watch) | certificates, issuers, clusterissuers | native (relocated `CertsView.needsAttention`) | not covered |
| External Secrets | informer leases (existing lazy watch) | externalsecrets, secretstores, clustersecretstores | native (relocated `ExternalSecretsView.needsAttention`) | not covered |
| Gateway API | new dynamic informers | gatewayclasses, gateways | native (relocated `gatewayModel.gatewayHealth`) | not covered |
| Karpenter | new dynamic informers | nodepools, nodeclaims | native (relocated `KarpenterView` claim/pool rules) | not covered |

**Slim transforms** bound memory (managedFields are already stripped everywhere). A cached Pod keeps:

- metadata: name, namespace, UID, labels, ownerReferences, creation and deletion timestamps, deletion grace;
- spec: nodeName, restartPolicy, and per-container name, image, restartPolicy and resources;
- status: complete.

It drops env, volumes, volumeMounts, probes, command/args, securityContext, affinity, tolerations and annotations. Workload objects keep the fields the `internal/health` rollout rules read (replicas, strategy, paused, progressDeadline, minReadySeconds, ordinals, partition, generation, full status) and container name and image. They drop the pod template's other spec. The inspector reads full objects on demand, as it does today.

**Helm** is never live. It is never a Secret payload either: `helm list` output carries no values, and Klyx surfaces none.

**Relocated native rules.** Each rule moves verbatim into its domain's pure Go package (`internal/certmanager`, `internal/externalsecrets`, `internal/gwapi`, `internal/karpenter`, `internal/helmcli`). The rule is exposed on the lens DTO as `attention` with a tone, and the lens drops its local copy. One predicate, two consumers. The wording stays the lens's native vocabulary:

| Domain | fault | degraded | Words |
|---|---|---|---|
| cert-manager | Ready=Failed, or expired | other not-Ready (Issuing, Unknown) | `n not ready` |
| ESO | SyncState Failed | Unknown, or refresh overdue | `n not synced or refresh overdue` |
| Gateway | Accepted=False at the current generation | Accepted=False last-observed (generation lag); Programmed=False | `n need attention` |
| Karpenter | NodeClaim not-ready; NodePool not-ready | NodeClaim unready past 15m, "appears stuck" | `n not ready · m appear stuck · k NodePools not ready` |
| Helm | failed | pending past `PENDING_DEADLINE_S`, "appears stuck" | `n failed · m appear stuck` |
| Flux | NotReady, suspended excluded (legacy: includes progressing, labelled so) | - | `n not ready` |
| Argo | `App.Broken` | - | `n need attention` |

Each relocated rule also returns its next time boundary when it has one (cert NotAfter, ESO refreshTime + 2×interval, NodeClaim creation + 15m, Helm pending deadline), so reclassification works for native domains too (§4).

## 2. Health-owned watch leases (R2)

New package `internal/observe`, Kubernetes-aware but with no Wails dependency, owns one `Observer` per cluster for the lifetime of its connection context.

- **Leases.** `Lease(domain)` is refcounted per cluster and domain. `Acquire(owner)` starts the domain's informers on the first holder and returns an idempotent `Release`; the last release stops them. The Observer holds a cluster-lifetime lease on every applicable domain. Lens `Open`/`Close` (`GitOpsService`, `CertManagerService`, `ExternalSecretsService`) become `Acquire`/`Release`, so a lens closing never tears down a cache the summary reads. Today's single-flight `*Wanted`/`*Opening` flags are subsumed by the refcount.
- **Callbacks.** Every handler is registered through the `ResourceEventHandlerRegistration` that `AddEventHandler` returns, and removed on release. A test asserts that the handler count returns to its baseline after Acquire/Release cycles. The lens push loops become event-driven: a dirty mark plus the throttle in §3. The 1s DeepEqual polling of the lease-held stores goes away.
- **Lifetime.** The Observer's context is a child of the connection context. Cancelling it (shutdown, or a future runtime removal: `Registry` has no Remove today) stops every informer and scheduler. It also emits one terminal publish, `Final: true`, so the frontend drops the cluster rather than freezing its last numbers.
- **Epochs.** `Epoch` is the connection generation. It increments on the first Synced and on every Stale→Synced recovery, which is when reflectors have re-listed. Informers are not recreated: client-go reflectors back off and re-list internally. The epoch tells consumers that object identity may have changed across a gap.
- **Capability appear/disappear.** Discovery is re-probed every 5m through the existing 60s TTL cache, and immediately when a lease's list returns NotFound for its resource. When a CRD appears, the domain is acquired with a start jitter of 0-3s. When it disappears, the domain is released and its coverage becomes `absent`. This also fixes the connect-time-only tiers noted in the backend map: Network, cert-manager, ESO, Karpenter and Reloader are never re-probed today.
- **Idempotent start.** `Observer.Start` called twice is a no-op. Domains start staggered after the core caches (nodes, pods) sync, so six clusters do not issue their initial lists at once.

## 3. Revisioned publication (R3)

- **Revision.** A per-cluster `uint64` observation revision is incremented by every informer event and every snapshot that changes content. It is monotonic for the process lifetime and never reset by an epoch. A summary carries the revision it was evaluated at.
- **Throttle.** A dirty mark schedules a publish at `max(now, lastPublish + 1s)`. The maximum delivery delay is 1s. The summary republishes whenever the revision advanced, even if its content is unchanged, so "updating" (below) always resolves within 1s. Six clusters emit at most six events per second (today `fleet:updated` emits every second unconditionally). The terminal publish (§2) bypasses the throttle.
- **Transport.** The `health:summary` Wails event carries the complete `Summary` (no deltas). Bootstrap uses `HealthService.Summary(cluster)` and `HealthService.Summaries()`. The frontend subscribes before it bootstraps. It keeps a summary only when its `(Epoch, Revision)` is newer than the one held, which drops a bootstrap reply that lost a race with an event, and drops late events. A `Final` summary removes the cluster's entry.
- **Rows and details.** Lists served from the shared caches (slice 1c-7: Pods, Workloads, Nodes; the lease-held Flux, cert-manager and ESO lens payloads gain the same field) carry the `Revision` they were read at. Details carry UID and generation. When a surface shows a summary count beside rows from a newer revision, the count renders as `updating` until the summary catches up. That takes at most 1s by the throttle bound. Surfaces still on their own per-fetch reads (Argo, Gateway topology, Karpenter, Helm and Events lenses) show their read time and are not compared. This gap is named; it is not hidden.

## 4. Time-driven reclassification (R4)

- Evaluators already return `ReclassifyAt`, and the relocated native rules return their boundary (§1). After each evaluation the Observer arms one timer per cluster at the earliest boundary. The candidates are object boundaries plus coverage deadlines: a snapshot older than 2× its cadence becomes stale. When the timer fires, the Observer re-evaluates the cached objects, with no API call, and publishes when anything changed. It uses the injected `internal/clock`, so tests advance time without object changes.
- `EvaluatedAt` is the time the classifications were computed. It is separate from the `ObservedAt` of each domain and kind (the newest observation folded in) and from `SyncedAt` (the last successful sync or read).
- **Three stale meanings, three names:**
  - `coverage: stale` means the cache or snapshot is last-good because its watch or read is failing.
  - `generationLag` means an object's controller has not observed its latest generation (the ratified "Syncing…" dim).
  - `refreshOverdue` means an ESO refresh is overdue.

  The summary schema never uses a bare "stale" for the latter two. Renaming the existing per-DTO `stale` fields belongs to each domain slice.

## 5. Coverage schema (R5)

```go
type Coverage string // live | stale | checking | unreadable | absent

type KindCoverage struct {
	Kind      string        // "pods", "certificates", "releases"...
	GVR       string        // "" for the Helm CLI
	Coverage  Coverage
	Class     string        // "", forbidden, unauthorized, notFound, transport, timeout, cliMissing, other
	Error     string        // verbatim, never truncated by the backend
	Objects   int
	Truncated bool          // snapshots only (Events cap); informers are never truncated
	SyncedAt  time.Time
	ErrorAt   time.Time
	Cadence   time.Duration // snapshots only
}

type Counts struct{ Total, Fault, Degraded, Unknown, Progressing, Paused, OK int }

type Ref struct { // Top refs: worst first, at most 5 per domain
	Cluster, GVR, Namespace, Name, UID string
	Verdict, Label                    string
	Generation                        int64
	Route                             string // the lens destination id from the nav registry
}

type Domain struct {
	ID         string // workloads pods nodes events helm flux argo certs eso gateway karpenter
	Classifier string // verdict | native | evidence
	Coverage   Coverage
	Kinds      []KindCoverage
	Counts     *Counts       // verdict domains
	Native     *NativeCounts // native domains: Fault, Degraded, Parts []{N, Word, Tone}
	Evidence   *int          // events: warning count (capped when Truncated)
	Top        []Ref
	ObservedAt time.Time
	Next       *time.Time    // this domain's next reclassification boundary
}

type Summary struct {
	Cluster     string
	Epoch       uint64
	Revision    uint64
	EvaluatedAt time.Time
	Connection  string   // the health spec §2 connection word
	Namespaces  *int     // nil until the namespace cache syncs
	Domains     []Domain // applicable domains plus absent ones (absent is explicit "not installed")
	Final       bool
}
```

Rules:

- **Domain coverage is the worst of its kinds,** in the order unreadable, stale, checking, live. A domain is absent only when all its kinds are absent.
- **Zeros are omitted only under complete coverage.** A `live` domain with zero attention says "none need attention". Any other coverage states what was read, as the step 7e wording already does.
- **Cluster "ok" requires every applicable domain `live` and no `unknown` verdicts.** A fault is reportable despite gaps.
- **Connection gates assertion** (health spec §2). With the connection Stale, every domain is at best `stale`, dimmed with its as-of time and never counted as a live incident. With Connecting or unreachable, domains are `checking` or `unreadable`.

## 6. Watch truth and budget (R6)

**Per informer.** Klyx tracks sync, the current error and recovery, reusing `watchErrTracker`, which is recovery-aware. Errors are classified:

- Forbidden and Unauthorized mean `unreadable` with that class. "Klyx's credentials cannot list X" is never presented as "controller down", and it never touches capability tiers.
- NotFound on a CRD resource means `absent` after an immediate re-probe confirms it.
- Transport errors and timeouts mean `stale` if the kind synced before, else `checking` until the sync timeout and `unreadable` after it.

The Helm CLI being missing is `unreadable` with class `cliMissing`, never `absent`.

**Watch counts per fully equipped cluster** (Flux, Argo, cert-manager, ESO, Gateway API, Karpenter):

| Group | Watches |
|---|---|
| core: nodes, pods, namespaces, deployments, statefulsets, daemonsets, replicasets | 7 |
| Flux: 2 counted + 5 source kinds still leased for the lens | 7 |
| cert-manager | 3 |
| ESO | 3 |
| Argo | 2 |
| Gateway | 2 |
| Karpenter | 2 |
| existing field-selected controller-health watches | about 4-6 |
| **Per cluster** | **about 30-32** |
| **Six clusters** | **about 185-195** |

This is higher than the parent spec's ≈78. That estimate predates the typed workload caches and the six domains now covered. Snapshots add one Events list per 60s and one `helm list` per 5m per cluster. Each cluster's watches share its HTTP/2 connection, well under the API server's per-connection stream limit. Slice 1c-7 removes the duplicate per-lens raw watches for pods, workloads and nodes (`OpenLive*`), which today add up to five more per open lens.

**Cost controls:**

- slim transforms;
- staggered domain start (§2);
- the reflector's paged initial list (resourceVersion 0 is served from the API server's watch cache);
- indexes: pods by controller UID, ReplicaSets by controller UID.

Evaluation recomputes a dirty domain in full on publish; incremental evaluation is deferred until the measurements below say it is needed.

**Measurement before claiming cost solved:**

- **Go load test (`internal/observe`).** Six fake-client clusters, one with 5000 pods, 1200 workloads, 50 nodes, 200 Flux objects, 300 certificates, 400 ExternalSecrets, 20 Gateways and 50 NodeClaims. Measured:
  - time to the first complete summary per cluster;
  - heap in use after a forced GC;
  - full-evaluation time for the large cluster;
  - publishes per second under 100 pod updates/s for 10s (≤ 1/s per cluster);
  - maximum delivery delay (≤ 1s plus scheduling).

  The thresholds are recorded as test assertions with headroom. Numbers go in the PR.
- **Real API server.** The local kind cluster with 5000 Pending pods (unschedulable by a node selector) across 50 namespaces and 1000 Deployments. Measured: Klyx RSS before and after, initial sync time, evaluation time and event rate. Numbers go in the PR. The six AKS clusters are the owner's acceptance run, not mine without a go-ahead.

## 7. Frontend migration

- `bridge/health.ts` subscribes to `health:summary` and then bootstraps with `Summaries()`, applying the newest-wins rule (§3). The store holds summaries for all clusters, not only the open one.
- `nav/attention.ts` maps `Domain` to `Area` (adding certs, eso, gateway, karpenter). `UNCOVERED` and its "not covered" wording are deleted, as are the 60s poll and the per-fetch fan-out in `bridge/attention.ts`. Area stale and last-good text come from coverage `stale` with `SyncedAt`; `unreadable` keeps "could not be read" with the error class worded, e.g. "Klyx cannot list certificates (forbidden)".
- Mast, tree and Overview keep their step 7e wording rules (count areas, never objects; gaps are named). Overview area rows may list up to three `Top` refs as direct links.

## 8. Slices and acceptance

Each slice is reviewed before the next starts. Gates throughout: `go test -race ./...`, vet, golangci-lint, gofmt, govulncheck; tsc, eslint, vitest, vite build.

1. **1c-1 Observer core.** `internal/observe` with no domains wired: leases, coverage tracker with error classes, publisher (revision, epoch, throttle, terminal publish), reclassification scheduler, clock. Fake-informer tests: refcount and idempotent release; handler-count baseline; ctx cancel → `Final` and no goroutine leak (goroutine-count baseline; goleak is not a dependency today); epoch on recovery; throttle bounds under a burst; forbidden vs NotFound vs transport classification; scheduler fires on a fake-clock advance with no object change.
2. **1c-2 Verdict domains on shared caches.** Typed slim informers for pods and workloads, ReplicaSet and namespace metadata, the indexes, and summary domains for pods, workloads and nodes. The eager metadata pod informer is replaced; the pod count comes from the new cache. The load test and the kind 5000-pod measurement belong to this slice. Acceptance: the shop fixture yields the health spec §3 numbers from the summary.
3. **1c-3 Leases for Flux, cert-manager and ESO.** Lens Open/Close become Acquire/Release; push loops become event-driven; the cert-manager and ESO rules are relocated to Go and the lenses use DTO `attention`. Acceptance: closing the Certificates lens leaves the summary's cert coverage `live`; the lens count equals the summary count at the same revision.
4. **1c-4 New domain watches.** Argo, Gateway and Karpenter informers; the Gateway and Karpenter rules relocated; the 5m capability re-probe. Acceptance: installing a CRD at runtime brings its domain to `live` without a restart, and deleting it makes the domain `absent`.
5. **1c-5 Snapshot domains.** Helm (rule relocated) and Events. Acceptance: a missing `helm` binary reads `unreadable (helm CLI not found)`, never "no failed releases".
6. **1c-6 Frontend on the summary.** §7. Acceptance is coverage parity, the layout spec §4 deletion condition: every area the step 7e summary showed is present with equal numbers on the kind fixture; cert-manager, ESO, Gateway and Karpenter fixtures (one failing object each) light their areas; no "not covered" remains while every domain is live; a forbidden kind reads as a named gap.
7. **1c-7 Rows on the shared observation.** The Pods, Workloads and Nodes lenses read the shared caches with `Revision`, replacing `OpenLive*` for those kinds; the "updating" rule applies.
8. **1c-8 Fleet on summaries.** The spine lamps, fleet board attention and fleet meters (clusters, connected, attention, unknown coverage) come from summaries. The fleet board's per-visit health fan-out goes away; its topology reads stay.

## 9. Open questions for review

1. Cadences: Helm 5m and Events 60s. Is an Events informer with a `type=Warning` field selector preferable to a 60s capped list?
2. Flux sources stay leased for the lens but outside the summary count. Should their coverage still show in the Flux domain as non-counting kinds?
3. Cert-manager "Issuing" counts as attention today (relocated verbatim). Leave that to the cert-manager domain slice, or change it now?
4. Is 1c-7 (rows on the shared caches) required before the TriageTape acceptance, or after it as written?
