import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, act, within, waitFor } from "@testing-library/react";
import type { ClusterDTO, KarpenterClaimDTO, KarpenterClassDTO, KarpenterPoolDTO, KarpenterSnapshotDTO } from "../store/fleet";
import { useFleet } from "../store/fleet";
import { useHealth } from "../store/health";
import type { KarpenterRows } from "../health/useKarpenterRows";
import { KarpenterView } from "./KarpenterView";
import { claimRule, poolRule, withRule } from "../test/backendRules";

vi.mock("../bridge/karpenter", () => ({ getKarpenterState: vi.fn(async () => ({}) as KarpenterSnapshotDTO) }));
import { getKarpenterState } from "../bridge/karpenter";
// The NodePools and NodeClaims are rows (health/useKarpenterRows): these
// tests hand the lens a live, complete read; the snapshot carries the
// disruption feed, and the controllers are the task's reading in the health
// summary (coverage S10 §5).
let rowsValue: KarpenterRows;
vi.mock("../health/useKarpenterRows", () => ({ useKarpenterRows: () => rowsValue }));
const liveCoverage = () => [
  { kind: "NodePool", served: true, synced: true, coverage: "live" as const },
  { kind: "NodeClaim", served: true, synced: true, coverage: "live" as const },
];
function setRows(r: { claims?: KarpenterClaimDTO[]; pools?: KarpenterPoolDTO[]; classes?: KarpenterClassDTO[]; coverage?: KarpenterRows["coverage"]; loading?: boolean; watchError?: string | null;
  poolNodesDep?: KarpenterRows["poolNodesDep"]; classesState?: KarpenterRows["classesState"] } = {}) {
  rowsValue = {
    claims: r.claims ?? [], pools: r.pools ?? [], classes: r.classes ?? [], coverage: r.coverage ?? liveCoverage(), watchError: r.watchError ?? null, loading: r.loading ?? false,
    poolNodesDep: r.poolNodesDep ?? { kind: "NodeClaim", served: true, synced: true, coverage: "live" },
    rowsState: r.classesState ?? { updating: false, refreshing: false, evaluatedAt: null }, partCounts: { nodepools: null, nodeclaims: null },
    classesState: r.classesState ?? { updating: false, refreshing: false, evaluatedAt: null },
  };
}
setRows();

const snap = (over: Partial<KarpenterSnapshotDTO> & { claims?: KarpenterClaimDTO[]; pools?: KarpenterPoolDTO[] } = {}): KarpenterSnapshotDTO => {
  // a fixture's claims/pools are the rows the lens reads
  const { claims, pools, ...rest } = over;
  setRows({ claims, pools });
  return { detected: true, events: [], ...rest };
};
// controller sets the karpenter task's controller in the health summary.
function controller(state: string | null, reason?: string) {
  const tasks = state ? [{ id: "karpenter", parts: [], coverage: "live" as const, controller: { state, reason } }] : [];
  useHealth.setState({ summaries: { x: { cluster: "x", incarnation: 1, seq: 1, evaluatedAt: new Date().toISOString(), connection: "connected", tasks } } });
}
// Fixtures carry the rule the backend computes (test/backendRules.ts, held to
// the Go golden fixture, which also covers the rule's cases and its exact
// launch-deadline boundary).
const claim = (over: Partial<KarpenterClaimDTO> = {}): KarpenterClaimDTO => withRule<KarpenterClaimDTO>({
  name: "default-abcde", pool: "default", nodeName: "ip-10-0-1-1", ready: "ready", failingType: "", failingReason: "", failingMessage: "",
  drifted: false, consolidatable: false, capacityType: "spot", instanceType: "m6i.large", ageSeconds: 3600, ...over,
  createdUnix: Math.floor(Date.now() / 1000) - (over.ageSeconds ?? 3600),
}, claimRule);
type D<T> = { promise: Promise<T>; resolve: (v: T) => void };
const deferred = <T,>(): D<T> => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => (resolve = r)); return { promise, resolve }; };
const rows = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("KarpenterView (redesign step 5)", () => {
  beforeEach(() => { vi.clearAllMocks(); setRows(); controller(null); });

  it("reads the Go-computed rule, never its own: header counts and row labels (1c §2)", async () => {
    // A natively ready claim the backend flags, and a natively failed one it clears.
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [
      claim({ name: "backend-flags", ready: "ready", rule: { included: true, role: "fault", label: "flagged by backend" } }),
      claim({ name: "backend-clears", ready: "not-ready", failingType: "Launched", failingStatus: "False", rule: { included: false, role: "ok", label: "ready" } }),
    ] }));
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("1 not ready")).toBeTruthy();
    expect(rows()[0]).toContain("flagged by backend"); // ordered by the rule's role
  });

  it("stuck first: failed, then appears stuck, then launching, then ready; header counts", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [
      claim({ name: "ok" }),
      claim({ name: "young", ready: "unknown", ageSeconds: 30, nodeName: "" }),
      claim({ name: "old", ready: "unknown", ageSeconds: 3600, nodeName: "" }),
      claim({ name: "bad", ready: "not-ready", failingType: "Launched", failingStatus: "False", failingReason: "InsufficientCapacity" }),
    ] }));
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("1 not ready · 1 appears stuck")).toBeTruthy();
    const r = rows();
    expect(r[0]).toContain("bad");
    expect(r[1]).toContain("old");
    expect(r[2]).toContain("young");
    expect(r[3]).toContain("ok");
  });

  it("a failed claim names the failing link and its message in words; Open node when registered", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim({ ready: "not-ready", failingType: "Registered", failingStatus: "False", failingReason: "NodeNotFound", failingMessage: "node did not join within 15m" })] }));
    render(<KarpenterView cluster="x" />);
    await screen.findByTestId("lens-inspector"); // the first claim needing attention is auto-selected (UI review 2026-10-13)
    const p = within(screen.getByTestId("lens-inspector"));
    expect(p.getByText("The launch chain failed at Registered: NodeNotFound.")).toBeTruthy();
    expect(p.getByText("node did not join within 15m")).toBeTruthy();
    expect(p.getByRole("button", { name: "Open node" })).toBeTruthy();
  });

  it("drift and consolidation are intended behaviour, not faults", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim({ drifted: true, consolidatable: true })] }));
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("all ready")).toBeTruthy();
    expect(screen.getByText("1 drifted (eligible for replacement)")).toBeTruthy();
    fireEvent.click(screen.getByText("default-abcde").closest(".lens-row") as HTMLElement);
    expect(within(screen.getByTestId("lens-inspector")).getByText("Intended behaviour, not a fault.")).toBeTruthy();
  });

  it("an unhealthy controller is the headline, from the task's reading", async () => {
    controller("not ready", "the karpenter controller karpenter/karpenter (AWS) is not ready");
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim()] }));
    render(<KarpenterView cluster="x" />);
    const n = await screen.findByTestId("karp-controller-down");
    expect(n.textContent).toContain("A Karpenter controller is not ready.");
    expect(n.textContent).toContain("karpenter/karpenter (AWS)");
  });

  // coverage S10 §5: platform management is a note, never a fault or a gap
  it("a managed controller is a note", async () => {
    controller("managed", "NodeClass managed by EKS Auto Mode; controller health not directly observed");
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim()] }));
    render(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("karp-controller-managed")).textContent).toContain("Managed by the platform. NodeClass managed by EKS Auto Mode");
    expect(screen.queryByTestId("karp-controller-down")).toBeNull();
    expect(screen.queryByTestId("karp-controller-unverified")).toBeNull();
  });

  it("pools show capacity against limits and warn near a limit", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ pools: [withRule<KarpenterPoolDTO>({ name: "default", ready: "ready", reason: "", message: "", weight: 10, cpuLimit: 100, cpuUsed: 95, memLimit: null, memUsed: 1024 ** 3, nodes: 3 }, poolRule)] }));
    render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^NodePools/ }));
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    expect(within(screen.getByTestId("lens-inspector")).getByText(/Karpenter stops provisioning from this pool/)).toBeTruthy();
  });

  it("not installed, unreadable rows, a failed snapshot and empty claims are distinct states", async () => {
    // "not installed" is the rows' own confirmed absence of both kinds
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap({ detected: false }));
    setRows({ coverage: [{ kind: "NodePool", served: false, synced: true, coverage: "absent" }, { kind: "NodeClaim", served: false, synced: true, coverage: "absent" }] });
    const first = render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("Karpenter is not installed on this cluster.")).toBeTruthy();
    first.unmount();
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap());
    setRows({ coverage: [{ kind: "NodePool", served: true, synced: false, coverage: "unreadable", error: "forbidden" }, { kind: "NodeClaim", served: true, synced: false, coverage: "unreadable", error: "nodeclaims is forbidden" }], watchError: "NodeClaim: nodeclaims is forbidden" });
    const second = render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("Could not read the NodeClaims.")).toBeTruthy();
    second.unmount();
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap({ error: "cluster client not available" }));
    const third = render(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("karp-snapshot-error")).textContent).toContain("cluster client not available");
    expect(screen.getByText("No NodeClaims.")).toBeTruthy(); // the rows' own evidence still speaks
    third.unmount();
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap());
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("No NodeClaims.")).toBeTruthy();
  });

  it("disruption activity is evidence with a full-message panel", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim()], events: [{ type: "Normal", reason: "DisruptionBlocked", message: "pdb prevents pod evictions", kind: "Node", name: "ip-10-0-1-1", lastSeenUnix: Math.floor(Date.now() / 1000) - 60 }] }));
    const { container } = render(<KarpenterView cluster="x" />);
    await screen.findByText("all ready");
    fireEvent.click(screen.getByRole("button", { name: /^Disruption activity/ }));
    expect(container.querySelector('.lens-row [role="img"]')).toBeNull();
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    expect(within(screen.getByTestId("lens-inspector")).getByText("pdb prevents pod evictions")).toBeTruthy();
  });

  it("a late snapshot for a previous cluster never lands on the new one", async () => {
    const late = deferred<KarpenterSnapshotDTO>();
    const ev = (name: string) => ({ type: "Normal", reason: "Disrupting", message: name, kind: "NodeClaim", name, lastSeenUnix: Math.floor(Date.now() / 1000) });
    vi.mocked(getKarpenterState).mockReturnValueOnce(late.promise).mockResolvedValueOnce(snap({ events: [ev("from-b")] }));
    const { rerender } = render(<KarpenterView cluster="a" />);
    rerender(<KarpenterView cluster="b" />);
    fireEvent.click(await screen.findByRole("button", { name: /^Disruption activity 1/ }));
    expect(await screen.findAllByText("from-b")).toBeTruthy();
    await act(async () => { late.resolve({ detected: true, events: [ev("from-a")] }); await late.promise; });
    expect(screen.queryAllByText("from-a")).toHaveLength(0);
  });
});

describe("KarpenterView evidence corrections (Codex 271342a7)", () => {
  beforeEach(() => { vi.clearAllMocks(); setRows(); controller(null); });

  it("Ready=False with an unproven link names no failure; its message is not presented as the cause", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim({ ready: "not-ready", failingType: "Launched", failingStatus: "Unknown", failingMessage: "waiting" })] }));
    render(<KarpenterView cluster="x" />);
    await screen.findByTestId("lens-inspector"); // the first claim needing attention is auto-selected (UI review 2026-10-13)
    const p = within(screen.getByTestId("lens-inspector"));
    expect(p.getByText("Ready is False, but no launch-chain link is confirmed failed - Launched is Unknown (unverified).")).toBeTruthy();
    expect(p.queryByText("waiting")).toBeNull();
  });

  it("registration expiry is only mentioned for an unregistered claim; a registered one gets an initialization diagnosis", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [
      claim({ name: "unreg", ready: "unknown", nodeName: "", ageSeconds: 3600 }),
      claim({ name: "reg", ready: "unknown", nodeName: "ip-1", ageSeconds: 3600 }),
    ] }));
    render(<KarpenterView cluster="x" />);
    fireEvent.click((await screen.findByText("unreg")).closest(".lens-row") as HTMLElement);
    expect(within(screen.getByTestId("lens-inspector")).getByText(/registration TTL/)).toBeTruthy();
    fireEvent.click(screen.getByText("reg").closest(".lens-row") as HTMLElement);
    const p = within(screen.getByTestId("lens-inspector"));
    expect(p.getByText(/Registered as node ip-1 but not initialized/)).toBeTruthy();
    expect(p.queryByText(/registration TTL/)).toBeNull();
  });

  it("an unreadable controller is unverified, never a confirmed outage", async () => {
    controller("unknown", "the Karpenter controllers could not all be read (forbidden)");
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim()] }));
    render(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("karp-controller-unverified")).textContent).toContain("forbidden");
    expect(screen.queryByTestId("karp-controller-down")).toBeNull();
  });

  it("an unreadable activity feed is unreadable, never quiet; a capped one discloses partial coverage", async () => {
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap({ claims: [claim()], eventsError: "NodeClaim events: forbidden; NodePool events: forbidden" }));
    const first = render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^Disruption activity/ }));
    expect(screen.getByText("Could not read disruption activity.")).toBeTruthy();
    expect(screen.queryByText("No recent disruption activity.")).toBeNull();
    first.unmount();
    const ev = { type: "Normal", reason: "Disrupting", message: "m", kind: "NodeClaim", name: "c", lastSeenUnix: Math.floor(Date.now() / 1000) };
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap({ claims: [claim()], events: [ev], eventsTruncated: true }));
    render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^Disruption activity/ }));
    expect(screen.getByText("Capped read - showing the newest among the events read.")).toBeTruthy();
    expect(screen.queryByText(/more exist/)).toBeNull();
    expect(screen.getByRole("button", { name: /^Disruption activity 1\+/ })).toBeTruthy();
  });

  it("an Unknown NodePool is never folded into 'all ready'", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim()], pools: [withRule<KarpenterPoolDTO>({ name: "p", ready: "unknown", reason: "", message: "", weight: 0, cpuLimit: null, cpuUsed: null, memLimit: null, memUsed: null, nodes: 1 }, poolRule)] }));
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("1 NodePool unverified")).toBeTruthy();
    expect(screen.queryByText("all ready")).toBeNull();
  });
});

describe("KarpenterView composes independent notices (Codex cfbf793c)", () => {
  beforeEach(() => { vi.clearAllMocks(); setRows(); controller(null); });
  it("a down controller and a partially failed feed are both disclosed, with an incomplete count", async () => {
    const ev = { type: "Normal", reason: "Disrupting", message: "m", kind: "NodeClaim", name: "c", lastSeenUnix: Math.floor(Date.now() / 1000) };
    controller("not ready", "the karpenter controller k/karpenter (AWS) is not ready");
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim()], events: [ev], eventsError: "NodePool events: forbidden" }));
    render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^Disruption activity 1\+/ }));
    expect(screen.getByTestId("karp-controller-down")).toBeTruthy();
    expect(screen.getByTestId("karp-feed-partial").textContent).toContain("Partial read - NodePool events: forbidden.");
    expect(screen.getByText("incomplete read · newest among the events read")).toBeTruthy();
  });
});

// Codex review of 442d42a #3: the controller/feed snapshot never hides the
// rows' own observation.
describe("KarpenterView availability is the rows' own", () => {
  beforeEach(() => { vi.clearAllMocks(); setRows(); });

  it("a snapshot reporting Karpenter undetected never hides live observed claims", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ detected: false, claims: [claim({ name: "observed" })] }));
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("observed")).toBeTruthy();
    expect(screen.queryByText("Karpenter is not installed on this cluster.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /^Disruption activity/ }));
    expect(screen.getByText("Disruption activity was not read.")).toBeTruthy();
  });

  it("checking coverage is not a confirmed absence", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ detected: false }));
    setRows({ coverage: [{ kind: "NodePool", served: true, synced: false, coverage: "checking" }, { kind: "NodeClaim", served: true, synced: false, coverage: "checking" }] });
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByLabelText("reading NodePools and NodeClaims")).toBeTruthy();
    expect(screen.queryByText("Karpenter is not installed on this cluster.")).toBeNull();
  });
});

// UI review 2026-10-13: like the other lenses, the first NodeClaim needing
// attention opens once from the first complete read; a dismissal sticks.
describe("KarpenterView auto-select", () => {
  it("opens the first claim needing attention once, and a dismissal is not undone", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim({ ready: "not-ready", failingType: "Launched", failingStatus: "False", failingReason: "InsufficientCapacity" })] }));
    render(<KarpenterView cluster="x" />);
    const insp = await screen.findByTestId("lens-inspector");
    expect(within(insp).getAllByText(/default-abcde/).length).toBeGreaterThan(0);
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(screen.queryByTestId("lens-inspector")).toBeNull());
  });
});

// Coverage S10 §3, §4 and §8: the NodeClasses view, a pool's link and
// findings, a claim's class, and the provider's words kept local.
describe("KarpenterView NodeClasses (S10)", () => {
  beforeEach(() => { vi.clearAllMocks(); controller(null); });
  const klass = (over: Partial<KarpenterClassDTO> = {}): KarpenterClassDTO => ({
    uid: "c1", gvr: "karpenter.k8s.aws/v1/ec2nodeclasses", generation: 1, name: "default", group: "karpenter.k8s.aws", kind: "EC2NodeClass", adapter: "ec2",
    reading: { state: "not ready", text: "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms", native: "SubnetsReady False: SubnetsNotFound: no subnets for subnet-0abc" },
    conditions: [{ type: "SubnetsReady", status: "False", reason: "SubnetsNotFound", message: "no subnets for subnet-0abc" }], conditionsTotal: 1,
    unknown: [], facts: { subnets: 0, securityGroups: 2, amis: 1, role: "KarpenterNodeRole", instanceProfile: "ProfileName" }, users: ["workers"], usersComplete: true, ...over,
  });
  const classCoverage = () => [...liveCoverage(), { kind: "EC2NodeClass", served: true, synced: true, coverage: "live" as const }];
  const pool = withRule<KarpenterPoolDTO>({ name: "workers", ready: "not-ready", reason: "UnhealthyDependents", message: "native ready text", weight: 0, cpuLimit: null, cpuUsed: null, memLimit: null, memUsed: null, nodes: 0,
    nodeClassRef: { group: "karpenter.k8s.aws", kind: "EC2NodeClass", name: "default" }, link: { state: "found", adapter: "ec2", kind: "EC2NodeClass", classUid: "c1" },
    class: { state: "not ready", text: "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms" }, handler: "the karpenter controller karpenter/karpenter is ready",
    findings: [{ role: "fault", label: "NodeClass default not ready", text: "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms", native: "SubnetsReady False: SubnetsNotFound: no subnets for subnet-0abc" },
      { role: "ok", label: "registration failure is historical", text: "NodeRegistrationHealthy False: RegistrationFailed: x; last reported before the pool or its NodeClass changed", note: true }] }, poolRule);
  pool.rule = { included: true, role: "fault", label: "NodeClass default not ready" };

  it("lists the classes with their reading and users, and opens a class from its pool", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue({ detected: true, events: [] });
    setRows({ pools: [pool], classes: [klass(), klass({ uid: "c2", name: "spare", reading: { state: "ready" }, users: [], usersComplete: false })], coverage: classCoverage() });
    render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^NodePools/ }));
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    const p = within(screen.getByTestId("lens-inspector"));
    expect(p.getByTestId("karp-pool-head").textContent).toBe("SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms");
    expect(p.getByText(/Karpenter: SubnetsReady False: SubnetsNotFound: no subnets for subnet-0abc/)).toBeTruthy(); // the provider's words, local
    expect(p.getByTestId("karp-pool-class").textContent).toContain("EC2NodeClass/default");
    expect(p.getByTestId("karp-pool-class").textContent).toContain("the karpenter controller karpenter/karpenter is ready");
    expect(p.getAllByTestId("karp-finding")[0].textContent).toContain("registration failure is historical");
    fireEvent.click(p.getByRole("button", { name: "Open NodeClass" }));
    const c = within(await screen.findByTestId("karp-class-panel"));
    expect(c.getByText(/its failure is counted on each NodePool that uses it/)).toBeTruthy();
    expect(c.getByText("workers")).toBeTruthy();
    expect(c.getByText("ProfileName")).toBeTruthy(); // the local inspector keeps the instance profile
    const r = rows();
    expect(r[0]).toContain("default");
    expect(r[1]).toContain("spare");
    expect(r[1]).toContain("0+"); // an incomplete pool read is a lower bound, never "unused"
  });

  it("a pool whose nodes fail to register is said in the summary", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue({ detected: true, events: [] });
    const reg = { ...pool, name: "reg", rule: { included: true, role: "degraded", label: "nodes fail to register" } };
    setRows({ pools: [reg], claims: [claim()], coverage: classCoverage() });
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("1 NodePool failing to register")).toBeTruthy();
  });

  // §6: on an AKS cluster with Cilium, the networking note, conditional
  it("the AKS networking note: only with azure:// nodes and Cilium, worded as unverified", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue({ detected: true, events: [] });
    const aks = klass({ uid: "a1", name: "aks", kind: "AKSNodeClass", group: "karpenter.azure.com", adapter: "aks", facts: { kubernetesVersion: "1.33.2", subnetSet: true } });
    setRows({ pools: [pool], classes: [aks], coverage: classCoverage() });
    useFleet.setState({ clusters: [{ name: "x", state: "Synced", azureNodes: true, ciliumPresent: true } as ClusterDTO] });
    const first = render(<KarpenterView cluster="x" />);
    const note = await screen.findByTestId("karp-aks-networking");
    expect(note.textContent).toContain("BYO or Azure-managed is unverified");
    fireEvent.click(screen.getByRole("button", { name: /^NodeClasses/ }));
    fireEvent.click((await screen.findByText("aks")).closest(".lens-row") as HTMLElement);
    expect((await screen.findByTestId("karp-class-networking")).textContent).toContain("excludes third-party CNI networking troubleshooting");
    first.unmount();
    for (const c of [{ azureNodes: true, ciliumPresent: false }, { azureNodes: false, ciliumPresent: true }]) {
      useFleet.setState({ clusters: [{ name: "x", state: "Synced", ...c } as ClusterDTO] });
      const r = render(<KarpenterView cluster="x" />);
      await screen.findByRole("button", { name: /^NodePools/ });
      expect(screen.queryByTestId("karp-aks-networking")).toBeNull();
      r.unmount();
    }
    useFleet.setState({ clusters: [] });
  });

  it("says when no class kind is served", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue({ detected: true, events: [] });
    setRows({ pools: [pool], coverage: [...liveCoverage(), { kind: "EC2NodeClass", served: false, synced: true, coverage: "absent" as const }] });
    render(<KarpenterView cluster="x" />);
    await screen.findByRole("button", { name: /^NodePools/ });
    expect(screen.queryByRole("button", { name: /^NodeClasses/ })).toBeNull();
  });
});

// Codex's S10 implementation review, findings 2 and 3: every Karpenter kind
// is budgeted, so a capped read proves neither whole-list health nor
// absence; and the NodeClasses part qualifies itself from its own evidence.
describe("KarpenterView capped and stale evidence (S10 implementation review)", () => {
  beforeEach(() => { vi.clearAllMocks(); controller(null); vi.mocked(getKarpenterState).mockResolvedValue({ detected: true, events: [] }); setRows(); });
  const cls = (over: Partial<KarpenterClassDTO> = {}): KarpenterClassDTO => ({ uid: "c1", gvr: "karpenter.k8s.aws/v1/ec2nodeclasses", generation: 1, name: "default", group: "karpenter.k8s.aws",
    kind: "EC2NodeClass", adapter: "ec2", reading: { state: "ready" }, conditions: [], conditionsTotal: 0, unknown: [], facts: {}, users: [], usersComplete: true, ...over });
  const cv = (extra: Record<string, unknown> = {}) => [...liveCoverage(), { kind: "EC2NodeClass", served: true, synced: true, coverage: "live" as const, ...extra },
    { kind: "AKSNodeClass", served: false, synced: true, coverage: "absent" as const }, { kind: "NodeClass", served: false, synced: true, coverage: "absent" as const }];
  const capped = (kind: string, n: number) => liveCoverage().map((k) => (k.kind === kind ? { ...k, truncated: true, notRetained: n } : k));
  const openClasses = async () => fireEvent.click(await screen.findByRole("button", { name: /^NodeClasses/ }));
  const pool = (over: Partial<KarpenterPoolDTO> = {}) => withRule<KarpenterPoolDTO>({ name: "default", ready: "ready", reason: "", message: "", weight: 10, cpuLimit: null, cpuUsed: null, memLimit: null, memUsed: null, nodes: 1, ...over }, poolRule);

  it("a capped NodeClaim read is a lower bound: never all ready, its omission named", async () => {
    setRows({ claims: [claim()], pools: [], coverage: capped("NodeClaim", 2) });
    render(<KarpenterView cluster="x" />);
    await screen.findByText("default-abcde");
    expect(screen.queryByText("all ready")).toBeNull();
    expect(screen.getByTestId("karp-not-all-ready").textContent).toBe("none known not ready (capped read)");
    expect(document.querySelector(".lens-header .t-number")?.textContent).toMatch(/^1\+/);
    expect(screen.getByTestId("karp-capped").textContent).toMatch(/^Capped: 2 NodeClaims not retained - .*never removed\.$/);
  });

  it("a capped NodePool read blocks all ready too, and its view names the omission", async () => {
    setRows({ claims: [claim()], pools: [pool()], coverage: capped("NodePool", 1) });
    render(<KarpenterView cluster="x" />);
    await screen.findByText("default-abcde");
    expect(screen.queryByText("all ready")).toBeNull();
    expect(screen.queryByTestId("karp-capped")).toBeNull(); // the claims view's own read is whole
    fireEvent.click(screen.getByRole("button", { name: /^NodePools/ }));
    expect(screen.getByTestId("karp-capped").textContent).toContain("1 NodePool not retained");
  });

  it("a whole, current read still says all ready", async () => {
    setRows({ claims: [claim()], pools: [pool()] });
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("all ready")).toBeTruthy();
  });

  it("a NodePool's node count from a capped NodeClaim read is a lower bound", async () => {
    setRows({ claims: [claim()], pools: [pool({ nodes: 3 })], poolNodesDep: { kind: "NodeClaim", served: true, synced: true, coverage: "live", truncated: true, notRetained: 4 } });
    render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^NodePools/ }));
    expect(rows()[0]).toContain("3+");
  });

  it("a selection left out of a capped read is unverified, never removed", async () => {
    setRows({ claims: [claim({ uid: "u1" })] });
    const r = render(<KarpenterView cluster="x" />);
    fireEvent.click((await screen.findByText("default-abcde")).closest(".lens-row") as HTMLElement);
    await screen.findByTestId("karp-claim-panel");
    setRows({ claims: [], coverage: capped("NodeClaim", 1) });
    r.rerender(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("selection-unverified")).textContent).toMatch(/Not in the current NodeClaim read\..*unverified/);
    // the same absence on a whole read is a removal
    setRows({ claims: [] });
    r.rerender(<KarpenterView cluster="x" />);
    await waitFor(() => expect(screen.queryByTestId("selection-unverified")).toBeNull());
    expect(screen.getByText(/NodeClaim removed\./)).toBeTruthy();
  });

  it("a class left out of a capped read is unverified, never removed", async () => {
    setRows({ classes: [cls()], coverage: cv() });
    const r = render(<KarpenterView cluster="x" />);
    await openClasses();
    fireEvent.click((await screen.findByText("default")).closest(".lens-row") as HTMLElement);
    await screen.findByTestId("karp-class-panel");
    setRows({ classes: [], coverage: cv({ truncated: true, notRetained: 1 }) });
    r.rerender(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("selection-unverified")).textContent).toMatch(/Not in the current EC2NodeClass read\..*unverified/);
    setRows({ classes: [], coverage: cv() });
    r.rerender(<KarpenterView cluster="x" />);
    expect(await screen.findByText(/EC2NodeClass removed\./)).toBeTruthy();
  });

  it("an empty capped class read keeps nothing: never no NodeClasses, the count a lower bound", async () => {
    setRows({ coverage: cv({ truncated: true, notRetained: 3 }) });
    render(<KarpenterView cluster="x" />);
    await openClasses();
    expect(screen.queryByText("No NodeClasses.")).toBeNull();
    expect(screen.getByText("No NodeClasses kept.")).toBeTruthy();
    expect(screen.getByText(/^3 EC2NodeClasses left out by Klyx's budget/)).toBeTruthy();
    expect(screen.getByTestId("karp-classes-count").textContent).toBe("0+");
  });

  it("a capped class read with rows names its omission and qualifies its count", async () => {
    setRows({ classes: [cls()], coverage: cv({ truncated: true, notRetained: 1 }) });
    render(<KarpenterView cluster="x" />);
    await openClasses();
    expect(screen.getByTestId("karp-capped").textContent).toContain("1 EC2NodeClass not retained");
    expect(screen.getByTestId("karp-classes-count").textContent).toBe("1+");
  });

  it("stale class-only rows show the failing read, and the inspector says last observed", async () => {
    setRows({ classes: [cls()], coverage: cv({ coverage: "stale", error: "watch failed", lastGoodAt: "2026-10-20T11:00:00Z" }), watchError: "EC2NodeClass: watch failed" });
    render(<KarpenterView cluster="x" />);
    await openClasses();
    expect(screen.getByRole("alert").textContent).toMatch(/showing the last good state from .*EC2NodeClass: watch failed/);
    expect(screen.getByTestId("karp-classes-count").textContent).toBe("1*");
    fireEvent.click((await screen.findByText("default")).closest(".lens-row") as HTMLElement);
    expect((await screen.findByTestId("karp-class-evidence")).textContent).toMatch(/^Last observed at .* - the NodeClass read is failing now, so this reading is not current\.$/);
  });

  it("a stale empty class read is a last good empty list, not a present absence", async () => {
    setRows({ coverage: cv({ coverage: "stale", error: "watch failed", lastGoodAt: "2026-10-20T11:00:00Z" }), watchError: "EC2NodeClass: watch failed" });
    render(<KarpenterView cluster="x" />);
    await openClasses();
    expect(screen.queryByText("No NodeClasses.")).toBeNull();
    expect(screen.getByText("No NodeClasses in the last good read.")).toBeTruthy();
  });

  it("the class part's own refresh qualifies its view and inspector", async () => {
    const state = { updating: true, refreshing: true, evaluatedAt: "2026-10-20T11:00:00Z", refreshError: "classes bootstrap failed" };
    setRows({ classes: [cls()], coverage: cv(), classesState: state });
    const r = render(<KarpenterView cluster="x" />);
    await openClasses();
    expect(screen.getByTestId("rows-refreshing").textContent).toContain("Last re-read failed: classes bootstrap failed");
    fireEvent.click((await screen.findByText("default")).closest(".lens-row") as HTMLElement);
    expect((await screen.findByTestId("karp-class-evidence")).textContent).toMatch(/^As of .* - the NodeClasses are being re-read\. The last re-read failed: classes bootstrap failed$/);
    r.unmount();
    setRows({ coverage: cv(), classesState: state });
    render(<KarpenterView cluster="x" />);
    await openClasses();
    expect(screen.getByText(/^No NodeClasses as of /)).toBeTruthy();
  });

  it("a historical condition is shown as earlier, beside the reading", async () => {
    const note = "AMIsReady False, reported for generation 1 (the NodeClass is at 2): AMINotFound: no AMI matched the selector terms, or it is not usable";
    setRows({ classes: [cls({ reading: { state: "not ready", text: "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms", note } })], coverage: cv() });
    render(<KarpenterView cluster="x" />);
    await openClasses();
    fireEvent.click((await screen.findByText("default")).closest(".lens-row") as HTMLElement);
    expect((await screen.findByTestId("karp-class-note")).textContent).toBe(`Earlier: ${note}.`);
  });
});
