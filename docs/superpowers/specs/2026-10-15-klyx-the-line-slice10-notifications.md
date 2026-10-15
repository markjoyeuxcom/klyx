# The Line, slice 10: health notifications

Design note for slice 10 of `2026-10-14-klyx-the-line-redesign.md` (parent).
- **Parent sections:** §8.6 (health notifications), with §14.9's defaults. The parent is the contract; this note records what exists, how the slice is cut and the points the parent leaves open.

## 0. What exists (surveyed 2026-10-15)

- **The transition stream** (slice 2b): every ledger part's commits reach `HealthSink.Transitions`, and through it every Go `HealthListener` (`OnTransitions`), on the Observer's loop goroutine, at commit.
  - A `TransitionBatch` is `baseline`, `changes`, `after-gap`, `loss` or `end`. It carries its cluster, Observer incarnation, task, part and source, its mode, its identity (continuity) and `AfterCap`.
  - Its items are `{Key (UID), NS, Name, GVR, From, To, Label, Reason, Since, Restated, Derived}`. `From` and `To` are a health role (`fault`, `degraded`, `unknown`, `progressing`, `paused`, `ok`) or a state (`absent`, `gone`, `unevaluated`).
  - The route and applied histories already listen this way.
- **OS notifications:** `ShellService.Notify` is called by the frontend for action outcomes while the window is unfocused, with authorization requested lazily on first use.
  - The Wails notifications service sends with a `Data` map and reports clicks through `OnNotificationResponse` (the response's `ID` and `UserInfo`). Klyx does not register for responses yet.
- **Settings:** `app/SettingsView.tsx` has panels (AI, Git). fleet.yaml's optional top-level blocks are written by `ConfigService` (`saveTopLevelBlock`).
- **In-app history:** the Activity log (persisted, `actionLog`) and toasts.
- **Focus:** the frontend knows whether the window is focused and which cluster its route shows; Go knows neither.
- **Opening an object by UID:** `reveal(cluster, task, part, ref)` with `ref.uid` pins it in its lens (slice 9c-1).

## 1. Sub-slices

| | Builds |
|---|---|
| **10a** | Go: `internal/healthnotify` (the episode machine, the triggers, de-duplication and coalescing; pure, clock-injected) and `appbridge.NotificationsService` (a `HealthListener` feeding it; the `notifications` block in fleet.yaml; focus from the frontend; sending through the OS service with the object in `Data`; clicks and baselines as events) |
| **10b** | Frontend: Settings > Notifications, focus reporting, the baseline listing in the app, and a click opening the object pinned to its cluster and UID |

Each is Codex-reviewed on its own, 10a first.

## 2. 10a: the episode machine

### 2.1 Inputs

- **Watched sources only.** A batch whose `Mode` is `sampled` is ignored. Today that is Helm CLI releases, keyed by namespace and name with no GVR (`healthsrc/helm.go`), so a notification could neither tell a replacement apart nor open the release pinned to a UID. Slice 2b §8 permitted sampled episodes; this slice leaves them out. An item with an empty `Key` is ignored too.
- **`Restated` and `Derived` items change nothing** (slice 2b §8). A derived item is a time-only re-evaluation of an earlier read, not a new observation: it never opens, closes or resumes an episode, and it does not move the retained evidence either.
- **A fault** is the role `fault`. Every other role is not a fault.
- **Keyed by cluster and Observer incarnation.** A batch from a newer incarnation drops the cluster's episodes, evidence and pending items before it is applied (the parent's "incarnation change: re-baseline everything"). A late batch from an older incarnation is ignored. De-duplication outlives incarnations.

### 2.2 Evidence per partition, folded per UID

The Observer may count one source's objects in several parts, so one UID can carry a role in several partitions `(task, part, source)`. The machine keeps each partition's evidence apart and folds it per UID.

- **Evidence:**
  - For each partition, the machine keeps the UIDs whose role there is **not ok**, each with its role.
  - An ok or absent UID is not kept: either way, the partition holds nothing against a recovery.
  - Each partition also has a **coverage** flag, lost at a `loss` and restored by its `after-gap` or `baseline`.
- **Applying a batch** to its partition:
  - **`baseline`:** replaces the partition's evidence with the batch's non-ok items (a baseline lists every entry), and restores coverage;
  - **`changes`:** each item sets the UID's role in the partition. An item into ok or `gone` removes it;
  - **`loss`:** marks the partition's coverage lost and keeps its evidence;
  - **`after-gap`:** applies the gap's items as `changes` does, then restores coverage. A UID the delta does not name keeps its **retained** role: an empty delta never resumes an episode whose retained role is unknown;
  - **`end`:** clears the incarnation's state (§2.4).
- **The fold** of a UID, over every partition that holds it, in order:
  1. **fault:** some partition with coverage holds it as `fault`;
  2. **suspended (coverage):** some partition without coverage holds it as `fault`;
  3. **suspended (unknown):** some partition holds it as `unknown` or `unevaluated`, or holds any non-ok role without coverage;
  4. **quiet:** some partition holds it as `degraded`, `progressing` or `paused`;
  5. **clear:** no partition holds it.
- **Worked cases:**
  - P1 and P2 both hold UID a as `fault`. P1's change to ok leaves the fold at fault, so the episode stays.
  - P1's loss suspends only P1's evidence. P2's covered fault keeps the fold at fault.
  - P1's baseline without a leaves P2's evidence alone.

### 2.3 Episodes

An episode is per UID. It opens when the fold becomes **fault**, and the batch that brought the fold there decides what kind it is. Every other change of the fold is applied to the open episode.

| The fold becomes | With no open episode | With an open episode |
|---|---|---|
| **fault** | opens. From `changes`: a **new** episode, eligible to notify (§3). From `baseline`: a **baseline** episode, listed once in the app, never notified. From `after-gap`: an **after-gap** episode, listed, never notified | resumes, silently (a suspended one included) |
| **suspended** (coverage or unknown) | nothing | suspended. No recovery is claimed |
| **quiet** | nothing | stays open and quiet: not a recovery. A return to fault is the same episode |
| **clear** | nothing | **closes**: see below |

**Closing an episode:**
- **Recovered:** the fold clears through a `changes` item into ok. Recovery eligibility is checked as usual, whatever the episode passed through: fault → unknown → ok and fault → degraded → ok both recover on the final ok.
- **Deleted:** the fold clears through a `gone` item. It closes silently, with no recovery.
- **Silently:** the fold clears through a `baseline` replacement or an `after-gap` delta. The moment of recovery is unknown, so no recovery is claimed. A later fault is a new episode.
- **A replaced object** has a new UID, so a new episode.

### 2.4 End and reincarnation

An `end` batch, or a batch from a newer incarnation:
- closes every episode of the cluster's older state, silently;
- clears its evidence;
- cancels its pending notifications (§3.3).

## 3. 10a: triggers, de-duplication and delivery

### 3.1 Settings and triggers

- **Settings** (`notifications:` in fleet.yaml, written only by Settings > Notifications, through the existing block writer with its validation and defaults):
  - `enabled` (default false: off until enabled, §14.9);
  - `scope`: `protected` (the default once enabled) or `all`;
  - `recoveries` (default false).
- **The machine runs while disabled,** so enabling notifies only onsets after it. Disabling cancels every pending notification at once.
- **Eligible:**
  - **an onset:** a new episode (§2.3) on a cluster in scope. Protected means the cluster's effective protection, as the destructive-verb guard reads it.
  - **a recovery:** a recovered episode whose onset was **successfully sent**, when recoveries are on and the cluster is in scope. A queued onset, a focus-suppressed one, one that failed to send, and one withheld by de-duplication do not count as sent.

### 3.2 De-duplication

- At most one notification per cluster, UID and role (fault or ok) per 30 minutes.
- **The allowance is consumed at dispatch** (§3.3): a sent, a focus-suppressed and a failed notification all consume it. A successful send is recorded separately (§3.1).
- An onset withheld by de-duplication is not queued, and its episode does not count as sent.

### 3.3 Pending items and delivery

**A pending item** is an **onset** or a **recovery**. It names its episode, its object and the time it was observed. Eligible items accumulate per cluster: the first opens a 60-second window, and the window's end dispatches them.

**At dispatch,** each item is re-checked against the current state. The settings (enabled, scope, recoveries) are read then, not when the item was queued.

| Item | Dropped when | Otherwise |
|---|---|---|
| **onset** | its episode has closed (recovered, deleted, silently or ended), or is no longer the episode that queued it | sent. An episode suspended since queueing still goes, as a historical observation: "observed 14:02; coverage lost since" or "…; now unknown". It never implies the verdict is still current |
| **recovery** | a new episode has opened for the UID since | sent |

- **Cancelled outright:** every pending item of a cluster at `end` or reincarnation (§2.4), and every pending item when notifications are disabled.
- **Focus:** nothing is sent while the app is focused on that cluster. A suppressed item consumes its de-duplication allowance and is not sent.

**Wording:**
- **Title:** the cluster place ("prd · North Europe").
- **One onset:** `<Kind> <namespace/name>: <verdict label>`.
- **One recovery:** `Recovered: <Kind> <namespace/name>: <label>`.
- **Several:** "N new faults and M recoveries on <place>: " (each part only when non-zero), then the first three items' lines and "+K more". The counts are the items that survived the re-check.

**The click:**
- A single item carries `{cluster, task, part, gvr, namespace, name, uid}` in `Data`. The task and part are those of the partition that opened the episode. A click reveals the object in that part's lens with its UID.
- A multi-object notification opens its cluster. This clarifies the parent's single-object click rule (§8.6).

**Off the Observer loop:**
- `Apply` runs on each cluster's Observer loop and only records state and signals.
- A one-second tick composes what is due under the engine's lock, then sends and lists **after releasing it**, so a blocking OS call never holds up a loop.
- Send outcomes are recorded afterwards, against the episode that queued the item.

### 3.4 Bounds and saturation

Every retained map is bounded per cluster. A full one fails closed.

| State | Bound | When full |
|---|---|---|
| **open episodes** (the only retained episodes: a closed episode is dropped at once) | 10,000 | a fault that would open another episode does not open one and is never notified. The cluster is **saturated** |
| **evidence** (non-ok entries, summed over partitions) | 20,000 | the partition that cannot record an entry is marked saturated until a baseline of it fits. The cluster is **saturated** |
| **de-duplication** (expired entries pruned first) | 10,000 unexpired | an item that would need a new entry is dropped, never evicting an unexpired guard. It is counted in the saturation listing |
| **pending items** | bounded by the two above: at most one onset per open episode, and one recovery per de-duplication entry | - |
| **in-app listings** | the last 20 | the oldest is dropped |

**A saturated cluster** sends nothing:
- its pending items are cancelled;
- it is listed once in the app: "Health notifications paused on <cluster>: more than 10,000 open faults" (or "…objects not ok");
- it resumes when every saturated partition has had a baseline that fits and the open episodes are below the bound.

A paused fold could be missing evidence. Sending from it could claim a false recovery or repeat an onset, so a saturated cluster sends nothing.

## 4. 10b: the frontend

- **Settings > Notifications:**
  - "Notify me about new faults" (off);
  - a scope choice (protected clusters, every cluster);
  - "Also notify recoveries".
  - Enabling requests the OS permission at that moment, through `ShellService`. When it is refused, the setting is kept and the panel says the OS has notifications off for Klyx.
- **Focus:** the frontend reports `{focused, cluster}` on window focus and blur and on every route change (`NotificationsService.SetFocus`).
- **The listings:**
  - a connect's partitions are gathered over 2 seconds into one listing per cluster ("3 faults present at connect on prd: Pod shop/a, …"), and likewise after a cap or a gap;
  - a saturated cluster's pause (§3.4) is listed too.
  - Each listing becomes one Activity entry and one toast of a new `info` kind (neither a success nor a failure). It is never an OS notification.
  - The service keeps the last 20 listings, numbered. The webview subscribes to `health:baseline`, then reads `RecentBaselines()`, and lists each number once, so a cluster that connected before the webview loaded is still listed.
- **A click** (`health:notification-open`) brings the window forward and reveals the object in its part's lens with its UID, which pins it (slice 9c-1). A multi-object notification opens its cluster.

## 5. Testing

- **10a:**
  - **The fold and the episode table,** transition by transition:
    - baseline faults never notified;
    - a new episode;
    - unknown suspends and resumes silently;
    - fault → unknown → ok and fault → degraded → ok recover on the final continuous ok;
    - gone deletes;
    - loss suspends;
    - after-gap opens, closes silently and resumes from retained evidence;
    - an empty after-gap delta keeps a retained unknown suspended, so a later genuine ok → fault is a new episode;
    - end and reincarnation;
    - replacement;
    - sampled batches, empty keys, and restated and derived items ignored.
  - **Partitions:** conflicting parts (P1 to ok while P2 holds fault keeps the episode), one-part loss, and one-part replacement by a baseline.
  - **Triggers:** protected only, all, recoveries, and disabled with no backlog.
  - **Pending items:**
    - a delivered onset followed by a recovery;
    - no recovery for a queued, suppressed, failed or de-duplicated onset;
    - a mixed burst's wording and counts;
    - recover-before-send and delete-before-send;
    - a suspended onset sent as historical;
    - end and reincarnation before the flush;
    - disabling, and narrowing the scope, during the window.
  - **De-duplication** across 30 minutes, and focus suppression consuming the allowance without counting as sent.
  - **Bounds:** turnover past each bound; a full episode map and a full evidence map pausing the cluster and resuming after a fitting baseline; a full de-duplication map dropping rather than evicting; a repeated UID.
  - **Off the loop:** a `Send` that blocks never blocks `Apply`.
  - **The service:** sending with `Data`, the click event, and the config block.
  - **End to end:** fed by a real Observer through `HealthSink`.
- **10b:** the Settings panel (lazy authorization, refusal shown), focus reporting, the listings, click handling (opens pinned to the UID; a multi-object notification opens the cluster), and headless captures of the panel in both themes.

## 6. Rulings (design review of 3f67f1c and 48ee4e9)

Codex accepted the seven open points, with these clarifications:
1. **Only `fault` opens an episode.**
2. **degraded, progressing and paused keep an episode open and quiet;** an eventual ok still closes it (§2.3).
3. **A recovery requires a successfully sent onset.** Queued, focus-suppressed or failed sends do not qualify (§3.1).
4. **Focus suppression consumes the de-duplication allowance only,** kept apart from the send status (§3.2).
5. **A multi-object notification opens its cluster,** recorded beside the parent's single-object click rule (§3.3).
6. **Settings live in fleet.yaml,** through the existing block writer, with its validation and defaults.
7. **The machine runs while disabled,** with no backlog on enabling; disabling cancels pending delivery (§3.1).

**A suspended onset** that was genuinely observed before its coverage was lost, or before it became unknown, is still sent: as a historical observation, with its observation time and its qualification (§3.3). This never covers an onset discovered after a gap.

The review's five design findings are answered in §2.1 (sampled sources, derived items), §2.2 (evidence per partition), §2.3 (settling from retained state), §3.3 (the pending items' kinds, lifecycle and cancellation) and §3.4 (bounds and saturation).
