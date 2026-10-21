# Native eyeball checklist - The Line (2026-10-15)

Branch `feat/the-line`, spec `docs/superpowers/specs/2026-10-14-klyx-the-line-redesign.md`.

Automation already covers what can be asserted (slice 12 note §7 and §8):
- the headless walk, in both themes at two widths;
- the native WKWebView smoke: every destination, tokens, fonts loaded, the inspector layout;
- the parity gate.

What follows is for a person at the native app. Run it in **night** and again in **day** (Settings > Appearance), against the **kind lab (`kind-klyx`) only**: every step that changes something names a disposable lab object and puts it back. Each item is an action, then what you should see.

## The fleet board
- [ ] **Open Fleet.** One sentence leads the page and says what is compared, and the legend reads "Lit: matches the comparison revision. A comparison, not a delivery path."
- [ ] **The board.** Stations sit on tracks by rank, and segments are lit only between stations that match. An unreachable station is greyed with its connection word, never "healthy".
- [ ] **Needs you.** Rows run prd first, then fault before look-at. Each shows its mark, kind, name, label and station tag. All, Prd only and Faults filter it, and the choice survives leaving and returning.
- [ ] **The aside.** The Ledger lists revisions on the line (with a Git repo configured: the commits not yet everywhere, with Open in Git), and the Horizon lists the next 30 days of expiries.

## A station
- [ ] **Open a cluster.** The page is titled by the station, followed by two lines: the health reading with its mark, then where the station stands on the line with its own mark. A failing line never colours the health line, and the reverse holds too.
- [ ] **The station's Needs you** has no filters. Clicking a row opens that object, pinned.
- [ ] **Below it, in order:** Resources beside the cluster facts, Recent warning events, Resource risks, By area, and Connection last.

## Lenses
- [ ] **Rows are 40px.** The first column is the mark:
  - a filled square for a fault;
  - a hollow square for look-at;
  - dotted for checking;
  - dashed for syncing;
  - nothing for a healthy row.
- [ ] **No ▲ sits beside a mark,** in a row or a header. Danger dialogs still carry their triangle and hazard tape.
- [ ] **Headers read as sentences,** for example "17 pods ■ 4 need attention" or "4 sources ■ 1 not ready". A header's mark agrees with the rows it counts.
- [ ] **Events have no mark column:** warnings are amber text.
- [ ] **A pending change** (`kubectl --context kind-klyx -n shop scale deployment/web --replicas=4`, then back to 3) shows the dashed mark and "Syncing…" while it is pending, never a confirmed healthy row, and returns to quiet once reconciled.

## Inspectors
- [ ] **Open any object.** The inspector names the kind, the name and the meta. The verdict leads with its mark (no ▲), then the drawing (Flux Dependencies, the cert-manager or ESO chain, the Gateway data path), then the actions, then the evidence.
- [ ] **Open one of the `shop/web` pods, then delete it** (`kubectl --context kind-klyx -n shop delete pod <that pod>`; the `web` Deployment recreates it under a new UID). Its open inspector says Removed, and never silently shows the replacement.
- [ ] **Narrow the window** below about 1100px. The inspector becomes a drawer over the list, and Escape closes it.

## The console (⌘K)
- [ ] **Type a pod's name.** Objects from every cluster are listed with their mark, station and note. Enter opens one pinned, and ⌘Enter opens it beside, moving the console to the left.
- [ ] **Type a lens name as the index shows it** ("Workloads", "Helm", "Flux CD"). Enter opens that lens.
- [ ] **alt+V** shows only verbs. With an inspector open, an empty console lists that object's verbs.
- [ ] **With the inspector drawer open,** ⌘K then Escape closes only the console.

## Health notifications
These are not automated: they show real OS notifications.
- [ ] **Settings > Notifications: turn them on.** macOS asks for permission once, and if you refuse, the panel says so.
- [ ] **Set the scope to every cluster** (`kind-klyx` is not protected). Then, with Klyx showing another page or in the background, **break the lab's `shop/web`** by changing its image to one that does not exist: `kubectl --context kind-klyx -n shop set image deployment/web nginx=nginx:does-not-exist`. Within about a minute, one notification arrives, titled with the cluster's place. Nothing arrives while Klyx shows `kind-klyx`.
- [ ] **Click the notification.** Klyx comes forward with the object open and pinned.
- [ ] **With "Also notify recoveries" on, restore it** (`kubectl --context kind-klyx -n shop rollout undo deployment/web`). A recovery notification arrives; with the option off, none does. Leave `shop/web` on `nginx:1.27`.
- [ ] **Reconnect a cluster** (or restart Klyx). The faults already present are listed once in Activity, never notified.

## Themes and accessibility
- [ ] **Night and day.** Text is readable on every surface, marks are distinct, and focus rings are a 2px route-coloured ring.
- [ ] **The bench themes** stay selectable in Settings as "(retiring)". They look flat, with no bevels, glows or pressed wells.
- [ ] **macOS Reduce Motion on:** nothing animates except opacity.

## Native
- [ ] **Quit and relaunch.** The window returns to its size and place.
- [ ] **The dock badge** counts as before.
- [ ] **Trackpad scrolling** is smooth in long lists (Pods with many rows, Events).
- [ ] **Zoom (⌘+ and ⌘-):** the layout reflows, with no clipped headers or overlapping rows.
