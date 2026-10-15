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
- **`spineCodes` was retired** with the spine in slice 4a (52dfb06). The placement `LinePos` needs is on `LineDTO`: each station's `track` and `rank`, the `siding` list and the tracks' labels. `LineStationDTO.flags.failing` is the selected key's GitOps comparison failure, not the cluster's health: a Pod fault leaves it false. The fleet indicator's fault truth is `useFleetAxes()` (`nav/fleetAxes.ts`).
- **Applied revisions:** the Observer's Flux rows carry each unit's applied revision, and the line derives `Applied` from them. No history records when it changed: the line history logs failing, held and state predicates only.

## 1. Sub-slices

| | Builds |
|---|---|
| **8a** | `Rail`; the cert-manager chain, the ESO chain and the chat read ledger on it; `LinePos` with restored short codes |
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
- **The ESO chain** (parent §5.1, design review of 9705575): the ExternalSecret inspector draws its bound store → the ExternalSecret → its target Secret on `Rail`. The evidence it shows today moves onto those steps: the store failure, the likely cause, the store facts, the refresh and the target Secret's link. No supplier or causal claim is added.
- **The chat read ledger** (§5.3): each answer's reads as a `Rail` in read order, inside the existing disclosure.
  - Completed and truncated reads are route, failed reads danger, cancelled reads dashed. A truncated read keeps its "truncated" tag.
  - The redaction count stays on its line. The chips are unchanged.
- **`LinePos`** (§5.4):
  - one square per rank present on the station's track, with the station's square filled, in danger when the cluster has a fault. The fault is the fleet indicator's own (the "fault" axis of `useFleetAxes()`); `LineDTO` supplies the placement only (design review of 9705575);
  - a siding station shows one hollow square and "siding".
  - **Short codes:** the retired `spineCodes` algorithm returns as `line/codes.ts`, unchanged, as the one source of a cluster's unique short code. `LinePos` always pairs with it.
  - The accessible name is the full cluster name plus its position: "aks-prd-neu, prd on North Europe".
  - **Placed** on the fleet queue rows ("Needs you", parent §3.7). The console rows get it with the console in slice 9.

## 3. 8b: the dependency graph's data

- **The call:** `GitOpsService.DependencyGraph(ctx, cluster, kind, namespace, name, uid)` returns `DepGraphDTO`.
  - Its first parameter is a context, so the frontend cancels the walk when the selection changes: Wails v3 cancels a bound call's context when its promise is cancelled. The context reaches every read. The bridge keeps the generated promise's cancel (no async wrapper erases it).
  - The selection's UID is passed. A different UID read for the start node is "replaced".
  - A reply is applied only for the selection that asked (cluster, UID and a request generation), so a late reply never lands on another selection.
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
  - **stale:** the node's `status.observedGeneration` is behind its `metadata.generation`, the ratified generation-lag state, stated with both generations. Each condition is qualified by its own `observedGeneration` where it carries one: a condition behind the generation is cited as evidence about that generation, never as the current spec's (design review of 9705575);
  - **replaced:** a UID differs from the one the edge or the selection recorded.
- **Supply facts:**
  - **Followed:** a source's `spec.secretRef`, and a Kustomization's `spec.decryption.secretRef`.
  - **The Secret** is read as metadata only, never its data.
  - **The ExternalSecret:** an ownerReference to an `external-secrets.io` ExternalSecret with `controller: true`, whose GET returns that UID. Then the fact reads "Credential supplied by ExternalSecret <ns>/<name>", with the ExternalSecret's own Ready condition.
  - **No inference:** without such a reference, the fact reads "Secret <ns>/<name> (no supplier recorded)". There is no match on target names, and nothing says the credential is a cause.
- **Change proximity** (§8.3), computed in Go from the objects' own `managedFields` (`lastwrite.Read`):
  - Each node and supply object carries its last recorded update and its last update that was not a status update.
  - **The pairing:** a failing node's time is its Ready=False condition's `lastTransitionTime`, labelled "(condition time)" and never presented as a role start: Flux rows carry no role `since` (parent §5.1 amended to this, design review of 9705575). A main-resource update falling within the 10 minutes before it is paired with it. The update may be the node's own, an upstream node's, or an upstream supply object's.
  - Both times stay qualified observations; nothing says which field changed.
- **The inventory line** (Kustomizations): "last applied <sha>; inventory N objects (M workloads)", from `status.inventory` and `lastAppliedRevision`. Workloads are Deployments, StatefulSets, DaemonSets, Jobs and CronJobs. It never says the workloads run that revision.
- **Git "not here yet"** (§7) is a separate call, so the graph needs no Git and Git needs no cluster reads:
  - `GitService.NotHereYet(ctx, cluster, nodes)` takes, for each node, the facts the graph returned: the source URL as the cluster writes it, the source ref's kind and name (branch, tag, semver or commit), the applied sha and `spec.path`.
  - **Identity** (design review of 9705575): the URL is mapped through the approved `git.repos` matches, as the line does, and only the approved URL is ever executed. The (repository, ref kind, ref name) key must be the one whose T Go holds now; T is always Go's own.
  - **Validation:** the sha is a full hex sha and the path clean and relative.
  - **"Behind" is proved, not assumed:** applied must be an ancestor of T (`merge-base --is-ancestor`) before anything says "not here yet".
  - **It returns, per node,** either:
    - the commits in T ^applied that touch the path (`gitcli.Repo.Log` gains a pathspec), at most 50, and the changed files between them (`DiffNames`, name-status from trees only), with T's fetch freshness;
    - or an explicit unavailable state: a different or unsupported key, applied ahead of T, diverged, or history missing from the cache.

### 3.1 As built in 8b-1 (the graph; Git's "not here yet" is 8b-2)

- **The builder** is `internal/gitops/depgraph` (pure, over a `Reader`); the fleet reader (`ClusterConn.DependencyGraph`) reads each kind at its served version through the cached discovery, and Secrets through the metadata client.
- **The controllers' words** were read from kustomize-controller v1.9.6 and helm-controller v1.6.5. Both use `DependencyNotReady` with "dependency '<ns>/<name>' …" (is not ready, not found, not ready according to readyExpr), and `ArtifactFailed`. helm-controller also sets `SourceNotReady` with "<Kind> '<ns>/<name>' is not ready: …" when its `isReady` judges the source not ready: a generation not yet reconciled, Stalled, or no artifact (design review of 06f7f47). The parsers are `flux.DependencyNamed` (which the line now uses too) and `flux.SourceNamed`.
- **A reported source edge:** `ArtifactFailed` with a source that is not ready, or `SourceNotReady` naming this source while it is still not ready, both by helm-controller's rules (not found, Ready=False, generation lag, Stalled, or no artifact). `ArtifactFailed` alone also covers a missing `spec.path`, which is not the source's.
- **Only a current condition reports** (design review of 06f7f47): a Ready condition whose own `observedGeneration` (else the status's) is behind the object's generation stays qualified evidence about that generation, and reports no edge of today's spec.
- **A HelmRelease's source** is its `chartRef`, else the HelmChart its `status.helmChart` records, else the chart template's `sourceRef`; a HelmChart's source is its own `sourceRef`.
- **Supply** follows a GitRepository's, OCIRepository's, Bucket's or HelmRepository's `spec.secretRef`, and a Kustomization's `spec.decryption.secretRef`.
- **The 24 reads** count node GETs, Secret metadata GETs and ExternalSecret GETs alike. **Every object is read once** (design review of 06f7f47): a node that is not found, unreadable or replaced stands for that object under its own ID, so a diamond reuses it; a Secret's metadata and an ExternalSecret are shared by every node naming them.
- **Bounded with its stubs:** once a budget (24 nodes counting stubs, 24 reads) runs out, or at depth 6, a node's remaining targets become one summary stub ("N more (first …) not read: …"). A node already read is linked at no cost; a cycle is cut per edge; duplicate targets are dropped. So a node with 1,000 dependencies gives 25 nodes and 24 edges.
- **Not found** is a stub that is a fact, not incomplete evidence: it can end a reported chain ("dependency '…' not found").
- **Stale** follows the field's presence: a recorded `observedGeneration` of 0 behind generation 1 is lag; an absent one is not.
- **Discovery:** the reader resolves each kind at whichever advertised version serves it (the preferred one first), through the context-aware discovery cache, so the caller's cancellation and deadline end discovery reads too. 7a's `ObjectWrites` shares the resolver.
- **Replaced** applies to the selection: Flux's references carry no UID. A Secret's owner reference whose ExternalSecret now has another UID names nobody, with a note.
- **Each node carries** its Ready condition with the condition's own `observedGeneration`, the object's generations, and its last update and last main-resource update.
- **Workloads** in the inventory line are apps Deployments, StatefulSets and DaemonSets, and batch Jobs and CronJobs.
- **Git facts** come only from a Kustomization bound to a GitRepository that was read, with an applied revision that parses as Git.
- **The binding:** `GitOpsService.DependencyGraph(ctx, cluster, kind, ns, name, uid)`, with the 30-second action timeout derived from the caller's context. `bridge/depGraph.ts` returns the generated `CancellablePromise` unwrapped.
- **Live:** `TestLabDependencyGraph` walks the kind lab, read-only.

### 3.2 As built in 8b-2 (Git's "not here yet")

- **The call** is `GitService.NotHereYet(ctx, nodes)`, at most 24 nodes. It takes no cluster: T is the line's, fleet-wide, and each node's facts name their own repository and ref.
- **T** is the GitService's last ordering: its target's sha and fetch time, and the line key that ordering was computed for, now recorded with it. With no ready ordering, every node reads "the line has no T".
- **Identity:** a node's URL and ref are normalised as the line normalises a Flux ref (`line.OverrideKey`). Its repository must map, through the approved `matches`, to the approved URL that served T, and its ref kind and name must equal T's key. Only that approved URL is read.
- **"Behind" is proved:** the applied sha must be in the fetched history, then an ancestor of T. Otherwise the node reads ahead, diverged (a merge base exists) or unrelated; at T it reads "at".
- **The lists:** `gitcli.Repo.LogPath` (new: a path-limited log, trees only, since the cache is `blob:none` and never fetches lazily) gives at most 20 commits, gitcli's log bound rather than the note's 50. "More" is proved by a path-limited count (`CountPath`, new). `DiffNames` gives the name-status. Pathspecs are literal (`GIT_LITERAL_PATHSPECS`), so a cluster's path is never pathspec magic. An empty path is the whole tree.
- **The bridge:** `bridge/notHereYet.ts` returns the generated cancellable call.

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
- **Go, the applied-revision history** (typed, design review of 9705575):
  - **The writer:** a predicate partition modelled on 7b's, under task `units`, part `applied`, written from the Observer's Flux rows and riding the rows `LineService` already subscribes. One key per unit **UID**, fenced by incarnation and coverage like 7b, so a same-name replacement never borrows history.
  - **The value is typed by the unit's source kind**, never by the revision's shape (design review of 5d6a35a):
    - **git:** a Kustomization bound to a GitRepository whose `status.lastAppliedRevision` parses as a Git revision (`line.ParseGitRevision`);
    - **artifact:** a Kustomization bound to an OCIRepository or a Bucket: its applied revision kept as the artifact's digest, never read as a commit even when hex-shaped;
    - **chart:** a HelmRelease's newest **deployed** history entry's recorded chart name and version, as the line's units read it (slice 3a). Never the Helm `Resource.Revision`, which can come from the newest failed snapshot;
    - **unknown:** anything else (no applied revision yet, an unparsable one, an unsupported source kind). Unknown is a value of its own and is never compared.
  - **Observed only between two known values:** basis observed only when the previous and the current value are both known, of the same UID, and differ. Unknown → known is first evidence (a baseline, basis unknown). Known → unknown breaks the key's proof, and the next known value is a fresh baseline. A first sighting, an after-gap or a replacement never reads as an observed applied change.
  - **The query:** `AppliedChange(cluster, uid)` returns the current typed value, when its change was observed with the basis, and the partition's live-since.
- **The route's unit** comes from the route's Flux labels (`kustomize.toolkit.fluxcd.io/name` and `namespace`, else `helm.toolkit.fluxcd.io/name` and `namespace`), resolved to the unit's UID through the Observer's Flux rows. Without them, or without a single matching row, there is no revision proximity. The binding is today's labels: the sentence says the unit manages the route now and claims nothing about when it began to.
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
  - **The time axis** is the whole 30-minute window: `Sparkline` gains an explicit x-domain, so the hatched region and missing time keep their width instead of the retained points being stretched across it.
- **Configured weight and measured split** sit side by side, never merged.
- **Onset** is a typed result of a pure function over the 5xx share's attributed points, read in segments split at gaps over 1.5 steps (design review of 9705575):
  - **crossing:** a sample at or below 1% followed, inside one uninterrupted segment, by one above it: "first sampled crossing of 1% at <time> (1-minute rate, 30-second step)";
  - **above at first sample:** a segment's first sample (the first attributable one, or the first after a gap) is already above 1%: "already above 1% at <time>, the first sample (after a gap)". It has no crossing time;
  - **none:** no sample above 1%; **no samples:** nothing attributable.
- **Revision proximity** names the unit: "Kustomization flux-system/apps (manages this route now): applied a1b9e3c, change observed 20:28; first sampled crossing 20:29". Each value type has its own words: a commit's short sha (git), "artifact sha256:1a2b3c4d" (artifact, never linked to Git), "chart podinfo 6.7.1" (chart).
  - **It appears only when:**
    - the onset is a witnessed crossing;
    - the applied change was observed (basis observed);
    - the change is at or before the crossing and within the 10 minutes before it;
    - the `applied` partition was live continuously from the change through the crossing.
  - It is never a cause.
- **No metrics endpoint:** "no route metrics: configure a metrics endpoint", and nothing is estimated.
- **Below the drawing:** the rules and policies table, and the mesh note for global Services.
- **Polling:** per-rule metrics every 30 seconds while the view is open (the step), single-flight and cancellable: the bridge keeps the generated promise's cancel, so `bridge/ruleMetrics.ts` is reworked here. The route-level poll ends, because the drawing replaces the lane's traffic line.

## 6. Testing

- **8a:**
  - `Rail`: every segment type's style and its accessible words; branches.
  - The cert chain unchanged in content.
  - The chat ledger: each outcome and the redaction counts.
  - The ESO chain: the same evidence as today, on its steps.
  - `LinePos`: ranks, the fault fill from the fleet axes (a Pod fault with matching revisions fills it), a siding, the accessible name.
  - The codes: the old spine tests carried over.
- **8b:**
  - The walk over fake dynamic objects: diamond, cycle, depth and count bounds, unreadable, stale, replaced, cancellation mid-walk.
  - Reported edges, by each reason and message form, and a dependency the message does not name.
  - Supply facts: owner reference with a matching and a mismatched UID; no owner.
  - Proximity: inside and outside 10 minutes, and a status-only update never paired.
  - The inventory line.
  - The walk's reply fence: a late reply for an earlier selection is dropped.
  - `NotHereYet`: refusals (an unapproved URL, a bad sha, an unclean path), an alias URL mapped to the approved one, a key that is not T's (another ref), applied ahead, diverged and missing history, T's freshness, the path filter and name-status on a fixture repository.
- **8c:** the section's rendering from DTO fixtures, cancellation on reselection, and headless captures in both themes.
- **8d:**
  - The topology's per-rule verdicts.
  - The applied partition: baseline, change, gap and end; keyed by UID (a same-name replacement starts afresh); the Helm value from the newest deployed entry, never a failed snapshot.
  - The value types: a GitRepository-backed sha, an OCIRepository- and a Bucket-backed digest (hex-shaped, still an artifact), a chart, unknown. Unknown → known is a baseline, not a change; known → unknown → known re-baselines; known → known (different) is the only observed change.
  - Onset: a crossing, above at the first sample, above at the first sample after a gap, none, no samples.
  - Revision proximity shown and withheld (above at first sample, unobserved basis, too far, a coverage break, no single matching unit); the Helm wording.
  - The drawing from fixtures; captures in both themes.

## 7. Open points (ruled in design review of 9705575: all nine accepted, with the conditions folded into §2-5 above)

1. **The supply link:** only a controller ownerReference whose UID is verified names an ExternalSecret. There is no target-name inference.
2. **"Stale read"** in the graph means status behind generation, as the ratified staleness language already does.
3. **Change-proximity pairing:** a failing node pairs with updates to itself, its upstream nodes and their supply objects within the 10 minutes before its Ready=False condition time.
4. **Git "not here yet"** is a separate call carrying the graph's facts. Every fact is validated in Go, and T is always Go's own.
5. **Revision proximity** needs the applied-revision history (built in 8d). It appears only with an observed change, at most 10 minutes before the onset, and with continuous coverage.
6. **Onset** reads the 5xx share's attributed points only: the window's start is the first attributable sample.
7. **`LinePos` short codes:** the retired `spineCodes` algorithm is restored unchanged as the one source.
8. **The walk** is a bound call with a context, cancelled by the frontend on reselection.
9. **The route-level traffic poll** retires with the lane's traffic line, replaced by the per-rule read.
