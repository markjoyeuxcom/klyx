# The Line, slice 3: the line model and LineService

**Parent spec:** `2026-10-14-klyx-the-line-redesign.md` §3 (the line), §3.6 (data lifecycle) and §12 (the line's table tests).

This note fixes the implementation shape the parent leaves open. The rules themselves (placement, units, keys, R, states, flags, held) are the parent's, implemented verbatim. Slice 3 is Go only: no UI.

## 1. Packages

- **`internal/line` (pure):**
  - placement;
  - comparison units and revision parsing;
  - keys and key selection;
  - R;
  - station states and flags;
  - held evidence;
  - the mesh rules.
- **Inputs are plain values:**
  - per station: the cluster's config (name, environment, `tags.region`, `group`);
  - the station's read state;
  - its Flux `Resource` and `Source` rows and its Argo `App` rows;
  - the mesh graph.
- **No client-go and no clock.** `now` is a parameter, so §12's tables run as table tests.
- **`internal/line/env`:** the environment ranking, moved out of `FleetSpine.tsx` into one module.
  - Go owns it. The frontend gets each station's rank on `LineDTO` and stops re-deriving it in slice 4.
  - `FleetSpine` keeps its regexes until then, behind a parity test against the Go table.

## 2. Acquisition: the Observer's shared rows, never a new watch

`LineService` (in `internal/appbridge`) builds facts only from rows the Observer already holds (parent §3.6).

- **It subscribes, per connected cluster,** to `flux/reconciliations`, `flux/sources` and `argo/applications`, with `SubscribeRows` and its own `Owner{Kind: "line"}`.
  - It holds the Flux and Argo domain leases, so those rows are built and broadcast on every part version.
  - It bootstraps each part with `RowsNowCtx`.
- **Rows reach it through a tap on `HealthSink`.** `HealthSink.Rows` hands each `observe.Rows` to registered Go listeners before the webview conversion.
  - The tap sees the raw `RowSet[flux.Resource]`, `RowSet[flux.Source]` and `RowSet[argo.App]`, with no second conversion.
  - The listener filters by cluster, incarnation, task and part.
- **The Observer's lifecycle drives the subscriptions.** The service also taps `HealthSink.Summary`:
  - a new incarnation of a cluster drops that station's facts (parent §3.6), then subscribes and bootstraps against the new Observer, found through the same `observers()` provider `HealthService` uses;
  - a Final summary marks the station checking (connection gone) and keeps its last good facts as "as of".
- **Fencing:**
  - A reply or push is applied only for the current incarnation of its cluster, and only when its part version is newer than the one held.
  - A bootstrap reply that loses a race to a newer push is discarded.
  - This is health 1c's rule.
- **Read state per station:**
  - **ok** when every part the station needs is live;
  - **partial** when some are live;
  - **stale** when one is stale (last good facts kept);
  - **error** when one is unreadable;
  - **checking** otherwise.
  - Each part's coverage comes from the same `Rows.Part`.

## 3. Recompute and push

- **On any accepted row change,** the service recomputes the whole line, which is cheap: a few hundred units per fleet. It then emits `line:dto` with a strictly increasing version, like the cluster summary.
- **Coalescing:**
  - Recomputes are coalesced to at most one per 250 ms. A pending recompute always runs, so the last change is never lost.
  - The frontend keeps a DTO only when its version is newer.
- **`LineService.Line()`** returns the current DTO, for bootstrapping (subscribe, then read).
- **`LineService.SetSource(key)`** switches the board's key for the session (the parent's "Selector"); it is never persisted. The `fleet.yaml` override (`line.source`) wins at startup.

## 4. `LineDTO`

- **Fleet:**
  - `version`;
  - `key`: the chosen comparison key, how it was chosen (most stations, override or selector) and its station count;
  - `keys`: every key, each with its station count;
  - `r`: the comparison revision (if any), its tie-break disclosure, and its headline numbers;
  - `tracks`: their labels and kind (region, group or fleet);
  - `siding`;
  - `fallback`: true when there is one row.
- **Per station:**
  - `cluster`, `track`, `rank`, `env`;
  - `state` (parent §3.3 precedence), with the facts E/A/U;
  - `flags`: "failing since", "compared X, applied Y", syncing, suspended, "source changed";
  - `held` and its evidence (attempted R, failing);
  - `incarnation`, `readAt`, the read state, and the last good facts' `asOf`;
  - `excluded` counts (OCI or Bucket sources, unparsable revisions).
- **Mesh:** the drawable links (same rank, adjacent tracks), the station-card-only pairs, and the off-fleet peers, all from `MeshGraphDTO` (parent §3.5).
- **Failing since:** the earliest proven `since` among E and its sources (parent §8.1 as revised). When no rule proves one, it is the observation log's proof for those keys (§8.2), worded by its basis. The ref's condition age is carried as qualified evidence, never as the start.
- **Git ordering (parent §3.4)** is slice 6. Its fields are reserved and empty here.

## 5. Tests

- **The parent §12 "Line" list,** as table tests in `internal/line`:
  - placement;
  - every comparison-unit case;
  - key selection;
  - R;
  - every state and flag;
  - held;
  - failure age;
  - mesh.
- **Lifecycle tests on `LineService`:**
  - an incarnation change drops facts;
  - a reconnect race (an old incarnation's late reply);
  - out-of-order pushes;
  - last good facts kept on a failed read;
  - a bootstrap racing a push.
- **The env parity test:** Go's ranking table against the frontend's `ENV_RANK`.
