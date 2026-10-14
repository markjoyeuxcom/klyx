# The Line, slice 2b: the role ledger, transition stream and observation log

**Parent spec:** `2026-10-14-klyx-the-line-redesign.md` §8.2 (the transition stream and the observation log) and §8.6 (health notifications, a consumer).

This note turns §8.2 into an Observer design, grounded in how the Observer works today. It needs Codex's design review before any code.

## 0. Constraints from the current Observer

All references are to `internal/observe` and `internal/healthsrc`.

- **The complete per-object evaluated state is kept only for subscribed parts.**
  - Every evaluation computes all items, but only the `Part` survives it: counts, Top ≤ 5, `Next` and `Kinds`.
  - Rows are built only when the part is subscribed or `RowsNow` is called (`publish.go:196-206, 240-244`).
- **`PartSpec.Evaluate` must be pure:** the same capture gives the same result (`observer.go:48-50`).
- **The 1s throttle evaluates only the state at publish time** (`publish.go:28-37`). A flip inside one window is never seen.
- **The incarnation is per connection lifetime.** A reconnect or watch drop does not change it. Instead:
  - a reflector restarts inside the same kind generation;
  - a kind generation changes only on a served-version change, or on absent → served;
  - the connection word changes through `SetConnection`.
- **Coverage is per kind** (`Checking`, `Live`, `Stale`, `Unreadable`, `Absent`) and rolls up per part. A stale kind evaluates over its last good model (`publish.go:285-288`).
- **Sink calls are synchronous** on the loop goroutine (`observer.go:72-77`).

## 1. Evaluators emit a role list, always

- `observe.PartResult` gains `Roles []RoleEntry`, produced by every evaluator on every evaluation, subscribed or not:

  ```go
  type RoleEntry struct {
      Key   string     // UID; namespace/name for Helm releases (no UID)
      GVR   string
      NS    string
      Name  string
      Role  health.Kind
      Since *time.Time // the rule-backed Since (2a), when the rule has one
  }
  ```

- **Producers:**
  - native parts: from their `nativeItem`s;
  - verdict parts (workloads, pods, nodes): from each object's verdict kind;
  - routes: from the attachments after reduction;
  - Helm: from its releases, keyed by namespace/name;
  - the evidence-only Events part produces none.
- **Evaluate stays pure:** the role list is output, never state.

## 2. The ledger: Observer state, per part

- `partState` gains `ledger map[string]ledgerEntry`: the role list of the last evaluation that was **live**, keyed by `Key`.
- After each evaluation, the loop compares the new role list with the ledger, under the rules in §3. Only then does it replace the ledger.
- **Bounds:** at most 100,000 entries per part.
  - A part over the bound stops diffing and emits a `capped` loss marker.
  - Its rows and counts are unaffected.

## 3. Coverage decides what a diff means

A part's coverage at evaluation is the roll-up of its kinds.

| Previous | Now | What the loop emits |
|---|---|---|
| none (first evaluation) | live | a **baseline** batch of every entry. Nothing is a transition: each state is "first observed at <t>; start unknown". |
| live | live | a **transition** for each key whose role changed, appeared (from `absent`) or disappeared (to `gone`), at the evaluation's capture time |
| live | stale, unreadable or checking | one **loss marker** for the part (`{from: live, to: <coverage>, at}`). The ledger is frozen and no transitions are emitted. |
| stale, unreadable or checking | stale, unreadable or checking | nothing |
| stale, unreadable or checking | live | an **after-gap** batch. Each key that differs from the frozen ledger becomes a transition flagged `afterGap {from: <loss time>, to: <now>}`, meaning "observed at <now>, after a gap". Unchanged keys are re-confirmed, with no transition. |

- **A kind generation change** (served version) inside a part is a gap: a loss marker, then after-gap rules once live.
- **A connection that leaves `connected`** (`SetConnection`) emits a loss marker for every part. Reconnection flows through each kind's coverage.
- **A `Replace`** (relist) after an outage collapses the outage into one model swap. The after-gap rule is exactly what that needs.
- **A deletion** is a transition to `gone`. Keys are UIDs, so a same-name recreation (a new UID) shows as `gone` plus a new key.

## 4. The stream

- The Sink gains an optional `Transitions(TransitionBatch)`, called synchronously after `Summary` and before `Rows`, on the loop goroutine:

  ```go
  type TransitionBatch struct {
      Cluster     string
      Incarnation uint64
      Task, Part  string
      At          time.Time        // the evaluation's capture time
      Coverage    Coverage
      Kind        string           // baseline | transitions | after-gap | loss
      Gap         *struct{ From, To time.Time }
      Loss        *struct{ From, To Coverage; Reason string } // reason: coverage | generation | connection | capped
      Items       []Transition     // {Key, GVR, NS, Name, From, To string, Since *time.Time}
  }
  ```

- **Throttling is disclosed:** transitions are between successive published evaluations, at most one per second per part. A flip and flip-back inside one window is never seen. The log says "observed at", never "changed at".

## 5. The observation log (`internal/obslog`)

- **Consumes** the stream, per cluster and incarnation. An incarnation change clears that cluster.
- **Entries:** `{at, cluster, incarnation, GVR, key, ns/name, from → to, kind, gap?}`.
  - Loss markers are entries too.
  - LineService's station states and reference changes join in slice 3.
- **Bounds:** 24 hours, or 20,000 entries per cluster.
- **Per-object state proof:** `{role, provenSince *time.Time, sinceSource: rule | observed | unknown}`. It resets on every change and every gap, and is purged on `gone` once its episode closes. Rules for `provenSince`:
  - A rule-backed `Since` (from 2a) is used when present.
  - Otherwise a transition seen with continuous live coverage gives "since <at> (observed)".
  - A baseline or after-gap first sight gives "start unknown".
  - A proof is never derived from when observation began.
- **Queries:**
  - `Proof(cluster, key)` for the queue, the inspector and the line's hold time;
  - `Recent(cluster, limit)`.
- **On disk:** in memory only (owner decision 10).

## 6. Health notifications (§8.6) consume the same stream later

- A baseline never notifies.
- A new episode is a `transitions` item into fault, under continuous live coverage.
- A loss marker suspends episodes; after-gap items never notify.
- Unknown or checking suspends an episode, and a return to fault resumes it.
- Recovery is only into `ok`.

## 7. Tests (`internal/observe`, following `observe_test.go`)

- Rows unaffected by the ledger: `RowsNow` and subscribed rows are unchanged, and parity stays green.
- The cases:
  - a first evaluation as a baseline;
  - a role flip as one transition, at capture time;
  - flip and flip-back inside one throttle window, with nothing emitted;
  - a delete as `gone`;
  - a same-name recreation as `gone` plus new;
  - live → stale as one loss marker and a frozen ledger;
  - stale → live after a relist as after-gap items only for changed keys;
  - a generation change as a gap;
  - `SetConnection` down and up;
  - the 100,000-entry cap;
  - Helm keyed by namespace/name;
  - routes after reduction.
- `obslog`:
  - the bounds;
  - proofs for each source;
  - reset on a gap;
  - purge on `gone`;
  - an incarnation change.
