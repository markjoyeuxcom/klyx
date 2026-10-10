# Klyx redesign: workspace, inspection and navigation contract

Date: 2026-10-10. Owner authorization: frontend redesign and rebuild, with owner veto. Claude implements; Codex reviews. Preserve the Go backend and migrate incrementally on the accepted remediation stack. This spec defines layout, navigation and presentation; the companion health-model spec owns classification, aggregation and freshness semantics. Where it supplies no verdict, presentation must show unknown rather than infer health.

## Outcomes and boundaries

- A lens switch changes the task and data, not the surrounding geometry.
- A selected object keeps its identity, inspector width and focus through live updates.
- From Fleet, the command palette can reach every supported cluster task without first visiting that cluster.
- An empty dataset has one meaningful state; a healthy unselected list has no idle inspector.
- Faults are discoverable without opening lenses. TriageTape is removed only after equivalent trustworthy coverage exists.
- Keep fleet-first navigation, tool-specific vocabulary, capability gating, both themes and existing operation safeguards. Do not add desired-state editors or chart installation.

All dimensions below are CSS px (desktop logical pixels), not screenshot bitmap pixels. All cited frontend paths are relative to `cmd/klyx/frontend/src`; citations describe the checkout at spec authoring, not immutable future locations.

## 1. Shell geometry

`AppShell` owns window frame, mast, fleet spine, navigation band and workspace. Replace the nested independent modules/gutters in `app/AppShell.tsx:45-55`. Preserve Wails drag/no-drag regions and native traffic-light clearance from `chrome/TopBar.tsx:12-14,43-46`.

| Token | Value | Contract |
| --- | ---: | --- |
| `--shell-inset` | 8px | Outer window inset on all sides |
| `--mast-height` | 36px | One app/context/utility band |
| `--shell-band-gap` | 4px | Mast-to-navigation and navigation-to-workspace gaps |
| `--spine-width` | 44px | Collapsed cluster channel |
| `--spine-expanded-width` | 160px | Expanded cluster names; persisted explicit toggle |
| `--spine-workspace-gap` | 8px | Horizontal gap; does not alter vertical budget |
| `--navigation-height` | 32px | One navigation row, never a second tier stacked beneath it |
| `--workspace-header-height` | 32px | Lens identity, total/attention, freshness and refresh |
| `--workspace-controls-height` | 28px | Namespace/status/search and optional filter menu |
| `--workspace-inline-padding` | 16px | Shared header/control/content left and right gutters |
| `--workspace-bottom-padding` | 8px | Bottom workspace inset |
| `--table-header-height` | 24px | Sticky column headings within the list |
| `--table-row-height` | 32px | Default one-line row; optional diagnostic row is 48px |
| `--workspace-footer-height` | 28px | Keyboard hints and visible-result count |
| `--split-gap` | 8px | Includes visible separator and its hit target |

The standard table first row begins at y=168px: 8+36+4+32+4+32+28+24. Do not insert additional top margins or meter rows. At 1524x1123, one-line table screens must meet this exact budget (allow 2px rendering tolerance). Necessary watch/error notices may add a named 32px notice slot below controls; it must contain actual degraded-state information, not a permanent slogan. Long notice text opens a readable detail view instead of clipping.

Mast: readable cluster context and protected-environment cue on the left; palette and app utilities on the right. Use flexible identity width rather than the fixed 180px allocation in `TopBar.tsx:41`. Context has priority over nonessential utility text; utility names remain accessible via tooltips and accessible labels. Never clip the production/protected cue. No separate global green freshness bar.

Spine header is a 32px-height button labelled Fleet and routes to Fleet. Remove duplicate Fleet from `LensNav.tsx:395-398`; replace the noninteractive caption in `FleetSpine.tsx:82`. Cluster names already have hover titles (`FleetSpine.tsx:88-90,147`); retain them, expose full names in the expanded 160px spine, and allow wrapping to two lines there. Collapsed codes are mnemonic shortcuts, not the only accessible identity. Same-cluster clicks preserve the active lens. Keyboard cluster switching keeps its existing shortcut contract.

Fleet uses the same navigation-band allocation for Fleet task tabs (Overview, existing comparison/utilization/operations views when available), not a blank spacer. App utilities have no cluster lens tabs. Fleet cards use a bounded grid: 360px minimum preferred card width, 480px maximum, 8px gaps, left aligned; below 360px available width a card uses 100%. One cluster never stretches a card across the whole desktop. This replaces the unbounded `1fr` grid in `fleet/FleetView.tsx:93`. Display ClusterMesh only when detection/data identifies a participating cluster; no empty patch-bay strip (`FleetView.tsx:67`). Categorical freshness uses a text/lamp indication, not a filled ratio bar. A quantitative bar requires a visible numerator, denominator and unit.

## 2. LensLayout slot contract

Proposed discriminated interface (names may follow repository conventions; responsibilities must remain):

```ts
type LensLayoutProps = CommonSlots & (
  | { variant: 'table-detail'; body: ReactNode; inspector?: ReactNode; selection: SelectionState }
  | { variant: 'canvas-detail'; body: ReactNode; inspector?: ReactNode; selection: SelectionState }
  | { variant: 'full-width'; body: ReactNode; inspector?: never }
);
// CommonSlots: identity, summary, freshness, controls, notice?, state, footer?
// state: initial-loading | ready | dataset-empty | no-match | unreadable | stale
```

Slots supply domain content and handlers. The template owns sizing, padding, scroll regions, resize/drawer mechanics and state placement. Callers cannot set outer height, grid columns, outer padding or inspector width. Table column definitions remain domain-owned under the rules below. Log windows/dialogs remain separate surfaces; they reuse text/spacing roles without being forced into a table API.

Header anatomy: task title (left), neutral total plus one health-model attention summary (adjacent), freshness/refresh (right). Counts carry entity scope; zero is neutral. Remove standalone meter strips and duplicate page titles from `cluster/GitOps.tsx:239-267`, `cluster/HelmView.tsx:144-155`, and analogous lenses. Additional facts belong in columns, inspector facts or an explicit optional header details popover. Ordering is a real sort control with a named current order, not engraved filler.

Controls: scope, status filter, search in that order, with 8px gaps. Search flexes between 160px and 280px; its neighbours remain associated rather than separated by a viewport-sized auto margin. At insufficient width, secondary filters move to a labelled Filters popover, preserving an active-filter count and clear-all action. Do not wrap controls into an unbounded second row. Minimum interactive target is 24px high inside the 28px band.

### Split and drawer algorithm

| Token | Value |
| --- | ---: |
| `--inspector-default-width` | 400px |
| `--inspector-min-width` | 360px |
| `--inspector-max-width` | 560px |
| `--table-body-min-width` | 600px |
| `--canvas-body-min-width` | 640px |
| `--detail-drawer-breakpoint` | 1040px workspace inner width |

Measure the workspace content width **after** spine and 16px inline padding, using ResizeObserver/container sizing. At inner width below 1040px, selection opens a drawer; otherwise split mode is allowed. For split mode, compute maximum width as `min(560, innerWidth - bodyMinimum - 8)`. Clamp the stored requested width to `[360, maximum]`; if maximum is below 360, use drawer. Store requested width separately from effective clamped width, so resizing the window does not destroy the user's preference.

Persist a single app-local preference `klyx.layout.v1.inspectorWidth` (number in CSS px) across all lenses and restarts, default 400; reject non-finite/out-of-range values. Persist spine expansion separately under the same versioned layout namespace. No per-lens width overrides. Double-click separator resets width to 400. Separator is keyboard focusable (`role=separator`, vertical orientation, min/max/current value); arrows change 16px, Home/End set bounds. Pointer resizing has an 8px hit target. Selecting a different object or loading details never changes width.

Drawer width is `min(560, max(360, requestedWidth), innerWidth)`, anchored to workspace right, full workspace height. At inner width below 360 it fills available width. It overlays rather than shrinks the body; a scrim blocks background input. Focus is contained while open, Escape closes the topmost nested surface first, then drawer; close returns focus to the originating row. Crossing the breakpoint preserves selected identity, detail tab, scroll and focused control where it remains valid. Drawer-to-split conversion removes modal focus trapping.

Both split regions scroll independently; sticky table header/footer and inspector identity remain visible. The workspace never relies on `overflow:hidden` to conceal over-wide content. Names receive flexible width (preferred minimum 240px); namespace may be a separate compact field. Collapse optional columns before name identity; node/version/revision columns may abbreviate with copy/full-value affordance. Remaining unavoidable column overflow is an explicit horizontal scrollbar within the table, not window overflow. Table header and rows share one column definition. This replaces divergent floors in `WorkloadsView.tsx:260`, `GitOps.tsx:296`, `CertsView.tsx:269`, `ExternalSecretsView.tsx:218`, `GatewayTopology.tsx:298` and Pods' independently sized panels (`PodsView.tsx:254,770-798`).

### Dataset states

| State | Template behaviour |
| --- | --- |
| Initial loading | Skeleton body; no success zero or idle inspector; header says loading |
| Dataset empty | One full-width state: scope, successful-read freshness, domain explanation and available next move; no inspector or meaningless inactive filters |
| No match | Keep controls and dataset total; show zero matching, Clear filters; hide inspector if selected object is excluded |
| Unreadable without data | Error/permission explanation with applicable retry; no fabricated healthy counters |
| Stale last-good data | Keep rows/selection with explicit last-good time and notice; do not auto-select from stale data |
| Ready | Render body and selected detail, or body alone |

Domain handlers supply available recovery actions. Fleet/Flux links are capability-aware; Helm empty-state does not offer installation. Replace permanent idle wells (`ResourceInspector.tsx:189-213`, `HelmView.tsx:242-247`) rather than shrinking their decorative placeholder.

## 3. InspectorLayout and selection

InspectorLayout takes identity, verdict, diagnosis, primaryAction, secondaryActions, tabs and ordered sections. It owns anatomy and typography. It does not compute health, permissions or operation confirmations.

1. Sticky identity header: kind, namespace, full resource name, copy, close. Name is 15px mono semibold and wraps with `overflow-wrap:anywhere`; never truncate the only full identity. Meta is 11px, wraps and supports copy (including IP). Padding 12px horizontal/8px vertical, minimum height 56px; it may grow for long identity. Actions do not displace the name.
2. Status/diagnosis: one dominant tool-native verdict from the health model, underlying phase secondarily; complete actionable failure and blocking object relationship. Errors wrap; no tooltip-only diagnostics. Pad 12px; sections separated by an 8px rhythm.
3. Action band: one explicit primary action, at most one frequently used secondary action, and More. Height 32px. Delete and overlapping shell/exec variants live in grouped More actions; confirmation/protected-cluster checks remain unchanged. No orphaned second-row buttons. Primary defaults: Pods Logs; Flux Reconcile; Argo Refresh (Sync remains safeguarded secondary); Helm History; Nodes inspect affected pods, with cordon/drain safeguarded secondary. Domain variants may choose another primary only when documented in the lens descriptor.
4. Tabs: Overview and Manifest (read-only YAML) plus a supported domain detail tab, at 28px height. Logs may open its existing dedicated surface. Do not offer two differently labelled YAML views for the same content.
5. Scroll body: immediate diagnosis/dependencies, runtime/containers or routing/issuance path, recent events, relevant facts, then metadata, successful conditions and inventory. Expanded by default: current failure, blocking dependency, containers, recent events. Collapsed by default: labels/annotations, successful conditions, full inventory. Show at most eight inventory entries in preview with explicit expansion; never claim unseen child health.

Header and action band stay fixed; variable diagnostic prose scrolls with body to prevent a long message consuming the entire viewport. Overview and Manifest remain discoverable. Sections have 12px padding, 8px content gaps, 11px section labels in sentence case, and explicit expansion controls. Events use readable wrapped text with reason, count and last occurrence; repeated occurrences are grouped without deleting access to the full message. These replace tooltip/ellipsis facts in `ResourceInspector.tsx:148`, event text in `GitOps.tsx:744`, headline in `GitOps.tsx:783`, and metadata-before-runtime Pod anatomy (`PodsView.tsx:832` onward). Keep inventory honesty/capping from `GitOps.tsx:580-587`.

### Selection state machine

- Selection key is cluster + destination + GVR + namespace + name. Keyboard row cursor and explicit inspector selection are distinct. Never key selection by sorted index.
- On route entry, wait for first usable **fresh** body snapshot. Restore a valid saved selection only if visible under current scope/filters. Otherwise auto-select the first visible broken row once, using the health model's actionable ordering. If there is none, render full-width body. No focus theft on auto-selection.
- Consume this auto-select opportunity even when the first fresh result is empty/healthy. Never reopen after Escape, or select a new failure on subsequent watches/filter changes. Explicitly navigating to this destination from another destination starts a new entry and clears the entry's closed-state; rerender, refresh, same-route clicks and scope/filter changes do not. Closed-state belongs to the active route entry, not a permanent preference across restarts.
- Refresh/reorder/status repair preserves selected identity and open inspector; healthy selected objects remain inspectable. Explicit selection clears closed-state. Switching cluster never carries selection across cluster boundaries.
- If a filter excludes selection, close inspector and move cursor to a valid visible row, without activating it; remember excluded identity for back-navigation, but do not reopen merely on clearing filters. If the selected object is deleted, show an Object removed state in the same inspector until close or explicit next selection; do not jump to another object. Last-good stale data retains its selected detail and provenance.
- Escape closes nested menus/dialogs first, then inspector; no operation runs through Escape or auto-selection. Search typing is never hijacked by list navigation. Opening Manifest/Logs preserves the parent row identity.

## 4. Navigation IA

Replace the rotated-sidebar model (`LensNav.tsx:13-17,264-285,395-439`) with one 32px contextual navigation band. Left: current lens-group selector (160px preferred width); its popover exposes the complete tree with capability gating. Right: current group's task destinations plus a Resources menu. No second stacked resource-kind row. If destinations exceed available width, retain active destination and priority neighbours; put remaining destinations in a labelled More menu. Never horizontally hide the active tab.

Task destinations have purpose-built workflow/inspection; resource destinations have generic GVR browsing, explicitly labelled Resources. Shared descriptors drive visible navigation, route restoration and palette commands. Resource-kind filters exist in exactly one place. Existing routes may adapt behind descriptors during migration; do not break saved navigation without a versioned route mapping.

| Group | Task destinations | Resources menu |
| --- | --- | --- |
| Cluster | Overview, Nodes, Events, Upgrade check | Namespaces, API Services |
| Workloads | All workloads, Pods | Deployments/StatefulSets/DaemonSets link to All workloads with that one kind filter; ReplicaSets, Jobs, CronJobs use existing GVR views |
| Config | None; group opens Resources | ConfigMaps, Secrets (masked), Quotas, LimitRanges, PriorityClasses, RuntimeClasses, Leases |
| Autoscaling | Karpenter overview when installed | HPAs, PDBs; capability-gated NodePools, NodeClaims, provider node classes |
| Network | Connectivity check, Gateway topology when installed, existing Hubble/routing views when supported | Services, Ingresses, NetworkPolicies, EndpointSlices, IngressClasses; capability-gated Gateway API/Cilium/Envoy kinds |
| Storage | None; group opens Resources | Existing volume/storage GVR kinds |
| Access | Access check | Read-only service accounts/RBAC/CSRs |
| Flux CD | Reconciliation, Sources | KS, HR and controller/source/notification kinds as explicitly generic raw views |
| Argo CD | Applications, Projects, ApplicationSets | Available Argo kinds through GVR |
| cert-manager | Certificates diagnosis | Certificates raw view, Requests, Orders, Challenges, Issuers, ClusterIssuers |
| External Secrets | Secrets diagnosis | ExternalSecrets raw view, SecretStores, ClusterSecretStores |
| Helm | Releases | No chart/install catalogue |
| API browser | CRDs by API group, Built-in resources | Discovered kinds/instances; preserves generic browse escape hatch |

Do not remove implemented specialist destinations omitted by the abbreviated table: register them under the closest group with explicit task/resource type and the same capability rules. No feature loss during regrouping. Network topology remains a task, never demoted into a raw CRD menu. Flux Reconciliation is the aggregate KS/HR view; its KS/HR kind filter is inside that view. Sources owns source-type filters. Generic Resources entries include a Raw resource view hint, preventing ambiguous equivalence. Replaces the Kustomizations-to-aggregate route in `LensNav.tsx:178-184` and duplicated aggregate type/source filters in `GitOps.tsx:267-287`.

### Fault discovery replaces TriageTape

Group selector shows current group's actionable count; the tree shows per-group counts from the health model. A mast Attention button shows cluster-scoped actionable summary and opens that same fault-aware tree, not a second ledger. Fleet spine uses the same per-cluster health result. The workspace header shows the current task's entity-scoped count. Do not sum parent workload and child Pod representations blindly. Loaded/unknown/stale states and timestamps follow the companion health spec.

Delete `AppShell.tsx:54` / `chrome/TriageTape.tsx` rendering only after known faults in every supported category are discoverable from this shared summary before lens entry. Current `LensNav.tsx:377-390` covers only selected loaded stores; it is not a sufficient replacement. Warning event type alone does not make an attention fault. Absent capability is omitted; detection pending is visibly checking rather than silently absent. No attention lamp may use absence of cached data as zero.

### App utilities

Mast utility area owns palette, Port-forwards, Activity, Terminal and Settings/theme menu. Port-forwards may show its live count and retain the emergency stop flow. Activity and Settings open app-wide full-width routes; Terminal launches its existing escape hatch. Remove app utilities from `LensNav.tsx:411-424`. Palette groups are Application, Clusters, Views, Resources, Objects and Actions; Port-forwards is never a Cluster.

### Palette from Fleet and every route

Static supported task descriptors are searchable independently of loaded object caches. Searching Events or All workloads from Fleet must produce a View result. With exactly one eligible connected cluster, result displays its full cluster name and activation explicitly routes there. With multiple eligible clusters, activation opens a second palette step Choose cluster; no silently inherited/default cluster. Back returns to query/results; Escape cancels without changing route. Zero eligible clusters shows the destination with a disabled explanation and Connect cluster action; unavailable capability is not fabricated as an eligible destination. Cluster-name-qualified queries may directly choose a cluster-specific result.

From a cluster, view navigation retains that cluster; switching cluster requires an explicit cluster result or chooser. Capability detection pending displays Checking capability rather than No matches. Navigation descriptors are distinct from cached object search: unloaded objects remain undiscoverable until loaded, but their **views** stay searchable. Existing destructive action descriptors retain target context, confirmations and production safeguards. Palette labels reserve minimum 200px before hints, wrap long titles to two lines, and never reduce a resource name to a lone initial. Selected result scrolls into view; focus returns to the origin on cancel.

Replace route-gated section construction in `chrome/commands.ts:111,165-184` and misgrouped app commands at `commands.ts:126-146`; retain focus-return behaviour in `CommandPalette.tsx:99-109`. Acceptance queries: Events, All workloads, Pods, Port-forwards, full long cluster name, and a long API kind, from Fleet, cluster and Settings.

## 5. Named type and spacing roles

No lens-local arbitrary font sizes/tracking for these roles. Use CSS classes/components backed by semantic tokens; existing themes provide colour values. Essential text has at least 4.5:1 contrast in both themes; nontext focus/selection affordances at least 3:1. Never use colour alone for severity; retain shape/word cues. Success may identify a healthy object, but numeric zero remains neutral. Allocatable/capacity is not saturation and cannot use warning thresholds; the health spec owns actual utilization warnings.

| Role | Size / line height | Weight / family | Usage |
| --- | --- | --- | --- |
| `text-workspace-title` | 15 / 20px | 600 / sans | Task and workspace identity |
| `text-object-title` | 15 / 20px | 600 / mono | Full selected resource name |
| `text-navigation` | 12 / 16px | 400; active 600 / sans | Group and destination labels |
| `text-control` | 12 / 16px | 400 / sans | Filters, scope, action labels |
| `text-body` | 13 / 18px | 400 / sans | Explanations, errors, empty states |
| `text-identifier` | 12 / 16px | 400 / mono | Table object names/revisions; selected 600 |
| `text-status` | 12 / 16px | 600 / sans | Verdict/actionable state |
| `text-secondary` | 11 / 16px | 400 / sans | Freshness, helper text, event age |
| `text-section-label` | 11 / 16px | 600 / sans | Inspector and table headings, sentence case |
| `text-number` | 13 / 18px | 600 / mono, tabular nums | Counts and exact measurements |
| `text-legend` | 9 / 12px | 500 / mono, tracking .06em | Nonessential equipment/shortcut legends only |

Spacing tokens: `space-1=4`, `space-2=8`, `space-3=12`, `space-4=16`, `space-5=24` px. Inline icon/text gap 4px; control/related-field gap 8px; inspector inset 12px; workspace inset 16px; empty-state explanatory blocks max-width 480px. Default icon 16px, lamp 8px. Border radius roles: small 4px, surface 6px. No decoration changes the geometry budget. Tooltip is supplementary, never the sole readable error/identity channel.

This replaces navigation engraving (`LensNav.tsx:284`), 9px tape labels (`TriageTape.tsx:93,148`), counter labels (`chrome/primitives/SummaryCell.tsx:26`), and 8px inspector captions (`ResourceInspector.tsx:127`). Keep numbers tabular; eliminate stranded sort slogans (`MetersStrip.tsx:20`). Current Signal Bench tokens are an identity reference, not an obligation to preserve permanent wells or glow without faults.

## 6. Incremental migration and review gates

1. Health-model contract first: establish shared observable summaries and task row verdicts. Keep existing tape until coverage/freshness parity is demonstrated. This spec consumes, rather than duplicates, that model.
2. Build LensLayout/InspectorLayout, roles and descriptor registry alongside legacy components. Mount one pilot (Pods) behind a reversible rollout switch. Exercise ErrImagePull, Unschedulable and CrashLoop fixtures with real long names. Map existing routes and preserve operation handlers.
3. Migrate Nodes and Workloads next. They stress widths, absent metrics, utilization semantics and selection/live reorder. Migrate generic resource/CRD views next so Resources destinations have the same template foundation.
4. Migrate Flux, Argo, cert-manager and ESO, preserving their native vocabulary and diagnostic relationship sections. Correct aggregate navigation during each migration; do not ship a renamed route over unchanged misleading contents.
5. Migrate Helm and remaining storage/config/access/upgrade task views; generic GVR-backed routes share the already migrated resource template. Empty Helm is a required gate, not an optional polish case.
6. Migrate Gateway/Network topology with canvas-detail, then existing logs/metrics/compare views with full-width or explicit canvas variant. Preserve graph interactions and capability detection.
7. Move Fleet and app utility routes to the shell/full-width frame, bounded cards and capability-aware Mesh. Enable fleet-route palette/descriptor navigation for all migrated views. Once all lenses are migrated and shared fault coverage passes, remove the old nav/tape/layout recipes. Delete dead primitives only after consumers are gone.

Each merge keeps the app runnable; pilot switch may select legacy or migrated **whole lens**, never mix two layout owners. New code uses templates immediately. No backend rewrite is authorized by this spec; schema additions needed by the health spec remain separate reviewed work. No silent loss of commands, operations, route persistence, watch-error visibility, metrics provenance or protected-cluster safeguards.

### Required checks per migrated lens

- Geometry and viewport matrix: 1280x900, 1524x1123, 1920x1080; both themes; expanded/collapsed spine; default and resized inspector. Verify workspace-inner widths 1039/1040 directly for drawer boundary. No window-level horizontal overflow or cut-off essential text.
- State matrix: loading, fresh empty, no-match, unreadable, stale last-good, populated healthy and populated faulted. No two empty states or green zero. Active scope and provenance remain visible.
- Interaction matrix: mouse selection; j/k cursor; Enter inspection; Escape close; resize keyboard/pointer; live reorder/repair/delete; filter exclusion; cluster switch; palette return. Auto-selection occurs once and never executes an action or steals typing focus.
- Real-data visual review: long Pod/Flux failure, long cluster/resource identity, missing node metrics, one-cluster fleet, no ClusterMesh, coexisting Flux/Argo. Full diagnosis accessible without native tooltips; node name beats unavailable measurement columns for width.
- Focus and semantics: named navigation landmarks, accessible resize values, drawer modal focus/return, visible focus, usable zoom. At 200% zoom use narrow mode/overflow controls, not clipped content. Respect reduced motion.
- Meaningful automated tests cover template geometry/state boundaries, selection transitions and descriptor/palette routing; existing domain tests continue. Screenshot review checks actual layout, not merely snapshot markup. Record commands/outcomes in each implementation PR; this document claims no implementation or runtime verification.

## Decision record

The live seeded-cluster review moved truthful shared health/status semantics ahead of layout. Layout is now the second-ranked implementation concern; navigation fourth, readability fifth; InspectorLayout implements the anatomy portion of investigation third. These are dependencies, not competing redesigns. The owner may veto direction; all geometry and descriptor changes remain reviewable in small runnable migrations.
