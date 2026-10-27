import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ThemeProvider } from "../../theme/ThemeProvider";
import { SettingsView } from "../../app/SettingsView";
import { useFleet } from "../../store/fleet";
import { _resetNavGuardForTest } from "../navGuard";

// Codex's review of S2c (2026-10-27), R3 and R4, through SettingsView: the
// page reads the fleet again after an Apply and while a cluster is held,
// and a failed read keeps what the page was saying.

const b = vi.hoisted(() => ({ getFleetConfig: vi.fn(), getClusterEntry: vi.fn(), planClusterEdit: vi.fn(), applyClusterPlan: vi.fn() }));
vi.mock("../../bridge/configsvc", () => ({ getFleetConfig: () => b.getFleetConfig(), addClusters: vi.fn(), refreshNewContextCount: vi.fn(), armProtected: vi.fn() }));
vi.mock("../../bridge/fleetPlans", () => ({ getClusterEntry: () => b.getClusterEntry(), planClusterEdit: () => b.planClusterEdit(), applyClusterPlan: () => b.applyClusterPlan() }));
vi.mock("./panels", () => ({ AISection: () => null, AppearanceSection: () => null, GitSection: () => null, NotificationsSection: () => null, ObservabilitySection: () => null }));

const cfg = { path: "/tmp/fleet.yaml", kubeconfigPath: "/tmp/kubeconfig", warnings: [], contexts: [], clusters: [{ name: "dev", context: "dev-ctx", env: "", region: "", provider: "", account: "", group: "", protected: false, hasMetrics: false }] };

beforeEach(() => {
  vi.clearAllMocks();
  _resetNavGuardForTest();
  useFleet.setState({ route: { name: "settings", section: "fleet" }, clusters: [{ name: "dev", state: "Connecting" } as never] });
});

describe("Settings > Fleet, read again", () => {
  it("R4: a held notice follows the connection's release while the page stays open", async () => {
    b.getFleetConfig.mockResolvedValueOnce({ ...cfg, clusters: [{ ...cfg.clusters[0], held: "saved, not connected yet: connecting", canRetry: true }] }).mockResolvedValue(cfg);
    render(<ThemeProvider><SettingsView /></ThemeProvider>);
    await screen.findByTestId("settings-held");
    expect(b.getFleetConfig).toHaveBeenCalledTimes(1); // no read on mount beyond the first
    act(() => useFleet.getState().setClusters([{ name: "dev", state: "Synced" } as never]));
    await waitFor(() => expect(screen.queryByTestId("settings-held")).toBeNull(), { timeout: 1500 });
  });

  it("R3: a failed read after an Apply whose answer was lost keeps both facts", async () => {
    b.getFleetConfig.mockResolvedValueOnce(cfg).mockResolvedValue(null);
    b.getClusterEntry.mockResolvedValue({ name: "dev", draft: { context: "dev-ctx", kubeconfig: "", group: "", environment: "", tags: {}, protected: false }, inFile: true, protectedNow: false, contexts: [], endpoints: [] });
    b.planClusterEdit.mockResolvedValue({ section: "fleet", kind: "save", cluster: "dev", class: "placement", digest: "d1", fileVersion: "v1", runtime: {}, changes: [{ id: "fleet.group", label: "Group", from: "", to: "new" }], ends: [], continues: [], writes: true, activates: true });
    b.applyClusterPlan.mockResolvedValue({ ok: false, unknown: true, error: "bridge closed" });
    render(<ThemeProvider><SettingsView /></ThemeProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "edit dev" }));
    const ed = await screen.findByTestId("cluster-editor");
    fireEvent.change(within(ed).getByLabelText("Group"), { target: { value: "new" } });
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    await screen.findByTestId("settings-plan");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(b.getFleetConfig).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByTestId("settings-banner").textContent).toContain("whether it applied is not known, and reading the fleet again failed"));
    expect(screen.getByTestId("settings-reread-failed").textContent).toContain("Could not read the fleet configuration again");
    expect(screen.getByRole("button", { name: "edit dev" })).toBeTruthy(); // the rows read before stay
  });

  it("R3: an older read never replaces a newer one", async () => {
    const held = (group: string) => ({ ...cfg, clusters: [{ ...cfg.clusters[0], group, held: "saved, not connected yet: connecting", canRetry: true }] });
    let slow: (v: unknown) => void = () => {};
    b.getFleetConfig.mockResolvedValueOnce(held("first")).mockReturnValueOnce(new Promise((r) => { slow = r; })).mockResolvedValue(held("newer"));
    render(<ThemeProvider><SettingsView /></ThemeProvider>);
    await screen.findByText("first");
    act(() => useFleet.getState().setClusters([{ name: "dev", state: "Synced" } as never])); // the slow read
    act(() => useFleet.getState().setClusters([{ name: "dev", state: "Connecting" } as never])); // a newer one
    await screen.findByText("newer");
    await act(async () => slow(held("older")));
    expect(screen.queryByText("older")).toBeNull();
    expect(screen.getByText("newer")).toBeTruthy();
  });
});
