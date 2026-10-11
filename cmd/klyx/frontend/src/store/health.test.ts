import { describe, it, expect, beforeEach } from "vitest";
import {
  useHealth, resetHealthStore, applySummary, applyRows, addSub, removeSub, openStarted, openSucceeded, openFailed,
  dueForBootstrap, bootstrapSettled, needsReopen, nextWake, taskCounts, backoff, MISMATCH_DEADLINE_MS,
  type HealthSummary, type HealthPart, type RowsDTO,
} from "./health";

// 1c spec §4 and slice 3 design §2b, §4a: the store decides what is current.

const part = (ver: number, included = 1): HealthPart => ({
  id: "certificates", entity: "Certificates", scope: "all namespaces", classifier: "native", coverage: "live", kinds: [],
  native: { total: 3, included, word: "not ready", roles: { total: 3, fault: included, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 3 - included } },
  obs: ver, ver,
});
const summary = (incarnation: number, seq: number, ver: number, extra: Partial<HealthSummary> = {}): HealthSummary => ({
  cluster: "c1", incarnation, seq, evaluatedAt: "2026-10-11T12:00:00Z", connection: "connected",
  tasks: [{ id: "certs", coverage: "live", parts: [part(ver, ver)] }], ...extra,
});
const rows = (incarnation: number, ver: number, n = 3): RowsDTO => ({
  cluster: "c1", incarnation, task: "certs", part: part(ver, ver), evaluatedAt: "2026-10-11T12:00:00Z", rows: Array.from({ length: n }, (_, i) => ({ uid: `u${i}` })),
});
const sub = (h = "t") => useHealth.getState().subs[h];
const counts = () => taskCounts(useHealth.getState(), "c1", "certs", "certificates");
const open = (h: string, token: string, incarnation = 1) => {
  addSub(h, "c1", "certs", "certificates");
  openStarted(h, token);
  openSucceeded(h, token, incarnation);
};

beforeEach(() => {
  resetHealthStore();
  open("t", "tok-t");
});

describe("health store: one number per task (spec §4, A4)", () => {
  it("continuous churn: rows and summary at one Ver; the rows' counts, never updating", () => {
    for (let v = 1; v <= 5; v++) {
      applySummary(summary(1, v, v), 1000 * v);
      applyRows("t", rows(1, v), 1000 * v + 10);
      expect(counts()).toMatchObject({ source: "rows", updating: false, refreshing: false });
      expect(counts().part?.ver).toBe(v);
    }
  });

  it("rows newer than the summary win at once", () => {
    applySummary(summary(1, 1, 1));
    applyRows("t", rows(1, 3));
    expect(counts()).toMatchObject({ source: "rows", updating: false });
    expect(counts().part?.ver).toBe(3);
  });

  it("summary ahead: updating until the deadline, then the summary's counts and a fenced re-bootstrap", () => {
    applyRows("t", rows(1, 2), 0);
    applySummary(summary(1, 2, 4), 100); // the summary got ahead at t=100
    expect(counts()).toMatchObject({ source: "rows", updating: true, refreshing: false });
    applySummary(summary(1, 3, 5), 900); // a later tick never resets the deadline
    expect(sub().mismatchSince).toBe(100);
    expect(dueForBootstrap(100 + MISMATCH_DEADLINE_MS - 1)).toEqual([]);
    const due = dueForBootstrap(100 + MISMATCH_DEADLINE_MS);
    expect(due).toEqual([{ handle: "t", token: "tok-t", requestGen: 1 }]);
    expect(counts()).toMatchObject({ source: "summary", refreshing: true });
    expect(counts().part?.ver).toBe(5);
    expect(dueForBootstrap(100 + MISMATCH_DEADLINE_MS + 5)).toEqual([]); // in flight: never twice
    bootstrapSettled("t", 0, { rows: rows(1, 5) }); // an older request generation is fenced out
    expect(sub().ver).toBe(2);
    bootstrapSettled("t", 1, { rows: rows(1, 5) }, 2000); // the current one with a newer Ver resolves it
    expect(counts()).toMatchObject({ source: "rows", updating: false, refreshing: false });
    expect(sub()).toMatchObject({ inflight: false, attempt: 0, retryAt: null });
  });

  it("a time boundary with no data change: the summary's Ver moves, the rows follow at that Ver", () => {
    applyRows("t", rows(1, 2));
    applySummary(summary(1, 2, 3), 0); // reclassification at Next
    expect(counts().updating).toBe(true);
    applyRows("t", rows(1, 3), 50); // the re-pushed rows at the new Ver
    expect(counts()).toMatchObject({ source: "rows", updating: false });
  });

  it("a dropped rows event is recovered by the deadline's bootstrap; a delayed old one is ignored", () => {
    applyRows("t", rows(1, 2), 0);
    applySummary(summary(1, 2, 3), 0); // its rows event never arrives
    expect(dueForBootstrap(MISMATCH_DEADLINE_MS)).toHaveLength(1);
    bootstrapSettled("t", 1, { rows: rows(1, 3) }, MISMATCH_DEADLINE_MS + 5);
    expect(applyRows("t", rows(1, 2))).toBe(false); // delayed, older
    expect(counts().part?.ver).toBe(3);
  });

  it("a bootstrap reply racing a later event cannot win", () => {
    applyRows("t", rows(1, 2), 0);
    applySummary(summary(1, 2, 4), 0);
    const [{ requestGen }] = dueForBootstrap(MISMATCH_DEADLINE_MS);
    expect(applyRows("t", rows(1, 5))).toBe(true); // the event lands first and resolves
    bootstrapSettled("t", requestGen, { rows: rows(1, 4) });
    expect(counts().part?.ver).toBe(5);
  });

  // Codex infra review #4: recovery is retried with backoff while
  // unresolved, never a one-shot that leaves a quiet lens refreshing.
  it("a failed re-bootstrap retries with backoff, keeping the fixed deadline and the summary's counts", () => {
    applyRows("t", rows(1, 2), 0);
    applySummary(summary(1, 2, 4), 0);
    const [first] = dueForBootstrap(1000);
    bootstrapSettled("t", first.requestGen, { error: "capture timed out" }, 1100);
    expect(sub()).toMatchObject({ inflight: false, attempt: 1, retryAt: 1100 + backoff(1), mismatchSince: 0, refreshing: true, refreshError: "capture timed out" });
    expect(counts()).toMatchObject({ source: "summary", refreshing: true });
    expect(dueForBootstrap(1100 + backoff(1) - 1)).toEqual([]);
    expect(nextWake()).toBe(1100 + backoff(1));
    const [second] = dueForBootstrap(1100 + backoff(1));
    expect(second.requestGen).toBe(first.requestGen + 1);
    bootstrapSettled("t", second.requestGen, { rows: rows(1, 4) }, 5000);
    expect(sub()).toMatchObject({ refreshing: false, mismatchSince: null, attempt: 0, retryAt: null, refreshError: null });
  });

  it("a bootstrap that is still behind - later events dropped - retries again", () => {
    applyRows("t", rows(1, 2), 0);
    applySummary(summary(1, 2, 6), 0);
    const [a] = dueForBootstrap(1000);
    bootstrapSettled("t", a.requestGen, { rows: rows(1, 4) }, 1200); // newer, still behind
    expect(sub()).toMatchObject({ ver: 4, refreshing: true, attempt: 1, inflight: false });
    const [b] = dueForBootstrap(1200 + backoff(1));
    bootstrapSettled("t", b.requestGen, { rows: rows(1, 6) }, 4000);
    expect(sub()).toMatchObject({ ver: 6, refreshing: false, mismatchSince: null });
  });

  it("Final clears the rows and fences its incarnation; a newer incarnation resets", () => {
    applyRows("t", rows(1, 7));
    applySummary(summary(1, 9, 7));
    applySummary(summary(1, 10, 7, { final: true }));
    expect(sub().rows).toBeNull();
    expect(useHealth.getState().summaries.c1).toBeUndefined();
    expect(applyRows("t", rows(1, 8))).toBe(false); // the old incarnation, after Final
    applySummary(summary(1, 11, 8)); // an old summary after Final
    expect(useHealth.getState().summaries.c1).toBeUndefined();
    expect(applyRows("t", rows(2, 1))).toBe(true); // the replacement Observer
    expect(sub()).toMatchObject({ incarnation: 2, ver: 1, mismatchSince: null, refreshing: false });
  });

  // Codex infra review #1: a newer incarnation fences the old rows and
  // counts from either channel, and selection compares (incarnation, Ver).
  it("a new incarnation's summary before its rows clears the old rows; late old data is fenced", () => {
    applyRows("t", rows(1, 50));
    applySummary(summary(1, 50, 50));
    applySummary(summary(2, 1, 1)); // the replacement Observer's first summary
    expect(sub()).toMatchObject({ rows: null, incarnation: null, ver: -1 });
    expect(counts()).toMatchObject({ source: "summary" });
    expect(counts().part?.ver).toBe(1);
    expect(applyRows("t", rows(1, 51))).toBe(false); // a late old event or reply
    applySummary(summary(1, 60, 60, { final: true })); // a late old Final
    expect(useHealth.getState().summaries.c1.incarnation).toBe(2);
    expect(useHealth.getState().finalizedThrough.c1).toBeUndefined();
    expect(applyRows("t", rows(2, 1))).toBe(true);
    expect(counts()).toMatchObject({ source: "rows" });
  });

  it("two consumers across incarnations: the current incarnation's rows count, whatever their Ver", () => {
    open("u", "tok-u");
    applyRows("t", rows(1, 51));
    applyRows("u", rows(2, 1)); // the new Observer: advances the cluster, clears t
    expect(sub("t").rows).toBeNull();
    expect(counts().part?.ver).toBe(1);
    expect(applyRows("t", rows(1, 52))).toBe(false);
  });

  it("a newer incarnation's rows reset an open mismatch", () => {
    applyRows("t", rows(1, 2), 0);
    applySummary(summary(1, 2, 9), 0);
    expect(sub().mismatchSince).toBe(0);
    applyRows("t", rows(2, 1), 10);
    expect(sub()).toMatchObject({ incarnation: 2, mismatchSince: null });
  });

  // Codex infra review #2: a subscription bound to a replaced Observer, or
  // whose open failed, is due for a reopen.
  it("a replaced Observer or a failed open makes the subscription due for a reopen", () => {
    expect(needsReopen()).toEqual([]);
    applySummary(summary(2, 1, 1));
    expect(needsReopen()).toEqual(["t"]);
    openStarted("t", "tok-t2");
    openSucceeded("t", "tok-t2", 2);
    expect(needsReopen()).toEqual([]);
    open("f", "tok-f");
    openStarted("f", "tok-f2");
    expect(openFailed("f", "tok-old", "late", 0)).toBe(false); // a stale token's failure is ignored
    openFailed("f", "tok-f2", "cluster not observed", 0);
    expect(sub("f")).toMatchObject({ open: "failed", token: null, error: "cluster not observed", openAttempt: 1, openRetryAt: backoff(1) });
    expect(needsReopen(backoff(1) - 1)).toEqual([]);
    expect(needsReopen(backoff(1))).toEqual(["f"]);
  });

  it("rows only for their own part, and nothing for a removed subscription", () => {
    expect(applyRows("t", { ...rows(1, 1), task: "eso" })).toBe(false);
    removeSub("t");
    expect(applyRows("t", rows(1, 1))).toBe(false);
  });

  it("without rows on screen a part shows the summary's counts", () => {
    removeSub("t");
    applySummary(summary(1, 1, 3));
    expect(counts()).toMatchObject({ source: "summary", updating: false });
    expect(counts().part?.ver).toBe(3);
  });
});
