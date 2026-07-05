# Signal Bench — implementation notes

Mockup of record: `docs/direction-c-signal-bench.html` (10 screens).
Theme draft: `docs/tokens-signal-bench.css` (`bench` / `bench-light`).

Most of the direction lands as CSS variables. Three things are new primitives.
Everything else is a restyle of existing components in place.

## Design rules (enforce in review, not just CSS)

1. **Only faults glow.** Success/muted lamps are dim and unlit. Glow tokens
   exist only for `warning` and `danger`. If a PR adds glow to anything else,
   it's wrong.
2. **Blink is spine-only.** `.klyx-lamp-blink` may appear only inside
   `FleetSpine`. In-content lamps are steady. `prefers-reduced-motion`
   disables blink entirely.
3. **Danger gets shape.** Danger status text carries a `▲ ` prefix
   (CSS `::before` on the danger tone), dialogs keep the alert-triangle icon
   and the hazard stripe. Red vs amber must never be hue-only.
4. **Wells hold data, modules hold chrome.** Lists, log panes, counter
   windows, command previews and inputs are wells (inset). Panels, navs,
   dialogs and buttons are modules/keys (raised). Nothing is a flat
   hairline-bordered card anymore.

## New primitives (3)

### 1. `Lamp` — replaces `StatusDot` (chrome/primitives/StatusDot.tsx)
8px circle. Props: `tone` (existing `Tone` type), `blink?: boolean`
(FleetSpine only), `size?`.
- ok: `--color-text-success` at 0.75 opacity, no glow
- warning/danger: full tone + `--lamp-glow-*`
- muted/off: `--lamp-off` with inset top shadow (unlit glass)
- unreachable: transparent + 1px dashed `--color-text-tertiary`
Same call sites as StatusDot; the tone maps in `primitives/styles.ts` are
unchanged.

### 2. `well` — a variant beside `frame` (primitives/styles.ts)
```ts
export const well: CSSProperties = {
  background: "var(--bench-well-bg)",
  borderRadius: "var(--radius-sm)",
  boxShadow: "var(--bench-well-shadow)",
};
```
`frame` itself becomes the module recipe:
`borderTop: 1px solid var(--bench-edge-hi); boxShadow: var(--bench-module-shadow)`
(replaces the 0.5px hairline border — the deliberate identity change of this
direction).

### 3. `FKeysBar` — new list-footer component
Permanent keyboard chrome (htop heritage): `<FKeysBar keys={[["j k","nav"],
["space","select"],["↵","inspect"],["l","logs"]]} status="14 of 214 · broken first" />`.
Replaces the ad-hoc footer text in PodsView/GitOps/CertsView/NodesView. The
`useListKeys` hook already owns the bindings; this just makes them visible.

## Restyles of existing components (no API changes)

| Component | Change |
|---|---|
| `Badge` / `Chip` | tone edge becomes `box-shadow: inset 0 0 0 1px <tone>`; verdict badges pair a Lamp with the word |
| `SummaryCell` | becomes a counter well: value in a `well`, 9px tracked uppercase label beneath (`meters` strip in mockup) |
| `FilterButton` | active = pressed into a well (inset shadow); inactive = flat text |
| row selection (`useListKeys` consumers) | `background: --bench-selection-bg` + `box-shadow: inset 2px 0 0 --bench-selection-edge`; stale rows keep 0.6 opacity + grey "Syncing…" chip |
| `btn` / `actionBtn` | raised key: `border-top: --bench-edge-hi`, bottom edge `--bench-edge-lo`, kbd hints inline (see `.abtn`) |
| `FleetSpine` | code blocks become channel strips: Lamp above 9px mono code; current = pressed well. Only place `blink` is allowed |
| `ClusterCard` | module face: Lamp + name plate header, two counter wells (nodes/pods), lamp-per-signal rows, verdict footer. Drops the 3px left rail — the lamp carries severity |
| `MeshStrip` | patch-bay: port circles (well + tone pin), cables neutral unless the peering is wrong |
| `OperationDialog` | hazard stripe header strip, `OptionTile` gains a physical toggle switch, prd-lock input gains the LED interlock progress row (`armProtected` flow unchanged); confirm label reads "… — locked" until armed |
| `CommandPalette` | input becomes a well; selected row uses the selection edge; destructive verbs show amber `prd lock` in the hint margin |
| `EmptyState` / `WatchErrorBanner` | see mockup screen 10 — banner keeps last-good data visible at 0.6 opacity with last-good timestamp + retry countdown; empty state proves the operator is healthy and offers next moves |

## Rollout

1. **Phase 1 — tokens only.** Ship `bench`/`bench-light` as themes 9/10.
   Everything keeps working; surfaces look flat but correctly coloured.
2. **Phase 2 — primitives.** Lamp replaces StatusDot; `frame`→module,
   add `well`; Badge/Chip inset rings. ~5 files
   (`StatusDot.tsx`, `primitives/styles.ts`, `Badge.tsx`, `Chip.tsx`,
   `FilterButton.tsx`).
3. **Phase 3 — chrome.** FKeysBar, FleetSpine channels, ClusterCard modules,
   MeshStrip patch bay, OperationDialog arming panel.

Phase 1 is reversible by deleting a CSS file; phases 2–3 change component
internals but no data flow, stores, or bridge code.
