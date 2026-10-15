# The Line, slice 9: the fleet metadata index and the console

Design note for slice 9 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §8.4 (the fleet metadata index) and §6 (the command console), with §14's bounds. The parent is the contract; this note records what exists, how the slice is cut and the points the parent leaves open.
- **Revision 2** answers the design review of f152e87 (five findings, seven rulings; §8). **Revision 3** answers the re-review of 13417b2: a bounded scanner replaces JSON decoding, so the stream reserve is enforced (§2.5, §2.6, §8).

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
  - **A bounded scanner, not a JSON decoder.** `encoding/json`'s Decoder buffers a whole value before decoding it and keeps the enlarged buffer afterwards (a 1 MiB annotation leaves a 2 MiB buffer behind), and client-go's decoders do the same. The index reads every response through its own scanner instead:
    - a fixed 32 KiB read buffer, never grown;
    - it walks the JSON byte by byte and keeps only the entry's fields: name, namespace, UID, resourceVersion and creationTimestamp (each at most 1 KiB), and labels (the first 2 KiB in the order sent, which is key order from a Kubernetes API server; every label is hashed, whatever is kept);
    - every other value (annotations, `managedFields`, `kind`, any status) is skipped as it streams past, never buffered, however large;
    - a kept field over its bound (no valid Kubernetes object has one) stops that read: the kind reads "oversize", keeping what it admitted, like a cap.
  - **Lists** are paginated at 500 and scanned item by item: each item is projected, admitted and applied before the next is read. A page never exists whole; the list's `resourceVersion` and `continue` come from its metadata, wherever it appears. An expired `continue` (410) restarts the list in place (mark-and-sweep).
  - **Watches** are scanned the same way, one event at a time, each applied before the next is read; nothing is queued. An `ERROR` event's status (code, reason, message, each bounded) is scanned the same way.
- **One list at a time per cluster**, in the §2.3 order; each kind's watch starts when its first list completes.
- **Relists** (watch expiry, recovery) are in place: mark, apply, sweep; there is no second copy of the kind during replacement. A relist admits new objects before it can release vanished ones, so it can read capped where a settled kind would not, and says so.

### 2.6 The estimate

- **What it counts** (a documented, conservative estimate; the estimate is the accounting):
  - per entry, a fixed overhead covering the entry struct and its share of the per-kind map, plus its strings (UID, namespace, name) and labels (a fixed per-label overhead plus key and value bytes);
  - per kind, a fixed container overhead;
  - per active stream (a list or a watch), a fixed reserve of 256 KiB, admitted before the request is sent and released when it closes: the scanner's 32 KiB buffer and its bounded scratch (about 8 KiB), and the transport's fixed per-connection buffers (TLS records, HTTP framing, a gzip window). A stream that cannot be admitted is not opened, and its kind reads capped;
  - the search's working set (§3), a fixed reserve charged once.
- **Constants** are calibrated by test: the retained heap after GC per entry, per label and per kind must stay at or under the constants.
- **Nothing outside it:** no read grows a buffer with the size of an object, so the reserve is a bound, not an allowance.

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

indexing (the first read), live, capped (frozen, as of), oversize (frozen at an object over the scanner's bounds, as of), denied (403 on list), watch denied (403 on watch after a good list: the list kept, stale), metadata unsupported (406; never a content read, Secrets included), stale / recovering (watch errors, retried with backoff; or the connection is down), not served, configured out. Each carries its as-of time where it holds entries.

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
  - the scanner: equivalence with `encoding/json` on generated metadata (fuzzed), escapes and unicode in kept and skipped strings, nested skipped values, a kept field over its bound (oversize, evidence kept), truncated and malformed input;
  - peak memory: a 60,000-object Observer kind with labels, a 120,000-object paginated list with 100 KiB annotations per object, label-only growth, fleet contention; and eight open watches each delivering one object with a 1 MiB annotation and 10 MiB of `managedFields`, then many small events: heap after GC, with the large values gone and the watches still open, at or under the admitted entries plus the charged reserves; retained heap after GC at or under the estimate throughout;
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
1. **Bounded acquisition:** no full-kind copy (the visit feed, §2.4), no accumulated pages or queued events (scanned lists and watches, in-place relists, §2.5), and an estimate that covers entries, containers, stream reserves and the search's working set, validated by peak-memory tests (§2.6, §5). Re-review of 13417b2: the uncharged one-object exception is gone - a fixed-buffer scanner skips what it does not keep, so the 256 KiB reserve is enforced (§2.5, §2.6).
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

## 9. As built: 9a

- **Package** `internal/fleetindex`: `budget.go` (the shared budget, `Class` and the Pod ceiling, `PodPercent` 80), `entry.go` (entries, the estimate, the label cap and an order-independent label hash), `scan.go` (the bounded scanner), `stream.go` (`Reader`: `ListPage` and `Watch`), `store.go` (a cluster's store: mark-and-sweep, freezing, the generation fence), `kinds.go` (`DefaultKinds`, `ParseKinds`, `FromConfig`), `coord.go` (`Index` and the per-cluster coordinator).
- **The Observer** gained `VisitKind(kind, visit) (state, complete, ok)`, `Kinds()` and `KindState.GVR` (the effective served GVR).
- **Wiring:** `main` builds the index from fleet.yaml's `index` block (`kinds`, `entriesPerCluster`, `mibPerCluster`, `entriesFleet`, `mibFleet`; malformed kinds are logged as config warnings at start) and hands it to the registry (`Registry.SetIndex`), which gives it to every conn and tells it the fleet's clusters in order. A conn attaches its Observer when it builds it and hands the index its connection word in `syncHealth`, right after the Observer. The index has no consumer until 9b.
- **The read path** (`ClusterConn.indexAcquire`): the conn's REST config, copied with no client timeout (every read is bounded by its context), through `rest.HTTPClientFor`. Discovery is the conn's CRD probe; the core group is always `v1`, unprobed.
- **What the API server sends** (checked against the kind lab): the Content-Type of a metadata answer is a bare `application/json`; the metadata form is named in the body. So the scanner requires the list's `kind` (`PartialObjectMetadataList`) before its `items`, and `PartialObjectMetadata` as the kind of every item and every watch event; anything else is "metadata unsupported" before an item is applied. A non-JSON Content-Type is unsupported too.
- **Hand-over:** an Observer kind becoming served or absent (Services with the Gateway API, a CRD installed or removed) starts a new acquisition generation, which re-plans and re-reads in place.
- **The estimate's constants**, calibrated (`TestEstimateCoversRetainedHeap`): 256 B per entry, 32 B per label list, 64 B per label, 4 KiB per kind; the charge exceeds the retained heap by 15-35% across 0 to 40 labels.
- **Measured:**
  - a steady-state visit of 50,000 objects takes 5.6 ms with 3 allocations; the first fill 22 ms;
  - streaming 1,000 objects of 100 KiB (50 MiB pages) peaks at 3.5 MiB of heap growth;
  - eight open watches, each after an 11 MiB object and 1,000 small events, retain 2.4 MiB against 4.9 MiB charged;
  - a 60,000-pod Observer kind stops at 40,000 entries, retaining 11 MiB against 23 MiB charged (the entries share the Observer's strings);
  - the kind lab: every default kind settles live in 149 ms, 412 entries.

### 9.1 Review of c03308d

Nine findings, all taken:
- **Transport buffers:** the index reads over HTTP/1.1 only (`fleetindex.NewReader` sets `NextProtos`): over HTTP/2 client-go's transport buffers up to a 4 MiB stream window ahead of the scanner. The reader closes its idle connections after every stream, through client-go's wrappers (`CloseIdleConnectionsFor`), so only open streams hold buffers; an unread body holds about 100 KiB, inside the 256 KiB reserve. Re-review of 06eb92e: response headers are bounded too (64 KiB, `HeaderBytes`; Go's default is 10 MiB). The reader's base transport is its own clone of client-go's cached one (never mutated, since it may be shared), so an oversized header fails the request before a watch is opened. Re-review of e76155a: unwrapping client-go's transport also leaves its CA-file rotation behind, so a CA read from a file is rotated by the index's own holder (`caHolder`, client-go's cadence of 5 minutes): on a changed, parseable file it swaps in a clone of its transport with the new roots (the header bound kept) and closes the old one's idle connections. Re-review of 312db52: the holder takes its roots and their committed bytes from one read of the file (client-go's cached transport, keyed by the file's path, may trust an older CA than the file holds); unreadable or unparseable at start, it keeps client-go's roots with nothing committed, so the first request re-reads.
- **Map capacity:** a kind's map slots are charged at their high-water mark (64 B each; an entry itself is 192 B), because a Go map never shrinks. A kind whose entries fall to a quarter of at least 1,024 slots is compacted, admitting the new map before the old capacity is released.
- **No input-sized copies:** `VisitKind`'s state carries no tally (computing one copies the model). An Observer object's labels are chosen with a bounded heap of the 64 smallest keys, never by sorting every key.
- **Label count:** at most 64 labels are kept (`MaxLabels`) as well as 2 KiB; empty keys are never kept.
- **An expired continue** restarts the list under a fresh mark, so the abandoned snapshot's objects are swept.
- **Recovery:** an accepted watch restores the kind to live.
- **Counts:** coverage reports the entries the store holds now, watch events included.
- **Validation:** a page completes only with its whole envelope (the list's kind first, metadata with a resourceVersion, items) and nothing after it. Items and events need a UID, and an unknown event type is malformed. Every skipped value is validated as it streams (grammar, escapes, control characters, nesting at most 512 deep).
- **Continue tokens** are kept whole up to 4 KiB (the scratch is sized to the largest bound) and refused past it as oversize.

## 10. As built: 9b

- **The role view:** `Observer.RolesNow(ctx, []RoleQuery{UID, Source}) ([]RoleView, bool)` is served on the loop goroutine through its own request channel, beside the rows bootstrap. A query names the Observer kind the object came from, so a capped source and a kind with no ledger are told apart from an object not observed:
  - **current:** an evaluated entry in a live partition, with its role, label and commit time;
  - **lastObserved:** an evaluated entry in a frozen partition, with its last commit time;
  - **unverified**, with its reason: `not evaluated` (an unevaluated member), `the role list is capped`, `no ledger for this kind`, `not observed`.
  - Across ledger parts counting the same source, current beats last observed beats unverified, and among current entries the worst role wins (`RoleRank`).
- **The search:** `Index.Search(ctx, query, limit)`, one at a time.
  - Each kind records what it is on its cluster (display name, served GVR, the Observer kind when visited).
  - A query is lower-cased and its spaces collapsed. A first word followed by more restricts the kind ("pod web"); a slash matches namespace/name.
  - Scores: an exact name 1000, a name prefix 800 less the extra length, a substring 600 less its position, namespace/name 1000, 700 or 500, and a kind alone 100 (every object of the kind, after every name match).
  - Ties break by fleet order, kind, namespace, name and UID. A top-K heap holds at most `limit` (at most 200) hits, and `Total` counts every match.
  - Each hit records the Observer incarnation and acquisition generation it was read under. Roles are read with one `RolesNow` per cluster, only while that cluster's Observer is the incarnation the hit was indexed under; otherwise, or when the Observer does not answer, the hit is unverified. A kind the Observer does not visit is unverified ("no ledger for this kind").
- **`IndexService.Search(ctx, query, limit)`** (bound) returns the hits (cluster, kind, group/version/resource, namespace, name, UID, the kind's state and as-of time, the role word), `Total`, the coverage of every cluster (connection word, indexed or not, each kind's state, entries, as-of and error), and the counts for the coverage line: indexed clusters, and configured kinds readable on at least one of them. A cancelled search answers "search cancelled".
- **Measured:** 50,000 entries searched in about 17 ms under `-race`.

### 10.1 Review of 647f21c

Five findings, all taken:
- **Recorded but uncommitted changes:** `RolesNow` first evaluates every dirty ledger part that counts a queried source, as a rows bootstrap does. A lost connection, a relist or a mutation the Observer has recorded is committed (its loss or after-gap emitted, once) before a role is read, so a role is never current across a recorded loss.
- **A hit's identity:** each entry records the acquisition generation and Observer incarnation it was last read under, refreshed whenever a read sees it. A hit carries its entry's identity, never the attachment's current one, so an entry a new incarnation has not re-read reads unverified.
- **The fence after the call:** a cluster's role answer is accepted only if the same Observer is still attached, with the hit's incarnation, after `RolesNow` returns.
- **Cancellation:** searches take a one-place slot whose wait honours the caller's context.
- **The working set:** each search admits its own reservation against the fleet budget, refused as `ErrSearchBudget`. It covers 64 KiB fixed, 1 KiB per possible hit (candidate, Hit, role query and answer, DTO), 512 B per cluster and 256 B per kind of coverage, and is released by `Result.Release` after the DTO is built.
  - Matches are kept as compact candidates in a heap preallocated to `limit`; Hits are built only for the winners.
  - A query over 512 bytes (`MaxQueryBytes`) can match no name and searches nothing, before it is normalized.
  - Measured: a 200-hit search holds 69 KiB while waiting on roles, against 264 KiB reserved.
- **Re-review of 3436289:**
  - A candidate keeps its entry's identity strings, never the entry: an entry's labels would outlive a concurrent deletion uncharged. The strings' bytes are admitted to the search's reservation as a candidate enters the top-K, released when it is evicted, and the rest with the result.
  - A search already cancelled returns its cancellation before contending for the slot, never at random against a free one.

## 11. As built: 9c-1 (the expected UID)

9c is reviewed in two parts: 9c-1 carries the UID through navigation; 9c-2 is the console on top of it.
- **Intents:** `select-pod`, `select-workload`, `select-node` and `select-flux` carry an optional `uid`. `reveal` forwards `ref.uid` into them, and the `reveal` intent already carries the ref.
  - Summary Top links carry their evaluated object's UID, so they now pin too. This is a deliberate change from §4.2's "behaviour unchanged": a Top link names an object the Observer evaluated, and a replacement there is now said rather than followed. Links without a UID are unchanged.
- **The expectation:** `expectPin(cluster, key, uid)` (`nav/inspectorPin.ts`) parks the UID for a lens's selection key (at most 32, newest kept). `usePinnedSubject` consumes it the first time it reads that key and pins it before any row is seen. While no row with that UID has been seen there is no subject, and the hook returns the UID as `expected`.
- **Lenses:** an intent with a UID whose row is different or absent selects its key at once with the expectation: no wait and no "no longer present" toast, so an incomplete read reads unverified. A matching row takes the existing path. This covers:
  - Pods, Workloads, Nodes;
  - Flux reconciliations and sources;
  - Argo Applications and ApplicationSets;
  - Certificates and Issuers;
  - ExternalSecrets and stores;
  - Gateways and HTTPRoutes;
  - Karpenter NodeClaims and NodePools.
- **`PinnedGone`** is the shared inspector for an expected object with no row: kind, namespace/name, the short UID and the state. It reads:
  - removed: "not in a complete read of its kind";
  - replaced: "a different object now has this name", with "Open the new object", which re-pins and, where the lens reads detail, reads the new object;
  - unverified: "neither confirmed nor ruled out".
  - An HTTPRoute absent from a complete attached-set read is "No longer attached to a Gateway … it may still exist", never removed.
- **Resources:** `InstanceRef.expectUid` comes from `openInstance(ns, name, expectUid)`. The instance read is authoritative:
  - a different UID is replaced, and "Open the new object" re-opens the name without the expectation;
  - a NotFound is removed (`InstanceDetailDTO.notFound`, from `apierrors.IsNotFound`);
  - any other failure is unverified.
- **Tests:**
  - the hook (`usePinnedSubject.test.tsx`), `PinnedGone`, and `reveal` forwarding;
  - per lens: Pods (same UID, replaced and adopted, absent on incomplete then complete reads, then arriving), Workloads, Nodes, Flux (both), Certificates, ESO, Argo, Gateways and Routes (detached wording), Karpenter;
  - Resources (`InstanceDetail.test.tsx`), and `notFound` in Go (`TestGetInstanceDetailNotFound`).

### 11.1 Review of ff41862

Four findings, all taken:
- **A later opening at a selected key:** every opening that carries a UID parks its expectation, a matching row included, and `usePinnedSubject` honours an expectation whatever was pinned at the key. The same object keeps its evidence; another starts from none. Opening the old UID over the selected new pod reads replaced, and opening the current UID over a replaced pin opens it. Expectations expire after 10 seconds, so a later plain selection of the key never inherits one.
- **Resources adoption:** the instance read also runs when the expected UID changes, so "Open the new object" (the same name, no expectation) reads the replacement, fenced as every read is.
- **The list never contradicts the read:** opened by UID, presence is the direct read's to settle. The list's `removed` (absence from a list read before the object existed) is ignored.
- **A failed read:** a rejected detail read is recorded as an error DTO, never the last good detail, so an object opened by UID reads not verified and any other instance says it could not be loaded.
- **Re-review of efbfb31:**
  - **Scoped expectations:** expectations are scoped to the pin they are for, by kind (`expectPin(cluster, scope, key, uid)`, `usePinnedSubject(..., scope)`). Two pins of one lens that share a key, Argo's Application and ApplicationSet, never take each other's; a hook without a scope takes none.
  - **Identical reopenings:** the store counts openings (`instanceOpens`), and the instance inspector reads on each one, so the same object opened again reads and settles instead of being left loading.

## 12. As built: 9c-2 (the console)

- **Structure:** `CommandPalette` keeps only the open state, the hotkeys and focus restore; the console itself mounts per open, so each open is a fresh session (its state and its search fence its own) and nothing subscribes while it is closed.
- **The overlay:** 760px wide, 88px from the top, a modal dialog ("Command console") whose Tab is trapped (`trapTab`), with no animation.
- **The empty query**, in order:
  - recent places (up to 6, `recentPlaces()`);
  - the fleet indicator's affected clusters (up to 8, in chip order, each with its axis and reason);
  - "Run · <kind> <name>": the verbs whose target is the open inspector's pin, matched by UID, or by cluster, kind, namespace and name. Verb commands gain a `target` for this.
- **Groups:**
  - **Ask:** `chat:*` commands.
  - **Run:** actions, plus terminal, zoom, theme and shortcuts.
  - **Go to:** every other application command, plus clusters, views and resources.
  - **Objects:** the index's hits. The open cluster's loaded rows for kinds the index covers are dropped; Helm releases, which it does not index, remain.
  - **Order:** a command whose title the query starts leads with its group; otherwise Objects, Run, Go to, Ask.
- **An object row:** its `Aspect` (only a current role marks; anything else reads "checking", never healthy), kind, name, `LinePos` (or the cluster's code when the line does not place it) and a note: namespace · the role's word ("last observed <role> at <time>", "health unverified") · the kind's state when not live.
- **The search:** `bridge/fleetIndex.searchIndex` is cancellable. `useIndexSearch` debounces 150 ms; each request cancels the previous one in Go, and a reply is applied only for the latest request of the session (A → B → A tested). A short query reads the coverage alone.
- **Opening** (`console/openHit.ts`): a hit whose kind has a lens is revealed there with its UID; every other kind opens in Resources with its UID expected.
  - ↵ closes the console.
  - ⌘↵ keeps it open and moves it aside (left-aligned, a lighter backdrop) so the inspector on the right stays readable. Focus stays in the input, and closing then focuses the inspector's identity.
- **Verbs only:** a chip button in the header, and alt+V (labelled "alt+V": the ⌥ glyph does not render in the mono face).
- **Protected verbs** show "▲ prd lock" in their hint and still open the existing confirm and grant flows.
- **The coverage line:** "Searching N clusters · M kinds", then the gaps, the first two shown and all of them in the tooltip: clusters not indexed or not connected, and kinds partial, refused or stale, each with the lens to search instead. "+N more objects" when the index matched more than it returned.
- **Tests:** `console/console.test.tsx` covers the frame and focus trap, the empty query's three parts, verbs only, object rows (aspect, LinePos, notes), group lead, ↵ and ⌘↵ (aside, focus to the inspector), coverage notes, the A → B → A fence, and hit opening. The palette's existing tests are updated for the console.
- **Captures** (`pw/s9c.mjs`, kind lab, 1440×900, both themes): the empty query, a search, verbs only, an object opened beside, and the inspector after closing.

### 12.1 Review of b90da19

Codex requested changes with three P2 findings. All three are fixed, and each has a test that fails without the fix:

- **Hits belong to the visible query.** `useIndexSearch` tags each reply with the query it answers. Hits and errors are returned only for what is typed now, so typing B withholds A's hits at once: they can be neither listed nor opened while B is pending. B's failure takes the footer ("The fleet index could not be searched: …"), never hidden behind A's result. Coverage describes the index rather than the query, so the last reply's coverage stays in the footer, with "· searching…" while a query is pending.
- **Verbs follow their object's evidence.** Besides route and eligibility, a change to the slices verbs come from (pods, nodes, workloads, Flux, Argo, Helm) rebuilds the commands. The rebuild applies only when a verb's id, wording or object UID changed, so pushes that change nothing a verb names do not churn the list. As a result:
  - rows that arrive while the console is open bring their inspector's verbs;
  - an object replaced at the same name drops the old pin's verbs.
  - Activation also re-reads the verb against live state (`targetMoved`). A verb whose object is gone, or is now another object, runs nothing and says why.
- **Escape belongs to the console.** While open, the console takes Escape in the capture phase on window, before anything underneath. A drawer's close, a menu or a confirm never sees the Escape that closes the console, and the next Escape is the drawer's again.
