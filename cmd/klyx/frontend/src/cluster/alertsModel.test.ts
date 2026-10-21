import { describe, it, expect } from "vitest";
import { withClasses } from "../test/alertFixtures";
import { alertsFacts, alertsFor, causeText, clusterLabelSpread, distinguishingLabels, sortAlerts, subjectNote, subjectText, type AMAlertDTO } from "./alertsModel";

const al = (name: string, over: Partial<AMAlertDTO> = {}): AMAlertDTO => withClasses({
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
    expect(alertsFor(xs, { uid: "dep-uid", kind: "Deployment", namespace: "team", name: "api", podUIDs: ["pod-uid"] })).toEqual({ own: [xs[1]], onPods: [xs[0]], byNameOnly: new Set() });
    const job = alertsFor(xs, { uid: "job-uid", kind: "Job", namespace: "team", name: "backup-1" });
    expect(job.own.map((a) => a.name)).toEqual(["Job"]);
    expect([...job.byNameOnly]).toEqual([xs[2].key]); // qualified: by name only
    // a held kind's subject Klyx could not establish is kept, by name only
    const unest = al("Unest", { subject: { kind: "Pod", namespace: "team", name: "api-1", state: "not established" } });
    const pod = alertsFor([...xs, unest], { uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" });
    expect(pod.own.map((a) => a.name).sort()).toEqual(["PodA", "Unest"]);
    expect([...pod.byNameOnly]).toEqual([unest.key]);
    expect(alertsFor(xs, { uid: "ns-uid", kind: "Namespace", name: "team" }).own.map((a) => a.name).sort()).toEqual(["Dep", "Job", "JobGone", "NsOnly", "PodA"]);
    expect(alertsFor(xs, { uid: "other-uid", kind: "Pod", namespace: "team", name: "api-1" }).own).toEqual([]);
  });

  it("reads the source line from the rows' facts", () => {
    expect(alertsFacts(null)).toBeNull();
    expect(alertsFacts({ alertsSource: "https://am.example:443", alertsSourceKey: "a1b2", alertsTruncated: true, alertsMatch: { cluster: "prd" } }))
      .toEqual({ source: "https://am.example:443", sourceKey: "a1b2", truncated: true, match: { cluster: "prd" } });
  });
});

// v0.15.1: same-name alerts told apart; a shared Alertmanager suggested
describe("alerts that share a name (v0.15.1)", () => {
  const wd = (fp: string, labels: Record<string, string>, more = 0): AMAlertDTO => withClasses({ fingerprint: fp, name: "Watchdog", severity: "none", counted: false, state: "active",
    labels: Object.entries({ alertname: "Watchdog", ...labels }).map(([name, value]) => ({ name, value })), labelsMore: more, annotations: [], subject: {} });
  it("names the labels that differ, where-from labels first, at most two", () => {
    const xs = [wd("a", { cluster: "k8s-b1", prometheus: "mon/p", zz: "1" }), wd("b", { cluster: "k8s-b3", prometheus: "mon/p", zz: "2" }), wd("c", { cluster: "k8s-b3", prometheus: "mon/q", zz: "3" }),
      withClasses({ fingerprint: "d", name: "Alone", state: "active", counted: true, labels: [{ name: "alertname", value: "Alone" }], annotations: [], subject: {} })];
    const d = distinguishingLabels(xs);
    expect(d.get("k-a")).toBe("cluster=k8s-b1, prometheus=mon/p");
    expect(d.get("k-c")).toBe("cluster=k8s-b3, prometheus=mon/q");
    expect(d.has("k-d")).toBe(false);
    // a label one alert lacks reads (none)
    expect(distinguishingLabels([wd("e", { cluster: "x" }), wd("f", {})]).get("k-f")).toBe("cluster=(none)");
    // nothing kept differs, a label was cut: said as such
    expect(distinguishingLabels([wd("g", { cluster: "x" }, 2), wd("h", { cluster: "x" })]).get("k-g")).toBe("differs in a label not shown");
  });
  it("several values of a cluster label with no match suggest a shared Alertmanager", () => {
    const xs = [wd("a", { cluster: "k8s-b1" }), wd("b", { cluster: "k8s-b3" })];
    expect(clusterLabelSpread(xs)).toEqual({ label: "cluster", values: ["k8s-b1", "k8s-b3"] });
    expect(clusterLabelSpread(xs, {})).toEqual({ label: "cluster", values: ["k8s-b1", "k8s-b3"] });
    expect(clusterLabelSpread(xs, { cluster: "k8s-b3" })).toBeNull();
    expect(clusterLabelSpread([wd("a", { cluster: "k8s-b1" }), wd("b", { cluster: "k8s-b1" })])).toBeNull();
    expect(clusterLabelSpread([wd("a", { tenant: "t1" }), wd("b", { tenant: "t2" })])).toEqual({ label: "tenant", values: ["t1", "t2"] });
  });
});
