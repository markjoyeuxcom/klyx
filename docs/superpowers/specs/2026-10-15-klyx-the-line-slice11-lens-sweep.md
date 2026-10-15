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
| **11a** | Rows: 40px, one row mark (`VerdictMark`) in every verdict table's first column, the dashed mark for not-yet-realized rows, Upgrade and InstanceList onto `Aspect`, `▲` off status words |
| **11b** | Headers: one `CountSentence` for every lens summary, in each tool's vocabulary, with `Aspect` replacing the header `▲` |
| **11c** | The station page: the Overview's body becomes the station's verdict, its "Needs you" rows, then the Overview's sections in the parent's order |
| **11d** | The bench remnants retired from components; headless captures of every lens in both themes; the parity gate re-run |

Each is Codex-reviewed on its own, in this order.

## 2. 11a: rows

- **Height:** `TABLE_ROW_HEIGHT` becomes 40, and so does the CSS default. `PodsView` reads the constant, and its CSS grid widens its mark column to 20px, with the breakpoints recomputed. Cell text stays 12-13px, centred vertically: a 40px row gains its height as air, not as larger text.
- **The mark column:**
  - Every verdict table's first column is `ROW_MARK` (`{ id: "mark", width: 20, priority: 0 }`), replacing `lamp`. Its header cell has no visible label.
  - **`VerdictMark`** (`health/VerdictView.tsx`) draws the column's cell: `<span class="row-mark" role="img" aria-label>` around the verdict's `Aspect` at size 10.
  - It replaces every local `Lamp` and `RowLamp`, and InstanceList's dot. Inspector lists and the topology's nodes use it at size 8.
  - **A healthy row's empty mark is `aria-hidden`:** it is not announced. Its verdict word stays in its own column.
- **Evidence and simulations take no mark.** Events (the Events lens and Karpenter's events) are evidence, not an object's verdict; this keeps the step-5 ruling "warnings are evidence: amber text, no lamp". Access grants are a simulation's answer.
- **Precedence, one rule for rows:** a row's mark is its verdict's (fault, attention, checking, syncing, paused, or none). A not-yet-realized row (`data-stale`) whose verdict mark would be none takes `syncing`, the dashed square, named "Syncing: changes not yet realized".
  - **The 0.45 dim on a stale row's mark is retired:** a mark carries meaning, so it keeps its 3:1 stroke contrast (parent §2.1).
  - The "Syncing…" chip stays.
- **Upgrade:**
  - an API removed at or before the target takes `fault`, and one removed later or unscheduled takes `attention`;
  - a kubelet skew violation takes `fault`, and a skew warning takes `attention`.
- **InstanceList:** its mark follows its rows' existing "needs attention" test, drawn as `attention` and named with the reason.
- **`▲` comes off status words** wherever an `Aspect` is drawn beside them:
  - the row status cells of Certs, ESO, Gateways, HTTPRoutes, Flux, Helm, Karpenter, Argo and Upgrade, and the topology's nodes;
  - `VerdictLabel`, the row status word of Pods, Workloads and Nodes;
  - the inspector verdict chip (`VerdictStatus`), which draws its `Aspect`;
  - Flux's `klyx-danger-prefix` in its row.

  Danger dialogs keep their `▲` and hazard tape: they mark danger verbs, not health. Headers lose theirs in 11b.
- **Quiet healthy rows:** no code change in `night` and `day` (success is ink-2). Captures confirm it.

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
  1. **The station's verdict** (`StationVerdict`): two lines, each with its own mark.
     - **The health line** (the headline) is `stationReading(c, m)`: the health reading in the fleet's words ("2 areas need attention · 1 unverified", "nothing raised", "health checking…", or the connection's words when the cluster is unreachable).
       - Its mark comes from the reading alone: fault for a danger tone, attention for a warning tone, checking when it cannot be read or is unverified, none otherwise.
       - It never inherits the line's failure.
     - **The line line** comes from `LineDTO`, fenced to the line's Git ordering as the board is (`useLineGitFor`).
       - It reads "<env> on <track> · <comparison>", plus "held …" and "failing …" times, with `markOf`'s mark and the board's held-behind rule.
       - It keeps the board's guards: an unreachable cluster shows its place but no comparison, times or mark.
       - Before a first `LineDTO` it says "the line is being read…", never "not on the line". A siding station says so, and only a cluster absent from a `LineDTO` says "not on the line".
       - `comparisonText` and `timeText` move from `LineBoard.tsx` to `line/station.ts`, so the board and the page share them.
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
  - `VerdictMark` units: labels, healthy rows unannounced, a stale ok row dashed, a stale fault kept, the label override;
  - source rules: rows are 40px in the constant and the CSS, with no list passing its own height; a stale mark is never dimmed; every verdict table starts with `ROW_MARK` (evidence and simulations exempt by name); no status cell repeats `▲`;
  - Upgrade's marks and InstanceList's mark;
  - the lens tests updated from `▲` text to the row's mark.
- **11b:** `CountSentence` units (noun agreement, lower bound, fault versus attention mark, all-clear and not-known words), and each lens's header test updated to the sentence with unchanged numbers.
- **11c:**
  - the section order;
  - the verdict when connected, unreachable, checking, a siding and not on the line;
  - the station queue scoped to the cluster, with no filters and rows revealing with their UID;
  - `notRead` wording;
  - the board and the page sharing `comparisonText` and `timeText`.
- **11d:** the source test, the captures and the parity gate.

## 7. Rulings (design review of 558e190)

Codex accepted the design, with these rulings:
1. **Size 10** in 40px verdict-table rows; size 8 on the other named surfaces.
2. **A healthy row's empty mark is `aria-hidden`,** and its readable status word stays.
3. **A stale ok row is dashed `syncing`;** other verdict marks keep full stroke contrast. Generation lag stays distinct from a failed or stale read.
4. **Events and Access grants are unmarked:** evidence and a simulation's answers do not become object-health verdicts.
5. **The page title is the station's name.** Overview stays the destination, with its keyboard and parity identity.
6. **The station's "Needs you" has no fleet filters and no station tag,** with the existing UID-pinned opening and not-read wording.
7. **The bench themes stay selectable but lose their machining,** keeping usable colour fallbacks until their removal one release later.

For 11c, the verdict is two independently marked lines, health and line (§4). The health line never inherits `markOf`'s line failure, and the board's unreachable and read guards and the `LineDTO`/`LineGit` version fence hold.

Codex accepted 11a at e7af6d0. Its P3 (the `InspectorStatus` danger prefix beside its `Aspect`) is fixed in 11d part 1, and so is the staleness comment that still described the retired dim.

## 8. As built: 11a and 11d part 1

- **11a** (accepted at e7af6d0) follows §2.
- **11d part 1** (2aeeae5) retires the machining. Each component use now reads its `--line-*` token with the classic fallback it had:
  - wells: `--line-sunk` and `--line-well-ring`;
  - selection: `--line-route-wash` and `--line-route`;
  - modules: `--line-module-ring`;
  - an idle mark: `--line-track`.

  Night and day define `--line-well-ring` and `--line-module-ring` in place of the flattened bench extras. The idle marker's literal inset shadow, the last machining a token test cannot see, goes too. Bevels and glows are gone, and a pressed filter chip reads as a selection. `InspectorStatus` drops the danger prefix beside its `Aspect`, and the `.klyx-danger-prefix` rule goes with its last user. A source test fails on any component reading `--bench-` or `--lamp-`.

## 9. As built: 11c

- **`cluster/StationVerdict.tsx`** follows §4's two lines. A siding station is read like one on a track (its mark, revision, held and failing times, and Git ordering); only its place says "<env> on the siding" (review of c9740dc).
  - **The health line:** an `h2` with the reading's `Aspect` at size 12 and `data-tone`.
  - **The line line:** `markOf`'s `Aspect` at size 10 and the board's words, from `line/station.ts`: `comparisonText`, `timeText` and `stationTimes`, which the board's `StationCell` now shares.
- **`NeedsYou station`:**
  - one cluster, `filter` fixed to all;
  - no filter group and no station tag;
  - "Nothing on this station needs attention, in what was read.";
  - an unread station's one line, "Not read yet: <why>. Nothing is claimed about what needs you here.", which stands alone.
- **`Overview`** is the station page:
  - **The title** is the station's display name; **the summary** is the version and the provider, region and group. The station's place on the line is the verdict's second line, not repeated in the header.
  - **The body:** the verdict, the station's queue, Resources beside the cluster facts, recent warning events, resource risks, "By area", and Connection last, holding the connection notice that was in the notice slot.
- **Tests:**
  - `StationVerdict.test.tsx`: the board's words with separate marks; health never inheriting the line's failure; a fault reading; before a first `LineDTO`; a siding; not on the line; the unreachable guard; the station queue's shape and UID-pinned opening; an unread station claiming nothing. Mutation-checked.
  - `Overview.test.tsx`: the title, the section order, and a failed cluster leading with its connection with its notes last.
- **Captures:** `pw/s11c.mjs`, `<scratchpad>/cap11c/{night,day}-1440-station{,-lower}.png`, against the kind lab.
