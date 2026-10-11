import { create } from "zustand";

// The health Observer's publications (1c spec §4) and the lenses' shared
// rows (slice 3 design §2-§4a). The transport lives in bridge/healthRows.ts;
// this store holds what arrived and decides what is current. Every rule
// here is a pure function of the store, so it is tested without a runtime.

export type Coverage = "live" | "stale" | "checking" | "unreadable" | "absent";

export type KindCoverage = {
  kind: string; role: string; scope: string; coverage: Coverage;
  class?: string; error?: string; objects: number; truncated?: boolean;
  asOf?: string; lastGoodAt?: string; cadenceSec?: number; discovery?: string;
};
export type Counts = { total: number; fault: number; degraded: number; unknown: number; progressing: number; paused: number; ok: number };
export type NativeCounts = { total: number; roles: Counts; included: number; word: string };
export type HealthRef = { cluster: string; gvr: string; namespace?: string; name: string; uid?: string; parent?: string; role: string; label: string; generation?: number; route?: string };
export type HealthPart = {
  id: string; entity: string; scope: string; classifier: "verdict" | "native" | "evidence"; coverage: Coverage;
  kinds: KindCoverage[]; counts?: Counts; native?: NativeCounts; evidence?: number; top?: HealthRef[];
  obs: number; ver: number; asOf?: string; next?: string;
};
export type HealthTask = { id: string; parts: HealthPart[]; controller?: { state: string; reason?: string }; coverage: Coverage; asOf?: string };
export type HealthSummary = {
  cluster: string; incarnation: number; seq: number; evaluatedAt: string; connection: string;
  namespaces?: number; tasks: HealthTask[]; final?: boolean;
};
export type RowsDTO<T = unknown> = { cluster: string; incarnation: number; task: string; part: HealthPart; evaluatedAt: string; rows: T[] };

// A4: how long a summary may be ahead of the on-screen rows before the
// counts switch to the summary and the rows are qualified (spec §4).
export const MISMATCH_DEADLINE_MS = 1000;

export type RowsSub = {
  token: string; cluster: string; task: string; part: string;
  incarnation: number | null; // of the rows held
  ver: number; // -1 before any rows
  partData: HealthPart | null;
  rows: unknown[] | null;
  evaluatedAt: string | null;
  mismatchSince: number | null; // when the summary first got ahead (ms)
  refreshing: boolean; // past the deadline: summary counts, rows "as of … - refreshing"
  requestGen: number; // the fenced re-bootstrap generation
  error: string | null;
};

type HealthState = {
  summaries: Record<string, HealthSummary>;
  finalizedThrough: Record<string, number>;
  subs: Record<string, RowsSub>;
};

export const useHealth = create<HealthState>(() => ({ summaries: {}, finalizedThrough: {}, subs: {} }));

export function findPart(s: HealthSummary | undefined, task: string, part: string): HealthPart | undefined {
  return s?.tasks.find((t) => t.id === task)?.parts.find((p) => p.id === part);
}

// reconcile recomputes one subscription's A4 state against the summary.
function reconcile(sub: RowsSub, summary: HealthSummary | undefined, now: number): RowsSub {
  const sp = summary && sub.incarnation !== null && summary.incarnation === sub.incarnation ? findPart(summary, sub.task, sub.part) : undefined;
  if (!sp || sp.ver <= sub.ver) {
    // resolved: rows at or above the summary (or nothing to compare)
    return sub.mismatchSince === null && !sub.refreshing ? sub : { ...sub, mismatchSince: null, refreshing: false };
  }
  // The deadline is fixed at the FIRST unresolved mismatch; later ticks never reset it.
  return sub.mismatchSince === null ? { ...sub, mismatchSince: now } : sub;
}

function reconcileCluster(st: HealthState, cluster: string, now: number): Record<string, RowsSub> {
  const subs = { ...st.subs };
  for (const [token, sub] of Object.entries(subs)) {
    if (sub.cluster === cluster) subs[token] = reconcile(sub, st.summaries[cluster], now);
  }
  return subs;
}

// applySummary keeps only a strictly newer (incarnation, seq). A Final
// clears the cluster's rows of that incarnation and fences everything at
// or below it; a newer incarnation's rows reset the held state.
export function applySummary(s: HealthSummary, now = Date.now()): void {
  useHealth.setState((st) => {
    const cur = st.summaries[s.cluster];
    const fin = st.finalizedThrough[s.cluster] ?? 0;
    if (s.incarnation <= fin) return st;
    if (cur && (s.incarnation < cur.incarnation || (s.incarnation === cur.incarnation && s.seq <= cur.seq))) return st;
    if (s.final) {
      const summaries = { ...st.summaries };
      delete summaries[s.cluster];
      const subs = { ...st.subs };
      for (const [token, sub] of Object.entries(subs)) {
        if (sub.cluster === s.cluster && (sub.incarnation === null || sub.incarnation <= s.incarnation)) {
          subs[token] = { ...sub, incarnation: null, ver: -1, partData: null, rows: null, evaluatedAt: null, mismatchSince: null, refreshing: false };
        }
      }
      return { summaries, subs, finalizedThrough: { ...st.finalizedThrough, [s.cluster]: s.incarnation } };
    }
    const next = { ...st, summaries: { ...st.summaries, [s.cluster]: s } };
    return { ...next, subs: reconcileCluster(next, s.cluster, now) };
  });
}

export function addSub(token: string, cluster: string, task: string, part: string): void {
  useHealth.setState((st) => ({
    subs: { ...st.subs, [token]: { token, cluster, task, part, incarnation: null, ver: -1, partData: null, rows: null, evaluatedAt: null, mismatchSince: null, refreshing: false, requestGen: 0, error: null } },
  }));
}

export function removeSub(token: string): void {
  useHealth.setState((st) => {
    if (!st.subs[token]) return st;
    const subs = { ...st.subs };
    delete subs[token];
    return { subs };
  });
}

export function setSubError(token: string, error: string): void {
  useHealth.setState((st) => (st.subs[token] ? { subs: { ...st.subs, [token]: { ...st.subs[token], error } } } : st));
}

// applyRows takes a rows payload for one subscription: only its own part,
// never a finalized incarnation, and only strictly newer (incarnation, Ver).
// A newer incarnation resets the held Ver and any A4 state.
export function applyRows(token: string, dto: RowsDTO, now = Date.now()): boolean {
  let applied = false;
  useHealth.setState((st) => {
    const sub = st.subs[token];
    if (!sub || dto.cluster !== sub.cluster || dto.task !== sub.task || dto.part.id !== sub.part) return st;
    if (dto.incarnation <= (st.finalizedThrough[sub.cluster] ?? 0)) return st;
    if (sub.incarnation !== null) {
      if (dto.incarnation < sub.incarnation) return st;
      if (dto.incarnation === sub.incarnation && dto.part.ver <= sub.ver) return st;
    }
    applied = true;
    const base = sub.incarnation !== null && dto.incarnation > sub.incarnation ? { ...sub, mismatchSince: null, refreshing: false } : sub;
    const next: RowsSub = { ...base, incarnation: dto.incarnation, ver: dto.part.ver, partData: dto.part, rows: dto.rows ?? [], evaluatedAt: dto.evaluatedAt, error: null };
    return { subs: { ...st.subs, [token]: reconcile(next, st.summaries[sub.cluster], now) } };
  });
  return applied;
}

// dueForBootstrap marks every subscription past its A4 deadline as
// refreshing, bumps its request generation, and returns what to ask for.
export function dueForBootstrap(now = Date.now()): { token: string; requestGen: number }[] {
  const due: { token: string; requestGen: number }[] = [];
  useHealth.setState((st) => {
    let subs: Record<string, RowsSub> | null = null;
    for (const [token, sub] of Object.entries(st.subs)) {
      if (sub.mismatchSince !== null && !sub.refreshing && now - sub.mismatchSince >= MISMATCH_DEADLINE_MS) {
        subs ??= { ...st.subs };
        subs[token] = { ...sub, refreshing: true, requestGen: sub.requestGen + 1 };
        due.push({ token, requestGen: sub.requestGen + 1 });
      }
    }
    return subs ? { subs } : st;
  });
  return due;
}

// applyBootstrap takes a fenced re-bootstrap reply: only the current
// request generation of a live subscription, and then only a newer Ver.
export function applyBootstrap(token: string, reply: { rows?: RowsDTO | null; requestGen?: number; closed?: boolean }, now = Date.now()): boolean {
  const sub = useHealth.getState().subs[token];
  if (!sub || reply.closed || !reply.rows || reply.requestGen !== sub.requestGen) return false;
  return applyRows(token, reply.rows, now);
}

export type SelectedCounts = {
  part: HealthPart | null; // whose counts to show
  source: "rows" | "summary" | "none";
  updating: boolean; // summary ahead, inside the deadline: rows' counts, marked
  refreshing: boolean; // past the deadline: the summary's counts; rows qualified as of their evaluation
};

// taskCounts is the ONE count selector (design §4a) for the lens header,
// the nav tree and the mast: a part whose rows are on screen shows its
// rows' counts until A4 switches every surface to the summary together;
// any other part shows the summary's.
export function taskCounts(st: HealthState, cluster: string, task: string, part: string): SelectedCounts {
  const summaryPart = findPart(st.summaries[cluster], task, part) ?? null;
  let best: RowsSub | null = null;
  for (const sub of Object.values(st.subs)) {
    if (sub.cluster === cluster && sub.task === task && sub.part === part && sub.partData && (!best || sub.ver > best.ver)) best = sub;
  }
  if (!best) return { part: summaryPart, source: summaryPart ? "summary" : "none", updating: false, refreshing: false };
  if (best.refreshing && summaryPart) return { part: summaryPart, source: "summary", updating: false, refreshing: true };
  return { part: best.partData, source: "rows", updating: best.mismatchSince !== null, refreshing: false };
}
