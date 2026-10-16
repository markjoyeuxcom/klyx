import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor, screen } from "@testing-library/react";
import { Overview } from "./Overview";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, MetricsDTO } from "../store/fleet";
import { applySummary, resetHealthStore, type HealthRef } from "../store/health";
import { absentPart, core, dep, evidence, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";

// Default: sparklines unavailable so tests render without them.
import type { SparklinesDTO } from "../bridge/metrics";
const mockGetClusterSparklines = vi.fn<(cluster: string) => Promise<SparklinesDTO>>(() =>
  Promise.resolve({ available: false, message: "metrics unavailable", cpu: [], mem: [] }),
);
vi.mock("../bridge/metrics", () => ({
  getClusterMetrics: vi.fn(),
  getClusterSparklines: (cluster: string) => mockGetClusterSparklines(cluster),
}));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
vi.mock("../bridge/helm", () => ({ openHelmRelease: vi.fn(async () => {}) }));
vi.mock("../bridge/events", () => ({ fetchEventsSnapshot: vi.fn() }));
vi.mock("../bridge/crd", () => ({ listInstancePage: vi.fn().mockResolvedValue({ items: [], nextToken: "" }) }));
import { fetchEventsSnapshot } from "../bridge/events";
import { getClusterMetrics } from "../bridge/metrics";

const dto: ClusterDTO = {
  name: "homelab-nelli", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 58,
  version: "v1.36.1", gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "homelab", region: "", provider: "k3s", group: "", ageSeconds: 3,
};
const blue: ClusterDTO = { ...dto, name: "homelab-blue" };
const NELLI = "homelab-nelli";

function setMetrics(d: MetricsDTO | null) {
  useFleet.setState({ metrics: { cluster: "homelab-blue", dto: d, loading: false } });
}

// the cluster's summary (health 1c slice 4): core tasks plus extras
const seed = (tasks = core(), over: Parameters<typeof summary>[1] = {}, cluster = NELLI) => applySummary(summary(tasks, over, cluster));
const ref = (gvr: string, name: string, namespace?: string, label = ""): HealthRef => ({ cluster: NELLI, gvr, namespace, name, role: "fault", label });
const area = (id: string) => screen.getByTestId(`ov-area-${id}`);

beforeEach(() => {
  vi.mocked(fetchEventsSnapshot).mockResolvedValue({ namespaces: [], events: [] });
  useFleet.setState({ metrics: { cluster: null, dto: null, loading: false }, navIntent: null, clusters: [dto] });
  resetHealthStore();
});

describe("Overview on the workspace template (redesign step 7e)", () => {
  it("the station page: titled by the station, then its verdict, Needs you and the sections in the parent's order", () => {
    seed();
    render(<Overview c={dto} />);
    expect(screen.getByRole("heading", { name: NELLI })).toBeTruthy(); // the station; the destination stays Overview
    expect(screen.getByText("v1.36.1")).toBeTruthy();
    expect(screen.getByText("k3s")).toBeTruthy();
    expect(screen.getByText("1/1 ready")).toBeTruthy();
    expect(screen.getByText("58")).toBeTruthy();
    const order = [...screen.getByTestId("overview").children].map((el) => el.getAttribute("aria-label") ?? el.getAttribute("data-testid") ?? el.className);
    expect(order).toEqual(["Station verdict", "station-queue", "ov-columns", "Recent warning events", "Resource risks", "By area"]);
    // utilisation and monitoring lead the two columns, the facts follow
    expect([...document.querySelectorAll(".ov-columns > section")].map((x) => x.getAttribute("aria-label"))).toEqual(["Resources", "Cluster facts"]);
  });

  it("a failed cluster leads with its connection, never healthy; the reason's notes come last, shaped", () => {
    render(<Overview c={{ ...dto, state: "Failed", reason: "connect timed out" }} />);
    const verdict = screen.getByTestId("station-verdict");
    expect(verdict.querySelector("h2")?.textContent).toBe("failed: connect timed out");
    expect(verdict.querySelector('h2 [data-aspect="checking"]')).toBeTruthy();
    const connection = screen.getByTestId("ov-connection");
    // the Aspect carries the fault's shape; the ▲ is retired (slice 13d)
    expect(connection.textContent).toContain("connect timed out");
    expect(connection.textContent).not.toContain("▲");
    expect(connection.querySelector('[data-aspect="fault"]')).toBeTruthy();
    const kids = [...screen.getByTestId("overview").children];
    expect(kids[kids.length - 1]).toBe(connection);
  });

  it("Refresh re-reads what is still fetched - the metrics; the attention is pushed", () => {
    seed();
    render(<Overview c={dto} />);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(getClusterMetrics).toHaveBeenCalledWith(NELLI, true);
  });
});

describe("Overview attention reads the cluster's summary (health 1c slice 4)", () => {
  it("before anything answers it says so - no zeros", () => {
    render(<Overview c={dto} />);
    expect(screen.getByText("Reading homelab-nelli…")).toBeTruthy();
    expect(screen.queryByText(/none need attention/)).toBeNull();
  });

  it("one row per area with its lines: faults shaped and toned, absent tools omitted, nothing green", () => {
    seed([...replace(replace(replace(core(), task("workloads", [verdict("workloads", { fault: 3, ok: 2 }, [kind("deployments")])])),
      task("helm", [native("releases", { fault: 1, ok: 1 }, 1, "failed or pending", [kind("helm")])])),
      task("events", [evidence(9, [kind("events")])])),
      task("flux", [native("reconciliations", { fault: 2 }, 2, "not ready", [kind("kustomizations")]), native("sources", { ok: 1 }, 0, "not ready", [kind("gitrepositories")])])]);
    const { container } = render(<Overview c={{ ...dto, fluxPresent: true }} />);
    expect(area("workloads").textContent).toContain("3 need attention");
    expect(area("workloads").querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(area("helm").textContent).toContain("1 failed or pending");
    expect(area("flux").textContent).toContain("Reconciliations:  2 not ready");
    expect(area("flux").querySelector('[data-aspect="fault"]')).toBeTruthy();
    for (const a of ["workloads", "helm", "flux"]) expect(area(a).textContent).not.toContain("▲");
    expect(area("flux").textContent).toContain("Sources: none not ready");
    expect(area("events").textContent).toContain("9 warning events");
    expect(screen.queryByTestId("ov-area-argo")).toBeNull();
    expect(container.innerHTML).not.toMatch(/color-text-success/);
  });

  it("unreadable and qualified areas are named, and the quiet line is scoped to what was read", () => {
    seed(replace(replace(core(), task("pods", [verdict("pods", { unknown: 1, ok: 4 }, [kind("pods")])])),
      task("nodes", [verdict("nodes", {}, [kind("nodes", { coverage: "unreadable", class: "forbidden" })], { coverage: "unreadable" })])));
    render(<Overview c={dto} />);
    expect(area("pods").textContent).toContain("1 unverified");
    expect(area("nodes").textContent).toContain("could not be read (forbidden)");
    expect(screen.getByText("No attention in the 4 areas read; 2 unverified.")).toBeTruthy();
  });

  it("each area opens its lens with that lens's attention filter", () => {
    seed(replace(core(), task("workloads", [verdict("workloads", { fault: 1 }, [kind("deployments")])])));
    useFleet.getState().openCluster(NELLI);
    render(<Overview c={dto} />);
    fireEvent.click(screen.getByRole("button", { name: "Workloads" }));
    expect(useFleet.getState().route).toMatchObject({ section: "workloads" });
    expect(useFleet.getState().workloads.needsAttention).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Events" }));
    expect(useFleet.getState().route).toMatchObject({ section: "events" });
    expect(useFleet.getState().events.warningsOnly).toBe(true);
  });

  it("a lit line's Top refs enter the emitting part's view through selection intents", () => {
    useFleet.getState().openCluster(NELLI);
    seed([...replace(core(), task("pods", [verdict("pods", { fault: 4 }, [kind("pods")], { top: ["api-1", "api-2", "api-3", "api-4"].map((n) => ref("v1/pods", n, "shop", "CrashLoopBackOff")) })])),
      task("certs", [native("certificates", { ok: 2 }, 0, "not ready", [kind("certificates")]),
        native("issuers", { fault: 1 }, 1, "not ready", [kind("issuers"), kind("clusterissuers")], { top: [ref("cert-manager.io/v1/clusterissuers", "letsencrypt")] })])]);
    render(<Overview c={{ ...dto, certManagerPresent: true }} />);
    expect(screen.getAllByTestId("ov-top").filter((b) => b.textContent?.startsWith("shop/"))).toHaveLength(3); // up to three per line
    fireEvent.click(screen.getByRole("button", { name: /^shop\/api-1/ }));
    expect(useFleet.getState().route).toMatchObject({ section: "pods" });
    expect(useFleet.getState().navIntent).toEqual({ kind: "select-pod", cluster: NELLI, namespace: "shop", name: "api-1" });
    fireEvent.click(screen.getByRole("button", { name: /^letsencrypt/ }));
    expect(useFleet.getState().route).toMatchObject({ section: "certs" });
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "reveal", task: "certs", part: "issuers", ref: { gvr: "cert-manager.io/v1/clusterissuers", name: "letsencrypt" } });
  });

  it("namespaces come from the current summary; unknown is a dash, never zero", () => {
    seed(core(), { namespaces: 7 });
    const { unmount } = render(<Overview c={dto} />);
    expect(screen.getByText("7")).toBeTruthy();
    unmount();
    resetHealthStore();
    seed(core(), { namespaces: undefined });
    render(<Overview c={dto} />);
    expect(screen.getByText("namespaces").nextElementSibling?.textContent).toBe("—");
  });

  it("another cluster's summary is never shown", () => {
    seed(replace(core(), task("workloads", [verdict("workloads", { fault: 4 }, [kind("deployments")])])), {}, "homelab-blue");
    render(<Overview c={dto} />);
    expect(screen.getByText("Reading homelab-nelli…")).toBeTruthy();
  });

  it("after a Final nothing is current", () => {
    seed();
    seed([], { seq: 2, final: true, evaluatedAt: "2026-10-12T12:05:00Z" });
    render(<Overview c={dto} />);
    expect(screen.getByTestId("ov-ended").textContent).toMatch(/^Observation ended at /);
    expect(screen.queryByTestId("ov-area-workloads")).toBeNull();
  });
});

describe("Overview warning events", () => {
  it("shows warning events and opens the filtered Events page", async () => {
    vi.mocked(fetchEventsSnapshot).mockResolvedValueOnce({
      namespaces: ["apps"],
      events: [{ type: "Warning", reason: "BackOff", message: "Back-off restarting failed container", count: 3, namespace: "apps", kind: "Pod", name: "api-1", lastSeenUnix: 100, firstSeenUnix: 10 }],
    });
    useFleet.getState().openCluster("homelab-nelli");
    render(<Overview c={dto} />);
    await waitFor(() => expect(screen.getByText("BackOff")).toBeTruthy());
    fireEvent.click(screen.getByText("BackOff"));
    const state = useFleet.getState();
    expect(state.route).toMatchObject({ name: "cluster", section: "events" });
    expect(state.events.warningsOnly).toBe(true);
    expect(state.events.search).toBe("api-1");
  });

  it("a failed events read is a failure, never 'no warning events'", async () => {
    vi.mocked(fetchEventsSnapshot).mockRejectedValueOnce(new Error("events is forbidden"));
    render(<Overview c={dto} />);
    expect(await screen.findByText(/Events could not be read: events is forbidden/)).toBeTruthy();
    expect(screen.queryByText(/No warning events/)).toBeNull();
  });

  it("an empty read is neutral and scoped", async () => {
    render(<Overview c={dto} />);
    const line = await screen.findByText("No warning events in the current read.");
    expect(line.className).toContain("t-secondary");
  });
});

describe("Overview metrics", () => {
  it("renders cpu/mem percents and the discovered monitoring line; used is never green", () => {
    setMetrics({ available: true, mode: "discovered-service", source: "monitoring/prometheus-operated:9090", warning: "", reason: "", cpuFraction: 0.38, memFraction: 0.92 });
    const { container } = render(<Overview c={blue} />);
    expect(screen.getByText("38%")).toBeTruthy();
    expect(screen.getByText("92%")).toBeTruthy();
    expect(screen.getByText("92%").parentElement?.querySelector('[data-aspect="attention"]')).toBeTruthy();
    expect(screen.getByText(/monitoring: discovered · svc monitoring\/prometheus-operated:9090/)).toBeTruthy();
    expect(container.innerHTML).not.toMatch(/color-text-success/);
  });

  it("renders — for null fractions", () => {
    setMetrics({ available: true, mode: "explicit-endpoint", source: "https://h", warning: "", reason: "", cpuFraction: null, memFraction: null });
    render(<Overview c={blue} />);
    expect(screen.getAllByText("—").length).toBeGreaterThanOrEqual(2);
  });

  it("renders the unavailable reason", () => {
    setMetrics({ available: false, mode: "unavailable", source: "", warning: "", reason: "multiple candidate Services found, set metrics.serviceRef", cpuFraction: null, memFraction: null });
    render(<Overview c={blue} />);
    expect(screen.getByText(/monitoring unavailable: multiple candidate Services found/)).toBeTruthy();
  });

  it("renders cpu/mem sparklines when the range series are available", async () => {
    mockGetClusterSparklines.mockResolvedValueOnce({ available: true, cpu: [{ t: 100, v: 0.4 }, { t: 160, v: 0.5 }], mem: [{ t: 100, v: 0.6 }, { t: 160, v: 0.7 }] });
    render(<Overview c={dto} />);
    const sparks = await screen.findAllByRole("img", { name: /metric sparkline/i });
    expect(sparks.length).toBe(2);
  });
});

describe("Overview's tool facts read the model (health 1c slice 5 §5)", () => {
  it("argo cd and gateway: their lines, checking, or not detected only on confirmed absence", () => {
    const c = { ...dto, argoPresent: true, gatewayAPIVersion: "" };
    useFleet.setState({ clusters: [c] });
    seed(core());
    const { unmount } = render(<Overview c={c} />);
    expect(screen.getByText("argo cd").nextElementSibling?.textContent).toBe("checking…");
    expect(screen.getByText("gateway").nextElementSibling?.textContent).toBe("not detected");
    unmount();
    seed([...core(), task("argo", [native("applications", { fault: 1, ok: 2 }, 1, "need attention", [kind("applications")]), native("applicationsets", { ok: 1 }, 0, "not healthy", [kind("applicationsets")])])], { seq: 2 });
    render(<Overview c={c} />);
    expect(screen.getByText("argo cd").nextElementSibling?.textContent).toBe("applications 1 need attention · applicationsets none not healthy");
  });
});

// Codex review of 06559e1: the facts read the model's lifecycle and
// coverage - never a capability flag that may be unknown or lagging.
describe("Overview's tool facts follow the model's lifecycle", () => {
  const fact = (label: string) => screen.getByText(label).nextElementSibling?.textContent;

  it("an unknown capability is never called installed", () => {
    const c = { ...dto, state: "Connecting" };
    useFleet.setState({ clusters: [c] });
    seed(core());
    render(<Overview c={c} />);
    for (const label of ["flux", "argo cd", "gateway"]) expect(fact(label)).toBe("checking… · capability not yet known");
  });

  it("after a Final every fact says the observation ended - never checking, never not detected", () => {
    const c = { ...dto, fluxPresent: true, argoPresent: false, gatewayAPIVersion: "v1.2" };
    useFleet.setState({ clusters: [c] });
    seed(core());
    seed([], { seq: 2, final: true, evaluatedAt: "2026-10-12T12:05:00Z" });
    render(<Overview c={c} />);
    for (const label of ["flux", "argo cd", "gateway"]) expect(fact(label)).toMatch(/^observation ended at /);
  });

  it("a controller's state keeps its reason", () => {
    const c = { ...dto, fluxPresent: true };
    useFleet.setState({ clusters: [c] });
    seed([...core(), task("flux", [native("reconciliations", { ok: 2 }, 0, "not ready", [kind("kustomizations")]), native("sources", { ok: 1 }, 0, "not ready", [kind("gitrepositories")])],
      { controller: { state: "unknown", reason: "kustomize-controller unreadable" } })]);
    render(<Overview c={c} />);
    // short words in the facts (slice 13d.3): the lines, then the controller's
    // state; its reason stays in the fact's title
    expect(fact("flux")).toMatch(/^reconciliations .* · controller unverified$/);
    expect(screen.getByTitle("controller unknown: kustomize-controller unreadable")).toBeTruthy();
  });

  it("the Observer's confirmed absence wins over a lagging present flag", () => {
    const c = { ...dto, fluxPresent: true, argoPresent: true, gatewayAPIVersion: "v1.2" };
    useFleet.setState({ clusters: [c] });
    seed([...core(), task("flux", [absentPart("reconciliations", "kustomizations"), absentPart("sources", "gitrepositories")]),
      task("argo", [absentPart("applications", "applications"), absentPart("applicationsets", "applicationsets")]),
      task("gateway", [absentPart("gateways", "gateways"), absentPart("routes", "httproutes")])]);
    render(<Overview c={c} />);
    for (const label of ["flux", "argo cd", "gateway"]) expect(fact(label)).toBe("not detected");
  });
});

describe("Overview names what the summary does not carry (slice 6 §4)", () => {
  it("installed domains outside the summary are checking areas, and the quiet line counts them as gaps", () => {
    const c = { ...dto, certManagerPresent: true, externalSecretsPresent: true, gatewayAPIVersion: "v1.2", karpenterPresent: true };
    useFleet.setState({ clusters: [c] });
    seed();
    const { container } = render(<Overview c={c} />);
    for (const id of ["certs", "eso", "gateway", "karpenter"]) {
      expect(area(id).getAttribute("data-state")).toBe("checking");
      expect(area(id).textContent).toContain("checking…");
    }
    expect(screen.getByText(/No attention in the 5 areas read; 4 unverified\./)).toBeTruthy();
    expect(container.textContent).not.toContain("not covered");
  });

  it("a stale area says when its value was last good", () => {
    seed(replace(core(), task("nodes", [verdict("nodes", { fault: 1 }, [kind("nodes", { coverage: "stale", lastGoodAt: new Date(2026, 9, 11, 9, 5).toISOString() })], { coverage: "stale" })])));
    render(<Overview c={dto} />);
    expect(area("nodes").textContent).toContain("Nodes last good 09:05");
  });

  it("a dependency gap is named beside a known fault", () => {
    seed(replace(core(), task("pods", [verdict("pods", { fault: 2 }, [kind("pods"), dep("replicasets", { coverage: "unreadable" })])])));
    render(<Overview c={dto} />);
    expect(area("pods").textContent).toContain("2 need attention · ReplicaSets could not be read - a dependency");
    expect(area("pods").querySelector('[data-aspect="fault"]')).toBeTruthy();
  });
});

// UI review 2026-10-13: a connection with notes reads as one line naming
// what is unverified; the full notes stay one click away, and each area says
// "controller health unverified" in one line with its reason behind it.
describe("Overview connection notes", () => {
  beforeEach(() => { resetHealthStore(); });

  it("summarises controller notes and keeps every note in a disclosure", () => {
    seed([...core(),
      task("certs", [native("certificates", { ok: 2 }, 0, "not ready", [kind("certificates")])], { controller: { state: "unknown", reason: "no Deployment cert-manager/cert-manager" } }),
      task("karpenter", [native("nodeclaims", { ok: 1 }, 0, "failing", [kind("nodeclaims")])], { controller: { state: "unknown", reason: "no Deployment kube-system/karpenter" } })]);
    const reason = "cert-manager installed but cert-manager health is unverified (no Deployment cert-manager/cert-manager); Karpenter CRDs installed but the karpenter controller's health is unverified (no Deployment kube-system/karpenter)";
    const { container } = render(<Overview c={{ ...dto, state: "Degraded", reason, certManagerPresent: true, karpenterPresent: true }} />);
    const notice = container.querySelector("details.ov-notice") as HTMLDetailsElement;
    expect(notice).toBeTruthy();
    expect(notice.querySelector("summary")?.textContent).toMatch(/^Controller health unverified: Certificates, Karpenter · 2 notes$/);
    expect(notice.querySelectorAll("li")).toHaveLength(2);
    expect(notice.textContent).toContain("no Deployment kube-system/karpenter");
    // each area: one line, its reason behind a disclosure
    const why = screen.getByTestId("ov-area-certs").querySelector("details.ov-why") as HTMLDetailsElement;
    expect(why.querySelector("summary")?.textContent).toBe("controller health unverified");
    expect(why.textContent).toContain("no Deployment cert-manager/cert-manager");
  });

  it("a failed connection keeps its reason in full", () => {
    seed();
    render(<Overview c={{ ...dto, state: "Failed", reason: "connect timed out" }} />);
    expect(screen.getByText("connect timed out")).toBeTruthy();
    expect(screen.getByText("connect timed out").parentElement?.querySelector('[data-aspect="fault"]')).toBeTruthy();
  });
});

// Codex review of da40ac6: a controller summary never stands in for a
// different connection problem.
describe("Overview connection notes keep the real problem first", () => {
  beforeEach(() => { resetHealthStore(); });
  const fluxUnknown = () => [...core(), task("flux", [native("reconciliations", { ok: 2 }, 0, "not ready", [kind("kustomizations")])],
    { controller: { state: "unknown", reason: "no Deployment flux-system/kustomize-controller" } })];

  it("a stale watch error is shown in full, never as a controller summary", () => {
    seed(fluxUnknown());
    const { container } = render(<Overview c={{ ...dto, state: "Stale", reason: "pods watch failed: forbidden", fluxPresent: true }} />);
    expect(container.querySelector("details.ov-notice")).toBeNull();
    expect(screen.getByText("pods watch failed: forbidden")).toBeTruthy();
  });

  it("with notes of both kinds, the non-controller note leads", () => {
    seed(fluxUnknown());
    const reason = "pods watch failed: forbidden; Flux installed but kustomize-controller health is unverified (no Deployment flux-system/kustomize-controller)";
    const { container } = render(<Overview c={{ ...dto, state: "Degraded", reason, fluxPresent: true }} />);
    const summary = container.querySelector("details.ov-notice > summary")?.textContent ?? "";
    expect(summary).toBe("pods watch failed: forbidden · 2 notes");
    expect(container.querySelectorAll("details.ov-notice li")).toHaveLength(2);
  });
});
