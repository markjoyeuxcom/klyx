import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";

const backend = vi.hoisted(() => ({ settings: vi.fn(), apply: vi.fn() }));
vi.mock("../bridge/notifications", async () => {
  const { askApply } = await import("../bridge/plan");
  return {
    getNotificationSettings: () => backend.settings(),
    applyNotificationsQuiet: (s: unknown) => askApply(() => backend.apply(s)),
    planNotificationsFromFile: vi.fn(),
    applyNotifications: vi.fn(),
  };
});
import { NotificationSettingsPanel } from "./NotificationSettingsPanel";

const off = () => ({ inUse: { enabled: false, scope: "protected", recoveries: false }, diverged: false });
beforeEach(() => vi.clearAllMocks());

// Codex's F2b implementation review probes (R3), adopted: a lost answer
// is not a refusal, and a failed readback qualifies what is shown.
describe("F2b review: notification write and readback failures", () => {
  it("reconciles a committed toggle after losing its answer instead of asserting it was not applied", async () => {
    let actual = off();
    backend.settings.mockImplementation(async () => actual);
    backend.apply.mockImplementation(async (next) => {
      actual = { ...actual, inUse: next };
      throw new Error("lost bridge answer");
    });
    await act(async () => { render(<NotificationSettingsPanel />); });
    await act(async () => { fireEvent.click(screen.getByLabelText("Notify me about new faults")); });
    expect(actual.inUse.enabled).toBe(true); // the write committed
    expect.soft(backend.settings).toHaveBeenCalledTimes(2);
    expect.soft((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(true);
    expect.soft(screen.queryByText(/Not applied:/)).toBeNull();
  });

  it("qualifies a successful Apply when the settings in use cannot be read back", async () => {
    backend.settings.mockResolvedValueOnce(off()).mockResolvedValue(null);
    backend.apply.mockResolvedValue({ ok: true });
    await act(async () => { render(<NotificationSettingsPanel />); });
    await act(async () => { fireEvent.click(screen.getByLabelText("Notify me about new faults")); });
    expect(backend.settings).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(/could not.*read|read.*again|cannot.*confirm|unverified/i)).not.toBeNull();
  });
});
