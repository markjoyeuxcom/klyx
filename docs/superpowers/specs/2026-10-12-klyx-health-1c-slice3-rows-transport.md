# Klyx health 1c, slice 3 addendum: the rows transport after the §6a gate

Status: draft for Codex review (2026-10-12). Parent: `2026-10-11-klyx-health-1c-slice3-rows.md` §6a ("If any limit is missed, the transport changes before the Pods lens switches ... reviewed before it is built").

## 1. What the gate measured

Kind lab, the klyx-load fixture (5000 Pending pods across 10 namespaces, 1000 Deployments), the perf build of 2ec1168 with its review fixes (probe in the real Wails webview, `KLYX_ROWS_TRACE` on the Go side), Pods lens on screen with Workloads and Nodes rows open beside it.

Before churn (the cluster's own restarts only):

| | limit | measured | |
|---|---|---|---|
| pods push rate | ≤ 1/s per part | 0.32/s | pass |
| pods push size (complete RowsDTO, Go trace) | ≤ 4 MB | 4.79 MB (5017 rows, about 950 bytes a row) | **fail** |
| pods emit to committed, p95 | ≤ 50 ms | 88 ms (transport + decode 87, merge 0, render 8) | **fail** |
| workloads push size / emit to committed p95 | - | 1.75 MB / 44 ms | near the limit |
| interaction (search), p95 | ≤ 100 ms | 37 ms | pass |

Under churn (50 pod updates/s for 6 minutes, 18000 label patches; the probe's last 120s window and the Go trace over the churn after its first minute):

| | limit | measured | |
|---|---|---|---|
| push rate, pods and workloads | ≤ 1/s per part | 1.00/s each (min gap 978 ms) | pass |
| pods push size (complete RowsDTO) | ≤ 4 MB | 4.79 MB p95 (4.57 MiB) | **fail** |
| pods emit to committed, p95 | ≤ 50 ms | 73 ms (transport + decode 65, merge 0, render 10; max 77) | **fail** |
| workloads emit to committed, p95 | ≤ 50 ms | 31 ms (1.68 MB pushes) | pass |
| search keystroke to paint, p95 | ≤ 100 ms | 43 ms (n 40) | pass |
| filter click to paint, p95 | ≤ 100 ms | 33 ms (n 40, every click a real change) | pass |
| WebContent RSS, steady | ≤ 400 MB | 550-620 MB (GC sawtooth) | **fail** |
| WebContent RSS growth over 5 min | ≤ 10% | about 3% (599 to 619 MB) | pass |

"Emit to committed" is the Go emit time (stamped only while the trace is on) to the end of the React work the push caused (a MessageChannel task after React's microtask-flushed sync renders), on one machine's wall clock: decode, merge and the lens's sort, with transport included - conservative for the gate's "event receipt to store committed". The frame-interval figures in the overlay are diagnostics only.

The cost is the decode of the whole part on every push: merge and render are small; transport plus decode scales with the payload, and every push re-sends every row whether or not it changed. The steady memory follows from the same: a 4.8 MB message decoded into fresh objects every second.

## 2. Why the §6a candidate is not enough

The named candidate - per-owner, namespace-filtered payloads - shrinks a push only while a namespace is selected. The default view is all namespaces, which is the measured case; it would still fail. Per-owner payloads also add per-owner converter work and payload identity the broadcast does not need.

## 3. Proposal: row deltas on the existing envelope

A rows push carries the part (counts, coverage, kinds, Ver) in full, as now, plus either:

- **full**: every row (the bootstrap reply, a first push, and any push after a fence); or
- **delta** against the previous published Ver of that part: `base` (the Ver it applies to), `upserts` (rows whose converted DTO changed, by UID), `removed` (UIDs gone).

Go side:

- the sink keeps, per part, a fingerprint per row UID of the last published converted DTO (a hash of its JSON) and its Ver;
- each publish compares and emits a delta; a part with no previous publish, an incarnation change or a fingerprint map reset emits full;
- the bootstrap reply (RowsBootstrap / OpenRows) is always full, at its Ver.

Frontend (`store/health` applyRows):

- a full payload replaces the rows, as now;
- a delta applies only when the held Ver equals `base` and the incarnation matches; it upserts by UID and removes UIDs; anything else (a gap, a dropped event, a delta older or newer than expected) triggers the existing fenced re-bootstrap and is otherwise ignored;
- the A4 machinery is unchanged (it keys on Ver).

Scope: the verdict parts with large row sets - pods/pods, workloads/workloads, nodes/nodes. Their lenses already sort themselves (slice 3 reviews), so row order is not part of the contract. Native parts keep full pushes.

Second lever, only if the full bootstrap reply itself misses the budget: slim the pods list row (containers' ports, resources and last state move to the UID-fenced detail, which already exists).

## 4. Tests

- Go: delta correctness against full (apply every delta to the previous full and compare with the next full) under randomized churn; upsert on any field change including the captured verdict; removal; recreation (new UID, old UID removed); incarnation change and a reset emit full; the bootstrap reply is full.
- Frontend: a delta applies only on its base; a gap re-bootstraps and never applies; a dropped and a delayed delta; a delta racing a bootstrap reply; A4 unchanged; rows content equal to a full replace after any sequence.
- Lab: the §6a gate re-measured with the same probe and trace - push size, emit to committed p95, interaction p95 and RSS over 5 minutes at 50 updates/s.

## 5. The Pods slice until then

The Pods lens switch (2ec1168 and its review fixes) stays on the unpushed branch with the slice open; it closes only after this transport passes the re-measured gate. Alternatively the lens switch can be reverted until then - a decision for review.
