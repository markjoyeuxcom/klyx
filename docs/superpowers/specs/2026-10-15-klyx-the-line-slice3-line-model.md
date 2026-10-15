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

**Revision 3** answers Codex's review of e28f81d:

| Finding | Where it is addressed |
|---|---|
| 1. Gaps per source partition, recovery by baseline too, seeded at attach | §2.3 |
| 2. Coalesced rows must not hide a flip | §2.1, §5 |
| 3. Bootstraps as cancellable jobs, never blocking the worker | §2.2 |
| 4. Mesh status and freshness publish, and the provider is context-aware | §6 |

**Revision 4** answers Codex's review of 5a66408:

- **Partition state replaces the batch list:** the tap keeps the latest fenced state per partition, so nothing can overflow (§2.1, §2.3).
- **Mesh placement follows station placement too** (§6).
- **The superseded worker-bootstrap sentences are removed** (§2.1).
- **The observation log gains an incarnation-fenced partition snapshot** (§2.3).

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
  - **overwritten:** a sticky per-(cluster, part) flag. It is set when a new `Rows` replaces an unconsumed one with a different `Part.Ver`. A discarded row set may have shown a predicate flip and flip-back, so the worker treats this as a history gap for that part (§5). Presentation may coalesce; history discloses what it did not see;
  - **partitions:** per (cluster, part, source), fenced by incarnation, the **latest state** a batch set:
    - `lost`, `absent` or `available`;
    - for `available`, its recovery `PartVer`;
    - a sticky **history-gap** bit, set by any loss and cleared only when the worker drains it.

    This is bounded by the partition count, never by the number of batches, so nothing overflows. However many batches arrive between two drains, the worker sees the last state and its recovery `PartVer`, and learns from the bit that continuity broke;
  - **end:** a sticky flag set by Final.
- **A one-slot channel wakes the worker.** Nothing in a listener blocks.
- **The service worker is one goroutine, cancelled with the app context.** It owns all line state:
  - it drains the slots;
  - it does every subscription and lease;
  - it starts bootstrap jobs (§2.2) and applies their results;
  - it recomputes and emits.

  It never waits on I/O itself.
- **The tap sees raw `RowSet[flux.Resource]`, `RowSet[flux.Source]` and `RowSet[argo.App]`,** before the webview conversion. The line never reads or changes the rows lineage.

### 2.2 Initialise from current state, then reconcile

- **Order:** register the listeners first, then scan, so nothing published after the scan is missed. Anything published before it is read by the scan's bootstrap.
- **The scan, at start and every 5 s, plus on demand from config changes:**
  - **every configured cluster** in the live config (`LiveClusterConfig`, hot-adds included) is a station;
  - **with no connection or no Observer** (a failed connection factory, or not started), the station reads `error`, or `checking` while connecting, with no facts;
  - **with an Observer not yet attached** (or a newer incarnation than the attached one), the service creates an attachment, which:
    - has its own id and a context derived from the service's;
    - acquires the Flux and Argo domain leases once each, with `Owner{Kind: "line"}`;
    - calls `SubscribeRows` per required part;
    - starts bootstrap **jobs** (below);
  - **a replaced, removed or ended Observer** has its attachment cancelled (its jobs end at once), and its subscriptions and leases are released.
- **Bootstraps are jobs, never worker calls** (finding 3). Each part's `RowsNowCtx` runs on its own goroutine:
  - under the attachment's context with a 10 s deadline;
  - with at most 8 jobs fleet-wide (a semaphore);
  - delivering its result to the worker through a result channel, tagged with the attachment id and incarnation.
  The worker keeps draining lifecycle events, publishing checking states and cancelling attachments while jobs wait. A result whose attachment is no longer current is discarded.
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
- **Availability does not travel in row versions.** A connection change leaves rows live, at the same `Part.Ver`. So the station also tracks:
  - **the connection word,** from the same-incarnation `Summary`. When it is not usable, the station reads `checking`: nothing matches and the last good facts show "as of".
  - **each source partition's continuity** (finding 1), keyed by (task, part, source) and fenced by incarnation, from the batches:

| Batch | Partition state |
|---|---|
| loss (coverage, relist, generation, connection, membership) | **lost** |
| loss (capped) | **lost** (its recovery is a baseline) |
| loss (absent) | **absent**: known empty; never a gap for the read, but a break in history |
| baseline (first sight, cap recovery, reinstall) | **available** from that batch's `PartVer` |
| after-gap | **available** from that batch's `PartVer` |
| changes | unchanged |

    - **Two facts are kept apart.** A partition's current availability (above, the latest state the tap recorded) is one. "A gap occurred since the last drain" is the sticky history-gap bit (§5), set by any loss.
    - **No sequence of batches can mislead the worker:** it sees the latest state and its recovery `PartVer`, and the history bit. Tests cover a burst of batches between two drains ending in a loss, an after-gap, a baseline and an absence, each followed by quiet with no further event.
    - **A partition becomes current only once the worker holds that part's rows at or after the recovery batch's `PartVer`.** Until then its retained facts stay "as of".
    - **Only needed partitions gate the read** (§2.4). A lost Bucket partition never blocks a station that needs only GitRepository. A GitRepository recovery never clears a Bucket loss.
  - **Seeding at attach:** losses that predate the listener are not replayed by `RowsNowCtx`. So when a bootstrap lands, each source partition is seeded:
    - from the bootstrap reply's per-kind coverage (`Part.Kinds`): live is available, absent is absent, anything else is lost;
    - and from the observation log's new **fenced snapshot**, `obslog.PartitionSnapshot(cluster) (incarnation, []PartitionState)`, where each state carries its `PartVer`. It is used only when its incarnation equals the attachment's, and it holds a loss still awaiting its recovery.
- **Recovery recomputes even with identical rows:** a connection becoming usable again, a baseline or after-gap that makes a partition current, or an unchanged relist (its loss and after-gap pair) each trigger a recompute.
- **Tests:**
  - cap, then a baseline;
  - uninstall, then known empty, then reinstall;
  - two source kinds recovering independently;
  - attaching during an existing relist.
- **Tests:**
  - a same-incarnation outage and recovery with unchanged rows;
  - an unchanged relist;
  - Final racing a bootstrap;
  - out-of-order pushes;
  - a reconnect race.

### 2.4 The read-state truth table

- **The station needs these kinds:**
  - **always:** `kustomizations`, `helmreleases` and `applications` (the unit kinds);
  - **sources:** only the source kinds its readable Kustomizations (Git units) reference: `gitrepositories`, `ocirepositories`, `buckets`.
  - **Never** a HelmRelease's source. Its recorded-name chart unit (parent §3.2 as revised) compares history alone, and its source's read is context (`ContextRead`), disclosed apart.
  - Coverage is read per kind from `Rows.Part.Kinds`, never from the part's roll-up. A forbidden Bucket kind that no unit references cannot poison a live GitRepository.
- **Per needed kind:**

| Kind coverage | Meaning for the station |
|---|---|
| live | read |
| absent | known empty: contributes no units (a Flux-only, Argo-only or GitOps-free cluster reads ok) |
| checking | unread |
| stale | last good, not current |
| unreadable | error |

- **An unread unit kind is never assumed empty.** While `kustomizations` is unreadable, the station cannot know which sources it would reference. Every source kind a Kustomization can reference then counts as needed, and the station reads at best `partial`.
- **Station read precedence:**
  1. `checking` when the connection is unusable, a needed part is gapped (§2.3), or the station is attaching;
  2. `ok` when every needed kind is live or absent;
  3. `partial` when at least one needed kind is live and another is not;
  4. otherwise the worst of `error` (unreadable), then `stale`, then `checking`.
- **The per-unit read:**
  - for a Git unit, the worse of its own kind's and its referenced source kind's coverage;
  - for a recorded-name chart unit, its own kind's (`helmreleases`) alone, with the source's read as `ContextRead`.
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
- **Flux chart units (revised 2026-10-15):**
  - The key kind `chart-name` (by recorded name; repository not recorded), as parent §3.2 now specifies. `LineKeyDTO.kind` names it, and `label` carries the qualification so every view shows it on the selected comparison.
  - `rTieBy` discloses how a tie for R was broken: "fetch time" (Git and Argo charts) or "lexical version string" (Flux charts).
  - Units carry `configuredChartDiffers` as current context.

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
- **Line coverage is continuous** only while all of these hold between recomputes:
  - the station read stays `ok`;
  - no needed partition had a gap since the last drain;
  - no needed part's rows were **overwritten** unconsumed (§2.1; finding 2);
  - the connection was not lost.

  Any break emits a line loss, and after it each predicate is re-baselined as start unknown. For example, the worker held failing=true since 09:00 while v2 (recovered) and v3 (failing) arrived. v2 was overwritten unseen, so 09:00 is not kept: the predicate is re-baselined.
- **Evidence order** (revised after the review of bd385e7):
  - **Partitions:** each unit kind's failing predicates (Kustomizations, HelmReleases, Argo applications), held, and state. Each partition reads its own input parts and kinds.
    - A Kustomization's or HelmRelease's failure needs its bound source kind. A recorded-name chart *comparison* never does (§2.4).
    - A gap breaks only the partitions whose inputs need the gapped kind.
  - **Capture order:**
    - Accepted row sets are installed and committed one at a time, in capture order, so distinct parts published in one drain cannot hide the composite state between them.
    - Gaps and overwritten rows are applied before any of the drain's commits.
    - An overwrite keeps its part's kinds unread until that part's newer rows are installed. Its gap lasts until the commit after them, so retained pre-gap rows never re-establish continuity.
  - **Dating:**
    - A partition's commit is dated by the newest capture among its own input parts, never by an unrelated part or the worker's clock.
    - Only a real change moves that watermark.
  - **Out of order:**
    - The fence is the partition's accepted-input order, quiet contributions included. A row set captured before an input already committed is a break, as is another part captured at the same instant.
    - The lower bound moves only on a real change.
    - Composite changes are ordered by the Observer's capture clock and nothing finer.
  - **Absence:** an absence is fenced by its version, like a recovery. Rows older than it read checking: their old objects are never known-empty.
- **A reference change** (a new key, R changing, or the selector) is recorded as "reference changed", never as a deployment or a hold. It ends every held and state proof, because both are tied to the key and R. A changed attempted revision likewise restarts that unit's held evidence.
- **Rule-proven `since`** (parent §8.1 as revised) dates a failing predicate only where the rule's role is exactly that predicate: a Flux source or Kustomization Ready=False with no overlay. Since no Flux role is proven today, line failure ages come from the line's own history. The deciding condition's age is shown beside them as evidence.
- **Fencing:**
  - Proofs are read only from the line's own history, under the station's current incarnation.
  - The worker writes them in the same step that applies the rows, so a proof can never describe rows the station does not hold. Transitions arriving before rows only mark gaps.
- **Tests:**
  - same role with a newly failed operation (dated from the operation's failure, not the role);
  - a changed attempted R (held restarts);
  - a changed selector and a changed R (reference changed, held ends);
  - a proof racing the rows;
  - continuity across a gap (start unknown after it);
  - a deterministic true → false → true while the worker is held (re-baselined, never 09:00);
  - an attempted revision and a reference that change and change back while the worker is held.

## 6. The mesh feed (finding 6)

- **`LineService` samples the mesh graph itself,** off the Observer and off the pure builder:
  - once at start, then every 60 s, single-flight;
  - each sample under a context with a 15 s deadline, cancelled with the service;
  - never inside a recompute.
- **The provider is context-aware** (finding 4). `MeshService`'s member reader becomes `func(ctx context.Context) []clustermesh.Member`. `main.go` passes that context into each cluster's mesh reads, replacing its own 30 s Background timeout, so cancellation and the 15 s deadline reach the underlying reads. `GetMeshGraph` passes its own bounded context.
- **The sample is cached** with its `readAt`, its status (ok, or the error) and its node facts (state, read notes):
  - `LineDTO.mesh` discloses "as of <readAt>", or the error when the last sample failed, which keeps the last good graph;
  - a member whose station is not connected has unknown peerings, never "no peers".
- **Publishing:**
  - Every sample outcome that changes anything the DTO shows publishes a new DTO version: status (ok, error, recovered), node facts, or the refreshed `readAt` of a successful sample.
  - **Mesh placement (`line.Mesh`)** depends on the edges and on station placement: membership, ranks, tracks and their adjacency. An environment, region or group edit, or a track inserted or removed, can move an unchanged edge between "drawn" and "station card only". So `line.Mesh` runs in every full line recompute, which already rebuilds placement. A sample with a changed edge set triggers a recompute. A metadata-only sample (readAt, status, node facts) republishes without re-placing.
- **Tests:**
  - the same edge set across an environment edit, a track edit, and a track inserted or removed (the drawn and card-only lists follow);
  - unchanged edges with a failure;
  - failure, then success;
  - a node-only change;
  - a refreshed `readAt` on a quiet fleet;
  - cancellation reaching the underlying reads;
  - shutdown cancelling a sample in flight.

## 7. Tests (summary)

- **The parent's §12 "Line" list,** as table tests in `internal/line`. These are done in slice 3a; the lifecycle items move to the service.
- **The service tests** named in §2.2-§2.4, §5 and §6.
- **The fleet benchmark** in §3.
- **The env parity test.**
