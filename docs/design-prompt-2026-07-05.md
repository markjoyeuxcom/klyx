# Prompt: Klyx redesign exploration (paste into Claude)

You are the design lead for **Klyx**, a platform-engineer-grade Kubernetes desktop client (native Go + Wails, TypeScript/React webview - deliberately not Electron). Your job: produce **three visually distinct redesign directions** as self-contained HTML mockups, then recommend one. The current app works and is information-correct; the brief is to make it *look and feel* better without losing its operator-tool character.

## Who uses it
One senior platform engineer daily-driving six AKS clusters (dev/stg/prd across West Europe + North Europe) plus a homelab k3s ("nelli"). Stack: Cilium + ClusterMesh, Envoy Gateway, FluxCD (some Argo CD), External Secrets Operator, cert-manager, Prometheus/LGTM. They live in this tool for hours: triage first, keyboard first, terminal adjacent.

## Non-negotiable product principles (design within these)
1. Fleet is the root, not a dropdown - a persistent vertical "fleet spine" shows every cluster's health at all times.
2. Broken-first: every list sorts failures to the top. The 5-second test: a broken thing must be findable instantly.
3. Status semantics are ratified and must survive redesign:
   - **Staleness**: when a resource's spec changed but the controller hasn't observed it, the row dims to ~0.6 opacity and its status becomes a grey "Syncing…" chip. Ready is never shown as confirmed while changes are pending.
   - **Suspended** (Flux/Argo) is an intended paused state - neutral/amber, never an error.
   - **Danger needs shape, not just colour**: destructive dialogs lead with an alert-triangle icon; prd-locked clusters require typing the cluster name to confirm.
   - Speak each tool's vocabulary: Flux "ready / reconciling / suspended", Argo "synced / degraded" - never blended.
4. Honest states: empty views explain why and offer an action; failing watches render as error banners, never as clean empty lists; ages tick and the absolute time is one hover away; huge event counts render compact ("×28k") with the exact number on hover.
5. Keyboard-first: ⌘K command palette is primary navigation AND runs day-2 verbs (drain node, scale deployment, sync app); lists navigate with j/k; selection shows an inset accent bar. These affordances should be *visible* in the design, not hidden.

## Current visual system (evolve it or argue against it - don't ignore it)
Dark-first (7 dark themes + 1 light; system-follow). Sans UI font + monospace for all data (names, namespaces, revisions). Small type scale: 10-13px, 10px uppercase section labels. 0.5px hairline borders, low-alpha, small radii. Tabler outline icons at 1.5px stroke. A five-tone system carries ALL meaning-colour: success / warning / danger / info / muted - colour is never decorative. Layouts: dense row grids (28-32px rows), master-detail with resizable side panels (list never leaves the screen), summary strips of labelled counts above lists, filter pills with counts, status dots + chips.

## Screens to mock (all six, in each direction)
1. **Fleet overview**: six cluster cards + the mesh strip showing ClusterMesh peering arcs; one cluster degraded, one unreachable.
2. **Pods triage** (master-detail): ~14 rows incl. a CrashLoopBackOff with 7 restarts and a ×28k BackOff event; side panel open with status-first header (tone badge + reason chip + "0/1 ready"), actions row, containers, conditions, events; three rows checkbox-selected with a bulk bar ("3 pods selected · delete selected").
3. **Flux lens**: kustomizations/helmreleases list (one Failed with reason chip "HealthCheckFailed", one suspended, one Syncing…) + inspector with source health, dependsOn, inventory links, events.
4. **Command palette**: open over the UI, query "dra", showing verb results ("drain node aks-sys-3…", "restart deployment web/api") + a "+12 more — keep typing" footer.
5. **Certificates lens**: broken-first with one cert expiring in 6 days (amber) and one issuer-broken (red), inspector showing the issuance chain with the deepest stuck node flagged.
6. **A danger dialog**: "Drain node aks-sys-3" composer on the prd cluster - options (ignore daemonsets, delete emptydir data, force, timeout), the exact kubectl command preview, and the typed-confirmation field ("type aks-prd-weu to confirm").

Use realistic fake data throughout (cluster names like aks-dev-weu, aks-prd-neu, nelli; namespaces like flux-system, monitoring, ingress; charts like ingress-nginx-4.10.0). No lorem ipsum, no placeholder greeking.

## The three directions (make them genuinely different)
- **A. Refined instrument**: evolve the current dense-mono aesthetic - tighten rhythm (consistent 4px spacing grid), stronger status hierarchy, quieter chrome, sharper focus states. Lowest-risk direction.
- **B. Editorial terminal**: lean harder into the terminal heritage - richer mono typography with real hierarchy (weight/size, not colour), ruled lines instead of boxes, generous line-height in panels, restrained accent use. Think "beautifully typeset htop".
- **C. Your call**: one direction we haven't thought of that still passes every principle above. Surprise us - but it must survive the 5-second triage test.

## Deliverables & constraints
- One **self-contained HTML file per direction** (inline CSS + minimal inline JS only - no CDNs, no external fonts/images; system font stacks: `ui-sans-serif` and `ui-monospace`). Desktop layout at 1440px.
- Each file: all six screens stacked with section headings, a working **light/dark toggle**, and a short design-rationale note per screen (what changed and why, 2-3 sentences).
- Judge yourself before delivering: (1) 5-second triage test on every screen, (2) does colour ever appear without meaning?, (3) would a keyboard user know where they are?, (4) does it read as a professional's instrument or a SaaS marketing dashboard? Kill anything that fails.

Anti-goals, to be explicit: no giant KPI hero cards, no gradients-as-decoration, no pie/donut charts, no illustration mascots, no roundy consumer-app softness, no whitespace so generous it costs rows. Operators measure a tool by rows-per-screen and time-to-broken-thing.
