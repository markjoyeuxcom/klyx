import { describe, it, expect, beforeEach } from "vitest";
import { useHealth, resetHealthStore, applySummary } from "../store/health";
import type { ClusterDTO } from "../store/fleet";
import { C, core, kind, native, summary, task } from "../test/healthFixtures";
import { clusterAttention, mastReading } from "./clusterAttention";
import { healthReading, spineLamp } from "./fleetAttention";
import { fleetAxes } from "./fleetAxes";
import { navCounts } from "./useNavCounts";
import { coverageOf } from "./lensIndex";
import { fleetQueue } from "../fleet/queue";

// Coverage S10 §5: a controller task's "managed" state (EKS Auto Mode, AKS
// node auto-provisioning) is a note on every consumer: never attention,
// never a gap. Unknown stays a gap and not ready stays a fault.

const cl = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name: C, state: "Synced", reason: "", karpenterPresent: true, ...over } as ClusterDTO);
const karpenter = (controller: { state: string; reason?: string }) => task("karpenter", [
  native("nodeclaims", { ok: 2 }, 0, "not ready or appear stuck", [kind("nodeclaims")]),
  native("nodepools", { ok: 1 }, 0, "not ready or failing to register", [kind("nodepools")]),
], { controller });
const model = () => clusterAttention(useHealth.getState(), C, cl());
const MANAGED = "NodeClass managed by EKS Auto Mode; controller health not directly observed";

beforeEach(() => resetHealthStore());

describe("a managed controller is a note everywhere", () => {
  it("the area: quiet, no gap, and the cluster reads healthy", () => {
    applySummary(summary([...core(), karpenter({ state: "managed", reason: MANAGED })]));
    const m = model();
    const a = m.areas.find((x) => x.id === "karpenter")!;
    expect(a).toMatchObject({ state: "quiet", gap: false, tone: undefined });
    expect(a.controller).toEqual({ state: "managed", reason: MANAGED });
    expect(mastReading(m)).toMatchObject({ state: "quiet", areas: 0, gaps: 0 });
    // the fleet lamp and axes: healthy, never attention or unverified
    expect(spineLamp(cl(), m).kind).toBe("healthy");
    const axes = fleetAxes([cl()], { [C]: m }).axes;
    expect(axes.fault.length + axes.warning.length + axes.unverified.length).toBe(0);
    // Needs you: no controller row
    expect(fleetQueue([cl()], { [C]: m }).rows).toHaveLength(0);
    // the fleet reading names it, last, as management
    expect(healthReading(m).slice(-1)[0]).toBe(`Karpenter: controller managed by the platform - ${MANAGED}`);
    // the lens index: a note in the row's title, never a coverage word or attention
    const counts = navCounts(m);
    expect(counts.attention.has("karpenter")).toBe(false);
    expect(counts.destinationNote.karpenter).toContain("controller managed by the platform");
    expect(coverageOf(counts.destinationNote.karpenter)).toBe("");
    expect(counts.groupNote.autoscaling ?? "").not.toContain("unverified");
  });

  it("unknown stays a gap and not ready a fault beside the same management", () => {
    applySummary(summary([...core(), karpenter({ state: "unknown", reason: `the Karpenter controllers could not all be read; ${MANAGED}` })]));
    let m = model();
    expect(m.areas.find((x) => x.id === "karpenter")).toMatchObject({ state: "gaps", gap: true });
    expect(spineLamp(cl(), m).kind).toBe("unverified");
    applySummary(summary([...core(), karpenter({ state: "not ready", reason: `the karpenter controller k/karpenter (AWS) is not ready; ${MANAGED}` })], { seq: 2 }));
    m = model();
    expect(m.areas.find((x) => x.id === "karpenter")).toMatchObject({ state: "attention", tone: "danger" });
    expect(fleetQueue([cl()], { [C]: m }).rows.map((r) => r.kind)).toEqual(["controller"]);
  });
});
