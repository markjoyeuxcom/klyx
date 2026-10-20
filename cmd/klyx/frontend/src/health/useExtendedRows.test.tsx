import { describe, it, expect, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import type { LensRows } from "./useRows";

// Coverage S11a: the adapter joins reservations by node UID and counts what
// the bounds left out from each part's Evidence (its count before any cut).
let parts: Record<string, LensRows<unknown>>;
vi.mock("./useRows", () => ({ useRows: (_c: string, _t: string, part: string) => parts[part] }));
import { useExtendedRows } from "./useExtendedRows";

const counts = { part: null, source: "rows" as const, updating: false, refreshing: false, at: null, resync: false, refreshError: null };
const lens = (rows: unknown[], evidence: number, pods = "live"): LensRows<unknown> => ({
  rows, ver: { incarnation: 1, ver: 1 }, facts: {}, evaluatedAt: null, counts, error: null, refreshError: null,
  part: { id: "p", entity: "p", scope: "", classifier: "evidence", coverage: "live", obs: 1, ver: 1, evidence,
    kinds: [{ kind: "nodes", role: "counted", scope: "", coverage: "live", objects: 1 }, { kind: "pods", role: "dependency", scope: "", coverage: pods as never, objects: 1 }] },
});

describe("useExtendedRows", () => {
  it("joins by UID, counts dropped rows and omitted totals, and qualifies by each part's reads", () => {
    parts = {
      reservations: lens([{ uid: "u1", node: "a", entries: [] }], 3, "stale"),
      totals: lens([{ name: "nvidia.com/gpu", capacity: "8", allocatable: "8", requested: "1" }], 33),
    };
    const { result } = renderHook(() => useExtendedRows("x"));
    expect(result.current.reservations.get("u1")?.node).toBe("a");
    expect(result.current.reservationsDropped).toBe(2);
    expect(result.current.totalsOmitted).toBe(32);
    expect(result.current.reservationMarks).toEqual({ nodes: "", requested: "*", dra: "*" });
    expect(result.current.totalMarks).toEqual({ nodes: "", requested: "", dra: "" });
  });
  it("no part yet: nothing read, every figure unestablished", () => {
    const none = { ...lens([], 0), part: null, rows: null };
    parts = { reservations: none, totals: none };
    const { result } = renderHook(() => useExtendedRows("x"));
    expect(result.current.reservationsRead).toBe(false);
    expect(result.current.reservationMarks.requested).toBe("?");
  });
});
