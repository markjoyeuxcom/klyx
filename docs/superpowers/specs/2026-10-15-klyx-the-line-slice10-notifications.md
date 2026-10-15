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

- **Keyed by cluster, Observer incarnation and UID.** A batch from a new incarnation drops every episode of the cluster's older incarnations before it is applied (the parent's "incarnation change: re-baseline everything").
- **A fault** is the role `fault`; every other role is not a fault.
- **An episode:**
  - opened by a fault: `notified`, `baseline` or `after-gap`;
  - suspended, with `coverage` or `unknown`;
  - closed: `recovered`, `deleted` or `ended`.

| Batch / transition | Effect |
|---|---|
| `baseline` (connect, reconnect, after a cap) | a replacement: an episode of the source it does not show as a fault closes silently; each fault opens a **baseline** episode (or keeps its open one), listed once in the app with the connect's other faults, never notified |
| `changes`: non-fault → fault | an open episode for the UID stays as it is (one source may be counted by several parts); a suspended one **resumes** silently; otherwise a **new episode**, eligible to notify (§3) |
| `changes`: fault → `unknown` or `unevaluated` | **suspended (unknown)**; a return to fault resumes the same episode, silently |
| `changes`: fault → `ok` | **recovered**: closes the episode; notifies only if recoveries are enabled and the episode's onset was notified |
| `changes`: fault → `degraded`, `progressing` or `paused` | the episode stays open, quiet: not a recovery, and a return to fault is the same episode |
| `changes`: any → `gone` | **deleted**: closes with no recovery; drops the UID from any burst not yet sent |
| `loss` | every open episode of the batch's source is **suspended (coverage)**; no recovery is claimed |
| `after-gap` | the gap's changes. A non-fault → fault opens an **after-gap** episode: listed in the app with the reconnect, never notified. fault → `ok` closes the episode as recovered without a notification (its moment is unknown); fault → `gone` deletes. A suspended episode not in the delta resumes: its role held across the gap |
| `end` | the incarnation's episodes close as **ended** |

- A **replaced** object is a new UID, so a new episode.
- `Restated` items (same role, new since) change nothing. `Derived` items (a sampled time boundary) are transitions like any other.

## 3. 10a: triggers, de-duplication and coalescing

- **Settings** (`notifications:` in fleet.yaml, written only by Settings > Notifications):
  - `enabled` (default false: off until enabled, §14.9);
  - `scope`: `protected` (the default once enabled) or `all`;
  - `recoveries` (default false).
  - With `enabled` false the machine still runs, so enabling never notifies a backlog: only onsets after enabling notify.
- **Eligible:** a new episode on a cluster in scope (protected is the cluster's effective protection, as the destructive-verb guard reads it), or a recovery when recoveries are on.
- **De-duplication:** at most one notification per cluster, UID and role per 30 minutes (a bounded map, pruned by age).
- **Coalescing:**
  - The first eligible item on a cluster opens a 60-second window, and items accumulate in it.
  - At its end, items that have since recovered or been deleted are dropped. An onset suspended since (coverage lost, unknown) was still seen with continuous coverage, and stays. What remains is sent as one notification: one item as itself, several as "N new faults on <cluster place>" with the first names.
- **Focus:** nothing is sent while the app is focused on that cluster. Suppressed items count as sent for de-duplication, so losing focus never releases a stale notification.
- **Content:** title `<cluster place>`; body `<Kind> <namespace/name>: <verdict label>` (several: the count and the first three names).
- **Click:** a single item's notification carries `{cluster, task, part, gvr, namespace, name, uid}` in `Data`. A click emits `health:notification-open` with it, and the frontend reveals the object in its part's lens (`reveal`, with the UID). A burst's click opens the cluster.
- **Bounds:** at most 10,000 open episodes and 10,000 de-duplication entries per cluster (beyond that, the oldest closed state is dropped first, and an episode never opened is never notified); bursts are at most 50 items (the rest are counted).

## 4. 10b: the frontend

- **Settings > Notifications:**
  - "Notify me about new faults" (off);
  - a scope choice (protected clusters, every cluster);
  - "Also notify recoveries".
  - Enabling requests the OS permission at that moment, through `ShellService`. When it is refused, the setting is kept and the panel says the OS has notifications off for Klyx.
- **Focus:** the frontend reports `{focused, cluster}` on window focus and blur and on every route change (`NotificationsService.SetFocus`).
- **The baseline listing:** a connect's partitions are gathered over 2 seconds into one listing per cluster ("3 faults present at connect on prd: Pod shop/a, …"). It becomes one Activity entry and one toast of a new `info` kind (neither a success nor a failure). It is never an OS notification.
  - The service keeps the last 20 listings, numbered. The webview subscribes to `health:baseline`, then reads `RecentBaselines()`, and lists each number once, so a cluster that connected before the webview loaded is still listed.
- **A click** (`health:notification-open`) brings the window forward and reveals the object in its part's lens with its UID, which pins it (slice 9c-1).

## 5. Testing

- **10a:**
  - the machine, transition by transition, against the table above (baseline faults never notified; a new episode; unknown suspends and resumes silently; fault → degraded stays open; ok recovers; recoveries notify only a notified onset; gone deletes; loss suspends; after-gap opens, recovers silently and resumes; end; incarnation re-baseline; replacement);
  - triggers (protected only, all, recoveries; disabled never backlogs);
  - de-duplication across 30 minutes and coalescing across 60 seconds with a fake clock, including recover-before-send and delete-before-send;
  - focus suppression counting toward de-duplication;
  - bounds;
  - sending with `Data` and the click event;
  - the config block;
  - fed by a real Observer through `HealthSink` in a test.
- **10b:** the Settings panel (lazy authorization, refusal shown), focus reporting, the baseline listing, and click handling (opens pinned to the UID); headless captures of the panel in both themes.

## 6. Open points (for review)

1. **Only `fault` is a fault.** `degraded` never opens an episode.
2. **fault → degraded, progressing or paused** keeps the episode open and quiet: it is neither a recovery nor, on returning to fault, a new episode.
3. **Recoveries notify only episodes whose onset notified.** A baseline or after-gap episode recovers silently, and so does a recovery seen in an after-gap batch, whose moment is unknown.
4. **Suppressed by focus counts as sent** for de-duplication.
5. **A burst's click opens the cluster**, not one of its objects.
6. **Settings live in fleet.yaml** (`notifications:`), beside `git:` and `line:`.
7. **The machine runs while disabled**, so enabling notifies only onsets after it.
