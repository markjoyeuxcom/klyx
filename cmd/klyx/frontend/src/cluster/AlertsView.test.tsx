import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { LensRows } from "../health/useRows";
import type { HealthPart, KindCoverage } from "../store/health";
import type { AMAlertDTO } from "./alertsModel";
import { withClasses } from "../test/alertFixtures";

let rows: LensRows<AMAlertDTO>;
const opened: (string | null)[] = [];
vi.mock("../health/useRows", () => ({ useRows: (cluster: string | null) => { opened.push(cluster); return rows; } }));
const { AlertsView, COLUMNS } = await import("./AlertsView");
const { fitColumns } = await import("../layout/columns");
const { useInspectorAlerts } = await import("./AlertsBlock");

const kindCov = (over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: "alerts", role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const part = (over: Partial<HealthPart> = {}): HealthPart => ({ id: "alerts", entity: "Alertmanager alerts", scope: "", classifier: "alerts", coverage: "live", kinds: [kindCov()],
  alerts: { counted: 2, critical: 1, warning: 1, uncounted: 1, suppressed: 2, unprocessed: 0, unknown: 0, truncated: false }, obs: 1, ver: 1, ...over });
const set = (list: AMAlertDTO[] | null, p: HealthPart | null = part(), facts: Record<string, unknown> | null = { alertsSource: "https://am.example:443", alertsTruncated: false, alertsReadAt: "2026-10-19T12:00:00Z", alertsMatch: { cluster: "kind-klyx" } }) => {
  rows = { rows: list, ver: list ? { incarnation: 1, ver: 1 } : null, facts, part: p, evaluatedAt: null, error: null, refreshError: null,
    counts: { part: p, source: "rows", updating: false, refreshing: false, at: null, resync: false, refreshError: null } };
};
const al = (name: string, over: Partial<AMAlertDTO> = {}): AMAlertDTO => withClasses({ fingerprint: `fp-${name}`, name, severity: "critical", counted: true, state: "active", labels: [{ name: "alertname", value: name }],
  annotations: [], subject: { kind: "Pod", namespace: "team", name: "api-1", uid: "pod-uid", state: "found", basis: ["namespace", "pod"] }, summary: `${name} summary`, startsAt: "2026-10-19T11:00:00Z", ...over });
const rowNames = () => [...document.querySelectorAll(".lens-row")].map((r) => r.getAttribute("data-testid"));

beforeEach(() => {
  useFleet.setState({ clusters: [{ name: "c", state: "Synced", alertsPresent: true } as never], navIntent: null } as never);
});

describe("AlertsView (coverage S8a §7)", () => {
  it("active first, then suppressed with its cause; the source line and the counts", () => {
    set([
      al("Silenced", { state: "suppressed", counted: false, silencedBy: ["3d550c37-ab97"] }),
      al("Warn", { severity: "warning" }),
      al("Crit"),
      al("Watchdog", { severity: "none", counted: false, subject: {} }),
      al("Inhibited", { state: "suppressed", counted: false, severity: "warning", inhibitedBy: ["fp-Crit"] }),
    ]);
    render(<AlertsView cluster="c" />);
    expect(rowNames()).toEqual(["alert-row-Crit", "alert-row-Warn", "alert-row-Watchdog", "alert-row-Silenced", "alert-row-Inhibited"]);
    expect(screen.getByTestId("alert-row-Inhibited").textContent).toContain("inhibited by Crit");
    expect(screen.getByTestId("alert-row-Watchdog").textContent).toContain("the cluster");
    expect(screen.getByTestId("alerts-source").textContent).toContain("https://am.example:443");
    expect(screen.getByTestId("alerts-summary").textContent).toContain("2 active critical or warning");
    expect(screen.getByTestId("alerts-summary").textContent).toContain("1 active without a counted severity");
  });

  it("no Alertmanager is said as such; an unreadable first read is not an empty list", () => {
    set([], part({ coverage: "absent", kinds: [kindCov({ coverage: "absent" })] }), null);
    const { unmount } = render(<AlertsView cluster="c" />);
    expect(screen.getByText("No Alertmanager is configured or found on this cluster.")).toBeTruthy();
    unmount();
    set(null, part({ coverage: "unreadable", kinds: [kindCov({ coverage: "unreadable", error: "forbidden: services/proxy" })] }), null);
    render(<AlertsView cluster="c" />);
    expect(screen.getByText("Could not read alerts.")).toBeTruthy();
    expect(screen.getByText("forbidden: services/proxy")).toBeTruthy();
  });

  it("a capped read says every count is at least; a stale read keeps the list and never says resolved", () => {
    set([al("Crit")], part({ kinds: [kindCov({ coverage: "stale", lastGoodAt: "2026-10-19T12:02:00Z", error: "timeout" })] }), { alertsSource: "s", alertsTruncated: true });
    render(<AlertsView cluster="c" />);
    expect(screen.getByTestId("alerts-capped").textContent).toContain("every count is at least");
    expect(document.body.textContent).toContain("showing the last good read");
    expect(document.body.textContent).not.toMatch(/resolved(?! )/);
    expect(rowNames()).toEqual(["alert-row-Crit"]);
  });

  it("an alert whose last reported end has passed, across failing reads, is last seen active - never ending or resolved", () => {
    set([al("Crit", { endsAt: "2020-01-01T00:00:00Z", updatedAt: "2019-12-31T23:00:00Z" })],
      part({ kinds: [kindCov({ coverage: "stale", lastGoodAt: "2026-10-19T12:02:00Z", error: "timeout" })] }),
      { alertsSource: "s", alertsTruncated: false, alertsReadAt: "2026-10-19T12:02:00Z" });
    render(<AlertsView cluster="c" />);
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "c", task: "alerts", part: "alerts", ref: { gvr: "v1/pods", name: "api-1", fingerprint: "k-fp-Crit" } }); });
    const panel = screen.getByTestId("alert-panel");
    expect(panel.textContent).toMatch(/last seen active at .+ \(reads failing since\)/);
    expect(screen.getByTestId("alert-row-Crit").textContent).toMatch(/^critical.*active/);
    expect(document.body.textContent).not.toMatch(/resolved(?! )|ending|ended|expired/i);
  });

  it("the State column is the reading: kept at every width, the others dropped first", () => {
    for (const w of [360, 520, 640, 800]) expect([...fitColumns(COLUMNS, w).visible], `at ${w}px`).toContain("state");
    const narrow = fitColumns(COLUMNS, 640).visible;
    expect(narrow.has("summary")).toBe(false);
  });

  it("a reveal selects the alert by fingerprint and the inspector links its subject", () => {
    set([al("Crit"), al("Other", { fingerprint: "fp-x" })]);
    render(<AlertsView cluster="c" />);
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "c", task: "alerts", part: "alerts", ref: { gvr: "v1/pods", name: "api-1", fingerprint: "k-fp-Crit" } }); });
    const panel = screen.getByTestId("alert-panel");
    expect(within(panel).getByText("Open Pod")).toBeTruthy();
    expect(panel.textContent).toContain("matched by label namespace/pod");
  });
});

function Block({ target }: { target: Parameters<typeof useInspectorAlerts>[1] }) {
  return <div data-testid="block">{useInspectorAlerts("c", target)}</div>;
}

// Codex review of 71c181c: classes from the raw alert, failures always
// said, selection by the alert's key with a source change ending it.
describe("AlertsView after the implementation review", () => {
  it("classes, not display text, decide the group and the words", () => {
    // a credential equal to "active" redacts the display state; the class holds
    set([al("Crit", { state: "[redacted]", stateClass: "active" }), al("Sup", { state: "suppressed", counted: false, silencedBy: ["s1"] })]);
    render(<AlertsView cluster="c" />);
    expect(rowNames()).toEqual(["alert-row-Crit", "alert-row-Sup"]);
    expect(screen.getByTestId("alert-row-Crit").textContent).toMatch(/active/);
    expect(screen.getByTestId("alert-row-Crit").getAttribute("data-state")).toBe("active");
  });

  it("a failed poll is said after a previously empty read, and a rows failure is no endless skeleton", () => {
    set([], part({ kinds: [kindCov({ coverage: "stale", lastGoodAt: "2026-10-19T12:00:00Z", error: "read timed out" })] }));
    const { unmount } = render(<AlertsView cluster="c" />);
    expect(document.body.textContent).toContain("read failed - showing the last good read");
    expect(document.body.textContent).toContain("No alerts in the last good read.");
    unmount();
    set(null);
    rows.error = "rows subscription was forbidden";
    render(<AlertsView cluster="c" />);
    expect(screen.getByText("Could not read alerts.")).toBeTruthy();
    expect(screen.getByText("rows subscription was forbidden")).toBeTruthy();
  });

  it("rows behind the cluster after a failed re-read are said as of their evaluation, never live", () => {
    set([al("Crit")]);
    rows.evaluatedAt = "2026-10-19T12:00:00Z";
    rows.refreshError = "rows re-bootstrap failed";
    rows.counts = { ...rows.counts, refreshing: true, refreshError: "rows re-bootstrap failed", source: "summary" };
    render(<AlertsView cluster="c" />);
    expect(screen.getByTestId("alerts-rows-behind").textContent).toContain("Last re-read failed: rows re-bootstrap failed");
    expect(screen.getByTestId("alerts-source").textContent).toContain("rows as of");
    expect(screen.getByTestId("alerts-source").textContent).not.toContain("live");
  });

  it("while the rows are behind, the headline counts and their cap are the summary's, and an empty set is not a current 'no alerts'", () => {
    const old = { counted: 1, critical: 1, warning: 0, uncounted: 0, suppressed: 0, unprocessed: 0, unknown: 0, truncated: false };
    const behind = (list: AMAlertDTO[]) => {
      set(list, part({ alerts: old }));
      rows.refreshError = "rows re-bootstrap failed";
      rows.counts = { ...rows.counts, source: "summary", refreshing: true, refreshError: "rows re-bootstrap failed",
        part: part({ ver: 2, kinds: [kindCov({ truncated: true })], alerts: { ...old, counted: 7, critical: 7, truncated: true } }) };
    };
    behind([al("Crit")]);
    const { unmount } = render(<AlertsView cluster="c" />);
    expect(screen.getByTestId("alerts-summary").textContent).toContain("7+ active critical or warning");
    unmount();
    behind([]);
    render(<AlertsView cluster="c" />);
    expect(screen.queryByText("No alerts on this Alertmanager for this cluster.")).toBeNull();
    expect(screen.getByText("No alerts in the rows held; they are behind the cluster summary.")).toBeTruthy();
    expect(document.body.textContent).toContain("The cluster summary counts 7+ active critical or warning alerts");
  });

  it("a historical 'no Alertmanager' from rows behind the cluster never hides a newer summary's alerts", () => {
    const absentPart = part({ coverage: "absent", kinds: [kindCov({ coverage: "absent", objects: 0 })] });
    set([], absentPart, null);
    rows.refreshError = "rows re-bootstrap failed";
    rows.counts = { ...rows.counts, source: "summary", refreshing: true, refreshError: "rows re-bootstrap failed",
      part: part({ ver: 2, alerts: { counted: 7, critical: 7, warning: 0, uncounted: 0, suppressed: 0, unprocessed: 0, unknown: 0, truncated: false } }) };
    const { unmount } = render(<AlertsView cluster="c" />);
    expect(screen.queryByText("No Alertmanager is configured or found on this cluster.")).toBeNull();
    expect(screen.getByTestId("alerts-summary").textContent).toContain("7 active critical or warning");
    expect(screen.getByText("No alerts in the rows held; they are behind the cluster summary.")).toBeTruthy();
    unmount();
    // a current absence, from the selected counts, is said
    set([], absentPart, null);
    render(<AlertsView cluster="c" />);
    expect(screen.getByText("No Alertmanager is configured or found on this cluster.")).toBeTruthy();
  });

  it("a re-keyed generation not read yet ends the selection too", () => {
    const a = al("Alert-A", { key: "A/fp" });
    set([a], part(), { alertsSource: "source A", alertsSourceKey: "A", alertsTruncated: false });
    const view = render(<AlertsView cluster="c" />);
    fireEvent.click(screen.getByTestId("alert-row-Alert-A"));
    expect(screen.getByTestId("alert-panel")).toBeTruthy();
    set([], part({ ver: 2, kinds: [kindCov({ coverage: "checking", objects: 0 })] }), null); // source B, checking, no facts
    view.rerender(<AlertsView cluster="c" />);
    set([a], part({ ver: 3 }), { alertsSource: "source A", alertsSourceKey: "A", alertsTruncated: false });
    view.rerender(<AlertsView cluster="c" />);
    expect(screen.queryByTestId("alert-panel")).toBeNull();
  });

  it("a source change ends the selection; the same fingerprint on another source is never shown under it", () => {
    const a = al("Alert-A", { fingerprint: "same-fp", key: "A/same-fp" });
    set([a], part(), { alertsSource: "source A", alertsSourceKey: "A", alertsTruncated: false });
    const view = render(<AlertsView cluster="c" />);
    fireEvent.click(screen.getByTestId("alert-row-Alert-A"));
    expect(screen.getByTestId("alert-panel").textContent).toContain("Alert-A");
    set([al("Alert-B", { fingerprint: "same-fp", key: "B/same-fp" })], part({ ver: 2 }), { alertsSource: "source B", alertsSourceKey: "B", alertsTruncated: false });
    view.rerender(<AlertsView cluster="c" />);
    expect(screen.queryByTestId("alert-panel")).toBeNull();
    // the selection ended: the first source back does not reopen it
    set([a], part({ ver: 3 }), { alertsSource: "source A", alertsSourceKey: "A", alertsTruncated: false });
    view.rerender(<AlertsView cluster="c" />);
    expect(screen.queryByTestId("alert-panel")).toBeNull();
    set([al("Alert-B", { fingerprint: "same-fp", key: "B/same-fp" })], part({ ver: 4 }), { alertsSource: "source B", alertsSourceKey: "B", alertsTruncated: false });
    view.rerender(<AlertsView cluster="c" />);
    // a reveal of the old source's key finds nothing
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "c", task: "alerts", part: "alerts", ref: { gvr: "", name: "Alert-A", fingerprint: "A/same-fp" } }); });
    expect(screen.queryByTestId("alert-panel")).toBeNull();
  });
});

describe("the inspector's Alerts block (S8a §7)", () => {
  it("renders nothing without an Alertmanager; lists the object's alerts and its pods' apart", () => {
    set([], part({ coverage: "absent", kinds: [kindCov({ coverage: "absent" })] }), null);
    const { unmount } = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("block").textContent).toBe("");
    unmount();
    set([al("OnDep", { subject: { kind: "Deployment", namespace: "team", name: "api", uid: "dep-uid", state: "found" } }), al("OnPod")]);
    render(<Block target={{ uid: "dep-uid", kind: "Deployment", namespace: "team", name: "api", podUIDs: ["pod-uid"] }} />);
    expect(screen.getByTestId("insp-alert-OnDep")).toBeTruthy();
    expect(screen.getByText("on its pods")).toBeTruthy();
    expect(screen.getByTestId("insp-alert-OnPod")).toBeTruthy();
  });

  it("a cluster without an Alertmanager opens no alerts rows at all", () => {
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", alertsPresent: false } as never] } as never);
    set([al("OnPod")]);
    opened.length = 0;
    render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(opened.every((c) => c === null)).toBe(true);
    expect(screen.getByTestId("block").textContent).toBe("");
  });

  it("no claim when the subject join or a workload's pods are incomplete; by-name alerts are kept and qualified", () => {
    const podsLive = kindCov({ kind: "pods", role: "dependency" });
    const podsCapped = kindCov({ kind: "pods", role: "dependency", truncated: true });
    // a pod omitted by the pod budget: its alert is by name, not established
    set([al("Named", { subject: { kind: "Pod", namespace: "team", name: "api-1", state: "not established" } })], part({ kinds: [kindCov(), podsCapped] }));
    let v = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alert-Named").textContent).toContain("by name only (not established)");
    v.unmount();
    // nothing correlated, but pods capped: never "No alert matches"
    set([], part({ kinds: [kindCov(), podsCapped] }));
    v = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toContain("not established: pods are not read live and complete");
    v.unmount();
    // a workload whose pod set is not complete
    set([], part({ kinds: [kindCov(), podsLive, kindCov({ kind: "deployments", role: "dependency" })] }));
    v = render(<Block target={{ uid: "dep-uid", kind: "Deployment", namespace: "team", name: "api", podUIDs: [], podsComplete: false }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toContain("its pods are not read live and complete");
    v.unmount();
    // an alert naming an object of this kind that Klyx cannot show exactly
    // (redacted or clipped - the backend's exact flag, never the marker
    // text) blocks the claim; one of another kind does not
    const hidden = al("Hidden", { subject: { kind: "Job", namespace: "team", name: "backup-[re…", state: "not looked up", exact: false } });
    set([hidden], part({ kinds: [kindCov(), podsLive] }));
    v = render(<Block target={{ uid: "job-uid", kind: "Job", namespace: "team", name: "backup-xyz" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toContain("an alert names an object Klyx cannot show exactly");
    v.unmount();
    v = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toBe("No alert matches this object.");
    v.unmount();
    // a subject shown exactly is compared; one not shown exactly never is
    set([al("Exact", { subject: { kind: "Job", namespace: "team", name: "backup-xyz", state: "not looked up", exact: false } })], part({ kinds: [kindCov(), podsLive] }));
    v = render(<Block target={{ uid: "job-uid", kind: "Job", namespace: "team", name: "backup-xyz" }} />);
    expect(screen.queryByTestId("insp-alert-Exact")).toBeNull();
    v.unmount();
    // a found pod whose namespace is not shown exactly: its UID resolves the
    // pod, never its namespace, so a namespace cannot rule it out
    set([al("NsHidden", { subject: { kind: "Pod", namespace: "[redacted]", name: "api-1", uid: "pod-uid", state: "found", exact: false } })],
      part({ kinds: [kindCov(), podsLive, kindCov({ kind: "namespaces", role: "dependency" })] }));
    v = render(<Block target={{ uid: "ns-uid", kind: "Namespace", name: "team" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toContain("an alert names an object Klyx cannot show exactly");
    v.unmount();
    v = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alert-NsHidden")).toBeTruthy(); // the pod itself still matches by UID
    v.unmount();
    // a last-observed subject is said as such, on the object and on its pods
    set([al("Last", { subject: { kind: "Pod", namespace: "team", name: "api-1", uid: "pod-uid", state: "last observed" } })], part({ kinds: [kindCov(), podsLive] }));
    v = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alert-Last").textContent).toContain("the subject as last observed");
    v.unmount();
    v = render(<Block target={{ uid: "dep-uid", kind: "Deployment", namespace: "team", name: "api", podUIDs: ["pod-uid"], podsComplete: true }} />);
    expect(screen.getByTestId("insp-alert-Last").textContent).toContain("the subject as last observed");
    v.unmount();
    // rows behind the cluster after a failed re-read: named, never "live",
    // and no negative claim
    set([], part({ kinds: [kindCov(), podsLive] }));
    rows.refreshError = "rows re-bootstrap failed";
    rows.counts = { ...rows.counts, refreshing: true, refreshError: "rows re-bootstrap failed", source: "summary" };
    v = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toContain("not established: the last re-read of the alerts rows failed");
    expect(screen.getByTestId("insp-alerts-failure").textContent).toContain("rows re-bootstrap failed");
    expect(screen.getByTestId("block").textContent).not.toContain("live");
    v.unmount();
    // a failed read is said in the block
    set([], part({ kinds: [kindCov({ coverage: "stale", lastGoodAt: "2026-10-19T12:00:00Z", error: "read timed out" }), podsLive] }));
    render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alerts-failure").textContent).toContain("read timed out");
  });

  it("'No alert' only from a live, complete read", () => {
    set([al("Elsewhere", { subject: { kind: "Pod", namespace: "x", name: "y", uid: "other", state: "found" } })], part({ kinds: [kindCov(), kindCov({ kind: "pods", role: "dependency" })] }));
    const { unmount } = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toBe("No alert matches this object.");
    unmount();
    set([], part({ kinds: [kindCov({ truncated: true })] }), { alertsSource: "s", alertsTruncated: true });
    render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toContain("not established");
  });
});

// v0.15.1: four Watchdogs from a shared Alertmanager read as four, told apart
describe("same-name alerts on a shared Alertmanager (v0.15.1)", () => {
  const wd = (cl: string) => al("Watchdog", { fingerprint: `fp-${cl}`, key: `k-${cl}`, severity: "none", counted: false, subject: {},
    labels: [{ name: "alertname", value: "Watchdog" }, { name: "cluster", value: cl }, { name: "severity", value: "none" }] });
  it("each row says the label that tells it apart, and no match is pointed at fleet.yaml", () => {
    set([wd("k8s-b1"), wd("k8s-b2"), wd("k8s-b3"), wd("k8s-b4")], part(), { alertsSource: "monitoring/am", alertsTruncated: false, alertsReadAt: "2026-10-21T12:00:00Z" });
    render(<AlertsView cluster="c" />);
    expect(screen.getAllByTestId("alert-distinct").map((e) => e.textContent).sort()).toEqual([" · cluster=k8s-b1", " · cluster=k8s-b2", " · cluster=k8s-b3", " · cluster=k8s-b4"]);
    expect(screen.getByTestId("alerts-shared").textContent).toContain("These alerts carry 4 values of the cluster label (k8s-b1, k8s-b2, k8s-b3, k8s-b4)");
    expect(screen.getByTestId("alerts-shared").textContent).toContain("alertmanager.match");
  });
  it("with this cluster's match set, nothing is suggested", () => {
    set([wd("k8s-b1"), wd("k8s-b3")]);
    render(<AlertsView cluster="c" />);
    expect(screen.queryByTestId("alerts-shared")).toBeNull();
    expect(screen.getAllByTestId("alert-distinct").length).toBe(2);
  });
});
