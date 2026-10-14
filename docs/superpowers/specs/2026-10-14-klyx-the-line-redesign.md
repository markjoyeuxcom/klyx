# Klyx redesign: The Line

**Owner decisions (2026-10-14):**
- The owner asked for a free-pass UI/UX redesign, reviewed five mockups, called them "completely awesome", and asked for a spec.
- **Mockups of record:** the "Klyx redesign" canvas (claude.ai artifact `WpwuEtxvLH547pikiSa8CS`, private to the owner). It holds five screens:
  - Fleet · The Line
  - Cluster · Flux diagnosis
  - Network · data path
  - Command console
  - Ask in day mode
- **What this replaces:** Signal Bench's visual language (direction C, 2026-07-05) and the chrome of the 2026-10-10 workspace redesign.
- **What it keeps:** the engineering principles, the health 1c model (one Observer per cluster, one cluster summary, the verdict engine), every honesty rule, and every write-path guard.
- **Git access (folded in after the first draft):** Klyx gains read-only Git access through the system `git` (§7).
  - With an allowed, fetched repo, the line can order revisions ("3 commits behind"), and the Ledger shows real commits.
  - Without one, the opaque-revision rules hold.

**The idea.** The fleet is drawn as a railway signal box: regions are parallel tracks, and environments are stations along them (dev, then stg, then prd). ClusterMesh peerings are links between tracks. The track is lit as far as the fleet's GitOps revision runs, and a red signal stands where a station is held. The same track motif carries every causal or positional relation in the app: the Flux cause chain, the Gateway data path, the chat read ledger, and a position glyph beside every object. A healthy object carries no mark at all.

## 0. What exists

- **Theme** (`src/theme/tokens.css`, `ThemeProvider.tsx`):
  - Semantic `--color-*` tokens across 11 themes.
  - `bench` and `bench-light` are the defaults, with a one-time migration, plus bench-only extras (`--bench-*`, `--lamp-*`).
  - Fonts are system stacks (`--font-sans`, `--font-mono`).
  - `contrast.test.ts` pins text contrast.
- **Chrome** (`AppShell.tsx`): `TopBar` (the mast), `FleetSpine` (the channel strip, the only place anything blinks), `NavBand` (group selector, tabs and nav tree), the content, then `ChatDrawer`. `MetersStrip` and `FKeysBar` are already gone.
- **Fleet:**
  - `FleetView` and `ClusterCard`.
  - `MeshStrip`, driven by `MeshGraphDTO` edges: mutual, one-way from the configuring side, or off-fleet.
  - `FleetPromotion`: GitOps units as rows, env-ranked clusters as columns. Revisions are opaque, and the dev-most column is the reference.
  - `FleetExpiry`.
- **Cluster identity:**
  - `ClusterConfig` has `environment`, `group`, `tags` and `protected` (effective).
  - `ClusterDTO` carries `env` and `group`.
  - `FleetSpine` ranks environments: dev, then test/qa/uat, then stg, then prd, then unknown.
- **Palette** (`commands.ts`):
  - Groups: Application, Clusters, Views, Resources, Objects, Actions.
  - Objects come only from the open cluster's loaded slices.
  - Fleet-wide object search is in the backlog, waiting on the metadata-informer work.
- **Flux detail DTO:** `source`, `dependsOn`, `appliedRevision`, `attemptedRevision`, `inventory`, and the bound source's health.
- **Gateway:**
  - Backend verdicts per HTTPRoute rule (v0.12.0).
  - `internal/routemetrics` holds inline Envoy rates when a metrics endpoint is configured.
- **Shared panel anatomy:** `LensTable`, `InspectorStatus`, `InspectorConditions`, `InspectorEvents` and `useResizableDock`.
- **No Git access.**
  - Klyx sees only the revision strings Flux and Argo report.
  - `internal/clibin` resolves the external CLIs (kubectl, helm, flux).
  - `internal/fluxcli` runs `flux diff kustomization --path`. The path defaults to `spec.path`, because Klyx has no local clone: the open M10-f follow-up.
- **Promotion snapshot:** `PromotionUnit.Revision` is a unit's applied revision, and `PromotionSource.Revision` is a source's fetched head.

## 1. Principles

1. **One sentence leads every screen**, then a drawing, then the evidence.
   - The sentence is Klyx's verdict in words: "Revision a1b9e3c runs on 5 of 6 stations; prd · North Europe is held."
   - The drawing is the line, a chain or a path.
   - The evidence is the conditions, events, facts and reads behind it.
2. **Healthy carries no mark.** There are exactly two status aspects, plus two in-between marks, and each is distinguished by shape as well as colour:
   - **fault:** a filled red square;
   - **look at:** a hollow amber square;
   - **checking:** a dashed grey hollow square, with "checking…" in the text;
   - **not yet realized:** a dashed hollow square in tertiary ink. This is the staleness language: `generation > observedGeneration`, or a revision attempted but not yet applied.
3. **One accent.** "Route" blue means reached, selected, focused, or a primary action. It never means status. Green is gone: `--color-text-success` maps to neutral ink, and success fills to transparent.
4. **Danger keeps its shape.** Destructive and protected verbs keep ▲ and the typed-confirmation interlock, unchanged.
5. **Nothing blinks.** Only overlays animate (console, drawer, dialogs): opacity plus an 8px translate, at most 200ms, eased out. Lists, rows, tabs and results never animate. Under `prefers-reduced-motion`, only opacity changes.
6. **The track is never decoration.** A line drawn on screen states a relation: position on the line, cause, data flow, or read order.
7. **Honesty rules carry over unchanged.**
   - A failed read is an error, never an empty result.
   - An unknown is "checking…".
   - Revisions are opaque strings unless an allowed repo's fetched history orders them (§7). Without that history, Klyx never says "behind" (§3.2).
   - Counts are exact or labelled as a lower bound.

**What this supersedes in the Signal Bench rules:**

| Signal Bench rule | Becomes |
|---|---|
| "Only faults glow" | "Only faults and the lit route carry colour; nothing glows." |
| "Blink is spine-only" | "Nothing blinks." |
| "Danger gets shape" | Stays. |
| "Wells hold data, modules hold chrome" | Three flat surfaces (ground, raised, sunk), each with a hairline border. No machining shadows. |

## 2. Visual system

### 2.1 Themes and tokens

**Two new themes become the defaults:** `night` (dark) and `day` (light).
- Both define the existing semantic tokens, so every lens restyles without per-component work.
- They also add `--line-*` tokens for the new primitives.

| Token | night | day | Role |
|---|---|---|---|
| `--line-ground` | `#0E1316` | `#F1F2EE` | the app ground |
| `--line-raised` | `#141A1E` | `#FAFAF7` | panels, inspector, drawer, station cards |
| `--line-sunk` | `#0A0E10` | `#E6E8E2` | list wells, inputs, the board |
| `--line-hair` | `rgba(203,216,222,.08)` | `rgba(20,30,36,.10)` | default border |
| `--line-hair-2` | `rgba(203,216,222,.15)` | `rgba(20,30,36,.18)` | emphasised border, control outline |
| `--line-ink-1` | `#E3E8E9` | `#131B20` | primary text |
| `--line-ink-2` | `#A6B1B6` | `#44515A` | secondary text |
| `--line-ink-3` | `#7F8C93` | `#5E6B73` | tertiary text, meta |
| `--line-ink-4` | `#56636A` | `#98A1A6` | disabled only |
| `--line-track` | `#2E3A40` | `#C3CAC5` | idle or unreached track |
| `--line-route` | `#79C2E4` | `#1C6A98` | lit route, selection, focus, primary action |
| `--line-route-ink` | `#0B2533` | `#FFFFFF` | text on a route fill |
| `--line-route-wash` | `rgba(121,194,228,.10)` | `rgba(28,106,152,.08)` | selected row |
| `--line-danger` | `#F0565B` | `#C42F35` | fault |
| `--line-danger-line` / `-wash` | `.55` / `.10` alpha | `.45` / `.07` alpha | fault border / fill |
| `--line-caution` | `#E9A23B` | `#9A5B00` | look at |
| `--line-caution-line` / `-wash` | `.50` / `.10` alpha | `.40` / `.08` alpha | look-at border / fill |
| `--line-lift` | 1px ring plus `0 24px 60px` black at .5 | 1px ring plus two soft layers | overlays only |

**Mapping onto the semantic tokens:**
- **Background:** `tertiary` = ground, `primary` = raised, `secondary` = one step above raised.
- **Text:** `primary`, `secondary` and `tertiary` = ink-1, ink-2 and ink-3.
- **Tone colours:** `info` = route, `danger` = danger, `warning` = caution.
- **`success` = ink-2,** with a transparent background.
- **Borders:** `tertiary` and `secondary` = hair and hair-2. The tone borders are the `-line` values.
- **Bench extras:** `--bench-well-bg` = sunk, with a 1px hair ring and no inset shadow. `--bench-module-shadow: none`. `--lamp-off` = track. The lamp glows are `none`.

**Contrast:**
- `contrast.test.ts` extends to both themes.
- ink-1 to ink-3, danger, caution and route as text must each reach 4.5:1 on ground, raised and sunk.
- route-ink on route must reach 4.5:1.
- ink-4 is exempt, because it is for disabled controls only.

**Migration:** runs once, keyed by a new flag.
- `bench` becomes `night` and `bench-light` becomes `day`.
- `system` follows the OS into night or day.
- Every other saved theme is left as it is.

The other themes' fate is an open decision (§13).

### 2.2 Type

| Use | Family | Weights |
|---|---|---|
| Interface text | **Barlow** | 400, 500, 600 |
| Station names, section labels, the wordmark | **Barlow Condensed** | 500, 600; uppercase, tracked 0.12-0.18em |
| Object names, namespaces in tables, revisions, numbers, keys, code | **DM Mono** | 400, 500; `tabular-nums` |

**Bundling:**
- All three are bundled through `@fontsource/barlow`, `@fontsource/barlow-condensed` and `@fontsource/dm-mono` (SIL OFL 1.1), as latin-subset woff2 only (about 160 KB).
- Klyx never loads a font from the network.
- New tokens: `--font-sans`, `--font-label` and `--font-mono`, each with a close system fallback.

**Scale** (ratio 1.2, base 14px):

| Size | Use |
|---|---|
| 11 | legend, caption |
| 12 | meta |
| 13 | dense table |
| 14 | body |
| 15 | lead |
| 17 | drawer title |
| 19-20 | inspector verdict |
| 22 | lens title |
| 28-30 | screen verdict |

**Weights:**
- 600 for verdicts and headings.
- 500 for names and the active item.
- 400 for body text.
- Headings over 22px use -0.01em tracking and `text-wrap: balance`. Body text uses `text-wrap: pretty`.

### 2.3 Space, radius, depth

- **Spacing:** a 4px base.
- **Rows:** 40px in tables.
- **Controls:** 36px visual height, with the hit area extended to at least 40px.
- **Padding:** 20-24px inside panels; 28-32px between sections.
- **Radius:** 4 (tags), 6 (controls), 8 (cards and stations), 10 (wells and panels), 12 (overlays).
- **Depth:** borders only. `--line-lift` belongs to overlays alone.

### 2.4 Primitives

- **`Aspect`** replaces `StatusDot`'s lamp physics.
  - Props: `kind: fault | attention | checking | syncing | none` and `size: 8 | 10 | 12`.
  - Role mapping from the verdict engine: fault → fault; degraded or attention → attention; checking or unknown → checking; not yet realized → syncing; ok or excluded → none.
  - It always pairs with text, or carries an `aria-label`.
- **`LinePos`**, the position glyph: three squares (the station's environment rank on its track) plus the region code.
  - The station's square is filled with ink-1, or with danger when the station has a fault.
  - Its accessible name is "prd · weu".
- **`Rail`:** a vertical track with step markers, used by the chain, the read ledger and every vertical drawing.
- **`Chain`:** `Rail` plus steps (kind, name, tag, sentence, evidence line), with segments drawn solid, dashed or red.

## 3. The line

### 3.1 Tracks and stations

- **A station is a cluster in `fleet.yaml`.**
  - Stations are ordered along a track by environment rank, using the `FleetSpine` ranking moved into one shared `env` module: dev, then test/qa/uat, then stg, then prd.
  - A track is a region:
    - `tags.region` when set;
    - else `group`;
    - else a single track named "Fleet".
- **Placement:**
  - A cell (track × rank) holds its stations side by side when more than one cluster shares it.
  - A cluster with no recognised environment goes on the **siding** (off the line, drawn below a hairline). So does a cluster that shares no line source (§3.2), for example the homelab.
- **Fallback:** with fewer than two ranks in the fleet, the board degrades to one row of stations with no lighting. Aspects and mesh still show.
- **Width:** the board sits in a sunk well and scrolls horizontally when narrow. The queue and the aside stack below it.

### 3.2 What lights the track

**Revisions are opaque, and Klyx does not order commits.** The line therefore makes only claims it has evidence for.

- **The line source:**
  - By default, the Flux GitRepository that the most stations share, matched by normalised URL (scheme and `.git` stripped, lowercased host).
  - Argo CD stations take part through an Application's `repoURL`.
  - `fleet.yaml` may set it explicitly as `line: { source: <url> }`.
  - With no source shared by two or more stations, nothing is lit, and the board says "No shared GitOps source: set line.source in fleet.yaml".
- **A station's revision:**
  - Its revision is the artifact revision of the line source on that cluster.
  - It counts as *applied* when every Flux unit bound to that source reports `lastAppliedRevision` equal to that revision. These are the promotion tracker's units.
- **The reference revision:**
  - It is the revision that runs on the most stations.
  - A tie is broken by the most recent artifact `lastUpdateTime`.
  - Its label always reads "runs on N of M stations".
- **Station states:**

| State | When | Drawn as |
|---|---|---|
| **reached** | the revision is the reference and is applied | the segment into the station is lit |
| **held** | the station differs from the reference (or is not applied), and the line source or a unit bound to it is failing | a red segment ending in a signal head; "held Nm", timed from the earliest failing condition's `lastTransitionTime` |
| **differs** | it differs, and nothing is failing (a pinned ref, a deliberate hold) | a dashed track labelled "differs". Never "behind". |
| **checking** | the source or its units are unread or unreadable | a dashed grey segment with "checking…" or the read error |

- **With Git history (§7).** This applies when the line source's repo is allowed and fetched, and every station revision is in that history.
  - **The reference** is the fetched tip of the ref the stations' sources track: a branch tip, a tag's commit, or a pinned commit. All stations must track the same ref. When they do not, the majority rule above stays, and ordering is shown pairwise only.
  - **The states refine:**

| State | When | Drawn as |
|---|---|---|
| **reached** | the station revision is the reference | lit |
| **behind by N** | an ancestor of the reference (N = `rev-list --count`), with nothing failing | a dashed track labelled "behind by N" |
| **held · N behind** | behind or not applied, and failing | red, with a signal head |
| **ahead by N** | the reference is an ancestor of the station revision | dashed, labelled |
| **diverged** | neither is an ancestor of the other | dashed, labelled "diverged at <merge-base>" |

  - A station revision missing from the fetched history stays "differs (not in fetched history)". It is never ordered.
  - The headline may then say "prd · North Europe is held, 3 commits behind".
- **The aspect** on a station card is that cluster summary's worst role (health 1c). It is independent of the lighting: converged is not the same as healthy.
- **The headline** is generated from the states. For example: "Revision a1b9e3c runs on 5 of 6 stations; prd · North Europe is held". When the cause chain (§5.1) knows the root cause, the subline names it.
- **New reads.** The promotion snapshot already has each unit's applied revision (`PromotionUnit.Revision`) and each source's fetched head (`PromotionSource.Revision`). `LineService` adds three reads on top of it:
  - the source's `spec.url`;
  - the source's `status.artifact.lastUpdateTime`, for the tie-break;
  - each unit's sourceRef namespace, since cross-namespace refs exist.

  Argo stations add `spec.source.repoURL` and `status.sync.revision`.

### 3.3 Mesh on the board

- Mesh links come from `MeshGraphDTO`.
- **Same rank, adjacent tracks:** a mutual peering draws a solid track link between the two stations. A one-way peering is dashed in caution, with "one-way" and the configuring side in its title.
- **Any other pair** is listed on the station card ("mesh ↔ prd · neu") and is not drawn.
- **Off-fleet peers** are listed under the board.

### 3.4 The fleet screen

From top to bottom:
- **Verdict sentence** and legend.
- **The board.**
- **"Needs you":** the queue.
  - Its rows are attention rows from every connected cluster's summary, ordered by:
    1. environment rank, prd first;
    2. fault before look at;
    3. root causes first, with their downstream rows folded under them ("holds infra-controllers and apps · 14 workloads keep 9f2c11d").
  - A row shows: aspect, kind, name, the verdict's reason, a `LinePos` station tag, age, and a link into the lens with the object selected.
  - Filters are All, Prd only and Faults, kept for the session.
- **The aside:**
  - **Ledger.**
    - **Without Git history:** "revisions on the line now". Each distinct station revision is shown with its per-track dots (lit, held or unread) and its artifact time.
    - **With history (§7):** the commits from the oldest station revision to the reference, newest first, at most 20.
      - Each shows its short sha, subject, author and commit time.
      - Each carries per-track dots for which stations contain it, meaning the station's revision descends from the commit.
    - **"Open in Git"** links to the commit. It is built for known hosts (GitHub, GitLab, Azure DevOps), or from `git.repos[].web`.
  - **Horizon:** the next 30 days from `FleetExpiry`.
- **Footer rail:** clusters watched, the age of the last change seen, and the AI provider.

### 3.5 The station strip

Every cluster screen's location bar draws the cluster's own track: dev — stg — prd, with this station boxed and the segment into it in its state.

## 4. Chrome and navigation

- **The location bar** replaces `TopBar` and `NavBand`'s group selector:
  - Contents: mark, then `Fleet / Station / Lens / Object`, then the station strip, the ⌘K well, Ask, and settings.
  - History (mod+[ and mod+]) stays. The recent-places menu moves into the console's empty state.
- **`FleetSpine` is removed.** Its pager role, noticing faults on other clusters while inside one, moves to:
  - the location bar's `Fleet` crumb: a fault aspect plus "N faults elsewhere";
  - the console's empty state ("Faults across the fleet");
  - the existing dock badge and OS notifications.
- **The lens index** replaces the `NavBand` tree and tabs:
  - **Width:** 232px, collapsible with mod+\.
  - **"Needs you":** the lenses with fault or look-at rows, ordered by severity, then count.
  - **"Everything else":** the remaining capability-gated lenses in registry order, shown quietly.
  - A missing capability is not listed. One quiet line names what is not installed.
  - **Data:** derived from `nav/registry.ts` and `nav/clusterAttention.ts`.
  - **Keys:** mod+1..9 keep their current targets.
- **The inspector** keeps its right slot (`useResizableDock`) and takes one anatomy:
  - kind, name and meta;
  - the verdict sentence with its `Aspect`;
  - the drawing, when the kind has one (§5);
  - actions;
  - evidence (`InspectorConditions`, `InspectorEvents`, facts).
- **The station page:** the open cluster's Overview becomes:
  - the station's verdict sentence;
  - its "Needs you" rows (the fleet queue's component, scoped to this cluster);
  - capabilities, versions and connection notes.
- **The chat drawer** keeps its slot, restyled (§5.4).

## 5. Drawings

### 5.1 The Flux cause chain

- **Built for** a selected Kustomization or HelmRelease.
- **The walk:**
  - It follows `dependsOn` upstream to a depth of 6. Cycles are cut and labelled.
  - Each unit's bound source is added.
  - Then come this object, and its inventory summary (workload count; "not yet realized" when attempted ≠ applied).
- **Order:** the root comes first.
- **The "Root cause" tag** goes on the deepest failing step whose upstream Klyx read in full. If an upstream step could not be read, the chain ends in a dashed grey stub ("could not read: …") and no step is tagged.
- **Evidence lines:** the source fetch error, conditions, and controller events.
- **"Where the fix lives"** appears only when typed facts name the owner. For example: the source's credential Secret is managed by an ExternalSecret (the existing cross-link) gives "A credential, not a manifest". It is never free text.
- **With Git history (§7),** a held step gains "not here yet":
  - the commits between the station's applied revision and the reference that touch the unit's `spec.path` (`git log -- <path>`);
  - the changed files, as name-status from trees, with no file contents.
- **Out of scope:** change-time correlation, such as the mockup's "Secret changed 19m before the first 401". It needs change history that Klyx does not keep.
- **Reuse:** cert-manager's issuance chain (M11-c) and ESO's store → ExternalSecret → Secret use the same `Chain`.

### 5.2 The Gateway data path

- **The drawing:** Gateway (listener, hostnames, attached policies), then HTTPRoute, then one branch per rule. Each rule has a weight bar per backend (an unset weight counts as 1), then the Service with its ready endpoints.
- **Built from** the per-rule backend verdicts. A failing backend's branch is red.
- **The inline 503 rate and sparkline** appear only with a configured metrics endpoint (`routemetrics`). They are never estimated.
- **Below:** the rules and policies table, and the mesh note for global Services.

### 5.3 The board

Specified in §3.

### 5.4 The chat read ledger

- Reads are drawn as a `Rail`: completed in route, failed in danger, cancelled dashed. A read's redaction count stays on its line.
- Chips stay as in v0.13.1 (kind-labelled, with "+N more").
- The guarantee line and the provider line keep their current copy.

## 6. The command console

- **The overlay:** ⌘K, 760px wide, 88px from the top.
- **The empty query** shows:
  1. Recent places;
  2. "Faults across the fleet", from every summary;
  3. the verbs for the current selection.
- **Groups:** Objects, Run, Go to, Ask.
- **Object scope, stated in the UI:** "open cluster, plus anything that needs attention fleet-wide".
  - That is the open cluster's loaded slices (as today) plus the attention rows of every connected cluster's summary.
  - A full fleet-wide object index stays in the backlog (metadata informers).
- **A row:** `Aspect`, kind, name, `LinePos`, and a note.
  - Protected verbs show ▲ and "type the cluster name". The grant flow is unchanged.
- **Keys:**
  - ↑↓ move.
  - ↵ opens.
  - ⌘↵ opens beside, in the inspector, keeping the list.
  - ⇥ shows verbs only.
  - esc closes.
- Results never animate.

## 7. Git access (read-only, through the system git)

**Why.** History lets Klyx:
- order revisions on the line ("3 commits behind");
- show commit subjects and authors in the Ledger;
- show a held station what it does not have yet;
- link "Open in Git" to the commit;
- give `flux diff --path` a local clone root, closing the M10-f follow-up.

**The binary.**
- Klyx runs the system `git`, resolved through `internal/clibin` like kubectl, helm and flux.
- It uses no hosting API and stores no tokens. Authentication is the owner's own: the SSH agent, `~/.ssh/config`, or Git's credential helper.
- If git is missing, every Git feature is off, with one note ("Git not found: install git to order revisions"), and the line keeps its opaque rules.

**What it may run.** Klyx builds every argv itself, and only these subcommands are allowed:

| Command | Purpose |
|---|---|
| `git clone --bare --filter=blob:none --no-tags --template= <url> <cache>` | once per allowed repo |
| `git fetch --prune --filter=blob:none origin <tracked refs>` | refresh |
| `git log`, `git rev-list --count`, `git merge-base [--is-ancestor]`, `git diff --name-status` | trees and commits only, no file contents |
| `git diff <a> <b> -- <path>` | only on an explicit "show changes" click. It fetches the blobs it needs, within the bounds below. |
| `git archive <rev> -- <path>` | into a temp dir, for `flux diff --path` |

Never allowed: `push`, `commit`, `tag`, `branch`, a checkout into the owner's working trees, `config --global`, hooks or submodules.

**Hardening on every call.**
- **Config flags:**
  - `-c core.hooksPath=/dev/null`, and the clone's empty `--template=` leaves no sample hooks;
  - `-c protocol.allow=never -c protocol.https.allow=always -c protocol.ssh.allow=always`: no `file`, no `ext`, no local paths;
  - `-c core.fsmonitor=false` and `-c submodule.recurse=false`.
- **Environment:**
  - It is the owner's environment, minus what other shell-outs already drop (KUBECONFIG and cloud credentials).
  - Plus `GIT_TERMINAL_PROMPT=0`, and `GIT_SSH_COMMAND` adding `-o BatchMode=yes` to the owner's ssh.
  - Klyx never shows or waits on a prompt, so a passphrase-protected key works only through the agent.
- **Arguments:**
  - Revisions must be hex shas (`[0-9a-f]{7,64}`) or the exact ref a source tracks, placed after `--end-of-options`.
  - Paths come after `--`.
  - Nothing read from a cluster object ever becomes an option.
- **Deadlines:** 60s for clone and fetch, 10s for local queries. The process group is killed on a deadline or a cancel, and output is capped.

**Which repos.** Repo URLs come from cluster objects, which Klyx treats as untrusted. A mistaken or malicious GitRepository could point Klyx's fetch, and with it the owner's credentials, at any host. So Klyx fetches only repos the owner has allowed:
- **Listed in `fleet.yaml`** under `git.repos`. Each entry has a `url`, plus an optional `web` for commit links on hosts Klyx does not know.
- **Or allowed once in Settings > Git,** which writes the same list, so there is one source of truth. The allow dialog shows the exact URL and which clusters reference it.
- **Matching:** URLs are compared normalised (scheme, user, `.git`, and host case).
- **A source whose URL is not allowed** shows "Allow <url> in Settings > Git to read its history" and keeps the opaque rules.
- **Never automatic:** a new URL seen on a cluster never triggers a fetch.

**The cache.**
- **Location:** `<config dir>/git-cache/<sha256 of the normalised URL>.git`, beside `fleet.yaml`, mode 0700.
- **Contents:** only the refs the line sources track (a branch, a tag, or the pinned commit), with full commit and tree history and no file contents.
- **Settings > Git** shows each repo's size and last fetch, with "Fetch now" and "Clear cache".
- **Size limit:** a cache over 1 GiB stops fetching, and says so.

**When it fetches.**
- When the fleet view opens.
- At most every 5 minutes while the fleet view stays open.
- On "Fetch now".
- One fetch per repo at a time.

**Failing honestly.**
- A failed fetch keeps the last good history, labelled "history as of <time>; fetch failed: <scrubbed error>".
- With no good history, the line falls back to the opaque rules, with that note.
- A revision that is not in the fetched history is never ordered.

**Untrusted text.**
- Commit subjects, author names and file paths are plain text, never markdown or HTML.
- Caps: a subject at 200 characters; at most 20 commits and 200 paths per view.
- This spec never sends them to an AI provider.

## 8. Applying it to every lens

- **Lenses keep** their data, verdict logic and keyboard paths.
- **Each lens takes:**
  - the header: lens title plus a count sentence ("2 faults · 1 held · 1 syncing · 7 ready");
  - `LensTable` rows: 40px, an `Aspect` column, quiet healthy rows, dashed not-yet-realized marks;
  - the §4 inspector anatomy.
- **Retired:** the remaining Signal Bench primitives, meaning lamp glow, module and well machining, and the pressed-filter styling. Filters become segmented controls on sunk wells.

## 9. Accessibility

- **Never colour alone:** every `Aspect` has a text equivalent, and fill versus outline separates fault from look at.
- **Focus:** a 2px route ring with a 2px offset on every control.
- **The board:**
  - Each station is a link named, for example, "prd · North Europe, aks-prd-neu, held at 9f2c11d, 2 faults".
  - Segments are `aria-hidden`.
  - A visually hidden list mirrors the board.
- **Tables** keep their roles. Icon-only buttons carry `aria-label`.
- **Reduced motion:** opacity only.

## 10. Slices (each Codex-reviewed)

1. **Tokens and type.**
   - The `night` and `day` themes and the semantic mapping, including success → neutral.
   - Bundled fonts.
   - Contrast tests.
   - The migration.
   - `Aspect` replaces lamp physics: no glow, no blink.
   - No layout changes.
2. **Chrome.**
   - The location bar with the station strip, replacing `TopBar`.
   - The lens index, replacing `NavBand`.
   - `FleetSpine` removed, with its pager role moved (§4).
   - The inspector anatomy.
   - History and recents in the console.
3. **The line.**
   - Backend `LineService`: tracks, stations, the line source, the reference revision, station states, mesh and ledger, with honesty tests.
   - The fleet screen: board, queue, ledger, horizon.
4. **Git access** (§7).
   - The `gitcli` package: allowlisted subcommands, hardening, deadlines.
   - `git.repos` in `fleet.yaml`, and Settings > Git (allow, fetch now, sizes, clear).
   - The cache.
   - Ordering states on the line, and the Ledger's commits.
   - "Not here yet" in the chain.
   - **4b (may follow later):** `flux diff --path` from `git archive` of the cache (M10-f).
5. **Drawings.**
   - `Rail` and `Chain`, used for Flux, cert-manager and ESO.
   - The Gateway data path.
   - The chat ledger rail.
   - `LinePos`.
6. **Console.** The empty state, the groups, the fleet attention objects, open beside, `LinePos`.
7. **Lens sweep.**
   - Every lens onto the header, `LensTable` and the inspector anatomy.
   - Retire the remaining bench primitives.
   - Headless captures of every lens in both themes.
   - The parity gate re-run.
8. **Smoke** on the kind lab (the parity fixture), in both themes, with an eyeball checklist.

## 11. Testing

- **Line semantics (Go, table tests):**
  - placement: rank and region, collisions, a missing environment going to the siding, the single-row fallback;
  - the line source: most-shared URL normalisation, the explicit override, none shared;
  - the reference revision: majority, the tie broken by artifact time, opacity (no ordering);
  - station states: reached, held (a failing source; a failing bound unit), differs (no failure), checking (an unread or failed read);
  - "held Nm" timing;
  - the mesh drawing rules.
- **Git (Go, against local bare repos built in the test):**
  - The allowlist: an unlisted URL never fetches, and a URL normalising to a listed one does.
  - The protocol allowlist: `file://`, `ext::` and local paths are refused.
  - No hooks run. No prompt appears: a key that needs a passphrase fails fast.
  - Revision and path validation: an option-looking value is refused.
  - Deadlines and caps.
  - Ordering: behind, ahead, diverged, and a revision not in the history.
  - The Ledger's contains-dots.
  - A failed fetch keeps the last good history.
  - Missing git falls back with the note.
- **The chain:**
  - the depth cap;
  - a `dependsOn` cycle;
  - an unreadable step (no root-cause tag);
  - the root-cause rule;
  - "Where the fix lives" appearing only from typed facts.
- **The console:**
  - the scope label;
  - the fleet attention rows;
  - protected verbs keeping the grant flow.
- **Visual:**
  - headless captures (the existing `pw` scripts) of every screen in night and day;
  - contrast tests on both themes.
- **Parity:** the health 1c gate stays green. The redesign changes presentation, not summary semantics.

## 12. Non-goals

- Git hosting APIs (tokens, pull requests, issues, CI status). Git access is the local `git` binary only (§7).
- Any write to a repo: no push, commit, tag or branch.
- New health semantics: the verdict engine is unchanged.
- A fleet-wide object index (backlog).
- Changes to write-path guards, grants or typed confirmation.
- Phone or tablet layouts beyond "nothing breaks when narrow".

## 13. Open decisions for the owner

1. **The other themes.**
   - Proposal: keep `light` and `dark` as "classic", flat via `var()` fallbacks.
   - Remove the six palettes (`midnight`, `graphite`, `crimson`, `forest`, `amber`, `violet`).
   - Remove `bench` and `bench-light` one release after the migration.
2. **The region key.** Proposal: `tags.region`, falling back to `group`.
3. **The default line source.** Proposal: the most-shared GitRepository URL, with `line.source` as the override, rather than explicit configuration only.
4. **The spine's pager role.** Proposal: the `Fleet` crumb's "N faults elsewhere" plus the console's empty state. This gives up the per-cluster lamp column.
5. **Allowing a repo.** Proposal: Settings > Git writes `git.repos` in `fleet.yaml` (one list), and nothing else ever adds to it.
6. **Fetch cadence and history.** Proposal: fetch when the fleet view opens, every 5 minutes while it is open, and on "Fetch now"; keep the full commit and tree history of the tracked refs only, with a 1 GiB limit.
