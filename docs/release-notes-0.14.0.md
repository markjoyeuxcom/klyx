# Klyx v0.14.0

v0.14.0 is **The Line**, a redesign of the whole app from the owner's "Klyx redesign" mockups. The redesign also adds:
- the data those mockups needed and Klyx did not yet read;
- read-only Git history;
- health notifications;
- a command console;
- a new Describe.

It also ships v0.13.1's changes, which were written up but never published.

Every slice was reviewed by Codex. The spec is `docs/superpowers/specs/2026-10-14-klyx-the-line-redesign.md`, with one note per slice beside it.

## The look

- **Night and day themes.**
  - Night is the default.
  - A one-time migration moves bench to night, bench-light to day, and any retired palette to night.
  - The classic light and dark themes stay. Bench is still listed while it retires. Settings > Appearance chooses, and System follows the operating system.
- **Bundled type.** Barlow, Barlow Condensed and DM Mono (SIL OFL 1.1) ship inside the app. They are never fetched from the network.
- **Marks by shape, not only colour.**
  - A fault is a filled square, and look-at is a hollow one.
  - Checking is dotted, syncing is dashed, and paused is a plain outline.
  - A healthy object carries no mark.

  Nothing blinks. Signal Bench's lamps, machining and glow are retired.
- **One scale** (the design-fidelity pass):
  - 22px lens titles, 36px controls and 40px rows;
  - tables as panels that end after their last row;
  - status words in plain type;
  - inspectors ordered verdict, drawing, actions, evidence.

  Each lens is titled by its index name. Flux CD and Argo CD switch between their parts with one control that carries each part's count.

## The fleet: The Line

- **The fleet drawn as a railway line.**
  - Regions or groups are tracks, and environments are stations along them, dev through prd. ClusterMesh peerings are links between the tracks.
  - A segment is lit only between two stations that match the comparison revision. The board says what it compares and how the comparison was chosen. It is a comparison, not a delivery path.
- **Needs you.** One queue across the fleet: prd first, then fault before look-at. It filters by All, Prd only and Faults.
- **The Ledger and the Horizon.**
  - The Ledger lists the revisions on the line. With Git, it lists the commits not yet everywhere.
  - The Horizon lists the next 30 days of expiries.
- **The station page.** It opens with the station's health reading and where it stands on the line, each with its own mark. Then come:
  - Needs you;
  - resources and cluster facts;
  - recent warning events;
  - resource risks;
  - By area;
  - Connection.

## Chrome

- **A location bar** (fleet, station, lens, object) with a fleet indicator.
- **A lens index** that puts what needs you first and lists everything else after it.
- **A station strip.**
- **Inspectors pinned to their cluster and UID.** When the object at a name is replaced, the inspector says "Replaced" and offers the new one. It never shows the new object as the old.

## Read-only Git history

- **Settings > Git** approves repository URLs, which run exactly as written.
  - Klyx uses the system `git` (2.45 or newer) with your own credential helpers, and keeps a history cache.
  - It never writes to a working tree or to a cluster.
- **With history,** the line orders each station against the target revision: behind, ahead or held. The Ledger lists the commits, with Open in Git.
- **Flux "Compute diff"** builds from the cache when the Kustomization's repository is approved:
  - a confined build of the exact commit;
  - then a server-side dry run with Flux's own field manager.

  Any other Kustomization still uses the flux CLI.

## Drawings

- **The Flux dependency graph:** typed edges, supply facts and change proximity.
- **The cert-manager issuance chain and the External Secrets chain,** drawn as rails.
- **The Gateway data path:** configured weights and traffic measured per rule.
- **The chat read ledger.**

## New data

- Observer rows with rule-backed "since" and reason.
- A transition stream and an observation log, for "held since … (observed)".
- The last recorded managed-field update of an object.
- Route metrics per rule.

## The command console (⌘K)

- **Search the whole fleet** through a metadata index, beside Run, Go to and Ask. The coverage line names any gap.
- **Open results:** ↵ opens an object pinned to its UID, and ⌘↵ opens it beside the current one.
- **Verbs only:** alt+V lists just the verbs.

## Health notifications

- **Off by default.** Turn them on in Settings > Notifications, which also sets the scope and whether recoveries notify. The OS permission is asked when you turn them on.
- **A click on a notification** opens the object, pinned to its UID.

## Describe

- **A Formatted view** of `kubectl describe`:
  - sections with an index;
  - key/value rows;
  - conditions as a table, with a fault marked by its own polarity;
  - events and other tables as real tables;
  - ages beside times.
- **A Text view** that is kubectl's output, byte for byte.
- **Find, Copy and Refresh.**
- **Copy command,** with the cluster's kubeconfig pinned in it, so a context name shared by two kubeconfigs can't reach another cluster from your terminal.
- **A notice** when the report describes a different object at that name.
- **Describe in every inspector's More:**
  - Flux;
  - Argo;
  - cert-manager;
  - External Secrets;
  - Gateways, routes and the topology;
  - workloads;
  - Karpenter.

## From v0.13.1

v0.13.1 was never published, so its changes ship here. The details are in `docs/release-notes-0.13.1.md`.
- **The Claude Code CLI admits any build at or above 2.1.296** once Test has checked it on this Mac.
- **Chat chips** name their kind and say how many they hold back.

## Known limitations / not yet verified

- **The Line is verified on the kind lab only.** That includes:
  - the native WKWebView smoke (114/114 in night and day at 1440 and 1024);
  - the headless walks;
  - the parity gate.

  It has not run against the AKS fleet. The native eyeball checklist `docs/eyeball-checklist-2026-10-15-the-line.md` is still open.
- **Git history** needs each repository approved in Settings > Git, and system `git` 2.45 or newer. Semver ranges, default branches and unqualified Argo refs are not fetched.
- **Describe's Formatted view** is a best-effort reading of kubectl's text. Where kubectl's text is ambiguous it shows plain lines, and the Text view is the authority. Describe needs `kubectl`.
- **A few ▲ marks remain:** Gateway topology, the Karpenter controller banner, Upgrade check verdicts, and log, settings and AI error lines. Danger dialogs keep ▲ on purpose.
- **No Linux arm64 build,** as before.
- **Every v0.13.0 and v0.13.1 limitation still applies,** and so does the v1.0.0 gate in `docs/backlog.md` §0.
