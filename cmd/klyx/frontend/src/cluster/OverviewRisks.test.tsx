import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { Overview } from "./Overview";
import { resetHealthStore } from "../store/health";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";

vi.mock("../bridge/metrics", () => ({
  getClusterMetrics: vi.fn(),
  getClusterSparklines: vi.fn().mockResolvedValue({ available: false, message: "metrics unavailable", cpu: [], mem: [] }),
}));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
vi.mock("../bridge/events", () => ({ fetchEventsSnapshot: vi.fn().mockResolvedValue({ namespaces: [], events: [] }) }));
const listInstancePage = vi.fn();
vi.mock("../bridge/crd", () => ({ listInstancePage: (...a: unknown[]) => listInstancePage(...a) }));

const dto: ClusterDTO = {
  name: "nelli", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 5,
  version: "v1.36.1", gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "homelab", region: "", provider: "k3s", group: "", ageSeconds: 3,
};

// Review 2026-10-03 #10: one failed page used to reject the whole risk scan
// into a confident "no resource risks found".
describe("Overview cluster risks honesty", () => {
  beforeEach(() => {
    listInstancePage.mockReset();
    useFleet.setState({ metrics: { cluster: null, dto: null, loading: false } });
    resetHealthStore();
  });

  it("claims no risks only when every kind was read", async () => {
    listInstancePage.mockResolvedValue({ items: [], nextToken: "" });
    const { findByText, queryByTestId } = render(<Overview c={dto} />);
    // neutral and scoped to the kinds read - never a green all-clear
    const line = await findByText(/No resource risks found in the \d+ kinds read\./);
    expect(line.className).toContain("t-secondary");
    expect(queryByTestId("risks-unreadable")).toBeNull();
  });

  it("names the kinds it could not read instead of claiming none", async () => {
    listInstancePage.mockImplementation(async (_c: string, ref: { kind: string }) => {
      if (ref.kind === "Ingress" || ref.kind === "Job") throw new Error("forbidden");
      return { items: [], nextToken: "" };
    });
    const { findByTestId, queryByText } = render(<Overview c={dto} />);
    const note = await findByTestId("risks-unreadable");
    expect(note.textContent).toMatch(/2 kinds could not be read/);
    expect(note.getAttribute("title")).toBe("Ingress, Job");
    await waitFor(() => expect(queryByText(/No resource risks found/)).toBeNull());
  });
});
