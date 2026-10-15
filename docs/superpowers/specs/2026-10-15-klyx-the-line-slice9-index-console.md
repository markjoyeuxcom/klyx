# The Line, slice 9: the fleet metadata index and the console

Design note for slice 9 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §8.4 (the fleet metadata index) and §6 (the command console), with §14's bounds. The parent is the contract; this note records what exists, how the slice is cut and the points the parent leaves open.

## 0. What exists (surveyed 2026-10-15)

- **The palette** (`chrome/CommandPalette.tsx`, `chrome/commands.ts`) is built in the frontend from the loaded slices of the open cluster: pods, nodes, workloads, Helm, Flux and Argo rows, Gateways (Observer rows) and HTTPRoutes (the topology). Ids are not cluster-qualified, and nothing is asked of the backend.
  - Groups are Application, Clusters, Views, Resources, Objects and Actions; the empty query lists every unqualified command by frecency (`localStorage`, 300 entries). Results stop at 50 with "+N more".
  - ↵ runs; there is no ⌘↵, no alt+V and no focus trap. The panel is 560px wide, 12vh from the top.
  - "Ask about <cluster>" opens the chat inside a cluster.
- **The Observer** watches, per cluster: pods, Deployments, StatefulSets, DaemonSets, ReplicaSets (metadata, no labels), nodes, namespaces (metadata: name and UID only), the seven Flux kinds, Argo Applications and ApplicationSets, GatewayClasses, Gateways, HTTPRoutes, and Services and EndpointSlices where the Gateway API is served, cert-manager Certificates, Issuers and ClusterIssuers, ESO ExternalSecrets and (Cluster)SecretStores, and Karpenter NodePools and NodeClaims.
  - Its ledger keeps every evaluated object's role by UID, healthy ones included (`partition.entries`), each part capped at 50,000.
  - There is no public accessor for a kind's model objects or for a role by UID.
- **Not watched** from §8.4's defaults: Jobs, CronJobs, Ingresses, GRPCRoutes, ConfigMaps, Secrets, PVCs, and Services on clusters without the Gateway API.
- **The metadata client** exists per cluster (`ClusterConn.meta`); informers with a transform exist only as the no-Observer fallbacks for nodes and pods.
- **Lifecycles:** the Observer starts once per connection, never stops on a lost connection (its reflectors go stale) and has an incarnation; there is no cluster removal path.
- **Recent places** (`nav/history.ts`, 12), the fleet axes (`nav/fleetAxes.ts`), inspector pinning (`InspectorLayout pin`, `usePinnedSubject`) and `reveal`/`select-*` intents exist. "Opening beside" does not.

## 1. Sub-slices

| | Builds |
|---|---|
| **9a** | The index (Go): the bounded store and its admission, metadata-only reflectors for the kinds the Observer does not watch, the Observer's kinds read from its model, per-kind states, the lifecycle, the `index` block in fleet.yaml |
| **9b** | The search (Go): a bound `IndexService.Search(ctx, query, limit)` over every cluster's index, with each hit's aspect from the Observer's ledger by UID and the coverage line |
| **9c** | The console (frontend): the 760px overlay, the empty query, the four groups, object rows with `Aspect` and `LinePos`, ↵ and ⌘↵, the verbs-only toggle, protected verbs |

Each is Codex-reviewed on its own, in this order.

## 2. 9a: the index

- **One indexer per cluster** (`internal/fleetindex`), started once the cluster's Observer has been live once, fenced by the Observer's incarnation (a new one restarts it), and stopped with the app.
- **Two sources, one shape.** An entry is `{cluster, gvr, kind, uid, namespace, name, labels (at most 2 KiB, with a truncation marker), created}`:
  - **The Observer's kinds** are read from its model through a new read accessor, `Observer.KindObjects(kind)`, which copies the projected metadata of the kind's store under the Observer's lock. There is never a second watch. Where the Observer's projection kept no labels or creation time (namespaces, ReplicaSets, Services), the entry has none, and says so.
  - **The other configured kinds** are listed and watched as `PartialObjectMetadata` through the metadata client, in pages of 500. A transform keeps the entry's fields and drops everything else - annotations and `managedFields` included - before anything is stored or queued.
- **Default kinds:** §8.4's list. The Observer's kinds come from its model; Jobs, CronJobs, Ingresses, GRPCRoutes, ConfigMaps, Secrets (name only, as all are), PVCs and, without the Gateway API, Services are watched by the index. A kind not served on a cluster is not watched there.
- **Configuration:** a top-level `index:` block in fleet.yaml with `kinds:` (group/version/resource entries) replaces the default list; absent, the defaults apply. Validation names a malformed entry and ignores it.
- **Bounds and admission** (§14): 50,000 entries and 32 MiB per cluster, 200,000 and 128 MiB across the fleet. One shared budget, atomic across kinds and clusters, admits every list page and every watch event that adds an entry or grows its labels before it is retained.
  - At a cap, the kind that would cross it stops (its list stops, or its watch is closed); what it holds is frozen and reads "capped at N · as of <time> · stale". Other kinds go on until they too would grow past it. Vacancies are not refilled.
  - The Observer's kinds are counted when they are read: a kind whose entries would cross a cap is truncated at the cap (name order) and reads capped; the Observer itself is never stopped.
  - Memory is estimated per entry from its strings and labels; the estimate is the accounting.
- **Per-kind states:** indexing (the first list), live, capped, denied (403 on list), watch denied (the last list kept, stale), metadata unsupported (406; never a content read), stale/recovering (watch errors, with backoff), not served.
- **What it holds:** never object contents.

## 3. 9b: the search

- **The call:** `IndexService.Search(ctx, query, limit)` (context first, so the console's cancel stops it) returns ranked hits and the coverage.
  - Matching is over name, then namespace/name, then kind; case-insensitive substring and prefix scoring, deterministic order (score, then cluster order, kind, namespace, name).
  - Same-name objects on different clusters are separate hits, each with its cluster and UID.
- **A hit's aspect** comes from the Observer's ledger by UID, through a second read accessor, `Observer.RoleOf(uid)`. An object of a kind the Observer does not evaluate reads "health unverified", never healthy.
- **The coverage:** "Searching 7 clusters · 14 kinds", and, for any cluster whose index is building, partial, capped, denied or unsupported for a kind, a note naming it and the lens to search instead.
- **Bounds:** at most 200 hits per call; a query under two characters returns none (the empty query is the frontend's).

## 4. 9c: the console

- **The overlay** (§6): 760px wide, 88px from the top, no animation. A focus trap keeps Tab inside; Tab moves focus normally.
- **The empty query**, in order: recent places (`nav/history.ts`), the fleet indicator's affected clusters (`useFleetAxes`), and the verbs for the current selection.
- **Groups:** Objects (the index), Run (verbs), Go to (places, clusters, views and resources) and Ask (the chat). Today's groups map onto these.
- **An object row:** its `Aspect`, kind, name, `LinePos` (with the cluster's short code) and a note (namespace, "health unverified", or the coverage note).
- **Opening:** ↵ opens the object: it navigates to its lens and reveals it, pinned to the row's cluster and UID. ⌘↵ opens it beside: the same reveal, with the console kept open over the list. A kind whose lens has no inspector of its own navigates instead, and says so on the row.
- **Verbs only:** a toggle button in the console header (alt+V) that hides every group but Run.
- **Protected verbs** keep ▲, the "type the cluster name" confirmation and the grant flow (they open the existing dialogs).
- **The search** is debounced (150 ms), cancelled when the query changes, and applied only for the query that asked.

## 5. Testing

- **9a:** admission (a page and a watch event at each cap, shared across kinds and clusters; frozen and stale wording; no refill), the transform (annotations and `managedFields` never retained; labels capped with a marker), per-kind states (403, 406, watch denied, errors with backoff), the Observer's kinds read without a second watch, the lifecycle across a new incarnation, the config block. The kind lab, read-only.
- **9b:** ranking and determinism, same-name hits across clusters, aspects by UID and "health unverified", coverage notes, bounds, cancellation.
- **9c:** the empty query's three parts, the groups, rows with `Aspect` and `LinePos`, ↵ and ⌘↵ (pinned to cluster and UID; the console kept open), verbs only (alt+V), the focus trap, protected verbs, the stale-reply fence; headless captures in both themes.

## 6. Open points (for review)

1. **The Observer's kinds are read through a new accessor** (`KindObjects`) that copies projected metadata under its lock, rather than a second watch, and kinds whose projection dropped labels or creation time keep none.
2. **The cap for the Observer's kinds** is applied when they are read (truncated in name order, marked capped); the Observer is never stopped.
3. **Memory accounting** is an estimate from each entry's strings and labels.
4. **Aspects by UID** come from the ledger through `RoleOf`; an object the Observer does not evaluate is "health unverified".
5. **The search runs in Go** (substring and prefix scoring), not in the frontend: the index never crosses the bridge whole.
6. **⌘↵** keeps the console open over the reveal, which is how the inspector opens "beside" it.
7. **The `index:` block** replaces the default kinds list rather than adding to it.
