# Klyx drill-in navigation design (chrome + cluster detail)

Date: 2026-06-04
Status: approved design, ready for plan
Scope: a frontend-only slice adding the app chrome (sidebar/header/breadcrumb),
a styled theme toggle, clickable cards that drill into a per-cluster detail shell
(real Overview + honest placeholders), built on the B-1 Wails fleet app. No Go /
appbridge changes. Reference: `docs/design/mockups-2026-06-04.html` (chrome appears across all
mockups; breadcrumbs in mockups 2/3/5).

## 1. Context and goal

B-1 shipped a native Wails app rendering a live cluster-card grid, but with no
chrome and no way to drill into a cluster. This slice adds the sidebar + header +
breadcrumb, a proper theme toggle, and the click-into-a-cluster interaction with
a real Overview page and honest placeholders for the not-yet-built sections.

Decisions taken in brainstorming:
- Scope = chrome + drill-in only. Full card fidelity, filter pills, and search
  stay in a separate fleet-polish slice; the command palette is B-3.
- Navigation model = **fleet-root, cluster-scoped sections**: Fleet is the root
  grid; clicking a card enters that cluster's scope; the sidebar sections render
  that cluster's views; breadcrumb `Fleet > <cluster> > <section>`. Matches the
  mockups and principle 1.
- Frontend-only: the cluster Overview reuses the existing `ClusterDTO`; no new
  data crosses the bridge. Richer per-cluster detail is a later DTO extension.

## 2. Routing model (Zustand store, no router dep)

```ts
type ClusterSection = "overview" | "gitops" | "network" | "resources" | "observability";
type Route =
  | { name: "fleet" }
  | { name: "cluster"; cluster: string; section: ClusterSection };
```

The store gains `route: Route` (default `{ name: "fleet" }`) and actions:
- `openFleet()` -> `{ name: "fleet" }`
- `openCluster(name)` -> `{ name: "cluster", cluster: name, section: "overview" }`
- `setSection(s)` -> updates the section (only meaningful in cluster scope)

The existing `clusters: ClusterDTO[]` + `setClusters` are unchanged. `ClusterDetail`
resolves the selected cluster's DTO by name via a selector, so it stays live as
the 1s `fleet:updated` push keeps updating the store while drilled in.

## 3. Component structure

```
src/app/AppShell.tsx          # sidebar + header + main; renders by route
src/chrome/Sidebar.tsx        # 46px icon rail: Fleet + cluster-scoped section icons
src/chrome/Header.tsx         # breadcrumb + section title + ThemeToggle (+ count chip on fleet root)
src/chrome/Breadcrumb.tsx     # derived from route
src/chrome/ThemeToggle.tsx    # styled sun/moon icon button (replaces the bare button)
src/chrome/Placeholder.tsx    # honest, capability-aware empty state
src/cluster/ClusterDetail.tsx # renders the selected cluster's active section
src/cluster/Overview.tsx      # the real section: summary from the DTO
```
- `App.tsx` shrinks to `<AppShell/>` (keeps the bridge-init `useEffect`).
- `ClusterCard` gains `onClick -> openCluster(c.name)`, a pointer cursor, and a
  subtle hover (token-based) so it reads as clickable.
- `FleetView` is rendered by `AppShell` when `route.name === "fleet"`.

## 4. Chrome (to the mockup)

**Sidebar** - 46px rail, secondary bg, 0.5px right border. Top: the inverted "K"
logo square. Icon buttons (Tabler outline):
- `layout-grid` -> Fleet (always enabled -> `openFleet()`)
- `layout-dashboard` -> Overview, `stack-2` -> Resources, `git-branch` -> GitOps,
  `route` -> Network, `chart-line` -> Observability: **cluster-scoped** -
  enabled/selectable only when a cluster is open (click -> `setSection`); at the
  fleet root they render visibly muted/disabled (no fake navigation). The Overview
  icon is the explicit cluster-root destination: `openCluster` lands on it, and it
  carries the active highlight while on the Overview section.
- bottom group (`terminal-2`, `settings`): inert, for chrome completeness.
- Active state: the current section icon (including Overview) gets the boxed
  highlight (primary bg + 0.5px border); others `text-secondary`.

**Header** - two rows:
- Breadcrumb (`Breadcrumb.tsx`): fleet root -> `Fleet` + count chip
  (`N clusters - M regions`, derived from tags). Cluster scope ->
  `Fleet > <cluster> > <Section>` with chevrons, cluster name in monospace;
  `Fleet` and `<cluster>` segments are clickable (-> grid / -> Overview).
- Title + actions: the section title, and the `ThemeToggle` on the right.

**ThemeToggle** - a 0.5px-bordered icon button (`ti-sun`/`ti-moon`) using the
tokens, replacing the bare B-1 button; same `useTheme()` underneath.

Visual language carries from B-1: sentence case, two weights, monospace for K8s
identifiers, status colours via CSS variables.

## 5. Cluster detail

**ClusterDetail** - resolves the cluster DTO by name; if it has vanished (removed
from config) shows a "this cluster is no longer in the fleet" notice with the
breadcrumb still offering a way back (no blank crash). Otherwise renders the
active section: `overview` -> `<Overview>`, the rest -> `<Placeholder>`.

**Overview** (the real one) - a fuller layout of existing DTO data:
- cluster name (mono) + status dot/state, version, all tags (env/region/provider/group)
- Health: state + reason (when non-empty), age ("last refresh Ns ago")
- Capacity: nodes ready/total, pods
- Capabilities: GitOps tier + reason, Network tier + reason, as labelled
  tier-coloured rows

**Placeholder** (the other four sections) - honest, capability-aware, reads the
DTO tier:
- GitOps: `gitopsTier === "Absent"` -> "No Flux or Argo installed on this
  cluster." else -> "GitOps reconciliation + inline drift arrives in M3."
- Network: `networkTier === "Absent"` -> "No Gateway API or Cilium here." else ->
  "Gateway topology arrives in M5."
- Resources -> "CRD browser arrives in M4." Observability -> "Inline metrics
  arrive with the Prometheus client (M7)."

Each names what is missing and when it is coming - the capability-detection ethos
keeps the maximal chrome truthful.

## 6. Testing

Frontend only (Vitest + Testing Library); the Go suite is untouched.
- Store routing: `openCluster` -> cluster/overview; `setSection`; `openFleet`
  reset; selected DTO stays resolvable after `setClusters`.
- Sidebar: Fleet click -> `openFleet`; section icon disabled (no-op) at fleet
  root, calls `setSection` in cluster scope; active highlight reflects route -
  including the Overview (`layout-dashboard`) icon highlighted after `openCluster`.
- Breadcrumb: fleet root label + count chip; cluster `Fleet > cluster > Section`;
  segment clicks fire the right actions.
- ClusterCard: `onClick` -> `openCluster(c.name)`.
- Overview: renders name, version, state+reason, nodes/pods, gitops/network
  tier+reason, tags from a fixture DTO.
- Placeholder: capability-aware text differs for `Absent` vs `Healthy` (GitOps).
- ClusterDetail: missing-cluster route -> the notice.
- ThemeToggle: flips `data-theme` and persists.
- Playwright smoke (dev server): load -> grid -> click card -> Overview +
  breadcrumb -> click GitOps section -> placeholder -> click Fleet breadcrumb ->
  grid -> theme toggle.
- Native check stays the user's (run `wails3 dev`, confirm the drill-in feel).

## 7. Out of scope (documented)

- Full card fidelity, filter pills, search (separate fleet-polish slice).
- Command palette (B-3).
- Real content for GitOps/Network/Resources/Observability (M3/M4/M5/M7).
- Richer per-cluster data (Flux version, Cilium Hubble/mesh, node list) - a later
  appbridge DTO extension.
- Any Go/appbridge change.

## 8. Decisions log

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Fleet-root, cluster-scoped sections | Matches mockups + principle 1 (fleet is root) |
| 2 | Frontend-only; Overview reuses ClusterDTO | Keeps the slice bounded; no bridge churn |
| 3 | Route in the Zustand store, no router dep | Few views; consistent with B-1 |
| 4 | Cluster-scoped section icons disabled at fleet root | No fake navigation (principle: render only what's real) |
| 5 | Capability-aware honest placeholders | Maximal chrome stays truthful (principle 7/9) |
| 6 | Styled ThemeToggle replaces the bare button | The B-1 toggle was an unstyled stopgap |
| 7 | Explicit Overview (layout-dashboard) icon in the rail | Makes the cluster root a first-class rail destination; gives Overview an active-highlight home |
