import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, ExpiryScanResultDTO } from "../store/fleet";
import { FleetExpiry } from "./FleetExpiry";

vi.mock("../bridge/expiry", () => ({
  getExpiryScan: vi.fn(async () => ({ items: [], warnings: [] }) as ExpiryScanResultDTO),
}));
import { getExpiryScan } from "../bridge/expiry";

const cl = (name: string): ClusterDTO => ({ name, env: "dev" } as unknown as ClusterDTO);
const days = (n: number) => Math.floor(Date.now() / 1000) + n * 86400;

describe("FleetExpiry", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ clusters: [cl("homelab"), cl("prd-weu")] });
  });

  it("merges clusters soonest-first and tones the near-expiry rows", async () => {
    vi.mocked(getExpiryScan).mockImplementation(async (cluster: string) =>
      cluster === "homelab"
        ? { items: [{ source: "webhook-ca", namespace: "", name: "cm-webhook", detail: "webhook.cert-manager.io", subject: "CN=ca", notAfterUnix: days(10) }], warnings: [] }
        : { items: [{ source: "tls-secret", namespace: "team-a", name: "web-tls", detail: "tls.crt", subject: "CN=web", notAfterUnix: days(80) }], warnings: [] },
    );
    const { findByText, getByText } = render(<FleetExpiry onClose={() => {}} />);
    expect(await findByText("10d")).toBeTruthy();
    expect(getByText("80d")).toBeTruthy();
    expect(getByText("cm-webhook")).toBeTruthy();
    expect(getByText("team-a/web-tls")).toBeTruthy();
    expect(getByText(/NO renewer/)).toBeTruthy();
  });

  it("window filter narrows; the clear state names the horizon", async () => {
    useFleet.setState({ clusters: [cl("homelab")] });
    vi.mocked(getExpiryScan).mockResolvedValue({
      items: [{ source: "tls-secret", namespace: "x", name: "far-tls", detail: "tls.crt", subject: "CN=far", notAfterUnix: days(200) }],
      warnings: [],
    });
    const { findByText, getByText, findByTestId } = render(<FleetExpiry onClose={() => {}} />);
    // Default 90d window: the 200d cert is outside it.
    const clear = await findByTestId("expiry-clear");
    expect(clear.textContent).toContain("nothing expires within 90 days");
    fireEvent.click(getByText("all"));
    expect(await findByText("x/far-tls")).toBeTruthy();
  });

  it("expired items render as overdue", async () => {
    useFleet.setState({ clusters: [cl("homelab")] });
    vi.mocked(getExpiryScan).mockResolvedValue({
      items: [{ source: "webhook-ca", namespace: "", name: "dead-webhook", detail: "hook", subject: "CN=dead", notAfterUnix: days(-3) }],
      warnings: [],
    });
    const { findByText } = render(<FleetExpiry onClose={() => {}} />);
    expect(await findByText("3d ago")).toBeTruthy();
  });

  it("per-source warnings and cluster errors surface as partial-scan notes", async () => {
    vi.mocked(getExpiryScan).mockImplementation(async (cluster: string) =>
      cluster === "homelab"
        ? { items: [], warnings: ["tls secrets: forbidden"] }
        : { items: [], warnings: [], error: "cluster not connected: prd-weu" },
    );
    const { findByText } = render(<FleetExpiry onClose={() => {}} />);
    expect(await findByText(/partial scan — homelab: tls secrets: forbidden/)).toBeTruthy();
    expect(await findByText(/prd-weu: cluster not connected/)).toBeTruthy();
  });
});
