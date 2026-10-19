import { describe, it, expect } from "vitest";
import { alertsFacts, alertsFor, causeText, sortAlerts, subjectNote, subjectText, type AMAlertDTO } from "./alertsModel";

const al = (name: string, over: Partial<AMAlertDTO> = {}): AMAlertDTO => ({
  fingerprint: `fp-${name}`, name, severity: "critical", counted: true, state: "active", labels: [], annotations: [], subject: {}, startsAt: "2026-10-19T10:00:00Z", ...over,
});

describe("alerts model (coverage S8a §7)", () => {
  it("orders by state group, then severity, then the longest firing", () => {
    const xs = [
      al("Sup", { state: "suppressed" }), al("Warn", { severity: "warning" }), al("Unp", { state: "unprocessed" }),
      al("CritNew", { startsAt: "2026-10-19T11:00:00Z" }), al("CritOld", { startsAt: "2026-10-19T09:00:00Z" }), al("Odd", { state: "pending-upstream" }),
    ];
    expect(sortAlerts(xs).map((a) => a.name)).toEqual(["CritOld", "CritNew", "Warn", "Unp", "Sup", "Odd"]);
  });

  it("says why an alert is not plainly active, and never calls one resolved", () => {
    const crit = al("KlyxLabCritical", { fingerprint: "0a05fbb640de8506" });
    const by = new Map([[crit.fingerprint, crit]]);
    expect(causeText(al("S", { state: "suppressed", silencedBy: ["3d550c37-ab97-4824"] }))).toBe("silenced (silence 3d550c37…)");
    expect(causeText(al("I", { state: "suppressed", inhibitedBy: ["0a05fbb640de8506"] }), by)).toBe("inhibited by KlyxLabCritical (0a05fbb6…)");
    expect(causeText(al("M", { state: "suppressed", mutedBy: ["always"] }))).toBe("muted by time interval always");
    expect(causeText(al("P", { mute: { kind: "partial", mutedFor: ["quiet"], activeFor: ["lab"] } }))).toBe("muted for quiet; active for lab");
    expect(causeText(al("X", { mute: { kind: "incomplete", expected: ["a", "b"], shown: ["a"] } }))).toBe("mute evidence incomplete: expected a, b; groups show a");
    expect(causeText(al("U", { state: "unprocessed" }))).toBe("not yet processed by Alertmanager");
    expect(causeText(al("O", { state: "pending-upstream" }))).toContain("not one Klyx knows");
    expect(causeText(al("D", { disagreement: "copies disagreed: active and suppressed" }))).toBe("copies disagreed: active and suppressed");
    for (const a of [al("S", { state: "suppressed" }), al("A")]) expect(causeText(a)).not.toMatch(/resolved/);
  });

  it("names the subject and what Klyx can say about it", () => {
    const s = { kind: "Pod", namespace: "team", name: "api-1", basis: ["namespace", "pod"] };
    expect(subjectText({})).toBe("the cluster");
    expect(subjectText(s)).toBe("Pod team/api-1");
    expect(subjectNote({ ...s, state: "found" })).toBe("matched by label namespace/pod");
    expect(subjectNote({ ...s, state: "found", startedBeforeCreated: true })).toContain("started before this object was created");
    expect(subjectNote({ ...s, state: "not found" })).toContain("not found in this cluster now");
    expect(subjectNote({ ...s, state: "not established" })).toContain("not established");
    expect(subjectNote({ ...s, state: "not looked up" })).toContain("named, not looked up");
    expect(subjectNote({})).toContain("cluster alert");
  });

  it("correlates by UID, by name for an unheld kind, by namespace for a namespace, and a workload's pods apart", () => {
    const xs = [
      al("PodA", { subject: { kind: "Pod", namespace: "team", name: "api-1", uid: "pod-uid", state: "found" } }),
      al("Dep", { subject: { kind: "Deployment", namespace: "team", name: "api", uid: "dep-uid", state: "found" } }),
      al("Job", { subject: { kind: "Job", namespace: "team", name: "backup-1", state: "not looked up" } }),
      al("JobGone", { subject: { kind: "Job", namespace: "team", name: "backup-1", state: "not found" } }),
      al("NsOnly", { subject: { kind: "Namespace", name: "team", state: "found" } }),
      al("Elsewhere", { subject: { kind: "Pod", namespace: "other", name: "x", uid: "x-uid", state: "found" } }),
    ];
    expect(alertsFor(xs, { uid: "dep-uid", kind: "Deployment", namespace: "team", name: "api", podUIDs: ["pod-uid"] })).toEqual({ own: [xs[1]], onPods: [xs[0]] });
    expect(alertsFor(xs, { uid: "job-uid", kind: "Job", namespace: "team", name: "backup-1" }).own.map((a) => a.name)).toEqual(["Job"]);
    expect(alertsFor(xs, { uid: "ns-uid", kind: "Namespace", name: "team" }).own.map((a) => a.name).sort()).toEqual(["Dep", "Job", "JobGone", "NsOnly", "PodA"]);
    expect(alertsFor(xs, { uid: "other-uid", kind: "Pod", namespace: "team", name: "api-1" }).own).toEqual([]);
  });

  it("reads the source line from the rows' facts", () => {
    expect(alertsFacts(null)).toBeNull();
    expect(alertsFacts({ alertsSource: "https://am.example:443", alertsTruncated: true, alertsMatch: { cluster: "prd" }, alertsReadAt: "2026-10-19T12:00:00Z" }))
      .toEqual({ source: "https://am.example:443", truncated: true, match: { cluster: "prd" }, readAt: "2026-10-19T12:00:00Z", note: undefined });
  });
});
