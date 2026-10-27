import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ThemeProvider } from "../../theme/ThemeProvider";
import { SettingsView } from "../../app/SettingsView";
import { useFleet } from "../../store/fleet";
import { _resetNavGuardForTest, resolveLeave, useLeaveRequest } from "../navGuard";

// Codex's re-review of S2c (2026-10-27): its probes, RR1 to RR3, as
// regression tests - transitions that cross clusters or keep an editor
// open - and an action on the edited cluster asking first.

const b = vi.hoisted(() => ({ getFleetConfig: vi.fn(), getClusterEntry: vi.fn(), planClusterEdit: vi.fn(), planClusterRemove: vi.fn(), applyClusterPlan: vi.fn() }));
vi.mock("../../bridge/configsvc", () => ({ getFleetConfig: () => b.getFleetConfig(), addClusters: vi.fn(), refreshNewContextCount: vi.fn(), armProtected: vi.fn() }));
vi.mock("../../bridge/fleetPlans", () => ({ getClusterEntry: (...a: unknown[]) => b.getClusterEntry(...a), planClusterEdit: (...a: unknown[]) => b.planClusterEdit(...a), planClusterRemove: (...a: unknown[]) => b.planClusterRemove(...a), applyClusterPlan: (...a: unknown[]) => b.applyClusterPlan(...a) }));
vi.mock("./panels", () => ({ AISection: () => null, AppearanceSection: () => null, GitSection: () => null, NotificationsSection: () => null, ObservabilitySection: () => null }));
const row = { name: "dev", context: "dev-ctx", env: "", region: "", provider: "", account: "", group: "", protected: false, hasMetrics: false };
const cfg = { path: "/tmp/fleet.yaml", kubeconfigPath: "/tmp/kubeconfig", warnings: [], contexts: [], clusters: [row] };
const entry = { name: "dev", draft: { context: "dev-ctx", kubeconfig: "", group: "", environment: "", tags: {}, protected: false }, inFile: true, protectedNow: false, contexts: [], endpoints: [] };
const plan = { section: "fleet", kind: "save", cluster: "dev", class: "placement", digest: "d1", fileVersion: "v1", runtime: {}, changes: [{ id: "fleet.group", label: "Group", from: "", to: "new" }], ends: [], continues: [], writes: true, activates: true };
beforeEach(() => { vi.clearAllMocks(); _resetNavGuardForTest(); useFleet.setState({ route: { name: "settings", section: "fleet" }, clusters: [{ name: "dev", state: "Connecting" } as never] }); });

async function open() {
  fireEvent.click(await screen.findByRole("button", { name: "edit dev" }));
  return screen.findByTestId("cluster-editor");
}

describe("Settings > Fleet, across clusters and an open editor", () => {
  it("keeps a dirty dev draft when a different cluster's removal applies", async () => {
    b.getFleetConfig.mockResolvedValueOnce({ ...cfg, clusters: [row, { ...row, name: "test", context: "test-ctx" }] }).mockResolvedValue(cfg);
    b.getClusterEntry.mockResolvedValue(entry);
    b.planClusterRemove.mockResolvedValue({ ...plan, kind: "remove", class: "removal", cluster: "test", changes: [{ id: "fleet.cluster", label: "Cluster", from: "test", to: "removed" }] });
    b.applyClusterPlan.mockResolvedValue({ ok: true });
    render(<ThemeProvider><SettingsView /></ThemeProvider>);
    fireEvent.change(within(await open()).getByLabelText("Group"), { target: { value: "not-applied" } });
    fireEvent.click(screen.getByRole("button", { name: "remove test" }));
    await screen.findByTestId("settings-plan");
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(b.getFleetConfig).toHaveBeenCalledTimes(2));
    expect(useLeaveRequest.getState().request).toBeNull();
    expect(screen.getByTestId("cluster-editor")).toBeTruthy();
    expect((within(screen.getByTestId("cluster-editor")).getByLabelText("Group") as HTMLInputElement).value).toBe("not-applied");
  });

  it("updates an open editor's published protection at release while keeping its dirty draft", async () => {
    b.getFleetConfig.mockResolvedValueOnce({ ...cfg, clusters: [{ ...row, protected: true, pendingProtected: false, held: "saved, not connected yet: connecting", canRetry: true }] }).mockResolvedValue(cfg);
    b.getClusterEntry.mockResolvedValueOnce({ ...entry, protectedNow: true, pendingProtected: false }).mockResolvedValue(entry);
    render(<ThemeProvider><SettingsView /></ThemeProvider>);
    const ed = await open();
    fireEvent.change(within(ed).getByLabelText("Group"), { target: { value: "keep-my-draft" } });
    expect(screen.getByTestId("cluster-protection-now").textContent).toContain("Protected now");
    act(() => useFleet.getState().setClusters([{ name: "dev", state: "Synced" } as never]));
    await waitFor(() => expect(screen.queryByTestId("settings-held")).toBeNull());
    expect((within(screen.getByTestId("cluster-editor")).getByLabelText("Group") as HTMLInputElement).value).toBe("keep-my-draft");
    expect(screen.getByTestId("cluster-protection-now").textContent).toBe("Not protected now.");
  });

  it("stops claiming a successful reconnect is still pending after its release", async () => {
    const held = { ...cfg, clusters: [{ ...row, context: "dev-2", held: "saved, not connected yet: connecting", canRetry: true }] };
    b.getFleetConfig.mockResolvedValueOnce(cfg).mockResolvedValueOnce(held).mockResolvedValue({ ...cfg, clusters: [{ ...row, context: "dev-2" }] });
    b.getClusterEntry.mockResolvedValue(entry);
    b.planClusterEdit.mockResolvedValue({ ...plan, class: "reconnect", changes: [{ id: "fleet.context", label: "Context", from: "dev-ctx", to: "dev-2" }] });
    b.applyClusterPlan.mockResolvedValue({ ok: true });
    render(<ThemeProvider><SettingsView /></ThemeProvider>);
    fireEvent.change(within(await open()).getByLabelText("Context"), { target: { value: "dev-2" } });
    fireEvent.click(within(screen.getByTestId("cluster-editor")).getByRole("button", { name: "Review and apply" }));
    await screen.findByTestId("settings-plan");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await screen.findByTestId("settings-held");
    await waitFor(() => expect((screen.getByRole("button", { name: "edit dev" }) as HTMLButtonElement).disabled).toBe(false));
    act(() => useFleet.getState().setClusters([{ name: "dev", state: "Synced" } as never]));
    await waitFor(() => expect(screen.queryByTestId("settings-held")).toBeNull());
    expect(screen.getByTestId("settings-banner").textContent).not.toContain("not connected yet");
    expect(screen.getByTestId("settings-banner").textContent).not.toContain("Nothing new starts");
  });

  it("asks before an action on the cluster whose editor holds a changed draft", async () => {
    b.getFleetConfig.mockResolvedValue(cfg);
    b.getClusterEntry.mockResolvedValue(entry);
    b.planClusterRemove.mockResolvedValue({ ...plan, kind: "remove", class: "removal", changes: [{ id: "fleet.cluster", label: "Cluster", from: "dev", to: "removed" }] });
    render(<ThemeProvider><SettingsView /></ThemeProvider>);
    fireEvent.change(within(await open()).getByLabelText("Group"), { target: { value: "not-applied" } });
    fireEvent.click(screen.getByRole("button", { name: "remove dev" }));
    expect(useLeaveRequest.getState().request).not.toBeNull();
    expect(b.planClusterRemove).not.toHaveBeenCalled();
    await act(async () => resolveLeave("stay"));
    expect((within(screen.getByTestId("cluster-editor")).getByLabelText("Group") as HTMLInputElement).value).toBe("not-applied");
    fireEvent.click(screen.getByRole("button", { name: "remove dev" }));
    await act(async () => resolveLeave("discard"));
    await waitFor(() => expect(b.planClusterRemove).toHaveBeenCalledWith("dev", []));
    expect(screen.queryByTestId("cluster-editor")).toBeNull();
  });
});
