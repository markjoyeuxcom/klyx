# Klyx v0.10.0

v0.10.0 covers everything since v0.9.0. That includes the July feature work merged to main (PRs #15-#49): the guarded editing surface, AI assist, Reloader awareness and a set of new lenses and fleet tools. It also includes the 2026-10-03 full-codebase review remediation, a rebuilt workspace and navigation, and a shared health engine. That engine keeps one live observation per cluster, and every attention surface reads it. Git remains the source of truth: the editing paths are conflict-aware and guarded, and AI Suggest-fix proposes Git changes but never applies them. This is a pre-1.0 release; see "Known limitations" before relying on it.

## Health and attention

- **One verdict engine.** One backend rule set classifies pods, workloads and nodes as fault, degraded, progressing, paused, ok or unknown. Attention means fault or degraded. Unknown has its own count and never folds into ok. A zero is neutral, never green. Labels say what happened ("ImagePullBackOff", "Exited 1", "NotReady"), and restart history is a secondary note.
- **One observation per connected cluster, from app start.** A backend Observer is fed by watches, not polling, and does not depend on which lens is open. It publishes a cluster summary with per-kind coverage (live, stale, checking, unreadable) and as-of times.
- **Coverage:** workloads, pods, nodes, Flux reconciliations and sources, Argo CD Applications and ApplicationSets, cert-manager Certificates and issuers, External Secrets and their stores, Gateway API Gateways and route attachments, Karpenter NodeClaims and NodePools, Helm releases and warning events. Each tool keeps its own vocabulary, and its existing rules moved into the backend unchanged.
- **Every attention surface reads the summary.** In the open cluster, that is the mast Attention button, the navigation tree badges and Overview's Attention section. Across the fleet, it is the spine lamps, the board meters and the cluster cards. The fleet board no longer fetches health on every visit; cpu/memory utilization is still a per-visit metrics read.
- **Lenses read the same live rows:** Pods, Workloads, Nodes, Flux, Argo CD, Certificates, External secrets, Gateways and Karpenter. The Pods, Workloads and Nodes rows travel as deltas against the last full set. A lens header, its tree badge and the mast therefore show the same number. The Pods lens covers all namespaces with no list cap; the namespace is a filter.
- **Missing data is never shown as quiet.** A part that nothing has answered yet reads "checking…" and counts as a gap ("capability not yet known" while detection runs). An ended observation reads "observation ended at HH:MM:SS". A failed read keeps its last good value and marks it.
- **Spine lamps follow one rule order.** An unreachable cluster shows an unpowered socket. A fault blinks, and the spine is still the only place anything blinks. A warning glows amber. Connecting, checking or any gap shows unverified with no glow. Only a fully read, quiet cluster shows the dim healthy lamp.
- **Controller re-checks.** Controller health for cert-manager, External Secrets, Karpenter and the Gateway API data plane is now re-checked every 5 minutes after connect; before, a fault seen at connect was never re-checked. Flux and Argo controllers stay on a live watch.

## Redesign: workspace, navigation and shell

- **One workspace template for every lens:** a header (task, entity-scoped total and attention, freshness, Refresh), a controls row (scope, status filter, search), one table surface, and a footer with keyboard hints and the visible count. The permanent meter strips, the function-key bar and the triage tape are gone.
- **Every dataset state is explicit:** loading, scoped empty, no match (with Clear filters), could not be read (with Retry), and last-good stale data with its time. No green zeros, no idle inspector.
- **Inspector layout:** a sticky identity with the full name, one tool-native verdict, an action band (one primary, one secondary, More), Overview and Manifest tabs, and ordered collapsible sections.
- **Inspector width and selection:** drag or use the keyboard to set a width from 360 to 560px; double-click resets it to 400, and Klyx remembers it. Below about 1040px of workspace width the inspector opens as a modal drawer. On entering a lens, Klyx selects the first attention row once, from fresh data. A deleted object shows "removed" with its last evidence rather than jumping to another row.
- **Navigation band:** one row replaces the two stacked lens strips. It holds a group selector (whose popover is the full capability-gated tree, with search), the group's task destinations, a Resources menu of raw views, and More for tabs that do not fit; the active tab is never hidden. Flux splits into Reconciliation and Sources, Argo CD into Applications, Projects and ApplicationSets, and Hubble flows is its own Network destination.
- **Mast:** Back, Forward and Recent (mod+[ / mod+], mouse buttons 3/4; Recent holds the last twelve places), a readable "cluster / place" context, and the protected cue, which never shrinks. Port-forwards, Activity, Terminal and Settings moved to the mast, and the spine header is the Fleet button.
- **Fleet spine:** clusters are grouped dev, test, stg, prd. The expanded spine shows full names on up to two lines. Collapsed codes now tell apart names that differ only in the middle. mod+1..9 follows the spine's order.
- **Fleet routes:** Fleet board, Promotion and Expiry are tabs on the shared frame. Cards sit in a bounded 360-480px grid. ClusterMesh renders only when a cluster participates. The quiet states for Promotion and Expiry say how many clusters were read.
- **Overview:** one Attention section of per-area rows, each opening its lens with the attention filter, replaces the tile strip, the "Needs Attention" list and the tape.
- **Command palette:** every view is searchable from every route, including Fleet and Settings, whether or not its data has loaded. When several clusters are eligible, it asks you to choose one rather than picking a default. Results are grouped as Application, Clusters, Views, Resources, Objects and Actions, and an exact title match outranks frecency.
- **App zoom:** 80% to 200%, with real reflow (⌘= / ⌘- / ⌘0 or the palette); Klyx remembers the level.

## Editing and day-2

- **Guarded YAML editor for any object,** in the inspector's Manifest tab. It applies server-side with `Force:false`, so a field owned by kustomize-controller, helm-controller or Argo returns a conflict naming that owner. Before the write, the editor names the owning GitOps unit from the Flux inventory or Argo's tracked resources, honouring Argo's tracking mode and installation ID. Every lens links its object to this one editor.
- **"Deploy yaml" composer** for scratch objects: multi-document, dry-run first and create-only (a name collision refuses). Secrets are refused, and every object is stamped `klyx.dev/adhoc`. A namespace that belongs to a GitOps unit is reported as context, not as a deletion verdict.
- **Debug pod:** a diagnostic pod (netshoot, busybox or dnsutils) in the unmanaged `klyx-scratch` namespace, with a TTL the cluster enforces. The **Scratch** panel lists everything Klyx created and deletes it; deletion refuses any object without the adhoc label.
- **Rollout history, undo and pause/resume:**
  - The workload inspector shows revision history for Deployments, StatefulSets and DaemonSets, with images and change-cause.
  - Undo replaces the template the way kubectl does, refuses the current revision, and warns when a GitOps owner will revert it.
  - Pause and resume are available for Deployments.
- **Write verbs are armed against the object's UID:** pod delete and debug, restart, scale, pause, undo, cordon, drain, Flux reconcile/suspend, and Argo refresh/sync. If the object was replaced under the same name, the write is refused and says so. After a transport error, the verb says the outcome is unverified.
- **Debug shell confirmation.** The debug shell (`kubectl debug` adds an ephemeral container that stays until the pod is recreated) now asks for confirmation. Danger verbs open with focus on Cancel, so a shortcut followed by a stray Enter cannot run them.

## New lenses and fleet tools

- **Fleet compare:** the same workload side by side across DEV/STG/PRD, with image, revision, replica and resource drift marked against the origin and the live Flux revision per cluster.
- **Promotion** (Fleet tab): the applied GitOps revisions per cluster in environment-ranked columns, with drift and not-ready shown by default and a source-heads strip.
- **Expiry** (Fleet tab): certificate lifetimes across the fleet (TLS Secrets, webhook CAs, cert-manager status), soonest first.
- **Upgrade check:** version, EOL runway and version skew, plus served deprecated APIs against a target minor version.
- **Connectivity check:** a static NetworkPolicy simulation between live pods. Cilium policies are counted as PARTIAL.
- **Survivability headroom on Nodes:** pool and zone identity, and a requests-based zone/pool loss simulation with PDB drain blockers.
- **Hubble flows:** drop/deny triage from Hubble's Prometheus metrics (verdict rates, drop reasons by rate, DNS/HTTP error rates). Hubble detection now fires.
- **Access check (read-only RBAC):** can-i as the kubeconfig user or as an impersonated ServiceAccount, and a who-can enumerator with click-through grant paths.
- **Utilization:**
  - On the node board: cpu/memory used versus allocatable.
  - In the pod inspector: per-container usage, requests and limits, and rightsizing findings.
- **Karpenter:** broken-first NodePools (limits against provisioned capacity) and stuck-first NodeClaims. Each stuck claim names the first broken launch-chain link (Launched, Registered, Initialized). The lens also shows spot/on-demand identity and a disruption feed.
- **Inspector depth:**
  - Data-path facts for about 30 kinds, such as HTTPRoute backends, Gateway listeners and ConfigMap key names and sizes; values are never shown as facts.
  - Conditions read wherever a kind reports them.
  - Reverse lookups: PVC to pods, PDB to guarded pods, ServiceAccount to its bindings. A binding whose roleRef does not exist is flagged.
  - ResourceQuota meters.
  - Reloader awareness: the ConfigMaps and Secrets a workload "reloads on", and the reverse drill-in.
- **Helm:** releases stuck in `pending-*` or uninstalling now appear, and read "stuck" after 10 minutes.
- **Events:** the full message is in an inspector, and the 500-event read cap is stated.

## AI assist

- **Explain:** a grounded root-cause narrative on any resource and on the cert-manager issuance chain, reasoning over the resource's health, related objects and operator health. Only conditions and facts are sent, never YAML or Secret data.
- **Suggest fix:** drafts a minimal change for you to commit to Git; Klyx never applies it. The manifest it sends has credential-like values redacted and the last-applied annotation dropped. If redaction fails, nothing is sent, and the panel says how many values were redacted.
- **Backends and protected clusters:**
  - Backends are hosted (Anthropic Messages API) or local (any OpenAI-compatible endpoint, such as Ollama). Configure them in the config file's `ai:` block.
  - On protected clusters, only a localhost endpoint is allowed without `ai.allowProtectedEgress`. Redirects to another host are refused.

## Hardening and fixes

- **Kubeconfig pinning.** Every kubectl, helm and flux shell-out (drain, Helm rollback, exec, debug, describe, flux diff) is pinned to the cluster's own kubeconfig selection. A context name that also exists in the default kubeconfig can no longer send a verb to a different cluster.
- **Protection is enforced the same way in the UI and the backend:**
  - An inferred-production cluster (a prd/prod/production name or context) now gets typed arming in the UI, and Settings says it is inferred.
  - A hot-added cluster is protected immediately.
  - Flux reconcile/suspend, Argo refresh and the debug shell now consume the protected-cluster grant.
- **Failed reads show as errors, never as empty lists or zero counts.** A late response can no longer land under another cluster after a switch. Live lists re-list after a watch reconnects.
- **Gateway API health** is judged by GatewayClass acceptance, so Cilium-native, Istio and other implementations no longer read Degraded.
- **Flux:** each resource is read at the version the cluster serves it at (for example, OCIRepository at v1beta2). A failing source watch is an error, and Suspended no longer counts as attention.
- **Rollout and Helm verbs:** rollout undo no longer leaves a hybrid of the old and new templates. Helm rollback has no Klyx deadline; Helm's own `--timeout` bounds it.
- **Streams:** log and drain streams subscribe before opening, so the first lines of a short log are no longer lost. An open drain stays on the cluster it was confirmed on.
- **Connectivity and PromQL:** an any-port connectivity verdict now needs a port admitted in both directions. PromQL matchers are escaped.
- **Exec terminal:** it now opens on Linux and Windows; Windows uses PowerShell.
- **Settings:** adding clusters works when the config has fields after `clusters:`.
- **Smaller fixes:**
  - Dialogs return focus to the control that opened them.
  - A completed init container no longer flags a fresh rollout as restarting.
  - On macOS, the window no longer shrinks a pixel each launch.
  - Session restore is no longer overwritten at boot.

## Security

- The Go floor is now 1.27.2, for the net/http and http2 standard-library fixes, with golang.org/x/net v0.60.0; v0.9.0 built with Go 1.26.4 and x/net v0.53.0. govulncheck gates CI and reports no vulnerabilities.
- **Masking:**
  - Helm values are masked before they leave the backend, and sensitivity is inherited down secret subtrees.
  - The labels and annotations on a Secret's inspector come from the masked copy; the last-applied annotation had carried the data.
  - AI prompts are redacted as described above.

## Platform and distribution

- **Assets:** a macOS universal zip; Linux x86_64 as tar.gz, deb and rpm; Windows x64 and ARM64 as an NSIS installer plus a zipped executable. Linux ARM64 is not built yet.
- **Checksums:** every build ships a `.sha256` that records the bare file names, so `sha256sum -c` / `shasum -a 256 -c` works from the download directory.
- **Signing:** macOS notarization and Windows Authenticode are wired into the release workflow but not active, because the signing secrets are not configured. Your OS may warn on first launch. Verify each asset against its `.sha256`.

## Known limitations / not yet verified

- **This is a pre-1.0 release.** The v1.0.0 verification gate in `docs/backlog.md` §0 is not done:
  - the two eyeball checklists (2026-07-04 GUI pass, 2026-07-05 Signal Bench) and an eyeball of everything merged since, including the Karpenter lens against a live install;
  - a real-LLM smoke test (AI is so far verified against a fake client);
  - a live-cluster smoke of the write paths;
  - a Windows check of the exec terminal.
- **Health engine verification.** It has been verified on a local kind fixture (the coverage-parity gate), not yet on a real cluster.
- **Helm and Events attention are periodic snapshots, not watches.** Helm is read every 5 minutes, and also after a Klyx Helm operation or a lens Refresh. Events are read every 60 seconds. Events is evidence only and never counts as attention.
- **Request reads, each with an as-of time rather than live:** the Helm and Events lens lists, the Gateway topology, Argo CD projects, and Karpenter's controller and activity reads.
- **Settings:** the AI backend has no Settings form yet; edit the config file.
