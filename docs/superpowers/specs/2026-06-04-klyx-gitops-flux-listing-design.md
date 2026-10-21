# Klyx GitOps M3-a design (Flux reconciliation listing)

Date: 2026-06-04
Status: approved design, ready for plan
Scope: M3-a, the first content-bearing drilldown. Read-only, Flux-only listing of
Kustomizations + HelmReleases with reconciliation status, rendered in the GitOps
section. Establishes the lazy per-cluster drilldown data + bridge pattern reused
by all future drilldowns. Reference: `docs/design/mockups-2026-06-04.html` mockup 2 (minus the
drift expansion). Builds on the drill-in navigation slice.

## 1. Context and scope

M3 (GitOps with Flux) is decomposed into:
- **M3-a (this slice): Flux reconciliation listing (read-only).**
- M3-b: inline live-vs-applied drift diff.
- M3-c: actions (reconcile/suspend/resume + prd confirmation) + view-in-git.

Argo + coexistence are explicitly M6, out of M3. The homelab runs Flux (gitops
Healthy), so this lights up immediately.

Decisions taken in brainstorming:
- **Drilldown data flow = lazy informers + event push.** When the UI opens the
  GitOps section for a cluster, the data layer starts dynamic informers on the
  Flux CRDs (lazy, only while viewed) and a ~1s coalescing loop pushes a
  `gitops:updated` event; closing the section stops the watch. Watch-based
  (honours "never poll the API"); this is the reusable drilldown pattern.
- **Read the Flux CRDs as unstructured** and parse the well-known status fields -
  no Flux Go API dependency, tolerant of Flux version drift.

## 2. Data layer

### 2.1 `internal/gitops/flux` (pure)

Vocabulary-correct types (principle 8):
```go
type Kind string // "Kustomization" | "HelmRelease"
type ReadyState string // "Ready" | "Reconciling" | "Failed" | "Unknown"

type Resource struct {
    Kind        Kind
    Namespace   string
    Name        string
    Ready       ReadyState
    Message     string    // Ready condition message (why failing)
    Revision    string    // applied revision (short)
    LastApplied time.Time
    Suspended   bool
}
```
Pure parsers, unstructured in / typed out:
- `ParseKustomization(u *unstructured.Unstructured) Resource`
- `ParseHelmRelease(u *unstructured.Unstructured) Resource`

Parsing rules: read `status.conditions`; the `Ready` condition `status==True` ->
`Ready`, `False` -> `Failed`; a `Reconciling` condition present/True -> `Reconciling`;
absent/unknown -> `Unknown`. `spec.suspend==true` -> `Suspended`. Revision from
`status.lastAppliedRevision` (Kustomization) / `status.lastAppliedRevision` or
the last `status.history` entry (HelmRelease), shortened. `Message` from the
Ready condition message. `LastApplied` from the Ready condition
`lastTransitionTime` (best-effort).

### 2.2 `internal/fleet/gitopswatch.go` - lazy per-cluster watch

The reusable drilldown pattern. `ClusterConn` gains a `dynamic.Interface` (added
in the conn factory) and:
- `OpenGitOps()`: resolve the served GVRs via discovery
  (`kustomize.toolkit.fluxcd.io` `kustomizations`; `helm.toolkit.fluxcd.io`
  `helmreleases` - the served version varies across Flux releases, so pick the
  one discovery reports), start a `dynamicinformer` shared factory on those two
  GVRs (lazy - only now), register event handlers that coalesce. Idempotent /
  ref-counted: repeated opens while active are no-ops.
- `GitOpsResources() []flux.Resource`: read the two informer stores and parse.
- `CloseGitOps()`: stop the dynamic factory (cancel its context) after a short
  TTL, freeing the watches on leave.

The informers run on a child context of the conn's context, so they also stop if
the conn/app shuts down. Mirrors the eager-set + cap-health watch patterns.

## 3. appbridge: GitOps subscription + push

Reuses the `FleetService` sample-and-push pattern (the lazy informers keep the
store fresh; a ticker samples it).

- `Registry.Conn(name) (Conn, bool)` lookup; the `Conn` interface gains
  `OpenGitOps()`, `CloseGitOps()`, `GitOpsResources() []flux.Resource`.
- `appbridge.GitOpsService` bound to JS:
  - `Open(cluster string)`: `reg.Conn(cluster)` -> `conn.OpenGitOps()`; launch a
    per-cluster goroutine that on a ~1s tick emits `gitops:updated` with
    `{ cluster, resources: []FluxResourceDTO }` from `conn.GitOpsResources()`.
    Tracked in a `map[string]context.CancelFunc` under a mutex. Idempotent.
  - `Close(cluster string)`: cancel that goroutine; `conn.CloseGitOps()`.
- `FluxResourceDTO`:
  ```
  { kind, namespace, name, ready, message, revision, lastAppliedAgeSeconds, suspended }
  ```
  Pure `ToFluxDTO(r flux.Resource, now time.Time) FluxResourceDTO`
  (`lastAppliedAgeSeconds` via injected clock, like `ClusterDTO`).
- One `gitops:updated` event carrying a `cluster` field; the frontend ignores
  events whose cluster it isn't currently viewing (guards a late event after
  Close).

`internal/appbridge` stays the only Wails-aware Go; `flux` and the fleet watch
stay pure/reusable.

## 4. Frontend

- Store slice: `gitops: { cluster: string | null; resources: FluxResourceDTO[];
  loading: boolean }` + `setGitOps`.
- `bridge/gitops.ts`: `openGitOps(cluster)` calls `GitOpsService.Open`, sets
  loading, subscribes to `gitops:updated` (writes resources only when
  `ev.data.cluster` matches the open cluster); `closeGitOps(cluster)` calls
  `Close` and clears the slice; returns an unsubscribe.
- `cluster/GitOps.tsx` (replaces the GitOps placeholder for `section: "gitops"`):
  - Reads the cluster's `gitopsTier` from the fleet store. If `Absent`, render the
    honest "No Flux or Argo installed on this cluster." state and do NOT open a
    watch.
  - Else `useEffect`: `openGitOps(cluster)` on mount, `closeGitOps` on unmount.
  - Renders a summary row (Kustomizations N · HelmReleases N · Ready N · not-ready
    N - "drifted" specifically arrives with M3-b) and the resource table to
    mockup 2: kind icon (folder=ks, package=hr), monospace `namespace/name`, the
    revision (mono), and a status badge (ready=success, reconciling=info,
    failed=danger with message, plus a suspended tag). "Loading reconciliation
    state..." until the first push lands.
- `ClusterDetail`: `section === "gitops"` routes to `<GitOps cluster={...} />`;
  the other three sections keep their capability-aware `Placeholder`.

## 5. Testing

- **`internal/gitops/flux` (pure):** table-driven parser tests from fixture
  unstructured objects -> Resource (Ready/Failed/Reconciling/Suspended, revision,
  message). Highest-value.
- **`internal/appbridge`:** `ToFluxDTO` projection (incl. age via injected clock);
  `GitOpsService` with a fake registry + fake `Conn` + fake emitter - `Open`
  starts + emits, `Close` stops, a post-Close event does not fire. `-race`.
- **`internal/fleet` lazy watch:** `client-go/dynamic/fake` + dynamic informer -
  seed Flux unstructured objects, `OpenGitOps()` -> `GitOpsResources()` reflects
  them, `CloseGitOps()` stops cleanly. `-race`. Documented fallback for
  fake-dynamic quirks (same precedent as metadata-fake).
- **Frontend (Vitest):** `GitOps.tsx` renders the table from fixture DTOs (Ready,
  Failed-with-message, Suspended rows); the `Absent` no-Flux empty state (and
  asserts it does NOT call `openGitOps`); the loading state; summary counts.
  Store/bridge: a `gitops:updated` for the open cluster updates the slice, a
  different cluster is ignored.
- **Playwright smoke + native handoff:** open `homelab-nelli` -> GitOps -> the
  Flux list renders with live statuses; native confirmation is the user's.

## 6. Out of scope (documented)

- Inline drift diff (M3-b); reconcile/suspend/resume + view-in-git (M3-c).
- Argo + coexistence + split-brain (M6).
- A "drifted" count in the summary (needs the drift engine, M3-b) - M3-a shows
  "not-ready" instead.
- Per-resource detail panels beyond the row.

## 7. Decisions log

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | M3 = Flux-only; decomposed a/b/c | CLAUDE.md (Argo+coexistence is M6); a is read-only + foundational |
| 2 | Lazy informers + event push for drilldown data | Watch-based (never poll); reusable pattern for all drilldowns |
| 3 | Read Flux CRDs as unstructured + parse | No Flux Go dep; tolerant of version drift |
| 4 | Resolve served Flux GVR versions via discovery | HelmRelease/Kustomization versions vary across Flux releases |
| 5 | Sample-and-push via GitOpsService ticker | Reuses the proven FleetService pattern; no extra callback plumbing |
| 6 | GitOps view skips Open when gitopsTier is Absent | Don't watch Flux on a cluster without Flux; honest empty state |
