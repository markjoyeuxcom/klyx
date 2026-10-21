# Native eyeball checklist — GUI friction pass (2026-07-04)

Branch `feat/gui-friction-pass`. Everything below changed headless-only (tsc +
vitest + go test green; frontend production build green). These are the
behaviors that need real eyes on a real cluster before merging or releasing.

## Session restore
- [ ] Quit in `cluster → Pods → namespace X` with needs-attention on; relaunch lands exactly there (namespace + toggle restored, search box empty).
- [ ] Switch Pods → Overview → Pods within one run: namespace, toggle, AND search text survive.
- [ ] Remove the restored cluster from fleet.yaml; relaunch falls back to the fleet grid, no error.

## Palette as operator console
- [ ] `⌘K` → "drain <node>" opens the drain composer prefilled; cancel leaves no drain started.
- [ ] "suspend kustomization …" flips to "resume …" for suspended resources; confirm opens the right dialog.
- [ ] "scale deployment …" expands the row and opens the replica popover; DaemonSets offer restart but no scale verb.
- [ ] A verb for a pod that vanished (delete it first via kubectl) clears with an error toast, no dialog.
- [ ] Run the same command a few times; reopen palette empty - it floats to the top (frecency).
- [ ] With >50 matches the footer reads "+N more — keep typing".
- [ ] prd-lock: palette verb on a protected cluster still demands the typed confirmation (nothing bypassed).

## Toasts, activity, notifications
- [ ] Trigger two outcomes quickly (e.g. reconcile + a failing sync): both toasts stack, error lingers ~10s.
- [ ] Activity view lists them newest-first; relaunch keeps the history; "clear history" empties it.
- [ ] Kill a port-forward target so the forward breaks: error toast appears.
- [ ] Background the window, cause an error: OS notification arrives (first use prompts for authorization on macOS).
- [ ] Take a cluster down: dock icon badges with the count; recovers to no badge.

## Window & title
- [ ] Resize/move the window, quit, relaunch: geometry restored.
- [ ] Navigate around: ⌘-Tab shows "cluster · Section — Klyx".

## Keyboard & a11y
- [ ] `?` opens the cheatsheet; Esc closes; `?` inside a filter box types a literal `?`.
- [ ] `⌘1..9` hops clusters in fleet-spine order.
- [ ] j/k/Enter//` now work in External Secrets, Nodes, Helm, Gateway API, and any Resources/CRD instance table.
- [ ] Close any confirm dialog: focus returns to the button that opened it.
- [ ] Danger dialogs (delete/drain/suspend) lead with the alert triangle.

## Copy affordances
- [ ] Hover any pod/workload/node/release/instance row: copy icon reveals next to the name; click copies, never opens the row.
- [ ] Pod containers: image copy puts the FULL ref on the clipboard.
- [ ] Pod/node YAML tabs and helm values: corner copy + find box.

## Cross-links
- [ ] Workload context panel: pod rows open pod detail in Pods; node names jump to the Nodes board; flux owner opens the GitOps lens preselected.
- [ ] Pod detail: owner → Workloads (selected+expanded); node → Nodes (inspector open).
- [ ] Node inspector "Pods on node": click opens THAT pod's detail (after the pods list loads).
- [ ] Flux Kustomization inventory entries open the live object; unknown kinds stay plain text.
- [ ] Certificate secret name / ExternalSecret target secret open the Secret instance.

## Helm triage
- [ ] Failed release floats first; filters + search + namespace select narrow; health strip counts match; "updated HH:MM:SS" caption after refresh.

## States & ages
- [ ] Empty lens views show the designed EmptyState; filter-made emptiness offers "Clear filters" and it works.
- [ ] First loads shimmer (skeletons) everywhere - no plain "Loading…" text remains.
- [ ] Leave a list on screen >1 min: ages advance; hover shows the absolute instant.
- [ ] Edit a Deployment spec (kubectl) mid-view: the row dims with the grey "Syncing…" chip until observedGeneration catches up.

## Bulk pods
- [ ] Hover-reveal checkboxes; `x` toggles the highlighted row; bulk bar counts live.
- [ ] Bulk delete of 3 pods on an UNPROTECTED cluster: one confirm, three deletions, selection clears.
- [ ] Bulk delete on the prd-locked cluster: typed confirmation once, all N pods deleted (per-call re-arm is invisible); break the guard (wrong config) to see the honest "stopped at pod X" toast.

## Visual
- [ ] All close/expand affordances are Tabler icons now (no ✕ / ▸ glyphs); CRD group chevron rotates.
- [ ] TopBar right side no longer shifts when the fleet seeds.
- [ ] YAML highlighting is readable in light AND a dark theme; find highlights + cycles with Enter.

## Master-detail resources (added after the event-count fix)
- [ ] Open any Resources/CRD list (Secrets, Services, a CRD): clicking a row opens a resizable side panel; the list stays put, j/k keeps moving, Esc closes the panel.
- [ ] The panel's YAML editor / secret reveal / related objects all work at panel width; drag the divider - width persists (shared with the pod/node panels).
- [ ] Deep links still land right: palette jump to an instance, Argo app resource click, Flux inventory click - all open list + panel, not a full-page swap.
- [ ] Fleet-scope browsing: clicking a row from another cluster switches cluster and opens list + panel there.

## Panel presentation pass (added with master-detail)
- [ ] Pod/node/helm/workload/instance panels open with a tone badge headline (state + reason chip + detail) before any key-value rows; tones read correctly in light and dark.
- [ ] Conditions look identical across pod/node/instance panels: dot, type, mono reason chip, ellipsised message; node pressure conditions still redden on Unknown.
- [ ] Events look identical across panels: dot, reason, message, compact ×count with exact hover, ticking age.
- [ ] A stale (Syncing…) workload's panel headline goes muted "syncing…" instead of a confident rank.
- [ ] InspectorSection collapse chevrons are rotating Tabler icons now (no ASCII > / v).
