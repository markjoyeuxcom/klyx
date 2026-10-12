import { describe, it, expect, beforeEach, vi } from "vitest";
import { renderHook } from "@testing-library/react";
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
import { useNavCounts } from "./useNavCounts";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { addSub, applyRows, applySummary, openStarted, openSucceeded, resetHealthStore } from "../store/health";
import { C, core, evidence, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";

// The tree's counts (health 1c slice 4 §2.2): a group shows its strongest
// lit line, never a sum; a destination shows exactly its own part; a part
// without a destination of its own is a note, never a number.

const counts = () => renderHook(() => useNavCounts(C)).result.current;
const caps = (over: Partial<ClusterDTO>) => useFleet.setState({ clusters: [{ name: C, state: "Synced", ...over } as ClusterDTO] });

beforeEach(() => {
  resetHealthStore();
  caps({});
});

describe("useNavCounts on the cluster's attention model", () => {
  it("each destination badges its own part; the group the strongest line, named", () => {
    caps({ fluxPresent: true });
    applySummary(summary([...core(), task("flux", [
      native("reconciliations", { degraded: 1, ok: 4 }, 1, "not ready", [kind("kustomizations")]),
      native("sources", { fault: 3 }, 3, "not ready", [kind("gitrepositories")]),
    ])]));
    const n = counts();
    expect(n.destination.flux).toMatchObject({ n: 1, tone: "warning" });
    expect(n.destination["flux-sources"]).toMatchObject({ n: 3, tone: "danger" });
    expect(n.group.flux).toMatchObject({ n: 3, tone: "danger" });
    expect(n.group.flux.title).toMatch(/^Flux - Sources: 3 not ready - cluster summary as of /);
  });

  it("a lit part without a destination is a note on its task's, never a number; no generic resource destination is badged", () => {
    caps({ certManagerPresent: true });
    applySummary(summary([...core(), task("certs", [
      native("certificates", { ok: 4 }, 0, "not ready", [kind("certificates")]),
      native("issuers", { fault: 1 }, 1, "not ready", [kind("issuers"), kind("clusterissuers")]),
    ])]));
    const n = counts();
    expect(n.destination.certs).toBeUndefined();
    expect(n.destinationNote.certs).toContain("issuers: 1 not ready");
    expect(n.group.certs).toMatchObject({ n: 1, tone: "danger" });
    expect(Object.keys(n.destination).some((d) => ["issuers", "clusterissuers", "secretstores", "nodeclaims", "applicationsets"].includes(d))).toBe(false);
  });

  it("Karpenter badges its NodeClaims - the view the lens opens on", () => {
    caps({ karpenterPresent: true });
    applySummary(summary([...core(), task("karpenter", [
      native("nodepools", { ok: 1 }, 0, "not ready", [kind("nodepools")]),
      native("nodeclaims", { fault: 1, ok: 2 }, 1, "failing", [kind("nodeclaims")]),
    ])]));
    const n = counts();
    expect(n.destination.karpenter).toMatchObject({ n: 1, tone: "danger" });
    expect(n.destinationNote.karpenter ?? "").not.toContain("nodepools");
  });

  it("a controller not ready is a note, never a number; evidence is counted without tone and never lights a group", () => {
    caps({ argoPresent: true });
    applySummary(summary([...replace(core(), task("events", [evidence(7, [kind("events")])])),
      task("argo", [native("applications", { ok: 3 }, 0, "need attention", [kind("applications")]), native("applicationsets", { ok: 1 }, 0, "not healthy", [kind("applicationsets")])], { controller: { state: "not ready", reason: "argocd-application-controller 0/1" } })]));
    const n = counts();
    expect(n.group.argo).toBeUndefined();
    expect(n.groupNote.argo).toBe("controller not ready");
    expect(n.destinationNote.argo).toContain("controller not ready");
    expect(n.destination.events).toMatchObject({ n: 7, tone: undefined });
    expect(n.group.cluster).toBeUndefined();
  });

  it("rows newer than the summary are the count, captioned with their own time", () => {
    applySummary(summary(core()));
    addSub("p", C, "pods", "pods"); openStarted("p", "t"); openSucceeded("p", "t", 1);
    applyRows("p", { cluster: C, incarnation: 1, task: "pods", part: { ...verdict("pods", { fault: 2 }, [kind("pods")]), ver: 3, obs: 3 }, evaluatedAt: "2026-10-12T12:00:09Z", rows: [], full: true });
    expect(counts().destination.pods.title).toMatch(/- Pods lens rows as of /);
    expect(counts().destination.pods).toMatchObject({ n: 2, tone: "danger" });
  });

  it("after a Final every group says the observation ended", () => {
    applySummary(summary(core()));
    applySummary(summary([], { seq: 2, final: true }));
    const n = counts();
    expect(n.group).toEqual({});
    expect(n.groupNote.workloads).toBe("observation ended");
  });
});
