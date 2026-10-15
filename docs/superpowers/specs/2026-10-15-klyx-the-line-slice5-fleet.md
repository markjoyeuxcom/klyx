# The Line, slice 5: the fleet screen

Design note for slice 5 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §3.1-3.7 (the line and the fleet screen), §8.1-8.2 (refs' `since`, `condition` and `reason`; proof wording), §10 (accessibility), §12 (the queue cases).
- **Mockup of record:** the Main screen on the "Klyx redesign" canvas. Where it disagrees with the parent, the parent wins (§8 below).
- **Scope:** frontend, plus one small `LineDTO` addition (§5.1). It reads `LineDTO` (slice 3, bridged in 4c), the health store and the fleet store.

## 0. What exists (surveyed 2026-10-15)

- **`FleetView`** is a `LensLayout`:
  - a header of counts (connected, unreachable, stale) and `fleetMeters` (attention, partly unverified);
  - `MeshStrip`, from `MeshService.GetMeshGraph()`, fetched on mount;
  - a grid of `ClusterCard`s, sorted by `clusterDiagnosticScore` (unreachable, danger, warning, connection, gaps, then quiet);
  - the footer "N clusters · attention first".
- **`ClusterCard`** shows a lamp (`spineLamp`), nodes and pods, an areas badge, and signal rows (workloads, GitOps, gateway, other lit areas, unverified, mesh, connection). It never renders `Top` refs. The parity gate's card stage renders it.
- **Summary `Top` refs** (`HealthRef`) carry `since?`, `condition?` and `reason?`. No frontend code reads them. `Top` holds at most five refs per part.
  - Verdict parts (workloads, pods, nodes) set none of the three.
  - Native parts set them where a rule proves them. Flux and Argo leave `since` empty and carry `condition` and `reason`.
  - `Overview` is the only consumer of refs: three per line, with no "+N more".
- **`LineDTO`** carries the stations and the siding (state, flags, held, `failingSince` and `heldSince` proofs, read, `readAt`, `asOf`, units), the tracks, the keys and the chosen key with `keyHow`, R with its station count and tie-break, the headline counts, `fallback`, and `mesh` (`drawn`, `card`, `offFleet`, `unknown`). Nothing renders `mesh` yet.
- **`FleetExpiry`** pulls `ExpiryService.GetExpiryScan` per cluster on mount (`useFleetBatch`). It filters to 30, 60, 90 days or all on the client.
- **`FleetPromotion`** pulls a per-unit × cluster revision matrix on mount.
- **Not here yet:** `LinePos` (parent §5.4) and folding along reported-blocker edges (parent §5.1, the dependency graph) are slice 8.

## 1. Sub-slices

| | Builds | Retires |
|---|---|---|
| **5a** | The verdict sentence and legend (§2), the board (§3) | `ClusterCard` and its grid, `MeshStrip` and its fetch, the header meters |
| **5b** | "Needs you" (§4) | - |
| **5c** | The aside: Ledger (§5) and Horizon (§6); the footer (§7); the `LineDTO` revision times (§5.1) | - |

The fleet views (promotion tracker, expiry horizon) stay, listed by the lens index on fleet routes.

## 2. The verdict sentence and legend (5a)

A pure `line/verdict.ts` derives it from `LineDTO`, in the parent's §3.3 words.

- **The key line:** "Comparing github.com/acme/fleet @ branch main · on 6 of 7 stations · chosen by most stations". `keyHow` gives "chosen by most stations", "set in fleet.yaml" or "chosen in this session". The key's own label is used as the backend writes it, including the Flux chart-name disclosure.
- **The headline:**
  - with R: "a1b9e3c is applied on 5 of 6 compared stations · 1 not compared". It counts only proven matches (`applied`) over `compared`, and names the rest (`notCompared`, `checking`);
  - a tie broken by fetch time adds "tie broken by fetch time, not commit order";
  - a held station adds one clause each: "prd · North Europe is held: attempted a1b9e3c, failing" (`heldWhy`), with its `heldSince` wording (§3.3);
  - no R: "No comparison revision: no station has a single applied revision";
  - no key: "Nothing is compared: no source is shared by two stations".
- **"As of":** when any station's read is not ok, "as of <oldest readAt>" follows the headline.
- **Never claimed:** a cause ("traces to one cause"), a delivery path, or that the line is live.
- **The legend:** "Lit: matches the comparison revision. A comparison, not a delivery path." Then the marks: fault, look at, checking, syncing, and mesh (solid mutual, dashed one-way).

## 3. The board (5a)

### 3.1 Layout

- **A grid of tracks × ranks**, from `LineDTO.tracks` and each station's `rank`. A track is labelled as the backend writes it ("West Europe", or "group: core"). A cell holds its stations side by side.
- **Segments** between adjacent ranks on a track are lit (route colour) only when the stations at both ends match R (parent §3.3).
- **The siding** sits below a hairline, labelled "Siding · no environment".
- **The fallback** (fewer than two ranks): one row, no lighting. Aspects and mesh still show.
- **The well:** the board sits in a sunk well and scrolls horizontally when narrow; the page never does.

### 3.2 A station cell

Top to bottom:
1. **The label:** "PRD" in the label face, and the "protected" tag when the cluster is.
2. **The cluster name,** in mono.
3. **The comparison:** the single applied revision (short), or the state word ("mixed", "applied unknown", "not compared", "checking"), with its `Aspect` from the station's marks (failing, held, checking, syncing).
4. **The cluster's reading,** from its attention model (the fleet indicator's model, §3 of slice 4): "2 areas need attention", "1 to look at", "health partly unverified", or "nothing raised". It counts areas, never objects.
5. **The time,** when the station is held or failing: the proof wording of §3.3.

- **An unreachable cluster** is a dashed cell with its connection text ("failed: dial tcp …"). Its last line facts are not drawn as current.
- **The whole cell is a link** to the cluster, named "aks-prd-neu, prd on West Europe, differs at 9f2c11d, failing since 20:21 (condition time); 2 areas need attention".

### 3.3 Proof wording

A pure `line/proof.ts` words a `LineProofDTO`, after parent §8.2:

| Basis | Words |
|---|---|
| `rule` | "since 20:21" |
| `observed` | "between 20:19 and 20:21 (observed)" |
| `sampled` | "between 20:19 and 20:21 (between two reads)" |
| `derived` | "start unknown" |
| `unknown` | "first observed 20:21; start unknown"; after a gap, "observed 20:21, after a gap" |

- **A capped proof** (basis unknown, `capped`, no times) reads "start unknown (proof capped)". The proof store held no lower bound for it, and a proof cap is not the entry log's trimming, so nothing reads "at least since".
- A proof the DTO marks suspended (basis unknown) never shows a time as a start.

### 3.4 Mesh

From `LineDTO.mesh`, which replaces `MeshStrip` and its fetch:
- **`drawn`:** same rank, adjacent tracks. A mutual peering is a solid link; a one-way one is dashed in caution, with "one-way" and the configuring side.
- **`card`:** every other pair, listed on its station cell ("mesh: aks-prd-weu").
- **`offFleet`:** listed under the board.
- **`unknown`:** "mesh unverified for aks-stg-weu", under the board. `error` is shown there too.

## 4. "Needs you" (5b)

### 4.1 Rows

A pure `fleet/queue.ts` builds the rows from each cluster's attention model (`clusterAttention`), so the queue, the lens index and the fleet indicator never disagree:
- **Object rows:** every attention line's `top` refs. Only fault and attention roles are rows; a paused or progressing ref never is.
- **"+N more":** when a line's count exceeds its refs, the line ends in "+N more in <lens>", linked to the lens. A "lower" bound reads "+N or more".
- **Controller rows:** an area whose controller is not ready is a row of its own ("kustomize-controller not ready: <reason>"), with no object.
- **Coverage rows:** an unreadable or checking line is a row of its own ("Pods could not be read (forbidden)"), never silently dropped.
- **Clusters with no reading:** a connecting cluster, or one whose observation ended, adds one quiet line under the queue: "Not read yet: aks-stg-weu (connecting)". The queue never reads as an all-clear for a cluster it has not read.

### 4.2 A row

- its `Aspect` (fault or look at);
- its kind (from the ref's GVR) and name;
- the verdict label in the tool's own word (`label`);
- the station, as "prd · West Europe" text (`LinePos` arrives in slice 8);
- its area;
- **its time:**
  - `since` present: "since 09:00" (rule-backed);
  - else `condition` present: "Ready=False since 09:00 (condition time)", evidence, never the role's start;
  - else nothing (§8, open point 1);
- `reason`, when present, on a second line, capped by the backend.

A row opens its object through `reveal(cluster, task, part, ref)`, pinned to its own cluster and UID. Same-name objects on two clusters are two rows.

### 4.3 Order and filters

- **Order:** environment rank, prd first; then role (fault before attention); then area in task order; then the ref's order.
- **Filters:** All, Prd only (the stations of rank prd), Faults. The choice lasts the session.
- **No folding** in slice 5 (§8, open point 2).

## 5. The Ledger (5c)

Without Git history it is **"Revisions on the line now"**, for the chosen key only:
- each distinct single applied revision among the stations, newest R first, then the others by station count;
- per revision, one row of dots per track: filled for a station whose single applied revision it is, hollow otherwise;
- "R" marks the comparison revision.

### 5.1 Revision times (a `LineDTO` addition)

The parent shows each revision's artifact time. The backend holds one, but it is the **source's current** artifact time, which can belong to a newer revision than the one applied. So:
- `LineUnitDTO` gains `comparedAt`: the source artifact's `lastUpdateTime`, which dates the unit's **compared** revision only;
- the Ledger shows "fetched 20:31" beside a revision only when some unit's compared revision is that revision. An applied revision the sources have moved past shows no time.

## 6. The Horizon (5c)

The next 30 days, from `ExpiryService`:
- **Acquisition:** one scan per cluster when the fleet screen first opens in a session, kept in the store with its "as of" time and a Refresh. Later visits reuse it; the Expiry view shares the same results. It is never re-run on every visit.
- **Membership changes:** a cluster that joins the fleet mid-session (Settings adds clusters live) is scanned once when it joins, while the fleet screen or the Expiry view holds the cache; existing clusters are never rescanned for it. Until its scan returns it reads "aks-new: not scanned yet". A cluster that leaves the fleet drops its rows.
- **A row:** days left ("6d", or "expired 2d ago"), the source kind, the object, and the station ("stg · West Europe"). Rows inside 14 days carry the attention mark.
- **A failed scan** names its cluster ("aks-prd-neu: could not be scanned"). It is never an empty horizon.

## 7. The footer (5c)

Connection counts only: "7 clusters · 5 connected · 1 failed · 1 connecting". It never says the line, or the fleet, is live.

## 8. Open points (ruled in design review d25f7275)

All eight are accepted. Point 4 carries the condition written into it, and review findings changed §3.3 (a capped proof) and §6 (membership changes).


1. **No per-object observation wording yet.** The parent wants "first observed …" for refs without a rule-backed `since`. The observation log's per-object proofs do not reach the frontend; only stations' proofs do. Slice 5 shows `since`, else `condition` evidence, else no time. Proposal: per-object proof wording arrives with the console join (slice 9), which needs the same data.
2. **No folding.** Rows fold only along reported-blocker edges, which come with the dependency graph (slice 8).
3. **Station text, not `LinePos`,** in rows until slice 8.
4. **`comparedAt` (§5.1)** is a small Go addition in 5c, so the Ledger never dates a revision by a newer artifact. It is emitted only on a unit whose compared revision is exactly the revision being dated, never applied to an older applied revision.
5. **The Horizon's acquisition (§6):** once per session, not per visit, so the fleet screen adds no per-visit fan-out.
6. **"Prd only"** means the stations of rank prd, not every protected cluster.
7. **`ClusterCard` retires.** The station cell keeps its honest readings (areas, connection, gaps). The parity gate's card stage moves to the cell, with the same expectations.
8. **The mockup is not followed** where the parent forbids it: no "traces to one cause" in the headline (parent §5.1) and no "watched live" in the footer (parent §3.6).

## 9. Tests

| Sub-slice | Tests |
|---|---|
| 5a | The verdict sentence: R, a tie, held (one and two), no R, no key, the chart-name key, "as of". The board: placement by rank and track, a group track, the siding, the fallback, lighting at both ends only, an unreachable cell, the proof wording table (a capped proof included), mesh drawn/listed/off-fleet/unknown. The cell's accessible name. The parity gate's cell stage. |
| 5b | Parent §12: more than five faults in a part ("+N more" and the samples), a paused native ref (not a row), a controller-only failure, an unreadable kind (a coverage row), same-name objects on two clusters (two rows, each revealing its own). The time wording (since, condition, none). Order and filters. The "not read yet" line. `noFanout`. |
| 5c | The Ledger's dots and R mark; `comparedAt` shown only for a compared revision (Go and TS). The Horizon: 30-day window, expired rows, a failed scan, one scan per session, a cluster joining mid-session scanned once without rescanning the rest, a leaving cluster's rows dropped. The footer counts. |

**Every sub-slice also passes** tsc, eslint, vitest, vite build, and headless captures of the fleet screen in night and day at 1440×900 and 1024×768.
