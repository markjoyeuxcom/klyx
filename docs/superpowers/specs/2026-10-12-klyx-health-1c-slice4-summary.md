# Klyx health 1c, slice 4: the open cluster's mast, tree and Overview on the summary

Status: CLOSED by Codex (2026-10-12): a3cb975 + 070d3de (mast and tree), 4d8b545 + cc3546d (Overview, Top links, reveal), cc9b32b (removals). Spec: Codex APPROVE-WITH-CHANGES (2026-10-12); amended per its seven required changes (§1, §2.1, §2.2, §3, §5.4, §6), and per its confirmation's two corrections (Karpenter's primary part is nodeclaims; a mixed read with a stale kind is "current and last observed", not a lower bound). Parent spec: `2026-10-11-klyx-health-1c-cluster-summary.md` (§2 native counts, §5 coverage, §8 Frontend, §9 slice 4). Slice 3 (`2026-10-11-klyx-health-1c-slice3-rows.md`) is closed; its §4a count selector "lands now, the mast and tree are wired to it in slice 4".

## 0. Outcome and limits

The open cluster's three attention surfaces - the mast Attention button, the navigation tree's badges and notes, and Overview's Attention section - read the cluster's Observer publications instead of the per-entry fetch fan-out (`bridge/attention.ts`: seven list endpoints, then a 60 s re-poll). Every number comes from the one count selector, `taskCounts(st, cluster, task, part)`, so a lens header, its tree badge and the mast agree, and switch together under A4.

Not in this slice:

- the fleet surfaces (spine lamps, fleet meters, the fleet board's per-visit fan-out) - slice 5;
- deleting the "not covered" mechanism and the qualification of the quiet state - slice 6's parity gate (§6 below makes it summary-derived and fail-closed).

## 1. Source and lifecycle (review 5)

Per cluster, only the current incarnation (`knownIncarnation[cluster]`) is read. For each expected part (§6):

- **answered by rows**: a lens holds the part's rows of the current incarnation - `taskCounts` selects them (slice 3 §4a). Rows may arrive before the newer summary; those parts are answered;
- **answered by the summary**: the current summary carries the part;
- **unanswered**: neither - the part is "checking…", a gap, no count. An area is checking only when all its expected parts are unanswered; one answered part shows.

A newer incarnation replaces the old one whole (the store already clears an older incarnation's rows and summary in `advance`); no area keeps a value from it.

**Final.** The store keeps fenced terminal metadata: a Final summary records `ended[cluster] = { incarnation, at }` (`at` is the Final's `evaluatedAt`), and a newer incarnation from either channel clears it. A Final at or below the recorded incarnation is ignored, and so is a late older one. While `ended` holds for the current incarnation, no area shows a count: every surface says "observation ended at HH:MM:SS" (the mast, the tree's group notes, Overview's Attention section), and nothing reads as quiet.

**Namespaces.** Overview's namespace count is `Summary.Namespaces` from the current-incarnation summary only; until one supplies it, it is unknown ("—"), never a count from rows or an older incarnation.

## 2. Areas: one per expected task

Each expected task (§6) is one area, mapped by the nav registry to a group and a destination:

| task | group / destination | parts (classifier) | expected when |
|---|---|---|---|
| workloads | workloads / workloads | workloads (verdict) | always |
| pods | workloads / pods | pods (verdict) | always |
| nodes | cluster / nodes | nodes (verdict) | always |
| events | cluster / events | warnings (evidence) | always |
| helm | helm / helm | releases (native) | always |
| flux | flux / flux | reconciliations, sources (native) | capability `flux` present or not yet known |
| argo | argo / argo | applications, applicationsets (native) | capability `argo` present or not yet known |
| certs | certs / certs | certificates, issuers (native) | capability `certManager` present or not yet known |
| eso | eso / eso | externalsecrets, stores (native) | capability `externalSecrets` present or not yet known |
| gateway | network / gateway | gateways, routes (native) | capability `gatewayAPI` present or not yet known |
| karpenter | autoscaling / karpenter | nodeclaims, nodepools (native) | capability `karpenter` present or not yet known |

A part whose coverage the Observer reports `absent` (its counted kinds are not served) has no line; an area whose parts are all absent is omitted. A task the summary carries with a non-absent part is shown even if capability detection says absent (the Observer reads the cluster; detection may lag).

### 2.1 Part lines (review 1, 2)

**Usable evidence is the counted kinds', not the part's roll-up.** `Part.coverage` rolls up every kind, dependencies included, so it is not the line's evidence. A line's evidence is the roll-up of its **counted** kinds (`KindCoverage.role === "counted"`):

- counted live: the counts as read;
- counted stale: the counts as last read, with "last good HH:MM", a gap;
- counted checking: "checking…", a gap, no count;
- counted unreadable: "could not be read (class)", a gap, no count;
- mixed counted kinds, all read ones live (for example Deployments live, StatefulSets not yet read or unreadable): the counts are a lower bound on current attention over the kinds read - "3+ need attention" - with the missing kinds named as a gap;
- mixed counted kinds with a stale one (for example Deployments live, StatefulSets stale, DaemonSets unreadable): the retained stale objects may have recovered or gone, so the total is not a lower bound on current attention. The line keeps the counts and the faults, qualified as a reading of current and last-observed objects ("3 need attention - current and last observed"), and names each stale kind with its last good time and each missing kind. Nothing is recomputed per kind: the per-kind coverage qualifies the one count.

A known fault stays discoverable and nothing reads quiet in any of these.

**Dependencies qualify, they never hide.** A dependency kind that is not live (for example ReplicaSets unreadable for workloads or pods) names a gap beside the line ("owners unresolved - ReplicaSets could not be read"); the counted kinds' counts still show. **Derived membership is the exception:** a Gateway-attached route exists only through its parent, so for the routes part the parent Gateways count as evidence alongside HTTPRoutes - with Gateways not live, attachment membership is not established and the routes line follows the worse of the two (stale with last good, or no count). Services and EndpointSlices only qualify backend verdicts (a gap note; the counts stand).

A discovery gap (`KindCoverage.discovery`) is named beside a live count as a capability gap, never as staleness.

**Verdict lines** (`Counts`): attention is fault + degraded; tone danger with any fault, else warning; the text reuses the verdict vocabulary ("3 need attention · 1 unverified"). `unknown > 0` is "n unverified", a gap that blocks quiet.

**Native lines** (`Native`):

- lit (attention) only when `Roles.fault + Roles.degraded > 0`, independently of `Included`; tone danger with any fault role, else warning;
- the line's number is `Included` with the tool's `Word` ("2 not ready"), as the lens counts - so an `Included` made only of progressing or paused objects is shown in the tool's vocabulary, unlit, never as attention;
- `Roles.unknown > 0` is "n unknown", a gap that blocks quiet, even when `Included` is 0.

**Evidence lines** (Events warnings): "12 warning events", no tone, never attention; a truncated snapshot is a lower bound ("12+"). Snapshot tasks (Helm, Events) show their read time (`asOf`).

**Controller issues** (`Task.controller`), separate from every count:

- `not ready` (verified): lights the area with the controller's state and reason and no object count; it counts in the mast's areas needing attention;
- `unknown`: an unverified gap on the area ("controller health unknown - <reason>"); it never counts as attention;
- either can coexist with object lines: an area can be lit by objects and carry an unknown-controller gap.

### 2.2 Badges: group, destination, part (review 3)

- **Area and group**: the strongest line - danger before warning, then the first counted part - never a sum across parts or areas (two parts are two representations or two vocabularies). The badge's title names the area and the part it came from ("Flux sources: 3 not ready - cluster summary as of 12:04:31").
- **Destination**: a destination badges exactly one part, its own:

| destination | part |
|---|---|
| workloads, pods, nodes | workloads/workloads, pods/pods, nodes/nodes |
| events | events/warnings (evidence, no tone) |
| helm | helm/releases |
| flux, flux-sources | flux/reconciliations, flux/sources |
| argo, argo-appsets | argo/applications, argo/applicationsets |
| certs, eso, gateway, karpenter | certs/certificates, eso/externalsecrets, gateway/gateways, karpenter/nodeclaims |

The destination badge equals that part's lens count (its header in that view), at one selection. A multi-view lens destination (certs, eso, gateway, karpenter) badges the part of the view the lens opens on - its primary - only (Karpenter opens on Claims); a lit secondary part (issuers, stores, routes, nodepools) is the destination's note ("issuers: 1 not ready"), never a number on its badge. A generic resource destination (Issuers, SecretStores, NodeClaims, ApplicationSets as raw resources) never carries a part's count: its kinds differ from the part's. Argo Projects has no part and no badge.

The mast counts AREAS needing attention, never objects (unchanged).

## 3. Counts: the one selector and its A4 contract (review 4)

Every number is `taskCounts(st, cluster, task, part)`, and every surface consumes all of it:

- `source: "rows"`, `updating: false` - the rows' counts;
- `source: "rows"`, `updating: true` - the rows are held while a newer summary is inside its first second (slice 3 A4), or a wrong-base resync is pending: the rows' counts, marked as updating on every surface;
- `source: "summary"`, `refreshing: true` - past the deadline: the summary's counts on every surface together, marked as refreshing; back to the rows after the re-bootstrap;
- `source: "summary"` otherwise - the summary's counts;
- a resync or a `refreshError` is a visible gap on its line ("rows re-reading" / "last re-read failed: …") even with no newer summary.

**One selected part, one provenance.** A line's counts, coverage, Top refs and time all come from the selected part - the rows' part or the summary's - never a mix. `SelectedCounts` gains the selected part's evaluation time: rows-sourced lines are captioned "<lens> rows as of <rows' evaluatedAt>", summary-sourced lines "cluster summary as of <summary evaluatedAt>"; a newer row-derived count is never captioned with an older summary's time.

The tree's ad hoc overrides go: the Pods badge "live lens wins" rule (e5288d2) and the Flux sources count read from the Flux lens become ordinary parts read through the selector.

## 4. Coverage and freshness wording (summary)

Per line, from §2.1: live (as read), stale ("last good HH:MM", gap), checking ("checking…", gap), unreadable ("could not be read (class)", gap), absent (no line), mixed counted kinds (lower bound, gap), dependency not live (gap note), discovery gap (named), truncated (lower bound), snapshot (as of), controller not ready (lit, no count), controller unknown (gap), resync or refresh error (gap), observation ended (§1). The mast's quiet state stays qualified (§5.1, §6).

## 5. Surfaces

### 5.1 Mast (`TopBar` AttentionButton)

- lit: "▲ 2 areas need attention · 3 gaps" - areas lit by a fault/degraded line or a verified not-ready controller;
- otherwise scoped to what was read: "No attention in 9 areas read · 3 gaps · 1 not covered" - gaps and not-covered parts counted independently of lit areas, never an unqualified all-clear;
- "Checking…" while every expected part is unanswered; "Observation ended at HH:MM:SS" after a Final (§1);
- the tooltip lists each area's lines with their provenance (§3) and the not-covered parts; "refreshing" while any part is refreshing.

### 5.2 Tree (`useNavCounts`, `NavBand`)

Group and destination badges per §2.2; group and destination notes per §2.1-§4 ("unreadable", "last good", "unverified", "checking…", "not covered", "issuers: 1 not ready"). Badge titles name the part and the provenance.

### 5.3 Overview (`AttentionSection`)

One row per area with its lines; each lit line lists up to three `Top` refs as links (§5.4); the not-covered entries (§6); the namespace count from §1; Overview's other reads of the attention slice (`counts.flux` for the Flux capability line) move to the summary's flux task.

### 5.4 Top links enter the emitting part's view (review 7)

A Top ref names a task (`Route`); the link is navigated by task, part and ref through a new selection-only intent, never an action intent:

```ts
{ kind: "reveal"; cluster: string; task: string; part: string; ref: { gvr: string; namespace?: string; name: string; uid?: string; parent?: string } }
```

- the part's view is opened: Flux `fluxView: "sources"` for flux/sources, Argo `argoView: "applicationsets"` for argo/applicationsets, and the lens-local views - certs "issuers", eso "stores", gateway "routes", karpenter "claims" and "pools" - which the lens switches to on consuming the intent;
- the object is revealed through the lens's filters and selected where the lens supports selection (the existing selection intents for pods, workloads, nodes and Flux reconciliations - `select-pod`, `select-workload`, `select-node`, `select-flux` - remain the route for those parts; Issuer and ClusterIssuer are told apart by `gvr`; an attached route by its `parent`, the worst parent);
- a lens that cannot select the object opens the part's view and the link says so ("open Issuers"), never the object's name as if it were selected.

## 6. "Not covered" until slice 6: summary-derived and fail-closed (review 6)

The static `UNCOVERED` list goes. "Not covered" derives from the expected mapping (§2 table): for each expected task and each of its parts,

- a part present in the current summary (or answered by rows) is covered;
- a task the summary carries but missing a part is incomplete: that part is "not covered by the summary - open to check";
- a task missing from the summary, capability present: "not covered by the summary - open to check";
- a task missing from the summary, capability not yet known (checking or unreadable detection): "capability not yet known - not covered by the summary" - it never silently disappears;
- confirmed absent (capability absent and no non-absent summary part): omitted.

Not-covered parts count in the mast and Overview independently of lit areas and gaps. Slice 6's parity gate removes the mechanism and the qualification of the quiet state.

## 7. Removals

- `bridge/attention.ts` (`fetchAttention`, the 60 s re-poll, `abandonAttention`), `useClusterAttention`, the store's `AttentionSlice` and its setters.
- The tree's lens-specific overrides (§3).
- The `bridge/overview.ts` helpers that only fed `fetchAttention` (kept where the fleet board still uses them until slice 5).
- Request methods (`ListPods`, `ListNodes`, `ListEvents` and so on) stay while `bridge/fleetboard.ts` calls them; those go in slice 5.

## 8. Tests

Mounted on the real health store (the slice 3 pattern), plus mutation checks:

- Lifecycle: no summary; rows before the summary (answered parts show, unanswered checking); Final (ended at, no counts, nothing quiet); a late older Final ignored; a newer incarnation clearing `ended` and carrying nothing over; staggered parts of a newer incarnation; namespaces unknown until a current summary.
- Evidence: counted live with an unreadable dependency (the known fault shows, the gap is named, the lens agrees); mixed counted kinds, live and missing (lower bound, gap, no quiet); mixed live, stale and unreadable (no lower bound, "current and last observed", stale and missing kinds named); routes with Gateways not live (membership not established); Services/EndpointSlices only qualifying; discovery gap; truncated Events; snapshot as-of.
- Native and controller: lit only by fault/degraded roles; `Included` of progressing/paused unlit in the tool's word; unknown-only native part (gap, no quiet, `Included` 0); controller not ready (lit, no count, counted in the mast); controller unknown alone (gap, not attention); objects lit plus controller unknown.
- Badges: strongest line never summed, titled with its part; Karpenter with a healthy pool and a failing claim (the destination badge agrees with the Claims view the lens opens on); a secondary part stronger than the primary (group badge from the secondary, destination badge stays the primary's, the secondary as a note); flux and flux-sources, argo and argo-appsets each equal to their part's lens count with each view open in turn; no part count on a generic resource destination.
- Selector: rows newer than the summary win on all three surfaces with the rows' own time; inside the first second (updating) all three keep the rows' counts marked; past the deadline all three switch to the summary together and return after the re-bootstrap; a summary-less resync and a failed re-bootstrap are visible gaps; counts, Top refs and time from one selected part.
- Not covered: a missing core task; a task missing a part; a present capability without its task; an unknown capability without its task; a confirmed absence omitted.
- Top links: Issuer and ClusterIssuer, a Store, a Flux source, an ApplicationSet, a NodeClaim and a NodePool, an attached route with its worst parent - each opens the part's view and reveals or labels; no action intent is raised.
- No fetch fan-out remains (no `ListWorkloads`/`ListPods`/… call on cluster entry).

## 9. Order of work

One reviewed commit each:

1. The store and model: `ended` metadata, `SelectedCounts` time and provenance, the expected mapping, part lines and area values (pure, tested), with the mast and the tree.
2. Overview's Attention section, Top links and the `reveal` intent in the lenses.
3. The removals (§7).
