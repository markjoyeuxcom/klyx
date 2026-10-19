import { describe, it, expect } from "vitest";
import type { ClusterDTO, ClusterSection, Route } from "../store/fleet";
import { NAV_GROUPS, activeLocation, capState, destState, entryOf, groupState, isActive } from "./registry";

const all = { Deployment: true, StatefulSet: true, DaemonSet: true };
const c = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "", gitopsTier: "", gitopsReason: "",
  networkTier: "", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 0, ...over,
} as ClusterDTO);
const dest = (id: string) => NAV_GROUPS.flatMap((g) => g.destinations).find((d) => d.id === id)!;
const route = (section: ClusterSection, extra: object = {}): Route => ({ name: "cluster", cluster: "x", section, ...extra });

describe("navigation registry (redesign step 7a)", () => {
  it("every cluster section has a destination - no feature loss in the regrouping", () => {
    const sections: ClusterSection[] = ["overview", "gitops", "argo", "helm", "certs", "externalsecrets", "gatewayApi", "flows", "resources", "crds", "workloads", "pods", "events", "nodes", "access", "upgrade", "connectivity", "karpenter"];
    const reached = new Set(NAV_GROUPS.flatMap((g) => g.destinations).flatMap((d) => (d.target.kind === "section" ? [d.target.section] : [])));
    expect(sections.filter((s) => !reached.has(s))).toEqual([]);
  });

  it("destination ids are unique within a group, and tasks are unique app-wide", () => {
    for (const g of NAV_GROUPS) {
      const ids = g.destinations.map((d) => d.id);
      expect(new Set(ids).size).toBe(ids.length);
    }
    const tasks = NAV_GROUPS.flatMap((g) => g.destinations.filter((d) => d.type === "task").map((d) => d.id));
    expect(new Set(tasks).size).toBe(tasks.length);
  });

  it("a diagnosis task and its raw view are distinct destinations", () => {
    const certs = NAV_GROUPS.find((g) => g.id === "certs")!;
    const named = certs.destinations.filter((d) => d.label === "Certificates");
    expect(named.map((d) => d.type).sort()).toEqual(["resource", "task"]);
  });

  it("capability: detected present/absent; connecting is checking; unreadable is unknown - never absent", () => {
    expect(capState(c({ fluxPresent: true }), "flux")).toBe("present");
    expect(capState(c({ fluxPresent: false }), "flux")).toBe("absent");
    expect(capState(c({ state: "Connecting" }), "flux")).toBe("checking");
    expect(capState(c({ state: "Failed" }), "flux")).toBe("unknown");
    expect(capState(c({ state: "Stale", hasHubble: true }), "hubble")).toBe("present");
    expect(capState(c(), undefined)).toBe("present");
  });

  it("a destination needs its group's capability; a group with any available destination is offered", () => {
    const flux = NAV_GROUPS.find((g) => g.id === "flux")!;
    expect(destState(c({ fluxPresent: false }), flux, dest("flux"))).toBe("absent");
    expect(groupState(c({ fluxPresent: false }), flux)).toBe("absent");
    const network = NAV_GROUPS.find((g) => g.id === "network")!;
    expect(groupState(c(), network)).toBe("present");
    expect(destState(c(), network, dest("gateway"))).toBe("absent");
    expect(destState(c({ gatewayAPIVersion: "v1.2" }), network, dest("gateway"))).toBe("present");
  });

  it("group entry is the first available task, else the first resource", () => {
    const autoscaling = NAV_GROUPS.find((g) => g.id === "autoscaling")!;
    // coverage S9: the Autoscalers task needs no capability
    expect(entryOf(c(), autoscaling)?.label).toBe("Autoscalers");
    expect(entryOf(c({ karpenterPresent: true }), autoscaling)?.id).toBe("karpenter");
    expect(entryOf(c(), NAV_GROUPS.find((g) => g.id === "config")!)?.label).toBe("ConfigMaps");
  });

  it("route identity: argo/flux default views, kind links, raw views", () => {
    expect(isActive(route("argo"), dest("argo"), all)).toBe(true);
    expect(isActive(route("argo", { argoView: "projects" }), dest("argo"), all)).toBe(false);
    expect(isActive(route("gitops"), dest("flux"), all)).toBe(true);
    expect(isActive(route("gitops", { fluxView: "sources" }), dest("flux-sources"), all)).toBe(true);
    expect(isActive(route("workloads"), dest("workloads"), all)).toBe(true);
    expect(isActive(route("workloads"), dest("workloads:Deployment"), { Deployment: true, StatefulSet: false, DaemonSet: false })).toBe(true);
    const jobs = route("crds", { resource: { group: "batch", version: "v1", plural: "jobs", kind: "Job", scope: "Namespaced" } });
    expect(activeLocation(jobs, all).destination?.label).toBe("Jobs");
    expect(activeLocation(jobs, all).group?.id).toBe("workloads");
  });

  it("an instance opened from the CRD browser keeps the API browser group", () => {
    const crd = route("crds", { resource: { group: "example.com", version: "v1", plural: "widgets", kind: "Widget", scope: "Namespaced" } });
    const loc = activeLocation(crd, all);
    expect(loc.destination).toBeUndefined();
    expect(loc.group?.id).toBe("api");
  });
});
