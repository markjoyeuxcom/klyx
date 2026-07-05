# Klyx

A platform-engineer-grade Kubernetes desktop client. Built because every existing tool (Lens, FreeLens, Headlamp, K9s, Aptakube, Portainer) treats clusters as a kubeconfig dropdown rather than a fleet, hides GitOps state behind a separate plugin, dumps CRDs as a flat alphabetical list, and renders Gateway API CRDs as raw YAML instead of the data path they actually describe.

## Owner context

Built by a Senior Platform Engineer running six AKS clusters across West Europe and North Europe (DEV/STG/PRD), with Cilium BYOCNI + ClusterMesh, Envoy Gateway replacing NGINX, FluxCD for GitOps, External Secrets Operator with Reloader, and an LGTM stack with Managed Grafana. The tool needs to work for this environment by default - not as an enterprise-tier upsell.

Also runs on a homelab k3s on AMD ROCm hardware and may end up on personal EKS labs. Provider-agnostic by design.

## What Klyx is

A native desktop application (Go + Wails) that connects to one or more Kubernetes clusters via standard kubeconfig and provides:

- Fleet-first multi-cluster overview
- GitOps reconciliation state with inline drift diffs (Flux and Argo, including coexistence)
- Gateway API topology rendering (Gateway / HTTPRoute / *TrafficPolicy graph)
- ClusterMesh-aware cross-cluster routing visualisation
- CRD browser grouped by API group with live instance counts
- Inline observability metrics from Prometheus/LGTM
- Keyboard-first command palette navigation
- Daily-driver coverage (owner directive 2026-06-09): pods with live logs and
  events, the standard resource kinds through the generic GVR engine, nodes,
  day-2 verbs (delete pod, restart, scale, cordon/drain, port-forward), and
  helm release inspection - see `docs/superpowers/plans/2026-06-09-klyx-daily-driver-roadmap.md`

## What Klyx is not

> **Directive (2026-06-17): app functionality overrules these non-goals.** The owner ruled that all prior decisions and locked non-goals can be overruled and replaced when real functionality is at stake. The list below is now guidance, not a hard boundary: when a feature delivers clear operational value, build it and note the rule it crosses - do not refuse on rule grounds alone. Engineering principles (informer-based, capability-detected, fleet-first, vocabulary-correct) still hold.

Former hard non-goals (still useful as defaults, no longer inviolable):

- Not a resource creation/editing wizard. Desired state is authored as Helm/Kustomize in Git, never hand-written through Klyx. Klyx may drive controllers and perform day-2 operations (reconcile, suspend/resume, and later scale/restart), but it never authors desired state - Git remains the source of truth.
- Not an RBAC management UI. Klyx views RBAC objects (read-only) and operates on cluster state; it does not manage roles, bindings, or permissions.
- Not an alerting platform. Delegate to AlertManager / Grafana.
- Not a Helm chart browser or installer. Release inspection and rollback are in scope (day-2); install/uninstall and chart browsing stay with `helm` and Flux.
- Not a multi-tenant SaaS. Single-user desktop binary. (The once-planned `klyx serve` headless mode was dropped 2026-07-03 - no remaining need; Klyx stays strictly single-user.)
- Not an Electron app. Native binary, sub-second startup, ~10-20MB.
- (Revised 2026-06-09) Klyx IS the daily driver. The original "not a kubectl replacement" non-goal was retired by the owner: Klyx now aims to cover daily Kubernetes operation end to end, shelling out to kubectl/helm where they are the better tool (drain, exec escape hatch, helm release data). What it still never does is author desired state.

## Tech stack (locked decisions)

- **Language**: Go 1.22+
- **Native shell**: Wails v3 (https://wails.io)
- **K8s client**: client-go (informer-based for live state, never direct API calls per render)
- **Frontend**: TypeScript + React with Tabler outline icons
- **Styling**: CSS variables for theming, both light and dark mode mandatory
- **Build targets**: macOS universal (Intel + Apple Silicon), Linux x86_64 + ARM64, Windows x64 + ARM64
- **Distribution**: Homebrew tap, apt/rpm repos, AppImage, .msi, winget, scoop
- **Auto-update**: built in with per-platform signed channels

## Architecture layers

```
┌─────────────────────────────────────────────────┐
│  UI (TypeScript + React in Wails webview)       │
├─────────────────────────────────────────────────┤
│  View layer (per-feature view models)            │
│   - FleetVM, GitOpsVM, NetworkVM, CRDVM ...      │
├─────────────────────────────────────────────────┤
│  Capability detection                            │
│   - Detects Flux, Argo, Cilium, Gateway API,    │
│     ESO, Reloader, cert-manager, monitoring     │
│   - Conditional view rendering based on caps    │
├─────────────────────────────────────────────────┤
│  Data layer (Go)                                 │
│   - One informer factory per cluster             │
│   - Shared cache, watch-based, never polled      │
│   - Cross-cluster query aggregation              │
├─────────────────────────────────────────────────┤
│  Connection layer                                │
│   - kubeconfig loader with exec plugin support  │
│   - aws eks get-token, kubelogin, bearer token  │
│   - Per-cluster credential isolation             │
└─────────────────────────────────────────────────┘
```

## Design principles

See `docs/design-principles.md` for the full set. The non-negotiable ones:

1. Fleet is the root, not a dropdown.
2. GitOps is primary, not a plugin.
3. Gateway API is rendered as a graph, not a CRD list.
4. ClusterMesh is a visible edge between clusters.
5. Inline observability, not a separate Grafana tab.
6. Command palette as primary nav (`⌘K`).
7. Capability detection over assumption - render only what's installed.
8. Speak each tool's vocabulary correctly (Flux "ready/drift", Argo "synced/degraded").

## Initial milestone

Build M1 before anything else. M1 is the smallest thing that demonstrates the design works:

- Wails app skeleton with sidebar, header, command palette
- Kubeconfig connection to one cluster
- Fleet view rendering one cluster card with live data via client-go informers
- Capability detection for Flux, Argo, Cilium, Gateway API
- Light and dark mode

No GitOps view, no network topology, no CRD browser yet. Just prove the foundation works end to end.

After M1, milestones in priority order:
- M2: Multi-cluster fleet view (the killer feature)
- M3: GitOps view with Flux support
- M4: CRD browser grouped by API group
- M5: Gateway API topology view (M5-a lanes, M5-b-i Envoy policies, M5-b-ii Cilium inference shipped; M5-c ClusterMesh edges shipped 2026-07-03 — the fleet MeshStrip draws peering arcs: solid=mutual, dashed amber=one-way (edge records the configuring side), dashed grey=off-fleet; eyeballed 2026-07-03)
- M7: Inline observability (Prom queries) — shipped through M7-c-ii (workloads health + cpu/mem)
- M9: Daily driver — SHIPPED 2026-06-10 (pods + live logs, events, standard resources incl. masked secrets, nodes + cordon/drain, scale, port-forward, exec escape hatch, helm releases, cmd+K palette, layout polish). Roadmap + eyeball checklist: `docs/superpowers/plans/2026-06-09-klyx-daily-driver-roadmap.md`
- M8: `klyx serve` headless mode — DROPPED 2026-07-03 (owner ruling: no remaining need; Klyx stays a single-user desktop binary)
- M6: Argo support — SHIPPED 2026-06-11 (deferral expired when Argo CD entered the homelab). Own "Argo CD" section speaking Argo vocabulary (synced/degraded), applications lens broken-first, refresh + sync behind confirm; verified against six live Applications on nelli. Flux and Argo coexist as parallel sections. Pruning policy (ratified 2026-07-02, supersedes the original "never prune"): prune is OFF by default to prevent destructive reconciliation and requires an explicit per-sync opt-in in the sync modal; on protected clusters the sync additionally consumes the typed-confirmation backend grant. Also ratified 2026-07-02: `Suspended` is an intended paused state, not an error — it never counts toward the broken-first list (matches the Flux side and Argo's own UI).
- M10: Flux diagnosis depth — SHIPPED 2026-06-17 (PR #3). Diagnose a stuck Flux resource without a terminal: source health (watches GitRepository/OCIRepository/Bucket/HelmRepository/HelmChart; bound-source health in the detail panel; sources filter), reconcile-with-source, failing-condition reason chip, dependsOn blocked-by, a drift surface that reads the controller's own events, and a gated on-demand `flux diff` (suspended/apply-failing Kustomizations only, via `internal/fluxcli`). Plan + spec: `docs/superpowers/{plans,specs}/2026-06-17-klyx-flux-diagnosis*`. Open follow-up: `flux diff --path` defaults to spec.path — a per-cluster local-clone-root config would make M10-f resolve reliably.
- M11: cert-manager diagnosis — CM-a/b/c SHIPPED 2026-06-19 (branch `feat/m11-certmanager`, eyeballed 2026-07-03). A capability-gated "Certificates" section: cert-manager capability detection + sidebar badge (M11-a), a broken-first Certificate triage list with an issuers filter and a per-cert inspector (M11-b), and an on-demand issuance-chain inspector (M11-c) that resolves bound-issuer health (the headline) + the Certificate→CertificateRequest→Order→Challenge tree, flagging the deepest stuck node — the M10 Flux-diagnosis pattern applied to cert-manager. New pure pkg `internal/certmanager`; lazy watch + on-demand chain resolver in `internal/fleet`; `CertManagerService`. Read-only. Built from the "Klustr" reference screenshots in `images/`. Plan + spec: `docs/superpowers/{plans,specs}/2026-06-19-klyx-certmanager-diagnosis*`. M11-d Renew: ruled out 2026-07-03 — a one-click re-issuance trigger invites ACME rate-limit burn (LE duplicate limit 5/week) and rarely fixes a broken chain; the lens diagnoses, `cmctl` stays the escape hatch. Revisit only on a concrete operational need, and then as "Request re-issuance" with rate-limit-aware confirm copy.
- Full-codebase review remediation — 2026-07-02, on `feat/m11-certmanager`. Five commits: P0 correctness (triage-semantics inversions, informer races, listener leaks), P1 hardening (backend protected-cluster gating via one-shot typed-confirmation grants, kubectl argv validation, error honesty - failing watches/lists render as errors, never as empty), P2/P3 (informer-cache-served nodes, TTL discovery cache, `internal/clibin`, design-system completion, virtualized workloads/instances), push/PR CI (`.github/workflows/ci.yml`), and observedGeneration staleness. Staleness visual language (ratified): when `metadata.generation > observedGeneration`, the row dims to 0.6 opacity and the status chip becomes a grey "Syncing…" chip whose tooltip names both generations — "dim = not yet realized"; Ready is never shown as confirmed while changes are pending (Flux resources, Certificates, Gateways; shared `src/cluster/staleness.tsx`). Naming ratified: the Ready condition's transition time is `ReadySince`/`readySinceSeconds` (never "last applied" — Flux status has no per-apply timestamp).
- M12: External Secrets Operator diagnosis — SHIPPED 2026-07-03 (branch `feat/m12-eso-lens`, eyeballed 2026-07-03). The M10/M11 diagnosis pattern applied to ESO: capability-gated "External Secrets" section (external-secrets.io served + `external-secrets` Deployment health), a broken-first ExternalSecret triage list with a stores filter and a per-secret inspector where the bound (Cluster)SecretStore's health is the headline, and ESO-native refresh staleness (`now - refreshTime > 2×refreshInterval` renders an amber "stale" chip — never confidently synced). New pure pkg `internal/externalsecrets`; lazy watch in `internal/fleet`; `ExternalSecretsService`. Read-only. Plan: `docs/superpowers/plans/2026-07-03-klyx-eso-diagnosis.md`. Deferred: Reloader awareness, ClusterExternalSecret/PushSecret kinds, day-2 force-refresh verb.
- GUI friction pass — 2026-07-04, MERGED 2026-07-05 as PR #9 (grew to 17 commits: +compact event counts, master-detail instance browsing, shared panel anatomy `InspectorStatus`/`InspectorConditions`/`InspectorEvents`; eyeball still pending: `docs/eyeball-checklist-2026-07-04-gui-pass.md`). The daily-drive friction list, built in one sweep: session restore (route + per-cluster filters persist; search survives section switches only), the palette as an operator console (day-2 verbs via one-shot `NavIntent`s that open the existing confirm/composer - never bypassing prd arming; frecency; honest "+N more"), toast queue + persisted Activity log + forward-death toasts, row copy-name/image/YAML/values affordances, keyboard parity everywhere + "?" cheatsheet + focus restore + mod+1..9, cross-links (workload pods/owner/node, nodes→pod, flux inventory→live objects via shared `resourceRefs.ts`, cert/ESO→Secret), Helm triage parity, EmptyState/skeleton/WatchErrorBanner unification with "Clear filters" actions, live-ticking ages + absolute hover + updated-at captions, workload observedGeneration staleness (Go DTO + ratified dim/Syncing… language), native shell (window-state restore via `internal/winstate`, contextual title, dock badge, lazy-authorized OS notifications through `ShellService`), Tabler icons for every glyph affordance + danger-dialog triangles, YAML highlight+find (`YamlBlock`), and bulk pod delete (one confirm; per-call grant re-arm on protected clusters via `ConfirmDialog.onConfirmTyped`). Deliberately staged for owner review, not dropped: workload rollout-undo (new destructive backend verb), pinned resources (Q16 product shape), density setting (needs a spacing-token pass), VirtualList ARIA (needs per-row cooperation), fleet-wide palette search (belongs with the metadata-informer work).
- Signal Bench redesign (direction C) — 2026-07-05. The owner ran a Klyx design prompt in claude.ai, picked direction C ("Signal Bench": the app as bench hardware - lamps that only glow on faults, inset wells for data vs raised modules for chrome, a permanent function-key row). Design artifacts of record live in `design/` (mockup + implementation notes + tokens). Shipped in two PRs: PR #10 (tokens: `bench`/`bench-light` as the new DEFAULT themes with one-time migration + system→bench faces; primitives: lamp physics in StatusDot, frame→module/well machining, inset tone rings, pressed filters; chrome: FKeysBar, channel-strip spine - the ONE legal blink site, module cluster cards, hazard-stripe arming dialog with LED interlock and "— locked" confirm, desk shell with floating modules) and PR #11 (fidelity: mast replaces TopBar+Header - mono "cluster / lens" + live lamp + prd chip + ⌘K well; horizontal two-row LensNav REPLACES the vertical Sidebar (same model rotated 90°, capability-gated, honest fault lamps/counts from loaded stores only); inspectors + lens lists as inset wells; first-class `MetersStrip` on every lens with per-lens vocabulary and ordering notes, zero-fault cells never render; TriageTape rebuilt as a one-row signal strip; InspectorStatus as the single verdict element; `inputWell`/`selectWell` for every form control; dead helm "repositories" stub tab removed). Four enforce-in-review design rules: only faults glow; blink is spine-only; danger gets shape (▲/hazard tape/typed interlock); wells hold data, modules hold chrome. Eyeball checklist: `docs/eyeball-checklist-2026-07-05-signal-bench.md`. Deferred: MeshStrip patch-bay cosmetics, WatchErrorBanner retry countdown (needs per-watch retry state).
- Next: PR #11 merge + the two pending eyeball checklists (2026-07-04 GUI pass, 2026-07-05 Signal Bench) are the gate for v1.0.0 - the owner called the release 2026-07-05. Beyond that, work is demand-driven from the deferred notes above (Reloader awareness, M10-f clone-root config, ClusterExternalSecret/PushSecret) and the engineering backlog (LogsPane virtualization, metadata-informers for heavy list views, Conn interface split).

## References

- `docs/design-principles.md` - full design philosophy
- `docs/mockups.html` - six UI mockups (open in browser to view)
- `docs/brainstorm-questions.md` - open architectural questions
- `docs/example-prompts.md` - example Claude Code prompts per phase

## Working style

- Direct, principle-led, technically precise. Push back on weak assumptions.
- Prefer hyphens over em dashes.
- Concise actionable prose. Bulleting only when it earns its place.
- First-person ownership language.
- Show stress-tests of decisions before posting/committing.
- Context is a competitive advantage - bring it up front, do not ask the same question twice.
