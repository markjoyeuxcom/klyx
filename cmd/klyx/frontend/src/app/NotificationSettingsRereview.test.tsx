import { describe, it, expect, vi } from "vitest";
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

type State = {inUse:{enabled:boolean;scope:"protected";recoveries:boolean};diverged:boolean};
const off = ():State => ({inUse:{enabled:false,scope:"protected",recoveries:false},diverged:false});

// Codex's F2b re-review probe (RR2), adopted: an earlier lost-answer
// readback never replaces a later confirmed toggle.
describe("F2b R1 review: late readbacks", () => {
  it("never replaces a later confirmed toggle with an earlier lost-answer readback", async () => {
    let actual = off();
    let release!: (s:State) => void;
    const earlier = new Promise<State>((resolve) => { release = resolve; });
    backend.settings.mockResolvedValueOnce(actual).mockImplementationOnce(() => earlier).mockImplementation(async () => actual);
    backend.apply.mockImplementationOnce(async (s:State["inUse"]) => {
      actual={...actual,inUse:s};
      throw new Error("lost first answer");
    }).mockImplementation(async (s:State["inUse"]) => {
      actual={...actual,inUse:s};
      return {ok:true};
    });
    await act(async () => {render(<NotificationSettingsPanel />);});
    await act(async () => {fireEvent.click(screen.getByLabelText("Notify me about new faults"));});
    const enabledAtFirstRead = actual;
    expect(backend.settings).toHaveBeenCalledTimes(2);
    const control=screen.getByLabelText("Notify me about new faults") as HTMLInputElement;
    // Either keep the whole settlement busy, or order replies so a late one cannot win.
    if (control.disabled) { await act(async () => {release(enabledAtFirstRead);}); return; }
    await act(async () => {fireEvent.click(control);});
    expect(actual.inUse.enabled).toBe(false);
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(false);
    await act(async () => {release(enabledAtFirstRead);});
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(actual.inUse.enabled);
  });
});
