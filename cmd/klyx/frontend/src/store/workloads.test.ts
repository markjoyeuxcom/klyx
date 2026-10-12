import { describe, it, expect, beforeEach } from "vitest";
import { useFleet } from "./fleet";
import type { WorkloadDTO } from "./fleet";

const wl = (name: string, ns = "ns"): WorkloadDTO => ({
  kind: "Deployment", namespace: ns, name, uid: `uid-${name}`, desired: 1, ready: 1, available: 1, updated: 1, restarts: 0, reason: "Available", rank: "healthy", gitops: null, pods: [],
  resources: { cpu: { usage: null, request: 0.25, limit: 0.5 }, mem: { usage: null, request: null, limit: 536870912 } },
});

// The workloads slice is filled by the rows adapter (health/useWorkloadRows,
// 1c slice 3 §5.8); the namespace is a list filter over all-namespace rows.
describe("workloads slice", () => {
  beforeEach(() => useFleet.getState().clearWorkloads());

  it("a new cluster's visit starts empty; a revisit keeps its rows and filters", () => {
    const f = useFleet.getState();
    f.setWorkloadsVisit("c");
    f.setWorkloadsFromRows("c", { items: [wl("api")], namespaces: ["ns"], fluxPresent: true, loading: false });
    f.setWorkloadsNamespace("ns");
    f.setWorkloadsVisit("c");
    expect(useFleet.getState().workloads.items).toHaveLength(1);
    expect(useFleet.getState().workloads.namespace).toBe("ns");
    f.setWorkloadsVisit("other");
    expect(useFleet.getState().workloads.items).toEqual([]);
    expect(useFleet.getState().workloads.namespaces).toEqual([]);
    expect(useFleet.getState().workloads.loading).toBe(true);
  });

  it("toggles expand by key", () => {
    useFleet.getState().toggleWorkloadExpand("Deployment/x/y");
    expect(useFleet.getState().workloads.expanded).toContain("Deployment/x/y");
    useFleet.getState().toggleWorkloadExpand("Deployment/x/y");
    expect(useFleet.getState().workloads.expanded).not.toContain("Deployment/x/y");
  });

  it("clearWorkloads resets kindFilter, needsAttention, search, namespace and usage", () => {
    const f = useFleet.getState();
    f.toggleWorkloadKind("Deployment");
    f.toggleNeedsAttention();
    f.setWorkloadsSearch("api");
    f.setWorkloadsNamespace("team");
    expect(useFleet.getState().workloads.kindFilter.Deployment).toBe(false);
    f.clearWorkloads();
    const s = useFleet.getState().workloads;
    expect(s.kindFilter.Deployment).toBe(true);
    expect(s.needsAttention).toBe(false);
    expect(s.search).toBe("");
    expect(s.namespace).toBe("");
    expect(s.usage).toEqual({});
  });

  it("setWorkloadsSearch stores the search string", () => {
    useFleet.getState().setWorkloadsSearch("grafana");
    expect(useFleet.getState().workloads.search).toBe("grafana");
  });

  it("usage is kept apart from the rows: a rows push never erases it", () => {
    const f = useFleet.getState();
    f.setWorkloadsVisit("c");
    f.setWorkloadsFromRows("c", { items: [wl("api")], namespaces: ["ns"], fluxPresent: false, loading: false });
    f.setWorkloadUsage("c", "", { status: { available: true, message: "", updatedAt: "t1" }, usage: { "Deployment/ns/api": { cpuUsage: 0.3, memUsage: 400000000 } } });
    f.setWorkloadsFromRows("c", { items: [{ ...wl("api"), reason: "Progressing" }], namespaces: ["ns"], fluxPresent: false, loading: false });
    const s = useFleet.getState().workloads;
    expect(s.usage["Deployment/ns/api"]).toEqual({ cpu: 0.3, mem: 400000000 });
    expect(s.items[0].resources.cpu.usage).toBeNull(); // the rows stay as captured; usage joins at render
    expect(s.metricsAvailable).toBe(true);
  });

  it("setWorkloadUsage drops an answer for another namespace scope", () => {
    const f = useFleet.getState();
    f.setWorkloadsVisit("c");
    f.setWorkloadsNamespace("team");
    f.setWorkloadUsage("c", "", { status: { available: true, message: "", updatedAt: "t1" }, usage: { "Deployment/ns/api": { cpuUsage: 0.3, memUsage: null } } });
    expect(useFleet.getState().workloads.usage).toEqual({});
  });

  it("setWorkloadUsage transient failure keeps last-good usage, marks stale", () => {
    const f = useFleet.getState();
    f.setWorkloadsVisit("c");
    f.setWorkloadUsage("c", "", { status: { available: true, message: "", updatedAt: "t1" }, usage: { "Deployment/ns/api": { cpuUsage: 0.3, memUsage: null } } });
    f.setWorkloadUsage("c", "", { status: { available: false, message: "down", updatedAt: "" }, usage: {} });
    const s = useFleet.getState().workloads;
    expect(s.usage["Deployment/ns/api"].cpu).toBe(0.3);
    expect(s.metricsStale).toBe(true);
  });

  it("setWorkloadUsage first-load unavailable keeps metricsAvailable false (columns hidden)", () => {
    const f = useFleet.getState();
    f.setWorkloadsVisit("c");
    f.setWorkloadUsage("c", "", { status: { available: false, message: "no source", updatedAt: "" }, usage: {} });
    const s = useFleet.getState().workloads;
    expect(s.metricsAvailable).toBe(false);
    expect(s.metricsStale).toBe(false);
  });
});
