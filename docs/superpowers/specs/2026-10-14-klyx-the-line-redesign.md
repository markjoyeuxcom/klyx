# Klyx redesign: The Line

**Owner decisions (2026-10-14):**
- **The request:** the owner asked for a free-pass UI/UX redesign, reviewed five mockups, called them "completely awesome", and asked for a spec.
- **The mockups of record** are on the "Klyx redesign" canvas: claude.ai artifact `WpwuEtxvLH547pikiSa8CS`, private to the owner. They are illustrations with sample data. Where a mockup shows something this spec does not back with evidence, the spec wins.
- **Fully featured (owner, after the review):** "provide the data in the app that's not provided, make the app fully featured".
  - Where a mockup shows something Klyx does not read today, Klyx gains the data (§8), rather than the feature being dropped.
  - Every claim still needs evidence. A time-based claim names its source.
  - The five screens:
    1. Fleet · The Line.
    2. Cluster · Flux diagnosis.
    3. Network · data path.
    4. Command console.
    5. Ask, in day mode.
  - What the spec changes in them:
    - **"Root cause"** becomes "reported chain ends here", with its evidence listed. Causation is never claimed.
    - **"A credential, not a manifest"** becomes the typed supply fact plus change proximity (§5.1, §8.3).
    - **"held 18m"** becomes "held since <time> (observed)" (§8.2).
    - **"503 on /api", its sparkline, onset and retries** are measured per rule (§8.5).
- **What this replaces:** Signal Bench's visual language (direction C, 2026-07-05) and the chrome of the 2026-10-10 workspace redesign.
- **What it keeps:** the engineering principles, the health 1c model (one Observer per cluster, one cluster summary, the verdict engine), every honesty rule, and every write-path guard.
- **Git access was folded in after the first draft.** Klyx gains read-only Git history through the system `git` (§7).

**The idea.**
- **The board:** the fleet is drawn as a railway signal box.
  - Regions (or groups) are parallel tracks.
  - Environments are stations along them: dev, then test, then stg, then prd.
  - ClusterMesh peerings are links between the tracks.
- **What lights the board:** stations that match a comparison revision. Only with Git history does it show order: behind, ahead or held.
- **The track motif** is reused for every causal or positional relation:
  - the GitOps dependency graph;
  - the Gateway data path;
  - the chat read ledger;
  - the position glyph.
- **A healthy object carries no mark.**

Revised after Codex's design reviews of 4c654fc (nine findings), 3fdd4f7 (ten), fe9ced4 (nine), c1268b1 (four) and 9d24e15 (two), all folded into §§0-14.

## 0. What exists

- **Theme:**
  - Semantic `--color-*` tokens across 11 themes.
  - `bench` and `bench-light` are the defaults, with a one-time migration, plus bench-only `--bench-*` and `--lamp-*` tokens.
  - System font stacks.
  - `contrast.test.ts`.
- **Chrome:**
  - `AppShell` composes `TopBar`, `FleetSpine` (the only place anything blinks), `NavBand`, the content and `ChatDrawer`.
  - `TopBar` holds:
    - history and recent places;
    - the attention button;
    - the protected cue;
    - Activity;
    - Terminal;
    - the port-forward indicator with stop-all;
    - the theme toggle;
    - Settings.
- **`FleetSpine`** (`nav/fleetAttention.ts`) gives each cluster its own lamp. It distinguishes:
  - fault;
  - warning;
  - unreachable;
  - connection stale or degraded;
  - unverified.
- **Dock badge and notifications** are separate from the spine:
  - The dock badge counts Failed, Degraded and Unconnected *connections*.
  - OS notifications report action errors only, and only while the app is unfocused. Neither reports Observer health transitions.
- **The open cluster's Overview** shows:
  - utilisation and sparklines;
  - monitoring status;
  - `CriticalEvents`;
  - `ClusterRisks`;
  - the attention areas;
  - connection notes.
- **Navigation:**
  - `nav/registry.ts` has two kinds of destination: task lenses and generic-resource lenses.
  - Capability states are present, absent, checking and unknown.
- **Summaries (health 1c):**
  - Each area part keeps at most **five** `Top` refs (`internal/healthsrc/core.go`).
  - Native `Top` is picked by `Included`, so it can hold progressing or paused objects. It is not a fault list.
  - An `observe.Ref` has an identity, a label, a role and a parent. It has no failure age, no full reason and no dependency edges.
- **Promotion** (`internal/fleet/promotion.go`):
  - An on-demand dynamic-list snapshot, with no read time and no incarnation in its DTO.
  - `PromotionUnit.Revision` is the applied revision, but for a HelmRelease it falls back to the chart version (HelmRelease v2 has no `lastAppliedRevision`).
  - `PromotionSource.Revision` is a source's fetched head.
  - The Argo parser keeps only the first source/revision pair.
- **The Flux detail DTO** has `source`, `dependsOn`, `appliedRevision`, `attemptedRevision` and `inventory`.
  - The GitOps view says "blocked by" only when the selected unit's reason is `DependencyNotReady`.
- **Gateway:**
  - Backend verdicts are per HTTPRoute rule.
  - `routemetrics` returns current route-level RPS, p50, p99 and a **route-level 5xx** error rate. It has no per-rule or per-status series and no history.
- **`MeshGraphDTO`:** edges are mutual or one-way (from the configuring side), plus off-fleet peers.
- **The palette** knows objects only in the open cluster's loaded slices.
- **No Git access.**
  - Klyx sees only the revision strings Flux and Argo report.
  - `internal/clibin` resolves the external CLIs.
  - `internal/fluxcli` passes `--path` to `flux diff` as `spec.path`, because there is no local clone (the open M10-f follow-up).
- **Shared panel anatomy:** `LensTable`, `InspectorStatus`, `InspectorConditions`, `InspectorEvents` and `useResizableDock`.

## 1. Principles

1. **One sentence leads every screen**, then a drawing, then the evidence. The sentence states only what the evidence supports.
2. **Healthy carries no mark.** The status marks differ by shape as well as colour:

| Mark | Shape | Meaning |
|---|---|---|
| **fault** | filled red square | something is failing |
| **look at** | hollow amber square | something needs a look |
| **checking** | dashed grey hollow square | unread or unknown; the text says "checking…" |
| **not yet realized** | dashed hollow square in tertiary ink | `generation > observedGeneration`, or a revision attempted but not yet applied |

3. **One accent.** Route blue means matched, selected, focused or primary action, never status. Success colour is retired for health (§2.1) but kept for edit evidence (§2.1, diffs).
4. **Danger keeps its shape.** ▲ and the typed-confirmation interlock stay unchanged.
5. **Nothing blinks.** Only overlays animate: opacity plus an 8px translate, at most 200ms, eased out. Lists, rows, tabs and results never animate. Reduced motion means opacity only.
6. **A line drawn on screen states a typed relation:**
   - a comparison match on the board;
   - a declared or controller-reported dependency in the graph;
   - configured routing on the data path;
   - read order in the ledger.

   The legend always says which.
7. **Honesty rules carry over.**
   - A failed read is an error.
   - An unknown is "checking…".
   - Counts are exact, or labelled as a lower bound or a sample.
   - Revisions are opaque unless an allowed repo's fetched history orders them (§7).
   - A comparison is never shown as a delivery path.

**What this supersedes in the Signal Bench rules:**

| Signal Bench rule | Becomes |
|---|---|
| "Only faults glow" | "Only faults and matched or selected items carry colour; nothing glows." |
| "Blink is spine-only" | "Nothing blinks." |
| "Danger gets shape" | Stays. |
| "Wells hold data, modules hold chrome" | Three flat surfaces (ground, raised, sunk), each with a hairline border. |

## 2. Visual system

### 2.1 Themes and tokens

**`night` (dark) and `day` (light) become the defaults.** They define the existing semantic tokens, so every lens restyles without per-component work, plus `--line-*` tokens for the new primitives.

| Token | night | day | Role |
|---|---|---|---|
| `--line-ground` | `#0E1316` | `#F1F2EE` | app ground |
| `--line-raised` | `#141A1E` | `#FAFAF7` | panels, inspector, drawer, station cards |
| `--line-sunk` | `#0A0E10` | `#E6E8E2` | list wells, inputs, the board |
| `--line-hair` | `rgba(203,216,222,.08)` | `rgba(20,30,36,.10)` | default border |
| `--line-hair-2` | `rgba(203,216,222,.15)` | `rgba(20,30,36,.18)` | emphasised border |
| `--line-control` | `#66747B` | `#737E79` | control outline (solid, ≥ 3.4:1 on every surface) |
| `--line-ink-1` | `#E3E8E9` | `#131B20` | primary text |
| `--line-ink-2` | `#A6B1B6` | `#44515A` | secondary text |
| `--line-ink-3` | `#7F8C93` | `#56636B` | tertiary text, meta |
| `--line-ink-4` | `#56636A` | `#98A1A6` | disabled only |
| `--line-track` | `#606E75` | `#737E79` | unmatched or idle track (≥ 3:1) |
| `--line-route` | `#79C2E4` | `#1A6190` | matched, selected, focus, primary action |
| `--line-route-ink` | `#0B2533` | `#FFFFFF` | text on a route fill |
| `--line-route-wash` | `rgba(121,194,228,.10)` | `rgba(26,97,144,.08)` | selected row |
| `--line-danger` | `#F0565B` | `#B42A30` | fault |
| `--line-caution` | `#E9A23B` | `#8A5100` | look at |
| `--line-danger-line`, `-wash` | danger at .55 / .10 alpha | danger at .45 / .07 alpha | decorative fault tint only |
| `--line-caution-line`, `-wash` | caution at .50 / .10 alpha | caution at .40 / .08 alpha | decorative look-at tint only |
| `--line-diff-add`, `-add-bg` | `#7FCB9B`, `rgba(127,203,155,.10)` | `#1F6B3A`, `rgba(31,107,58,.08)` | added line |
| `--line-diff-del`, `-del-bg` | `#F08A8D`, `rgba(240,138,141,.10)` | `#9E2B30`, `rgba(158,43,48,.07)` | removed line |
| `--line-lift` | 1px ring plus `0 24px 60px` black at .5 | 1px ring plus two soft layers | overlays only |

**Contrast** (`contrast.test.ts`, both themes; measured on the values above):
- **Text at 4.5:1 or better on ground, raised and sunk:** ink-1 to ink-3, danger, caution, route, and the diff foregrounds.
- **Route-ink on route: 4.5:1 or better.**
- **Meaningful strokes at 3:1 or better on the surfaces they are drawn on**, measured on the composited colour. They are track, route, control, and the solid `--line-danger` and `--line-caution`.
  - **A stroke that carries meaning is always solid:** a fault outline, a look-at outline, a control boundary, or a track.
  - The alpha `-line` and `-wash` tokens are decoration. They always sit beside an `Aspect` and text that carry the meaning.
- **Exempt:** ink-4 (disabled only) and the hairlines (decoration).

**Semantic mapping:**
- **Background:** `tertiary` = ground, `primary` = raised, `secondary` = one step above raised.
- **Text:**
  - `primary`, `secondary` and `tertiary` = ink-1, ink-2 and ink-3.
  - `info` = route.
  - `danger` = danger.
  - `warning` = caution.
  - **`success` = ink-2 with a transparent fill, for health and status only.** Words like Ready and Synced remain, so the meaning survives.
- **Edit evidence keeps its own colours.**
  - Diff additions and removals (the YAML diff in `InstanceDetail`, `flux diff`, Git diffs) move to `--line-diff-*`, with the `+` and `−` prefixes kept.
  - No component may use the success token for diffs once the mapping lands; slice 1 moves them.
- **Bench extras:**
  - `--bench-well-bg` = sunk, with a 1px hair ring.
  - `--bench-module-shadow: none`.
  - `--lamp-off` = track.
  - The lamp glows are `none`.

**Migration** (once, under a new flag):
- `bench` becomes `night`, and `bench-light` becomes `day`.
- `system` follows the OS into night or day.
- Any other saved theme is kept (§14).

### 2.2 Type

| Use | Family | Weights |
|---|---|---|
| Interface text | **Barlow** | 400, 500, 600 |
| Station names, section labels, the wordmark | **Barlow Condensed** | 500, 600; uppercase, tracked 0.12-0.18em |
| Object names, namespaces in tables, revisions, numbers, keys, code | **DM Mono** | 400, 500; `tabular-nums` |

**Bundling:**
- All three are bundled through `@fontsource/barlow`, `@fontsource/barlow-condensed` and `@fontsource/dm-mono` (SIL OFL 1.1), as latin-subset woff2 only (about 160 KB).
- Fonts are never loaded from the network.
- Tokens: `--font-sans`, `--font-label` and `--font-mono`, each with a close system fallback.

**Scale** (ratio 1.2, base 14px):

| Size | Use |
|---|---|
| 11 | legend |
| 12 | meta |
| 13 | dense table |
| 14 | body |
| 15 | lead |
| 17 | drawer title |
| 19-20 | inspector verdict |
| 22 | lens title |
| 28-30 | screen verdict |

**Weights:** 600 for verdicts and headings, 500 for names and the active item, 400 for body text. Headings over 22px use -0.01em tracking and `text-wrap: balance`; body text uses `text-wrap: pretty`.

### 2.3 Space, radius, depth

- **Spacing:** a 4px base.
- **Rows:** 40px in tables.
- **Controls:** 36px visual height, with the hit area extended to at least 40px.
- **Padding:** 20-24px inside panels; 28-32px between sections.
- **Radius:** 4 (tags), 6 (controls), 8 (cards and stations), 10 (wells and panels), 12 (overlays).
- **Depth:** borders only. `--line-lift` is for overlays alone.

### 2.4 Primitives

- **`Aspect`** replaces `StatusDot`'s lamp physics.
  - Props: `kind: fault | attention | checking | syncing | none` and `size: 8 | 10 | 12`.
  - Role mapping from the verdict engine: fault → fault; degraded or attention → attention; checking or unknown → checking; not yet realized → syncing; ok or excluded → none.
  - It always pairs with text, or carries an `aria-label`.
- **`Rail`:** a vertical track of step markers. Each segment is typed, and its style follows its type (§5).
- **`LinePos`**, the position glyph (§5.4).

## 3. The line

### 3.1 Tracks and stations

- **A station is a cluster in `fleet.yaml`.**
  - Stations are ordered by environment rank: dev, then test/qa/uat, then stg, then prd. This is the `FleetSpine` ranking, moved into one shared `env` module.
  - Tracks come from `tags.region`. When that is unset, they come from `group`, and the track is then labelled as a group ("group: core"), never as a region. Otherwise there is a single track named "Fleet".
- **Placement:**
  - A cell (track × rank) shows its stations side by side.
  - A cluster with no recognised environment goes to the **siding** (off the line, below a hairline), labelled "no environment".
- **Fallback:** with fewer than two ranks in the fleet, the board is one row of stations with no comparison lighting. Aspects and mesh still show.
- **Width:** the board sits in a sunk well and scrolls horizontally when narrow.

### 3.2 What is compared

**Comparison units:**
- **Flux Kustomizations** whose `sourceRef` is a GitRepository.
- **Argo CD Applications:** each Git source is its own unit, whether the Application has one `spec.source` or several `spec.sources`.
- **Chart units:** Flux HelmReleases, and Argo Helm-chart sources. They form a second key type, never mixed with Git keys.
- **Not compared,** and listed per station with counts:
  - Kustomizations sourced from an OCIRepository or a Bucket (their digests are shown, unordered);
  - revisions Klyx cannot parse.

**Each unit carries three revision facts,** never merged (§8.1):

| Fact | Flux Kustomization | Argo source | Chart unit |
|---|---|---|---|
| **applied** (deployment evidence) | `lastAppliedRevision` | the latest `status.history[]` entry, paired with the source(s) recorded *in that entry* | Flux: the newest `status.history[]` entry whose status is `deployed`. Argo: the latest history entry's chart revision |
| **attempted** | `lastAttemptedRevision` | `status.operationState.syncResult.revision(s)` while an operation is running or has failed, with its phase | Flux: `history[0]` when its status is not `deployed`. Argo: the running operation |
| **compared** (desired, not deployed) | the source's fetched artifact | `status.sync.revision(s)`, paired with `status.sync.comparedTo.source(s)`, never with the current `spec.sources` order | Argo: `targetRevision`, which may be a range such as `1.*`; never treated as a version |

- **Pairing:** Argo revisions are always paired with the source definitions recorded in the same status block or history entry. A source reorder or edit since then is flagged "source changed since last deployment".
- **"Applied unknown":** a unit with no deployment record has an unknown applied revision, which is never assumed to be "never applied". The cases:
  - Argo `revisionHistoryLimit: 0`, or truncated history;
  - Helm with no deployed history entry;
  - Flux with an empty `lastAppliedRevision`.

  Such a unit counts toward no match.
- **Chart units** compare the chart version (`chartVersion` for Flux, the history entry's revision for Argo). A differing Flux `configDigest` adds "values differ".

**Revision identity:**
- Git units carry `{repo, ref, sha}`:
  - `repo` is the normalised URL (scheme, user, `.git` and host case removed), used for matching only (§7).
  - The sha is parsed from the `<ref>@sha1:<hex>`, `sha1:<hex>` and `sha256` forms.
- **The ref keeps its kind:**
  - **Flux:** branch, tag, semver or commit, from `spec.ref`.
  - **Argo:** `targetRevision` is "unqualified" unless it is fully qualified (`refs/heads/…`, `refs/tags/…`) or a sha. Argo can resolve a bare `main` to a tag of that name, so an unqualified Argo ref never shares a key with a Flux branch.
- Chart units carry `{chart source, chart name, version}`.

**The comparison key:**
- `(repo, ref kind, ref name)` for Git units.
- `(chart source, chart name)` for chart units.
- Units compare only within one key.

**The line source:**
- **Default:** the Git key held by the most stations, each station counted once. Ties go to the lexically first key.
- **Selector:** the board can switch to any other key, Git or chart, with each key's station count shown.
- **Always shown:** "Comparing github.com/acme/fleet @ branch main · on 6 of 7 stations · chosen by most stations".
- **Override:** `fleet.yaml` may set `line: { source: { url: …, ref: { kind: branch, name: main } } }`.
- **No shared key:** nothing is compared, and the board says so.

### 3.3 Station facts and states

**Facts per station, for the chosen key:**

| Fact | Meaning |
|---|---|
| **E** | the eligible units |
| **A** | the applied revisions of the units that have one |
| **U** | the units whose applied revision is unknown (no deployment record) |
| **failing** | an eligible unit or its bound source is Ready False or its sync failed (suspended units excluded) |
| **syncing** | `generation > observedGeneration`, or an attempted fact differs from applied with no failure |
| **suspended** | a unit is suspended |
| **read** | ok, partial, stale, error, or checking, per unit and for the station |

**State precedence.** The first row that applies wins.

| Order | State | When |
|---|---|---|
| 1 | **checking** | the station read is not ok, or any eligible unit is unread or stale. Last good facts show as "as of …". It can never match or be at target. |
| 2 | **not compared** | \|E\| = 0. The card lists what was excluded. |
| 3 | **applied unknown** | no eligible unit has a known applied revision |
| 4 | **mixed** | the known applied revisions differ, or some units are known and some unknown |
| 5 | **matches R / differs** | every eligible unit has a known applied revision, and all are equal; compared to R (below) |
| 6 | **(Git)** at target, behind, ahead, diverged, unrelated, not in history | §3.4, replacing step 5 when Git ordering applies to this station |

**The comparison revision R** (no Git ordering):
- R is the single revision of the most stations **in state 5**. Mixed, unknown, checking and not-compared stations never vote.
- Ties are broken by the newest source artifact time, disclosed: "tie broken by fetch time, not commit order".
- When no station has a single applied revision, there is no R. Nothing is lit, and the board says "no comparison revision".
- R is a comparison reference, never a rollout target.

**Flags,** independent of the state:
- "failing since <time>": the earliest failure time among E and its sources, using each rule's `since` semantics (§8.1).
- "compared X, applied Y": when a compared fact differs from applied, for example an Argo app that is OutOfSync with a desired revision.
- "syncing", "suspended", and "source changed since last deployment".

**"Held" requires evidence tied to R:**
- Without Git, a station is held only when an eligible unit's attempted revision equals R and that unit is failing: "attempted R, failing".
- A station that differs and has an unrelated failure is "differs · failing since …".
- **The time on a hold** comes from the observation log (§8.2), worded by its continuity:
  - "first observed held at <time>; start unknown" when Klyx first saw it on a baseline read;
  - "held since <time> (observed)" only when Klyx saw it go from not held to held with continuous coverage.

**The headline** counts only proven applied matches, and names its denominator: "a1b9e3c is applied on 5 of 6 compared stations · 1 not compared".

**Lighting:**
- A segment is lit when the stations at both its ends match R, or are at target T.
- The legend reads: "Lit: matches the comparison revision. A comparison, not a delivery path."

### 3.4 With Git history (§7)

**Applies per station.** A station is ordered when all of these hold:
- the key's repo is allowed and fetched;
- the key's ref resolves to a target T;
- the station is in state 5 (single applied revision);
- its sha is in the fetched history.

Every other station keeps its §3.3 state. One unknown sha qualifies only its own station.

**The target T** is Klyx's fetched tip of the ref:
- a Flux branch tip, a tag's commit, or a pinned commit;
- an Argo ref only when it is fully qualified, or a sha.

Semver ranges, `HEAD` and unqualified Argo refs have no T: they stay comparison-only and say why. T always shows its freshness: "main at a1b9e3c · fetched 20:31".

**Ordered states:**

| State | When |
|---|---|
| **at target** | the sha is T |
| **behind by N** | an ancestor of T; N = `rev-list --count sha..T` |
| **ahead by N** | T is an ancestor of the sha |
| **diverged** | they share a merge-base, and neither is an ancestor of the other: "diverged at <merge-base>" |
| **unrelated history** | there is no merge-base |
| **not in fetched history** | Klyx's history lacks the sha |

**"Held · N behind"** needs evidence tied to T:
- the station's source fails to fetch while the station is behind T;
- an eligible unit attempted T and is failing;
- an eligible unit reports `DependencyNotReady` while it is behind T. This is labelled "blocked by dependency <name>", the controller's own report.

**The headline** may then say "prd · North Europe is held, 3 commits behind main".

### 3.5 Mesh on the board

- Mesh links come from `MeshGraphDTO`.
- **Same rank, adjacent tracks:** a mutual peering draws a solid link between the two stations. A one-way peering is dashed in caution, with "one-way" and the configuring side.
- **Any other pair** is listed on the station card and is not drawn.
- **Off-fleet peers** are listed under the board.

### 3.6 Data lifecycle

- **One acquisition per cluster, shared by every consumer** (board, station strip, queue). Health 1c's rule of no per-screen fan-out holds.
  - **`LineService`** builds its facts from the Observer's shared Flux and Argo rows.
  - **It extends those rows** with what the comparison needs:
    - the source ref and URL;
    - the applied and attempted revisions;
    - the artifact `lastUpdateTime`;
    - every Argo source pair.
  - **It never starts its own watch or list.**
- **`LineDTO`** carries:
  - per station: the facts, the state, `readAt`, the Observer incarnation, the read state, and the last good facts;
  - a version, so the frontend orders replies.
  - A station's facts are dropped when its cluster's incarnation changes. The connection and request fencing follows health 1c.
- **Pushed on change**, like the cluster summary. The station strip and the board subscribe; they never poll.
- **The board shows "as of <oldest readAt>"** when any station is stale. The footer shows connection counts only; it does not claim the line is live.

### 3.7 The fleet screen

**Top to bottom:**
1. The verdict sentence (§3.3) and the legend.
2. The board.
3. "Needs you", the queue.
4. The aside: Ledger and Horizon.

**"Needs you":**
- **Rows come from** each connected cluster's summary, selected the way `clusterAttention` already selects them:
  - fault and attention roles only, never native `Top` by `Included` alone;
  - each tool's own vocabulary kept (Flux "Not ready", Argo "Degraded");
  - controller-only issues and coverage gaps as rows of their own, even with no object ref ("kustomize-controller not found: status from objects only"; "Events unreadable").
- **Samples:** `Top` is at most five refs per part, so each area's rows end in "+N more in <lens>", from the task counts, linked to the lens.
- **A row shows:** its aspect, kind, name, the verdict label, a `LinePos` station tag, and its area.
  - **Age and reason are new data (§8.1).** Each `Ref` carries `since` and `reason`, with per-rule meaning (§8.1). An age with no rule-backed source shows the observation log's wording: "first observed …".
- **Folding:** rows fold under another only along **reported-blocker** edges (§5.1), labelled "reported blocker". They never fold under a "root cause".
- **Order:** environment rank (prd first), then role, then area.
- **Filters:** All, Prd only, Faults. They are kept for the session.

**The aside:**
- **Ledger:**
  - Without Git history, it is "revisions on the line now": each distinct station sha with its per-track dots and artifact time.
  - **With history (§7), "commits not yet everywhere":**
    - the union, de-duplicated, of each ordered station's missing set (`git rev-list T ^<station sha>`), in topological order, newest first, capped at 20;
    - each with its short sha, subject, author and time, and dots for which ordered stations contain it.
    - Commits that exist only on a diverged or ahead station are listed under that station, never merged into one order.
  - "Open in Git" links to the commit, on known hosts or through `git.repos[].web`.
- **Horizon:** the next 30 days, from `FleetExpiry`.

**Footer:** clusters connected, failed and checking.

### 3.8 The station strip

- Every cluster screen's location bar draws the cluster's own track: its ranks in order, with this station boxed and its comparison state.
- A siding station shows "siding".

## 4. Chrome and navigation

**The location bar** replaces `TopBar`'s mast and `NavBand`'s group selector:
- the mark;
- `Fleet / Station / Lens / Object`;
- the station strip;
- the fleet indicator;
- the ⌘K well;
- the utilities: forwards, Activity, Terminal, Ask, Settings.

History (mod+[ and mod+]) stays.

**The fleet indicator** replaces `FleetSpine`'s pager role:
- **What it shows:** per-axis counts of **clusters**, from `fleetAttention.ts`:
  - fault;
  - warning;
  - unreachable;
  - connection stale or degraded;
  - unverified.
- Each axis has its own mark. It never sums object counts.
- Clicking it lists the affected clusters, each with its axis and reason, and links to them.
- It is always visible inside a cluster.
- **Given up:** the blink and the per-cluster lamp column. This is an owner trade-off (§14).
- **The dock badge and action notifications stay as they are.** They are not counted as replacing the spine.
- **Health notifications are new behaviour,** specified in §8.6.

**Every current function gets a named home:**

| Today | New home |
|---|---|
| History and recent places | Location bar arrows; recent places in the console's empty state |
| Attention button | The fleet indicator |
| Protected cue | A persistent "protected" tag on the station crumb, and in the station strip |
| Activity | Location bar utility, and a console entry |
| Terminal | Location bar utility (open cluster), and a console verb |
| Port-forward indicator, stop-all | Location bar "forwards" utility when any is active, with stop-all inside |
| Theme toggle | Settings > Appearance, a console entry, and the existing shortcut |
| Settings | Location bar utility |
| Overview: utilisation and sparklines, monitoring status, `CriticalEvents`, `ClusterRisks`, attention areas, connection notes | All on the station page, in that order, below the station's verdict and its "Needs you" rows |

**The lens index** replaces `NavBand`'s tree and tabs:
- **Width:** 232px, collapsible with mod+\.
- **"Needs you":** task lenses with fault or attention rows, ordered by severity, then count.
- **"Everything else":** every other task lens, *and* the generic-resource destinations (Resources and the API browser), in registry order.
- **Capability states:**
  - A capability that is checking or unknown stays listed, as "checking…".
  - Only a confirmed absence hides a lens, and one quiet line names what is absent.
- **Keys:** mod+1..9 keep their targets.

**The inspector** keeps its right slot (`useResizableDock`), with one anatomy:
1. kind, name and meta;
2. the verdict sentence with its `Aspect`;
3. the drawing, when the kind has one;
4. actions;
5. evidence.

It is **pinned to its object's cluster and UID**. Every verb, follow-up and Explain from it uses that cluster and UID, never the list's or the open cluster's.

**The chat drawer** keeps its slot (§5.3).

## 5. Drawings

### 5.1 The GitOps dependency graph

- **Built for** a selected Kustomization or HelmRelease, from its bound source and its `dependsOn`, followed recursively.
- **A node** carries:
  - its identity: cluster, GVR, namespace, name and UID;
  - its provenance: Observer rows or a detail read, with `readAt`.
- **Edges are typed:**
  - **`source`:** the `sourceRef`.
  - **`dependsOn`:** declared in the spec.
  - **reported blocker:** a declared edge that the downstream node's controller *reports* is blocking it.
    - For `dependsOn`, that means Ready False with reason `DependencyNotReady`, and the message names that dependency.
    - For `source`, it means the source-readiness reasons the GitOps view already uses.
- **Drawn as** a `Rail` tree:
  - Branches show, and multiple blockers each get their own branch.
  - A failing upstream node is labelled "upstream failure".
  - A reported-blocker edge is solid danger, labelled "reported blocker".
  - The last node of a chain of reported blockers is labelled "reported chain ends here".
  - **"Root cause" is never claimed.**
- **Bounds:**
  - depth 6;
  - at most 24 nodes and 24 reads;
  - cancelled when the selection changes.
- **Incomplete evidence** is a dashed grey stub naming what happened, and the graph header says "incomplete: …". The cases are:
  - truncation at depth;
  - a cycle, cut and named;
  - an unreadable node;
  - a stale read;
  - a node whose UID was replaced mid-walk.
- **Supply facts are stated without causality.** "Credential supplied by ExternalSecret flux-system/github-deploy-key" comes from the typed `secretRef` → Secret → ExternalSecret link. Nothing claims the credential is the cause.
- **Change proximity (§8.3):** each node and supply object shows its last recorded managed-field update: time, manager, operation and subresource. When a main-resource update (never a status-only one) falls within 10 minutes before a failing node's rule-backed `since`, the graph places the two side by side. For example: "Secret platform-auth: last managed-field update 20:20 (external-secrets, Update). Source Ready=False since 20:21 (condition time)." Both are qualified observations; nothing claims which field changed, or a cause.
- **The end of a reported chain** lists its evidence: the failing condition, the supply facts, the change proximity, and the Git "not here yet".
- **The inventory line** is "last applied <sha>; inventory 38 objects (14 workloads)". It never claims the workloads run that revision.
- **With Git (§7)**, a node behind T gains "not here yet":
  - the commits between its applied sha and T that touch its `spec.path`;
  - and the changed files (name-status, trees only).
- **cert-manager and ESO chains** reuse `Rail` with their existing semantics; this section adds no new causal claims to them.

### 5.2 The Gateway data path

- **The drawing:**
  1. The Gateway: listener, hostnames, attached policies.
  2. The HTTPRoute.
  3. One branch per rule, with a bar per backend for its **configured weight** (an unset weight counts as 1), labelled "configured".
  4. The Service, with its ready endpoints.
- **A backend the per-rule verdict marks failing** has a danger branch.
- **Measured traffic (§8.5), per rule,** only where attribution is verified:
  - the request rate;
  - the 5xx and 503 shares, with a 30-minute sparkline;
  - retries;
  - the split per backend.

  Each metric shows separately as available or unavailable. Each value carries its window, and a missing series is "no samples", never zero.
- **Configured weight and measured split** are shown side by side, never merged.
- **Onset** is the "first sampled crossing of 1% at 20:29 (1-minute rate, 30-second step)". When the window's first sample is already above the threshold, it reads "already above 1% at window start". A gap in the samples restarts the search.
- **Revision proximity** places two qualified times side by side: "a1b9e3c applied change observed 20:28; first sampled crossing 20:29". It appears only when the observation log has continuous coverage over that span. It is never a cause.
- **No metrics endpoint:** "no route metrics: configure a metrics endpoint", and nothing is estimated.
- **Below the drawing:** the rules and policies table, and the mesh note for global Services.

### 5.3 The chat read ledger

- Reads are a `Rail` in read order: completed in route, failed in danger, cancelled dashed. A read's redaction count stays on its line.
- Chips stay as in v0.13.1.

### 5.4 `LinePos`

- One square per rank present on the station's track, typically two to four. The station's square is filled, with danger when the station has a fault.
- A siding station shows one hollow square and "siding".
- The glyph always pairs with the cluster's unique short code (`spineCodes`), and its accessible name is the full cluster name plus its position: "aks-prd-neu, prd on North Europe".

## 6. The command console

- **The overlay:** ⌘K, 760px wide, 88px from the top.
- **The empty query** shows, in order:
  1. Recent places.
  2. The fleet indicator's affected clusters.
  3. The verbs for the current selection.
- **Groups:** Objects, Run, Go to, Ask.
- **Object scope** is the fleet metadata index (§8.4): every indexed kind on every connected cluster, by name, namespace and kind.
  - Aspects come from the Observer's full evaluated state, by UID (§8.2). An object of a kind the Observer does not evaluate shows "health unverified", never healthy.
  - The console states its coverage: "Searching 7 clusters · 14 kinds". Any cluster whose index is building, partial, capped or forbidden for a kind says so in the results, with the lens to search instead.
- **A row:** its `Aspect`, kind, name, `LinePos` and a note.
  - Same-name objects on different clusters are separate rows. Each is pinned to its cluster and UID.
- **Opening:**
  - ↵ opens.
  - ⌘↵ opens beside, in the inspector, pinned to the row's cluster and UID, keeping the list.
  - A lens with no independent inspector navigates instead.
- **Verbs only:** a "Verbs only" toggle button in the console header, with the shortcut alt+V. Tab moves focus normally.
- **Protected verbs** keep ▲, "type the cluster name", and the grant flow.
- Results never animate.

## 7. Git access (read-only, through the system git)

**Why.** History lets Klyx:
- order revisions (§3.4);
- show real commits in the Ledger;
- show a dependency-graph node what it does not have yet;
- link "Open in Git" to the commit;
- give `flux diff --path` a local clone root, closing the M10-f follow-up.

**The binary.**
- Klyx runs the system `git`, resolved through `internal/clibin`.
- **Git 2.45 or newer is required,** for `GIT_NO_LAZY_FETCH` (`--no-lazy-fetch`, new in 2.45). Older or missing git turns every Git feature off, with one note naming the reason, and the §3.3 rules hold.
- No hosting API. Klyx stores no tokens.

**The transport boundary:**
- **What runs is always the owner's approved string.** Klyx executes the `git.repos[].url` exactly as the owner approved it.
  - A URL read from a cluster is only *matched*, after normalisation, to select an approved entry. It is never executed.
  - Several cluster URLs may map to one approved entry. The owner sees each mapping in Settings > Git.
- **No URL rewriting:**
  - Git runs with an isolated config, so the owner's `url.<base>.insteadOf` rewrites do not apply. The owner approves the final URL instead.
  - `http.followRedirects=false`: a redirect fails, and names the URL it pointed at for approval.
  - An SSH host alias in an approved URL resolves through the owner's own `~/.ssh/config`, which Klyx does not interpret. The approval dialog shows the alias as written.
- **Authentication paths kept:**
  - the SSH agent and `~/.ssh/config`, through `ssh` with `-o BatchMode=yes`;
  - the owner's credential helper, read once from their git config and passed explicitly as `credential.helper`.
  - Nothing else from the owner's git config is used.
- **Prompts are off:**
  - `GIT_TERMINAL_PROMPT=0`.
  - `GIT_ASKPASS=/usr/bin/false`, `SSH_ASKPASS=/usr/bin/false` and `SSH_ASKPASS_REQUIRE=never`.
  - `GCM_INTERACTIVE=never` for Git Credential Manager.
  - A credential helper that shows its own window anyway is outside Klyx's control. Settings > Git says so.

**The environment is an allowlist, built from scratch:**
- **Passed through:** `HOME`, `PATH`, `USER`, `LANG`/`LC_*`, `TMPDIR` and `SSH_AUTH_SOCK`.
- **Set by Klyx:**
  - `GIT_CONFIG_NOSYSTEM=1`;
  - `GIT_CONFIG_GLOBAL=<a Klyx-generated file>`;
  - `GIT_DIR` set per operation: the staging repo for an acquisition, the cache for reads and publication. The working directory matches it.
  - `GIT_NO_LAZY_FETCH=1` on **every** command except an acquisition fetch. Only a staged acquisition (below) ever downloads objects.
  - `GIT_PAGER=cat` and `PAGER=cat`;
  - the prompt variables above.
- **Never passed:** every other `GIT_*` variable (exec path, alternates, config overrides), cloud credentials, and KUBECONFIG.
- **The existing shell-outs** (`execenv`, `fluxcli`) inherit the parent environment today. Git does not follow them.

**Allowed commands.** Klyx builds every argv:

| Command | Purpose |
|---|---|
| `git init --bare --template=` | create the cache, or a staging repo |
| `git fetch --atomic --no-tags --filter=blob:none origin +<ref>:refs/klyx/staging/<ref>` | history acquisition, into a staging repo only, with explicit refspecs for the tracked refs |
| `git fetch --no-tags --no-write-fetch-head --filter=blob:none --stdin origin` | content acquisition: named blob ids on stdin, into a staging repo only |
| `git ls-tree -r -z <rev> -- <path>` | list a snapshot's blobs: tree objects only, no history walk |
| `git diff-tree -r --no-renames -z <a> <b> -- <path>` | list a content diff's endpoint blobs: trees only |
| `git cat-file --batch-check` | report which listed ids are missing (under `GIT_NO_LAZY_FETCH`) |
| `git verify-pack -v <staging pack>.idx` | validate only the packs this acquisition created |
| `git update-ref --stdin` | publish refs, in one transaction |
| `git log --no-renames`, `git rev-list --count`, `git merge-base [--is-ancestor]` | metadata reads |
| `git diff --name-status --no-renames --no-ext-diff --no-textconv` | metadata reads |
| `git diff --no-ext-diff --no-textconv <a> <b> -- <path>` | a content diff, only after its blobs were acquired (below) |
| `git archive --format=tar <rev> -- <path>` | the cache diff's input, only after its blobs were acquired |

- **On every call:**
  - `--no-pager`;
  - `-c core.hooksPath=/dev/null`;
  - `-c protocol.allow=never -c protocol.https.allow=always -c protocol.ssh.allow=always`;
  - `-c core.fsmonitor=false -c submodule.recurse=false -c diff.external= -c http.followRedirects=false`;
  - `-c fetch.unpackLimit=1 -c transfer.unpackLimit=1`, so every acquisition stays in packs Klyx can enumerate, never in loose objects.
- **Arguments:**
  - Revisions are hex shas or the tracked ref, placed after `--end-of-options`.
  - Paths come after `--`, under `--literal-pathspecs` (`GIT_LITERAL_PATHSPECS=1`).
- **Never allowed:** `push`, `commit`, `tag`, `branch`, a checkout into the owner's working trees, `config --global`, hooks, submodules.
- **Deadlines and bounds:** 60s for a fetch and 10s for a local query. Each call runs in its own process group, killed on a deadline or a cancel. Captured output is capped at 1 MiB.

**Acquisition: one contract for history and content.**

Every download goes through these steps, under one fleet-wide acquisition lock. Two kinds of download use it: a history refresh, and the content a preview or a cache diff needs.

1. **Reserve.**
   - The lock means one acquisition at a time across the fleet. A refresh and a preview queue behind each other.
   - Klyx reserves the room left under the per-repo cap and the total cap.
   - A preview's reservation is also capped at 64 MiB.
2. **Stage.**
   - A fresh staging repo is created beside the cache. It borrows the cache's objects through an alternates file that Klyx writes, never an environment variable.
   - **A history refresh** fetches `--atomic` into `refs/klyx/staging/*`.
   - **A content acquisition** lists exactly the blobs its consumer needs, from trees only, never by walking history:
     - **a snapshot** (the cache diff's input): `ls-tree -r` of the requested paths at the one revision;
     - **a content diff:** `diff-tree -r` between the two endpoint revisions, limited to the requested paths. It lists the old and new blob of each changed path, and nothing older.

     It then checks those ids with `cat-file --batch-check` under `GIT_NO_LAZY_FETCH`, fetches only the missing ones through `--stdin`, and is refused above 5,000 ids.
3. **Validate the new packs only.**
   - The staging repo's *own* packs are enumerated with `verify-pack -v`. Alternates are never enumerated, so blobs already in the cache neither fail validation nor hide an incoming one. The unpack limits keep every object in a pack.
   - **A history refresh** must bring no blob. A server that ignored the filter is refused ("this server does not support partial clone").
   - **A content acquisition** must bring only the blob ids it asked for.
   - The refs resolve, and the size is within the reservation.
4. **Publish, journaled.** Each step is made durable (the file, then its directory, fsynced) before the next begins:
   1. **Journal.** Klyx writes a journal into the cache. It records every file it is about to create, and every ref update as `{ref, old, new}`, with `old` read at the start of publication.
   2. **Files.** For each new pack, Klyx moves every file it has, including the `.promisor` marker a filtered fetch creates: `.pack`, `.rev` and `.promisor` first, `.idx` last.
   3. **Refs.** A history refresh runs one `update-ref --stdin` transaction. Each update names its expected old value (`update <ref> <new> <old>`), so the transaction fails if anything else moved the ref.
   4. **Mark.** The journal is marked committed.
   5. **Remove.** The journal is removed.
5. **Recovery reads the refs, never just the mark.** It runs on start, and before every acquisition, for any journal left behind. It decides by what the refs show now:

| Refs now | Meaning | Recovery |
|---|---|---|
| every journaled ref at `new` (a mark is optional) | the transaction committed | **finalize:** keep the files, remove the journal |
| every journaled ref at `old`, and no mark | it never committed | **roll back:** delete exactly the journaled files, then remove the journal |
| some at `new` and some at `old` (outside interference only, since the transaction is atomic) | mixed | every journaled file still present: move the rest to `new` in one transaction, then finalize. Otherwise: restore all to `old` in one transaction, then roll back. |
| any ref at a value that is neither `old` nor `new` | uncertain | **delete nothing.** Mark the cache "needs repair", stop using it, and offer "Clear cache" to rebuild it from scratch |
| a content acquisition (no refs) | the new blobs are never ref targets | finalize if every file is present and the mark exists; otherwise roll back. Either way the cache stays a valid partial clone. |

   - **Failures before step 3** (a kill, a cap, a validation refusal, cancellation) roll back directly.
   - **The staging repo** is always deleted afterwards.
   - **The last good history stays,** and the budget is released in every branch.
6. **Size while staging.**
   - The staging repo is sampled every 250ms, and the fetch is killed when it passes its reservation.
   - Overshoot is bounded to one interval's transfer. It lives only in staging, and is deleted.

**The cache's own config,** written by Klyx when it creates the cache, keeps the partial clone valid:
- `core.repositoryformatversion=1`;
- `extensions.partialClone=origin`;
- `remote.origin.url=<the approved URL>`;
- `remote.origin.promisor=true`;
- `remote.origin.partialclonefilter=blob:none`.

A published cache passes `git fsck --connectivity-only` with its blobs missing, including after a restart. This is a test check, not something the app runs.

**Content for previews and the cache diff.** `git diff` with content, and `git archive`, run only after a content acquisition has brought every blob they need. They run with `GIT_NO_LAZY_FETCH`, so a blob still missing fails the preview ("content incomplete") and never downloads behind the budget.

**The cache diff** (slice 6b, replacing the M10-f `flux diff --path` follow-up) is a separate authorisation from reading history. The build must be confined, not just the extraction: a Kustomize generator, patch, schema or nested base can otherwise read outside the tree. Codex reproduced exactly that with the flux CLI, which builds without confinement.
- **Klyx does not run the flux CLI for cache builds.** It builds in-process:
  1. **The input set is computed in rounds.**
     - It starts from the literal `spec.path`.
     - Each round's `kustomization.yaml` files are acquired as content and parsed. The in-repo relative paths they reference (`resources`, `components`, `patches`, generator `files`) add their subtrees. A reference that leaves the repo root is refused.
     - Rounds repeat until no new path is added: at most 10 rounds, within the 5,000-file and 64 MiB caps.
     - The final set is acquired as content, then exported with `git archive` of exactly those paths.
  2. Klyx's tar reader loads it into an **in-memory filesystem**, refusing absolute paths, `..`, and links that leave the root. Caps: 5,000 files and 64 MiB.
  3. The Kustomize API builds over that filesystem only:
     - root-only load restrictions;
     - plugins and exec functions off;
     - a loader that refuses every remote URL.

     Because nothing outside the archive exists in that filesystem, no generator, patch, schema or nested base can read the owner's disk.
  4. The objects built are diffed against the cluster with a server-side dry-run, through Flux's `ssa` package, which is what `flux diff` itself uses. It runs on the station's pinned connection.
- **Remote bases** are refused, unless the owner enables them per repo in Settings > Git. In that case they are fetched only from approved `git.repos` URLs, through the same staged acquisition.
- **Not done:**
  - **Decryption:** never. SOPS-encrypted files are diffed as stored, and the result says so.
  - **Flux post-build substitution:** not applied. The diff says "variables shown unsubstituted".
- **A fixture** proves that a generator reading `../outside.txt` fails, and so does a nested base escaping the root.

**The cache and its bounds:**
- **Location:** `<config dir>/git-cache/<sha256 of the approved URL>.git`, 0700.
- **Contents:** commits and trees for the tracked refs. Blobs appear only after a content acquisition for a preview or a cache diff. "Clear cache" removes everything.
- **Caps:** 1 GiB per repo and 2 GiB across all repos (§14). They are enforced only through the acquisition contract above.
- **Settings > Git** shows each repo's size, last fetch, the cluster URLs mapped to it, and "Fetch now" and "Clear cache".

**When it fetches.**
- When the fleet view opens.
- At most every 5 minutes while it stays open.
- On "Fetch now".
- One acquisition at a time across the fleet (above).

**Failing honestly.**
- A failed or capped fetch keeps the last good history, labelled "history as of <time>; fetch failed: <scrubbed error>".
- With no good history, the §3.3 rules apply.

**Untrusted text.**
- Commit subjects, author names and file paths are plain text, never markdown or HTML.
- Caps: a subject at 200 characters; at most 20 commits and 200 paths per view.
- This spec never sends them to an AI provider.

## 8. New data Klyx collects

Each source below is new, and each declares where its claims come from.

### 8.1 Observer row extensions

- **Flux rows add:**
  - the `sourceRef` (kind, namespace, name);
  - `lastAppliedRevision` and `lastAttemptedRevision`.
- **HelmRelease rows add:** the `status.history[]` entries, at most 5, each with its chart version, `configDigest` and status.
- **Source rows add:** `spec.url`, `spec.ref`, the artifact revision and its `lastUpdateTime`.
- **Argo rows** keep every source pair from `spec.sources`, `status.sync.comparedTo` with `sync.revision(s)`, `status.operationState` (phase plus `syncResult` revisions), and `status.history[]` (at most 5 entries, each with its own source(s)).
- **Refs carry `since` and `reason`,** with per-rule meaning:

| Rule kind | `since` comes from |
|---|---|
| **Condition-backed** (Flux Ready, Argo health and sync, cert-manager Ready, Gateway Accepted/Programmed) | the matching condition's `lastTransitionTime` |
| **Derived** | a time computed from fields. ESO overdue: last refresh + 2 × interval. Expiry: `notAfter` minus the warning window. |
| **Observed only** (CrashLoopBackOff, restart counts, zero endpoints, and any rule without a matching timestamp) | the observation log's wording, never an unrelated condition's age |

  `reason` is the condition's reason and message, scrubbed and capped at 200 characters.
- **All of this is additive.** Existing lens rules and the parity gate are unchanged.

### 8.2 The observation log and the transition stream

- **The source:**
  - The Observer emits a bounded **transition stream** from its complete, UID-keyed evaluated state, not from summary `Top` samples. `Top` stays a UI sample.
  - Each transition is `{at, cluster, incarnation, GVR, UID, namespace/name, role from → to, coverage}`.
  - A buffer overflow or a coverage change emits an explicit **loss marker** for the kinds affected.
- **The log** records:
  - transitions;
  - `LineService` station states, with the comparison key and R or T at the time;
  - applied-revision changes;
  - connection states;
  - loss markers and gaps.
  - **A change of reference** (R recomputed, a new key chosen) is recorded as "reference changed", never as a deployment or a hold.
- **Continuity wording:**
  - A state seen on a baseline (the first complete read, or after a reconnect or gap) is "first observed at <time>; start unknown".
  - A change seen after a gap is "observed at <time>, after a gap from <t0> to <t1>".
  - "Since <time> (observed)" is used only with continuous coverage from before the change.
  - "Already present by <time>" is used only when other evidence, such as a condition time, proves it.
- **Bounds:** 24 hours, or 20,000 entries per cluster.
  - On eviction, each object keeps one **state proof**: its current state, plus either a proven lower bound for when that state began (a transition seen with continuous coverage, or a rule-backed time) or "start unknown".
  - The proof resets on every state change and every coverage gap. It is purged when the object is deleted or replaced, and once its episode closes.
  - Ages beyond the trimmed log read "at least since <proven lower bound> (log trimmed)". When no proof exists, they read "start unknown". A watermark of when observation began is never used as a state's age.
- **The console join:** an object's aspect comes from this evaluated state. A kind the Observer does not evaluate is "health unverified".
- **On disk:** persistence is an owner decision (§14).

### 8.3 Last recorded managed-field updates

- From `metadata.managedFields[]`, Klyx shows "last recorded managed-field update", with its `time`, `manager`, `operation` and `subresource`. It is not an audit log:
  - Manager names are workflow identifiers, not authenticated writers.
  - Status-subresource entries are labelled "status update" and never used for change proximity.
  - Klyx never claims which field changed.
- **Read for:** dependency-graph nodes and their supply objects, metadata only.
- **No record:** absent or reset `managedFields` read "no managed-field record".

### 8.4 The fleet metadata index

- **Acquisition:**
  - Per cluster, metadata-only (`PartialObjectMetadata`) list and watch, for the configured kinds that the Observer does not already watch. Kinds the Observer does watch are indexed from its model, never with a second watch.
  - Lists paginate at 500.
- **Projection before retention:**
  - An informer transform keeps `{GVR, UID, namespace, name, labels, creationTimestamp}`. Labels are capped at 2 KiB per object, with a truncation marker.
  - Annotations and `managedFields` are dropped before anything is stored or queued.
- **Default kinds:**
  - namespaces and nodes;
  - Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs, CronJobs and Pods;
  - Services, Ingresses, Gateways, HTTPRoutes and GRPCRoutes;
  - ConfigMaps and Secrets, by name only;
  - PVCs;
  - the Flux, Argo, cert-manager and ESO kinds.
  - Each is configurable in `fleet.yaml`.
- **Bounds** across the store, the buffers and the search index:
  - 50,000 objects and 32 MiB per cluster;
  - 200,000 objects and 128 MiB across the fleet (§14).
- **Admission:**
  - Every list page *and* every watch event (an Add, or an Update that grows labels) is admitted against both budgets before it is retained.
  - The accounting is shared and atomic across kinds and clusters.
  - **At a cap:**
    - the kind that would cross it stops: its list stops, or its watch is closed;
    - the subset it already holds is frozen, and reads "capped at N · as of <time> · stale";
    - other kinds continue until they too would grow past the cap.
  - Vacancies are not refilled, and the index never claims to be complete.
- **Kinds the Observer already watches:** the cap applies to the *derived index entries*. The shared Observer is never stopped.
- **Per-kind states:**

| State | Meaning |
|---|---|
| **indexing** | the first list is in progress |
| **live** | listed, and the watch is running |
| **capped** | stopped at a cap (above) |
| **denied** | list refused (403) |
| **watch denied** | the watch was refused after a good list: the last list is kept, labelled stale |
| **metadata unsupported** | a 406. Klyx never falls back to a content read, Secrets included. |
| **stale / recovering** | watch errors, retried with backoff |

- **Lifecycle:** it starts after the Observer is live, is fenced by the same incarnation, and is cancelled on disconnect.
- **What it holds:** never object contents.

### 8.5 Route metrics, per rule

- **Supported mapping (v1):**
  - Envoy Gateway's default per-rule cluster stat names (`httproute/<ns>/<name>/rule/<n>`);
  - scoped to the Gateway's Envoy pods by their owning-gateway labels.
- **Treated as unattributable,** shown "unavailable (shared or custom cluster names)":
  - a custom `clusterStatName`;
  - `MergeBackends`;
  - several parents sharing clusters;
  - any ambiguity.
- **Queries per rule:**
  - the request rate;
  - shares by response class and by code (503);
  - retries;
  - a range query over 30 minutes at a 30-second step, using a 1-minute rate.
- **Availability is tracked per metric,** each separately:
  - the request rate;
  - the class and code shares;
  - retries;
  - the per-backend split.
- **The per-backend split** only where the gateway exposes per-backend clusters for that rule.
- **Attributed history needs a proven interval.**
  - **What an interval is:** a span where Klyx observed the same route UID and the same **rule fingerprint** continuously. The fingerprint is a hash of each rule index's matches, backends and weights. Prometheus labels carry neither the generation nor the UID, so the proof comes from the observation log.
  - **Outside it** (before Klyx first saw the route, across a coverage gap, before a fingerprint change), the series reads "unattributable before <time>" and is not drawn as this rule's.
  - **Samples within one rate window** (1 minute) after an interval's start are excluded, because `rate()` over that window still includes the previous rule's samples.
  - A recreated or reordered route never joins history to today's backends.
- **`internal/routemetrics` parsing** keeps the rule index (today it discards it).
- **Captured-metric fixtures:**
  - dedicated and shared clusters;
  - custom names;
  - several parents;
  - retries;
  - direct and local responses.

### 8.6 Health notifications

- **Opt-in,** in Settings > Notifications. The OS permission is requested lazily, through `ShellService`.
- **Driven by** the transition stream (§8.2). Never driven by summaries.
- **The episode state machine,** per cluster and UID:

| Phase | What happens |
|---|---|
| **Baseline** | at connect or reconnect, or after a loss marker, faults already present are listed once in the app ("3 faults present at connect"). They never become notifications (§14). |
| **New episode** | a non-fault → fault transition with continuous coverage. It notifies, subject to the triggers below. |
| **Coverage lost** | the episode is suspended. No recovery is claimed. |
| **Unknown** | fault → unknown or checking, even with live coverage (a Node's Ready=Unknown, for example) suspends the episode. A return to fault resumes the same episode, with no new notification. |
| **Recovered** | only a transition into a **positive healthy role** (ok) with live coverage. Unknown, checking and unverified are never recovery. It notifies only if recoveries are enabled. |
| **Deleted** | the UID is gone. The episode closes as "deleted", with no recovery. |
| **Replaced** | a new UID under the same name starts a new episode. |
| **Incarnation change** | everything is re-baselined. |

- **Triggers:** new faults on protected clusters (the default once enabled); optionally, any cluster, and recoveries.
- **De-duplication:**
  - one notification per UID and role per 30 minutes;
  - bursts coalesced per cluster over 60 seconds ("3 new faults on prd · North Europe"). Items that recover before the burst is sent are dropped from it.
  - Nothing is sent while the app is focused on that cluster.
- **Content:** cluster, kind/name and the verdict label.
- **Click:** opens the object, pinned to its cluster and UID.

## 9. Applying it to every lens

- **Lenses keep** their data, verdict logic and keyboard paths.
- **Each lens takes:**
  - the header: lens title plus a count sentence, in the tool's own vocabulary;
  - `LensTable` rows: 40px, an `Aspect` column, quiet healthy rows, dashed not-yet-realized marks;
  - the §4 inspector anatomy.
- **Retired:** the remaining Signal Bench primitives (lamp glow, module and well machining, pressed-filter styling).

## 10. Accessibility

- **Never colour alone:** every `Aspect` has a text equivalent, and fill versus outline separates fault from look at.
- **Focus:** a 2px route ring with a 2px offset. Normal Tab order everywhere; no Tab hijacking.
- **The board:**
  - Each station is a link named, for example, "prd · North Europe, aks-prd-neu, differs at 9f2c11d, failing for 18 minutes".
  - Segments are `aria-hidden`.
  - A visually hidden list mirrors the board, with its comparison source and its legend.
- **Tables** keep their roles. Icon-only buttons carry `aria-label`.
- **Reduced motion:** opacity only.

## 11. Slices (each Codex-reviewed)

1. **Tokens and type.**
   - The `night` and `day` themes, and the semantic mapping.
   - The diff tokens, with every diff surface moved to them.
   - Bundled fonts.
   - Contrast tests for text and strokes.
   - The migration.
   - `Aspect` replaces lamp physics.
   - No layout change.
2. **New data, part 1** (Go): the Observer row extensions with per-rule `since` (§8.1), the Observer's transition stream with loss markers, and the observation log with continuity wording (§8.2).
3. **The line model** (Go, no UI).
   - Placement.
   - The comparison unit and revision parsing.
   - Key selection, R, the states and flags, and "held" evidence.
   - `LineService` on the extended Observer rows, with `LineDTO`, versions, incarnation fencing and push.
   - The adversarial fixtures of §12.
4. **Chrome.**
   - The location bar and the fleet indicator.
   - The parity table, implemented.
   - The lens index: task and resource destinations, with checking kept visible.
   - The station strip on `LineDTO`.
   - The inspector anatomy with cluster and UID pinning.
5. **The fleet screen.**
   - The board.
   - The queue: summary-based selection, "+N more", controller and coverage rows, and `Ref.since`/`reason`.
   - The Ledger without Git.
   - The Horizon.
6. **Git access** (§7).
   - The `gitcli` package.
   - `git.repos` and Settings > Git.
   - The cache.
   - The §3.4 states, the Ledger's commits, and "not here yet".
   - **6b:** `flux diff --path` from the cache (M10-f).
7. **New data, part 2:** object write times (§8.3) and route metrics per rule (§8.5).
8. **Drawings.**
   - The dependency graph, with typed edges, bounds, supply facts and change proximity.
   - The data path, with configured weights, measured per-rule traffic, onset and revision proximity.
   - The chat rail.
   - `LinePos`.
9. **The fleet metadata index** (§8.4) **and the console:** scope and coverage messages, pinning, the verbs-only toggle.
10. **Health notifications** (§8.6).
11. **The lens sweep and station page.**
   - Every lens onto the header, `LensTable` and the inspector.
   - The Overview sections on the station page.
   - Headless captures of every lens in both themes.
   - The parity gate re-run.
12. **Smoke.**
   - On the kind lab in both themes, **in the native app's WKWebView** as well as headless Chromium.
   - With an eyeball checklist.

## 12. Testing

- **Line (Go, table tests):**
  - Placement:
    - rank and region;
    - a group labelled as a group;
    - collisions;
    - the siding;
    - the one-row fallback.
  - Comparison unit:
    - same URL with different refs (two keys);
    - two sources sharing a key in different namespaces;
    - several units on one station;
    - mixed Flux and Argo with compatible and incompatible refs;
    - a HelmRelease: chart unit, the deployed history entry, a failed latest entry, missing history;
    - an Argo app whose sync.revision B is compared but unsynced over a deployed A;
    - a failed or partial Argo sync;
    - an Argo source reorder or edit since the last deployment;
    - an Argo chart range in `targetRevision`;
    - an Argo multi-source app (one unit per source);
    - a successful Argo app with `revisionHistoryLimit: 0` (applied unknown, kept out of matches and the vote);
    - a station with one known applied revision plus an unknown unit (mixed, no vote);
    - an unqualified Argo ref against a Flux branch of the same name (separate keys);
    - an unrecognised revision string.
  - Key selection:
    - each station counted once;
    - ties broken lexically;
    - the override;
    - no shared key.
  - R:
    - the majority;
    - the disclosed tie-break;
    - mixed stations kept out of the vote.
  - States and flags:
    - the precedence table;
    - applied unknown (no deployment record);
    - an empty A;
    - every station mixed (no R);
    - mixed;
    - zero eligible units;
    - suspended;
    - stale generation;
    - partial, stale and failed reads never matching or being at target;
    - compared ≠ applied.
  - "Held": only with attempted R and failing; "differs" with an unrelated failure stays "differs".
  - Failure age shown as failure age.
  - Mesh rules.
  - Lifecycle:
    - an incarnation change drops the facts;
    - a reconnect race;
    - out-of-order pushes;
    - last good kept on a failed read.
- **The queue and console:**
  - more than 5 faults in one part, giving "+N more" and the sample message;
  - a paused native ref, which is not a fault;
  - a controller-only failure;
  - an unreadable kind (a coverage row);
  - same-name objects across clusters, pinned to their own cluster and UID.
- **The fleet indicator** while inside another cluster:
  - a remote warning;
  - a controller-only fault;
  - a failed connection;
  - a lost watch.
- **The dependency graph:**
  - independent failures, with no folding;
  - a diamond;
  - a cycle;
  - depth exhaustion;
  - the node and read caps;
  - cancellation;
  - a replaced UID;
  - an unreadable node;
  - a reported blocker against a merely declared edge;
  - the supply fact appearing only from typed links.
- **The data path:**
  - several rules;
  - zero or missing traffic;
  - configured weight shown apart from the measured split;
  - no metrics endpoint;
  - per-backend split not exposed;
  - onset detection (a flat series; a step; noise below 1%);
  - revision proximity both inside and outside the 10-minute window;
  - the first observation after a reorder or recreation (unattributable before);
  - a change missed during a gap;
  - a rate window crossing the boundary, excluded.
- **New data:**
  - Transition stream and log:
    - more than 5 faults in one part;
    - `Top` eviction;
    - stale → live;
    - reconnect;
    - replacement;
    - failure → unknown → recovery;
    - loss markers;
    - baseline wording;
    - gap wording;
    - healthy → fault → log eviction, never "fault since" the first observation;
    - a baseline fault after eviction ("start unknown");
    - a coverage gap resetting the proof;
    - purging on delete and replace;
    - a reference change never logged as a deployment;
    - rule-specific `since` (condition, derived, observed).
  - Managed-field updates:
    - a status-only update (no proximity);
    - a metadata-only change;
    - absent or reset records;
    - several managers;
    - a missing condition time.
  - The index:
    - every per-kind state;
    - large annotations and labels dropped or capped before retention;
    - Pods capped first;
    - no content fallback on a 406;
    - Observer kinds never watched twice;
    - fencing, cancellation and recovery;
    - peak memory across several clusters at the caps;
    - an under-cap list whose watch then crosses the cap;
    - byte-only growth (labels);
    - concurrent fleet exhaustion;
    - an Observer-backed kind capped without stopping the Observer.
  - Chart comparison:
    - chart version and values digest;
    - never mixed with Git keys.
  - Argo multi-source: one unit per source pair.
  - Notifications:
    - off by default;
    - permission refused;
    - every episode phase;
    - Fault → Unknown → Fault → OK with live coverage (one episode, one recovery);
    - existing faults at connect never notified;
    - de-duplication;
    - bursts, including recovery before sending;
    - focus suppression;
    - a click opening the pinned object.
- **Git:**
  - The allowlist, and the matching of normalised URLs.
  - Refused protocols: `file://`, `ext::` and local paths.
  - No hooks; no prompt (a passphrase key fails fast).
  - Option-shaped values refused.
  - Deadlines and caps.
  - The Ledger: a linear A-B-C with stations at A and C lists B and C; divergent stations.
  - Acquisition:
    - a server ignoring the filter (blob found in the new pack, staging deleted, nothing published);
    - a preview followed by a normal refresh (an alternate blob does not fail validation);
    - a file with many old versions and a small current tree (only current blobs listed and fetched);
    - a two-endpoint content diff (only the two endpoints' blobs);
    - in-repo bases across rounds, and a reference escaping the repo refused;
    - an incoming unfiltered blob with alternates present (still caught);
    - a two-ref fetch where one ref fails (no ref published);
    - failure injected after each file move, and at ref prepare and commit (exactly the journaled files removed, no orphan pack);
    - a crash between moving files and the ref transaction, replayed on restart;
    - interruption during the ref commit;
    - interruption after a successful commit but before the mark (finalized, never deleted; fsck passes);
    - a mark-write failure (finalized from the refs);
    - interruption after the mark but before the journal is removed;
    - a ref moved by something else (needs repair, nothing deleted);
    - `.promisor` published, with `fsck --connectivity-only` passing with blobs missing and after a restart;
    - a kill at the cap (staging deleted, cache unchanged);
    - an over-budget preview refused before download;
    - a history refresh and a preview at the same time (serialised);
    - archive and content diff never lazily fetching;
    - git 2.44 refused, and a missing object never fetched lazily on 2.45.
  - The orderings:
    - behind, ahead, diverged;
    - unrelated history;
    - not in history, qualifying only its station;
    - semver, `HEAD` and unqualified Argo refs without T;
    - target freshness.
  - The transport boundary:
    - a cluster URL variant never executed;
    - `insteadOf` rewrites ignored;
    - a redirect refused;
    - askpass and external-diff sentinels never run;
    - pathspec magic refused;
    - a server without filter support refused;
    - the cap enforced mid-fetch, with temporaries cleaned and last good kept.
  - The cache diff:
    - an archive escape (`..`, absolute, symlink) refused;
    - the file and size caps;
    - a `configMapGenerator` reading `../outside.txt` failing;
    - a nested base or patch escaping the root failing;
    - an unapproved remote base refused;
    - plugins off;
    - no decryption;
    - unsubstituted variables labelled.
  - The Ledger's dots.
  - A failed fetch keeping the last good history.
  - Missing git.
- **Navigation:**
  - every current destination reachable, task and resource alike;
  - checking capabilities kept listed;
  - the keyboard and focus paths;
  - a narrow layout.
- **Visual:**
  - headless captures of every screen in night and day;
  - the contrast tests;
  - the native WKWebView smoke (slice 12).
- **Parity:** the health 1c gate stays green. The line, the queue and the dependency-graph claims are covered by the fixtures above, not by the gate.

## 13. Non-goals

- Git hosting APIs (tokens, pull requests, issues, CI status). Git access is the local `git` binary only.
- Any write to a repo.
- New health semantics: the verdict engine is unchanged.
- Object contents in the index: metadata only.
- Changes to write-path guards.
- Phone or tablet layouts beyond "nothing breaks when narrow".

## 14. Open decisions for the owner

1. **The other themes.** Proposal:
   - Keep `light` and `dark` as "classic", flat through `var()` fallbacks.
   - Remove the six palettes.
   - Remove `bench` and `bench-light` one release after the migration.
2. **The track key.** Proposal: `tags.region`, falling back to `group` labelled as a group.
3. **The default comparison source.** Proposal: the key most stations share, with `line.source` as the override.
4. **The spine's trade-off.** Proposal: the fleet indicator (per-axis counts of clusters, with the list) replaces the per-cluster lamp column and the blink.
5. **Allowing a repo.** Proposal: Settings > Git writes `git.repos` in `fleet.yaml`, and nothing else ever adds to it.
6. **Fetch cadence and the Git budget.** Proposal:
   - Fetch when the fleet view opens, every 5 minutes while it is open, and on "Fetch now".
   - Keep the tracked refs' full commit and tree history.
   - Limits: 1 GiB per repo and 2 GiB in total, with one fetch at a time. Git 2.45 or newer is required.
7. **Comparison reference or rollout target.** Proposal: with Git history, the fetched tip T is the target, and the headline speaks of it; without Git, R is only a comparison and never a target.
8. **A fleet with several repos or refs.** Proposal: one selected comparison, with every other key shown as an exclusion count per station. The alternative is one line per key, stacked.
9. **Notification defaults.** Proposal:
   - Off until enabled. Once enabled, new episodes on protected clusters only; recoveries off.
   - Faults already present at connect are never notified, only listed once in the app.
10. **The observation log on disk.** Proposal: in memory only (the last 24 hours), with persistence to the config dir as an opt-in.
11. **The index's kinds and memory budget.** Proposal:
    - The §8.4 kinds.
    - 50,000 objects and 32 MiB per cluster; 200,000 objects and 128 MiB across the fleet.
    - Pods capped first.
    - Each configurable in `fleet.yaml`.
12. **Git previews and builds.** Proposal:
    - Reading an allowed repo never authorises remote Kustomize bases, plugins or decryption.
    - Remote bases can be enabled per repo, explicitly, and only from approved repos.
    - Cache builds are in-process and confined; the flux CLI is not used for them.
    - Decryption is never offered.
