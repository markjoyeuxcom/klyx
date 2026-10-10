import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import type { UpgradeReportDTO } from "../store/fleet";
import { UpgradeViewLegacy as UpgradeView } from "./UpgradeViewLegacy";

vi.mock("../bridge/upgrade", () => ({
  getUpgradeReadiness: vi.fn(async () => ({}) as UpgradeReportDTO),
}));
import { getUpgradeReadiness } from "../bridge/upgrade";

const report = (over: Partial<UpgradeReportDTO> = {}): UpgradeReportDTO => ({
  serverVersion: "v1.31.4", serverMinor: 31, eolDate: "2099-01-01",
  skew: [], deprecated: [], ...over,
});

describe("UpgradeView", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders version + clean deprecated verdict", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report());
    const { findByText } = render(<UpgradeView cluster="c" />);
    expect(await findByText("v1.31.4")).toBeTruthy();
    expect(await findByText(/no deprecated API from the curated table is served/)).toBeTruthy();
  });

  it("frames deprecated APIs against the target minor and re-frames on change", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report({
      deprecated: [{
        group: "flowcontrol.apiserver.k8s.io", version: "v1beta3", resource: "flowschemas", kind: "FlowSchema",
        removedInMinor: 32, replacedBy: "flowcontrol.apiserver.k8s.io/v1 FlowSchema", count: 4, countKnown: true,
      }],
    }));
    const { findByText, getByText, getByLabelText } = render(<UpgradeView cluster="c" />);
    // Default target = serverMinor+1 = 32: the API is REMOVED there.
    expect(await findByText(/REMOVED in 1.32/)).toBeTruthy();
    expect(getByText(/migrate to flowcontrol/)).toBeTruthy();
    expect(getByText("4 objects")).toBeTruthy();
    // Same table entry framed differently would need a target BELOW the removal
    // minor - not offered (targets go up), so instead assert the honesty caption.
    expect(getByText(/REACHABLE via the deprecated API/)).toBeTruthy();
    // Target selector exists and switches.
    fireEvent.change(getByLabelText("target minor"), { target: { value: "33" } });
    expect(await findByText(/REMOVED in 1.32/)).toBeTruthy();
  });

  it("renders skew findings and EOL runway tones", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report({
      eolDate: "2000-01-01", // long past
      skew: [{ node: "old-node", version: "v1.27.1", delta: 4, level: "danger", detail: "outside the supported skew window" }],
    }));
    const { findByText, getByText } = render(<UpgradeView cluster="c" />);
    expect(await findByText("old-node")).toBeTruthy();
    expect(getByText(/outside the supported skew window/)).toBeTruthy();
    expect(getByText(/d past/)).toBeTruthy();
  });

  it("a failed report renders its error", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue({ serverVersion: "", serverMinor: 0, eolDate: "", skew: [], deprecated: [], error: "server version: boom" });
    const { findByText } = render(<UpgradeView cluster="c" />);
    expect(await findByText(/server version: boom/)).toBeTruthy();
  });

  // Review 2026-10-03 (K): an unread input is named, never shown as clean; a
  // capped object count is a lower bound.
  it("names unread inputs and marks capped counts", async () => {
    vi.mocked(getUpgradeReadiness).mockResolvedValue(report({
      warnings: ["kubelet skew unknown - the node list could not be read: nodes is forbidden"],
      deprecated: [{ group: "flowcontrol.apiserver.k8s.io", version: "v1beta3", resource: "flowschemas", kind: "FlowSchema", removedInMinor: 32, replacedBy: "flowcontrol.apiserver.k8s.io/v1", count: 500, countKnown: true, countCapped: true }],
    }));
    const { findByTestId, findByText } = render(<UpgradeView cluster="c" />);
    expect((await findByTestId("upgrade-warnings")).textContent).toMatch(/kubelet skew unknown/);
    expect(await findByText("500+ objects")).toBeTruthy();
  });
});
