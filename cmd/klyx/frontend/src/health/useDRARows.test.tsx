import { describe, it, expect, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import type { LensRows } from "./useRows";

// Coverage S11b §3.5: a claims read is complete (a claim's absence in it is
// proof) only while the ResourceClaims it counts are live and whole; the
// pods it joins are a dependency, which proves nothing of a claim's absence.
let part: LensRows<unknown>;
vi.mock("./useRows", () => ({ useRows: () => part }));
import { useDRAClaims } from "./useDRARows";

const counts = { part: null, source: "rows" as const, updating: false, refreshing: false, at: null, resync: false, refreshError: null };
const lens = (claims: string, truncated = false, pods = "live"): LensRows<unknown> => ({
  rows: [], ver: { incarnation: 1, ver: 1 }, facts: {}, evaluatedAt: null, counts, error: null, refreshError: null,
  part: { id: "claims", entity: "ResourceClaims", scope: "", classifier: "native", coverage: "live", obs: 1, ver: 1,
    kinds: [{ kind: "resourceclaims", role: "counted", scope: "", coverage: claims as never, objects: 1, truncated }, { kind: "pods", role: "dependency", scope: "", coverage: pods as never, objects: 1 }] } as never,
});

describe("useDRAClaims", () => {
  it("is complete only while the claims it counts are live and whole", () => {
    part = lens("live");
    expect(renderHook(() => useDRAClaims("c")).result.current.complete).toBe(true);
    part = lens("live", false, "stale");
    expect(renderHook(() => useDRAClaims("c")).result.current.complete).toBe(true);
    part = lens("stale");
    expect(renderHook(() => useDRAClaims("c")).result.current.complete).toBe(false);
    part = lens("live", true);
    expect(renderHook(() => useDRAClaims("c")).result.current.complete).toBe(false);
    part = { ...lens("live"), part: null, rows: null };
    expect(renderHook(() => useDRAClaims("c")).result.current.complete).toBe(false);
  });
});
