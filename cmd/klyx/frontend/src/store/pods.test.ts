import { describe, it, expect, beforeEach } from "vitest";
import { useFleet } from "./fleet";
import type { PodSummaryDTO } from "./fleet";
import { rowOf } from "../test/podRows";

const makeContainer = () => ({ name: "app", image: "nginx:latest", ready: true, restarts: 0, state: "running", init: false });

const healthy: PodSummaryDTO = {
  namespace: "default", name: "api-abc", uid: "uid-abc", ready: true, phase: "Running", reason: "", rank: "healthy",
  restarts: 0, node: "node-1", ip: "10.0.0.1", ownerKind: "ReplicaSet", ownerName: "api-rs",
  ageSeconds: 3600, containers: [makeContainer()],
};
const broken: PodSummaryDTO = {
  namespace: "default", name: "api-xyz", uid: "uid-xyz", ready: false, phase: "Running", reason: "CrashLoopBackOff", rank: "unhealthy",
  restarts: 5, node: "node-2", ip: "10.0.0.2", ownerKind: "ReplicaSet", ownerName: "api-rs",
  ageSeconds: 600, containers: [{ ...makeContainer(), ready: false, restarts: 5, state: "waiting:CrashLoopBackOff" }],
};
const ref = (p: PodSummaryDTO) => ({ namespace: p.namespace, name: p.name, uid: p.uid ?? "" });
const detailOf = (p: PodSummaryDTO, over: Record<string, unknown> = {}) => ({ summary: p, labels: {}, conditions: [], events: [], yaml: "yaml: {}", qosClass: "BestEffort", serviceAccount: "default", ...over });

// The pods slice is filled by the rows adapter (health/usePodRows, 1c slice 3
// §5.9); the namespace is a list filter over all-namespace rows.
describe("pods slice", () => {
  beforeEach(() => useFleet.getState().clearPods());

  it("a new cluster's visit starts empty; a revisit keeps its rows and filters", () => {
    const f = useFleet.getState();
    f.setPodsVisit("c");
    f.setPodsFromRows("c", { items: [rowOf(healthy), rowOf(broken)], namespaces: ["default", "kube-system"], loading: false });
    f.setPodsNamespace("default");
    f.setPodsVisit("c");
    expect(useFleet.getState().pods.items).toHaveLength(2);
    expect(useFleet.getState().pods.namespace).toBe("default");
    f.setPodsVisit("other");
    expect(useFleet.getState().pods.items).toEqual([]);
    expect(useFleet.getState().pods.namespaces).toEqual([]);
    expect(useFleet.getState().pods.loading).toBe(true);
  });

  it("selectPod sets the selected object, clears detail, marks detailLoading", () => {
    useFleet.getState().selectPod(ref(healthy));
    const s = useFleet.getState().pods;
    expect(s.selected).toEqual({ namespace: "default", name: "api-abc", uid: "uid-abc" });
    expect(s.detail).toBeNull();
    expect(s.detailLoading).toBe(true);
  });

  it("re-selecting the same object keeps its detail; another object with the name clears it", () => {
    const f = useFleet.getState();
    f.selectPod(ref(healthy));
    f.setPodDetail(ref(healthy), detailOf(healthy));
    f.selectPod(ref(healthy));
    expect(useFleet.getState().pods.detail).not.toBeNull();
    f.selectPod({ ...ref(healthy), uid: "uid-abc-2" });
    expect(useFleet.getState().pods.detail).toBeNull();
  });

  it("selectPod(null) clears selection", () => {
    useFleet.getState().selectPod(ref(healthy));
    useFleet.getState().selectPod(null);
    const s = useFleet.getState().pods;
    expect(s.selected).toBeNull();
    expect(s.detailLoading).toBe(false);
  });

  it("setPodDetail stale-guard: detail for a deselected pod is dropped", () => {
    useFleet.getState().selectPod(ref(healthy));
    useFleet.getState().selectPod(null);
    useFleet.getState().setPodDetail(ref(healthy), detailOf(healthy));
    expect(useFleet.getState().pods.detail).toBeNull();
  });

  it("setPodDetail stale-guard: detail for a different pod or object is dropped", () => {
    useFleet.getState().selectPod(ref(healthy));
    useFleet.getState().setPodDetail(ref(broken), detailOf(broken));
    expect(useFleet.getState().pods.detail).toBeNull();
    useFleet.getState().setPodDetail({ ...ref(healthy), uid: "uid-abc-2" }, detailOf(healthy));
    expect(useFleet.getState().pods.detail).toBeNull();
  });

  it("setPodDetail lands when the ref matches the selected object", () => {
    useFleet.getState().selectPod(ref(healthy));
    useFleet.getState().setPodDetail(ref(healthy), detailOf(healthy, { labels: { app: "api" }, qosClass: "Burstable" }));
    const s = useFleet.getState().pods;
    expect(s.detail).not.toBeNull();
    expect(s.detail!.qosClass).toBe("Burstable");
    expect(s.detailLoading).toBe(false);
  });

  it("clearPods resets everything but a pending namespace request", () => {
    const f = useFleet.getState();
    f.setPodsVisit("c");
    f.setPodsFromRows("c", { items: [rowOf(healthy)], namespaces: ["default"], loading: false });
    f.selectPod(ref(healthy));
    f.togglePodsNeedsAttention();
    f.setPodsSearch("xyz");
    f.setPodsNamespace("default");
    f.requestPodsNamespace("c", "team");
    f.clearPods();
    const s = useFleet.getState().pods;
    expect(s.items).toHaveLength(0);
    expect(s.selected).toBeNull();
    expect(s.needsAttention).toBe(false);
    expect(s.search).toBe("");
    expect(s.namespace).toBe("");
    expect(s.cluster).toBeNull();
    expect(s.nsRequest).toEqual({ cluster: "c", namespace: "team" });
  });
});
