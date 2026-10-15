# The Line, slice 11: the lens sweep and the station page

Design note for slice 11 of `2026-10-14-klyx-the-line-redesign.md` (the parent).
- **Parent sections:** §9 (applying it to every lens), §2.3 and §2.4 (rows, `Aspect`), §4's parity table (the Overview's sections on the station page), §3.7 ("Needs you") and §3.3 (station facts). The parent is the contract; this note records what exists, how the slice is cut and the points the parent leaves open.

## 0. What exists (surveyed 2026-10-15)

- **Tables:** every table lens renders `LensTable` with columns fitted by `layout/columns.fitColumns`. Most column sets start with a 16px `lamp` column.
  - Row height is `TABLE_ROW_HEIGHT = 32` (`layout/LensLayout.tsx`), written into `--table-row-height`, whose CSS default is also 32px. `PodsView` passes a literal `rowHeight={32}`.
- **Row marks:**
  - Most rows draw `VerdictLamp` (an `Aspect` via `aspectOfVerdict`); a healthy row draws no mark.
  - Eight lenses wrap it in an identical local `Lamp` (GitOps, Certs, ESO, Helm, Karpenter, Gateways, HTTPRoutes, GatewayTopology), and Argo has its own `RowLamp`.
  - `InstanceList` draws a custom round amber dot, which is not an `Aspect`.
  - Events and Upgrade colour their text only.
  - Status cells repeat a fault as `▲` text: Helm, Karpenter, and Upgrade's removal and skew cells.
- **Not yet realized:** `cluster/staleness.tsx` dims a row whose `generation > observedGeneration` (`data-stale`) and shows a grey "Syncing…" chip. The row's mark is still its verdict's, so a stale row that is otherwise ok has no mark.
- **Headers:** each lens passes `title` and a `summary` fragment to `LensLayout`. A summary is a bare number, then a coloured attention phrase (with `▲` text on faults), then a secondary breakdown. Each lens has its own words; the survey is in the working notes.
- **Green:** in `night` and `day`, `--color-text-success` is already ink-2 (slice 1), so the healthy usage cells (Workloads, Nodes), Argo's success `RiskBadge`, and Access and Connectivity's "allow" render neutral there. Only the retiring `bench` themes still show green.
- **Inspectors:** every lens inspector uses `InspectorLayout` (slice 4), and `PinnedGone` covers the pinned lenses (slice 9c-1). Connectivity, the CRD browser, Built-ins, Hubble and the Overview have no inspector.
- **The Overview** (`cluster/Overview.tsx`), in render order:
  1. the header (state word, version, tags; `UpdatedAt`, Refresh);
  2. `ConnectionNotice` in the notice slot;
  3. `AttentionSection`;
  4. two columns: "Cluster" facts and "Resources" (usage with sparklines, `MonitoringLine`);
  5. `CriticalEvents`;
  6. `ClusterRisks`.

  It has no station verdict and no "Needs you" rows.
- **"Needs you"** (`fleet/NeedsYou.tsx` over `fleet/queue.fleetQueue`) is fleet-wide with session filters. `fleetQueue([c], {c: model})` already yields one cluster's queue; nothing calls it that way.
- **Station words:** `fleet/stationReading.ts` (the health reading), and `comparisonText` and `timeText` in `fleet/LineBoard.tsx` (the line state, held and failing times). `line/stationStrip.markOf` gives the station's mark.
- **Signal Bench remnants:**
  - `--bench-*` and `--lamp-off` are read by `layout/layout.css` (lens body and inspector wells, row cursor and selection, `.l-btn` edges, the pressed `.l-chip` well, module shadow) and by `chrome/primitives/styles.ts` (`well`, `btn`).
  - They are also read by CommandPalette, LocationBar, OperationDialog, ResourceInspector, NodesView, ConnectivityView and WorkloadsView.
  - In `night` and `day` they are flattened (sunk, hair ring, no shadow, no glow). The `bench` themes stay selectable until one release after the migration (§14.1).

## 1. Sub-slices

| | Builds |
|---|---|
| **11a** | Rows: 40px, one row mark (`RowAspect`) in every table's first column, the dashed mark for not-yet-realized rows, Events and Upgrade and InstanceList onto `Aspect` |
| **11b** | Headers: one `CountSentence` for every lens summary, in each tool's vocabulary, with `Aspect` replacing the header `▲` |
| **11c** | The station page: the Overview's body becomes the station's verdict, its "Needs you" rows, then the Overview's sections in the parent's order |
| **11d** | The bench remnants retired from components; headless captures of every lens in both themes; the parity gate re-run |

Each is Codex-reviewed on its own, in this order.

## 2. 11a: rows

- **Height:** `TABLE_ROW_HEIGHT` becomes 40, and so does the CSS default; `PodsView` reads the constant. Cell text stays 13px, centred vertically. Inner padding is unchanged, so a 40px row gains its height as air rather than larger text.
- **The mark column:**
  - Every table's first column is `{ id: "mark", width: 20, priority: 0 }`, replacing `lamp`. It draws `RowAspect`, and a header cell with no visible label.
  - **`RowAspect`** (`health/VerdictView.tsx`): `<span role="img" aria-label={label}>` around `Aspect` at size 10, with the verdict's label. It replaces every local `Lamp` and `RowLamp`, and InstanceList's dot.
  - **A healthy row draws nothing and is not announced:** `aria-hidden` on an empty mark. Its verdict word stays in its own column.
- **Precedence, one rule for rows:** fault, then attention, then checking, then syncing, then paused, else none. It is the verdict's mark, except that a not-yet-realized row (`data-stale`) whose verdict mark is `none` takes `syncing`, the dashed square. The dim and the "Syncing…" chip stay; they are the ratified staleness language.
- **Events:** a Warning event takes `attention` ("Warning"); a Normal event draws nothing. The Type and Reason text keep their caution colour.
- **Upgrade:**
  - an API removed at or before the target version takes `fault`, and one removed later or unscheduled takes `attention`;
  - a kubelet skew violation takes `fault`, and a skew warning takes `attention`.
- **`▲` in a status cell goes:** the mark column carries the shape. Danger dialogs keep their `▲` and hazard tape; they mark danger verbs, not health.
- **InstanceList:** its mark follows its rows' existing "needs attention" test, and draws `attention`.
- **Quiet healthy rows:** no code change in `night` and `day` (success is ink-2). Captures confirm it in 11d.

## 3. 11b: header count sentences

`CountSentence` (`layout/CountSentence.tsx`) renders one sentence from parts:

```
<total> <noun> · <mark> <n> <attention phrase> (<breakdown>) · <secondary facts> · <scope> · as of <time>
```

- **The total** is said with its noun, singular or plural: "214 pods", "1 Kustomization", "38 reconciliations". A lower bound keeps its existing marker ("214+ pods").
- **The attention part:**
  - It carries an `Aspect`: `fault` when any counted object is a fault, otherwise `attention`.
  - Its phrase is the lens's existing words, for example Flux "not ready", Argo "out of sync or unhealthy", ESO "not synced or stale", Helm "failed" and "pending, appear stuck", Karpenter "not ready" and "NodePools not ready", HTTPRoutes "with a failing attachment".
  - The phrase takes the danger or caution colour.
  - With no attention, the lens's existing all-clear words appear in ink-2, with no mark ("all ready", "all deployed", "none need attention"). When evidence is incomplete, its existing "not known" words appear instead.
- **The breakdown and secondary facts** are the lens's existing ones, in ink-2.
- **The `▲` glyph is retired from headers;** the `Aspect` carries the shape.
- **Lenses that are checks** (Upgrade, Access, Connectivity) and **browsers** (CRDs, Built-ins, Hubble) keep their sentences as they are, with `Aspect` replacing `▲`.
- No lens changes what it counts. 11b changes the wording and structure only, so every count test still holds against the same numbers.

## 4. 11c: the station page

The Overview destination becomes the station page.
- **Its header:** the title is the station (`clusterDisplayName`), and the summary is "<env> on <track> · <version> · <provider, region>" in ink-2. The protected tag stays with the location bar's station crumb.
- **Its body,** top to bottom:
  1. **The station's verdict** (`StationVerdict`):
     - **The headline** is `stationReading(c, m)`: the health reading in the fleet's words ("2 areas need attention · 1 unverified", "nothing raised", "health checking…", or the connection's words when the cluster is unreachable). It is led by the station's `Aspect`, which is `markOf` merged with the reading's tone the way `StationCell` merges them.
     - **The line clause** comes from `LineDTO`: "<env> on <track> · <comparison>", plus "held …" and "failing …" times. It uses `comparisonText` and `timeText`, which move from `LineBoard.tsx` to `line/station.ts` so the board and the page share them. A siding station says "siding", and a cluster the line does not place says "not on the line".
     - **No new claim:** every word already exists on the fleet board's station cell.
  2. **"Needs you" for this station:** `NeedsYou` gains a `station` mode.
     - It shows `fleetQueue([c], {c: model}, "all")`.
     - It has no filters, since All, Prd only and Faults are fleet questions, and no station tag, since every row is here. Each row shows its area.
     - Rows open exactly as on the fleet screen: `reveal` with the UID, or the lens for "+N more", controller and coverage rows.
     - A cluster with no reading says so (the queue's `notRead`), never "nothing needs you".
  3. **Resources:** utilisation with sparklines, then `MonitoringLine`, beside the cluster facts, as today's two columns.
  4. **Recent warning events** (`CriticalEvents`).
  5. **Resource risks** (`ClusterRisks`).
  6. **By area** (`AttentionSection`): the per-area lines with their lens links and "checking…" parts.
  7. **Connection notes** (`ConnectionNotice`), moved from the notice slot.
     - A failed, stale or connecting cluster still leads with its connection, in the verdict's headline.
     - The full notes sit here.

  The order of 3 to 7 is the parent's: utilisation and sparklines, monitoring status, `CriticalEvents`, `ClusterRisks`, attention areas, connection notes.
- **Nothing is fetched anew:** the verdict and queue read the attention model and `LineDTO` already in the stores; the sections keep their own reads.

## 5. 11d: retiring the bench, captures, parity

- **Components stop reading `--bench-*` and `--lamp-off`.** Each use moves to the `--line-*` token the night/day themes already resolve it to:
  - wells → `--line-sunk` with a `--line-hair` ring;
  - selection → `--line-route-wash` with a `--line-route` edge;
  - module shadow → none;
  - `--lamp-off` → `--line-track`.
- **The pressed-filter well on `.l-chip[aria-pressed="true"]`** becomes the route wash with a route border, the same as a selected row.
- **The `bench` themes** keep their colour tokens until they are removed one release after the migration (§14.1). They lose their machining, which components no longer read.
- **A source test** fails on any `--bench-` or `--lamp-` reference outside `theme/tokens.css`.
- **Captures:** headless at 1440×900, every lens in both themes, against the kind lab (`pw/s11.mjs`), plus the station page with and without faults.
- **The parity gate** (`src/nav/parityGate.test.tsx`) is re-run with its output kept, and any new destination, verb or key is added to its table.

## 6. Testing

- **11a:**
  - the row height reaches `--table-row-height` as 40px;
  - `RowAspect` labels, with healthy rows unannounced;
  - the precedence (a stale ok row is dashed; a stale fault stays a fault);
  - Events' Warning mark, Upgrade's marks and InstanceList's mark;
  - every lens's column set starts with `mark`.
- **11b:** `CountSentence` units (noun agreement, lower bound, fault versus attention mark, all-clear and not-known words), and each lens's header test updated to the sentence with unchanged numbers.
- **11c:**
  - the section order;
  - the verdict when connected, unreachable, checking, a siding and not on the line;
  - the station queue scoped to the cluster, with no filters and rows revealing with their UID;
  - `notRead` wording;
  - the board and the page sharing `comparisonText` and `timeText`.
- **11d:** the source test, the captures and the parity gate.

## 7. Open points (for review)

1. **The row mark is size 10** in 40px rows. The console and the fleet queue keep size 8.
2. **A healthy row's empty mark is `aria-hidden`.** The verdict word in its own column is what a screen reader hears.
3. **A stale ok row takes the dashed `syncing` mark,** alongside the dim and the "Syncing…" chip. A stale row with any other verdict keeps that verdict's mark.
4. **A Warning event takes the `attention` mark.** An event is evidence rather than an object's verdict, so it never takes `fault`.
5. **The station page's title is the station.** The destination keeps the name "Overview" (mod+1, the registry, the console, parity).
6. **The station's "Needs you" has no filters and no station tag.**
7. **The bench themes stay selectable but lose their machining** in this slice; their removal follows §14.1.
