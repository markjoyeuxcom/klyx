import { describe, it, expect, beforeEach } from "vitest";
import { useHealth, resetHealthStore, applySummary } from "../store/health";
import type { ClusterDTO, Route, WorkloadKind } from "../store/fleet";
import { core, evidence, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";
import { clusterAttention } from "./clusterAttention";
import { navCounts } from "./useNavCounts";
import { NAV_GROUPS } from "./registry";
import { absentLine, activeKey, lensIndex } from "./lensIndex";

// The Line slice 4 §4: the lens index's entries, its two sections and its
// absent line - every number navCounts', nothing counted here.

const cl = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name: "dev", state: "Synced", reason: "", env: "dev", region: "", fluxPresent: true, argoPresent: true, ...over } as ClusterDTO);
const ALL: Record<WorkloadKind, boolean> = { Deployment: true, StatefulSet: true, DaemonSet: true };
const at = (section: string, extra: object = {}): Route => ({ name: "cluster", cluster: "dev", section, ...extra } as Route);
const model = (c: ClusterDTO, route: Route = at("overview"), kinds = ALL) => lensIndex(c, navCounts(clusterAttention(useHealth.getState(), c.name, c)), route, kinds);
const labels = (es: { label: string }[]) => es.map((e) => e.label);
const flux = (recs: Parameters<typeof native>[1], srcs: Parameters<typeof native>[1], over = {}) =>
  task("flux", [native("reconciliations", recs, (recs.fault ?? 0) + (recs.degraded ?? 0), "not ready", [kind("kustomizations")]), native("sources", srcs, (srcs.fault ?? 0) + (srcs.degraded ?? 0), "not ready", [kind("gitrepositories")])], over);
const argo = (over = {}) => task("argo", [native("applications", { ok: 2 }, 0, "out of sync or unhealthy", [kind("applications")]), native("applicationsets", { ok: 1 }, 0, "need attention", [kind("applicationsets")])], over);

beforeEach(() => resetHealthStore());

describe("lens index entries (§4.1)", () => {
  it("core tasks are lenses of their own; tool groups are one lens each; the browsers are two; index labels apply", () => {
    applySummary(summary([...core(), flux({ ok: 2 }, { ok: 1 }), argo()], {}, "dev"));
    const m = model(cl({ gatewayAPIVersion: "v1.2", hasHubble: true, karpenterPresent: true, certManagerPresent: true, externalSecretsPresent: true, veleroPresent: true, admissionPresent: true, loadBalancingPresent: true, alertsPresent: true }));
    expect(m.needsYou).toEqual([]);
    expect(labels(m.rest)).toEqual(["Overview", "Nodes", "Events", "Alerts", "Upgrade check", "Workloads", "Pods", "Admission", "Karpenter", "Autoscalers", "Connectivity check", "Gateways and routes", "Load balancers", "Hubble flows",
      "Access check", "Flux CD", "Argo CD", "cert-manager", "External Secrets", "Velero", "Helm", "API browser", "Resources"]);
    expect(m.absent).toEqual([]);
  });

  it("a tool lens opens its first task; its quiet parts are the part switch's, so the open lens lists none and is current on any part (slice 13b.1)", () => {
    applySummary(summary([...core(), flux({ ok: 2 }, { ok: 1 }), argo()], {}, "dev"));
    const closed = model(cl());
    const argoLens = closed.rest.find((e) => e.key === "lens:argo")!;
    expect(argoLens.destination.id).toBe("argo");
    expect(argoLens.subs).toEqual([]);
    expect(argoLens.current).toBe(false);
    const open = model(cl(), at("argo", { argoView: "projects" }));
    const lens = open.rest.find((e) => e.key === "lens:argo")!;
    expect(lens.subs).toEqual([]);
    expect(lens.current).toBe(true);
    expect(open.rest.find((e) => e.key === "lens:flux")!.current).toBe(false);
  });

  it("checking and unknown capabilities stay listed with their word; a confirmed absence is hidden and named once", () => {
    const connecting = model(cl({ state: "Connecting" }));
    const fx = connecting.rest.find((e) => e.key === "lens:flux")!;
    expect([fx.state, fx.note]).toEqual(["checking", "checking…"]);
    const failed = model(cl({ state: "Failed" }));
    expect(failed.rest.find((e) => e.key === "lens:argo")!.note).toBe("unknown");
    expect(failed.absent).toEqual([]);
    const m = model(cl({ argoPresent: false }));
    expect(m.rest.some((e) => e.key === "lens:argo")).toBe(false);
    expect(m.absent).toEqual(["Alerts", "Admission", "Karpenter", "Gateways and routes", "Load balancers", "Hubble flows", "Argo CD", "cert-manager", "External Secrets", "Velero"]);
    expect(absentLine(m.absent)).toBe("Alerts, Admission, Karpenter, Gateways and routes, Load balancers, Hubble flows, Argo CD, cert-manager, External Secrets and Velero are not installed here, so they are not listed.");
    expect(absentLine(["Argo CD"])).toBe("Argo CD is not installed here, so it is not listed.");
    expect(absentLine(["Argo CD", "Karpenter"])).toBe("Argo CD and Karpenter are not installed here, so they are not listed.");
    expect(absentLine([])).toBeNull();
  });
});

describe("Needs you (§4.2)", () => {
  it("danger before warning, then count, then registry order; each with its own badge", () => {
    applySummary(summary([...replace(replace(replace(core(),
      task("workloads", [verdict("workloads", { degraded: 5, ok: 1 }, [kind("deployments")])])),
      task("pods", [verdict("pods", { fault: 1, ok: 3 }, [kind("pods")])])),
      task("nodes", [verdict("nodes", { fault: 2 }, [kind("nodes")])])), flux({ ok: 2 }, { ok: 1 }), argo()], {}, "dev"));
    const m = model(cl());
    expect(m.needsYou.map((e) => [e.label, e.tone, e.badge?.n])).toEqual([["Nodes", "danger", 2], ["Pods", "danger", 1], ["Workloads", "warning", 5]]);
    expect(labels(m.rest)).not.toContain("Pods");
  });

  it("a sub-task lit alone sits under its lens, a context row with no mark or count of its own (slice 13a.5)", () => {
    applySummary(summary([...core(), flux({ ok: 2 }, { fault: 1 }), argo()], {}, "dev"));
    const m = model(cl(), at("gitops", { fluxView: "reconciliation" }));
    expect(m.needsYou.map((e) => [e.key, e.label, e.tone, e.badge?.n, e.context])).toEqual([["lens:flux", "Flux CD", undefined, undefined, true]]);
    const lens = m.needsYou[0];
    expect(lens.current).toBe(true);
    expect(lens.subs.map((s) => [s.key, s.label, s.tone, s.badge?.n])).toEqual([["sub:flux-sources", "Sources", "danger", 1]]);
    expect(m.rest.some((e) => e.key === "lens:flux")).toBe(false);
  });

  it("a group takes the place of its highest member; every member keeps its own tone and count, never a sum", () => {
    // the lens at look-at (2 degraded), its sub at fault (1): the group ranks
    // as danger, ahead of a look-at Workloads with more objects
    applySummary(summary([...replace(core(), task("workloads", [verdict("workloads", { degraded: 5, ok: 1 }, [kind("deployments")])])),
      flux({ degraded: 2, ok: 1 }, { fault: 1 }), argo()], {}, "dev"));
    const m = model(cl());
    expect(m.needsYou.map((e) => [e.key, e.tone, e.badge?.n, e.context ?? false])).toEqual([["lens:flux", "warning", 2, false], ["dest:workloads", "warning", 5, false]]);
    expect(m.needsYou[0].subs.map((s) => [s.key, s.tone, s.badge?.n])).toEqual([["sub:flux-sources", "danger", 1]]);
  });

  it("a not-ready controller puts its lens in Needs you as danger, with the words", () => {
    applySummary(summary([...core(), flux({ ok: 2 }, { ok: 1 }), argo({ controller: { state: "not ready", reason: "0/1" } })], {}, "dev"));
    const m = model(cl());
    expect(m.needsYou.map((e) => [e.key, e.tone])).toEqual([["lens:argo", "danger"]]);
    expect(m.needsYou[0].note).toContain("controller not ready");
  });

  it("evidence is never Needs you; quiet notes ride along in Everything else", () => {
    applySummary(summary([...replace(replace(core(), task("events", [evidence(4, [kind("events")])])),
      task("pods", [verdict("pods", { ok: 3 }, [kind("pods", { coverage: "stale", lastGoodAt: "2026-10-15T09:00:00Z" })], { coverage: "stale" })])), flux({ ok: 2 }, { ok: 1 }), argo()], {}, "dev"));
    const m = model(cl());
    expect(m.needsYou).toEqual([]);
    const ev = m.rest.find((e) => e.key === "dest:events")!;
    expect([ev.badge?.n, ev.badge?.tone]).toEqual([4, undefined]);
    expect(m.rest.find((e) => e.key === "dest:pods")!.note).toBe("last good");
  });
});

describe("the current entry (§4.3)", () => {
  it("task routes, tool lenses, raw kinds and kind shortcuts each mark one entry", () => {
    expect(activeKey(at("pods"), ALL)).toBe("dest:pods");
    expect(activeKey(at("gitops", { fluxView: "sources" }), ALL)).toBe("sub:flux-sources");
    expect(activeKey(at("gitops"), ALL)).toBe("lens:flux");
    expect(activeKey(at("resources", { resource: { group: "batch", version: "v1", plural: "jobs", kind: "Job", scope: "Namespaced" } }), ALL)).toBe("dest:builtins");
    expect(activeKey(at("crds", { resource: { group: "cilium.io", version: "v2", plural: "ciliumnetworkpolicies", kind: "CiliumNetworkPolicy", scope: "Namespaced" } }), ALL)).toBe("dest:crds");
    expect(activeKey(at("resources", { resource: { group: "kustomize.toolkit.fluxcd.io", version: "v1", plural: "kustomizations", kind: "Kustomization", scope: "Namespaced" } }), ALL)).toBe("lens:flux");
    expect(activeKey(at("workloads"), { Deployment: true, StatefulSet: false, DaemonSet: false })).toBe("dest:workloads");
    expect(activeKey(at("workloads"), { Deployment: false, StatefulSet: true, DaemonSet: true })).toBe("dest:workloads");
    expect(activeKey({ name: "fleet" }, ALL)).toBeNull();
  });

  it("every registry task destination is reachable from an index entry or its lens", () => {
    applySummary(summary([...core(), flux({ ok: 2 }, { ok: 1 }), argo()], {}, "dev"));
    const c = cl({ gatewayAPIVersion: "v1.2", hasHubble: true, karpenterPresent: true, certManagerPresent: true, externalSecretsPresent: true, veleroPresent: true, admissionPresent: true, loadBalancingPresent: true, alertsPresent: true });
    const reach = new Set<string>();
    for (const g of NAV_GROUPS) {
      for (const d of g.destinations.filter((x) => x.type === "task")) {
        const route = { name: "cluster", cluster: "dev", section: d.target.kind === "section" ? d.target.section : "resources",
          ...(d.target.kind === "section" ? { argoView: d.target.argoView, fluxView: d.target.fluxView } : {}) } as Route;
        const m = model(c, route);
        for (const e of [...m.rest, ...m.needsYou]) {
          reach.add(e.destination.id);
          for (const s of e.subs) reach.add(s.destination.id);
          // a tool lens reaches each of its tasks through its part switch
          if (e.kind === "lens") for (const t of NAV_GROUPS.find((x) => x.id === e.groupId)!.destinations.filter((x) => x.type === "task")) reach.add(t.id);
        }
      }
    }
    const tasks = NAV_GROUPS.flatMap((g) => g.destinations.filter((d) => d.type === "task").map((d) => d.id));
    expect(tasks.filter((id) => !reach.has(id))).toEqual([]);
  });
});
