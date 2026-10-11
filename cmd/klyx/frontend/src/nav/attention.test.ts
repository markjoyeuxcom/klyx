import { describe, it, expect } from "vitest";
import type { AttentionSlice, AttentionCounts } from "../store/fleet";
import type { VerdictObservation } from "../health/verdict";
import { attentionAreas, attentionSummary, uncoveredDomains } from "./attention";

const obs = (fault = 0, degraded = 0, over: Partial<VerdictObservation> = {}): VerdictObservation =>
  ({ coverage: "complete", counts: { total: 5, fault, degraded, unknown: 0, progressing: 0, paused: 0, ok: 5 - fault - degraded, attention: fault + degraded }, ...over });
const slice = (over: Partial<AttentionCounts> = {}, extra: Partial<AttentionSlice> = {}): AttentionSlice => ({
  cluster: "dev", loading: false, at: 1,
  counts: { workloads: obs(), pods: obs(), events: 0, nodes: obs(), helm: 0, flux: 0, argo: 0, namespaces: 3, ...over },
  ...extra,
});
const byId = (s: AttentionSlice) => Object.fromEntries(attentionAreas(s, "dev").map((a) => [a.id, a]));

describe("cluster attention areas (redesign step 7e)", () => {
  it("no read yet, or another cluster's read, is loading - never quiet", () => {
    expect(attentionAreas(slice({}, { loading: true, at: null }), "dev").every((a) => a.state === "loading")).toBe(true);
    expect(attentionAreas(slice(), "prd").every((a) => a.state === "loading")).toBe(true);
    expect(attentionSummary(attentionAreas(null, "dev")).state).toBe("loading");
  });

  it("a re-poll in flight keeps the last good reading", () => {
    expect(byId(slice({ workloads: obs(2) }, { loading: true })).workloads.state).toBe("attention");
  });

  it("verdict areas: attention toned by the worst verdict; capped or unclassified is unverified", () => {
    const a = byId(slice({ workloads: obs(1, 1), pods: obs(0, 2), nodes: obs(0, 0, { coverage: "partial" }) }));
    expect(a.workloads).toMatchObject({ state: "attention", n: 2, tone: "danger" });
    expect(a.pods).toMatchObject({ state: "attention", n: 2, tone: "warning" });
    expect(a.nodes).toMatchObject({ state: "unverified", text: "unverified (capped)" });
  });

  it("warning events are evidence, never attention", () => {
    const a = byId(slice({ events: 9 }));
    expect(a.events).toMatchObject({ state: "evidence", n: 9, tone: undefined });
    expect(attentionSummary(attentionAreas(slice({ events: 9 }), "dev")).state).toBe("quiet");
  });

  it("absent tools are omitted; unreadable is never absent or quiet", () => {
    const areas = attentionAreas(slice({ argo: "absent", flux: "unreadable", helm: "unreadable" }), "dev");
    expect(areas.map((a) => a.id)).not.toContain("argo");
    expect(areas.find((a) => a.id === "flux")?.state).toBe("unreadable");
    expect(attentionSummary(areas)).toMatchObject({ state: "gaps", gaps: 2 });
  });

  it("the mast counts AREAS, not objects - a workload and its pods are two areas, not a sum", () => {
    const sum = attentionSummary(attentionAreas(slice({ workloads: obs(3), pods: obs(5), flux: 2 }), "dev"));
    expect(sum).toMatchObject({ state: "attention", areas: 3, tone: "danger", gaps: 0 });
  });

  it("coverage is independent of attention: a fault with capped or unknown rows is lit AND a gap", () => {
    const a = byId(slice({ pods: obs(1, 0, { coverage: "partial" }), workloads: { ...obs(2), counts: { ...obs(2).counts, unknown: 1 } } }));
    expect(a.pods).toMatchObject({ state: "attention", gap: true });
    expect(a.workloads).toMatchObject({ state: "attention", gap: true });
    expect(attentionSummary(attentionAreas(slice({ pods: obs(1, 0, { coverage: "partial" }) }), "dev"))).toMatchObject({ state: "attention", areas: 1, gaps: 1 });
  });

  it("a stale area keeps its reading, names its time and counts as a gap", () => {
    const at = new Date(2026, 9, 11, 9, 5).getTime();
    const a = byId(slice({ nodes: obs(1) }, { stale: ["nodes"], areaAt: { nodes: at } }));
    expect(a.nodes).toMatchObject({ state: "attention", stale: true, gap: true, at });
    expect(a.nodes.text).toContain("last good 09:05");
  });

  it("Helm counts stuck releases with failed ones, toned by the worst", () => {
    expect(byId(slice({ helm: 0, helmStuck: 2 })).helm).toMatchObject({ state: "attention", n: 2, tone: "warning", text: "2 appear stuck" });
    expect(byId(slice({ helm: 1, helmStuck: 1 })).helm).toMatchObject({ n: 2, tone: "danger", text: "1 failed · 1 appears stuck" });
  });

  it("installed domains outside the summary are named, absent ones are not", () => {
    const c = { name: "dev", state: "Synced", certManagerPresent: true, externalSecretsPresent: false, gatewayAPIVersion: "v1", karpenterPresent: false } as never;
    expect(uncoveredDomains(c).map((u) => u.label)).toEqual(["Certificates", "Gateways"]);
    expect(uncoveredDomains({ name: "dev", state: "Connecting", certManagerPresent: true } as never)).toEqual([]);
  });
});
