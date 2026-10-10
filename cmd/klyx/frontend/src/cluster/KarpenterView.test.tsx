import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, act, within } from "@testing-library/react";
import type { KarpenterClaimDTO, KarpenterSnapshotDTO } from "../store/fleet";
import { KarpenterView, claimHealth, LAUNCH_DEADLINE_S } from "./KarpenterView";

vi.mock("../bridge/karpenter", () => ({ getKarpenterState: vi.fn(async () => ({}) as KarpenterSnapshotDTO) }));
import { getKarpenterState } from "../bridge/karpenter";

const snap = (over: Partial<KarpenterSnapshotDTO> = {}): KarpenterSnapshotDTO => ({ detected: true, controllerHealthy: true, pools: [], claims: [], events: [], ...over });
const claim = (over: Partial<KarpenterClaimDTO> = {}): KarpenterClaimDTO => ({
  name: "default-abcde", pool: "default", nodeName: "ip-10-0-1-1", ready: "ready", failingType: "", failingReason: "", failingMessage: "",
  drifted: false, consolidatable: false, capacityType: "spot", instanceType: "m6i.large", ageSeconds: 3600, ...over,
});
type D<T> = { promise: Promise<T>; resolve: (v: T) => void };
const deferred = <T,>(): D<T> => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => (resolve = r)); return { promise, resolve }; };
const rows = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("claimHealth", () => {
  it("False chain = failed; Unknown = launching until the 15m heuristic, then appears stuck", () => {
    expect(claimHealth(claim()).kind).toBe("ok");
    expect(claimHealth(claim({ ready: "not-ready", failingType: "Launched" }))).toEqual({ kind: "fault", label: "failed at Launched" });
    expect(claimHealth(claim({ ready: "unknown", ageSeconds: 60 })).kind).toBe("progressing");
    expect(claimHealth(claim({ ready: "unknown", ageSeconds: LAUNCH_DEADLINE_S + 1 }))).toEqual({ kind: "degraded", label: "launching, appears stuck" });
  });
});

describe("KarpenterView (redesign step 5)", () => {
  beforeEach(() => vi.clearAllMocks());

  it("stuck first: failed, then appears stuck, then launching, then ready; header counts", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [
      claim({ name: "ok" }),
      claim({ name: "young", ready: "unknown", ageSeconds: 30, nodeName: "" }),
      claim({ name: "old", ready: "unknown", ageSeconds: 3600, nodeName: "" }),
      claim({ name: "bad", ready: "not-ready", failingType: "Launched", failingReason: "InsufficientCapacity" }),
    ] }));
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("▲ 1 failed · 1 launching, appears stuck")).toBeTruthy();
    const r = rows();
    expect(r[0]).toContain("bad");
    expect(r[1]).toContain("old");
    expect(r[2]).toContain("young");
    expect(r[3]).toContain("ok");
  });

  it("a failed claim names the failing link and its message in words; Open node when registered", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim({ ready: "not-ready", failingType: "Registered", failingReason: "NodeNotFound", failingMessage: "node did not join within 15m" })] }));
    render(<KarpenterView cluster="x" />);
    fireEvent.click((await screen.findByText("default-abcde")).closest(".lens-row") as HTMLElement);
    const p = within(screen.getByTestId("lens-inspector"));
    expect(p.getByText("The launch chain stopped at Registered: NodeNotFound.")).toBeTruthy();
    expect(p.getByText("node did not join within 15m")).toBeTruthy();
    expect(p.getByRole("button", { name: "Open node" })).toBeTruthy();
  });

  it("drift and consolidation are intended behaviour, not faults", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ claims: [claim({ drifted: true, consolidatable: true })] }));
    render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("all ready")).toBeTruthy();
    expect(screen.getByText("1 drifted (will be replaced)")).toBeTruthy();
    fireEvent.click(screen.getByText("default-abcde").closest(".lens-row") as HTMLElement);
    expect(within(screen.getByTestId("lens-inspector")).getByText("Intended behaviour, not a fault.")).toBeTruthy();
  });

  it("an unhealthy controller is the headline", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerHealthy: false, claims: [claim()] }));
    render(<KarpenterView cluster="x" />);
    expect((await screen.findByTestId("karp-controller-down")).textContent).toContain("The Karpenter controller is not ready.");
  });

  it("pools show capacity against limits and warn near a limit", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ pools: [{ name: "default", ready: "ready", reason: "", message: "", weight: 10, cpuLimit: 100, cpuUsed: 95, memLimit: null, memUsed: 1024 ** 3, nodes: 3 }] }));
    render(<KarpenterView cluster="x" />);
    await screen.findByText("all ready");
    fireEvent.click(screen.getByRole("button", { name: /^NodePools/ }));
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    expect(within(screen.getByTestId("lens-inspector")).getByText(/Karpenter stops provisioning from this pool/)).toBeTruthy();
  });

  it("not installed, errors and empty claims are distinct states", async () => {
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap({ detected: false }));
    const first = render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("Karpenter is not installed on this cluster.")).toBeTruthy();
    first.unmount();
    vi.mocked(getKarpenterState).mockResolvedValueOnce(snap({ error: "nodeclaims is forbidden" }));
    const second = render(<KarpenterView cluster="x" />);
    expect(await screen.findByText("Could not read Karpenter state.")).toBeTruthy();
    second.unmount();
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
    vi.mocked(getKarpenterState).mockReturnValueOnce(late.promise).mockResolvedValueOnce(snap({ claims: [claim({ name: "from-b" })] }));
    const { rerender } = render(<KarpenterView cluster="a" />);
    rerender(<KarpenterView cluster="b" />);
    expect(await screen.findByText("from-b")).toBeTruthy();
    await act(async () => { late.resolve(snap({ claims: [claim({ name: "from-a" })] })); await late.promise; });
    expect(screen.queryByText("from-a")).toBeNull();
  });
});
