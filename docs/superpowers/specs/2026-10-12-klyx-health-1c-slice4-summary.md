# Klyx health 1c, slice 4: the open cluster's mast, tree and Overview on the summary

Status: draft for Codex review (2026-10-12). Parent spec: `2026-10-11-klyx-health-1c-cluster-summary.md` (§8 Frontend, §9 slice 4). Slice 3 (`2026-10-11-klyx-health-1c-slice3-rows.md`) is closed lens by lens; its §4a count selector "lands now, the mast and tree are wired to it in slice 4".

## 0. Outcome and limits

The open cluster's three attention surfaces - the mast Attention button, the navigation tree's badges and notes, and Overview's Attention section - read the cluster's Observer summary instead of the per-entry fetch fan-out (`bridge/attention.ts`: seven list endpoints, then a 60s re-poll). Every count they show comes from the one count selector, `taskCounts(cluster, task, part)`, so a lens header, its tree badge and the mast agree at one `Ver`, and switch together under A4.

Not in this slice:

- the fleet surfaces (spine lamps, fleet meters, the fleet board's per-visit fan-out) - slice 5;
- removing "not covered" - slice 6's parity gate is its deletion condition (§6 below keeps the mechanism, summary-derived).

## 1. Source

The cluster's current summary in the health store (`useHealth.summaries[cluster]`), which the app already receives (`startSummaries`: subscribe-before-bootstrap, newest-wins by (incarnation, seq), Final and tombstones per the parent §3.4/§4).

- No summary yet for the cluster: every area is "checking…" (today's loading state).
- A Final summary (the cluster's Observer ended - disconnected or removed): the areas are not shown as current. The surfaces say the summary ended and when; nothing reads as quiet.
- A newer incarnation replaces the old one whole; no area keeps a value from the earlier incarnation.

## 2. Areas: one per task

Each summary Task is one area. The nav registry maps it to a group and destination, extending today's seven areas with the four domains the fetch fan-out never read:

| task | group / destination | parts |
|---|---|---|
| workloads | workloads / workloads | workloads (verdict) |
| pods | workloads / pods | pods (verdict) |
| nodes | cluster / nodes | nodes (verdict) |
| events | cluster / events | warnings (evidence) |
| helm | helm / helm | releases (native) |
| flux | flux / flux | reconciliations, sources (native) |
| argo | argo / argo | applications, applicationsets (native) |
| certs | certs / certs | certificates, issuers (native) |
| eso | eso / eso | externalsecrets, stores (native) |
| gateway | network / gateway | gateways, routes (native) |
| karpenter | autoscaling / karpenter | nodepools, nodeclaims (native) |

A task whose parts are all absent (the tool is not installed) is omitted, as "absent" areas are today.

### 2.1 Part lines

Each part is one wording line of its area, from its classifier:

- **verdict** (`Counts`): attention = fault + degraded, tone danger with any fault, else warning; unknown is "unverified" (a gap, never quiet); the line reuses the verdict vocabulary ("3 need attention · 1 unverified").
- **native** (`Native`): `included` objects in the tool's own word (`Native.Word`, for example "not ready", "failing"), tone from the role partition (danger with any fault role); progressing and paused never light.
- **evidence** (`Evidence`, Events warnings): a count with no tone ("12 warning events"); never attention.

### 2.2 Area value

An area is lit when any of its lines is lit. Its badge is its strongest line - danger before warning, then the first counted part - never a sum across parts (two parts are two representations or two vocabularies, as the tree does today for Flux and its sources). A part also badges its own destination only where a lens destination for exactly that part exists: Flux sources (`flux-sources`) and Argo ApplicationSets (`argo-appsets`). The other second parts (Certificates issuers, External secrets stores, Gateways routes, Karpenter NodeClaims) have no lens destination of their own - their registry entries are generic resource destinations whose kinds differ from the part's (Issuers without ClusterIssuers, for example) - so they show as a line on their task's destination (its note and tooltip), never as a badge on a resource destination.

The mast counts AREAS needing attention, never objects (unchanged).

## 3. Counts: the one selector

Every number comes from `taskCounts(cluster, task, part)` (slice 3 §4a):

- a part whose rows a lens holds and that are at least as new as the summary: the rows' counts;
- under A4 (the summary ahead past the deadline): the summary's counts, on every surface together;
- otherwise the summary's.

The tree's ad hoc overrides go: the Pods badge "live lens wins" rule (e5288d2) and the Flux sources count read from the Flux lens become ordinary parts read through the selector.

## 4. Coverage and freshness wording

From the parent §5, per part, never reduced to loaded/error booleans:

- live: the count as read;
- stale: the count is the last good one - "last good HH:MM" beside it, and the area is a gap;
- checking: "checking…", a gap, no count;
- unreadable: "could not be read (class)", a gap, no count;
- absent: the part's line is omitted;
- a discovery gap (`KindCoverage.Discovery`) is named beside the live count, as a capability gap, never as staleness;
- a truncated snapshot (Events): the count is a lower bound ("12+ warning events");
- snapshot tasks (Helm, Events) show their read time (`AsOf`), as their lenses do.

A controller issue (`Task.Controller`) lights its area with the controller's state and reason and no object count, and is never added to a part count.

The mast's quiet state stays qualified: no unqualified all-clear (§6).

## 5. Surfaces

- **Mast** (`TopBar` AttentionButton): the area count and gaps from §2-§4; the tooltip lists each area's lines and the summary's evaluation time; "refreshing" while an A4 deadline has passed for an open part.
- **Tree** (`useNavCounts`, `NavBand`): group and destination badges from §2.2, group and destination notes from §4 ("unreadable", "last good", "unverified", "checking…"). Badge titles name their source: "cluster summary as of HH:MM:SS" or "Pods lens rows".
- **Overview** (`AttentionSection`): one row per area with its lines; each lit line lists up to three `Top` refs as direct links (the ref's `Route` destination with the existing select intents - select-pod, select-workload, select-node, select-flux and so on; a kind without an intent opens its lens). The namespace count comes from `Summary.Namespaces` (null when unknown).

## 6. "Not covered" until slice 6

The static `UNCOVERED` list goes: every former entry is now a summary task. The "not covered" mechanism stays, derived from the summary - an installed capability (capability detection says present) whose task is missing from the summary is named "not covered by the summary - open to check", exactly as today. Slice 6's parity gate removes the mechanism and the qualification of the quiet state.

Question for review: is the summary-derived mechanism acceptable as the slice 4 transitional state, or must the four domains keep their static "not covered" note until slice 6 even though their tasks are summarized?

## 7. Removals

- `bridge/attention.ts` (`fetchAttention`, the 60s re-poll, `abandonAttention`), `useClusterAttention`, the store's `AttentionSlice` and its setters.
- The tree's lens-specific overrides (§3).
- The `bridge/overview.ts` helpers that only fed `fetchAttention` (`unwrapSources`, `summariseSources`, `observations` - kept only where the fleet board still uses them until slice 5).
- Request methods (`ListPods`, `ListNodes`, `ListEvents` and so on) stay while `bridge/fleetboard.ts` calls them; those go in slice 5.

## 8. Tests

- Area mapping per classifier (verdict, native, evidence) and per coverage state (live, stale with last good, checking, unreadable with class, absent omitted, discovery gap named, truncated lower bound, snapshot as-of).
- A controller issue lights its area with no object count.
- Strongest-line badges: danger over warning; never a sum across parts; per-part badges only on `flux-sources` and `argo-appsets`, and no part badge on a generic resource destination.
- One number: with a lens open, the lens header, tree badge and mast agree; rows newer than the summary win on all three; under A4 all three switch to the summary together and back after the re-bootstrap.
- No summary, a Final summary, and a newer incarnation (no carry-over).
- Top ref links route through the existing intents.
- The summary-derived "not covered": a present capability with no task is named; a summarized task is not.
- No fetch fan-out remains (no `ListWorkloads`/`ListPods`/... call on cluster entry).
- Mounted tests on the real health store (the slice 3 pattern), plus mutation checks.

## 9. Order of work

One reviewed commit each:

1. The area model from the summary, with the mast and the tree.
2. Overview's Attention section (lines, Top refs, namespaces).
3. The removals (§7).
