import { describe, it, expect, beforeEach } from "vitest";
import { renderHook } from "@testing-library/react";
import { useNavCounts } from "./useNavCounts";
import { useFleet } from "../store/fleet";

const counts = (fault: number, degraded: number) => ({ total: 10, fault, degraded, unknown: 0, progressing: 0, paused: 0, ok: 10 - fault - degraded });

beforeEach(() => {
  useFleet.getState().clearPods();
  useFleet.getState().clearAttention();
});

// The Pods badge: the open lens's global verdict counts win over the poll
// while its Pod read is complete (1c slice 3 §4a).
describe("useNavCounts - the Pods lens rows win while complete", () => {
  it("uses the lens's global counts, whatever the namespace filter", () => {
    useFleet.getState().setPodsVisit("c");
    useFleet.getState().setPodsFromRows("c", { items: [], namespaces: [], loading: false, counts: counts(2, 1), coverage: [{ kind: "Pod", served: true, synced: true, coverage: "live" }] });
    useFleet.getState().setPodsNamespace("team");
    const { result } = renderHook(() => useNavCounts("c"));
    expect(result.current.destination.pods).toMatchObject({ n: 3, tone: "danger" });
    expect(result.current.destination.pods.title).toMatch(/Pods lens rows$/);
  });

  it("an incomplete read, or another cluster's rows, never wins", () => {
    useFleet.getState().setPodsVisit("c");
    useFleet.getState().setPodsFromRows("c", { items: [], namespaces: [], loading: false, counts: counts(2, 0), coverage: [{ kind: "Pod", served: true, synced: true, coverage: "stale", error: "dial tcp" }], error: "Pod: dial tcp" });
    expect(renderHook(() => useNavCounts("c")).result.current.destination.pods.title ?? "").not.toMatch(/Pods lens rows/);
  });

  it("another cluster's complete rows never win", () => {
    useFleet.getState().setPodsVisit("c");
    useFleet.getState().setPodsFromRows("c", { items: [], namespaces: [], loading: false, counts: counts(2, 0), coverage: [{ kind: "Pod", served: true, synced: true, coverage: "live" }] });
    expect(renderHook(() => useNavCounts("other")).result.current.destination.pods.title ?? "").not.toMatch(/Pods lens rows/);
  });
});
