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

The "protected" tag is drawn beside the boxed station.

### 5.3 Accessibility

- **The group is labelled** "Position on the line". Its accessible text reads, for example, "dev matches; stg matches; prd, this station, differs at 9f2c11d, failing".
- Segments are `aria-hidden`.

## 6. The inspector (4d)

### 6.1 Anatomy

`InspectorLayout` renders, in order:
1. identity: kind, namespace, name and meta;
2. the verdict sentence with its `Aspect`, from a new `aspect` prop beside `verdict`;
3. an optional `drawing` slot;
4. actions;
5. evidence: the existing Overview/Manifest tabs and body.

No lens changes its content in slice 4. The lens sweep is slice 11.

### 6.2 Pinning

**`InspectorLayout` takes a required `pin`:**
- `{ cluster, uid?, kind, namespace?, name }`;
- `uid` is omitted only for objects without one (Helm releases, which use the release identity).

**Consumers read the pin from `InspectorPin` context:**
- `useInspectorAI` uses the pin's cluster and UID whenever its caller passes none. That fixes the seven route-cluster fallbacks by construction.
- "Ask a follow-up" opens the chat on the pin's cluster.
- Actions built inside an inspector take the pin's cluster. `ConfirmDialog`'s typed cluster name is the pin's.

**Selections carry their cluster:**
- Each lens's selection type gains `cluster`.
- A lens renders an inspector only when `selection.cluster` equals its own cluster. Any other selection is dropped on render, never re-targeted.

**UID fencing:**
- Each lens passes the UID it currently finds at the pinned name (`liveUid`).
- **Replaced** (a different UID): the inspector says "Replaced: this object was deleted and created again", offers "Open the new object", and disables every action.
- **Missing:** it says "Deleted", and actions are disabled.
- Explain and follow-ups stay fenced to the pinned UID, as today.

### 6.3 The object crumb

A small `inspectorPin` store, set while an inspector is mounted, gives the location bar its fourth crumb: kind and name. Clicking it focuses the inspector.

## 7. Tests

| Sub-slice | Tests |
|---|---|
| 4a | `fleetAxes` cases from parent §12 while inside another cluster: a remote warning, a controller-only fault, a failed connection, a lost watch, plus overlaps and the all-zero wording. Popover keyboard and focus return. The station switcher. mod+1..9 targets unchanged. Settings > Appearance. Ported `TopBar` and `FleetSpine` tests. |
| 4b | `lensIndex` cases: ordering by severity and count, sub-entries, checking and unknown listed, absent named once, fleet routes. Collapse and persistence. Every registry destination reachable through the index's browsers or ⌘K. Ported `NavBand` tests. The parity gate and `noFanout`. |
| 4c | The bridge keeps only newer versions: a reply older than an event, and duplicate events. `stationStrip` cases: siding, absent, checking, lighting at both ends only, held, failing. Accessible text. |
| 4d | A route-cluster change with an inspector open drops the selection. AI and chat use the pin's cluster. Replaced and deleted UIDs disable actions. The object crumb. |

**Every sub-slice also passes:**
- tsc, eslint and vitest;
- vite build;
- headless Chromium captures of the cluster screen in `night` and `day` at 1440×900 and 1024×768, for the review.

## 8. Open points for review

1. **The station switcher (§2.1).** It is additive to the parent. Without it, a healthy cluster is reached only through the fleet board or ⌘K.
2. **Order stays env rank in TS (§2.2),** rather than the DTO's placement, so mod+1..9 keep their targets before the line loads.
3. **Tool groups as one lens with sub-entries (§4.1),** rather than every task destination at the top level. This follows the mockup.
4. **No theme shortcut is added (§2).** The parent's "existing shortcut" never existed.
