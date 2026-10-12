# Klyx health 1c, slice 3 addendum: the rows transport after the §6a gate

Status: Codex APPROVE-WITH-CHANGES to the delta direction (2026-10-12); amended (§3-§5) per its four required changes, implemented, and re-measured (§6) - for review. Parent: `2026-10-11-klyx-health-1c-slice3-rows.md` §6a ("If any limit is missed, the transport changes before the Pods lens switches ... reviewed before it is built").

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

## 3. The transport: time-stable rows, a bounded full row, and deltas

### 3.1 Time-stable wire rows (review item 1)

A delta is only as small as the set of rows that really changed, so the rows must not change by ageing. The rows transport carries absolute anchors only:

- `ageSeconds` is not sent on the rows transport for pods, nodes or a workload's nested pods (the lenses tick ages from `createdUnix`); the compatibility list endpoints (`ListPods`, `ListNodes`, `ListWorkloads`) keep their relative fields;
- nothing is excluded from a fingerprint: what is sent is what is compared.

The captured verdict is sent as evaluated. A verdict whose text is relative ("restarted 4m ago", "last OOMKilled 3m ago", "deletion deadline passed 2m ago") changes when its minute changes, and that row upserts: the change is real verdict text, bounded to pods with such a history and to once a minute each. A verdict that changes at a timer boundary (reclassification) upserts too.

### 3.2 A bounded full pods row (review item 2)

The full representation (bootstrap replies, a first push, any push after a reset) is bounded now, not as a fallback. The pods rows use a list row type, `PodRowDTO`:

- kept: identity (namespace, name, uid, gvr, generation), owner (kind, name, uid), node, ip, ready, restarts, `createdUnix`, the captured verdict without its message (kind, label, reason, rule, native, note, rank), and per container its name, init, ready, restarts and state;
- moved to the UID-fenced detail (`GetPodDetail`, which already returns the full `PodSummaryDTO`): the verdict message, and each container's image, ports, declared resources and last termination;
- dropped from the rows: the legacy `rank`, `reason` and `phase` strings and `ageSeconds`.

The type split makes consumer parity a compile check: the pods slice holds `PodRowDTO`; the inspector reads the message and the container detail from the detail of the selected UID (with its read time), and says "loading" for them until it arrives - never an empty claim. On the fixture this measures 948 bytes a row today; the slim row removes about 40 per cent of it (message 18, container detail 17.5, legacy and relative fields 6). Cold bootstrap and a forced re-bootstrap are measured as well as steady churn, and the RSS gate is unchanged: deltas reduce allocation, and the run must show the retained set under 400 MB.

### 3.3 Deltas and their identity (review item 3)

Every rows message is an envelope with the part (counts, coverage, kinds, `Ver`), the captured facts and `evaluatedAt`, always, plus either:

- `full: true` and `rows`; or
- a delta: `base` (the `Ver` it applies to), `upserts` (rows, by UID, whose sent form changed) and `removed` (UIDs gone).

An empty delta (no row changed) still carries the part, facts and evaluation time, so a fact-only, freshness-only or coverage-only change arrives atomically.

**Go: one lineage per (cluster, incarnation, task, part).** The rows sink keeps, per key, the last published state: its `Ver`, a fingerprint per UID (a hash of the row's sent JSON) and the full rows. A publish emits a delta against it, or full when there is no state, the incarnation changed, or the rows lack valid unique UIDs (a row with an empty or duplicate UID forces full). Lifecycle: a key's state is dropped when its part's last subscription closes, when a newer incarnation of the cluster publishes, and on the cluster's Final.

**Bootstrap replies come from the lineage.** `OpenRows` and `RowsBootstrap` reply with the lineage's last published state, in full, at its `Ver` - never a capture the broadcast has not published, so a subscriber never holds a `Ver` outside the lineage (the case of a fresh `RowsNow` capture running ahead of the sink's baseline). With no state yet (the part's first subscriber), the reply captures with `RowsNow`, and that capture seeds the lineage as its published state under the sink's lock; a broadcast already newer than it wins and the reply uses that instead.

**Frontend.** For each subscription, per (incarnation, `Ver`):

- an older or equal message is ignored, with no retry;
- a full message replaces the rows and metadata;
- a delta whose `base` equals the held `Ver` (same incarnation) applies: rows are upserted by UID and removed by UID, and the metadata replaced;
- a genuinely newer delta with a wrong base leaves rows and metadata unchanged and requests one fenced full bootstrap, backed off like a failed bootstrap - on its own, whether or not a newer summary arrived (the A4 mismatch trigger alone is not enough);
- a bootstrap reply racing deltas follows the same (incarnation, `Ver`) order.

Several consumers of one part share the broadcast and each apply it independently.

Scope: pods/pods, workloads/workloads and nodes/nodes - the verdict parts with large row sets, whose lenses sort themselves (so row order is not part of the contract). Native parts keep full pushes.

### 3.4 The probe anchors to the lens commit (review item 4)

The timing that proves the gate ends at the React commit of the lens that shows the pushed `Ver`, not at a scheduled task:

- the rows adapter carries the accepted (incarnation, `Ver`) into the lens slice, and the lens reports, from a layout effect, the `Ver` its committed render shows (a no-op hook outside the lab build);
- per accepted message (not per event; a rejected or ignored message is counted apart): emit to handler (transport and decode), the handler (merge into the store), and handler to that lens commit, and emit to commit in total;
- interaction samples count only when the rendered list changed (the shown count or the first visible row), not only the input's own value;
- frame intervals stay diagnostics.

## 4. Tests

- Go, deltas: applying every delta to the previous full equals the next full, under randomized churn; an advancing clock with one object mutated upserts only that row (and rows whose captured verdict text changed, including at a timer boundary); removal; recreation (new UID upserted, old UID removed); empty and duplicate UIDs force full; an incarnation change and a reset publish full; lifecycle cleanup on last close, a newer incarnation and Final; an empty delta still carries a fact-only, freshness-only or coverage-only change.
- Go, bootstrap: a reply is the lineage's published state; the first subscriber's capture seeds the lineage; a capture older than a concurrent broadcast loses to it; multiple consumers share one lineage.
- Go, wire: the pods rows carry no relative age, no message and no container detail; the detail still carries them.
- Frontend: a delta applies only on its base; a wrong base leaves rows and metadata unchanged and requests one backed-off fenced full bootstrap with no newer summary; older and equal messages are ignored without a retry; duplicate and delayed deltas; a full reply racing deltas; A4 unchanged; any sequence ends equal to a full replace; multiple consumers.
- Frontend, consumers: the Pods inspector shows the verdict message and container detail from the selected UID's detail, "loading" until then; the row tooltip and the palette use the row's own fields.
- Lab: the §6a gate re-measured with the anchored probe and the trace - steady churn, cold bootstrap and a forced re-bootstrap; push size, emit to commit p95, interaction p95 and RSS over 5 minutes at 50 updates/s.

## 5. The Pods slice until then

Codex's ruling: the Pods lens switch (2ec1168 and its review fixes) stays on the unpushed branch with slice 3 open; it is not shipped or closed until the amended transport passes every §6a gate. The three reported failures (pods push size, pods emit-to-commit p95, WebContent RSS) are closure blockers.

## 6. Implemented, and the gate re-measured

### 6.1 What was built

- Go: `PodRowDTO` (the bounded row) and time-stable node and workload rows (`ageSeconds` omitted on the rows transport, kept on the list endpoints); `RowsDTO` gains `full`, `base`, `upserts`, `removed`. `internal/appbridge/rows_lineage.go` keeps one lineage per (cluster, incarnation, task, part) for pods, workloads and nodes: retained by each subscription before it subscribes, dropped on the last close, by a newer incarnation and by a Final; a Ver the lineage holds is not re-emitted; a row set without valid unique UIDs travels in full. Bootstraps answer from the lineage; only a part with no published state captures, and that capture seeds it (a broadcast that landed first wins). `LinkRowsLineage` shares the sink's lineage with the service (unlinked, everything stays full, as before).
- Frontend: the health store applies a full message, or a delta whose base is the Ver held; any other newer delta leaves rows and metadata unchanged and is held (bounded at 16) - in the held incarnation it sets `resync`, which makes one fenced full bootstrap due at once and backs it off on failure, with or without a summary, and which a summary that is not ahead never cancels. Held deltas apply when they chain. An empty delta keeps the rows' identity. `applyRows` reports applied/held/ignored. The Pods inspector reads the verdict message and the container image, ports, resources and last termination from the UID-fenced detail ("Reading the verdict message…", "loading…" until it lands; "not read" when it cannot); the palette and rows read the row's own fields.
- Probe: the lens reports the rows Ver its committed render shows (`useCommitProbe`, a layout effect); per accepted message the probe times transport and decode, the handler, and handler to that commit; bootstraps from call to commit; a forced lost message makes a real wrong-base resync; interaction samples count only when the rendered list changed.
- The lab churn gained a row-visible mode (`KLYX_LAB_CHURN_KIND=status`: each patch changes the pod's status IP, so every update is an upsert). The label churn of §1 changes no lens row: with deltas it yields metadata-only messages, so it no longer represents a churn on its own.

### 6.2 Results (kind lab, 5017 pods, Pods lens on screen, Workloads and Nodes rows beside it)

Row-visible churn, 50 pod updates/s (status IP patches, about 50 upserts a push), Go trace over the churn after its first minute and the probe's 120 s windows:

| | limit | measured | |
|---|---|---|---|
| push rate, pods and workloads | ≤ 1/s per part | 1.00/s (min gap 972 ms) | pass |
| pods push size, steady (delta) | ≤ 4 MB | 0.027 MB p95 | pass |
| pods full form (bootstrap reply) | ≤ 4 MB | 2.47 MB | pass |
| pods emit to lens commit, p95 (accepted deltas) | ≤ 50 ms | 4 ms (max 9; transport + decode p95 1-2, handler ≤ 1, handler to commit p95 3) | pass |
| search keystroke to paint, p95 | ≤ 100 ms | 33 ms (n 40, every sample a list change) | pass |
| filter click to paint, p95 | ≤ 100 ms | 30-32 ms (n 40, every sample a list change) | pass |
| WebContent RSS, steady | ≤ 400 MB | plateau 339-342 MB (15-minute run) | pass |
| WebContent RSS growth over 5 min | ≤ 10% | +0.9% at the plateau (339 to 342); see warm-up below | pass at steady state |

Bootstraps (measured, one-off):

| | measured |
|---|---|
| cold open (first subscriber: capture, seed, reply) | call 36-40 ms, call to lens commit 53-55 ms (three launches) |
| re-bootstrap from the lineage (2.47 MB reply) | call 17-24 ms, call to lens commit 22-27 ms |
| forced lost message: held delta to the commit of the full reply | 8-20 ms |

RSS warm-up: from launch the WebContent process grows in steps (about 10-28 MB each) while churn runs - 142 MB at launch, 200 MB when churn starts, 306 MB at 6 minutes, 339 MB at 11 minutes - and then holds at 339-342 MB for the last five minutes of a 15-minute run (45000 updates) and after it. The steps do not follow the forced re-bootstraps (a run without them grows the same way) and stop at a plateau while the churn continues, which points to the webview's heap and code caches sizing up rather than retained rows - an inference, not a trace (Wails delivers every event as a uniquely sourced `ExecJS` script, which the webview compiles). In a 5-minute window inside the warm-up the growth exceeds 10% (for example 181 to 258 MB in the 6-minute run); the old full-push transport was measured only in its steady window (550-620 MB). Metadata-only churn (labels) ran at 2 KB a push and 183-236 MB.

Question for review: is the steady-state reading of the RSS growth limit (the plateau, after warm-up) the gate's intent, given the warm-up above? If not, the remaining lever is the delivery path (fetching rows over the call path instead of `ExecJS` events), which this slice has not built.

