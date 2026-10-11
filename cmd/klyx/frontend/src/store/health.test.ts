import { describe, it, expect, beforeEach } from "vitest";
import {
  useHealth, applySummary, applyRows, addSub, removeSub, dueForBootstrap, applyBootstrap, taskCounts, MISMATCH_DEADLINE_MS,
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
const sub = (token = "t") => useHealth.getState().subs[token];
const counts = () => taskCounts(useHealth.getState(), "c1", "certs", "certificates");

beforeEach(() => {
  useHealth.setState({ summaries: {}, finalizedThrough: {}, subs: {} });
  addSub("t", "c1", "certs", "certificates");
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
    expect(due).toEqual([{ token: "t", requestGen: 1 }]);
    expect(counts()).toMatchObject({ source: "summary", refreshing: true });
    expect(counts().part?.ver).toBe(5);
    // a reply for an older request generation is fenced out
    expect(applyBootstrap("t", { rows: rows(1, 5), requestGen: 0 })).toBe(false);
    // the current generation with a newer Ver resolves it
    expect(applyBootstrap("t", { rows: rows(1, 5), requestGen: 1 })).toBe(true);
    expect(counts()).toMatchObject({ source: "rows", updating: false, refreshing: false });
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
    expect(applyBootstrap("t", { rows: rows(1, 3), requestGen: 1 })).toBe(true);
    expect(applyRows("t", rows(1, 2))).toBe(false); // delayed, older
    expect(counts().part?.ver).toBe(3);
  });

  it("a bootstrap reply racing a later event cannot win", () => {
    applyRows("t", rows(1, 2), 0);
    applySummary(summary(1, 2, 4), 0);
    const [{ requestGen }] = dueForBootstrap(MISMATCH_DEADLINE_MS);
    expect(applyRows("t", rows(1, 5))).toBe(true); // the event lands first
    expect(applyBootstrap("t", { rows: rows(1, 4), requestGen })).toBe(false); // older Ver
    expect(counts().part?.ver).toBe(5);
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

  it("a newer incarnation's rows reset an open mismatch", () => {
    applyRows("t", rows(1, 2), 0);
    applySummary(summary(1, 2, 9), 0);
    expect(sub().mismatchSince).toBe(0);
    applyRows("t", rows(2, 1), 10);
    expect(sub()).toMatchObject({ incarnation: 2, mismatchSince: null });
  });

  it("rows only for their own part, and nothing for a removed subscription", () => {
    expect(applyRows("t", { ...rows(1, 1), task: "eso" })).toBe(false);
    removeSub("t");
    expect(applyRows("t", rows(1, 1))).toBe(false);
    expect(applyBootstrap("t", { rows: rows(1, 1), requestGen: 0 })).toBe(false);
  });

  it("two consumers of one part: the newest rows count", () => {
    addSub("u", "c1", "certs", "certificates");
    applyRows("t", rows(1, 2));
    applyRows("u", rows(1, 4));
    expect(counts().part?.ver).toBe(4);
    removeSub("u");
    expect(counts().part?.ver).toBe(2);
  });

  it("without rows on screen a part shows the summary's counts", () => {
    removeSub("t");
    applySummary(summary(1, 1, 3));
    expect(counts()).toMatchObject({ source: "summary", updating: false });
    expect(counts().part?.ver).toBe(3);
  });
});
