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
// since and reason are The Line §8.1's rule-backed start time (RFC 3339) and
// the rule's capped reason; absent when no rule backs them.
export type HealthRef = { cluster: string; gvr: string; namespace?: string; name: string; uid?: string; parent?: string; role: string; label: string; generation?: number; route?: string; since?: string; condition?: { type: string; status: string; since: string }; reason?: string };
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
// A rows message (slice 3 addendum §3.3): full (rows is the complete set),
// or a delta against base - the Ver it applies to - carrying the rows whose
// sent form changed (upserts, by UID) and the UIDs gone (removed). Part,
// facts and evaluatedAt travel on both. A message without full: false is
// full (bootstrap replies and native parts).
export type RowsDTO<T = unknown> = {
  cluster: string; incarnation: number; task: string; part: HealthPart; evaluatedAt: string; rows: T[] | null;
  facts?: Record<string, unknown>; emittedAtMs?: number;
  full?: boolean; base?: number; upserts?: T[] | null; removed?: string[] | null;
};

// PENDING_MAX bounds the deltas held while they cannot apply (no rows yet,
// or a wrong base awaiting the full bootstrap).
export const PENDING_MAX = 16;

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
  // deltas newer than the rows held that cannot apply yet, oldest first
  pending: RowsDTO[];
  // resync: a newer delta of the held incarnation did not chain from the
  // held Ver - a fenced full bootstrap is due, backed off, whether or not a
  // newer summary arrived (addendum §3.3)
  resync: boolean;
};

export type HealthState = {
  summaries: Record<string, HealthSummary>;
  finalizedThrough: Record<string, number>;
  // ended: a cluster's observation ended (its Final) - fenced to that
  // incarnation and cleared by a newer one (slice 4 §1).
  ended: Record<string, { incarnation: number; at: string }>;
  // knownIncarnation is the newest Observer incarnation seen for a cluster,
  // from either channel; anything older is fenced out.
  knownIncarnation: Record<string, number>;
  subs: Record<string, RowsSub>;
};

export const useHealth = create<HealthState>(() => ({ summaries: {}, finalizedThrough: {}, ended: {}, knownIncarnation: {}, subs: {} }));

export function resetHealthStore(): void {
  useHealth.setState({ summaries: {}, finalizedThrough: {}, ended: {}, knownIncarnation: {}, subs: {} });
}

export function findPart(s: HealthSummary | undefined, task: string, part: string): HealthPart | undefined {
  return s?.tasks.find((t) => t.id === task)?.parts.find((p) => p.id === part);
}

const cleared = (sub: RowsSub): RowsSub => ({
  ...sub, incarnation: null, ver: -1, partData: null, rows: null, facts: null, evaluatedAt: null,
  mismatchSince: null, refreshing: false, inflight: false, attempt: 0, retryAt: null, refreshError: null,
  pending: [], resync: false, // a delta that arrives later is held afresh
});

// newer compares (incarnation, ver) as one key.
const newer = (inc: number, ver: number, thanInc: number, thanVer: number) => inc > thanInc || (inc === thanInc && ver > thanVer);

// reconcile recomputes one subscription's A4 state against the summary.
function reconcile(sub: RowsSub, summary: HealthSummary | undefined, now: number): RowsSub {
  const sp = summary && sub.incarnation !== null && summary.incarnation === sub.incarnation ? findPart(summary, sub.task, sub.part) : undefined;
  if (!sp || sp.ver <= sub.ver) {
    if (sub.mismatchSince === null && !sub.refreshing && !sub.inflight && sub.retryAt === null && sub.refreshError === null) return sub;
    // a resync keeps its bootstrap in flight and its backoff: a summary
    // that is not ahead resolves the mismatch, never the wrong base
    if (sub.resync) return sub.mismatchSince === null && !sub.refreshing ? sub : { ...sub, mismatchSince: null, refreshing: false };
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
  let ended = st.ended;
  if (ended[cluster] && ended[cluster].incarnation < incarnation) {
    ended = { ...ended };
    delete ended[cluster];
  }
  return { ...st, subs, summaries, ended, knownIncarnation: { ...st.knownIncarnation, [cluster]: incarnation } };
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
      return { ...st, summaries, subs, finalizedThrough: { ...st.finalizedThrough, [s.cluster]: s.incarnation },
        ended: { ...st.ended, [s.cluster]: { incarnation: s.incarnation, at: s.evaluatedAt } } };
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
        pending: [], resync: false,
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

const uidOf = (r: unknown) => (r as { uid?: string } | null)?.uid ?? "";

// withMessage moves a subscription to a full message, or a delta whose base
// is the Ver held: rows upserted and removed by UID (a kept row keeps its
// object), the part, facts and evaluation time replaced.
function withMessage(sub: RowsSub, dto: RowsDTO): RowsSub {
  let rows: unknown[];
  if (dto.full === false && !dto.upserts?.length && !dto.removed?.length) rows = sub.rows ?? []; // metadata only: the rows keep their identity
  else if (dto.full === false) {
    const gone = new Set(dto.removed ?? []);
    const up = new Map<string, unknown>();
    for (const r of dto.upserts ?? []) up.set(uidOf(r), r);
    rows = [];
    for (const r of sub.rows ?? []) {
      const u = uidOf(r);
      if (gone.has(u)) continue;
      const n = up.get(u);
      if (n !== undefined) { rows.push(n); up.delete(u); } else rows.push(r);
    }
    for (const r of up.values()) rows.push(r);
  } else rows = dto.rows ?? [];
  return { ...sub, incarnation: dto.incarnation, ver: dto.part.ver, partData: dto.part, rows, facts: dto.facts ?? null, evaluatedAt: dto.evaluatedAt };
}

const chains = (sub: RowsSub, d: RowsDTO) => d.full !== false || (sub.incarnation === d.incarnation && d.base === sub.ver);

// drain applies the held deltas that now chain, in order, and keeps only
// those still newer than the rows. One left over in the held incarnation
// cannot chain until a full arrives: a resync.
function drain(sub: RowsSub): RowsSub {
  let s = sub;
  for (;;) {
    const cur = s;
    const d = cur.pending.find((x) => chains(cur, x));
    if (!d) break;
    s = { ...withMessage(cur, d), pending: cur.pending.filter((x) => x !== d) };
  }
  const pending = s.incarnation === null ? s.pending : s.pending.filter((d) => newer(d.incarnation, d.part.ver, s.incarnation as number, s.ver));
  return { ...s, pending, resync: s.incarnation !== null && pending.some((d) => d.incarnation === s.incarnation) };
}

export type RowsOutcome = "applied" | "held" | "ignored";

// applyRows takes a rows message for one subscription: only its own part,
// never a finalized or superseded incarnation, and only a strictly newer
// (incarnation, Ver) - an older or equal one is ignored, with no retry. A
// newer incarnation advances the cluster's. A full message, or a delta
// whose base is the Ver held, applies; any other newer delta leaves the
// rows and their metadata unchanged and is held - in the held incarnation
// it requests a full bootstrap (resync). Held deltas apply once they chain.
export function applyRows(handle: string, dto: RowsDTO, now = Date.now()): RowsOutcome {
  let outcome: RowsOutcome = "ignored";
  useHealth.setState((st0) => {
    const sub0 = st0.subs[handle];
    if (!sub0 || dto.cluster !== sub0.cluster || dto.task !== sub0.task || dto.part.id !== sub0.part) return st0;
    if (dto.incarnation <= (st0.finalizedThrough[sub0.cluster] ?? 0)) return st0;
    if (dto.incarnation < (st0.knownIncarnation[sub0.cluster] ?? 0)) return st0;
    const st = advance(st0, sub0.cluster, dto.incarnation);
    const sub = st.subs[handle];
    if (sub.incarnation !== null && !newer(dto.incarnation, dto.part.ver, sub.incarnation, sub.ver)) return st;
    let next: RowsSub;
    if (chains(sub, dto)) {
      outcome = "applied";
      const base = sub.incarnation !== null && dto.incarnation > sub.incarnation ? cleared(sub) : sub;
      next = drain(withMessage(base, dto));
    } else {
      outcome = "held";
      next = drain({ ...sub, pending: [...sub.pending, dto].slice(-PENDING_MAX) });
    }
    return { ...st, subs: { ...st.subs, [handle]: reconcile(next, st.summaries[sub.cluster], now) } };
  });
  return outcome;
}

// dueForBootstrap: a subscription past its A4 deadline turns refreshing
// (the deadline stays fixed); while unresolved and not in flight, it is due
// for a fenced re-bootstrap at its retry time. A resync is due at once (and
// then at its retry time) without waiting for a summary or the deadline; it
// does not by itself switch the counts to the summary.
export function dueForBootstrap(now = Date.now()): { handle: string; token: string; requestGen: number }[] {
  const due: { handle: string; token: string; requestGen: number }[] = [];
  useHealth.setState((st) => {
    let subs: Record<string, RowsSub> | null = null;
    for (const [h, sub] of Object.entries(st.subs)) {
      const late = sub.mismatchSince !== null && now - sub.mismatchSince >= MISMATCH_DEADLINE_MS;
      if (!late && !sub.resync) continue;
      if (sub.inflight || !sub.token || (sub.retryAt !== null && now < sub.retryAt)) {
        if (late && !sub.refreshing) {
          subs ??= { ...st.subs };
          subs[h] = { ...sub, refreshing: true };
        }
        continue;
      }
      subs ??= { ...st.subs };
      subs[h] = { ...sub, refreshing: sub.refreshing || late, inflight: true, requestGen: sub.requestGen + 1 };
      due.push({ handle: h, token: sub.token, requestGen: sub.requestGen + 1 });
    }
    return subs ? { subs } : st;
  });
  return due;
}

// bootstrapSettled records one re-bootstrap's outcome: a reply for an old
// request generation is ignored; a resolved mismatch and resync clear the
// retry state; an error, a rejection or a reply still behind (or one the
// held deltas still do not chain from) schedules the next attempt with
// backoff (A4's counts stay on the summary meanwhile).
export function bootstrapSettled(handle: string, requestGen: number, outcome: { rows?: RowsDTO | null; error?: string | null; closed?: boolean }, now = Date.now()): void {
  const sub = useHealth.getState().subs[handle];
  if (!sub || requestGen !== sub.requestGen || !sub.inflight) return;
  if (outcome.rows && !outcome.closed) applyRows(handle, outcome.rows, now);
  patch(handle, (s) => {
    if (s.mismatchSince === null && !s.resync) return { ...s, inflight: false, attempt: 0, retryAt: null, refreshError: null };
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
    if (s.resync && !s.inflight && s.token) min(s.retryAt ?? 0);
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
  updating: boolean; // summary ahead inside the deadline, or a resync: rows' counts, marked
  refreshing: boolean; // past the deadline: the summary's counts; rows qualified as of their evaluation
  // the selected part's own evaluation time: the rows' or the summary's,
  // never one for the other's counts (slice 4 §3)
  at: string | null;
  resync: boolean; // the rows wait for a full re-read after a wrong base
  refreshError: string | null; // the last failed re-bootstrap
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
  const summary = currentSummary(st, cluster);
  const summaryPart = findPart(summary, task, part) ?? null;
  const summaryAt = summaryPart && summary ? summary.evaluatedAt : null;
  const best = bestSub(st, cluster, task, part);
  if (!best) return { part: summaryPart, source: summaryPart ? "summary" : "none", updating: false, refreshing: false, at: summaryAt, resync: false, refreshError: null };
  const qual = { resync: best.resync, refreshError: best.refreshError };
  if (best.refreshing && summaryPart) return { part: summaryPart, source: "summary", updating: false, refreshing: true, at: summaryAt, ...qual };
  // a resync's rows are known to be behind a newer Ver: marked, as updating
  return { part: best.partData, source: "rows", updating: best.mismatchSince !== null || best.resync, refreshing: false, at: best.evaluatedAt, ...qual };
}
