# The Line, slice 2b: the role ledger, transition stream and observation log

**Parent spec:** `2026-10-14-klyx-the-line-redesign.md` §8.2 (the transition stream and the observation log) and §8.6 (health notifications, a consumer).

This note turns §8.2 into an Observer design, grounded in how the Observer works today.

**Revision 2** answered Codex's review of a8a669f (seven findings). **Revision 3** answered its review of revision 2 (six findings). **Revision 4** answers its review of revision 3 (94b8825):

- **Per-item boundaries:** each derived item carries its own boundary (§1.1, §4.4, §5.4, §6.2).
- **Silent closing after a gap:** an after-gap ok or `gone` closes a suspended episode without notifying (§8).

Revision 3's findings:

| Revision 3 finding | Where it is addressed |
|---|---|
| 1. A continuity change must schedule an evaluation | §2 |
| 2. The pending-queue cap and overflow recovery were incoherent | §5: there is no queue; a batch is emitted at commit |
| 3. A baseline is not the largest diff | §5.3, §7 |
| 4. Final must settle pending commits | §5: nothing is ever pending |
| 5. The proof for a derived Helm item | §4.4, §6.2 |
| 6. Capture the effective served GVR | §1.1, §2 |

The revision 2 findings stay addressed: continuity at the source (§2), lifecycle (§3), exactly-once delivery (§5), same-role `Since` changes (§4.3), membership and absence (§1.2, §4.2), Helm sampling (§4.4), and bounds (§7).

## 0. Constraints from the current Observer

All references are to `internal/observe` and `internal/healthsrc`.

- **The complete per-object evaluated state is kept only for subscribed parts.**
  - Every evaluation computes all items, but only the `Part` survives it.
  - Rows are built only when the part is subscribed or `RowsNow` is called (`publish.go:196-246`).
- **`PartSpec.Evaluate` must be pure.**
- **The loop goroutine is the only evaluator.** It evaluates on a publish (throttled to 1s) and on a `RowsNow` bootstrap.
  - `serveRows` runs on the loop goroutine, from its `select`.
  - The bootstrap clears the part's dirty flag and advances `p.last` and `p.ver` outside the throttle (`publish.go:359-381`).
- **Freshness is sampled, not sticky.**
  - `record` turns Live into Stale (`kind.go:307`), and the next `Replace` restores Live (`kind.go:422`).
  - A 410 or expired resourceVersion is benign: the reflector re-lists and `Replace`s inside the same instance, and coverage stays Live (`errors.go:40`).
  - An identical Live `Replace` and a benign error schedule no evaluation today (`kind.go:416-427`, `kindwatch.go:119`).
- **The incarnation is per connection lifetime.**
  - It changes only on a new connection (`fleet/conn.go` `startHealth`).
  - `SetConnection` changes only `summaryDirty` (`observer.go:286`).
  - The degraded word is `connected · <reason>` (`fleet/conn.go:800`).
- **A CRD `KindSpec.GVR` is `group/resource`, without a version** (`native.go:320`). Only the generation's version, or the object's `apiVersion`, knows the served API. Refs already use the object's `apiVersion` (`native.go:191-196`).
- **`final()` emits only the Final summary** (`publish.go:417`).
- **Sink calls are synchronous** on the loop goroutine.
- **Helm is a snapshot read every five minutes.** A successful read is Live (`snapshot.go:196`). `ReleaseRule` supplies `Next` (the pending deadline), never `Since`.

## 1. Evaluators emit a role list

### 1.1 The entry

`observe.PartResult` gains `Roles []RoleEntry` and `RolesCapped bool`:

```go
type RoleEntry struct {
    Source    string     // the counted kind or snapshot the object came from
    GVR       string     // the object's served API: its apiVersion + "/" + resource (refOf)
    Key       string     // the UID; for a snapshot, the evaluator's locator (Helm: namespace/name)
    NS, Name  string
    Role      string     // a health role
    Label     string     // the rule's label
    Since     *time.Time // the proven rule Since (parent §8.1), when the rule has one
    Boundary  *time.Time // the time-only threshold this role crossed (NativeEval.Boundary), per object
    Condition *Condition // the deciding condition's own time (parent §8.1), evidence only
    Reason    string     // scrubbed, at most 200 runes
}
```

- **`Boundary` is new on `health.NativeEval`.** It is the instant a time-only threshold flipped this object's role, set by the rule that computes it, while the role is the crossed one. It is evidence, never a proven start.
  - It matters because `Next` is cleared once the role flips (`helmcli/rules.go:40-46`).
  - **Producers:**
    - a Helm release past its pending deadline: updated + 601s;
    - a Karpenter claim that appears stuck: created + 15m + 1s;
    - an overdue ExternalSecret: `StaleAt`;
    - an expired certificate: `notAfter`.

- **Every evaluator produces it on every evaluation, subscribed or not.** The exception is the evidence-only Events part, which produces none.
- **Strings are shared** with the evaluator's own values, never copied.
- **Evaluate stays pure.** The list is output, never state.
- **Producers:**
  - native and verdict parts: centrally, in `nativeResult` and `verdictResult`, from refs that gain their source kind;
  - routes: one entry per HTTPRoute after reduction;
  - Helm: from its releases.
- **Capture carries `RoleCap`** (§7). An evaluator appends at most `RoleCap` entries, then sets `RolesCapped` and stops appending. Its counts, Top and rows are unaffected.

### 1.2 Membership is the Observer's, not the evaluator's

- **A key missing from Roles is not, by itself, a deletion.**
  - Example: a route whose parent Gateway was removed stays in the HTTPRoute model, but is not evaluated (`healthsrc/gateway.go:154-168`).
- **The Observer derives authoritative membership** for each counted watched kind from the same capture: the UIDs of its captured objects, via `meta.Accessor`.
  - Every projected type keeps its `ObjectMeta`.
  - An object without accessible metadata makes that source's membership unknown, and that source claims no deletions (fail closed).
- **For a key in the ledger but not in Roles:**
  - **gone:** the key is absent from a Live source's membership, and the source's continuity is unbroken (§2);
  - **unevaluated:** the key is present in membership but was not evaluated. It is a ledger state, not a role. It ends a proof (§6.2), never counts, and never notifies a recovery;
  - **unobserved:** the source is not Live, or its membership is unknown. This falls under the loss rules (§4.2).
- **For a snapshot source** (Helm), membership is the keys of a complete, untruncated successful read (§4.4).

## 2. Continuity is recorded where it breaks, and schedules an evaluation

- **Each kind generation gains:**
  - a continuity epoch (`epoch uint64`);
  - a `relisting` flag, true from an error until the next `Replace`.
- **Both are changed under `o.mu`,** at the source event, behind the existing generation and instance fences:

| Event | Freshness (unchanged) | Epoch | `relisting` |
|---|---|---|---|
| A recorded non-benign error (`record`) | Live → Stale, as today | advances | set |
| A benign error (410, expired or too-large resourceVersion) | stays Live, as today | advances | set |
| Every `Replace` (a list replaced the model, changed or not) | → Live, as today | advances | cleared |
| A resumable watch rotation (a watch closes and resumes from its resourceVersion, with no list) | Live | unchanged | unchanged |

- **Every epoch change schedules an evaluation** (finding 1): it marks the kind's parts dirty and wakes the loop.
  - It does not advance `Obs`, so it never forces a `Part.Ver` change.
  - A benign error followed by an identical relist therefore produces a loss and an after-gap with no other mutation, bootstrap or object change.
- **A source is Live for the ledger** only when its coverage is Live and it is not `relisting`. While a benign error's relist is held, the partition is lost, not continuous.
- **Snapshots:**
  - Their read epoch advances on every failed read and on every overdue transition.
  - Both already mark the snapshot's parts dirty (`snapshot.go`).
  - A successful read after either is an after-gap read (§4.4).
- **`captureLocked` returns a continuity identity** alongside the objects, under the same lock. It is never part of `Part`, so `samePart`, `Part.Ver` and rows are unaffected. It records:
  - for each kind or snapshot the part reads (counted and dependencies): `{source, generation id, epoch, coverage, relisting, gvr}`. `gvr` is the **effective served GVR**: group/version/resource, from the generation's probed version (finding 6);
  - the Observer's connection epoch and usable state (§3);
  - `capturedAt`, the clock read under that lock.
- **A source partition (§4) was observed continuously** between its last commit and this capture if, and only if:
  - the connection was usable at both and its epoch is unchanged;
  - its own source's identity is unchanged and Live for the ledger;
  - every dependency's identity is unchanged and Live for the ledger, or Absent at both.
- **These are all gaps, never Live to Live:**
  - an outage and recovery wholly inside one throttle window;
  - a 410 re-list;
  - a gap while the loop was busy.

## 3. Connection, Final and incarnations

- **A connection is usable when its word is `connected` or starts with `connected · `.**
  - A controller-note change (`connected · <reason>`) is neither a loss nor a recovery.
- **`SetConnection` tracks a usable/down state and a connection epoch.** Either flip advances the epoch, marks every part dirty and wakes the loop.
  - **usable → down:** the next evaluation of each partition with a ledger emits a `connection` loss.
  - **down → usable:** each partition's next evaluation that is Live and continuous with a usable connection emits an after-gap against its frozen ledger, even if no object changed. This is the defined reconciliation.
- **Final:** `final()` emits one `end` batch for the cluster, before the Final summary.
  - Nothing is pending at that point, because batches are emitted at commit (§5), so no committed batch can be lost.
  - Stopped generations are never re-evaluated.
- **Incarnations in obslog:**
  - A batch from a newer incarnation of a cluster ends the older incarnation's proofs ("superseded at <t>"). Its log entries are kept, tagged with their incarnation.
  - A batch from an older incarnation than the newest seen is ignored. This covers an old Observer completing after its replacement.

## 4. The ledger and what a diff means

### 4.1 State

- **The ledger is per part, partitioned by source** (`partState.ledger map[source]*partition`). Each partition holds:
  - its committed entries, keyed by `Key`: `{role, label, since, condition, reason, ns, name, gvr}`, or the `unevaluated` state;
  - the continuity identity it was committed under (§2);
  - `committedAt` (the capture time) and, for a snapshot, `readAt`;
  - its state: `live`, `frozen` (a loss was emitted), or none (dropped).
- **Partitioning by source** lets one part mix kinds with different coverage. An absent or failing kind affects only its own partition, and every partition of a part that has it as a dependency.
- **The ledger is loop-owned.** It is committed on the loop goroutine after evaluation, never under `o.mu`, and never by `captureLocked`.

### 4.2 The lifecycle table, per partition

| Before | Now | Emits | Ledger |
|---|---|---|---|
| none | Live, continuous identity | **baseline**: a replacement checkpoint (§5.2) | committed |
| live | Live, continuous (§2) | **changes**: role changes, `absent` → role (new), role → `gone` (§1.2), role ↔ `unevaluated`, and restatements (§4.3) | committed |
| live | not Live for the ledger | **loss** `{reason: coverage \| relist \| generation \| connection}` | frozen at the last commit |
| live | Live, but the identity moved (a gap between two Live captures) | **loss**, then **after-gap**, in one commit | committed |
| frozen | not Live | nothing | frozen |
| frozen | Live | **after-gap** `{from: lossAt, to: now}`: every key that differs from the frozen ledger, including `gone`, new and `unevaluated` | committed |
| live or frozen | the source is **Absent** | **loss** `{reason: absent}`. A capability loss is never a deletion and never a recovery. | dropped |
| any | capped (§7) | **loss** `{reason: capped}`, once per episode | dropped |
| dropped | Live (back under the cap, or present again) | **baseline** (`afterCap` when it follows a cap) | committed |

- **A same-name recreation** is `gone` plus a new key (UIDs differ).
- **A relist that collapses an outage into one model swap** is exactly the after-gap rule. Unchanged keys are re-confirmed without a transition.
- **A suspended partition** never claims absence or health. A failing Flux source kind suspends only its own partition, plus any part that depends on it.

### 4.3 Same-role changes of `Since`

- **A key whose role is unchanged but whose proven `Since` changed** (nil → t, t → t', or t → nil) emits a **restated** item. Example: fault → ok → fault between two evaluations, on a rule that proves its start.
  - It is not a transition: it opens no episode, notifies nothing, and never counts as a recovery.
  - It updates the proof (§6.2).
- **Label, reason and condition changes are not diffed.** They travel on any item that is emitted.

### 4.4 Sampled sources: Helm

- **A snapshot partition is `sampled`.** Every batch carries:
  - `mode: sampled`;
  - `ReadAt`, the read the evaluation used;
  - `PrevReadAt`, the read its ledger was committed under. This is distinct from `PrevAt`: a bootstrap moves `PrevAt` without a read (finding 5).
- **When `ReadAt != PrevReadAt`,** changes are differences between two reads, worded "found <role> at read <t>; <old role> at read <t0>". Nothing between reads is claimed.
- **When `ReadAt == PrevReadAt`** (a time-only re-evaluation: a boundary fired with no new read), items are **derived**: the rule recomputed the role from the same read.
  - Each derived item carries its own `Boundary`, from its entry, and the batch carries the source read (`ReadAt`).
  - `At` stays the capture time. One delayed evaluation can cross several objects' boundaries (release A's at 12:01 and B's at 12:02, captured at 12:03), and each keeps its own.
  - A derived item is never "found at read" and never a fresh observation (§6.2).
- **A failed or overdue read is a loss.** The next good read is an after-gap read.
- **A truncated read** claims no `gone` and no new keys beyond the cap. It is treated as capped (§7).
- **Identity is weak.**
  - The key is namespace/name: a release locator, not a recreation-proof identity.
  - An uninstall and reinstall between two reads is invisible. The wording and the notifications (§8) say so.
  - Helm keys never get observed proofs or UID-based deletion guarantees.

## 5. Delivery: a batch is emitted at commit

### 5.1 Commit and emit together

- **Every evaluation commits.** Both the publish and the `RowsNow` bootstrap diff against the ledger and commit it.
- **The same call then hands the resulting batches to the `TransitionSink`,** synchronously on the loop goroutine (`serveRows` runs there too).
  - There is no pending queue. Nothing can overflow (finding 2), and nothing is outstanding at Final (finding 4).
  - A batch is emitted exactly once, at its commit. A repeated bootstrap diffs against the already-committed ledger, so it cannot duplicate an item.
  - In a publish cycle, batches go out as each part is evaluated: before that cycle's `Summary` and `Rows`.
  - A bootstrap's batches go out before its reply's next publish. A consumer orders by `Seq` and correlates with summaries by `PartVer`.
- **Bootstraps are observations.** Transitions are observed only at evaluations: publishes (at most one per second per part) and row bootstraps. A flip and flip-back between two evaluations is never seen.
- **Rows are unchanged.** `rowsLineage`, `Part.Ver`, `samePart` and the rows broadcast keep their contracts. The ledger reads only `Roles`, membership and the continuity identity, none of which are part of `Part`.
- **The `Sink` interface is unchanged.** Transitions go to an optional `TransitionSink` (asserted at `New`), so existing sinks and tests compile.

### 5.2 Replacement checkpoints

- **A baseline is a replacement checkpoint for its partition.** The consumer drops everything it holds for that partition, and its proofs end, before adopting the checkpoint. This covers:
  - keys absent after a cap or an absence (finding 2's "old fault UID disappeared");
  - every first sight.
- **A `loss {capped | absent}` drops the ledger.** The consumer ends that partition's proofs at once.
- **After-gap and changes are deltas** against the last emitted state, which with emit-at-commit is always the committed ledger.

### 5.3 Size

- **A batch holds at most `2 × RoleCap` items** (finding 3). The worst case is a full UID turnover between two evaluations: every old key `gone` plus every new key.
  - The ledger, which counts `unevaluated` entries, never exceeds `RoleCap`, so a diff cannot exceed both sides together.
  - Nothing is truncated: a part over the cap goes capped (§4.2) before a diff is computed.
- **A batch is one value handed to the sink, never split.** The sink applies it atomically under its own lock, so no reader sees half of a replacement. Paging for any wire transport belongs to that consumer.

### 5.4 Types

```go
type TransitionBatch struct {
    Cluster     string
    Incarnation uint64
    Seq         uint64           // per-Observer, strictly increasing in emission order
    Task, Part  string
    Source      string           // the partition; empty on an end batch
    GVR         string           // the effective served GVR captured with this evaluation (§2), kept on losses
    Mode        string           // watch | sampled
    Kind        string           // baseline | changes | after-gap | loss | end
    PartVer     uint64           // the part version of the evaluation
    Identity    ContinuityIdentity // the source's and dependencies' {generation, epoch, coverage, relisting, gvr}, plus the connection epoch
    At          time.Time        // capturedAt: the clock under the capture lock
    PrevAt      *time.Time       // the partition's previous commit (changes), or the loss time (after-gap)
    ReadAt      *time.Time       // sampled: the read used
    PrevReadAt  *time.Time       // sampled: the read the previous commit used
    Loss        *Loss            // {Reason, Coverage}
    AfterCap    bool
    Items       []Transition     // at most 2 × RoleCap
}

type Transition struct {
    Key, NS, Name, GVR string
    From, To           string     // a role, "absent" (new), "gone", or "unevaluated"
    Label, Reason      string     // the new state's, reason at most 200 runes
    Since              *time.Time // the proven rule Since now
    Boundary           *time.Time // the time-only threshold the new role crossed, per item (§1.1)
    Condition          *Condition // the deciding condition's own time, evidence only
    Restated           bool       // same role, Since changed (§4.3)
    Derived            bool       // a time-only change from the same read (§4.4)
}
```

## 6. The observation log (`internal/obslog`)

### 6.1 Entries

- **Consumes the stream** through `HealthSink`. Batches are accepted per cluster in incarnation order (§3), and applied whole under obslog's lock.
- **An entry** is `{at, prevAt, prevReadAt, cluster, incarnation, part, source, GVR, key, ns/name, from → to, label, reason, kind, mode, readAt, boundary, restated, derived, loss?}`.
  - A baseline or a replacement is one entry ("baseline: N objects, F faults"), not one per item.
  - An after-gap is always one marker entry with its gap bounds, even with no changed items, followed by its items.
  - Loss and end batches are entries too.
- **Storage:** an amortized deque per cluster. A push and a trim are O(1), and a batch logs at most the entry bound.
- **Expiry:** against the log's clock, on every write and read, so a quiet log still expires. After an end, partitions are terminally marked "ended". Once an ended cluster's history expires, only its incarnation fence remains.
- **Capped proof markers** carry their partition, are bounded by the proof cap, and are cleared with their partition (replacement, drop, end).
  - LineService's station states and reference changes join in slice 3.
- **Bounds:** 24 hours, or 20,000 entries per cluster, whichever comes first (§7).
- **On disk:** none. The log is in memory only (owner decision 10).

### 6.2 Proofs

- **A proof** is `{role, since *time.Time, notBefore *time.Time, source: rule | observed | sampled | derived | unknown, boundary (the item's own), readAt, condition, partition}`.
- **Proofs are kept only for non-ok roles,** which is what the queue, the inspector and the "failing since" wording need. A key that goes ok, `gone` or `unevaluated` has its proof purged or ended.
- **Precedence:**
  1. **rule:** a proven rule `Since` (parent §8.1) wins whenever present. It survives Klyx's own gaps while it is unchanged.
  2. **observed:** a `changes` item under continuous watch coverage, with no rule `Since`. The role began in `(PrevAt, At]`, worded "since ~<At> (observed)".
  3. **sampled:** a change between two reads (`ReadAt != PrevReadAt`), worded "between <PrevReadAt> and <ReadAt>".
  4. **derived:** a time-only change from the same read. The start is unknown. The proof keeps two pieces of evidence: the item's own `Boundary` and the read it came from. Example wording: "past its pending deadline at 12:01, from the read at 12:00 · start unknown". It is never a sampled interval and never a fresh observation.
  5. **unknown:** a baseline, after-gap or replacement first sight with no rule `Since`, worded "start unknown; first seen <At>". An observed-only baseline never proves a start.
- **The ref's `condition`** (the deciding condition's own age) is shown beside any of these as qualified evidence: "Ready=False since 09:00 (condition time)". It never becomes the proof's start.
- **A restated item** replaces the proof's `since` with the new rule `Since`. Nil downgrades the proof to unknown.
- **On loss,** the partition's proofs are suspended. A `capped` or `absent` loss ends them.
- **On after-gap,** every observed, sampled or derived proof in that partition, changed or not, is downgraded to unknown ("seen before and after a gap"). Rule proofs stay while their `Since` is unchanged.
- **On a baseline,** the partition's proofs are replaced (§5.2).
- **Proofs are never derived from when observation began.**
- **Queries:**
  - `Proof(cluster, key)`;
  - `Recent(cluster, limit)`;
  - `Partition(cluster, task, part)` for the queue's coverage rows.

## 7. Bounds

| What | Bound | Fallback |
|---|---|---|
| `Roles` per evaluation | `RoleCap` = 50,000 per part | `RolesCapped`. Counts, Top and rows continue; the partition goes capped (one loss, then a baseline when back under the cap). |
| Ledger | ≤ `RoleCap` entries per partition, `unevaluated` entries included. Strings are shared with the evaluator. | dropped while capped |
| Pending batches | none: emitted at commit (§5.1) | n/a |
| Items per batch | ≤ 2 × `RoleCap` (a full UID turnover) | n/a: a capped partition emits no diff |
| obslog entries | 20,000 per cluster, 24h. A baseline is one entry. | oldest first. A trimmed age reads "at least since <bound> (log trimmed)". |
| obslog proofs | ≤ 20,000 per cluster (non-ok keys only) | new proofs beyond the cap are not kept, and their queries read "start unknown (proof capped)" |
| Clusters | per-cluster bounds × connected clusters | a cluster's state is released on its `end`, plus the 24h window |

**Measurement before merge:**

- a benchmark of evaluate + diff on an unsubscribed 10,000- and 50,000-pod part, including a full-turnover diff (allocations and latency), with the numbers recorded in the commit;
- a multi-cluster cap-and-recovery fixture.

## 8. Health notifications (parent §8.6) consume the same stream later

The parent's episode state machine applies unchanged. This section only names how each stream item maps to it.

- **Baseline** (any replacement checkpoint): never notifies. Faults present are listed once in the app.
- **New episode:** a `changes` item from a non-fault role into fault, under continuous watch coverage.
- **Unknown:** fault → unknown (or `unevaluated`) suspends the episode, even with live coverage. A later `changes` item back into fault resumes the same episode, with no new notification. A `changes` item into fault opens a new episode only when no suspended episode exists for that key.
- **Coverage lost** (a loss): suspends episodes, and claims no recovery. A following after-gap never notifies, and settles each suspended episode silently:
  - **still fault:** the episode resumes;
  - **ok:** the episode closes with no recovery notification and no asserted recovery time. A later fault is a new episode;
  - **`gone`:** the episode closes as deleted, silently;
  - **unknown or `unevaluated`:** the episode stays suspended.
- **Recovered:** only a `changes` item into ok under continuous coverage.
- **Deleted:** `gone` closes the episode as deleted, with no recovery.
- **Replaced:** a new UID starts its own episode.
- **Incarnation change or end:** everything is re-baselined.
- **Restated and derived items** never open, close or resume an episode.
- **Sampled sources (Helm)** may open an episode from a change between two reads, worded as such. They make no deletion or reinstall claims.

## 9. Tests

`internal/observe` tests follow `observe_test.go` and its fake clock. Tests that need membership use a projection that keeps `ObjectMeta`.

### 9.1 Continuity

- an outage and recovery wholly within one throttle window: a gap;
- a benign error followed by an identical relist, with no object change and no `RowsNow`: a loss and an after-gap with no items (finding 1);
- a benign error with the relist held: a loss, and no after-gap until the relist;
- a resumable watch rotation: continuous;
- a gap while the loop is busy (evaluate held on a blocked fake);
- a served-version change: a new generation and a gap, with batches carrying the old and then the new effective GVR (v1beta2 → v1; extends `TestNativeRefsCarryTheServedVersion`).

### 9.2 Lifecycle

- a baseline;
- a role flip as one item at `capturedAt`;
- flip and flip-back between two evaluations: nothing;
- delete: `gone`; same-name recreation: `gone` plus a new key;
- live → stale: one loss and a frozen ledger; stale → live after a relist: after-gap items only for changed keys.

### 9.3 Connection and lifecycle

- a controller-note change: nothing;
- down/up without object changes: a loss, then an after-gap with no items;
- Final: an `end` batch;
- `RowsNow` then cancel before the scheduled publish: the bootstrap's batch was already emitted, then `end` (finding 4);
- an old Observer completing after its replacement: ignored by obslog.

### 9.4 Delivery

- all five Flux source kinds in one initial evaluation: five baselines, nothing dropped (finding 2);
- bootstrap then publish: exactly once;
- repeated bootstraps: exactly once;
- a cap and recovery where an old fault UID disappeared: the baseline replacement ends its proof (finding 2);
- a full UID turnover of N keys: one batch of 2N items (finding 3);
- retained `unevaluated` entries plus new members: they count toward the cap (finding 3);
- `rowsLineage` and the existing rows tests are unchanged;
- the parity gate stays green.

### 9.5 Since and proofs

- same role with a new `Since`: restated, no episode effect;
- loss and recovery with unchanged roles and a changed `Since`;
- proof deletion on `gone`;
- observed, sampled and derived proofs downgraded after a gap, rule proofs kept;
- condition evidence carried and never used as a start.

### 9.6 Membership and absence

- a route's parent Gateway removed while the route remains: `unevaluated`, never `gone`;
- an entire kind absent: a loss `{absent}`, then a baseline on return;
- mixed-kind absence in one part: only that partition.

### 9.7 Helm

- two reads spanning a change: sampled, with the proof wording "between <t0> and <t>";
- a same-name reinstall: no `gone`, worded as weak identity;
- a time-only pending deadline with no new read: a derived item. Its proof carries the boundary and source read and reads "start unknown", never "between 12:00 and 12:00";
- one delayed evaluation from the same read crosses release A's 12:01 deadline and B's 12:02 deadline: two derived items with their own boundaries, and `At` is the capture time.

### 9.8 Bounds

- the cap: a capped loss, then a baseline;
- obslog entry, proof and age bounds;
- the benchmarks in §7.

### 9.9 Notifications (when §8.6 lands)

- fault → unknown → fault: one episode, one notification;
- fault → loss → after-gap ok → continuous ok → fault, outside the de-duplication window: the old episode closed silently, then a new episode that notifies;
- fault → loss → after-gap `gone`: the episode closed as deleted, silently, never left suspended.
