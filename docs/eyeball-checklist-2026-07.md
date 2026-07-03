# Native eyeball checklist — v0.4.0 (+ M5-c PR #5)

Everything below shipped verified only by jsdom/tsc/race tests. Run the native
app (both light + at least one dark theme; the staleness/dim items in all 8 if
patience allows) against nelli + at least one AKS cluster.

## Triage semantics (the point of the release)
- [ ] A **stale** Flux resource (edit a Kustomization spec, don't wait): row dims to 0.6,
      status shows grey "Syncing…" chip, tooltip names both generations. Clears when
      the controller catches up. Same for a Certificate and a Gateway listener edit.
- [ ] `since` column (was "applied") on GitOps rows reads sensibly.
- [ ] Argo: a Suspended app does NOT appear in broken-first; healthy ApplicationSets
      no longer show as broken; multi-source app shows a revision.
- [ ] cert-manager: a mid-renewal cert shows **Issuing** (never Failed); chain inspector
      headline is the bound issuer; deepest stuck node flagged.

## Safety flows
- [ ] prd-lock: on a protected cluster, any destructive verb requires the typed name;
      confirm fires exactly one action (grant is one-shot — a second identical action
      without re-confirming must refuse with a clear error).
- [ ] YAML apply band on a protected cluster shows the typed-confirmation input.
- [ ] Argo sync modal: Prune defaults OFF; protected clusters arm before sync.
- [ ] Drain: two concurrent drains fine; a third refuses (no silent kill).

## Error honesty
- [ ] Kill network/revoke RBAC mid-view: GitOps + Certs show the red watch-failing
      banner instead of an empty list; Gateway list shows "could not list", not "no Gateways".
- [ ] Busy cluster events: "first 500 only" hint appears (all-namespaces on PRD).

## Capability gating + nav
- [ ] Sidebar on a cluster without cert-manager/Argo/Flux/Gateway API: those groups are
      absent entirely (no dead-end menus); they appear when detection lands (~sync time).
- [ ] ⌘K "jump to namespace" actually lands in that namespace with live updates flowing.
- [ ] ⌘K selection scrolls into view when arrowing below the fold.

## Design-system normalizations (visual deltas to accept/veto)
- [ ] Chips: radius 11→3 in Pods/Events; GatewayTopology filter pills → FilterButton
      treatment (square-ish, sans); Overview version badges (soft variant, new padding).
- [ ] PolicyChip tones on LIGHT mode (was unreadable); check all five policy kinds.
- [ ] Virtualized Workloads + Instances: scroll behavior on 500+ rows; ScalePopover
      stays open through live updates; selection follows the workload, not the row index.
      Known tradeoff (accepted): on very narrow windows the vertical scrollbar sits
      inside the horizontal scroller.
- [ ] LogsPane on a chatty pod: CPU stays sane; level filters instant.

## M5-c (PR #5, if checked out)
- [ ] MeshStrip draws arcs: solid between the homelab pair (mutual), dashed amber if
      you break one side's peering config, dashed grey to any off-fleet peer.
- [ ] Arc tooltips name the direction ("A → B one-way: B does not list A").
- [ ] Legend appears only when dashed arcs exist; readable in dark themes.

## Sign-off actions
- Veto anything above → file it; approve → delete this file and unblock M11-d Renew.
