# The Line, slice 8: drawings

Design note for slice 8 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §2.4 (`Rail`, `LinePos`), §5.1 (the GitOps dependency graph), §5.2 (the Gateway data path), §5.3 (the chat read ledger), §5.4 (`LinePos`), drawing on §8.3 (slice 7a) and §8.5 (slices 7b and 7c). The parent is the contract; this note records what exists, how the slice is cut and the points the parent leaves open.

## 0. What exists (surveyed 2026-10-15)

- **No `Rail` primitive.** The cert-manager issuance chain is drawn by a private `ChainRail` (`cluster/CertsView.tsx:797`, flattened depth-first by `flattenChain`). ESO has no chain drawing: its inspector shows the store, the refresh and the target Secret as blocks.
- **The Flux inspector** (`cluster/GitOps.tsx:790`) shows:
  - the bound source (`SourceSection`);
  - "blocked by" (`DependsOnSection`): the first dependency whose row is not Ready, shown when the reason is `DependencyNotReady`; the message is not parsed;
  - the inventory and the live diff.
- **No recursive walk** exists in Go or TS: every source and `dependsOn` lookup is one level deep. `DependencyNotReady` messages are parsed only by `internal/line` (`blockedBy`, the `dependency '<ns>/<name>'` form).
- **Credentials:** no source's `spec.secretRef` is read, and there is no Secret → ExternalSecret link. Only the forward `ExternalSecret.TargetSecret` exists.
- **Managed fields (7a):** `ObjectWritesService.ObjectWrites` and `bridge/writes.ts` exist, with no caller yet. `internal/lastwrite.Read` works on any object.
- **Git "not here yet" is not built.** `gitcli.Repo.Log` takes no path; `Repo.DiffNames(a, b, paths)` exists, called only by tests. `GitService.LineGit` gives each station's ordered state against T and the Ledger, with no path filter.
- **The Gateway topology** (`cluster/GatewayTopology.tsx`) draws one lane per route: route › primary backend › observed traffic.
  - Rules are not drawn. Backends carry their rule index, weight and `weightSet`.
  - The per-rule backend verdict (`gwapi.RouteRule`) is folded into the route's label.
  - Route-level metrics are polled every 20 seconds.
  - Per-rule metrics (7c, `bridge/ruleMetrics.ts`) have no caller.
- **`chrome/Sparkline.tsx`** draws `{t, v}` points by timestamp, breaks the line at a gap over 1.5 steps, and reads "no data" when empty.
- **The chat drawer** (`ai/ChatDrawer.tsx`) lists an answer's reads in a `<details>`: tool, target, outcome (completed, failed, truncated or cancelled), detail and redaction count.
- **`spineCodes` was retired** with the spine in slice 4a (52dfb06). The station data `LinePos` needs is on `LineStationDTO`: `track`, `rank` (−1 for a siding) and the flags.
- **Applied revisions:** the Observer's Flux rows carry each unit's applied revision, and the line derives `Applied` from them. No history records when it changed: the line history logs failing, held and state predicates only.

## 1. Sub-slices

| | Builds |
|---|---|
| **8a** | `Rail`; the cert-manager chain and the chat read ledger on it; `LinePos` with restored short codes |
| **8b** | The dependency graph's data (Go): the bounded walk, typed edges, incomplete evidence, supply facts, change proximity, the inventory line; Git "not here yet" |
| **8c** | The dependency graph drawing in the Flux inspector |
| **8d** | The data path: per-rule verdicts in the topology, an applied-revision history, then the drawing with configured weights, measured per-rule traffic, onset and revision proximity |

Each is Codex-reviewed on its own, in this order.

## 2. 8a: `Rail`, the chat ledger, `LinePos`

- **`Rail`** (`chrome/primitives/Rail.tsx`): a vertical track of steps.
  - A step has a key, a label, an optional `Aspect`, optional tags and notes, and optional branches (each one a rail of its own).
  - Each step names the segment that leads into it, by type: `source`, `dependsOn`, `reported` (solid danger), `chain` (route), `incomplete` (dashed grey), and the read outcomes `completed` (route), `failed` (danger) and `cancelled` (dashed).
  - Every segment type has words as well as a style: the step's accessible name includes its segment ("reported blocker", "cancelled"), so nothing is carried by colour or dash alone.
  - It renders as nested lists; links inside steps are ordinary links.
- **The cert-manager chain** moves onto `Rail` with its current semantics (the deepest stuck node, "stuck here"). Nothing new is claimed.
- **The chat read ledger** (§5.3): each answer's reads as a `Rail` in read order, inside the existing disclosure.
  - Completed and truncated reads are route, failed reads danger, cancelled reads dashed. A truncated read keeps its "truncated" tag.
  - The redaction count stays on its line. The chips are unchanged.
- **`LinePos`** (§5.4):
  - one square per rank present on the station's track, with the station's square filled, in danger when the station has a fault;
  - a siding station shows one hollow square and "siding".
  - **Short codes:** the retired `spineCodes` algorithm returns as `line/codes.ts`, unchanged, as the one source of a cluster's unique short code. `LinePos` always pairs with it.
  - The accessible name is the full cluster name plus its position: "aks-prd-neu, prd on North Europe".
  - **Placed** on the fleet queue rows ("Needs you", parent §3.7). The console rows get it with the console in slice 9.

## 3. 8b: the dependency graph's data

- **The call:** `GitOpsService.DependencyGraph(ctx, cluster, kind, namespace, name, uid)` returns `DepGraphDTO`.
  - Its first parameter is a context, so the frontend cancels the walk when the selection changes: Wails v3 cancels a bound call's context when its promise is cancelled.
  - The selection's UID is passed. A different UID read for the start node is "replaced".
- **The walk** (`internal/fleet`, a pure graph builder in `internal/gitops/depgraph`): from the selected Kustomization or HelmRelease, follow:
  - the `sourceRef` (for a HelmRelease, the chart's `sourceRef`, or `chartRef`);
  - the `dependsOn` entries, recursively.

  Every node is one fresh GET, which is its provenance ("read", with `readAt`). A node reached twice (a diamond) is read once.
- **Bounds:** depth 6, at most 24 nodes, and at most 24 reads in all. The reads counted are node GETs and supply GETs. Managed fields come from those same reads, so 7a's batch read is not called here.
- **Edges:**
  - **`source`** and **`dependsOn`**, as declared.
  - **Reported blocker on a `dependsOn` edge:** the downstream node is Ready=False with reason `DependencyNotReady`, and its message names that dependency. The parser moves from `internal/line` to the flux package, shared by both.
  - **Reported blocker on a `source` edge:** the downstream node is Ready=False with the controller's reason for a missing source artifact (Flux's `ArtifactFailed`, to be confirmed against the kustomize- and helm-controller sources while building), and the source itself is not Ready.
- **Labels** (the parent's, verbatim):
  - "upstream failure" on a failing upstream node;
  - "reported blocker" on a reported-blocker edge;
  - "reported chain ends here" on the last node of a chain of reported blockers.

  "Root cause" is never said.
- **Incomplete evidence** is a stub node naming what happened, and the header lists "incomplete: …":
  - **depth:** truncated at depth 6, or at 24 nodes or reads;
  - **cycle:** cut where it closes, naming both ends;
  - **unreadable:** the GET failed, with its error;
  - **stale:** the node's `status.observedGeneration` is behind its `metadata.generation`, so its conditions describe an older spec (the ratified staleness language);
  - **replaced:** a UID differs from the one the edge or the selection recorded.
- **Supply facts:**
  - **Followed:** a source's `spec.secretRef`, and a Kustomization's `spec.decryption.secretRef`.
  - **The Secret** is read as metadata only, never its data.
  - **The ExternalSecret:** an ownerReference to an `external-secrets.io` ExternalSecret with `controller: true`, whose GET returns that UID. Then the fact reads "Credential supplied by ExternalSecret <ns>/<name>", with the ExternalSecret's own Ready condition.
  - **No inference:** without such a reference, the fact reads "Secret <ns>/<name> (no supplier recorded)". There is no match on target names, and nothing says the credential is a cause.
- **Change proximity** (§8.3), computed in Go from the objects' own `managedFields` (`lastwrite.Read`):
  - Each node and supply object carries its last recorded update and its last update that was not a status update.
  - **The pairing:** a failing node's "since" is its Ready=False condition's `lastTransitionTime`, labelled "(condition time)". A main-resource update falling within the 10 minutes before it is paired with it. The update may be the node's own, an upstream node's, or an upstream supply object's.
  - Both times stay qualified observations; nothing says which field changed.
- **The inventory line** (Kustomizations): "last applied <sha>; inventory N objects (M workloads)", from `status.inventory` and `lastAppliedRevision`. Workloads are Deployments, StatefulSets, DaemonSets, Jobs and CronJobs. It never says the workloads run that revision.
- **Git "not here yet"** (§7) is a separate call, so the graph needs no Git and Git needs no cluster reads:
  - `GitService.NotHereYet(ctx, cluster, nodes)` takes, for each node behind T, the facts the graph returned: the source URL, the applied sha and `spec.path`.
  - **Validation:** each URL must be an approved `git.repos` entry, the sha a full hex sha, and the path clean and relative.
  - **T** is the GitService's own line T for that repository and ref, never one supplied by the caller.
  - **It returns:**
    - the commits in T ^applied that touch the path (`gitcli.Repo.Log` gains a pathspec), at most 50;
    - the changed files between them (`DiffNames`, name-status from trees only).

## 4. 8c: the dependency graph drawing

- **Where:** the Flux inspector gains a "Dependencies" section that replaces "blocked by". The source section stays.
- **The drawing:** a `Rail` tree from the selected node outward. Branches show, with each blocker on its own branch.
- **The end of a reported chain** lists its evidence:
  - the failing condition;
  - the supply facts;
  - the change proximity;
  - Git's "not here yet", when Git is set up.
- **Incomplete stubs** use the `incomplete` segment, and the header carries "incomplete: …".
- **Refresh:** the graph is fetched on selection and cancelled when the selection changes. A refresh control re-walks it; there is no polling.

## 5. 8d: the data path

- **Go, per-rule verdicts:** `TopologyDTO` routes gain `rules: [{index, verdict, backends}]`, from `gwapi.RouteRule`. A backend's issue is its own, so a failing backend gets a danger branch.
- **Go, the applied-revision history:**
  - **The writer:** a predicate partition modelled on 7b's, under task `units`, part `applied`. It has one key per Flux unit, whose value is the unit's applied revision, written from the Observer's Flux rows. It rides the rows `LineService` already subscribes.
  - **The query:** `AppliedChange(cluster, kind, ns, name)` returns:
    - the current applied revision;
    - when its change was observed (basis observed only when Klyx saw the change);
    - the partition's live-since.
- **The route's unit** comes from the route's Flux labels (`kustomize.toolkit.fluxcd.io/name` and `namespace`, else `helm.toolkit.fluxcd.io/name` and `namespace`). Without them there is no revision proximity.
- **The drawing** (§5.2), in order:
  1. the Gateway: listener, hostnames, attached policies;
  2. the HTTPRoute;
  3. one branch per rule, with a bar per backend for its configured weight (an unset weight counts as 1), labelled "configured";
  4. the Service, with its ready endpoints.
- **Measured traffic per rule** comes from 7c.
  - **The metrics:** the request rate, the 5xx and 503 shares (each with a 30-minute `Sparkline`), retries, and the per-backend split ("unavailable" for Envoy Gateway, with its reason).
  - **Availability:** each metric shows its own. A missing series reads "no samples", never zero.
  - **The window:** each value carries it ("30 min · 1-minute rate · 30-second step").
  - **Attribution:** points before the cut-off are a hatched "unattributable before <time>" region. The uptake note is shown once per Gateway.
- **Configured weight and measured split** sit side by side, never merged.
- **Onset** (a pure function, on the 5xx share's attributed points):
  - It reads "first sampled crossing of 1% at <time> (1-minute rate, 30-second step)".
  - When the first attributed sample is already above 1%, it reads "already above 1% at <time>, the first attributable sample".
  - A gap over 1.5 steps restarts the search.
- **Revision proximity:** "<sha7> applied change observed <time>; first sampled crossing <time>".
  - **It appears only when:**
    - the applied change was observed (basis observed);
    - the change is at or before the onset and within the 10 minutes before it;
    - the `applied` partition was live continuously from the change through the onset.
  - It is never a cause.
- **No metrics endpoint:** "no route metrics: configure a metrics endpoint", and nothing is estimated.
- **Below the drawing:** the rules and policies table, and the mesh note for global Services.
- **Polling:** per-rule metrics every 30 seconds while the view is open (the step). The route-level poll ends here, because the drawing replaces the lane's traffic line.

## 6. Testing

- **8a:**
  - `Rail`: every segment type's style and its accessible words; branches.
  - The cert chain unchanged in content.
  - The chat ledger: each outcome and the redaction counts.
  - `LinePos`: ranks, the fault fill, a siding, the accessible name.
  - The codes: the old spine tests carried over.
- **8b:**
  - The walk over fake dynamic objects: diamond, cycle, depth and count bounds, unreadable, stale, replaced, cancellation mid-walk.
  - Reported edges, by each reason and message form, and a dependency the message does not name.
  - Supply facts: owner reference with a matching and a mismatched UID; no owner.
  - Proximity: inside and outside 10 minutes, and a status-only update never paired.
  - The inventory line.
  - `NotHereYet`: refusals (an unapproved URL, a bad sha, an unclean path), T from the line, the path filter and name-status on a fixture repository.
- **8c:** the section's rendering from DTO fixtures, cancellation on reselection, and headless captures in both themes.
- **8d:**
  - The topology's per-rule verdicts.
  - The applied partition: baseline, change, gap and end.
  - Onset: crossing, already above, a gap restarting the search, no attributed points.
  - Revision proximity shown and withheld (unobserved basis, too far, a coverage break).
  - The drawing from fixtures; captures in both themes.

## 7. Open points (for review)

1. **The supply link:** only a controller ownerReference whose UID is verified names an ExternalSecret. There is no target-name inference.
2. **"Stale read"** in the graph means status behind generation, as the ratified staleness language already does.
3. **Change-proximity pairing:** a failing node pairs with updates to itself, its upstream nodes and their supply objects within the 10 minutes before its Ready=False condition time.
4. **Git "not here yet"** is a separate call carrying the graph's facts. Every fact is validated in Go, and T is always Go's own.
5. **Revision proximity** needs the applied-revision history (built in 8d). It appears only with an observed change, at most 10 minutes before the onset, and with continuous coverage.
6. **Onset** reads the 5xx share's attributed points only: the window's start is the first attributable sample.
7. **`LinePos` short codes:** the retired `spineCodes` algorithm is restored unchanged as the one source.
8. **The walk** is a bound call with a context, cancelled by the frontend on reselection.
9. **The route-level traffic poll** retires with the lane's traffic line, replaced by the per-rule read.
