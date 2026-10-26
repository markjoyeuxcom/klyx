import React from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import type { Plan } from "../bridge/plan";
import type { GitSettings } from "../bridge/gitSettings";

const api = vi.hoisted(() => ({
  getNotificationSettings: vi.fn(), applyNotifications:vi.fn(), applyNotificationsQuiet:vi.fn(), planNotificationsFromFile:vi.fn(),
  getGitSettings:vi.fn(), planGitFromFile:vi.fn(), applyGit:vi.fn(), fetchGitRepo:vi.fn(),
}));
vi.mock("../bridge/notifications", () => ({
  getNotificationSettings:api.getNotificationSettings, applyNotifications:api.applyNotifications,
  applyNotificationsQuiet:api.applyNotificationsQuiet, planNotificationsFromFile:api.planNotificationsFromFile,
}));
vi.mock("../bridge/gitSettings", async (orig) => ({
  ...(await orig<typeof import("../bridge/gitSettings")>()), getGitSettings:api.getGitSettings,
  planGitFromFile:api.planGitFromFile,applyGit:api.applyGit,fetchGitRepo:api.fetchGitRepo,
}));
import { NotificationSettingsPanel } from "./NotificationSettingsPanel";
import { GitSettingsPanel } from "./GitSettingsPanel";

// Codex's second F2b re-review controls, adopted: StrictMode's two mount
// reads, ordered; a late Git plan shown, never applied, and its stale
// Apply replaced without inheriting confirmation.
describe("F2b revision 2 review controls", () => {
  it("keeps the latest notification mount read in StrictMode", async () => {
    const off={inUse:{enabled:false,scope:"protected" as const,recoveries:false},diverged:false};
    const on={...off,inUse:{...off.inUse,enabled:true}};
    let release!: (v:typeof off) => void;
    api.getNotificationSettings.mockImplementationOnce(() => new Promise((resolve) => {release=resolve;})).mockResolvedValue(on);
    await act(async () => {render(<React.StrictMode><NotificationSettingsPanel /></React.StrictMode>);});
    expect(api.getNotificationSettings).toHaveBeenCalledTimes(2);
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(true);
    await act(async () => {release(off);});
    expect((screen.getByLabelText("Notify me about new faults") as HTMLInputElement).checked).toBe(true);
  });

  it("shows a late Git plan without applying it, then shows a replacement without inheriting confirmation", async () => {
    const state:GitSettings={path:"/fixture/fleet.yaml",binary:{ready:true,path:"/fixture/git",version:"stand-in"},helpers:[],refusedHelpers:0,
      repos:[{url:"https://example.test/fleet.git",key:"example.test/fleet",matches:[],sources:[],tracked:["refs/heads/main"],trackNotes:[],size:0,needsRepair:false,fetching:false}],
      unapproved:[],notes:[],diverged:true};
    const old:Plan={section:"git",kind:"file",digest:"old",fileVersion:"v1",runtime:{git:"0"},changes:[{id:"git.repos",label:"Approvals",from:"before",to:"after"}],ends:[],continues:[],writes:false,activates:true};
    const replacement={...old,digest:"replacement",fileVersion:"v2"};
    let release!: (p:Plan) => void;
    api.getGitSettings.mockResolvedValueOnce(state).mockResolvedValue({...state,diverged:false});
    api.planGitFromFile.mockImplementation(() => new Promise((resolve) => {release=resolve;}));
    api.fetchGitRepo.mockResolvedValue({ok:true,error:""});
    api.applyGit.mockResolvedValue({ok:false,error:"changed: review replacement",plan:replacement});
    await act(async () => {render(<GitSettingsPanel />);});
    await act(async () => {fireEvent.click(screen.getByRole("button",{name:"Use the file's values"}));});
    await act(async () => {fireEvent.click(screen.getByRole("button",{name:"Fetch now"}));});
    await act(async () => {release(old);});
    expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("old");
    expect(api.applyGit).not.toHaveBeenCalled();
    await act(async () => {fireEvent.click(screen.getByRole("button",{name:"Apply"}));});
    expect(api.applyGit).toHaveBeenCalledTimes(1);
    expect(api.applyGit).toHaveBeenCalledWith("old");
    expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("replacement");
  });
});
