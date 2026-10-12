import { Events } from "@wailsio/runtime";
import { HealthService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import {
  addSub, applyRows, applySummary, bootstrapSettled, dueForBootstrap, needsReopen, nextWake, openFailed, openStarted, openSucceeded,
  removeSub, useHealth, type HealthSummary, type RowsDTO,
} from "../store/health";
import { perfHooks } from "../health/perfHooks";

// The transport for the health summaries and the lenses' shared rows
// (slice 3 design §2b). Every subscription subscribes to its event BEFORE
// it bootstraps, and the store keeps only strictly newer data; a delta
// that arrives before its base is held until it chains (addendum §3.3). A mounted
// subscription survives the cluster's Observer being replaced (it reopens
// with a fresh token) and a failed open (it retries with backoff).

const SUMMARY_EVENT = "health:summary";
export const rowsEvent = (cluster: string, task: string, part: string) => `health:rows:${cluster}:${task}/${part}`;

let summariesStarted = false;


// startSummaries subscribes to every cluster's summary, then bootstraps.
// Idempotent: the first caller starts it for the app's lifetime.
export function startSummaries(): void {
  if (summariesStarted) return;
  summariesStarted = true;
  Events.On(SUMMARY_EVENT, (ev: { data: HealthSummary }) => {
    if (ev?.data) {
      applySummary(ev.data);
      service();
    }
  });
  void HealthService.Summaries()
    .then((all) => {
      for (const s of (all ?? []) as unknown as HealthSummary[]) applySummary(s);
      service();
    })
    .catch(() => { /* the events keep arriving */ });
}

// Handles are stable per mounted consumer; tokens are minted per open and
// never reused, so a late reply for a closed token can never land on its
// successor (design §2b).
let seq = 0;
const mint = (kind: string) => `${kind}-${Date.now().toString(36)}-${++seq}`;

// openRows opens one part's rows for a lens and returns its close.
export function openRows(cluster: string, task: string, part: string): () => void {
  startSummaries();
  const handle = mint("h");
  addSub(handle, cluster, task, part);
  const off = Events.On(rowsEvent(cluster, task, part), (ev: { data: RowsDTO }) => {
    if (perfHooks.drop?.(`${task}/${part}`)) return;
    const probe = perfHooks.rows;
    const receivedAtMs = probe ? Date.now() : 0;
    const start = probe ? performance.now() : 0;
    const outcome = ev?.data ? applyRows(handle, ev.data) : "ignored";
    if (outcome !== "ignored") service(); // a held delta may need its resync
    if (probe) probe(`${task}/${part}`, start, performance.now(), ev?.data ?? null, receivedAtMs, outcome);
  });
  reopen(handle);
  return () => {
    const sub = useHealth.getState().subs[handle];
    if (typeof off === "function") off();
    removeSub(handle);
    if (sub?.token) void HealthService.CloseRows(sub.token).catch(() => { /* tombstoned or closing */ });
  };
}

// reopen (re)opens a subscription with a fresh token, closing the token it
// held (bound to a replaced Observer, or failed).
function reopen(handle: string): void {
  const sub = useHealth.getState().subs[handle];
  if (!sub) return;
  if (sub.token) void HealthService.CloseRows(sub.token).catch(() => {});
  const token = mint("rows");
  openStarted(handle, token);
  const t0 = perfHooks.bootstrap ? performance.now() : 0;
  HealthService.OpenRows(sub.cluster, sub.task, sub.part, token)
    .then((reply) => {
      const cur = useHealth.getState().subs[handle];
      if (!cur || cur.token !== token) { // closed or reopened meanwhile
        void HealthService.CloseRows(token).catch(() => {});
        return;
      }
      if (!reply || reply.closed) return;
      if (reply.error || !reply.rows) {
        openFailed(handle, token, reply.error || "no rows");
      } else {
        const rows = reply.rows as unknown as RowsDTO;
        openSucceeded(handle, token, rows.incarnation);
        const outcome = applyRows(handle, rows);
        perfHooks.bootstrap?.(`${sub.task}/${sub.part}`, "open", t0, performance.now(), rows, outcome);
      }
      service();
    })
    .catch((e: unknown) => {
      openFailed(handle, token, e instanceof Error ? e.message : String(e));
      service();
    });
}

// service runs what is due now (reopens, A4 re-bootstraps) and arms one
// timer for the next wake.
let timer: ReturnType<typeof setTimeout> | null = null;

export function service(now = Date.now()): void {
  for (const h of needsReopen(now)) reopen(h);
  for (const { handle, token, requestGen } of dueForBootstrap(now)) {
    const t0 = perfHooks.bootstrap ? performance.now() : 0;
    HealthService.RowsBootstrap(token, requestGen)
      .then((reply) => {
        const rows = (reply?.rows as unknown as RowsDTO) ?? null;
        const before = perfHooks.bootstrap ? useHealth.getState().subs[handle] : undefined;
        bootstrapSettled(handle, requestGen, { rows, error: reply?.error ?? null, closed: reply?.closed });
        if (before && rows) {
          const after = useHealth.getState().subs[handle];
          perfHooks.bootstrap?.(`${before.task}/${before.part}`, "rebootstrap", t0, performance.now(), rows, after && after.ver !== before.ver ? "applied" : "ignored");
        }
        service();
      })
      .catch((e: unknown) => { bootstrapSettled(handle, requestGen, { error: e instanceof Error ? e.message : String(e) }); service(); });
  }
  if (timer) { clearTimeout(timer); timer = null; }
  const at = nextWake();
  if (at !== null) timer = setTimeout(() => { timer = null; service(); }, Math.max(0, at - Date.now()));
}

// test seam: reset module state between tests
export function _resetHealthRowsForTest(): void {
  summariesStarted = false;
  seq = 0;
  if (timer) clearTimeout(timer);
  timer = null;
}
