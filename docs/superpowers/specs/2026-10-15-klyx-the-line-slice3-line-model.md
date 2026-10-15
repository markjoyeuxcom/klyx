# The Line, slice 3: the line model and LineService

**Parent spec:** `2026-10-14-klyx-the-line-redesign.md` §3 (the line), §3.6 (data lifecycle), §8.2 (the observation log, which LineService's station states and reference changes join) and §12 (the line's table tests).

This note fixes the implementation shape the parent leaves open. The rules themselves (placement, units, keys, R, states, flags, held) are the parent's, implemented verbatim in the pure package (committed as slice 3a, d9933f6). Slice 3 is Go only: no UI.

**Revision 2** answers Codex's review of 3a9e479:

| Finding | Where it is addressed |
|---|---|
| 1. Taps only hand off | §2.1 |
| 2. Availability and continuity are not row versions | §2.3, §3 |
| 3. Initialise from current state | §2.2 |
| 4. The read-state truth table | §2.4 |
| 5. Predicate-specific, fenced ages | §5 |
| 6. A mesh feed | §6 |

## 1. Packages

- **`internal/line` (pure, slice 3a):**
  - placement;
  - units and revision parsing;
  - keys, R, states and flags, held;
  - the mesh rules.
  - It takes plain values and has no client-go, no clock and no I/O.
- **`EnvRank` lives in Go.** A parity test pins the frontend's `ENV_RANK` until slice 4 reads ranks from `LineDTO`.
- **`LineService` (in `internal/appbridge`)** owns acquisition, the worker, the line's own predicate history, the mesh feed and the push.

## 2. Acquisition

### 2.1 Taps only hand off

- **`HealthSink` gains Go listeners** for `Summary`, `Rows` and `Transitions`.
  - They run on the Observer's loop goroutine, so a listener only records state and signals. It never subscribes, bootstraps, recomputes, emits or waits.
  - `RowsNowCtx` waits on that same loop, so calling it from a listener would deadlock.
- **The record is bounded and coalescing:** a per-cluster slot set behind a small mutex.
  - **summary:** the latest per cluster;
  - **rows:** the latest `observe.Rows` per (cluster, part), for the three parts the line reads;
  - **losses:** a sticky per-(cluster, part) gap flag, set by any loss batch and cleared only by the worker;
  - **end:** a sticky flag set by Final.
- **A one-slot channel wakes the worker.** Nothing in a listener blocks.
- **The service worker is one goroutine, cancelled with the app context.** It drains the slots, does every subscription and bootstrap, recomputes and emits.
  - `RowsNowCtx` runs on the worker with a 10 s deadline, never under the service's state lock.
  - The worker owns all line state, so no lock is held across I/O.
- **The tap sees raw `RowSet[flux.Resource]`, `RowSet[flux.Source]` and `RowSet[argo.App]`,** before the webview conversion. The line never reads or changes the rows lineage.

### 2.2 Initialise from current state, then reconcile

- **Order:** register the listeners first, then scan, so nothing published after the scan is missed. Anything published before it is read by the scan's bootstrap.
- **The scan, at start and every 5 s, plus on demand from config changes:**
  - **every configured cluster** in the live config (`LiveClusterConfig`, hot-adds included) is a station;
  - **with no connection or no Observer** (a failed connection factory, or not started), the station reads `error`, or `checking` while connecting, with no facts;
  - **with an Observer not yet attached** (or a newer incarnation than the attached one), the service:
    - acquires the Flux and Argo domain leases once each, with `Owner{Kind: "line"}`;
    - calls `SubscribeRows` per required part;
    - bootstraps each part with `RowsNowCtx`;
  - **a replaced, removed or ended Observer** has its subscriptions and leases released, and any bootstrap in flight for it is cancelled.
- **A config change** (`ConfigService` add, edit or remove) triggers a scan and a recompute, even with no GitOps row event, so placement follows the fleet.
- **Tests:**
  - an Observer already running before the service starts;
  - one that is unavailable at first and attaches later;
  - a configured cluster with no Observer;
  - a replaced incarnation, with the old one's late bootstrap discarded.

### 2.3 Fencing, availability and continuity

- **Incarnation:**
  - Rows, summaries and transitions are accepted only for the station's current incarnation.
  - A newer incarnation drops the station's facts and its line history proofs (§5), then attaches.
  - Final is terminal for its incarnation. It marks the station checking with its last good facts, and any reply or row of that incarnation that arrives later is ignored.
- **Part version:** a row set is applied only when its `Part.Ver` is newer than the one held. A bootstrap reply that loses to a newer push is discarded.
- **Availability does not travel in row versions** (finding 2). A connection change leaves rows live, at the same `Part.Ver`. So the station also tracks:
  - **the connection word,** from the same-incarnation `Summary`. When it is not usable, the station reads `checking`: nothing matches and the last good facts show "as of".
  - **gaps,** from transition losses on the three parts the line reads. A loss marks those parts gapped until their after-gap. While gapped, the station reads `checking`, and the line's own predicate proofs are downgraded (§5).
- **Recovery recomputes even with identical rows:** a connection becoming usable again, an after-gap, or an unchanged relist (seen as its loss and after-gap pair) each trigger a recompute.
- **Tests:**
  - a same-incarnation outage and recovery with unchanged rows;
  - an unchanged relist;
  - Final racing a bootstrap;
  - out-of-order pushes;
  - a reconnect race.

### 2.4 The read-state truth table

- **The station needs these kinds:**
  - **always:** `kustomizations`, `helmreleases` and `applications` (the unit kinds);
  - **sources:** only the source kinds its readable units reference (`gitrepositories`, `helmrepositories`, `ocirepositories`, `buckets`, `helmcharts`).
  - Coverage is read per kind from `Rows.Part.Kinds`, never from the part's roll-up. A forbidden Bucket kind that no unit references cannot poison a live GitRepository.
- **Per needed kind:**

| Kind coverage | Meaning for the station |
|---|---|
| live | read |
| absent | known empty: contributes no units (a Flux-only, Argo-only or GitOps-free cluster reads ok) |
| checking | unread |
| stale | last good, not current |
| unreadable | error |

- **An unread unit kind is never assumed empty.** While `kustomizations` is unreadable, the station cannot know which sources it would reference. All of its Flux source kinds then count as needed, and it reads at best `partial`.
- **Station read precedence:**
  1. `checking` when the connection is unusable, a needed part is gapped (§2.3), or the station is attaching;
  2. `ok` when every needed kind is live or absent;
  3. `partial` when at least one needed kind is live and another is not;
  4. otherwise the worst of `error` (unreadable), then `stale`, then `checking`.
- **The per-unit read** is the worse of its own kind's and its referenced source kind's coverage.
- **The parent's state rule 1** (checking when the station read is not ok, or an eligible unit is unread or stale) applies on top: `partial` is never compared.
- **Tests:**
  - the absent, live, checking, stale and unreadable matrix for each needed kind;
  - Flux-only, Argo-only and no-GitOps clusters;
  - a needed versus an unrelated source kind that is unreadable.

## 3. Recompute and push

- **The worker recomputes the whole line** after each drained batch of changes, as one trailing run: a change arriving mid-recompute schedules exactly one more run. Recomputes run at most every 250 ms.
- **The worker emits `line:dto`** with a strictly increasing version. The frontend keeps a DTO only when its version is newer.
- **`LineService.Line()`** returns the current DTO, for bootstrapping (subscribe, then read).
- **`LineService.SetSource(key)`** switches the board's key for the session (the parent's "Selector"); it is never persisted. The `fleet.yaml` override (`line.source`) wins at startup.
- **A benchmark** recomputes a large supported fleet (50 clusters × 500 units across Flux and Argo), and its time is recorded in the commit.

## 4. `LineDTO`

- **Fleet:**
  - `version`;
  - `key`: the chosen comparison key, how it was chosen and its station count;
  - `keys`;
  - `r`: the comparison revision, its tie-break disclosure, and the headline counts;
  - `tracks`;
  - `siding`;
  - `fallback`;
  - `mesh`, with its own freshness (§6).
- **Per station:**
  - `cluster`, `track`, `rank`, `env`;
  - `state`, with the facts E/A/U;
  - `flags`;
  - `held` and its evidence;
  - `failingSince` and `heldSince`, as proofs (§5);
  - `incarnation`, `readAt`, the read state with its per-kind reasons, and the last good facts' `asOf`;
  - `excluded`.
- **Git ordering (parent §3.4)** is slice 6. Its fields are reserved and empty here.

## 5. The line's own predicate history (finding 5; parent §8.2)

- **A role proof is not a line predicate's age.** For example, an Argo application can stay Healthy and OutOfSync (the same role) while a sync operation fails later. And an object can stay faulted while attempted R, or R itself, changes. So the line keeps its own history and never borrows a role proof for these.
- **What it records,** per station and incarnation:
  - each eligible unit's **failing predicate** (§3.3 "failing": the unit or its bound source Ready False, or its sync failed; suspended units excluded), keyed `unit/<uid>#<source index>`;
  - the station's **held predicate**, keyed to (key, R) — attempted R and failing;
  - the station's **state**;
  - **reference changes:** the chosen key and R.
- **How it is recorded:** as batches into the observation log, in the station's cluster, with `Task: "line"` and `Mode: "line"`, so they share its bounds, wording and incarnation fencing.
  - A predicate becoming true under continuous line coverage is an observed change, "since ~<at> (observed)".
  - First sight, an after-gap or a new incarnation gives "start unknown; first seen <at>".
- **Line coverage is continuous** only while the station read stays `ok` with no gap and no connection loss between recomputes. Any break emits a line loss, and after it each predicate is re-baselined as start unknown.
- **A reference change** (a new key, R changing, or the selector) is recorded as "reference changed", never as a deployment or a hold. It ends every held proof, because held is tied to R. A changed attempted revision likewise restarts that unit's held evidence.
- **Rule-proven `since`** (parent §8.1 as revised) dates a failing predicate only where the rule's role is exactly that predicate: a Flux source or Kustomization Ready=False with no overlay. Since no Flux role is proven today, line failure ages come from the line's own history. The deciding condition's age is shown beside them as evidence.
- **Fencing:**
  - Proofs are read only from the line's own history, under the station's current incarnation.
  - The worker writes them in the same step that applies the rows, so a proof can never describe rows the station does not hold. Transitions arriving before rows only mark gaps.
- **Tests:**
  - same role with a newly failed operation (dated from the operation's failure, not the role);
  - a changed attempted R (held restarts);
  - a changed selector and a changed R (reference changed, held ends);
  - a proof racing the rows;
  - continuity across a gap (start unknown after it).

## 6. The mesh feed (finding 6)

- **`LineService` samples the mesh graph itself,** off the Observer and off the pure builder:
  - once at start, then every 60 s, single-flight, each read bounded by 15 s, and cancelled with the service;
  - through `MeshService`'s member reads.
  - It never reads the mesh inside a recompute.
- **The sample is cached** with its `readAt` and any error. `LineDTO.mesh` discloses "as of <readAt>", or the error, when the last sample failed. A member whose station is not connected has unknown peerings, never "no peers".
- **A changed sample** (a different edge set) triggers a recompute. An unchanged one only refreshes `readAt`.
- **Tests:**
  - a mesh-only change recomputes;
  - a failed sample discloses an error and keeps the last good graph as "as of";
  - shutdown cancels a sample in flight.

## 7. Tests (summary)

- **The parent's §12 "Line" list,** as table tests in `internal/line`. These are done in slice 3a; the lifecycle items move to the service.
- **The service tests** named in §2.2-§2.4, §5 and §6.
- **The fleet benchmark** in §3.
- **The env parity test.**
