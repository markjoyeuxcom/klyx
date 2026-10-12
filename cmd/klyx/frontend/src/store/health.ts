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
export type RowsDTO<T = unknown> = { cluster: string; incarnation: number; task: string; part: HealthPart; evaluatedAt: string; rows: T[] | null; facts?: Record<string, unknown>; emittedAtMs?: number };

// A4: how long a summary may be ahead of the on-screen rows before the
// counts switch to the summary and the rows are qualified (spec §4).
export const MISMATCH_DEADLINE_MS = 1000;
// Retries (a failed open, a failed or still-behind re-bootstrap): bounded
// exponential backoff while unresolved.
export const RETRY_MIN_MS = 1000;
export const RETRY_MAX_MS = 30000;
export const backoff = (attempt: number) => Math.min(RETRY_MAX_MS, RETRY_MIN_MS * 2 ** Math.max(0, attempt - 1));

// A subscription is one mounted consumer of one part, keyed by a stable
// handle; its backend token changes on every (re)open (design §2b).
export type RowsSub = {
  handle: string; cluster: string; task: string; part: string;
  token: string | null; // the backend token currently open (null between opens)
  boundIncarnation: number | null; // the Observer incarnation the token is bound to
  open: "opening" | "open" | "failed";
  openAttempt: number;
  openRetryAt: number | null; // a failed open's next attempt (ms)
  incarnation: number | null; // of the rows held
  ver: number; // -1 before any rows
  partData: HealthPart | null;
  rows: unknown[] | null;
  facts: Record<string, unknown> | null; // what the rows' evaluation used (§2a), from their capture
  evaluatedAt: string | null;
  mismatchSince: number | null; // when the summary first got ahead (ms); fixed until resolved
  refreshing: boolean; // past the deadline: summary counts, rows "as of … - refreshing"
  requestGen: number; // the fenced re-bootstrap generation
  inflight: boolean; // a re-bootstrap is outstanding
  attempt: number; // re-bootstrap attempts for this mismatch
  retryAt: number | null; // the next re-bootstrap attempt (ms)
  error: string | null; // the open's failure (the subscription has no rows of its own)
  refreshError: string | null; // the last failed re-bootstrap, while the mismatch is unresolved
};

type HealthState = {
  summaries: Record<string, HealthSummary>;
  finalizedThrough: Record<string, number>;
  // knownIncarnation is the newest Observer incarnation seen for a cluster,
  // from either channel; anything older is fenced out.
  knownIncarnation: Record<string, number>;
  subs: Record<string, RowsSub>;
};

export const useHealth = create<HealthState>(() => ({ summaries: {}, finalizedThrough: {}, knownIncarnation: {}, subs: {} }));

export function resetHealthStore(): void {
  useHealth.setState({ summaries: {}, finalizedThrough: {}, knownIncarnation: {}, subs: {} });
}

export function findPart(s: HealthSummary | undefined, task: string, part: string): HealthPart | undefined {
  return s?.tasks.find((t) => t.id === task)?.parts.find((p) => p.id === part);
}

const cleared = (sub: RowsSub): RowsSub => ({
  ...sub, incarnation: null, ver: -1, partData: null, rows: null, facts: null, evaluatedAt: null,
  mismatchSince: null, refreshing: false, inflight: false, attempt: 0, retryAt: null, refreshError: null,
});

// newer compares (incarnation, ver) as one key.
const newer = (inc: number, ver: number, thanInc: number, thanVer: number) => inc > thanInc || (inc === thanInc && ver > thanVer);

// reconcile recomputes one subscription's A4 state against the summary.
function reconcile(sub: RowsSub, summary: HealthSummary | undefined, now: number): RowsSub {
  const sp = summary && sub.incarnation !== null && summary.incarnation === sub.incarnation ? findPart(summary, sub.task, sub.part) : undefined;
  if (!sp || sp.ver <= sub.ver) {
    if (sub.mismatchSince === null && !sub.refreshing && !sub.inflight && sub.retryAt === null && sub.refreshError === null) return sub;
    return { ...sub, mismatchSince: null, refreshing: false, inflight: false, attempt: 0, retryAt: null, refreshError: null };
  }
  // The deadline is fixed at the FIRST unresolved mismatch; later ticks never reset it.
  return sub.mismatchSince === null ? { ...sub, mismatchSince: now } : sub;
}

// advance raises a cluster's known incarnation and clears every
// subscription's rows from an older one (their counts must not stand).
// It also drops an older stored summary: no part may fall back to the
// previous incarnation's counts as current, from whichever channel the
// advance came (Codex infra re-review #1).
function advance(st: HealthState, cluster: string, incarnation: number): HealthState {
  if (incarnation <= (st.knownIncarnation[cluster] ?? 0)) return st;
  const subs = { ...st.subs };
  for (const [h, sub] of Object.entries(subs)) {
    if (sub.cluster === cluster && sub.incarnation !== null && sub.incarnation < incarnation) subs[h] = cleared(sub);
  }
  let summaries = st.summaries;
  if (summaries[cluster] && summaries[cluster].incarnation < incarnation) {
    summaries = { ...summaries };
    delete summaries[cluster];
  }
  return { ...st, subs, summaries, knownIncarnation: { ...st.knownIncarnation, [cluster]: incarnation } };
}

// currentSummary is a cluster's summary only if it belongs to the known
// incarnation - every summary fallback goes through it.
function currentSummary(st: HealthState, cluster: string): HealthSummary | undefined {
  const s = st.summaries[cluster];
  return s && s.incarnation >= (st.knownIncarnation[cluster] ?? 0) ? s : undefined;
}

// applySummary keeps only a strictly newer (incarnation, seq), never one at
// or below a finalized incarnation. A Final clears the cluster's rows of
// that incarnation and fences everything at or below it.
export function applySummary(s: HealthSummary, now = Date.now()): void {
  useHealth.setState((st0) => {
    const cur = st0.summaries[s.cluster];
    if (s.incarnation <= (st0.finalizedThrough[s.cluster] ?? 0)) return st0;
    if (s.incarnation < (st0.knownIncarnation[s.cluster] ?? 0)) return st0;
    if (cur && !newer(s.incarnation, s.seq, cur.incarnation, cur.seq)) return st0;
    const st = advance(st0, s.cluster, s.incarnation);
    if (s.final) {
      const summaries = { ...st.summaries };
      delete summaries[s.cluster];
      const subs = { ...st.subs };
      for (const [h, sub] of Object.entries(subs)) {
        if (sub.cluster === s.cluster && (sub.incarnation === null || sub.incarnation <= s.incarnation)) subs[h] = cleared(sub);
      }
      return { ...st, summaries, subs, finalizedThrough: { ...st.finalizedThrough, [s.cluster]: s.incarnation } };
    }
    const summaries = { ...st.summaries, [s.cluster]: s };
    const subs = { ...st.subs };
    for (const [h, sub] of Object.entries(subs)) if (sub.cluster === s.cluster) subs[h] = reconcile(sub, s, now);
    return { ...st, summaries, subs };
  });
}

export function addSub(handle: string, cluster: string, task: string, part: string): void {
  useHealth.setState((st) => ({
    subs: {
      ...st.subs,
      [handle]: {
        handle, cluster, task, part, token: null, boundIncarnation: null, open: "opening", openAttempt: 0, openRetryAt: null,
        incarnation: null, ver: -1, partData: null, rows: null, facts: null, evaluatedAt: null,
        mismatchSince: null, refreshing: false, requestGen: 0, inflight: false, attempt: 0, retryAt: null, error: null, refreshError: null,
      },
    },
  }));
}

export function removeSub(handle: string): void {
  useHealth.setState((st) => {
    if (!st.subs[handle]) return st;
    const subs = { ...st.subs };
    delete subs[handle];
    return { subs };
  });
}

const patch = (handle: string, f: (sub: RowsSub) => RowsSub) =>
  useHealth.setState((st) => (st.subs[handle] ? { subs: { ...st.subs, [handle]: f(st.subs[handle]) } } : st));

// openStarted records the token of a new (re)open.
export function openStarted(handle: string, token: string): void {
  patch(handle, (s) => ({ ...s, token, open: "opening" }));
}

// openSucceeded binds the token to the incarnation its bootstrap came from.
export function openSucceeded(handle: string, token: string, incarnation: number): boolean {
  const sub = useHealth.getState().subs[handle];
  if (!sub || sub.token !== token) return false;
  patch(handle, (s) => ({ ...s, open: "open", boundIncarnation: incarnation, openAttempt: 0, openRetryAt: null, error: null }));
  return true;
}

// openFailed keeps the error visible and schedules a bounded retry.
export function openFailed(handle: string, token: string, error: string, now = Date.now()): boolean {
  const sub = useHealth.getState().subs[handle];
  if (!sub || sub.token !== token) return false;
  patch(handle, (s) => ({ ...s, open: "failed", token: null, error, openAttempt: s.openAttempt + 1, openRetryAt: now + backoff(s.openAttempt + 1) }));
  return true;
}

// applyRows takes a rows payload for one subscription: only its own part,
// never a finalized or superseded incarnation, and only a strictly newer
// (incarnation, Ver). A newer incarnation advances the cluster's.
export function applyRows(handle: string, dto: RowsDTO, now = Date.now()): boolean {
  let applied = false;
  useHealth.setState((st0) => {
    const sub0 = st0.subs[handle];
    if (!sub0 || dto.cluster !== sub0.cluster || dto.task !== sub0.task || dto.part.id !== sub0.part) return st0;
    if (dto.incarnation <= (st0.finalizedThrough[sub0.cluster] ?? 0)) return st0;
    if (dto.incarnation < (st0.knownIncarnation[sub0.cluster] ?? 0)) return st0;
    const st = advance(st0, sub0.cluster, dto.incarnation);
    const sub = st.subs[handle];
    if (sub.incarnation !== null && !newer(dto.incarnation, dto.part.ver, sub.incarnation, sub.ver)) return st;
    applied = true;
    const base = sub.incarnation !== null && dto.incarnation > sub.incarnation ? cleared(sub) : sub;
    const next: RowsSub = { ...base, incarnation: dto.incarnation, ver: dto.part.ver, partData: dto.part, rows: dto.rows ?? [], facts: dto.facts ?? null, evaluatedAt: dto.evaluatedAt };
    return { ...st, subs: { ...st.subs, [handle]: reconcile(next, st.summaries[sub.cluster], now) } };
  });
  return applied;
}

// dueForBootstrap: a subscription past its A4 deadline turns refreshing
// (the deadline stays fixed); while unresolved and not in flight, it is due
// for a fenced re-bootstrap at its retry time.
export function dueForBootstrap(now = Date.now()): { handle: string; token: string; requestGen: number }[] {
  const due: { handle: string; token: string; requestGen: number }[] = [];
  useHealth.setState((st) => {
    let subs: Record<string, RowsSub> | null = null;
    for (const [h, sub] of Object.entries(st.subs)) {
      if (sub.mismatchSince === null || now - sub.mismatchSince < MISMATCH_DEADLINE_MS) continue;
      if (sub.inflight || !sub.token || (sub.retryAt !== null && now < sub.retryAt)) {
        if (!sub.refreshing) {
          subs ??= { ...st.subs };
          subs[h] = { ...sub, refreshing: true };
        }
        continue;
      }
      subs ??= { ...st.subs };
      subs[h] = { ...sub, refreshing: true, inflight: true, requestGen: sub.requestGen + 1 };
      due.push({ handle: h, token: sub.token, requestGen: sub.requestGen + 1 });
    }
    return subs ? { subs } : st;
  });
  return due;
}

// bootstrapSettled records one re-bootstrap's outcome: a reply for an old
// request generation is ignored; a resolved mismatch clears the retry
// state; an error, a rejection or a reply still behind schedules the next
// attempt with backoff (the counts stay on the summary meanwhile).
export function bootstrapSettled(handle: string, requestGen: number, outcome: { rows?: RowsDTO | null; error?: string | null; closed?: boolean }, now = Date.now()): void {
  const sub = useHealth.getState().subs[handle];
  if (!sub || requestGen !== sub.requestGen || !sub.inflight) return;
  if (outcome.rows && !outcome.closed) applyRows(handle, outcome.rows, now);
  patch(handle, (s) => {
    if (s.mismatchSince === null) return { ...s, inflight: false, attempt: 0, retryAt: null, refreshError: null };
    const attempt = s.attempt + 1;
    return { ...s, inflight: false, attempt, retryAt: now + backoff(attempt), refreshError: outcome.error ?? s.refreshError };
  });
}

// nextWake is when the transport must look again: the earliest A4
// deadline, re-bootstrap retry or failed-open retry.
// Only actionable times count: a deadline not yet consumed, and a
// re-bootstrap retry that has a token to use. A refreshing subscription
// without a token (its open failed) waits for its open retry instead -
// never an expired deadline re-armed at zero delay (Codex re-review #2).
export function nextWake(): number | null {
  let at: number | null = null;
  const min = (t: number) => { at = at === null ? t : Math.min(at, t); };
  for (const s of Object.values(useHealth.getState().subs)) {
    if (s.mismatchSince !== null && !s.inflight) {
      if (!s.refreshing) min(s.mismatchSince + MISMATCH_DEADLINE_MS);
      else if (s.token && s.retryAt !== null) min(s.retryAt);
    }
    if (s.open === "failed" && s.openRetryAt !== null) min(s.openRetryAt);
  }
  return at;
}

// needsReopen: subscriptions whose token is bound to an older incarnation
// than the cluster's (the Observer was replaced), or whose failed open is
// due for its retry.
export function needsReopen(now = Date.now()): string[] {
  const st = useHealth.getState();
  const out: string[] = [];
  for (const s of Object.values(st.subs)) {
    const known = st.knownIncarnation[s.cluster] ?? 0;
    if (s.open === "open" && s.boundIncarnation !== null && s.boundIncarnation < known) out.push(s.handle);
    else if (s.open === "failed" && s.openRetryAt !== null && now >= s.openRetryAt) out.push(s.handle);
  }
  return out;
}

export type SelectedCounts = {
  part: HealthPart | null; // whose counts to show
  source: "rows" | "summary" | "none";
  updating: boolean; // summary ahead, inside the deadline: rows' counts, marked
  refreshing: boolean; // past the deadline: the summary's counts; rows qualified as of their evaluation
};

// bestSub picks, among a part's consumers, the newest rows of the cluster's
// current incarnation - comparing (incarnation, Ver) as one key.
export function bestSub(st: HealthState, cluster: string, task: string, part: string): RowsSub | null {
  const known = st.knownIncarnation[cluster] ?? 0;
  let best: RowsSub | null = null;
  for (const s of Object.values(st.subs)) {
    if (s.cluster !== cluster || s.task !== task || s.part !== part || !s.partData || s.incarnation === null || s.incarnation < known) continue;
    if (!best || newer(s.incarnation, s.ver, best.incarnation as number, best.ver)) best = s;
  }
  return best;
}

// taskCounts is the ONE count selector (design §4a) for the lens header,
// the nav tree and the mast: a part whose rows are on screen shows its
// rows' counts until A4 switches every surface to the summary together;
// any other part shows the summary's.
export function taskCounts(st: HealthState, cluster: string, task: string, part: string): SelectedCounts {
  const summaryPart = findPart(currentSummary(st, cluster), task, part) ?? null;
  const best = bestSub(st, cluster, task, part);
  if (!best) return { part: summaryPart, source: summaryPart ? "summary" : "none", updating: false, refreshing: false };
  if (best.refreshing && summaryPart) return { part: summaryPart, source: "summary", updating: false, refreshing: true };
  return { part: best.partData, source: "rows", updating: best.mismatchSince !== null, refreshing: false };
}
