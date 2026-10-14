# The Line, slice 2b: the role ledger, transition stream and observation log

**Parent spec:** `2026-10-14-klyx-the-line-redesign.md` §8.2 (the transition stream and the observation log) and §8.6 (health notifications, a consumer).

This note turns §8.2 into an Observer design, grounded in how the Observer works today.

**Revision 2** answers Codex's review of a8a669f (seven findings):

| Finding | Where it is addressed |
|---|---|
| 1. Continuity is recorded at the source, not sampled from coverage | §2 |
| 2. Connection, Final and incarnation lifecycle | §3 |
| 3. One commit and broadcast rule, with exactly-once delivery past `RowsNow` | §5 |
| 4. Same-role `Since` changes and proof precedence | §4.3, §6.2 |
| 5. Membership is not deletion; absence | §1.2, §4.2 |
| 6. Helm is sampled evidence with weak identity | §4.4 |
| 7. Bounded work, output and retained state | §7 |

## 0. Constraints from the current Observer

All references are to `internal/observe` and `internal/healthsrc`.

- **The complete per-object evaluated state is kept only for subscribed parts.**
  - Every evaluation computes all items, but only the `Part` survives it.
  - Rows are built only when the part is subscribed or `RowsNow` is called (`publish.go:196-246`).
- **`PartSpec.Evaluate` must be pure.**
- **The loop is the only evaluator.** It evaluates on a publish (throttled to 1s) and on a `RowsNow` bootstrap (`serveRows`).
  - The bootstrap clears the part's dirty flag and advances `p.last` and `p.ver` outside the throttle (`publish.go:359-381`).
- **Freshness is sampled, not sticky.**
  - `record` turns Live into Stale (`kind.go:307`), and the next `Replace` restores Live (`kind.go:422`).
  - A 410 or expired resourceVersion is benign: the reflector re-lists and `Replace`s inside the same instance, and coverage stays Live (`errors.go:40`).
  - An outage and recovery inside one throttle window, or a 410 re-list, therefore look like Live to Live.
- **The incarnation is per connection lifetime.**
  - It changes only on a new connection (`fleet/conn.go` `startHealth`).
  - `SetConnection` changes only `summaryDirty`. It neither dirties parts nor changes freshness (`observer.go:286`).
  - The degraded word is `connected · <reason>` (`fleet/conn.go:800`).
- **`final()` emits only the Final summary** (`publish.go:417`).
- **Sink calls are synchronous** on the loop goroutine.
- **Helm is a snapshot read every five minutes.** A successful read is Live (`snapshot.go:196`).

## 1. Evaluators emit a role list

### 1.1 The entry

`observe.PartResult` gains `Roles []RoleEntry`, `RolesCapped bool`:

```go
type RoleEntry struct {
    Source string     // the counted kind or snapshot the object came from
    Key    string     // the UID; for a snapshot, the evaluator's locator (Helm: namespace/name)
    NS     string
    Name   string
    Role   string     // a health role
    Since  *time.Time // the rule-backed Since (2a and 75b3990), when the rule has one
}
```

- **Every evaluator produces it on every evaluation, subscribed or not.** The exception is the evidence-only Events part, which produces none.
- **The GVR comes from the Source's `KindSpec`,** not from each entry.
- **Evaluate stays pure.** The list is output, never state.
- **Producers:**
  - native parts: from their `nativeItem`s (which gain their source kind);
  - verdict parts (workloads, pods, nodes): from each object's verdict;
  - routes: one entry per HTTPRoute after reduction;
  - Helm: from its releases.
- **Capture carries `RoleCap`** (§7). An evaluator appends at most `RoleCap` entries, then sets `RolesCapped` and stops appending. Its counts, Top and rows are unaffected.

### 1.2 Membership is the Observer's, not the evaluator's

- **A key missing from Roles is not, by itself, a deletion.**
  - Example: a route whose parent Gateway was removed stays in the HTTPRoute model, but is not evaluated (`healthsrc/gateway.go:154-168`).
- **The Observer derives authoritative membership** for each counted watched kind from the same capture: the UIDs of its captured objects, via `meta.Accessor`.
  - Every projected type keeps its `ObjectMeta`.
  - An object without accessible metadata makes that source's membership unknown. That source then claims no deletions (fail closed).
- **For a key in the ledger but not in Roles:**
  - **gone:** the key is absent from a Live source's membership, and the source's continuity is unbroken (§2);
  - **unevaluated:** the key is present in membership but was not evaluated. It is a state, not a role. It ends a proof (§6.2), never counts, and never notifies a recovery;
  - **unobserved:** the source is not Live, or its membership is unknown. This falls under the loss rules (§4.2).
- **For a snapshot source** (Helm), membership is the keys of a complete (untruncated) successful read (§4.4).

## 2. Continuity is recorded where it breaks (finding 1)

Each kind generation gains a **continuity epoch** (`kindGen.epoch uint64`). It advances under `o.mu`, at the source event:

| Event | Freshness (unchanged) | Epoch |
|---|---|---|
| A recorded non-benign error (`record`) | Live → Stale, as today | advances |
| A benign error (410, expired or too-large resourceVersion) | stays Live, as today | advances: watch history was lost, so continuity is unproven |
| Every `Replace` (a list replaced the model, changed or not) | → Live, as today | advances |
| A resumable watch rotation (a watch closes and resumes from its resourceVersion, with no list) | Live | unchanged |

- **`record` gains one line for benign errors:** it advances the epoch, then returns as today. Freshness semantics are unchanged.
- **The first `Replace` of a generation** is that generation's baseline.
- **Snapshots** advance a read epoch on every failed read and on every overdue transition. A successful read after either is an after-gap read (§4.4).
- **`captureLocked` returns a continuity identity** alongside the objects, taken under the same lock. It is never part of `Part`, so `samePart`, `Part.Ver` and rows are unaffected:
  - for each kind or snapshot the part reads (counted and dependencies): `{source, generation id, epoch, coverage}`;
  - plus the Observer's connection epoch (§3);
  - plus `capturedAt`, the clock read under that lock.
- **A source partition (§4) was observed continuously** between its last ledger commit and this capture if, and only if:
  - the connection was usable at both and its epoch is unchanged;
  - its own source's identity is unchanged and Live;
  - every dependency's identity is unchanged and Live, or Absent at both.
- **An outage and recovery wholly inside one throttle window,** a 410 re-list, and a gap while the loop was busy evaluating all change an epoch. All three are gaps, never Live to Live.

## 3. Connection, Final and incarnations (finding 2)

- **A connection is usable when its word is `connected` or starts with `connected · `.**
  - A controller-note change (`connected · <reason>`) is neither a loss nor a recovery.
- **`SetConnection` tracks a usable/down state and a connection epoch:**
  - **usable → down:** the epoch advances and every part is marked dirty. The next publish emits a `connection` loss batch for every partition with a ledger.
  - **down → usable:** the epoch advances and every part is marked dirty. This is the defined reconciliation: each partition's next evaluation that is Live with a usable connection emits an after-gap batch against its frozen ledger, even if no object changed.
  - **While down,** evaluations still publish summaries as today, but the ledger treats every partition as not Live.
- **Final:** `final()` emits one `end` batch for the cluster, before the Final summary. obslog ends every proof of that incarnation ("observation ended at <t>").
- **Incarnations in obslog:**
  - A batch from a newer incarnation of a cluster starts a new proof generation: older proofs end ("superseded at <t>"); their log entries are kept, tagged with their incarnation.
  - A batch from an older incarnation than the newest seen is ignored. This covers an old Observer completing after its replacement.

## 4. The ledger and what a diff means

### 4.1 State and identity

- **The ledger is per part, partitioned by source** (`partState.ledger map[source]*partition`). Each partition holds:
  - the entries of its last committed evaluation, keyed by `Key`: `{role, since, ns, name}`;
  - the continuity identity it was committed under (§2);
  - its `committedAt` (the capture time);
  - its state: `live`, `frozen` (a loss was emitted), or `capped`.
- **Partitioning by source** lets one part mix kinds with different coverage. An absent or failing kind affects only its own partition, plus every partition when it is a dependency.
- **The ledger is loop-owned.** It is committed on the loop after evaluation, never under `o.mu`, and never by `captureLocked`.

### 4.2 The lifecycle table, per partition

| Before | Now | Emits | Ledger |
|---|---|---|---|
| none | Live, continuous identity | **baseline**: every entry, nothing a transition | committed |
| live | Live, continuous (§2) | **changes**: role changes, `absent` → role (new), role → `gone` (§1.2), role → `unevaluated`, and restatements (§4.3) | committed |
| live | not Live, or not continuous | **loss** `{reason: coverage \| generation \| relist \| connection, coverage}` | frozen at the last commit |
| frozen | not Live | nothing | frozen |
| frozen | Live (identity now stable) | **after-gap** `{from: lossAt, to: now}`: each key that differs from the frozen ledger | committed |
| live or frozen | the source is **Absent** | **loss** `{reason: absent}`. A capability loss is never a deletion and never a recovery. | dropped |
| dropped (absent) | Live again | **baseline** (a new generation) | committed |
| any | capped (§7) | one **loss** `{reason: capped}` for the episode | dropped |
| capped | under the cap again | **baseline** `{afterCap: true}` | committed |

- **A same-name recreation** is `gone` plus a new key (UIDs differ).
- **A relist that collapses an outage into one model swap** is exactly the after-gap rule. Unchanged keys are re-confirmed without a transition.
- **A partition that is Live but not continuous with its ledger** (the epoch moved between two Live captures) emits its loss and its after-gap in one cycle: the loss at the last commit, then the after-gap at this capture.
- **Part-wide gating is not used.** A failing Flux source kind suspends only its own partition, plus any part that depends on it. A suspended partition never claims absence or health.

### 4.3 Same-role changes of `Since` (finding 4)

- **A key whose role is unchanged but whose rule-backed `Since` changed** (nil → t, t → t', or t → nil) emits a **restated** item. Example: fault → ok → fault inside one window, where Ready's time moved.
  - It is not a transition: it opens no episode, notifies nothing, and never counts as a recovery.
  - It updates the proof (§6.2).
- **Reason changes are not diffed.** Reason is display text, carried on the item when one is emitted.

### 4.4 Sampled sources: Helm (finding 6)

- **A snapshot partition is `sampled`.** Every batch carries:
  - `mode: sampled`;
  - the `readAt` of the read it used;
  - the `prevReadAt` of the read its ledger was committed under.
- **Changes are differences between two reads,** worded "found <role> at read <t>; <old role> at read <t0>". Nothing between reads is claimed.
  - A failed or overdue read is a loss, and the next good read is an after-gap read.
- **A truncated read** claims no `gone` and no new keys beyond the cap. It is treated as capped (§7).
- **A time-only re-evaluation** (a boundary fired, with no new read: `readAt == prevReadAt`) emits **derived** items. These are role changes the rule computes from the same read at a known instant (the boundary). They are kept distinct from a fresh read, and are never "found at read".
- **Identity is weak.**
  - The key is namespace/name: a release locator, not a recreation-proof identity.
  - An uninstall and reinstall between two reads is invisible. The wording and the notifications (§8) say so.
  - Helm keys never get observed-since proofs or UID-based deletion guarantees.

## 5. One commit and broadcast rule (finding 3)

- **Every evaluation commits.** Both the publish and the `RowsNow` bootstrap diff against the ledger, commit it, and append the resulting batch to the part's **pending queue**.
  - A bootstrap is a real capture, so it is an observation.
  - The throttle claim is stated precisely: transitions are observed only at evaluations. That means publishes (at most one per second per part) and row bootstraps. A flip and flip-back between two evaluations is never seen.
- **`publish` flushes every pending queue on every cycle,** in task/part order, after `Summary` and before `Rows`.
  - The early return (`!changed && !summaryDirty && len(rowsOut) == 0`) also checks pending queues, so a ledger publication is never gated on summary or row output.
  - `serveRows` reports "schedule" when the part has pending batches.
- **Each batch carries:**
  - a per-Observer stream sequence (`Seq`, strictly increasing in flush order);
  - the part, its source and the part version (`PartVer`) of the evaluation;
  - the continuity identity;
  - `At` = `capturedAt`, the clock read under the capture lock;
  - `PrevAt` = the partition's previous commit time.
- **Exactly once:**
  - A batch is appended once at commit and flushed once.
  - A repeated bootstrap diffs against the already-committed ledger, so it cannot duplicate a transition.
- **Rows are unchanged.** `rowsLineage`, `Part.Ver`, `samePart` and the rows broadcast keep their contracts. The ledger reads only `Roles` and the continuity identity, which are not part of `Part`.
- **The Sink interface is unchanged.** Transitions go to an optional `TransitionSink` (asserted at `New`), so existing sinks and tests compile.

```go
type TransitionBatch struct {
    Cluster     string
    Incarnation uint64
    Seq         uint64
    Task, Part  string
    Source      string     // the partition; empty on an end batch
    GVR         string
    Mode        string     // watch | sampled
    Kind        string     // baseline | changes | after-gap | loss | end
    PartVer     uint64
    At          time.Time  // the atomic capture time
    PrevAt      *time.Time // the previous commit (changes), or the loss time (after-gap)
    ReadAt      *time.Time // sampled: the read used
    Loss        *Loss      // {Reason, Coverage}
    AfterCap    bool
    Items       []Transition
}

type Transition struct {
    Key, NS, Name string
    From, To      string     // a role, "absent" (new), "gone", or "unevaluated"
    Since         *time.Time // the rule-backed Since now
    Restated      bool       // same role, Since changed (§4.3)
    Derived       bool       // a time-only change from the same read (§4.4)
}
```

## 6. The observation log (`internal/obslog`)

### 6.1 Entries

- **Consumes the stream** through `HealthSink`. Batches are accepted per cluster in incarnation order (§3).
- **An entry** is `{at, prevAt, cluster, incarnation, part, source, GVR, key, ns/name, from → to, kind, mode, restated, derived, loss?}`.
  - Loss and end batches are entries too.
  - LineService's station states and reference changes join in slice 3.
- **Bounds:** 24 hours, or 20,000 entries per cluster, whichever comes first (§7).
- **On disk:** none. The log is in memory only (owner decision 10).

### 6.2 Proofs

- **A proof** is `{role, since *time.Time, notBefore *time.Time, source: rule | observed | sampled | unknown, partition}`.
- **Proofs are kept only for non-ok roles,** which is what the queue, the inspector and the "failing since" wording need. A key that goes ok, `gone` or `unevaluated` has its proof purged or ended.
- **Precedence:**
  1. **A rule-backed `Since` wins whenever present.** It is controller evidence and survives Klyx's own gaps while it is unchanged.
  2. **observed:** a `changes` item under continuous watch coverage, with no rule `Since`. The role began in `(PrevAt, At]`, worded "since ~<At> (observed)".
  3. **sampled:** the same from two reads, worded "between <prevReadAt> and <readAt>".
  4. **unknown:** a baseline, after-gap or post-cap first sight with no rule `Since`, worded "start unknown; first seen <At>". An observed-only baseline never proves a start.
- **A restated item** replaces the proof's `since` with the new rule `Since`. Nil downgrades the proof to unknown.
- **On loss,** the partition's proofs are suspended.
- **On after-gap,** every observed or sampled proof in that partition, changed or not, is downgraded to unknown ("seen before and after a gap"). Rule-backed proofs stay while their `Since` is unchanged.
- **Proofs are never derived from when observation began.**
- **Queries:**
  - `Proof(cluster, key)`;
  - `Recent(cluster, limit)`;
  - `Partition(cluster, task, part)` for the queue's coverage rows.

## 7. Bounds (finding 7)

| What | Bound | Fallback |
|---|---|---|
| `Roles` per evaluation | `RoleCap` = 50,000 per part | `RolesCapped`. Counts, Top and rows continue; the partition goes capped (one loss, then a baseline when back under the cap). |
| Ledger | ≤ `RoleCap` entries per partition. Entries reference the evaluator's strings, with no copies. | dropped while capped |
| Pending batches | ≤ 4 per part between flushes (only bootstraps can add a second) | overflow drops the oldest batch, and the partition emits a `loss {reason: overflow}` with an after-gap |
| Items per batch | ≤ `RoleCap` (a baseline is the worst case) | flushed in chunks of 5,000 items, each with the same `Seq` lineage (`Chunk n/m`) |
| obslog entries | 20,000 per cluster, 24h | oldest first. A trimmed age reads "at least since <bound> (log trimmed)". |
| obslog proofs | ≤ 20,000 per cluster (non-ok keys only) | new proofs beyond the cap are not kept, and their queries read "start unknown (proof capped)" |
| Clusters | per-cluster bounds × connected clusters | a cluster's state is released on its `end` plus the 24h window |

- **Measurement before merge:**
  - a benchmark of evaluate + diff on an unsubscribed 10,000- and 50,000-pod part (allocations and latency), with the numbers recorded in the commit;
  - a multi-cluster cap-and-recovery fixture.

## 8. Health notifications (§8.6) consume the same stream later

- **Never notify:** baseline, after-gap, restated, derived, `unevaluated` and loss items.
- **A new episode** is a `changes` item into fault, under continuous watch coverage.
- **A sampled source** may notify a change between two reads, worded as such, with no deletion or reinstall claims.
- **A loss suspends episodes.** Recovery is only a `changes` item into ok, under continuous coverage.

## 9. Tests

`internal/observe` tests follow `observe_test.go` and its fake clock.

### 9.1 Continuity

- an outage and recovery wholly within one throttle window: a gap;
- a 410 re-list: a gap; a resumable watch rotation: continuous;
- a gap while the loop is busy (evaluate held on a blocked fake);
- a served-version change: a new generation, so a gap.

### 9.2 Lifecycle

- a baseline;
- a role flip as one item at `capturedAt`;
- flip and flip-back between two evaluations: nothing;
- delete: `gone`; same-name recreation: `gone` plus a new key;
- live → stale: one loss and a frozen ledger; stale → live after a relist: after-gap items only for changed keys.

### 9.3 Connection and lifecycle

- a controller-note change: nothing;
- down/up without object changes: a loss, then after-gap with no items;
- Final: an `end` batch;
- an old Observer completing after its replacement: ignored by obslog.

### 9.4 Delivery

- bootstrap-before-flush: exactly once;
- repeated bootstraps: exactly once;
- a pending-only cycle flushes with no summary or rows change;
- `rowsLineage` and the existing rows tests are unchanged;
- the parity gate stays green.

### 9.5 Since and proofs

- same role with a new `Since`: restated, no notification-eligible item;
- loss and recovery with unchanged roles and a changed `Since`;
- proof deletion on `gone`;
- observed proofs downgraded after a gap, rule proofs kept.

### 9.6 Membership and absence

- a route's parent Gateway removed while the route remains: `unevaluated`, never `gone`;
- an entire kind absent: a loss `{absent}`, then a baseline on return;
- mixed-kind absence in one part: only that partition.

### 9.7 Helm

- two reads spanning a change;
- a same-name reinstall: no `gone`, worded as weak identity;
- a time-only pending deadline with no new read: a derived item.

### 9.8 Bounds

- the cap: a capped loss, then a baseline;
- pending overflow;
- obslog entry, proof and age bounds;
- the benchmarks in §7.
