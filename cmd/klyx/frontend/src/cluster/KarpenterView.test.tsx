import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, act, within, waitFor } from "@testing-library/react";
import type { KarpenterClaimDTO, KarpenterPoolDTO, KarpenterSnapshotDTO } from "../store/fleet";
import type { KarpenterRows } from "../health/useKarpenterRows";
import { KarpenterView } from "./KarpenterView";
import { claimRule, poolRule, withRule } from "../test/backendRules";

vi.mock("../bridge/karpenter", () => ({ getKarpenterState: vi.fn(async () => ({}) as KarpenterSnapshotDTO) }));
import { getKarpenterState } from "../bridge/karpenter";
// The NodePools and NodeClaims are rows (health/useKarpenterRows): these
// tests hand the lens a live, complete read; the snapshot carries the
// controller probe and the disruption feed.
let rowsValue: KarpenterRows;
vi.mock("../health/useKarpenterRows", () => ({ useKarpenterRows: () => rowsValue }));
const liveCoverage = () => [
  { kind: "NodePool", served: true, synced: true, coverage: "live" as const },
  { kind: "NodeClaim", served: true, synced: true, coverage: "live" as const },
];
function setRows(r: { claims?: KarpenterClaimDTO[]; pools?: KarpenterPoolDTO[]; coverage?: KarpenterRows["coverage"]; loading?: boolean; watchError?: string | null } = {}) {
  rowsValue = {
    claims: r.claims ?? [], pools: r.pools ?? [], coverage: r.coverage ?? liveCoverage(), watchError: r.watchError ?? null, loading: r.loading ?? false,
    poolNodesDep: { kind: "NodeClaim", served: true, synced: true, coverage: "live" },
    rowsState: { updating: false, refreshing: false, evaluatedAt: null }, partCounts: { nodepools: null, nodeclaims: null },
  };
}
setRows();

const snap = (over: Partial<KarpenterSnapshotDTO> & { claims?: KarpenterClaimDTO[]; pools?: KarpenterPoolDTO[] } = {}): KarpenterSnapshotDTO => {
  // a fixture's claims/pools are the rows the lens reads
  const { claims, pools, ...rest } = over;
  setRows({ claims, pools });
  return { detected: true, controllerVerified: true, controllerHealthy: true, events: [], ...rest };
};
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
  beforeEach(() => { vi.clearAllMocks(); setRows(); });

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

  it("an unhealthy controller is the headline", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerHealthy: false, claims: [claim()] }));
    render(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("karp-controller-down")).textContent).toContain("The Karpenter controller is not ready.");
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
    await act(async () => { late.resolve({ detected: true, controllerVerified: true, controllerHealthy: true, events: [ev("from-a")] }); await late.promise; });
    expect(screen.queryAllByText("from-a")).toHaveLength(0);
  });
});

describe("KarpenterView evidence corrections (Codex 271342a7)", () => {
  beforeEach(() => { vi.clearAllMocks(); setRows(); });

  it("Ready=False with an unproven link names no failure; its message is not presented as the cause", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerVerified: true, claims: [claim({ ready: "not-ready", failingType: "Launched", failingStatus: "Unknown", failingMessage: "waiting" })] }));
    render(<KarpenterView cluster="x" />);
    await screen.findByTestId("lens-inspector"); // the first claim needing attention is auto-selected (UI review 2026-10-13)
    const p = within(screen.getByTestId("lens-inspector"));
    expect(p.getByText("Ready is False, but no launch-chain link is confirmed failed - Launched is Unknown (unverified).")).toBeTruthy();
    expect(p.queryByText("waiting")).toBeNull();
  });

  it("registration expiry is only mentioned for an unregistered claim; a registered one gets an initialization diagnosis", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerVerified: true, claims: [
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
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerVerified: false, controllerHealthy: false, controllerError: "controller Deployment unreadable (kube-system: forbidden)", claims: [claim()] }));
    render(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("karp-controller-unverified")).textContent).toContain("forbidden");
    expect(screen.queryByTestId("karp-controller-down")).toBeNull();
  });

  it("an unreadable activity feed is unreadable, never quiet; a capped one discloses partial coverage", async () => {
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap({ controllerVerified: true, claims: [claim()], eventsError: "NodeClaim events: forbidden; NodePool events: forbidden" }));
    const first = render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^Disruption activity/ }));
    expect(screen.getByText("Could not read disruption activity.")).toBeTruthy();
    expect(screen.queryByText("No recent disruption activity.")).toBeNull();
    first.unmount();
    const ev = { type: "Normal", reason: "Disrupting", message: "m", kind: "NodeClaim", name: "c", lastSeenUnix: Math.floor(Date.now() / 1000) };
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap({ controllerVerified: true, claims: [claim()], events: [ev], eventsTruncated: true }));
    render(<KarpenterView cluster="x" />);
    fireEvent.click(await screen.findByRole("button", { name: /^Disruption activity/ }));
    expect(screen.getByText("Capped read - showing the newest among the events read.")).toBeTruthy();
    expect(screen.queryByText(/more exist/)).toBeNull();
    expect(screen.getByRole("button", { name: /^Disruption activity 1\+/ })).toBeTruthy();
  });

  it("an Unknown NodePool is never folded into 'all ready'", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerVerified: true, claims: [claim()], pools: [withRule<KarpenterPoolDTO>({ name: "p", ready: "unknown", reason: "", message: "", weight: 0, cpuLimit: null, cpuUsed: null, memLimit: null, memUsed: null, nodes: 1 }, poolRule)] }));
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("1 NodePool unverified")).toBeTruthy();
    expect(screen.queryByText("all ready")).toBeNull();
  });
});

describe("KarpenterView composes independent notices (Codex cfbf793c)", () => {
  beforeEach(() => { vi.clearAllMocks(); setRows(); });
  it("a down controller and a partially failed feed are both disclosed, with an incomplete count", async () => {
    const ev = { type: "Normal", reason: "Disrupting", message: "m", kind: "NodeClaim", name: "c", lastSeenUnix: Math.floor(Date.now() / 1000) };
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerVerified: true, controllerHealthy: false, claims: [claim()], events: [ev], eventsError: "NodePool events: forbidden" }));
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
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerVerified: true, claims: [claim({ ready: "not-ready", failingType: "Launched", failingStatus: "False", failingReason: "InsufficientCapacity" })] }));
    render(<KarpenterView cluster="x" />);
    const insp = await screen.findByTestId("lens-inspector");
    expect(within(insp).getAllByText(/default-abcde/).length).toBeGreaterThan(0);
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(screen.queryByTestId("lens-inspector")).toBeNull());
  });
});
