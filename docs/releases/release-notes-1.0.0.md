# Klyx v1.0.0 — Signal Bench

The 1.0 release is a redesign and a declaration: the milestone roadmap is complete, the daily driver is real, and the app now has a visual language of its own — **Signal Bench**, direction C from the design exploration. The app reads as bench hardware: machined modules floating on a desk, data inset in wells, and lamps that only glow when something is wrong.

## The bench (new look, on by default)

- Two new themes, `bench` (dark) and `bench-light`, are the defaults. A theme saved before the upgrade migrates to its bench face once; any choice made afterwards sticks. "System" follows the OS between the two bench faces. The eight previous themes remain selectable.
- **Lamp physics**: status dots everywhere became lamps — danger and warning glow, healthy sits dim, idle reads as unlit glass. A quiet cluster is a dark bench; the broken thing is the lit thing.
- **Wells and modules**: lists, inspectors, inputs, selects, counter readouts and command previews are inset wells; panels, navs, dialogs and buttons are raised modules and keys. No flat hairline cards.
- **Blink discipline**: exactly one thing in the app may blink — a cluster with broken workloads, in the fleet spine. Reduced-motion disables it.

## Navigation, rebuilt

- The **mast** replaces the old title bar + header pair: a mono `cluster / lens` readout with the cluster's display name, a live lamp with the watching count, the prd-lock chip on protected clusters, and the ⌘K palette hint in a well.
- The vertical sidebar is gone. **Lenses are horizontal tab strips**: capability-gated groups (cluster, workloads, config … flux cd, helm, crds) with the current group pressed into a well, and the active group's items beneath. Tabs carry a red lamp + count when loaded data has faults — never a fake zero.
- The **fleet spine** is a channel strip: lamp over a two-letter code per cluster, the current channel pressed, unreachable clusters as unpowered sockets.

## Triage, made instrument-grade

- **Meters on every lens**: a first-class strip of counter wells with each list's ordering contract engraved beside it ("broken first · severity order"). Fault meters exist only while the fault does. Each lens speaks its own vocabulary — Argo says degraded/outofsync/synced, Flux says reconciling/suspended/ready.
- The **triage tape** is one compact signal row: a lamp per lens, quiet lenses unlit, faults lit and clickable straight to the filtered lens.
- Panel **verdicts** are a single element — lamp + state word in a toned ringed box with a mono reason line — shared by pods, nodes, workloads, helm and instance panels.
- A permanent **function-key bar** under the triage lists names the keys the lists already answer to (j/k, ↵, l, x, /) plus the honest "N of M · broken first" readout.

## Safety, made visible

- Destructive composers wear a **hazard-tape stripe**; options are physical toggle switches with their consequences printed beside them.
- The prd-lock typed confirmation is an **arming panel**: an LED interlock fills per correct character, and the confirm button reads "… — locked" until armed. The one-shot backend grant flow is unchanged.
- Danger is never hue-only: broken status text leads with ▲, dialogs keep the triangle.

## Also in this release (since v0.5.1)

- The GUI friction pass (PR #9): session restore, palette day-2 verbs through the existing confirm/composer flows, toast queue + persisted activity log, copy affordances, keyboard parity + "?" cheatsheet, cross-links between lenses, master-detail browsing for every resource kind, live-ticking ages, workload observedGeneration staleness, native window-state restore, dock badge and OS notifications, YAML highlight + find, bulk pod delete with per-call grant re-arming.
- Honest-UI fixes along the way: compact event counts (`×28k`, exact on hover), and the dead helm "repositories" tab removed rather than left lying.

## Notes

- Read-only lenses stay read-only; Git remains the source of truth. Klyx still never authors desired state.
- Eyeball checklists for this release: `docs/checklists/eyeball-checklist-2026-07-04-gui-pass.md`, `docs/checklists/eyeball-checklist-2026-07-05-signal-bench.md`.
