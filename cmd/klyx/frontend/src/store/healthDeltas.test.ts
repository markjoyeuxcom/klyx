import { describe, it, expect, beforeEach } from "vitest";
import {
  useHealth, resetHealthStore, applySummary, applyRows, addSub, openStarted, openSucceeded,
  dueForBootstrap, bootstrapSettled, nextWake, taskCounts, backoff, PENDING_MAX,
  type HealthPart, type HealthSummary, type RowsDTO,
} from "./health";

// Rows deltas (1c slice 3 addendum §3.3): a delta applies only on its base;
// a newer one with a wrong base leaves rows and metadata unchanged and asks,
// on its own, for one fenced, backed-off full bootstrap.

type Row = { uid: string; v: number };
const part = (ver: number, coverage: HealthPart["coverage"] = "live"): HealthPart => ({
  id: "pods", entity: "pods", scope: "all namespaces", classifier: "verdict", coverage, kinds: [],
  counts: { total: 0, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 }, obs: ver, ver,
});
const full = (ver: number, rows: Row[], inc = 1, extra: Partial<RowsDTO<Row>> = {}): RowsDTO =>
  ({ cluster: "c1", incarnation: inc, task: "pods", part: part(ver), evaluatedAt: `t${ver}`, rows, full: true, ...extra });
const delta = (base: number, ver: number, upserts: Row[], removed: string[] = [], inc = 1, extra: Partial<RowsDTO<Row>> = {}): RowsDTO =>
  ({ cluster: "c1", incarnation: inc, task: "pods", part: part(ver), evaluatedAt: `t${ver}`, rows: null, full: false, base, upserts, removed, ...extra });
const summary = (ver: number, seq = ver): HealthSummary => ({
  cluster: "c1", incarnation: 1, seq, evaluatedAt: "x", connection: "connected", tasks: [{ id: "pods", coverage: "live", parts: [part(ver)] }],
});
const sub = (h = "a") => useHealth.getState().subs[h];
// the rows held by UID - each UID exactly once
const held = (h = "a") => {
  const rows = (sub(h).rows ?? []) as Row[];
  expect(new Set(rows.map((r) => r.uid)).size).toBe(rows.length);
  return Object.fromEntries(rows.map((r) => [r.uid, r.v]));
};
const open = (h: string, inc = 1) => { addSub(h, "c1", "pods", "pods"); openStarted(h, `tok-${h}`); openSucceeded(h, `tok-${h}`, inc); };

beforeEach(() => {
  resetHealthStore();
  open("a");
});

describe("rows deltas (addendum §3.3)", () => {
  it("a delta applies on its base: upserts and removals by UID, the metadata replaced", () => {
    applyRows("a", full(3, [{ uid: "x", v: 1 }, { uid: "y", v: 1 }]));
    const keep = (sub().rows as Row[])[1];
    expect(applyRows("a", delta(3, 4, [{ uid: "x", v: 2 }, { uid: "z", v: 1 }], [], 1, { facts: { fluxPresent: true } }))).toBe("applied");
    expect(held()).toEqual({ x: 2, y: 1, z: 1 });
    expect((sub().rows as Row[]).find((r) => r.uid === "y")).toBe(keep); // an unchanged row keeps its object
    expect(sub()).toMatchObject({ ver: 4, evaluatedAt: "t4", facts: { fluxPresent: true }, resync: false });
    applyRows("a", delta(4, 5, [], ["x"]));
    expect(held()).toEqual({ y: 1, z: 1 });
  });

  it("an alert row is keyed by its opaque key (coverage S8a, Codex review of 71c181c)", () => {
    type Alert = { key: string; v: number };
    applyRows("a", full(3, [{ key: "A/fp", v: 1 }, { key: "B/fp", v: 1 }] as unknown as Row[]));
    applyRows("a", delta(3, 4, [{ key: "B/fp", v: 2 }] as unknown as Row[], ["A/fp"]));
    expect((sub().rows as unknown as Alert[]).map((r) => `${r.key}:${r.v}`)).toEqual(["B/fp:2"]);
    const kept = (sub().rows as unknown as Alert[])[0];
    applyRows("a", delta(4, 5, []));
    expect((sub().rows as unknown as Alert[])[0]).toBe(kept); // an unchanged read sends no rows
  });

  it("an empty delta carries a fact-, freshness- or coverage-only change; the rows keep their identity", () => {
    applyRows("a", full(1, [{ uid: "x", v: 1 }]));
    const rows = sub().rows;
    applyRows("a", { ...delta(1, 2, []), part: part(2, "stale"), facts: { fluxPresent: false } });
    expect(sub()).toMatchObject({ ver: 2, evaluatedAt: "t2", facts: { fluxPresent: false } });
    expect(sub().partData?.coverage).toBe("stale");
    expect(sub().rows).toBe(rows);
  });

  it("a wrong base leaves rows and metadata unchanged and resyncs on its own - no summary needed", () => {
    applyRows("a", full(3, [{ uid: "x", v: 1 }]), 0);
    const before = sub();
    expect(applyRows("a", delta(4, 5, [{ uid: "x", v: 9 }]), 10)).toBe("held"); // delta 4 was lost
    expect(sub()).toMatchObject({ ver: 3, rows: before.rows, partData: before.partData, evaluatedAt: before.evaluatedAt, resync: true });
    expect(useHealth.getState().summaries.c1).toBeUndefined();
    expect(taskCounts(useHealth.getState(), "c1", "pods", "pods")).toMatchObject({ source: "rows", updating: true, refreshing: false });
    expect(nextWake()).toBe(0);
    const due = dueForBootstrap(10);
    expect(due).toEqual([{ handle: "a", token: "tok-a", requestGen: 1 }]);
    expect(dueForBootstrap(11)).toEqual([]); // one in flight, never two
    expect(sub().refreshing).toBe(false); // a resync alone never switches the counts
    bootstrapSettled("a", 1, { error: "timeout" }, 20);
    expect(sub()).toMatchObject({ inflight: false, attempt: 1, retryAt: 20 + backoff(1), refreshError: "timeout", resync: true });
    expect(dueForBootstrap(20 + backoff(1) - 1)).toEqual([]); // backed off
    expect(nextWake()).toBe(20 + backoff(1));
    const again = dueForBootstrap(20 + backoff(1));
    expect(again).toEqual([{ handle: "a", token: "tok-a", requestGen: 2 }]);
    bootstrapSettled("a", 2, { rows: full(6, [{ uid: "x", v: 7 }]) }, 3000); // the lineage's latest, in full
    expect(held()).toEqual({ x: 7 });
    expect(sub()).toMatchObject({ ver: 6, resync: false, pending: [], inflight: false, attempt: 0, retryAt: null, refreshError: null });
  });

  it("a summary that is not ahead never cancels a resync's bootstrap", () => {
    applyRows("a", full(3, [{ uid: "x", v: 1 }]), 0);
    applyRows("a", delta(4, 5, [{ uid: "x", v: 9 }]), 10);
    dueForBootstrap(10);
    applySummary(summary(3), 20);
    expect(sub()).toMatchObject({ inflight: true, resync: true });
    bootstrapSettled("a", 1, { rows: full(5, [{ uid: "x", v: 9 }]) }, 30);
    expect(sub()).toMatchObject({ ver: 5, resync: false, inflight: false });
  });

  it("older and equal messages are ignored with no retry; duplicate and delayed deltas too", () => {
    applyRows("a", full(3, [{ uid: "x", v: 1 }]));
    applyRows("a", delta(3, 4, [{ uid: "x", v: 2 }]));
    for (const m of [delta(3, 4, [{ uid: "x", v: 99 }]), delta(2, 3, [{ uid: "x", v: 98 }]), full(4, [{ uid: "q", v: 1 }]), full(2, [])]) {
      expect(applyRows("a", m)).toBe("ignored");
    }
    expect(held()).toEqual({ x: 2 });
    expect(sub()).toMatchObject({ ver: 4, resync: false, pending: [] });
    expect(dueForBootstrap(0)).toEqual([]);
  });

  it("a delta that lands before the open's full is held, then applied once it chains", () => {
    addSub("b", "c1", "pods", "pods");
    openStarted("b", "tok-b");
    expect(applyRows("b", delta(5, 6, [{ uid: "x", v: 6 }]))).toBe("held");
    expect(sub("b")).toMatchObject({ rows: null, resync: false }); // the open's reply is coming: no resync
    openSucceeded("b", "tok-b", 1);
    expect(applyRows("b", full(5, [{ uid: "x", v: 5 }, { uid: "y", v: 5 }]))).toBe("applied");
    expect(held("b")).toEqual({ x: 6, y: 5 });
    expect(sub("b")).toMatchObject({ ver: 6, pending: [], resync: false });
  });

  it("a full reply racing deltas follows (incarnation, Ver) order", () => {
    applyRows("a", full(5, [{ uid: "x", v: 5 }]));
    applyRows("a", delta(5, 6, [{ uid: "x", v: 6 }]));
    expect(applyRows("a", full(5, [{ uid: "x", v: 5 }]))).toBe("ignored"); // a reply older than the deltas
    expect(applyRows("a", full(8, [{ uid: "x", v: 8 }]))).toBe("applied"); // a reply ahead of them
    expect(applyRows("a", delta(6, 7, [{ uid: "x", v: 7 }]))).toBe("ignored"); // the late delta it covers
    expect(applyRows("a", delta(8, 9, [{ uid: "x", v: 9 }]))).toBe("applied");
    expect(held()).toEqual({ x: 9 });
  });

  it("a newer incarnation's delta waits for its full; it never resyncs the old token", () => {
    applyRows("a", full(5, [{ uid: "x", v: 5 }]));
    expect(applyRows("a", delta(1, 2, [{ uid: "n", v: 2 }], [], 2))).toBe("held");
    expect(sub()).toMatchObject({ rows: null, incarnation: null, resync: false }); // the old incarnation's rows are cleared
    expect(applyRows("a", full(1, [{ uid: "n", v: 1 }], 2))).toBe("applied");
    expect(held()).toEqual({ n: 2 });
  });

  it("a Final drops the held deltas with the rows", () => {
    applyRows("a", full(3, [{ uid: "x", v: 1 }]));
    applyRows("a", delta(4, 5, [{ uid: "x", v: 9 }]));
    expect(sub().pending).toHaveLength(1);
    applySummary({ ...summary(5), final: true });
    expect(sub()).toMatchObject({ rows: null, pending: [], resync: false });
  });

  it("the held deltas are bounded", () => {
    applyRows("a", full(1, []));
    for (let v = 3; v < 3 + PENDING_MAX + 5; v++) applyRows("a", delta(v, v + 1, [{ uid: `u${v}`, v }]));
    expect(sub().pending).toHaveLength(PENDING_MAX);
    expect(sub().resync).toBe(true);
  });

  it("several consumers apply the broadcast independently", () => {
    open("b");
    applyRows("a", full(3, [{ uid: "x", v: 3 }]));
    applyRows("b", full(2, [{ uid: "x", v: 2 }]));
    const d = delta(3, 4, [{ uid: "x", v: 4 }]);
    expect(applyRows("a", d)).toBe("applied");
    expect(applyRows("b", d)).toBe("held");
    expect(sub("a").resync).toBe(false);
    expect(sub("b").resync).toBe(true);
    expect(dueForBootstrap(0)).toEqual([{ handle: "b", token: "tok-b", requestGen: 1 }]);
  });

  it("any delivery - reordered, duplicated, dropped, raced by full replies - ends equal to a full replace", () => {
    let seed = 7;
    const rnd = (n: number) => { seed = (seed * 1103515245 + 12345) % 2147483648; return seed % n; };
    for (let run = 0; run < 25; run++) {
      resetHealthStore();
      open("a");
      // the published states and the messages the lineage would send
      let state = new Map<string, number>(Array.from({ length: 12 }, (_, i) => [`u${i}`, 0]));
      let next = 12;
      const states: Map<string, number>[] = [new Map(state)];
      const msgs: RowsDTO[] = [full(1, [...state].map(([uid, v]) => ({ uid, v })))];
      for (let ver = 2; ver <= 40; ver++) {
        const prev = state;
        state = new Map(prev);
        for (let k = rnd(4); k > 0; k--) {
          const keys = [...state.keys()];
          const op = rnd(3);
          if (op === 0 || keys.length === 0) state.set(`u${next++}`, ver);
          else if (op === 1) state.delete(keys[rnd(keys.length)]);
          else state.set(keys[rnd(keys.length)], ver);
        }
        states.push(new Map(state));
        const ups = [...state].filter(([u, v]) => prev.get(u) !== v).map(([uid, v]) => ({ uid, v }));
        const removed = [...prev.keys()].filter((u) => !state.has(u));
        msgs.push(delta(ver - 1, ver, ups, removed));
      }
      // deliver: windows shuffled, some dropped, some duplicated; the open's
      // reply and a due resync answered with the latest full published so far
      let sent = 0;
      while (sent < msgs.length) {
        const win = msgs.slice(sent, sent + 1 + rnd(3));
        sent += win.length;
        for (let i = win.length - 1; i > 0; i--) { const j = rnd(i + 1); [win[i], win[j]] = [win[j], win[i]]; }
        for (const m of win) {
          if (rnd(10) === 0) continue; // dropped
          applyRows("a", m);
          if (rnd(8) === 0) applyRows("a", m); // duplicated
        }
        // the open's reply lands after the first window: the latest published, in full
        if (sent === win.length) applyRows("a", full(sent, [...states[sent - 1]].map(([uid, v]) => ({ uid, v }))));
        for (const d of dueForBootstrap(Date.now() + 60_000)) {
          const latest = sent - 1;
          bootstrapSettled(d.handle, d.requestGen, { rows: full(latest + 1, [...states[latest]].map(([uid, v]) => ({ uid, v }))) }, Date.now() + 60_000);
        }
      }
      // the stream ends: anything still unresolved is answered in full
      for (const d of dueForBootstrap(Date.now() + 3_600_000)) {
        bootstrapSettled(d.handle, d.requestGen, { rows: full(msgs.length, [...states[states.length - 1]].map(([uid, v]) => ({ uid, v }))) }, Date.now() + 3_600_000);
      }
      // whatever Ver it holds, its rows are exactly that published state
      expect(held()).toEqual(Object.fromEntries(states[sub().ver - 1]));
      expect(sub().resync).toBe(false);
      // a dropped last delta is found by A4 (the summary ahead): its full
      if (sub().ver !== msgs.length) applyRows("a", full(msgs.length, [...states[states.length - 1]].map(([uid, v]) => ({ uid, v }))));
      expect(sub().ver).toBe(msgs.length);
      expect(held()).toEqual(Object.fromEntries(states[states.length - 1]));
    }
  });
});
