import { describe, it, expect, beforeEach } from "vitest";
import { useHealth, resetHealthStore, applySummary, type HealthPart, type HealthRef } from "../store/health";
import type { ClusterDTO } from "../store/fleet";
import { core, kind, summary, task, verdict } from "../test/healthFixtures";
import { clusterAttention, mastReading } from "../nav/clusterAttention";
import { navCounts } from "../nav/useNavCounts";
import { spineLamp } from "../nav/fleetAttention";
import { fleetQueue, type QueueRow } from "./queue";

// Coverage S8a §6: alerts in Needs you, counted apart from object faults;
// never lighting an area, the mast, the lamp or the spine.

const cl = (name: string, env = "dev"): ClusterDTO => ({ name, state: "Synced", reason: "", env, region: "westeurope", alertsPresent: true } as ClusterDTO);
const aref = (label: string, role: "critical" | "warning", over: Partial<HealthRef> = {}): HealthRef =>
  ({ cluster: "a", gvr: "v1/pods", namespace: "team", name: "api-1", role, label, fingerprint: `fp-${label}`, since: "2026-10-19T10:00:00Z", reason: `${label} summary`, ...over });
const alertsPart = (counted: number, critical: number, top: HealthRef[], over: Partial<HealthPart> = {}): HealthPart => ({
  id: "alerts", entity: "Alertmanager alerts", scope: "this cluster's alerts", classifier: "alerts", coverage: "live", kinds: [kind("alerts")],
  alerts: { counted, critical, warning: counted - critical, uncounted: 0, suppressed: 0, unprocessed: 0, unknown: 0, truncated: false }, top, obs: 1, ver: 1, ...over,
});
const seed = (cluster: string, part: HealthPart, podFaults = 0) => applySummary(summary([
  ...core().map((t) => (t.id === "pods" && podFaults > 0 ? task("pods", [verdict("pods", { fault: podFaults, ok: 1 }, [kind("pods")], { top: [aref("PodFault", "critical", { role: "fault", fingerprint: undefined, uid: "uid-p" })] })]) : t)),
  task("alerts", [part]),
], {}, cluster));
const queue = (clusters: ClusterDTO[], filter: "all" | "prd" | "faults" = "all") =>
  fleetQueue(clusters, Object.fromEntries(clusters.map((c) => [c.name, clusterAttention(useHealth.getState(), c.name, c)])), filter);
const brief = (r: QueueRow) => r.kind === "alert" ? `alert ${r.label} ${r.subject ?? "cluster"} ${r.tone}` : r.kind === "alert-more" ? `+${r.count} alerts (${r.critical} critical)` : r.kind === "object" ? `object ${r.label}` : r.kind;

beforeEach(() => resetHealthStore());

describe("alerts in Needs you (S8a §6)", () => {
  it("a third count apart from faults; rows after the objects; a +N more past the samples", () => {
    const top = [aref("C1", "critical"), aref("C2", "critical", { gvr: "", name: "", namespace: undefined }), aref("W1", "warning"), aref("W2", "warning"), aref("W3", "warning")];
    seed("a", alertsPart(8, 3, top), 1);
    const q = queue([cl("a")]);
    expect(q.alerts).toBe(8);
    expect(q.faults).toBe(1); // the pod's fault only: an alert is never a fault
    expect(q.rows.map(brief)).toEqual(["object PodFault", "alert C1 Pod team/api-1 danger", "alert C2 cluster danger", "alert W1 Pod team/api-1 warning",
      "alert W2 Pod team/api-1 warning", "alert W3 Pod team/api-1 warning", "+3 alerts (1 critical)"]);
  });

  it("the Faults filter keeps critical alerts, and a +N more keeps only its hidden critical ones", () => {
    seed("a", alertsPart(8, 3, [aref("C1", "critical"), aref("C2", "critical"), aref("W1", "warning"), aref("W2", "warning"), aref("W3", "warning")]));
    expect(queue([cl("a")], "faults").rows.map(brief)).toEqual(["alert C1 Pod team/api-1 danger", "alert C2 Pod team/api-1 danger", "+1 alerts (1 critical)"]);
  });

  it("alerts light no area, mast, lamp or spine; the lens badge is neutral", () => {
    seed("a", alertsPart(2, 2, [aref("C1", "critical"), aref("C2", "critical")]));
    const c = cl("a");
    const m = clusterAttention(useHealth.getState(), "a", c);
    const area = m.areas.find((x) => x.id === "alerts")!;
    expect(area.lines[0].state).toBe("alerts");
    expect(area.state).not.toBe("attention");
    expect(mastReading(m).areas).toBe(0);
    expect(spineLamp(c, m).kind).not.toBe("danger");
    const counts = navCounts(m);
    expect(counts.destination.alerts).toMatchObject({ n: 2, tone: undefined });
    expect(counts.attention.has("alerts")).toBe(false);
  });

  it("active alerts without a counted severity are listed in the lens, never counted", () => {
    seed("a", alertsPart(1, 1, [aref("C1", "critical")], { alerts: { counted: 1, critical: 1, warning: 0, uncounted: 4, suppressed: 2, unprocessed: 1, unknown: 0, truncated: false } }));
    expect(queue([cl("a")]).alerts).toBe(1);
    expect(clusterAttention(useHealth.getState(), "a", cl("a")).areas.find((x) => x.id === "alerts")!.lines[0].text).toBe("1 active alert");
  });

  it("a capped read counts at least; an absent part is no area and no count", () => {
    seed("a", alertsPart(3, 0, [aref("W1", "warning")], { kinds: [kind("alerts", { truncated: true })] }));
    const m = clusterAttention(useHealth.getState(), "a", cl("a"));
    const line = m.areas.find((x) => x.id === "alerts")!.lines[0];
    expect(line.bound).toBe("lower");
    expect(line.text).toBe("3+ active alerts");
    expect(queue([cl("a")]).alertsLower).toBe(true);
    resetHealthStore();
    seed("b", alertsPart(0, 0, [], { coverage: "absent", kinds: [kind("alerts", { coverage: "absent" })] }));
    const mb = clusterAttention(useHealth.getState(), "b", cl("b"));
    expect(mb.areas.some((x) => x.id === "alerts")).toBe(false);
    expect(queue([cl("b")]).alerts).toBe(0);
  });

  it("a failed read is a coverage row, and the last good alerts stay counted", () => {
    seed("a", alertsPart(2, 1, [aref("C1", "critical"), aref("W1", "warning")], { coverage: "stale", kinds: [kind("alerts", { coverage: "stale", lastGoodAt: "2026-10-19T12:02:00Z", class: "transport" })] }));
    const q = queue([cl("a")]);
    expect(q.alerts).toBe(2);
    expect(q.rows.some((r) => r.kind === "coverage" && r.text.includes("Alertmanager alerts last good"))).toBe(true);
  });
});
