import { describe, it, expect, beforeEach } from "vitest";
import { useFleet } from "./fleet";
import type { PodsResultDTO, PodSummaryDTO } from "./fleet";

const makeContainer = () => ({ name: "app", image: "nginx:latest", ready: true, restarts: 0, state: "running", init: false });

const healthy: PodSummaryDTO = {
  namespace: "default", name: "api-abc", ready: true, phase: "Running", reason: "", rank: "healthy",
  restarts: 0, node: "node-1", ip: "10.0.0.1", ownerKind: "ReplicaSet", ownerName: "api-rs",
  ageSeconds: 3600, containers: [makeContainer()],
};
const broken: PodSummaryDTO = {
  namespace: "default", name: "api-xyz", ready: false, phase: "Running", reason: "CrashLoopBackOff", rank: "unhealthy",
  restarts: 5, node: "node-2", ip: "10.0.0.2", ownerKind: "ReplicaSet", ownerName: "api-rs",
  ageSeconds: 600, containers: [{ ...makeContainer(), ready: false, restarts: 5, state: "waiting:CrashLoopBackOff" }],
};

const allResult: PodsResultDTO = { namespaces: ["default", "kube-system"], pods: [healthy, broken] };

describe("pods slice", () => {
  beforeEach(() => useFleet.getState().clearPods());

  it("setPods populates items and namespaces on all-load", () => {
    useFleet.getState().setPods("c", "", allResult);
    const s = useFleet.getState().pods;
    expect(s.items).toHaveLength(2);
    expect(s.namespaces).toEqual(["default", "kube-system"]);
    expect(s.loading).toBe(false);
  });

  it("setPods preserves namespaces on scoped load", () => {
    useFleet.getState().setPods("c", "", allResult);
    useFleet.getState().setPods("c", "default", { namespaces: [], pods: [healthy] });
    expect(useFleet.getState().pods.namespaces).toEqual(["default", "kube-system"]);
  });

  it("setPods falls back to [namespace] when first load is scoped", () => {
    useFleet.getState().setPods("c", "team", { namespaces: [], pods: [] });
    expect(useFleet.getState().pods.namespaces).toEqual(["team"]);
  });

  it("selectPod sets selected, clears detail, marks detailLoading", () => {
    useFleet.getState().setPods("c", "", allResult);
    useFleet.getState().selectPod({ namespace: "default", name: "api-abc" });
    const s = useFleet.getState().pods;
    expect(s.selected).toEqual({ namespace: "default", name: "api-abc" });
    expect(s.detail).toBeNull();
    expect(s.detailLoading).toBe(true);
  });

  it("selectPod(null) clears selection", () => {
    useFleet.getState().selectPod({ namespace: "default", name: "api-abc" });
    useFleet.getState().selectPod(null);
    const s = useFleet.getState().pods;
    expect(s.selected).toBeNull();
    expect(s.detailLoading).toBe(false);
  });

  it("setPodDetail stale-guard: detail for a deselected pod is dropped", () => {
    useFleet.getState().selectPod({ namespace: "default", name: "api-abc" });
    // navigate away
    useFleet.getState().selectPod(null);
    // late response arrives for the old pod
    const fakeDetail = { summary: healthy, labels: {}, conditions: [], events: [], yaml: "yaml: {}", qosClass: "BestEffort", serviceAccount: "default" };
    useFleet.getState().setPodDetail({ namespace: "default", name: "api-abc" }, fakeDetail);
    expect(useFleet.getState().pods.detail).toBeNull();
  });

  it("setPodDetail stale-guard: detail for a different pod is dropped", () => {
    useFleet.getState().selectPod({ namespace: "default", name: "api-abc" });
    const fakeDetail = { summary: broken, labels: {}, conditions: [], events: [], yaml: "yaml: {}", qosClass: "BestEffort", serviceAccount: "default" };
    // response for a different pod
    useFleet.getState().setPodDetail({ namespace: "default", name: "api-xyz" }, fakeDetail);
    expect(useFleet.getState().pods.detail).toBeNull();
  });

  it("setPodDetail lands when ref matches selected", () => {
    useFleet.getState().selectPod({ namespace: "default", name: "api-abc" });
    const fakeDetail = { summary: healthy, labels: { app: "api" }, conditions: [], events: [], yaml: "yaml: {}", qosClass: "Burstable", serviceAccount: "default" };
    useFleet.getState().setPodDetail({ namespace: "default", name: "api-abc" }, fakeDetail);
    const s = useFleet.getState().pods;
    expect(s.detail).not.toBeNull();
    expect(s.detail!.qosClass).toBe("Burstable");
    expect(s.detailLoading).toBe(false);
  });

  it("clearPods resets everything including live", () => {
    useFleet.getState().setPods("c", "", allResult);
    useFleet.getState().selectPod({ namespace: "default", name: "api-abc" });
    useFleet.getState().togglePodsNeedsAttention();
    useFleet.getState().setPodsSearch("xyz");
    // Manually set live to true so we can verify clear resets it.
    useFleet.setState((s) => ({ pods: { ...s.pods, live: true } }));
    useFleet.getState().clearPods();
    const s = useFleet.getState().pods;
    expect(s.items).toHaveLength(0);
    expect(s.selected).toBeNull();
    expect(s.needsAttention).toBe(false);
    expect(s.search).toBe("");
    expect(s.cluster).toBeNull();
    expect(s.live).toBe(false);
  });

  it("setPodsLive updates live when cluster+namespace match", () => {
    useFleet.getState().setPods("c", "default", { namespaces: ["default"], pods: [] });
    useFleet.getState().setPodsLive("c", "default", true);
    expect(useFleet.getState().pods.live).toBe(true);
    useFleet.getState().setPodsLive("c", "default", false);
    expect(useFleet.getState().pods.live).toBe(false);
  });

  it("setPodsLive stale-guard: wrong cluster is a no-op", () => {
    useFleet.getState().setPods("c", "", allResult);
    useFleet.getState().setPodsLive("other", "", true);
    expect(useFleet.getState().pods.live).toBe(false);
  });

  it("setPodsLive stale-guard: wrong namespace is a no-op", () => {
    useFleet.getState().setPods("c", "default", { namespaces: ["default"], pods: [] });
    useFleet.getState().setPodsLive("c", "monitoring", true);
    expect(useFleet.getState().pods.live).toBe(false);
  });

  // Review 2026-10-03 #10: a failed read flags the slice and keeps the last
  // good rows for the same scope - never wipes them into a fake "no pods".
  it("a failed read keeps the last good rows for the same scope and flags the error", () => {
    useFleet.getState().setPods("c", "", allResult);
    useFleet.getState().setPods("c", "", { namespaces: [], pods: [], error: "pods is forbidden" });
    let s = useFleet.getState().pods;
    expect(s.items).toHaveLength(2);
    expect(s.error).toBe("pods is forbidden");
    // A later good read clears the flag.
    useFleet.getState().setPods("c", "", allResult);
    s = useFleet.getState().pods;
    expect(s.error).toBeNull();
    // A failure for a DIFFERENT scope must not show the previous scope's rows.
    useFleet.getState().setPods("other", "", { namespaces: [], pods: [], error: "cluster not connected: other" });
    s = useFleet.getState().pods;
    expect(s.items).toHaveLength(0);
    expect(s.error).toMatch(/not connected/);
  });
});
