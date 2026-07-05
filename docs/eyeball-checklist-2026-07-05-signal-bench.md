# Native eyeball checklist — Signal Bench (2026-07-05)

Branch `feat/signal-bench`, direction C from `design/direction-c-signal-bench.html`.
Everything below is a visual language change verified headless-only; the four
design rules (notes §Design rules) are the review bar.

## Rule 1 — only faults glow
- [ ] Fleet with one broken and five healthy clusters: the red/amber lamps are findable from across the room; healthy lamps are dim, unlit, glow-free.
- [ ] In lists, success dots read dim (0.75) and muted dots read as unlit glass - no green glow anywhere.

## Rule 2 — blink is spine-only
- [ ] A cluster with broken workloads blinks red in the spine; the same cluster's in-content lamps are steady.
- [ ] macOS Reduce Motion on: nothing blinks anywhere.

## Rule 3 — danger gets shape
- [ ] Failed Flux rows and danger InspectorStatus badges lead with the small ▲.
- [ ] Drain/rollback (danger composers) wear the hazard tape stripe; delete confirms keep the triangle icon.
- [ ] prd-lock arming: LEDs fill per correct character, wrong characters stop the fill, the confirm reads "Drain — locked" until armed, then drops the suffix.

## Rule 4 — wells hold data, modules hold chrome
- [ ] Panels/dialogs/spine read raised (top edge + drop); the palette input, counter windows (summary strips, cluster card nodes/pods), and active filter pills read inset.
- [ ] Active FilterButton = pressed into a well; Chip on-state = inset tone ring; Badge edges are 1px full-tone rings on bench.

## Themes
- [ ] Fresh profile boots into Bench (dark); Bench Light passes the same checks (wells/edges/lamps visible on the warm light desk).
- [ ] Flip through the eight legacy themes: everything still renders flat-but-correct (no missing shadows/rings artifacts).

## Chrome
- [ ] FKeysBar shows under Pods/Flux/Certs/Nodes lists with correct counts and "broken first"; keys match reality (j/k, ↵, l, x, /).
- [ ] FleetSpine collapsed: lamp-over-code channels; current channel pressed; expanded rows same language.
- [ ] ClusterCard: name-plate lamp carries severity (rail is gone), counter wells legible at a glance, verdict footer unchanged.
- [ ] Palette: mono input in a well, selected row shows the illuminated left edge, verbs on the prd cluster carry "· prd lock" in the hint.
- [ ] Row selection everywhere uses the same illuminated inset edge; stale rows still dim 0.6 with the grey Syncing… chip.

## The bench itself (added after "looks nothing like C" - the shell was the identity)
- [ ] The app is modules floating on a darker desk with 8px gutters: mast, spine, nav, and content are separate machined blocks - nothing is joined by a hairline anymore.
- [ ] Lens lists (pods/flux/certs/eso/nodes/gateways/instances) sit IN inset wells inside the content module - list=well, inspector=module (rule 4, both directions).
- [ ] First boot after upgrade lands in Bench even with an old saved theme (one-time migration); choosing any theme afterwards sticks. "System" now follows the OS into bench/bench-light.

## Meters (first-class per-type summaries)
- [ ] Every lens opens with a meters strip of counter wells + its ordering note: fleet (diagnostic order), pods (severity order), workloads (rank), flux, argo (argo vocabulary), certs (soonest expiry), eso (staleness), nodes (by name), gateway (broken paths), helm (helm status), events (newest first).
- [ ] Fault cells appear ONLY when the count is non-zero - a healthy view shows totals + green, never a row of red zeros.
- [ ] Argo meters speak Argo words (degraded/outofsync/progressing/synced), Flux meters speak Flux words - never blended.

## Screenshot-diff polish (pic1 vs pic2, 2026-07-05)
- [ ] No breadcrumb/big-title header inside the content module - the mast alone carries "cluster / lens" (display name, not the raw kubeconfig context), with the amber prd-lock chip beside it on protected clusters.
- [ ] Spine spans beside the nav rows with its "fleet" cap label.
- [ ] Tabs carry fault lamps + red counts when loaded data has faults (workloads/pods/flux/argo); labels read "ext-secrets"/"crds".
- [ ] Meter wells read as counter windows: 16px values, ≥86px, labels beneath.
- [ ] Pods filters are counted switches (all N / broken N), pressed-well when active.

## Seamless pass (pic3 feedback)
- [ ] Triage is one compact signal row: unlit lamps + "quiet" for healthy lenses, lit lamp + bold toned count for faults; no boxed cells, no column dividers, no band background. Clicking a lit signal still jumps to the filtered lens.
- [ ] Panel verdicts are one element: lamp + state word in a toned ringed box, mono sub-line "reason · detail" - not three disconnected chips.
- [ ] Every namespace select and filter input is an inset well (mono, no OS chrome, chevron on selects) - pods, workloads, events, gateway, certs, eso, flux, helm, argo, instances, logs grep.
- [ ] The workloads status caption reads as an engraved note (small tracked uppercase), not a second row of body text.
