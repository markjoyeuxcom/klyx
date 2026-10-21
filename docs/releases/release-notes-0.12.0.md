# Klyx v0.12.0

v0.12.0 is a polish and correctness release. It acts on two reviews of v0.11.1, both run jointly with Codex. A UI review found readability and navigation friction across the app. A code-quality review found ten defects: a Scratch panel that could delete on the wrong cluster, health verdicts that read healthy when they were not, and request lifecycles that let stale replies win. Every change was reviewed by Codex before it landed.

## Navigation and the command palette

- **Better palette ranking.** Results rank by a prefix match on the name first, then a match at the start of a word, then hints. Views come before verbs, and the list shows results in the order it ranks them.
- **New palette entries:**
  - Keyboard help.
  - Scratch, which asks which cluster when more than one is connected.
  - "Compare … across the fleet" for a workload.
  - Fleet compare from the fleet board, which asks for a cluster and then opens Workloads.
- **The keyboard cheatsheet is a proper dialog.** Tab stays inside it, and focus returns to where you were when it closes.
- **Fleet cards open from the keyboard** (Enter or Space).

## Readability

- **Text contrast.** Secondary and tertiary text in both bench themes now meets 4.5:1 contrast, and a test pins it.
- **A calmer Overview.** Areas flow in columns, and long controller reasons fold behind a disclosure. A connection problem is never hidden behind a summary of controller health.
- **Fleet cards** show Flux and Argo CD on their own lines, plus each area that needs attention, with its reason.
- **Syncing rows stay readable.** Only the status lamp dims; the row's text keeps full contrast.
- **One fault glyph.** Every fault uses the same ▲ marker.
- **Long controller messages** are clamped to a few lines, with a "Show full message" button.

## Wording and consistency

- **Settings says exactly what Klyx writes to fleet.yaml.** It appends clusters you add and saves the AI block. Settings > AI labels the active provider "In use:" and shows an "Unsaved changes" notice until you press Save.
- **Plainer words:**
  - "gaps" and "coverage" now read "unverified".
  - "raw resource view" is now "resource list".
  - The Flux header names its kinds.
  - The fleet tabs read "Promotion tracker" and "Expiry horizon".
  - The network view is "Gateways and routes".
- **Argo CD** no longer has a second view switch inside the lens.
- **Expiry horizon** uses the same table as the other lenses. A Subject column with no values is left out, and the footer says no subject was reported.
- **Karpenter** opens with the first failing NodeClaim already selected.
- **YAML** has a Wrap toggle. Unwrapped is the default, so indentation stays readable.
- On-screen prose uses hyphens instead of em dashes.

## Scratch panel

- **Switching clusters closes the panel.** A list read on one cluster can no longer drive a delete on another.
- **Deletes are pinned to the object you saw.** Klyx sends the listed UID as a server-side precondition, so an object recreated under the same name is never deleted.
- **The inventory covers everything the composer can create.**
  - It lists every served kind that supports list and delete, page by page, not a fixed set of 11.
  - Kinds it cannot read are named under "Inventory incomplete", and a failed read is an error, never "no scratch objects".
  - The composer refuses a kind Scratch could not list and delete.
- **Discovery errors are errors.** The cached discovery client now matches client-go: it includes the core API group, and any failure other than a 404 on the legacy `/api` root fails the read instead of returning fewer groups. This also keeps capability checks from reading a failed discovery as "not installed".

## Health verdicts

- **Gateway routes check every backend that carries traffic,** not only the first, and judge weights within each HTTPRoute rule.
  - A backend whose weight is unset carries traffic (the default weight is 1).
  - A rule whose backends all have weight 0 is a fault.
  - Within a rule, a backend fault is degraded while another backend of that rule still carries traffic.
- **EndpointSlice readiness.** An endpoint with no Ready condition counts as ready, as the API specifies.
- **External Secrets.** Only `Periodic` refresh can go stale. `CreatedOnce` and `OnChange` secrets never read as overdue, and the inspector names the refresh policy.
- **Node-loss headroom** uses the requests the scheduler actually reserves: init containers, sidecars, pod-level requests and overhead. A pod with a 4-core init container no longer "fits" a 2-core node.

## Request lifecycles

- **Resource lists and details.** Each read is numbered, so a late reply can never overwrite a newer one. A stale "load more" page, or its error, cannot land on a refreshed list.
- **Log streams.**
  - A stream reserves its slot before any network call, so a pending open counts toward the 8-stream cap.
  - Closing a stream cancels an open still in progress.
  - Opening a stream times out after 30 seconds, and the error says why.
- **Claude Code answers dropped after sending.** If Klyx discards an answer after the prompt reached Claude Code, the message now says so. It no longer claims "Klyx did not send this".

## Known limitations / not yet verified

- **No live-cluster check yet.** These fixes were tested with fakes and local HTTP test servers, not against a live cluster. The real-cluster smoke is part of the v1.0.0 gate.
- **Headroom** does not account for in-place pod resizes.
- **The Scratch panel's read cost.** Opening or refreshing it makes one filtered list call per served kind.
- **Every v0.11.x limitation still applies,** including the pending real-model AI smoke.
