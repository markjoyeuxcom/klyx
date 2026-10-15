import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";

// The Line slice 10b: Settings > Notifications - off until enabled, the
// OS permission asked when enabling (a refusal kept and said), scope and
// recoveries only once enabled, a failed save reverted.
const bridge = vi.hoisted(() => ({
  getNotificationSettings: vi.fn(async () => ({ enabled: false, scope: "protected", recoveries: false })),
  setNotificationSettings: vi.fn(async () => ({ ok: true } as { ok: boolean; error?: string })),
}));
vi.mock("../bridge/notifications", () => bridge);
import { NotificationSettingsPanel } from "./NotificationSettingsPanel";

beforeEach(() => vi.clearAllMocks());

describe("NotificationSettingsPanel", () => {
  it("is off by default, with the scope and recoveries disabled until enabled", async () => {
    await act(async () => { render(<NotificationSettingsPanel />); });
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(false);
    expect(screen.getByLabelText("Every cluster").matches(":disabled")).toBe(true); // the fieldset disables it
    expect((screen.getByLabelText("Protected clusters only") as HTMLInputElement).checked).toBe(true);
  });

  it("enabling saves, and a refused OS permission is kept and said", async () => {
    bridge.setNotificationSettings.mockResolvedValueOnce({ ok: true, error: "the OS has notifications off for Klyx: nothing will be shown until they are allowed" });
    await act(async () => { render(<NotificationSettingsPanel />); });
    await act(async () => { fireEvent.click(screen.getByLabelText("Notify me about new faults")); });
    expect(bridge.setNotificationSettings).toHaveBeenCalledWith({ enabled: true, scope: "protected", recoveries: false });
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("status").textContent).toContain("the OS has notifications off");
    await act(async () => { fireEvent.click(screen.getByLabelText("Every cluster")); });
    expect(bridge.setNotificationSettings).toHaveBeenLastCalledWith({ enabled: true, scope: "all", recoveries: false });
  });

  it("a failed save reverts and says why", async () => {
    bridge.setNotificationSettings.mockResolvedValueOnce({ ok: false, error: "fleet.yaml changed on disk" });
    await act(async () => { render(<NotificationSettingsPanel />); });
    await act(async () => { fireEvent.click(screen.getByLabelText("Notify me about new faults")); });
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(false);
    expect(screen.getByRole("alert").textContent).toContain("fleet.yaml changed on disk");
  });
});
