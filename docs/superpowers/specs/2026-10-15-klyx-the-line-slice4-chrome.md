# The Line, slice 4: chrome

Design note for slice 4 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §4 (chrome and navigation), §3.8 (the station strip), §2.4 (primitives), §10 (accessibility).
- **Mockup of record:** the Cluster screen on the "Klyx redesign" canvas.
- **Scope:** frontend only. It reads `LineDTO` (slice 3), the health store and the fleet store, and adds no backend.

## 0. What exists (checked against the tree)

Several facts here correct the parent's §0:
- **`AppShell`** stacks `TopBar`, then `FleetSpine` beside `NavBand` and the workspace, then `ChatDrawer`. FKeysBar is already gone.
- **`TopBar`** holds:
  - the mark;
  - history arrows with `RecentMenu`;
  - the "cluster / place" identity;
  - `AttentionButton`, which opens the nav tree;
  - the "prd lock" chip;
  - the ⌘K well;
  - `ForwardsIndicator`;
  - Ask, Activity, Terminal and Settings (with a new-contexts count);
  - `ThemeToggle`.

  `PopoutBar` is the logs window's mast.
- **mod+1..9 switch cluster, not lens.** They open `spineOrder(...)[n-1]`, which is env rank then fleet order. `NavBand` defines no keys.
- **There is no theme shortcut.** The parent's "the existing shortcut" does not exist. The theme is the bar toggle and a palette command. Settings has no Appearance section.
- **`useResizableDock` sizes the logs dock.** The inspector's width is `LensLayout`'s, and below 1040px the inspector is a modal drawer.
- **`InspectorLayout`** is used by 19 lens files. Its anatomy is identity, verdict, actions, Overview/Manifest tabs, then body.
- **The inspector's cluster is the route's cluster.** Selections carry a UID but no cluster.
  - `useInspectorAI` falls back to the route cluster when its caller omits one. Seven callers omit it.
- **Nothing in the frontend consumes `LineDTO` yet.**
- **The Go env-rank parity test** (`internal/line/env_parity_test.go`) reads `ENV_RANK` from `FleetSpine.tsx`.

## 1. Sub-slices

Each sub-slice is Codex-reviewed on its own, in this order:

| | Builds | Retires |
|---|---|---|
| **4a** | Location bar, fleet indicator, Settings > Appearance, the parity table (§2) | `TopBar`'s mast, `AttentionButton`, the main window's `ThemeToggle`, `FleetSpine` |
| **4b** | Lens index (§4) | `NavBand`, its group selector and the nav-tree event |
| **4c** | `LineDTO` bridge and store, station strip (§5) | - |
| **4d** | Inspector anatomy, cluster and UID pinning, the object crumb (§6) | Route-cluster fallbacks in inspectors |

`PopoutBar` stays for the logs window, with its `ThemeToggle`: a pop-out has no Settings, and its theme is its own window's.

## 2. The parity table, implemented

Every function of the retired chrome gets a home in the same sub-slice that retires it.

| Today | New home | Sub-slice |
|---|---|---|
| Mark (to the fleet) | Location bar mark | 4a |
| Back, forward, `RecentMenu` | Location bar arrows; the recent menu stays on them until the console (slice 9) takes recent places | 4a |
| "cluster / place" identity | Crumbs: Fleet / Station / Lens / Object | 4a (Object in 4d) |
| `AttentionButton` (the open cluster's areas, the nav tree) | The fleet indicator for clusters, the open one included. The lens index's "Needs you" for the open cluster's areas (always visible). Between 4a and 4b, `NavBand`'s badges and its tree carry the open cluster's areas. | 4a, 4b |
| "prd lock" chip | A persistent "protected" tag on the station crumb, and in the station strip. "protected (inferred)" when inferred. | 4a, 4c |
| ⌘K well | Location bar ⌘K well ("Go to, run or ask") | 4a |
| `ForwardsIndicator` and stop-all | Location bar "forwards" utility on every route: with none active it opens the Forwards view; with any active it shows the count and a list with stop-all | 4a |
| Ask | Location bar utility. Disabled with a reason on routes with no cluster. | 4a |
| Activity, Terminal, Settings (with its count) | Location bar utilities on every route. Terminal opens the fleet's kubeconfig, not one cluster's, so it is not tied to an open cluster. | 4a |
| `ThemeToggle` | Settings > Appearance (new) and the existing palette command. No new shortcut. | 4a |
| `FleetSpine` lamps and pager | The fleet indicator (§3), plus the station switcher on the station crumb (§2.1) | 4a |
| Spine expand toggle (`klyx.layout.v1.spineExpanded`) | Retired with the spine; the key is ignored | 4a |
| mod+1..9 | Unchanged targets: `stationOrder`, the spine's order moved to `nav/stationOrder.ts` | 4a |
| `NavBand` tabs, "More", Resources menu | Lens index entries (§4). The Resources and API browser entries reach every raw kind, as the palette does. | 4b |
| Group selector (search dialog, `OPEN_TREE_EVENT`) | ⌘K (the palette already indexes every registry destination) | 4b |
| Fleet route tabs (`FLEET_TABS`) | The lens index on fleet routes lists the fleet views | 4b |
| Overview's sections | Unchanged in slice 4; they move to the station page in slice 11 | - |

### 2.1 The station switcher

The spine was the only one-click way to reach a healthy cluster. The fleet indicator lists only affected clusters, so the station crumb opens a **station switcher**:
- every cluster in `stationOrder`, each with its station label and its fleet-indicator mark (or none);
- selecting one opens that cluster, as the spine did;
- mod+1..9 hints are shown beside the first nine.

This is additive to the parent: it keeps the spine's pager role reachable without the lamp column. It is flagged for review.

### 2.2 Station order and the parity test

`stationOrder` keeps the spine's order: env rank, then fleet order. That keeps mod+1..9's targets.
- It moves, with `ENV_RANK`, to `nav/stationOrder.ts`.
- The Go parity test follows the file. It still pins the frontend's copy to `line.EnvRank`.

The order is not read from `LineDTO`:
- the order must be stable before the first DTO arrives;
- it must hold for clusters with no Observer.

### 2.3 Geometry (as built)

- **Height:** 40px (`--mast-height`), up from the mast's 36px.
- **Crumbs:** "Fleet" in the label face, `/` separators, the station as "ENV · REGION" in the label face beside the cluster name in mono, then the lens. On the fleet board "Fleet" alone is the current place; on other fleet and app routes it is "Fleet / place".
- **Shrink order:** the ⌘K well's words first (down to its icon), then the station's name, then the lens (never below 7ch). The protected tag, the fleet indicator and every utility never shrink, so nothing leaves the screen at 1024px.
- **Ask:** off a cluster it stays, `aria-disabled`, with its reason ("a conversation is bounded to one cluster").

## 3. The fleet indicator (4a)

### 3.1 The model

A pure `nav/fleetAxes.ts`, built on `fleetAttention.ts` and `clusterAttention.ts`. It returns clusters per axis, never objects.

| Axis | A cluster is on it when | Reason shown |
|---|---|---|
| **fault** | `mastReading` is attention with a danger tone | its first danger line, or the controller that is not ready |
| **warning** | attention with a warning tone, and not fault | its first lit line |
| **unreachable** | Failed or Unconnected | `connectionText` |
| **connection** | Stale or Degraded | `connectionText` |
| **unverified** | Any of: Connecting (any state that is not settled); observation ended; or Synced or Degraded with `healthGaps`, as `fleetMeters` counts unknown | the connection text, "observation ended at …", "health checking…", or the first gap |

**Overlaps:**
- Fault and warning are exclusive: danger wins, as the spine's lamp did.
- An **unreachable** cluster is on no other axis. Its health reading is not current.
- **Otherwise the axes are independent facts**, as `fleetMeters` already counts them. For example, a Degraded cluster with a danger area is on both fault and connection.
- **Each axis count is of clusters.** Nothing is summed across axes, and no object count appears.

### 3.2 The control

- **The chip:** each non-zero axis shows its own mark and count, in the order fault, warning, unreachable, connection, unverified.
  - **Marks:**
    - fault: `Aspect` fault;
    - warning: `Aspect` attention;
    - unreachable: a Tabler plug-off icon;
    - connection: a Tabler antenna-off icon;
    - unverified: `Aspect` checking.
  - Every mark pairs with text in the accessible name. For example: "Fleet: 1 cluster with a fault, 1 unreachable, 2 unverified, of 6".
  - With every axis at zero, the chip reads "6 clusters" with no mark. That is never an all-clear wording, because zero unverified already means no gaps.
- **The list:** a click opens a popover.
  - Clusters are grouped by axis, in chip order. A cluster on two axes is listed under each.
  - Each row has its mark, its station label (env · region, cluster name) and its reason. It links to `openCluster`.
  - **Keyboard:** the button carries `aria-expanded`. The list uses roving focus. Escape closes it and returns focus to the button.
- **Visibility:** on every route, including inside a cluster (parent §4).
- **Retired with it:**
  - the blink and the lamp column (owner trade-off, parent §14.4);
  - `spineCodes` and the spine's expand toggle.

  `fleetMeters` and `ClusterCard` keep their readings. `ClusterCard` still calls `spineLamp`, so it stays.

## 4. The lens index (4b)

### 4.1 Entries

A pure `nav/lensIndex.ts` turns the registry, `navCounts` and `capState` into the index:
- **Core groups:** every task destination of Cluster, Workloads, Network, Access and Autoscaling is its own entry: Overview, Nodes, Events, Upgrade check, Workloads, Pods, Connectivity check, Gateways and routes, Hubble flows, Access check, Karpenter.
- **Tool groups** (Flux CD, Argo CD, cert-manager, External Secrets, Helm) are **one lens each**, labelled with the group's name.
  - The lens opens its first available task.
  - Its other task destinations (Flux sources; Argo projects and ApplicationSets) show as indented sub-entries while the lens is open.
  - The registry marks these groups with `lens: "group"`.
- **Generic resources:** two entries.
  - **"Resources"** is the built-in resource browser.
  - **"API browser"** is the CRD browser.

  Every raw kind the NavBand Resources menu listed stays reachable through them and through ⌘K. The registry's `res(...)` entries are unchanged.
- **Labels:** an optional `indexLabel` on a destination changes only the index's label:
  - "All workloads" → "Workloads";
  - "Built-in resources" → "Resources";
  - "CRDs by API group" → "API browser".

  The palette keeps the registry label and keywords.

### 4.2 Sections

- **"Needs you":** every entry or sub-entry in `navCounts.attention`.
  - **Order:** danger before warning, then count (descending), then registry order.
  - **A row shows:** its `Aspect`, its label and its badge count. A sub-entry reads "Flux CD · Sources".
- **"Everything else":** every other entry, in registry order. Each carries its quiet note when it has one: "unverified", "last good", "controller not ready" or "checking…".
- **Capability states (parent §4):**
  - **checking or unknown:** the entry is listed, disabled, with "checking…" or "unknown". Today's NavBand behaves the same way.
  - **confirmed absent:** the entry is hidden.
  - **Absent note:** one quiet line names every absent lens. For example: "Argo CD and Karpenter are not installed here, so they are not listed."
- **Fleet and app routes:** the index lists the fleet views (`FLEET_TABS`) with the current one marked. Settings, Activity and Forwards add no index entries.

### 4.3 Behaviour

- **Width:** 232px.
- **Collapse:** mod+\ toggles it, as does a button at the start of the location bar (`aria-expanded`, `aria-controls`). Collapsed means hidden, not a rail. The state persists under `klyx.layout.v1.lensIndex`.
- **Semantics:**
  - `nav aria-label="Lenses"`;
  - each entry is a link-styled button;
  - the active entry carries `aria-current="page"`;
  - no Tab hijacking.
- **Counts:** read through `useNavCounts` only. The parity gate and `noFanout` stay green.

### 4.4 As built (4b)

- **Tone in "Needs you":** danger when the entry's own badge is danger or its note names a not-ready controller (its area's tone); otherwise warning.
- **Notes:** a capability's own word ("checking…", "unknown") wins over the parts' words on an unavailable lens. On a row, the label wins: a long note (a secondary part's, such as "routes: 3 with a failing attachment") truncates, and its full text is the row's title.
- **Layout:** rows in "Needs you" lead with their `Aspect`; every label lines up after the mark column; counts sit at the row's right edge. A tool lens's indented sub-entries never repeat one already in "Needs you".
- **The current entry:** a raw kind of a tool group marks that tool's lens; any other raw kind marks the browser it was opened from (API browser for `crds`, else Resources); a kind shortcut or a custom kind mix marks Workloads.
- **The parity gate's tree stage** now checks the index: every lit line's lens and every not-ready controller in "Needs you" with its mark, and every gap word on its lens. The captured cases exercise the "unverified" and "last good" words; none has a whole part unreadable.
- **Retired:** `NavBand`, its tab fitting, `nav/navTree.ts` and the `klyx:open-nav-tree` event.

## 5. The station strip (4c)

### 5.1 Acquisition

- **`bridge/line.ts`:** subscribe to `line:dto`, then call `LineService.Line()`. Keep a DTO only when its version is greater than the held one. An equal or older version is ignored, whatever order the reply and the events arrive in.
- **`store/line.ts`:** a zustand store holding `{ dto: LineDTO | null }`. It never polls (parent §3.6).

### 5.2 The model

A pure `line/stationStrip.ts` derives the open cluster's strip from the DTO:
- **Its track:** the DTO's stations with the same `track`, in DTO order (rank, then fleet order).
- **Station marks:**
  - fault when `flags.failing`;
  - attention when `held` (the word "held");
  - checking when the state is checking;
  - syncing when `flags.syncing`;
  - otherwise none.
- **This station:** boxed, with its state word in the tool-neutral §3.3 wording: "matches", "differs", "mixed", "applied unknown", "not compared" or "checking".
- **Segments:** lit (route colour) only when the stations at both ends match R (parent §3.3 lighting). Otherwise the neutral track. The strip draws no behind, ahead or held order: that is Git's, in slice 6.

**Special cases:**

| Case | Strip |
|---|---|
| Siding | "siding" |
| Cluster absent from the DTO | Nothing |
| No DTO yet | Nothing |
| Station read is checking | Its marks, plus "checking…" in its box |

The "protected" tag is drawn once, on the station crumb (4a); the strip's phrase says it with this station ("prd, this station, protected, differs at 9f2c11d").

### 5.3 Accessibility

- **The group is labelled** "Position on the line". Its accessible text reads, for example, "dev matches; stg matches; prd, this station, differs at 9f2c11d, failing".
- Segments are `aria-hidden`.

### 5.4 As built (4c)

- **Start:** `AppShell` starts the line bridge beside the summaries, so the strip is ready before any cluster opens.
- **Placement:** the strip sits after the crumbs. A segment is 22px of track, lit in the route colour only between two "matches".
- **Narrow windows:** the crumbs win. Below 1280px only the boxed station is drawn; below 1180px no drawing is shown. The spoken phrase always names the whole track.
- **Labels:** a station is named by its environment, or by its cluster when two stations on the track share one. A git SHA is shortened to seven characters; a chart version is not.

## 6. The inspector (4d)

### 6.1 Anatomy

`InspectorLayout` renders, in order:
1. identity: kind, namespace, name and meta;
2. the verdict sentence with its `Aspect`, from a new `aspect` prop beside `verdict`;
3. an optional `drawing` slot;
4. actions;
5. evidence: the existing Overview/Manifest tabs and body.

No lens changes its content in slice 4. The lens sweep is slice 11.

### 6.2 What exists (surveyed 2026-10-15)

- **The cluster:** `ClusterDetail` reads the route's cluster and passes it to every lens as `cluster`. No lens or inspector reads the route itself; the only other reader is `useInspectorAI`'s fallback.
- **Selections:**
  - Store-held selections (Pods, Nodes, Helm, Flux reconciliations, certificates, external secrets, topology routes) sit in a cluster-fenced slice: the lens reads them only when `slice.cluster === cluster`.
  - Local selections (Workloads, Events, Access, Upgrade, Argo, Gateways, Gateway routes, Karpenter, Flux sources, issuers, secret stores) are keys in `useState`, reset by an effect when the cluster changes. That effect runs after a render, so for one render an old key meets the new cluster.
  - CRD instances are selected through the route (`route.instance`), so they carry the route's cluster.
- **UIDs:** most inspectors have one. Seven have none in their DTO: Events, the topology's routes, Access grants, Upgrade's deprecated APIs and node skew, Argo projects, Karpenter events. Helm releases use the release identity.
- **AI:** `useInspectorAI` takes the cluster from its caller, else the route. Seven callers omit it: the external secret and its store, the Flux source, the ApplicationSet, the Gateway, the issuer and the NodePool. "Ask a follow-up" uses the hook's cluster.
- **Confirmations:** every `ConfirmDialog` and `OperationDialog` takes the lens's or inspector's `cluster`; Nodes, Flux, Argo and the workload pod delete pin it when armed and drop the dialog if the cluster changes.
- **Gone objects:** Pods, Nodes, Workloads, Gateways, Gateway routes and Karpenter already say **"removed"** only after a complete read of the kind and **"unverified"** otherwise, keep the last row as evidence, and disable actions. Pods, Nodes and Workloads select by namespace/name and run verbs by UID, so a same-name replacement becomes the selected object and is read afresh.

### 6.3 Pinning (revised from the survey)

**`InspectorLayout` takes a required `pin`:** `{ cluster, uid?, kind, namespace?, name }`. `uid` is omitted for the objects with none (above).

**Consumers read the pin from `InspectorPin` context:**
- `useInspectorAI` uses the pin's cluster when its caller passes none, and the route fallback is removed. That fixes the seven callers by construction.
- "Ask a follow-up" opens the chat on the pin's cluster.
- Confirmations keep their pinned clusters. Their typed name already comes from the lens or inspector, never the route.

**Local selections carry their cluster:** a local key becomes `{ cluster, key }`, and a lens reads it only when `sel.cluster === cluster`. The one-render overlap goes; the reset effects stay as cleanup. Store-held selections are already fenced by their slice.

**Gone and replaced:**
- `InspectorLayout` gains one `gone` presentation, `"removed" | "unverified" | "replaced"`. The lenses' existing removed/unverified rule (by read completeness) stays the source; "Deleted" from the earlier draft is dropped, since it ignored whether the read was complete.
- **Replaced** is new, and only where a UID exists: the lens finds the object at the pinned namespace/name with a different UID. The inspector says "Replaced: this object was deleted and created again", offers "Open the new object", and disables every action. Explain and follow-ups stay on the pinned UID.

**Open point for review (6.3a):** "Replaced" changes behaviour in Pods, Nodes and Workloads, which follow a same-name replacement today. The parent pins the inspector to its UID (parent §4), which this follows. The alternative keeps following by name, and marks the inspector "Replaced at <time>" while it reads the new object afresh.

### 6.4 The object crumb

A small `inspectorPin` store, set while an inspector is mounted, gives the location bar its fourth crumb: kind and name. Clicking it focuses the inspector.

## 7. Tests

| Sub-slice | Tests |
|---|---|
| 4a | `fleetAxes` cases from parent §12 while inside another cluster: a remote warning, a controller-only fault, a failed connection, a lost watch, plus overlaps and the all-zero wording. Popover keyboard and focus return. The station switcher. mod+1..9 targets unchanged. Settings > Appearance. Ported `TopBar` and `FleetSpine` tests. |
| 4b | `lensIndex` cases: ordering by severity and count, sub-entries, checking and unknown listed, absent named once, fleet routes. Collapse and persistence. Every registry destination reachable through the index's browsers or ⌘K. Ported `NavBand` tests. The parity gate and `noFanout`. |
| 4c | The bridge keeps only newer versions: a reply older than an event, and duplicate events. `stationStrip` cases: siding, absent, checking, lighting at both ends only, held, failing. Accessible text. |
| 4d | A cluster change with an inspector open drops the selection on the same render. AI and chat use the pin's cluster; no route fallback remains. Removed, unverified and replaced disable actions; replaced offers the new object. UID-less inspectors never read as replaced. The object crumb. |

**Every sub-slice also passes:**
- tsc, eslint and vitest;
- vite build;
- headless Chromium captures of the cluster screen in `night` and `day` at 1440×900 and 1024×768, for the review.

## 8. Open points for review

1. **The station switcher (§2.1).** It is additive to the parent. Without it, a healthy cluster is reached only through the fleet board or ⌘K.
2. **Order stays env rank in TS (§2.2),** rather than the DTO's placement, so mod+1..9 keep their targets before the line loads.
3. **Tool groups as one lens with sub-entries (§4.1),** rather than every task destination at the top level. This follows the mockup.
4. **No theme shortcut is added (§2).** The parent's "existing shortcut" never existed.
5. **Replaced versus following by name (§6.3a).**
6. **One "protected" tag (§5.2).** It is drawn on the station crumb only; the strip's phrase says it.
7. **The strip on narrow windows (§5.4).** Below 1180px its drawing is hidden and only its phrase remains.
