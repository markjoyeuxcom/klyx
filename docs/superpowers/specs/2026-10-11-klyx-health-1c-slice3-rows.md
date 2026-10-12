# Klyx health 1c - slice 3 design: lenses read Observer rows

Status: CLOSED by Codex (2026-10-12) - every lens migrated, the Pods switch after the rows transport addendum (`2026-10-12-klyx-health-1c-slice3-rows-transport.md`) passed the §6a gate in its owner-ratified memory window. Approved with changes by Codex (2026-10-11); amended in §2a, §2b, §4a, §5a, §5b, §6a and §8 below before implementation. Parent spec: `2026-10-11-klyx-health-1c-cluster-summary.md` (§4 publication, §9 slice 3). Slices 1-2 are closed: every task's kinds are watched by the per-cluster Observer for the cluster's lifetime.

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

## 2a. The evaluated row envelope (review P1)

A row is not just its domain object. Each evaluator emits an immutable envelope from the same capture:

- **identity:** UID, served GVR, namespace/name and generation, where the kind has them;
- **the captured evaluation:** the native `NativeEval` (included, role, label, next) or the core verdict;
- **the presentation data:** the parsed domain object.

The DTO converters copy the captured evaluation into the DTO's `rule` or verdict fields. They never call a rule, never read the clock for a rule, and never fetch the current object. A boundary crossed between evaluation and conversion therefore cannot change a row's verdict without changing its part's `Ver`.

Facts the evaluation used are captured with the rows. Workloads carries the `fluxPresent` it was evaluated with, never one read from a later summary.

The existing lens DTOs are extended with `uid` and `generation` (and `gvr` where a link needs it). No universal resource schema is introduced.

Tests:

- a boundary crossing between evaluation and conversion keeps the captured rule;
- same-name recreation gives a new UID, a new row identity and a new selection key;
- serialization round-trip;
- count-partition parity: the rows' captured roles and inclusion partition exactly to the part's counts.

## 2b. Subscription and incarnation lifecycle (review P1)

- **Leases.** `OpenRows(cluster, task, part, owner)` first takes `Acquire(task, owner)`, then `SubscribeRows`. While `HealthOwner` holds every domain this starts no watch, and it honours the parent lease contract. One owner's parts of one task share the lease, which is released only when its last part closes (a per-owner reference count of parts).
- **Idempotence.** A repeated `OpenRows` for the same (owner, part) is a no-op that returns a fresh bootstrap. A `CloseRows` for an unknown (owner, part) is a no-op.
- **The subscription generation.** Each `OpenRows` returns a `subGen`. `CloseRows` and `RowsBootstrap` carry it. A close or bootstrap for an older `subGen` never cancels or re-creates a newer subscription of the same (owner, part). This covers a late Open or Close, and A->B->A.
- **Bootstrap bounds.** A bootstrap (`RowsNow`) is bounded by a 10s deadline and by the caller's cancellation, for example an unmount during bootstrap. If `OpenRows` fails after acquiring, it releases what it took.
- **Incarnations and Final.**
  - The frontend keys rows by (cluster, incarnation).
  - A Final summary (or tombstone) clears the cluster's rows and fences outstanding requests: replies at or below `finalizedThrough` are dropped.
  - A newer incarnation resets the held `Ver`, any A4 deadline and pending requests.
  - An echoed request generation alone is not an ownership or incarnation fence. Replies are checked against (incarnation, subGen, requestGen).

Tests:

- two consumers of one part, with one closing;
- unmount during bootstrap;
- close then reopen;
- A->B->A;
- an old Final, or an old reply, arriving after a replacement Observer.

## 3. Time

Rows are pushed when a part's `Ver` advances. Relative fields such as `ageSeconds`, `readySinceSeconds`, `expiresInSeconds` and `lastRefreshAgeSeconds` would therefore freeze between pushes. Today they are recomputed every tick with a 30s heartbeat.

The rows DTOs therefore carry absolute times (unix seconds) alongside or instead of the relative ones, and lenses tick them with the existing `useNow`. Time-dependent rules need no frontend logic:

- certificate expiry;
- ESO refresh overdue;
- the Karpenter launch deadline;
- the Helm pending deadline (still a snapshot, see §1).

The Observer's reclassification timer re-evaluates at each rule's `Next` and advances `Ver`, so the rows and rule are re-pushed at the boundary.

## 4. One number per task (A4), in the frontend

A new `bridge/healthRows.ts` and a store slice hold `rows[cluster][task/part] = {incarnation, Ver, part, rows, evaluatedAt, state}`.

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

## 4a. One count-selection policy (review P1)

One selector, `taskCounts(cluster, task, part)`, serves every surface: the lens header, the nav tree and the mast.

- For a part whose rows are on screen, the rows' counts win in all three surfaces.
- Rows newer than the summary win immediately.
- Under A4 all three switch to the summary together at the deadline.
- For other parts the summary's counts are used.

The selector lands now. The mast and tree are wired to it in slice 4.

Equality is scoped honestly. The global task counts are the part's counts:

- verdict parts (pods, workloads, nodes): `Counts`;
- native parts: `Native.Included` and its roles.

A namespace-, kind- or search-filtered list is never compared to those totals. It shows its own visible and filtered counts, labelled as such ("12 of 340 shown"), beside the global count.

The Gateway routes part gets its own DTO and converter for its reduced `Attached` rows (route identity, worst parent, role and label, attachments). The `GatewayRef` converter does not cover them.

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
   - The `fluxPresent` it was evaluated with travels with the rows (§2a).
   - Evidence follows §5a: read in full (live or stale) is complete, and the freshness comes from coverage.
9. **Pods** (`pods/pods`):
   - The Observer is all-namespaces and uncapped. The namespace filter moves to the frontend. The 5000 cap and its `Truncated` flag go, and the virtualized list holds the full set.
   - Payload size is measured on the kind lab (5000 pods, all rows open) before this lens switches. If a full-rows push under churn is too heavy for the webview, the fallback is per-subscription namespace filtering in the converter, which needs per-owner payloads. It is decided by measurement, not now.

## 5a. Coverage and removal semantics (review P1)

**Coverage.** The rows payload carries each kind's `KindCoverage` unreduced:

- the five states: live, stale, checking, unreadable, absent;
- class, error and `Discovery` gaps;
- `AsOf` and `LastGoodAt`.

The lens maps it to its banners:

- a stale kind is "last good at HH:MM";
- an unreadable first read is "could not be read (class)";
- an absent kind is "not installed";
- a discovery gap is named.

None of these is ever reduced to loaded/error booleans.

**Removal.** A selected object missing from the rows is shown "removed" only when its own counted kind is live and the row set is complete. A failing dependency never removes it, and never erases rows of a readable sibling kind (for example Issuers versus ClusterIssuers). Otherwise the selection is kept with its last row, marked unverified or last good.

**Joins.** Cross-part joins (issuer to certificates, store to external secrets, source to reconciliations, usage counts) carry the qualifiers of the side they read:

- a usage join is qualified by the data it consumes: an issuer's certificate count by the Certificates coverage, a store's secret count by the ExternalSecrets coverage (unverified, not zero, when that kind is unreadable);
- the issuer's or store's own coverage separately qualifies its health ("last good" when stale).

**Read completeness versus freshness.** A stale dependency is still read-in-full evidence (its last complete read). This is current `evalWorkloads` behaviour, and the workloads verdict is evaluated from it, as a last-observed verdict. The rows preserve that evaluated verdict as captured. The lens shows freshness from the coverage, so a verdict from a stale capture is labelled "last observed", never current. "Complete" means read in full (live or stale), never "fresh".

Tests:

- a stale and then recovering dependency;
- mixed issuer coverage (Issuers live, ClusterIssuers forbidden) and mixed store coverage;
- a missing selection during the A4 fallback window.

## 5b. Direct details (review P2)

Detail endpoints that read the lazy stores today (Flux `GetResourceDetail` and `ResolveGitLink`, the cert-chain issuer lookup) move to direct GETs:

- The GET runs at the kind's served version (the Observer's probe result), under a 15s context deadline and the caller's cancellation.
- A NotFound on a version re-probes once.
- NotFound (the object is gone) is distinguished from forbidden and transport failures (the object is unverified).
- Each detail returns its object's UID, generation and as-of time.

The inspector accepts a detail only if its UID equals the selected row's UID. A replacement object with the same name never becomes the old selection's detail, and never retargets a pending verb (verbs carry the UID they were armed against).

A chain lookup failure stays an explicit warning ("issuer unverified: forbidden"), never a silent gap.

The lazy stores are removed only after same-name recreation and permission-failure tests pass on the GET path.

## 6. Removals

These go once their last reader has moved:

- the lazy watches `OpenGitOps`, `OpenCertManager` and `OpenExternalSecrets`, with their push loops and events;
- `OpenLivePods` and `OpenLiveWorkloads`, with `WatchDirty`.

The eager typed node informer goes when the Nodes lens reads rows. The connection's node counts then come from the nodes kind through a native Ready tally taken from the same capture as the kind state: a per-kind tally hook evaluated under the Observer's lock and carried on `KindState`. The nodes part's `Counts.OK` is not a substitute: a Ready=True node under pressure or cordoned counts as Ready in `NodeReadiness` but not as OK.

The request methods `ListWorkloads`, `ListPods`, `ListNodes`, `ListApplications` and `ListGateways` stay while `bridge/attention.ts` and `bridge/fleetboard.ts` call them. Those go in slices 4 and 5.

## 6a. The webview gate, set before measuring (review P2)

Uncapped, all-namespace Pods rows are the intended data contract. Virtualisation bounds DOM work, not JSON decoding, store copies, sorting or GC, so the switch is gated.

The gate is measured in the real Wails webview on the kind lab, with 5000 pods, 50 pod updates/s, and every lens consumer open (Pods, Workloads, Nodes, the summary):

- **payload:** one push per part per second at most, and each pods push ≤ 4 MB of JSON;
- **main thread:** event receipt to store committed (decode, merge, sort) ≤ 50 ms p95 per push, so ≤ 5% of the main thread;
- **interaction:** a keystroke in the lens search or a filter click, to paint, ≤ 100 ms p95 during churn;
- **memory:** the WebContent process RSS steady ≤ 400 MB, with no growth beyond 10% over 5 minutes of churn.
  - Amendment, owner-ratified 2026-10-12 (addendum `2026-10-12-klyx-health-1c-slice3-rows-transport.md` §6.3): the webview grows in steps to a plateau even with no churn and no interaction, so the growth limit is read after its warm-up. A memory run churns for at least 15 minutes with the lens on screen; the 400 MB ceiling holds for the whole run, from launch; the 10% growth limit applies to the last 5 minutes of churn.

If any limit is missed, the transport changes before the Pods lens switches. Per-owner namespace-filtered payloads are the candidate. That transport (owner and scope identity, separate global part counts and visible counts, no shared broadcast name) is reviewed before it is built.

**Truncated.** `Truncated` goes away only for a confirmed complete Observer read (live or stale, read in full). A checking or unreadable pods kind is incomplete and is shown as incomplete, never as a full list.

## 7. Acceptance

For every lens moved:

- the lens's task counts equal the summary part's counts at one `Ver`, under the §4a classifier- and scope-specific rule (Counts for verdict parts, Native for native parts; filtered and visible counts labelled separately, never compared to totals);
- no lens watch is open for its kinds;
- the lab rerun shows the WATCH delta accordingly.

A kind lab run with every lens open reports RSS and the rows payload rates.

Per-lens gates (slice 3) are distinct from mast and nav parity (slice 4). Replacing a fetch lens (Argo, Karpenter, Gateways) by itself removes no watch. The WATCH delta drops for the push lenses' lazy watches and `OpenLive*`, and separately for the typed node informer's watch when the Nodes migration removes it.

## 8. Order of work

1. Infrastructure first, with its tests, before any lens:
   - the row envelope in the evaluators;
   - the converter registry, starting with certificates;
   - `OpenRows`, `CloseRows` and `RowsBootstrap` with leases and fences;
   - the frontend rows store with A4 and the shared count selector.
2. Then lenses in the §5 order, one reviewed commit each.
