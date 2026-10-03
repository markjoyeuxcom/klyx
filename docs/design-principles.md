# Klyx design principles

These are the design choices that make Klyx different from every existing Kubernetes GUI. Treat them as non-negotiable defaults. If a feature seems to require violating one, that is a signal to redesign the feature, not to bend the principle.

## 1. Fleet is the root, not a dropdown

Lens, FreeLens, Headlamp, K9s all treat the cluster as the unit of work and the fleet as a dropdown of kubeconfig contexts. For a six-cluster (or larger) estate, this is backwards. Klyx makes the fleet the home view and a single cluster a drilldown.

**Implication for build:** the first view a user sees on launch is a grid of all configured clusters with live health, GitOps state, and ClusterMesh peering. No "select a cluster" gate.

## 2. GitOps is primary, not a plugin

Drift detection, reconciliation state, and the actual diff (in-cluster vs Git) live alongside the resources they manage. Not in a separate Flux dashboard. Not behind a "install the Flux extension" prompt. Not as a tab that defaults to disabled.

**Implication for build:** the GitOps view is one of the first lenses in the LensNav (the horizontal lens bar that replaced the sidebar in Signal Bench, PR #11). Every workload resource shows its owning Kustomization/Application as a clickable reference. Drift diffs render inline, not in a modal.

## 3. Gateway API is rendered as a graph, not a CRD list

Every existing tool dumps `kubectl get httproute -o yaml`. Klyx renders the actual data path: Gateway -> HTTPRoute -> Service -> Pods, with ClientTrafficPolicy attached to the Gateway, BackendTrafficPolicy attached to the route, and CiliumNetworkPolicy shown at the service level.

**Implication for build:** the network view is graph-based, not table-based. Each node in the graph is interactive. Policies attached at each edge are inline-visible, not nested in click-throughs.

## 4. ClusterMesh is a visible edge between clusters

Cilium GlobalServices and cross-cluster peering surface as arrows on the topology, not buried in `cilium-cli` output. Solves the "ATM is blind to backend health" problem - you can see which cluster traffic is actually going to.

**Implication for build:** the fleet view and network view both render mesh edges where they exist. Don't fake them where they don't.

## 5. Inline observability, not a separate Grafana tab

p50/p99/rps next to the route. CPU/memory on the pod card. Reconciliation duration next to the Flux Kustomization. All sourced from Prometheus/LGTM via PromQL. Klyx queries, doesn't reinvent.

**Implication for build:** the data layer needs a Prom query client alongside the K8s client. Metrics are part of the resource model, not a separate "metrics view".

## 6. Command palette as primary nav

`⌘K` (or `Ctrl+K`) opens a fuzzy-search palette for context switching, namespace jumps, resource lookup, action execution. The LensNav is the fallback for mouse users.

**Implication for build:** every navigable surface is reachable from the palette, and day-2 verbs launched from it open the same confirm flows as their buttons - never a bypass.

## 7. Capability detection over assumption

Klyx discovers what's installed in each cluster on connect - Flux CRDs, Argo CRDs, Gateway API, Cilium, ESO, monitoring stack - and conditionally renders the relevant views. The kind cluster doesn't get an empty "GitOps" pane because it has no Flux. The homelab k3s doesn't get a phantom ClusterMesh edge.

**Implication for build:** every view has a capability gate. If the required CRDs aren't installed, the view either hides or shows a clear empty state explaining what's needed.

## 8. Speak each tool's vocabulary correctly

Flux Kustomizations are "ready" or "drifted". Argo Applications are "synced" or "degraded". A HelmRelease has a revision; an Application has a sync status and a health status. Klyx never invents a unified abstraction that flattens these into one fake type - they're not the same thing, and pretending otherwise lies about reality.

**Implication for build:** the model layer has separate types for Flux and Argo resources. The UI uses each tool's terminology when displaying its resources.

## 9. Git is the source of truth; no resource creation wizards (revised 2026-10-04)

Desired state is written as Helm, Kustomize, or plain YAML in Git. Klyx surfaces what's in the cluster, helps diagnose what's wrong, and links to the source location - it has no "Create Deployment" forms and no resource templating UI.

Under the 2026-06-17 directive (functionality may overrule a non-goal, and the crossing is recorded), the editable surface of 2026-07-10 (PRs #27-#31) added guarded escape hatches that write to the cluster without authoring desired state in Git:

- one YAML editor for any object, applying with server-side apply `Force:false`: a field owned by another manager (kustomize-controller, helm-controller, Argo) returns a conflict naming it instead of silently drifting, and the editor shows which GitOps unit owns the object;
- an ad-hoc "deploy yaml" composer that only creates new objects (collisions refuse, `kind: Secret` is refused), stamps them `klyx.dev/adhoc`, and states what the namespace's GitOps unit can do to them;
- a TTL-bounded diagnostic pod in the unmanaged `klyx-scratch` namespace, and a Scratch panel that inventories and deletes everything Klyx created.

**Implication for build:** still no creation forms or templating. A new write surface must say whose state it touches, refuse to overwrite a GitOps owner silently, and be cleanable - Git stays the source of truth.

## 10. Native, not Electron

Go + Wails. Native binaries. Sub-second cold start. ~10-20MB on disk. The webview is for the UI tree, not for running a Chromium process tree.

**Implication for build:** any dependency that requires Node.js at runtime is rejected. The binary carries everything except the platform webview: WebKit on macOS, WebView2 on Windows, and GTK 4 + WebKitGTK 6 on Linux, which are linked dynamically and declared as package dependencies (corrected 2026-10-04: an earlier version said "statically linked").

## 11. Diagnostic lenses first, daily driver included (revised 2026-06-09)

Klyx leads with diagnostic lenses - health, ownership, traffic path, policy, observability, and failure context decide what is surfaced first. The owner has since promoted Klyx to daily driver, so standard resource coverage (pods, configmaps, secrets, services, nodes, RBAC viewing, events, logs) is now in scope - but done the Klyx way, not as a dumb kind-tree:

- Every list defaults to a diagnostic ordering (triage sort, warning-first events), never alphabetical-by-default.
- Standard kinds ship through the one generic GVR engine with a curated category layout, not fifteen bespoke tables.
- Secrets are masked until explicitly revealed. RBAC is view-only.
- Logs and events exist both contextually (attached to the pod/workload they explain) and as first-class views, because a daily driver needs the direct path too.

**Implication for build:** the test for any new surface is now "does it diagnose, explain, or operate - and does its default presentation tell you what's wrong before what exists?" A plain table is acceptable only as the body under a diagnostic default.

## Visual language

Signal Bench (2026-07-05, PRs #10-#11) supersedes the original mockup language: the app as bench hardware - lamps that glow only on faults, inset wells for data against raised modules for chrome, a permanent function-key row. The design of record lives in `design/` (`direction-c-signal-bench.html`, `signal-bench-implementation-notes.md`, `tokens-signal-bench.css`). Four rules are enforced in review:

- Only faults glow. A healthy lamp is lit but never glows.
- Blink is spine-only: the fleet spine is the one place a lamp may blink.
- Danger gets shape, not only colour: ▲, hazard tape, typed interlock.
- Wells hold data; modules hold chrome.

Still in force from the original language:

- Sentence case for everything, never Title Case or ALL CAPS - except deliberate verdict words (e.g. REACHABLE / BLOCKED)
- Monospace for resource names, namespaces, revisions, and any string that's a K8s identifier
- Status colours from CSS variables (success, warning, danger, info) - never hardcoded hex
- Tabler outline icons only
- Light and dark mode are both first-class (`bench` / `bench-light` are the default themes). Every colour must work in both

Superseded: "flat surfaces, no shadows" (wells and modules are machined with inset and raised shading) and the two-weight / 0.5px-border-only rules.
