import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { LensRows } from "../health/useRows";
import type { HealthPart, KindCoverage } from "../store/health";
import type { AMAlertDTO } from "./alertsModel";

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
const al = (name: string, over: Partial<AMAlertDTO> = {}): AMAlertDTO => ({ fingerprint: `fp-${name}`, name, severity: "critical", counted: true, state: "active", labels: [{ name: "alertname", value: name }],
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
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "c", task: "alerts", part: "alerts", ref: { gvr: "v1/pods", name: "api-1", fingerprint: "fp-Crit" } }); });
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
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "c", task: "alerts", part: "alerts", ref: { gvr: "v1/pods", name: "api-1", fingerprint: "fp-Crit" } }); });
    const panel = screen.getByTestId("alert-panel");
    expect(within(panel).getByText("Open Pod")).toBeTruthy();
    expect(panel.textContent).toContain("matched by label namespace/pod");
  });
});

function Block({ target }: { target: Parameters<typeof useInspectorAlerts>[1] }) {
  return <div data-testid="block">{useInspectorAlerts("c", target)}</div>;
}

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

  it("'No alert' only from a live, complete read", () => {
    set([al("Elsewhere", { subject: { kind: "Pod", namespace: "x", name: "y", uid: "other", state: "found" } })]);
    const { unmount } = render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toBe("No alert matches this object.");
    unmount();
    set([], part({ kinds: [kindCov({ truncated: true })] }), { alertsSource: "s", alertsTruncated: true });
    render(<Block target={{ uid: "pod-uid", kind: "Pod", namespace: "team", name: "api-1" }} />);
    expect(screen.getByTestId("insp-alerts-none").textContent).toContain("not established");
  });
});
