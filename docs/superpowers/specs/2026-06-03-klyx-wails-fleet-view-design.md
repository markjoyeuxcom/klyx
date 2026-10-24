# Klyx Wails fleet view design (native shell + live fleet grid)

Date: 2026-06-03
Status: approved design, ready for plan
Scope: the M1 GUI milestone as one slice - Wails v3 native shell, the Go->JS
live data bridge, the full app chrome, the fleet cluster-card grid to mockup
fidelity, a rich command palette, and light/dark theming. Builds on the Go data
foundation + resilience + capability-health slices. Reference mockups:
`docs/design/mockups-2026-06-04.html` (mockups 1 and 4 are the fleet view).

## 1. Context and goal

The data layer is complete and proven headless via `klyxctl`. This slice puts a
native window on top: the cluster-card fleet grid from the mockups, updating live
as cluster state changes, with the full sidebar/header chrome, a keyboard-first
command palette, and first-class light/dark themes.

User decisions taken in brainstorming:
- Build the full GUI in ONE slice (skeleton + bridge + cards + chrome + palette +
  theming), not incrementally.
- Maximal chrome + rich palette, but HONEST: unbuilt views render a clear
  "not yet available" empty state, never a faked screen; the palette contains
  only commands that genuinely act. This reconciles "maximal" with principle 7/9
  (render only what's real).

## 2. Risk-first ordering

Wails v3 is alpha and its CLI is not installed on this machine. Therefore the
FIRST implementation task is a de-risking spike: install the `wails3` toolchain,
scaffold the skeleton, and prove one bound Go method renders in the webview. If
the alpha framework will not build here, everything else is moot - surface that
immediately, before any visual work. Pin a specific Wails v3 version in `go.mod`
(not `@latest`) for a reproducible build.

## 3. Architecture - the Go<->JS bridge

The Go data layer stays untouched: no Wails imports leak into `internal/fleet`,
`internal/capability`, etc. A thin new `internal/appbridge` package is the only
Go code that knows about Wails, so the data layer remains reusable (e.g. for
`klyx serve` later).

- `cmd/klyx/main.go` boots a Wails v3 `Application`, loads the Klyx config
  (default `~/.config/klyx/fleet.yaml`, env-overridable), builds the
  `fleet.Registry` + `fleet.DefaultConnFactory`, starts it, and registers a
  `FleetService`.
- `appbridge.FleetService` is bound to JS and exposes `GetFleet() []ClusterDTO`
  for initial load (plus action methods such as theme/reload later).
- `ClusterDTO` is a JSON-friendly projection of `fleet.Snapshot` + the cluster's
  config tags, joined by cluster name: `{name, state, reason, nodesReady,
  nodesTotal, pods, gitopsTier, gitopsReason, networkTier, networkReason, env,
  region, provider, group, version, lastSync, ageSeconds}`.
- **Server version capture (small data-layer addition in this slice).**
  `fleet.Snapshot` gains a `Version string` field, populated in `connectLoop`
  via a one-shot `c.typed.Discovery().ServerVersion()` call at connect (the same
  place presence detection runs) and stored under the conn mutex. The card's
  version badge (`v1.30.4` in the mockup) renders it; empty until the first
  successful connect. This is the only change to the otherwise-untouched data
  layer this slice requires.
- **Live updates by event push.** A background goroutine samples
  `registry.Snapshots()` (in-memory, cheap - no API calls; the data layer is
  watch-based) on a ~1s coalescing ticker and emits a Wails event
  `fleet:updated` carrying the DTO list. React subscribes and updates its store.
  This satisfies "never poll the API" (we sample already-watched in-memory state)
  while keeping the UI live and the bridge simple. Sampling cadence lives in Go,
  centralized; the same channel carries future per-resource updates.

## 4. Project structure

```
cmd/klyx/main.go              # Wails v3 app bootstrap
internal/appbridge/           # FleetService, ClusterDTO, Snapshot->DTO projection, push loop
frontend/                     # Vite + React + TS
  src/
    app/         # AppShell, active-section routing
    fleet/       # FleetView, ClusterCard, FilterPills, SearchInput
    palette/     # CommandPalette + command registry
    chrome/      # Sidebar, Header, placeholder views
    theme/       # tokens.css (from mockups), ThemeProvider, toggle
    bridge/      # typed wrappers around Wails bindings + fleet:updated subscription
    store/       # Zustand store (fleet + UI state)
```

## 5. Frontend stack, theming, state

- Vite + React + TypeScript (locked decisions).
- **Theming:** port the exact CSS-variable tokens from `mockups.html` (`:root` +
  `[data-theme="dark"]`: backgrounds, text, borders, status colours, fonts,
  radii) into `theme/tokens.css`. `ThemeProvider` sets `data-theme` on the
  document root and persists to `localStorage`. Both modes first-class; every
  colour references a variable, no hardcoded hex.
- **Icons:** `@tabler/icons-react` (bundled, tree-shaken - no CDN in a native
  app), Tabler outline only.
- **Visual rules in base CSS:** system sans + monospace stacks from the mockups,
  two weights (400/500), 0.5px component borders, flat surfaces (no shadows
  except focus rings), sentence case, monospace for K8s identifiers.
- **State:** Zustand (one small store). Rationale: fleet DTOs update ~1s;
  selector subscriptions let a single `ClusterCard` re-render only when its
  cluster changed, not the whole grid each tick. Store:
  `{ clusters, theme, search, filters, activeSection, paletteOpen,
  selectedCluster }`. The one notable runtime dep; fallback is Context +
  `useReducer` with memoized selectors if vetoed.
- **No router dep:** view switching is `activeSection` in the store; `<main>`
  renders the active view.
- **Data flow:** `bridge/` calls `GetFleet()` once on mount to seed the store,
  then subscribes to `fleet:updated` and writes DTOs into the store. Components
  read via selectors. The store is the single source of truth; the bridge is the
  only writer of fleet data.

## 6. Components and behavior (to mockups 1 and 4)

**Chrome:**
- `AppShell` - 46px icon sidebar + header + main; renders the active section.
- `Sidebar` - icon rail (Fleet active; Workloads, GitOps, Network, Observability,
  plus terminal/settings at the bottom). Clicking an unbuilt section renders an
  HONEST placeholder ("arriving in a later milestone"), never a fake screen.
- `Header` - title, cluster/region count chip, the fleet search input.

**Fleet view:**
- `FleetView` - the grid; subscribes to `clusters`/`search`/`filters`; shows the
  count chip (derived from tags), the filter-pills row, and the card grid.
- `ClusterCard` - matches the mockup: status dot (state colour), monospace name,
  env/region/version badges, nodes/pods grid, GitOps + mesh footer line. Renders
  all real states: loading skeleton (pre-sync), degraded (amber border + tag),
  unreachable/Failed (reason), syncing. cpu/mem/GPU render a muted "metrics
  pending" per the deferred-metrics decision.
- `FilterPills` - env / region / provider from tags; multi-select; drives the
  `filters` selector.
- `SearchInput` - filters cards by name (monospace match), wired to `search`.

**Command palette (rich, honest):**
- `CommandPalette` - fuzzy overlay over a command registry of genuinely-working
  commands: jump-to-cluster (scroll + highlight), filter-by-env/region/provider,
  focus search, toggle theme, switch section, reload config, copy cluster name.
  Keyboard-first (up/down/enter/esc); `Cmd/Ctrl+K` opens. No command without a
  real effect.

## 7. Build, verification, testing

**Toolchain:** install `wails3` CLI; pin a specific Wails v3 alpha version in
`go.mod`; scaffold the app; wire `Taskfile`/Make `dev` + `build` targets. The
first task adapts to the exact alpha API (`Application`, service registration,
`Events`).

**Verification (constraint: no visible native window):** `wails3 dev` serves the
frontend on a local Vite URL, drivable with the Playwright/browser tools.
Headlessly verifiable: renders match the mockups (light AND dark via toggle);
search filters cards; filter pills work; the palette opens, navigates, and acts;
LIVE updates (mutate a cluster's health via the homelab or a seeded fake registry
and confirm the card transitions without reload, proving `fleet:updated` end to
end). Needs the human: the packaged native binary, the window chrome, and the
sub-second-start / ~10-20MB targets (measured later, not a v1 gate).

**Testing:**
- Go `appbridge`: unit-test the `Snapshot` + config -> `ClusterDTO` projection
  against fixtures (pure mapping); a thin test for the coalescing push loop.
- Frontend (Vitest + Testing Library): `ClusterCard` per state
  (skeleton/live/degraded/unreachable) from fixture DTOs; `FilterPills`/
  `SearchInput` filtering; `CommandPalette` command execution; `ThemeProvider`
  flipping `data-theme`; a dark-mode render assertion per key component.
- Playwright smoke against the dev server: load -> cards render -> theme toggle ->
  search -> palette -> jump-to-cluster.

## 8. Out of scope (documented)

- Drilldown views (GitOps, Network, CRDs, Observability) - honest placeholders
  only this slice.
- Metrics in cards (cpu/mem/GPU) - the PromQL client is a separate slice;
  "metrics pending" state for now.
- In-app config editing; auto-update; packaging/signing; `klyx serve`.
- Mutating actions (reconcile/suspend) - viewer-only.

## 9. Decisions log

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | One slice for the whole M1 GUI | User choice; risk-first task ordering mitigates the coupling |
| 2 | Maximal chrome but honest placeholders | Reconciles "maximal" with render-only-what's-real (principle 7/9) |
| 3 | `internal/appbridge` is the only Wails-aware Go | Keeps the data layer pure/reusable for klyx serve |
| 4 | Event-push bridge, ~1s coalesce, sampling in Go | Live UI without flooding; "never poll the API" preserved (in-memory sample) |
| 5 | Zustand store | Per-card selector updates at the 1s cadence; small dep |
| 6 | Bundled @tabler/icons-react, no CDN | Native app has no CDN; Tabler outline per visual language |
| 7 | No router dep | Few views; activeSection state suffices |
| 8 | Pin Wails v3 version (not @latest) | Reproducible build on an alpha framework |
| 9 | Headless verify via Vite dev URL + Playwright | Native window not visible to the agent; frontend/bridge still verifiable |
| 10 | Capture K8s version via discovery ServerVersion() at connect (Snapshot.Version) | Populates the card version badge; one-shot discovery call, no watch |
