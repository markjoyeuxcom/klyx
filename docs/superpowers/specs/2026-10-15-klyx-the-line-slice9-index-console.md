# The Line, slice 9: the fleet metadata index and the console

Design note for slice 9 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §8.4 (the fleet metadata index) and §6 (the command console), with §14's bounds. The parent is the contract; this note records what exists, how the slice is cut and the points the parent leaves open.
- **Revision 2** answers the design review of f152e87 (five findings, seven rulings; §8).

## 0. What exists (surveyed 2026-10-15)

- **The palette** (`chrome/CommandPalette.tsx`, `chrome/commands.ts`) is built in the frontend from the loaded slices of the open cluster: pods, nodes, workloads, Helm, Flux and Argo rows, Gateways (Observer rows) and HTTPRoutes (the topology). Ids are not cluster-qualified, and nothing is asked of the backend.
  - Groups are Application, Clusters, Views, Resources, Objects and Actions; the empty query lists every unqualified command by frecency (`localStorage`, 300 entries). Results stop at 50 with "+N more".
  - ↵ runs; there is no ⌘↵, no alt+V and no focus trap. The panel is 560px wide, 12vh from the top.
  - "Ask about <cluster>" opens the chat inside a cluster.
- **The Observer** watches, per cluster, every kind of every domain (the health owner acquires them all at `Start`): pods, Deployments, StatefulSets, DaemonSets, ReplicaSets, nodes, namespaces, the seven Flux kinds, Argo Applications and ApplicationSets, GatewayClasses, Gateways, HTTPRoutes, and Services and EndpointSlices where the Gateway API is served, cert-manager Certificates, Issuers and ClusterIssuers, ESO ExternalSecrets and (Cluster)SecretStores, and Karpenter NodePools and NodeClaims.
  - **Projections:** ReplicaSets and Services keep identity and creation time (`ProjectOwnerMeta`) but no labels; namespaces keep name and UID only (`ProjectNameMeta`); the other kinds keep their labels. No projection keeps `resourceVersion`.
  - **The model** (`kindGen.model`) is written under `Observer.mu` by the reflector's store adapter; a value is replaced on update, never mutated. `WatchKind` delivers a kind's `KindState` (generation, seq, coverage, count) newest-wins, never holding the lock.
  - **The ledger** keeps, per ledger part and counted source, the roles of the last committed evaluation by UID (`partition.entries`), healthy and unevaluated members included, each partition capped at 50,000. It is loop-owned: `evaluate` captures under `mu`, releases it, and commits on the loop goroutine. A partition is live or frozen (a loss was emitted); a capped source has no partition. It exists only with a transition sink (the app always has one).
  - There is no public accessor for a kind's model or for a role by UID.
- **Not watched** from §8.4's defaults: Jobs, CronJobs, Ingresses, GRPCRoutes, ConfigMaps, Secrets, PVCs, and Services on clusters without the Gateway API.
- **Lifecycles:** `ClusterConn` builds one Observer per connection and keeps it across Stale, Failed and recovery; it hands the Observer the connection word in a serialized handoff (`syncHealth`). The Observer never stops on a lost connection (its reflectors go stale) and has an incarnation. There is no cluster removal path.
- **client-go's Reflector** accumulates every page of a paginated list into one slice before `Replace` (v0.36.2 `reflector.go`, `pager.go`), so page size alone bounds nothing it retains. **The metadata client's** Accept header ends with a bare `application/json` fallback (`metadata.go`), so a server without metadata support answers it with whole objects.
- **Navigation:** `reveal` forwards no UID to the `select-pod`, `select-workload`, `select-node` or `select-flux` intents, which have none; `usePinnedSubject` pins the first row it sees at a key. `openInstance(ns, name)` (Resources) carries no UID either. Recent places (`nav/history.ts`, 12), the fleet axes (`nav/fleetAxes.ts`) and the inspector pin (`nav/inspectorPin.ts`, `INSPECTOR_IDENTITY_ID`) exist. "Opening beside" does not.

## 1. Sub-slices

| | Builds |
|---|---|
| **9a** | The index (Go): the store and its admission with Pod priority, the bounded acquisition for the kinds the Observer does not watch, the visit feed for the kinds it does, per-kind states, the connection lifecycle, the `index` block in fleet.yaml |
| **9b** | The search (Go) and the role view: `IndexService.Search(ctx, query, limit)` with bounded top-K, `Observer.RolesNow` served on the loop, the coverage line |
| **9c** | The console (frontend) and the expected-UID path: the 760px overlay, the four groups, rows, ↵ and ⌘↵, verbs only, protected verbs, the request fence; UIDs through `reveal`, the select intents, `openInstance` and `usePinnedSubject` |

Each is Codex-reviewed on its own, in this order.

## 2. 9a: the index

### 2.1 Entries and sources

- **An entry** is `{gvr, kind, uid, namespace, name, labels (at most 2 KiB, with a truncation marker), created}`, held in a per-cluster, per-kind map by UID. It is built only after it is admitted (§2.3): its size is computed from the source object first.
- **Kinds the Observer watches** are read from its model, never with a second watch (§2.4). Their entries keep what the projection kept: ReplicaSets and Services have no labels, namespaces have neither labels nor creation time, and the entry says so ("labels not indexed").
- **Other configured kinds** are acquired by the index itself (§2.5).
- **Matching** a configured kind to an Observer kind is by group and resource, whatever the version: the Observer's kind wins whenever it is served on that cluster, so configuration can never add a second watch of it. Services follow the Gateway API: the index watches them only while the Observer's Services kind is not served, and hands over (dropping its own watch and entries, then visiting the Observer's) when it becomes served, and back when it stops.

### 2.2 Default kinds and configuration

- **Defaults:** §8.4's list: namespaces, nodes, Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs, CronJobs, Pods, Services, Ingresses, Gateways, HTTPRoutes, GRPCRoutes, ConfigMaps and Secrets (name only, as all are), PVCs, and the Flux, Argo, cert-manager and ESO kinds. A kind not served on a cluster reads "not served" there.
- **`index:` in fleet.yaml:** an optional top-level block whose `kinds:` list (`group/resource` or `group/version/resource`) replaces the defaults.
  - Absent block, or absent `kinds:`: the defaults.
  - `kinds: []`: no kinds; the console says "the index is configured with no kinds".
  - Entries are deduplicated by group and resource (a version is a preference, used only when served); a malformed entry is named in the settings warning and ignored.
- **Limits** are configurable in the same block (`entriesPerCluster`, `mibPerCluster`, `entriesFleet`, `mibFleet`); absent, §14's.

### 2.3 Admission, priority and freezing

- **One shared budget** (`Budget`), atomic across kinds and clusters, admits every addition and every growth before anything is retained: an entry's estimate (§2.6) against both the cluster's and the fleet's entries and bytes.
- **Pods are capped first** (§14.11). Two ceilings:
  - every kind is admitted up to the full caps;
  - a Pod is admitted only while the cluster and the fleet stay at or under 80% of each cap (40,000 entries and 25.6 MiB per cluster; 160,000 and 102.4 MiB across the fleet), so 20% of every budget is always left to the other kinds.
  - **Acquisition order** makes contention deterministic: each cluster reads its kinds' first states one at a time, Observer-backed non-Pod kinds first, then the index-acquired kinds in configuration order, Pods last.
- **At a cap** a kind stops: an index-acquired kind's list stops or its watch is closed; an Observer-backed kind is no longer visited. What it holds is **frozen** with its as-of time and reads "capped at N · as of <time> · stale". Within an acquisition generation a frozen kind never rotates, refills or applies deletes. Other kinds go on until they too would cross.
- **A new acquisition generation** (§2.7: recovery, a new incarnation, a configuration change) re-reads every kind, frozen ones included, in place: an unchanged entry costs nothing, a new one is admitted, a vanished one is released after the read completes. A re-read that crosses a cap freezes again.
- **The Observer is never stopped**; only its derived entries are capped.

### 2.4 The Observer's kinds: the visit feed

- **Accessor:** `Observer.VisitKind(kind, visit func(obj any) bool) (KindState, bool)` calls `visit` for each object of the kind's current model under `Observer.mu` and returns the kind's `KindState` captured in the same critical section, so entries and their generation, seq and coverage agree. `visit` must not retain `obj` or call the Observer, and returns false to stop. Nothing of the kind is copied.
- **The index's visit** is a mark-and-sweep against the kind's entries:
  - an unchanged object (same UID, name, namespace, creation time and label hash) is marked, with no allocation;
  - a new or grown object is admitted, then built;
  - at a refused admission the visit stops and the kind freezes (§2.3);
  - after a complete visit, unmarked entries are released.
- **Feed:** `WatchKind` notifications, coalesced newest-wins, trigger a visit at most once per second per kind; a visit is skipped while the kind's `KindState` is neither Live nor Stale (no count is known). Entries record the generation and seq they were read at.
- **Cost:** a visit holds `Observer.mu` for one map walk; a benchmark gates a 50,000-object steady-state visit (lookups, no allocation) and a first fill.
- **Lock order:** `Observer.mu`, then the index's per-cluster store lock, then the budget's. The index never calls the Observer while holding its own locks.

### 2.5 Index-acquired kinds: bounded acquisition

- **No Reflector and no DeltaFIFO.** The index runs its own list and watch per kind, so nothing accumulates pages or queues events:
  - **A strict Accept header.** Both are plain REST requests whose Accept names only the metadata forms (`application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1` for lists, `...;as=PartialObjectMetadata;...` for watches). The metadata client is not used: its Accept ends with a bare `application/json` fallback, under which a server that cannot serve metadata would send whole objects, Secret data included. A server that cannot answers 406, which reads "metadata unsupported".
  - **Lists** are paginated at 500 and stream-decoded: the response is walked token by token, and each item is decoded, projected, admitted and applied before the next is read. A page never exists whole; the list's `resourceVersion` and `continue` come from its metadata, wherever it appears. An expired `continue` (410) restarts the list in place (mark-and-sweep).
  - **Watches** are stream-decoded the same way, one event at a time, each applied before the next is read; nothing is queued.
  - **The projection** keeps the entry's fields and drops everything else, annotations and `managedFields` included, before the event is applied.
- **One list at a time per cluster**, in the §2.3 order; each kind's watch starts when its first list completes.
- **Relists** (watch expiry, recovery) are in place: mark, apply, sweep; there is no second copy of the kind during replacement. A relist admits new objects before it can release vanished ones, so it can read capped where a settled kind would not, and says so.

### 2.6 The estimate

- **What it counts** (a documented, conservative estimate; the estimate is the accounting):
  - per entry, a fixed overhead covering the entry struct and its share of the per-kind map, plus its strings (UID, namespace, name) and labels (a fixed per-label overhead plus key and value bytes);
  - per kind, a fixed container overhead;
  - per active stream (a list or a watch), a fixed decode reserve of 256 KiB, charged when it opens and released when it closes;
  - the search's working set (§3), a fixed reserve charged once.
- **Constants** are calibrated by test: the retained heap after GC per entry, per label and per kind must stay at or under the constants.
- **What it cannot bound by count:** one decoded object per active stream; the API server's object size limit bounds it, and an object larger than the reserve exceeds it transiently. The note says this rather than claiming it.

### 2.7 Lifecycle and connection

- **One coordinator per cluster**, created with the cluster's connection and stopped with the app. It runs **acquisition generations**: every list, watch and visit carries its generation, and the store applies a result only for the current one (checked under the store lock); a cancelled generation's late results are dropped.
- **The connection word** reaches the coordinator in the same serialized handoff that gives it to the Observer (`ClusterConn.syncHealth`), together with the Observer itself.
  - **Usable** ("connected", "connected · <reason>"): a generation runs.
  - **Down** (stale, unreachable, connecting): the generation is cancelled at once (its lists and watches stop; no visits), and every kind keeps its last-good entries, reading "stale · last good <time>".
  - **Recovery** in the same incarnation starts a new generation: a fresh list of every index-acquired kind and a visit of every Observer-backed kind, in place.
  - **A new incarnation** (a new Observer) starts a new generation the same way.
  - **Final:** when the Observer ends, the coordinator stops and releases the cluster's entries; the cluster reads "not indexed".
- **Coverage** names every configured cluster: one that has never become ready reads "not indexed: <connection word>", never silently absent.

### 2.8 Per-kind states

indexing (the first read), live, capped (frozen, as of), denied (403 on list), watch denied (403 on watch after a good list: the list kept, stale), metadata unsupported (406; never a content read, Secrets included), stale / recovering (watch errors, retried with backoff; or the connection is down), not served, configured out. Each carries its as-of time where it holds entries.

## 3. 9b: the search and the role view

- **The call:** `IndexService.Search(ctx, query, limit)` (context first, so the console's cancel stops it) returns ranked hits and the coverage.
  - Matching is over name, then namespace/name, then kind; case-insensitive substring and prefix scoring (names are compared lower-cased without storing a copy).
  - Order: score, then cluster order, kind, namespace, name: deterministic.
  - **Working storage** is a top-K heap of at most `limit` (at most 200) hits; searches run one at a time, and a newer one cancels the older. The scan holds each cluster's store read lock for that cluster's pass only.
  - Same-name objects on different clusters are separate hits, each with its cluster, UID and the incarnation and acquisition generation it was read under.
  - A query under two characters returns no hits (the empty query is the frontend's).
- **The role view:** `Observer.RolesNow(ctx, uids) (map[uid]RoleView, bool)` is served on the Observer's loop goroutine (like `RowsNowCtx`), so it reads the committed ledger without racing a commit. For each UID it reports one of:
  - **current:** an evaluated entry in a live partition: its role, label and the commit time;
  - **last observed:** an entry in a frozen partition (a loss was emitted: relisting, disconnection, a gap): its role and the time it was last committed, never presented as current;
  - **unverified:** no entry, an unevaluated member, a capped source, or no ledger for the kind.
  - Where several parts hold a UID, a current entry wins over a last-observed one, and the most severe current role wins.
- **A hit's aspect** comes from `RolesNow` on that cluster's Observer, fenced: if the Observer's incarnation is not the one the hit was indexed under, or the call fails or is cancelled, the hit is "health unverified". An object of a kind the Observer does not evaluate is always "health unverified", never healthy.
- **The coverage:** "Searching 7 clusters · 14 kinds", then a note for every cluster that is not indexed, stale, or has a kind building, capped, denied, watch-denied, unsupported or stale, naming the lens to search instead.

## 4. 9c: the console and the expected UID

### 4.1 The console

- **The overlay** (§6): 760px wide, 88px from the top, no animation. A focus trap keeps Tab inside; Tab moves focus normally.
- **The empty query**, in order: recent places (`nav/history.ts`), the fleet indicator's affected clusters (`useFleetAxes`), and the verbs for the current selection.
- **Groups:** Objects (the index), Run (verbs), Go to (places, clusters, views and resources) and Ask (the chat). Today's groups map onto these.
- **An object row:** its `Aspect`, kind, name, `LinePos` (with the cluster's short code) and a note (namespace; "health unverified"; "last observed <role> at <time>"; "as of <time>" for a stale or frozen kind).
- **The request fence:** each open of the console is a session; each search request carries the session and a sequence number, is debounced (150 ms) and cancels the previous one, and a reply is applied only when both match the latest request (so A → B → A never shows the first A's reply).
- **Verbs only:** a toggle button in the console header (alt+V) that hides every group but Run.
- **Protected verbs** keep ▲, the "type the cluster name" confirmation and the grant flow (they open the existing dialogs).

### 4.2 Opening, with the expected UID

- **↵ opens** the object: it navigates to its lens and selects it, expecting the hit's UID, and closes the console.
- **⌘↵ opens beside:** the same selection, with the console kept open over the lens list as an inspector preview. Focus stays in the console for repeated previews; when the console closes, focus goes to the revealed inspector's identity (`INSPECTOR_IDENTITY_ID`), never to a target that is no longer mounted.
- **The UID travels:** `reveal` forwards `ref.uid`; the `select-pod`, `select-workload`, `select-node` and `select-flux` intents gain an optional `uid`; `openInstance` (Resources, for the index-only kinds and ReplicaSets) gains an optional expected UID.
- **An intent with a UID never substitutes the current same-name row.** Its handler selects the key at once with the expectation (no "missing" toast, no wait), and `usePinnedSubject` gains an optional expected UID, seeded before any row is seen:
  - a row with the expected UID: pinned, as today;
  - a row with another UID: "replaced" on a complete read (with "Open the new object" to adopt it), "unverified" on an incomplete one;
  - no row: "removed" on a complete read, "unverified" on an incomplete one;
  - while no row of the expected object has been seen, the inspector shows a shared gone card (kind, namespace/name, short UID, the state, and the adopt action when replaced) instead of an object.
  - **Resources:** the instance read is authoritative: a different UID is "replaced", NotFound is "removed", any other failure is "unverified".
- **Without a UID** (summary links and other existing callers), behaviour is unchanged.
- **A lens with no inspector of its own** navigates instead and says so on the row.

## 5. Testing

- **9a:**
  - admission at each cap for a list item and a watch event, shared across kinds and clusters; the Pod ceiling (Pod-heavy first reads leave 20% to other kinds; Pod and non-Pod contention across two clusters); label-only growth refused at a cap;
  - freezing: repeated visits after deletes, inserts and label growth at the cap never rotate, refill or apply deletes; a new generation re-reads in place;
  - the visit feed: no second watch (the fake Observer's kind has one reflector), entries agree with the captured `KindState`, mark-and-sweep, throttling, the Services hand-over;
  - the projection: annotations and `managedFields` never retained; labels capped with a marker;
  - streaming lists and watches: a fake API server (httptest) serving paginated metadata lists with large annotations, an expired `continue`, 403, 406, and the request's Accept header carrying no bare `application/json`;
  - per-kind states, watch denied, errors with backoff;
  - the lifecycle: disconnect and reconnect without replacing the Observer (cancellation, stale wording, fresh list in place, late replies from the cancelled generation dropped), a new incarnation, Final, never-ready clusters;
  - peak memory: a 60,000-object Observer kind with labels, a 120,000-object paginated list with 100 KiB annotations per object, label-only growth, fleet contention; retained heap after GC at or under the estimate, and heap growth sampled during acquisition at or under admitted entries plus the stream reserves plus one object;
  - the config block (absent, empty, replacement, deduplication, malformed);
  - the kind lab, read-only.
- **9b:** ranking and determinism, same-name hits across clusters, top-K bounds, cancellation; `RolesNow` under `-race` against concurrent commits: current, healthy before a gap then last observed, relisting, capped, unevaluated, end, recovery; the incarnation fence; coverage notes including never-ready clusters.
- **9c:** the empty query's three parts, the groups, rows with `Aspect`, `LinePos` and notes; ↵ and ⌘↵ with a replacement between search and activation and with the target absent on incomplete and complete reads, in a pinned lens and in Resources; adoption; focus on close; verbs only (alt+V); the focus trap; protected verbs; the session and sequence fence (A → B → A); headless captures in both themes.

## 6. Open points

None beyond the rulings recorded in §8.

## 7. Correction

The first revision said ReplicaSets and Services keep no creation time. They keep it (`ProjectOwnerMeta`); only their labels are dropped. Namespaces drop both.

## 8. Design review of f152e87

Five findings, all taken:
1. **Bounded acquisition:** no full-kind copy (the visit feed, §2.4), no accumulated pages or queued events (streaming lists, unbuffered watches, in-place relists, §2.5), and an estimate that covers entries, containers, stream reserves and the search's working set, validated by peak-memory tests (§2.6, §5).
2. **Disconnect:** acquisition generations cancelled on a down connection, last-good stale coverage, a fresh in-place read on recovery within the same incarnation, and never-ready clusters named (§2.7).
3. **Roles:** `RolesNow` served on the loop, with current, last observed and unverified kept apart and fenced by incarnation (§3).
4. **The UID through navigation:** intents, `openInstance` and `usePinnedSubject` carry the expected UID; a replacement or absence is said, never substituted (§4.2).
5. **Priority and freezing:** the Pod ceiling and a fixed acquisition order (§14.11's "Pods capped first"); a frozen subset never rotates or refills within a generation (§2.3).

Rulings on the seven open points:
1. Accepted: the Observer's metadata is reused, keeping only what its projections keep, through the visit feed (§2.4).
2. Accepted: caps apply to derived entries, the Observer is never stopped, with §2.3's freeze and priority.
3. Variant accepted: a documented conservative estimate covering containers, buffers and the search, validated by peak-memory tests (§2.6).
4. Accepted: the Observer is the one role authority, through `RolesNow` (§3).
5. Accepted: search in Go, cancellable, bounded top-K, and replies fenced by session and sequence (§3, §4.1).
6. Accepted: ⌘↵ keeps the console open as a preview; focus stays in it, and on close goes to the inspector's identity (§4.2).
7. Accepted: `kinds:` replaces the defaults; absent and empty are distinct, and equivalent resources are deduplicated so configuration cannot add a second Observer watch (§2.1, §2.2).
