import { Events } from "@wailsio/runtime";
import { HealthService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import {
  addSub, applyRows, applySummary, bootstrapSettled, dueForBootstrap, needsReopen, nextWake, openFailed, openStarted, openSucceeded,
  removeSub, useHealth, type HealthSummary, type RowsDTO,
} from "../store/health";

// The transport for the health summaries and the lenses' shared rows
// (slice 3 design §2b). Every subscription subscribes to its event BEFORE
// it bootstraps, and the store keeps only strictly newer data. A mounted
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
    if (ev?.data && applyRows(handle, ev.data)) service();
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
        applyRows(handle, rows);
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
    HealthService.RowsBootstrap(token, requestGen)
      .then((reply) => { bootstrapSettled(handle, requestGen, { rows: (reply?.rows as unknown as RowsDTO) ?? null, error: reply?.error ?? null, closed: reply?.closed }); service(); })
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
