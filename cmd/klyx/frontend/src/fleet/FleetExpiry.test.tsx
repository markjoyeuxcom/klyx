import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act, screen } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, ExpiryScanResultDTO } from "../store/fleet";
import { FleetExpiry } from "./FleetExpiry";
import { resetExpiryStore } from "../store/expiry";

vi.mock("../bridge/expiry", () => ({
  getExpiryScan: vi.fn(async () => ({ items: [], warnings: [] }) as ExpiryScanResultDTO),
}));
import { getExpiryScan } from "../bridge/expiry";

const cl = (name: string): ClusterDTO => ({ name, env: "dev" } as unknown as ClusterDTO);
// half a day past n whole days: the lens floors (notAfter - now) to days,
// and its now is read at render, so a fixture on the exact boundary read a
// second later fell a day short
const days = (n: number) => Math.floor(Date.now() / 1000) + n * 86400 + 43200;

describe("FleetExpiry", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetExpiryStore();
    useFleet.setState({ clusters: [cl("homelab"), cl("prd-weu")] });
  });

  it("merges clusters soonest-first and tones the near-expiry rows", async () => {
    vi.mocked(getExpiryScan).mockImplementation(async (cluster: string) =>
      cluster === "homelab"
        ? { items: [{ source: "webhook-ca", namespace: "", name: "cm-webhook", detail: "webhook.cert-manager.io", subject: "CN=ca", notAfterUnix: days(10) }], warnings: [] }
        : { items: [{ source: "tls-secret", namespace: "team-a", name: "web-tls", detail: "tls.crt", subject: "CN=web", notAfterUnix: days(80) }], warnings: [] },
    );
    const { findByText, getByText } = render(<FleetExpiry />);
    expect(await findByText("▲ 10d", {}, { timeout: 3000 })).toBeTruthy();
    expect(getByText("80d")).toBeTruthy();
    expect(getByText("cm-webhook")).toBeTruthy();
    expect(getByText("team-a/web-tls")).toBeTruthy();
    expect(getByText(/have no renewer/)).toBeTruthy();
  });

  it("window filter narrows; an empty window is neutral and scoped, never 'nothing expires' (step 7d)", async () => {
    useFleet.setState({ clusters: [cl("homelab")] });
    vi.mocked(getExpiryScan).mockResolvedValue({
      items: [{ source: "tls-secret", namespace: "x", name: "far-tls", detail: "tls.crt", subject: "CN=far", notAfterUnix: days(200) }],
      warnings: [],
    });
    const { findByText, getByText, queryByText } = render(<FleetExpiry />);
    // Default 90d window: the 200d cert is outside it.
    expect(await findByText("Nothing found expiring within 90 days.", {}, { timeout: 3000 })).toBeTruthy();
    expect(getByText(/On the 1 of 1 cluster scanned/)).toBeTruthy();
    expect(queryByText(/nothing expires/i)).toBeNull();
    fireEvent.click(getByText("All"));
    expect(await findByText("x/far-tls", {}, { timeout: 3000 })).toBeTruthy();
  });

  it("leaves out a Subject column no row has, and says so (UI review 2026-10-13)", async () => {
    useFleet.setState({ clusters: [cl("homelab")] });
    vi.mocked(getExpiryScan).mockResolvedValue({
      items: [{ source: "webhook-ca", namespace: "", name: "hook-ca", detail: "hook", subject: "", notAfterUnix: days(30) }],
      warnings: [],
    });
    const { findByText, queryByText, getByText } = render(<FleetExpiry />);
    expect(await findByText("hook-ca", {}, { timeout: 3000 })).toBeTruthy();
    expect(queryByText("Subject")).toBeNull();
    expect(getByText(/no subject reported for these rows/)).toBeTruthy();
  });

  it("keeps the Subject column when any row has one", async () => {
    useFleet.setState({ clusters: [cl("homelab")] });
    vi.mocked(getExpiryScan).mockResolvedValue({
      items: [
        { source: "tls-secret", namespace: "a", name: "with-subject", detail: "tls.crt", subject: "CN=web", notAfterUnix: days(20) },
        { source: "webhook-ca", namespace: "", name: "no-subject", detail: "hook", subject: "", notAfterUnix: days(40) },
      ],
      warnings: [],
    });
    const { findByText, getByText, queryByText } = render(<FleetExpiry />);
    expect(await findByText("CN=web", {}, { timeout: 3000 })).toBeTruthy();
    expect(getByText("Subject")).toBeTruthy();
    expect(queryByText(/no subject reported/)).toBeNull();
  });

  it("expired items render as overdue", async () => {
    useFleet.setState({ clusters: [cl("homelab")] });
    vi.mocked(getExpiryScan).mockResolvedValue({
      items: [{ source: "webhook-ca", namespace: "", name: "dead-webhook", detail: "hook", subject: "CN=dead", notAfterUnix: days(-3) }],
      warnings: [],
    });
    const { findByText } = render(<FleetExpiry />);
    expect(await findByText("▲ 3d ago", {}, { timeout: 3000 })).toBeTruthy();
  });

  it("per-source warnings and cluster errors surface as partial-scan notes", async () => {
    vi.mocked(getExpiryScan).mockImplementation(async (cluster: string) =>
      cluster === "homelab"
        ? { items: [], warnings: ["tls secrets: forbidden"] }
        : { items: [], warnings: [], error: "cluster not connected: prd-weu" },
    );
    const { findByText, findAllByText } = render(<FleetExpiry />);
    expect(await findByText(/Partial scan - homelab: tls secrets: forbidden/, {}, { timeout: 3000 })).toBeTruthy();
    expect(await findByText(/Could not scan prd-weu: cluster not connected/, {}, { timeout: 3000 })).toBeTruthy();
    // the empty result names both the unread cluster and the partial one
    expect((await findAllByText(/1 of 2 clusters scanned, 1 partially/)).length).toBeGreaterThan(0);
  });

  // The Line slice 5 §6 (design review d25f7275): the session's shared
  // scans - a joining cluster is scanned once when it joins, the others are
  // not rescanned for it, a leaving one is dropped; Refresh scans anew.
  it("a joining cluster is scanned once; nobody else is rescanned; Refresh scans the current fleet", async () => {
    useFleet.setState({ clusters: [cl("homelab")] });
    render(<FleetExpiry />);
    await act(async () => { await new Promise<void>((r) => setTimeout(r, 0)); });
    act(() => { useFleet.setState({ clusters: [cl("homelab"), cl("prd-weu")] }); });
    await act(async () => { await new Promise<void>((r) => setTimeout(r, 0)); });
    expect(vi.mocked(getExpiryScan).mock.calls.map((c) => c[0])).toEqual(["homelab", "prd-weu"]);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await act(async () => { await new Promise<void>((r) => setTimeout(r, 0)); });
    expect(vi.mocked(getExpiryScan).mock.calls.map((c) => c[0])).toEqual(["homelab", "prd-weu", "homelab", "prd-weu"]);
  });

  it("a rejected scan is a named failure with Retry, never a hang", async () => {
    useFleet.setState({ clusters: [cl("homelab")] });
    vi.mocked(getExpiryScan).mockRejectedValueOnce(new Error("timeout"));
    render(<FleetExpiry />);
    expect(await screen.findByText(/Could not scan homelab: timeout/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});
