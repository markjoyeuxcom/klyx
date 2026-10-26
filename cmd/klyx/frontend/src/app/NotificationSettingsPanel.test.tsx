import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import type { ApplyResult, Plan } from "../bridge/plan";

// The Line slice 10b: Settings > Notifications - off until enabled, the
// OS permission asked when enabling (a refusal kept and said), scope and
// recoveries only once enabled, a failed save reverted. Settings slice F2b:
// a toggle applies at once through a plan that ends nothing, and says so;
// a hand edit is drift, adopted with "Use the file's values".
type State = { inUse: { enabled: boolean; scope: "protected" | "all"; recoveries: boolean }; diverged: boolean; fileError?: string };
const off = (): State => ({ inUse: { enabled: false, scope: "protected", recoveries: false }, diverged: false });
const bridge = vi.hoisted(() => ({
  getNotificationSettings: vi.fn(async (): Promise<unknown> => null),
  applyNotificationsQuiet: vi.fn(async (): Promise<ApplyResult> => ({ ok: true })),
  planNotificationsFromFile: vi.fn(async (): Promise<unknown> => null),
  applyNotifications: vi.fn(async (): Promise<ApplyResult> => ({ ok: true })),
}));
vi.mock("../bridge/notifications", () => bridge);
import { NotificationSettingsPanel } from "./NotificationSettingsPanel";

const filePlan = (over: Partial<Plan> = {}): Plan => ({
  section: "notifications", kind: "file", digest: "d1", fileVersion: "v1", runtime: { notifications: "0" },
  changes: [{ id: "notifications.enabled", label: "Notify me about new faults", from: "off", to: "on" }],
  ends: [], continues: [], writes: false, activates: true, ...over,
});

beforeEach(() => {
  vi.clearAllMocks();
  bridge.getNotificationSettings.mockImplementation(async () => off());
});

describe("NotificationSettingsPanel", () => {
  it("is off by default, with the scope and recoveries disabled until enabled", async () => {
    await act(async () => { render(<NotificationSettingsPanel />); });
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(false);
    expect(screen.getByLabelText("Every cluster").matches(":disabled")).toBe(true); // the fieldset disables it
    expect((screen.getByLabelText("Protected clusters only") as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByTestId("notifications-diverged")).toBeNull();
  });

  it("a toggle applies at once and says nothing running ends", async () => {
    await act(async () => { render(<NotificationSettingsPanel />); });
    bridge.getNotificationSettings.mockImplementation(async () => ({ ...off(), inUse: { enabled: true, scope: "protected", recoveries: false } }));
    await act(async () => { fireEvent.click(screen.getByLabelText("Notify me about new faults")); });
    expect(bridge.applyNotificationsQuiet).toHaveBeenCalledWith({ enabled: true, scope: "protected", recoveries: false });
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("status").textContent).toBe("Applied: nothing running ends.");
  });

  it("a refused OS permission is kept and said", async () => {
    bridge.applyNotificationsQuiet.mockResolvedValueOnce({ ok: true, note: "the OS has notifications off for Klyx: nothing will be shown until they are allowed" });
    bridge.getNotificationSettings.mockImplementationOnce(async () => off()).mockImplementation(async () => ({ ...off(), inUse: { enabled: true, scope: "protected", recoveries: false } }));
    await act(async () => { render(<NotificationSettingsPanel />); });
    await act(async () => { fireEvent.click(screen.getByLabelText("Notify me about new faults")); });
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("status").textContent).toContain("the OS has notifications off");
    await act(async () => { fireEvent.click(screen.getByLabelText("Every cluster")); });
    expect(bridge.applyNotificationsQuiet).toHaveBeenLastCalledWith({ enabled: true, scope: "all", recoveries: false });
  });

  it("a failed save reverts and says why", async () => {
    bridge.applyNotificationsQuiet.mockResolvedValueOnce({ ok: false, error: "fleet.yaml changed on disk" });
    await act(async () => { render(<NotificationSettingsPanel />); });
    await act(async () => { fireEvent.click(screen.getByLabelText("Notify me about new faults")); });
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(false);
    expect(screen.getByRole("alert").textContent).toContain("fleet.yaml changed on disk");
  });

  it("a hand edit is drift: Use the file's values shows its plan, and Apply applies that digest", async () => {
    bridge.getNotificationSettings.mockImplementation(async () => ({ ...off(), diverged: true }));
    bridge.planNotificationsFromFile.mockResolvedValueOnce(filePlan());
    await act(async () => { render(<NotificationSettingsPanel />); });
    expect(screen.getByTestId("notifications-diverged").textContent).toContain("differ from the ones in use");
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Use the file's values" })); });
    expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("d1");
    expect(screen.getByTestId("plan-changes").textContent).toContain("Notify me about new faults: off → on");
    expect(screen.getByTestId("plan-ends-none")).toBeTruthy();
    expect(bridge.applyNotifications).not.toHaveBeenCalled();
    bridge.getNotificationSettings.mockImplementation(async () => ({ inUse: { enabled: true, scope: "protected", recoveries: false }, diverged: false }));
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Apply" })); });
    expect(bridge.applyNotifications).toHaveBeenCalledWith("d1");
    expect(screen.queryByTestId("settings-plan")).toBeNull();
    expect(screen.queryByTestId("notifications-diverged")).toBeNull();
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(true);
  });

  it("a replacement plan is shown unapplied, keyed by its own digest", async () => {
    bridge.getNotificationSettings.mockImplementation(async () => ({ ...off(), diverged: true }));
    bridge.planNotificationsFromFile.mockResolvedValueOnce(filePlan());
    bridge.applyNotifications.mockResolvedValueOnce({ ok: false, error: "Something changed since this was planned: review the new plan.", plan: filePlan({ digest: "d2" }) });
    await act(async () => { render(<NotificationSettingsPanel />); });
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Use the file's values" })); });
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Apply" })); });
    expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("d2");
    expect(screen.getByRole("alert").textContent).toContain("Something changed");
    expect(bridge.applyNotifications).toHaveBeenCalledTimes(1);
  });

  it("a toggle whose plan comes back for review is not applied by the page", async () => {
    bridge.applyNotificationsQuiet.mockResolvedValueOnce({ ok: false, plan: filePlan({ kind: "save", digest: "d3", ends: [{ id: "w", kind: "report", cluster: "prd", what: "a report on prd" }] }) });
    await act(async () => { render(<NotificationSettingsPanel />); });
    await act(async () => { fireEvent.click(screen.getByLabelText("Notify me about new faults")); });
    expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("d3");
    expect(screen.getByTestId("plan-ends").textContent).toContain("a report on prd");
    expect(bridge.applyNotifications).not.toHaveBeenCalled();
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(false);
  });
});
