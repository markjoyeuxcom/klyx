import { describe, it, expect, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import type { LensRows } from "./useRows";

// The Karpenter adapter's own aggregation (coverage S10, Codex's
// implementation review finding 3): the NodeClasses part's refresh state and
// error reach the lens, and a change to them alone re-derives its result.
let parts: Record<string, LensRows<unknown>>;
vi.mock("./useRows", () => ({ useRows: (_c: string, _t: string, part: string) => parts[part] }));
import { useKarpenterRows } from "./useKarpenterRows";

const counts = { part: null, source: "rows" as const, updating: false, refreshing: false, at: null, resync: false, refreshError: null };
const lens = (over: Partial<LensRows<unknown>> = {}): LensRows<unknown> => ({
  rows: [], ver: { incarnation: 1, ver: 1 }, facts: {}, part: { id: "p", entity: "p", scope: "", classifier: "native", coverage: "live", kinds: [], obs: 1, ver: 1 },
  evaluatedAt: "2026-10-20T11:00:00Z", counts, error: null, refreshError: null, ...over,
});

describe("useKarpenterRows", () => {
  it("carries the NodeClasses part's refresh and its failure, and updates on each alone", () => {
    const base = lens();
    parts = { nodepools: base, nodeclaims: base, nodeclasses: base };
    const { result, rerender } = renderHook(() => useKarpenterRows("x"));
    expect(result.current.rowsState.refreshing).toBe(false);
    expect(result.current.classesState.refreshing).toBe(false);
    // only the class part changes, one field at a time, never its rows
    const step = (over: Partial<LensRows<unknown>>) => { parts = { ...parts, nodeclasses: { ...parts.nodeclasses, ...over } }; rerender(); };
    step({ counts: { ...counts, updating: true } });
    expect(result.current.rowsState.updating).toBe(true);
    step({ counts: { ...counts, updating: true, refreshing: true } });
    expect(result.current.rowsState.refreshing).toBe(true);
    expect(result.current.classesState.refreshing).toBe(true);
    step({ evaluatedAt: "2026-10-20T10:00:00Z" });
    expect(result.current.rowsState.evaluatedAt).toBe("2026-10-20T10:00:00Z");
    step({ refreshError: "classes bootstrap failed" });
    expect(result.current.rowsState.refreshError).toBe("classes bootstrap failed");
    expect(result.current.classesState.refreshError).toBe("classes bootstrap failed");
  });

  it("the NodeClasses part alone ends loading", () => {
    const none = lens({ part: null, rows: null, ver: null, evaluatedAt: null });
    parts = { nodepools: none, nodeclaims: none, nodeclasses: none };
    const { result, rerender } = renderHook(() => useKarpenterRows("x"));
    expect(result.current.loading).toBe(true);
    parts = { ...parts, nodeclasses: lens() };
    rerender();
    expect(result.current.loading).toBe(false);
  });
});

// Codex's re-review of 0758508: the selected counts carry their own part's
// qualification, never the held rows'.
describe("useKarpenterRows counts", () => {
  it("qualifies the selected counts by their own part", () => {
    const rowsPart = lens().part!;
    const summaryPart = { ...rowsPart, native: { total: 99, included: 5, word: "", roles: { total: 99, fault: 5, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 94 } },
      kinds: [{ kind: "nodeclaims", role: "counted", scope: "", coverage: "live" as const, objects: 92, truncated: true, notRetained: 7 }] };
    const claims = lens({ part: { ...rowsPart, kinds: [{ kind: "nodeclaims", role: "counted", scope: "", coverage: "live" as const, objects: 1 }] },
      counts: { ...counts, source: "summary", refreshing: true, part: summaryPart } });
    parts = { nodepools: lens(), nodeclaims: claims, nodeclasses: lens() };
    const { result } = renderHook(() => useKarpenterRows("x"));
    expect(result.current.partCounts.nodeclaims).toEqual({ total: 99, included: 5, fault: 5, lower: true });
    expect(result.current.coverage.find((k) => k.kind === "NodeClaim")?.truncated).toBeUndefined(); // the rows' own coverage
  });

  it("says last observed for a stale counted kind, never for a dependency, and nothing when whole", () => {
    const base = lens().part!;
    const native = { total: 3, included: 1, word: "", roles: { total: 3, fault: 1, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 2 } };
    const kind = (k: string, role: string, over = {}) => ({ kind: k, role, scope: "", coverage: "live" as const, objects: 1, ...over });
    const withCounts = (kinds: ReturnType<typeof kind>[]) => lens({ counts: { ...counts, part: { ...base, native, kinds } } });
    parts = {
      nodepools: withCounts([kind("nodepools", "counted"), kind("nodeclaims", "dependency", { coverage: "stale", truncated: true })]),
      nodeclaims: withCounts([kind("nodeclaims", "counted", { coverage: "stale" })]),
      nodeclasses: lens(),
    };
    const { result } = renderHook(() => useKarpenterRows("x"));
    expect(result.current.partCounts.nodepools).toEqual({ total: 3, included: 1, fault: 1 }); // its dependency qualifies no pool count
    expect(result.current.partCounts.nodeclaims).toEqual({ total: 3, included: 1, fault: 1, stale: true });
  });
});
