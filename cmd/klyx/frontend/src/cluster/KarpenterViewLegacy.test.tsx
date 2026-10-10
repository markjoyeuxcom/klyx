import { describe, it, expect, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import type { KarpenterSnapshotDTO } from "../store/fleet";
import { KarpenterViewLegacy as KarpenterView } from "./KarpenterViewLegacy";

vi.mock("../bridge/karpenter", () => ({
  getKarpenterState: vi.fn(async () => ({}) as KarpenterSnapshotDTO),
}));
import { getKarpenterState } from "../bridge/karpenter";

const snap = (over: Partial<KarpenterSnapshotDTO> = {}): KarpenterSnapshotDTO => ({
  detected: true, controllerHealthy: true, pools: [], claims: [], events: [], ...over,
});

describe("KarpenterView", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders pools with capacity vs limits and claim identity", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({
      pools: [{ name: "workers", ready: "ready", reason: "", message: "", weight: 10, cpuLimit: 100, cpuUsed: 90, memLimit: null, memUsed: null, nodes: 3 }],
      claims: [{ name: "wk-1", pool: "workers", nodeName: "wk-1-node", ready: "ready", failingType: "", failingReason: "", failingMessage: "", drifted: true, consolidatable: false, capacityType: "spot", instanceType: "m7g.large", ageSeconds: 7200 }],
    }));
    const { findByTestId, getByText } = render(<KarpenterView cluster="c" />);
    const pool = await findByTestId("karp-pool-workers");
    expect(pool.textContent).toContain("3 nodes");
    // 90/100 = 90% of limit -> warned.
    expect(pool.textContent).toContain("cpu 90% of limit");
    const claim = await findByTestId("karp-claim-wk-1");
    expect(getByText("m7g.large")).toBeTruthy();
    // "spot" also appears as a meters label; assert the claim's own chip.
    expect(claim.textContent).toContain("spot");
    expect(claim.textContent).toContain("drifted");
  });

  it("names the failing launch-chain link on a stuck claim", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({
      claims: [{ name: "stuck-1", pool: "workers", nodeName: "", ready: "not-ready", failingType: "Registered", failingReason: "NodeNotFound", failingMessage: "node did not join within 15m", drifted: false, consolidatable: false, capacityType: "", instanceType: "", ageSeconds: 900 }],
    }));
    const { findByText, getByText } = render(<KarpenterView cluster="c" />);
    expect(await findByText(/stuck at Registered: NodeNotFound/)).toBeTruthy();
    expect(getByText(/node did not join within 15m/)).toBeTruthy();
  });

  it("an unhealthy controller is the headline, not a footnote", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ controllerHealthy: false }));
    const { findByTestId } = render(<KarpenterView cluster="c" />);
    const banner = await findByTestId("karp-controller-down");
    expect(banner.textContent).toContain("NOT ready");
  });

  it("not-detected and errors render honestly", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({ detected: false }));
    const { findByText } = render(<KarpenterView cluster="c" />);
    expect(await findByText(/Karpenter not detected/)).toBeTruthy();

    vi.mocked(getKarpenterState).mockResolvedValue({ ...snap(), error: "list nodepools: forbidden" });
    const { findByText: f2 } = render(<KarpenterView cluster="c" />);
    expect(await f2(/list nodepools: forbidden/)).toBeTruthy();
  });

  it("renders the disruption feed", async () => {
    vi.mocked(getKarpenterState).mockResolvedValue(snap({
      events: [{ type: "Normal", reason: "DisruptionTerminating", message: "disrupting via consolidation delete", kind: "NodeClaim", name: "wk-old", lastSeenUnix: 900 }],
    }));
    const { findByTestId } = render(<KarpenterView cluster="c" />);
    const feed = await findByTestId("karp-events");
    expect(feed.textContent).toContain("DisruptionTerminating");
    expect(feed.textContent).toContain("NodeClaim/wk-old");
  });
});
