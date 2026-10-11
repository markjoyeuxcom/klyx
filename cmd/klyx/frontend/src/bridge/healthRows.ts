import { Events } from "@wailsio/runtime";
import { HealthService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import {
  MISMATCH_DEADLINE_MS, addSub, applyBootstrap, applyRows, applySummary, dueForBootstrap, removeSub, setSubError, useHealth,
  type HealthSummary, type RowsDTO,
} from "../store/health";

// The transport for the health summaries and the lenses' shared rows
// (slice 3 design §2b). Every subscription subscribes to its event BEFORE
// it bootstraps, and the store keeps only strictly newer data.

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
      scheduleDeadlines();
    }
  });
  void HealthService.Summaries()
    .then((all) => {
      for (const s of (all ?? []) as unknown as HealthSummary[]) applySummary(s);
      scheduleDeadlines();
    })
    .catch(() => { /* the events keep arriving; a failed bootstrap is retried by the next summary */ });
}

// tokens are minted per open: never reused, so a late reply for a closed
// subscription can never land on its successor (design §2b).
let tokenSeq = 0;
const mint = () => `rows-${Date.now().toString(36)}-${++tokenSeq}`;

// openRows opens one part's rows for a lens and returns its close. Closing
// before the bootstrap reply arrives is safe: the backend tombstones the
// token, and the store drops anything for a removed subscription.
export function openRows(cluster: string, task: string, part: string): () => void {
  startSummaries();
  const token = mint();
  let open = true;
  addSub(token, cluster, task, part);
  const off = Events.On(rowsEvent(cluster, task, part), (ev: { data: RowsDTO }) => {
    if (open && ev?.data && applyRows(token, ev.data)) scheduleDeadlines();
  });
  void HealthService.OpenRows(cluster, task, part, token)
    .then((reply) => {
      if (!open || !reply || reply.closed) return;
      if (reply.error) setSubError(token, reply.error);
      else if (reply.rows) {
        applyRows(token, reply.rows as unknown as RowsDTO);
        scheduleDeadlines();
      }
    })
    .catch((e: unknown) => { if (open) setSubError(token, e instanceof Error ? e.message : String(e)); });
  return () => {
    if (!open) return;
    open = false;
    if (typeof off === "function") off();
    removeSub(token);
    void HealthService.CloseRows(token).catch(() => { /* the token is tombstoned or the app is closing */ });
  };
}

// The A4 deadline: one timer, re-armed at the earliest open mismatch.
let deadline: ReturnType<typeof setTimeout> | null = null;

export function scheduleDeadlines(now = Date.now()): void {
  const pending = Object.values(useHealth.getState().subs).filter((s) => s.mismatchSince !== null && !s.refreshing);
  if (deadline) { clearTimeout(deadline); deadline = null; }
  if (pending.length === 0) return;
  const at = Math.min(...pending.map((s) => (s.mismatchSince as number) + MISMATCH_DEADLINE_MS));
  deadline = setTimeout(fireDeadlines, Math.max(0, at - now));
}

function fireDeadlines(): void {
  deadline = null;
  for (const { token, requestGen } of dueForBootstrap()) {
    void HealthService.RowsBootstrap(token, requestGen)
      .then((reply) => { if (reply && applyBootstrap(token, reply as never)) scheduleDeadlines(); })
      .catch(() => { /* the next event, or the next deadline, retries */ });
  }
  scheduleDeadlines();
}

// test seam: reset module state between tests
export function _resetHealthRowsForTest(): void {
  summariesStarted = false;
  tokenSeq = 0;
  if (deadline) clearTimeout(deadline);
  deadline = null;
}
