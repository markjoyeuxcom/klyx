import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup, act } from "@testing-library/react";
import type { FleetConfigDTO } from "../bridge/configsvc";
import type { AISettingsState } from "../bridge/aiSettings";
import type { GitSettings } from "../bridge/gitSettings";
import type { EndpointRow } from "../bridge/endpoints";
import type { Diagnostics } from "../bridge/settings";

// Settings S1 §7's frontend tests: the controls coverage (with its negative
// fixture), a row for every entry, the route with section and anchor,
// repeated rows, the draft guard, mod+, the rail and search, and the
// sections' sentences.

const b = vi.hoisted(() => ({
  getFleetConfig: vi.fn(),
  getEndpoints: vi.fn(),
  getAISettings: vi.fn(),
  saveAISettings: vi.fn(),
  getCLIStatus: vi.fn(),
  getGitSettings: vi.fn(),
  getNotificationSettings: vi.fn(),
  getDiagnostics: vi.fn(),
  checkVersions: vi.fn(),
  getIndexSettings: vi.fn(),
}));
vi.mock("../bridge/configsvc", () => ({
  getFleetConfig: () => b.getFleetConfig(),
  addClusters: vi.fn(),
  refreshNewContextCount: vi.fn().mockResolvedValue(undefined),
  armProtected: vi.fn(),
  openTerminal: vi.fn(),
}));
vi.mock("../bridge/endpoints", () => ({ getEndpoints: () => b.getEndpoints(), setCredential: vi.fn(), removeCredential: vi.fn(), moveToKeychain: vi.fn(), testEndpoint: vi.fn() }));
vi.mock("../bridge/aiSettings", async (orig) => ({ ...(await orig<typeof import("../bridge/aiSettings")>()), getAISettings: () => b.getAISettings(), saveAISettings: (d: unknown) => b.saveAISettings(d), getCLIStatus: () => b.getCLIStatus() }));
vi.mock("../bridge/gitSettings", async (orig) => ({ ...(await orig<typeof import("../bridge/gitSettings")>()), getGitSettings: () => b.getGitSettings() }));
vi.mock("../bridge/notifications", async (orig) => ({ ...(await orig<typeof import("../bridge/notifications")>()), getNotificationSettings: () => b.getNotificationSettings() }));
vi.mock("../bridge/settings", () => ({ getDiagnostics: () => b.getDiagnostics(), checkVersions: () => b.checkVersions(), getIndexSettings: () => b.getIndexSettings() }));

import { SettingsView } from "../app/SettingsView";
import { handleShellKey } from "../app/AppShell";
import { LensIndex } from "../chrome/LensIndex";
import { ThemeProvider } from "../theme/ThemeProvider";
import { useFleet } from "../store/fleet";
import { initHistory, back, entryLabel, _resetHistoryForTest } from "../nav/history";
import { saveRoute, restoreRouteOnBoot } from "../store/persist";
import { LeaveDialog } from "./LeaveDialog";
import { _resetNavGuardForTest } from "./navGuard";
import { useSettingsNotice } from "./notice";
import { ENTRIES, SETTINGS_SECTIONS, isRegistered, searchSettings, type SettingsSection } from "./registry";
import { SettingField } from "./SettingField";
import { EndpointSettingsLink } from "./EndpointSettingsLink";
import { buildCommands } from "../chrome/commands";
import snapshot from "./registry.snapshot.json";

const cfg: FleetConfigDTO = {
  path: "/home/me/.config/klyx/fleet.yaml",
  kubeconfigPath: "/home/me/.kube/config",
  warnings: [],
  clusters: [
    { name: "aks-we-prd", context: "aks-we-prd", env: "prd", region: "we", provider: "aks", account: "", group: "prd", protected: true, hasMetrics: true },
    { name: "aks-we-dev", context: "aks-we-dev", env: "dev", region: "we", provider: "aks", account: "", group: "dev", protected: false, hasMetrics: false },
  ],
  contexts: [{ name: "aks-we-prd", inFleet: true }, { name: "kind-klyx", inFleet: false }],
};

const aiState = (persisted: Partial<AISettingsState["persisted"]> = {}): AISettingsState => ({
  path: "/x/fleet.yaml",
  persisted: { provider: "anthropic", model: "claude-opus-5-5", keySource: "keychain", apiKeyEnv: "", baseUrl: "", allowProtectedEgress: false, tlsSkipVerify: false, refusalFallback: true, cliPath: "", ...persisted },
  legacyKeyInFile: false,
  keysStored: { anthropic: true },
  active: { backend: "anthropic", ready: true, model: "claude-opus-5-5", origin: "https://api.anthropic.com" },
  activeRev: 1,
  diverged: false,
});

const git: GitSettings = {
  path: "/x/fleet.yaml",
  binary: { ready: true, path: "/usr/bin/git", version: "2.56.0" },
  helpers: [], refusedHelpers: 0,
  repos: [
    { url: "git@github.com:acme/fleet.git", key: "github.com/acme/fleet", matches: [], sources: [], tracked: [], trackNotes: [], size: 3 << 20, needsRepair: false, fetching: false },
    { url: "https://github.com/acme/infra.git", key: "github.com/acme/infra", matches: [], sources: [], tracked: [], trackNotes: [], size: 1 << 20, needsRepair: false, fetching: false },
  ],
  unapproved: [{ key: "github.com/acme/tools", serves: "github.com/acme/tools", sources: [], suggest: "https://github.com/acme/tools.git" }],
  notes: [],
};

const endpoint = (over: Partial<EndpointRow>): EndpointRow => ({
  cluster: "aks-we-prd", kind: "alertmanager", source: "https://am.example", state: "missing", legacy: false, storedToo: false, revision: "r1",
  canSet: true, canRemove: false, canMove: false, canTest: true, authType: "bearer", ...over,
});

const diagnostics: Diagnostics = {
  fleet: { path: "/home/me/.config/klyx/fleet.yaml", fromEnv: false, state: "loaded", warnings: [], drifted: 0, fileOnly: [] },
  tools: [{ name: "kubectl", path: "/usr/local/bin/kubectl", source: "path", override: "KLYX_KUBECTL_PATH" }],
  proxies: [], envSet: ["HOME"], bounds: [{ id: "chat.modelCalls", label: "Chat model calls per turn", value: 6, unit: "calls" }], canCheck: true,
};

function at(section: SettingsSection, anchor?: string) {
  useFleet.setState({ route: { name: "settings", section, ...(anchor ? { anchor } : {}) } });
}

function page() {
  return render(<ThemeProvider><LensIndex /><SettingsView /><LeaveDialog /></ThemeProvider>);
}

beforeEach(() => {
  for (const f of Object.values(b)) f.mockReset();
  b.getFleetConfig.mockResolvedValue(cfg);
  b.getEndpoints.mockResolvedValue({ rows: [endpoint({}), endpoint({ kind: "loki", source: "https://loki.example", authType: "basic" }), endpoint({ cluster: "aks-we-dev", kind: "metrics" })] });
  b.getAISettings.mockResolvedValue(aiState());
  b.saveAISettings.mockResolvedValue({ ok: true, error: "" });
  b.getCLIStatus.mockResolvedValue({ path: "/usr/local/bin/claude", tested: false, admitted: false });
  b.getGitSettings.mockResolvedValue(git);
  b.getNotificationSettings.mockResolvedValue({ enabled: true, scope: "protected", recoveries: false });
  b.getDiagnostics.mockResolvedValue(diagnostics);
  b.getIndexSettings.mockResolvedValue({ kinds: ["apps/deployments"], entriesPerCluster: 50000, mibPerCluster: 32, entriesFleet: 200000, mibFleet: 128, podPercent: 80 });
  _resetNavGuardForTest();
  useSettingsNotice.setState({ notice: null });
  useFleet.setState({ settingsLast: null, route: { name: "fleet" } });
});
afterEach(() => cleanup());

const settled = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); await new Promise((r) => requestAnimationFrame(() => r(null))); });

// Every form control a section renders sits inside a registered
// SettingField, or carries an explicit exemption (S1 §3).
function uncovered(root: ParentNode = document.body): string[] {
  const out: string[] = [];
  root.querySelectorAll<HTMLElement>('input, select, textarea, [role="switch"], [role="checkbox"]').forEach((el) => {
    if (el.closest("[data-setting-exempt]")) return;
    const field = el.closest<HTMLElement>("[data-setting]");
    const id = field?.getAttribute("data-setting");
    if (!id || !isRegistered(id)) out.push(el.outerHTML.slice(0, 120));
  });
  return out;
}

async function show(section: SettingsSection) {
  at(section);
  const r = page();
  await settled();
  await settled();
  return r;
}

describe("the controls coverage (S1 §3)", () => {
  // the states that render every control: each provider's own fields, the
  // approve dialog, the key source variants
  const variants: [SettingsSection, () => void | Promise<void>][] = [
    ["fleet", () => {}],
    ["observability", () => {}],
    ["ai", () => {}],
    ["ai", () => { b.getAISettings.mockResolvedValue(aiState({ keySource: "env", apiKeyEnv: "ANTHROPIC_API_KEY" })); }],
    ["ai", () => { b.getAISettings.mockResolvedValue(aiState({ provider: "openaiCompatible", keySource: "none", baseUrl: "http://localhost:11434" })); }],
    ["ai", () => { b.getAISettings.mockResolvedValue(aiState({ provider: "claudeCLI", keySource: "none" })); }],
    ["git", () => {}],
    ["notifications", () => {}],
    ["appearance", () => {}],
    ["data", () => {}],
    ["diagnostics", () => {}],
  ];
  it.each(variants.map((v, i) => [`${v[0]} #${i}`, v] as const))("%s: every control is described", async (_, [section, setup]) => {
    await setup();
    await show(section);
    expect(uncovered()).toEqual([]);
  });

  it("covers the Git approve dialog", async () => {
    await show("git");
    fireEvent.click(await screen.findByRole("button", { name: /^Approve/ }));
    await settled();
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(uncovered()).toEqual([]);
  });

  it("fails on a raw control with no entry (the negative fixture)", () => {
    const { container } = render(<div><SettingField id="ai.model">{(ids) => <input id={ids.id} />}</SettingField><input aria-label="raw" /></div>);
    expect(uncovered(container)).toHaveLength(1);
    const { container: c2 } = render(<div data-setting="not.a.setting"><input aria-label="unregistered" /></div>);
    expect(uncovered(c2)).toHaveLength(1);
  });
});

describe("every entry has a row in its section", () => {
  it.each(SETTINGS_SECTIONS.map((s) => [s]))("%s", async (section) => {
    const seen = new Set<string>();
    const collect = () => document.querySelectorAll("[data-setting]").forEach((el) => seen.add(el.getAttribute("data-setting") ?? ""));
    await show(section);
    collect();
    if (section === "ai") {
      for (const p of [{ keySource: "env", apiKeyEnv: "X" }, { provider: "openaiCompatible", keySource: "none" }, { provider: "claudeCLI", keySource: "none" }]) {
        cleanup();
        b.getAISettings.mockResolvedValue(aiState(p));
        await show("ai");
        collect();
      }
    }
    if (section === "git") {
      fireEvent.click(await screen.findByRole("button", { name: /^Approve/ }));
      collect();
    }
    const missing = ENTRIES.filter((e) => e.section === section && !seen.has(e.id)).map((e) => e.id);
    expect(missing).toEqual([]);
  });
});

describe("the route (S1 §1)", () => {
  it("opens the last section, else Fleet; validates and persists the section, never the anchor", () => {
    useFleet.getState().openSettings();
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "fleet" });
    useFleet.setState({ settingsLast: "git" });
    useFleet.getState().openSettings();
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "git" });
    useFleet.getState().openSettings("ai", "ai.model");
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "ai", anchor: "ai.model" });
    saveRoute(useFleet.getState().route);
    expect(JSON.parse(localStorage.getItem("klyx-route") ?? "{}")).toEqual({ name: "settings", section: "ai" });
    useFleet.setState({ route: { name: "fleet" } });
    restoreRouteOnBoot([]);
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "ai" });
  });

  it("opens an unknown restored section at Fleet, and says so", () => {
    localStorage.setItem("klyx-route", JSON.stringify({ name: "settings", section: "plugins" }));
    restoreRouteOnBoot([]);
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "fleet" });
    expect(useSettingsNotice.getState().notice).toMatch(/not in this version/);
  });

  it("records each section change in history, and returns to the section", async () => {
    _resetHistoryForTest();
    at("fleet");
    const stop = initHistory();
    useFleet.getState().openSettings("ai");
    await Promise.resolve();
    useFleet.getState().openSettings("ai", "ai.model"); // an anchor is not a place
    await Promise.resolve();
    useFleet.getState().openSettings("git");
    await Promise.resolve();
    expect(back()).toBe(true);
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "ai" });
    expect(entryLabel({ route: { name: "settings", section: "data" } }).place).toBe("Settings › Data and storage");
    useFleet.getState().openFleet();
    useFleet.getState().openSettings();
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "ai" });
    stop();
  });

  it("mod+, opens Settings where the shell owns keys", () => {
    useFleet.setState({ settingsLast: "notifications" });
    handleShellKey(new KeyboardEvent("keydown", { key: ",", metaKey: true }));
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "notifications" });
    useFleet.setState({ route: { name: "fleet" } });
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.dispatchEvent(new KeyboardEvent("keydown", { key: ",", metaKey: true, bubbles: true }));
    const e = new KeyboardEvent("keydown", { key: ",", metaKey: true });
    Object.defineProperty(e, "target", { value: input });
    handleShellKey(e);
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
    input.remove();
  });
});

describe("anchors and repeated rows", () => {
  it("focuses one cluster's field, marks every instance for the field, and says a vanished one is gone", async () => {
    at("fleet", "clusters[].protected@aks-we-dev");
    page();
    await settled();
    await settled();
    const dev = document.querySelector('[data-anchor="clusters[].protected@aks-we-dev"]');
    await waitFor(() => expect(document.activeElement).toBe(dev));
    expect(dev?.hasAttribute("data-anchored")).toBe(true);
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "fleet" }); // the anchor left the route
    cleanup();

    at("fleet", "clusters[].protected");
    page();
    await settled();
    await settled();
    await waitFor(() => expect(document.querySelectorAll('[data-setting="clusters[].protected"][data-anchored]').length).toBe(3)); // both clusters, and the description
    cleanup();

    at("fleet", "clusters[].protected@aks-gone");
    page();
    await settled();
    await settled();
    await waitFor(() => expect(useSettingsNotice.getState().notice).toMatch(/Protection for aks-gone is no longer there/));
  });

  it("gives two repositories' fields unique control ids", async () => {
    await show("git");
    const ids = Array.from(document.querySelectorAll('[data-setting="git.repos[].matches"] input')).map((el) => el.id);
    expect(ids).toHaveLength(2);
    expect(new Set(ids).size).toBe(2);
  });

  it("opens the AI section at the model and focuses it", async () => {
    at("ai", "ai.model");
    page();
    await settled();
    await settled();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("Model")));
  });
});

describe("endpoint credential prompts (S1 §1)", () => {
  it("link only a reason that names Observability, and open the cluster's endpoint with its credential focused", async () => {
    const { container } = render(<div>
      <EndpointSettingsLink text="no credential is stored for it - set one in Settings > Observability" cluster="aks-we-prd" kind="alertmanager" />
      <EndpointSettingsLink text="the read timed out" cluster="aks-we-prd" kind="alertmanager" />
    </div>);
    const links = container.querySelectorAll("[data-endpoint-link]");
    expect(links).toHaveLength(1);
    fireEvent.click(links[0]);
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "observability", anchor: "clusters[].alertmanager.endpoint@aks-we-prd" });
    cleanup();
    page();
    await settled();
    await settled();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("aks-we-prd alertmanager token")));
  });
});

describe("the rail and search", () => {
  it("marks the current section and opens another", async () => {
    await show("ai");
    expect(screen.getByRole("button", { name: "AI" }).getAttribute("aria-current")).toBe("page");
    fireEvent.click(screen.getByRole("button", { name: "Diagnostics" }));
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "diagnostics" });
  });

  it("finds protection by its effect and opens Fleet at it", async () => {
    expect(searchSettings("typed confirmation destructive").map((e) => e.id)).toContain("clusters[].protected");
    expect(searchSettings("PROTECTION").map((e) => e.id)).toContain("clusters[].protected");
    await show("ai");
    fireEvent.change(screen.getByLabelText("Search settings"), { target: { value: "protection" } });
    const result = document.querySelector<HTMLButtonElement>('[data-result="clusters[].protected"]');
    expect(result).toBeTruthy();
    fireEvent.click(result!);
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "fleet", anchor: "clusters[].protected" });
  });

  it("lists every section and each settable entry in the console", () => {
    const cmds = buildCommands(useFleet.getState());
    for (const s of SETTINGS_SECTIONS) expect(cmds.some((c) => c.id === `settings:${s}`)).toBe(true);
    const protection = cmds.find((c) => c.id === "setting:clusters[].protected");
    expect(protection?.title).toBe("Settings: Protection");
    protection?.run();
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "fleet", anchor: "clusters[].protected" });
    expect(cmds.some((c) => c.id.startsWith("setting:bound."))).toBe(false); // fixed ceilings are listed, not offered
  });
});

describe("the draft guard (S1 §1)", () => {
  async function editModel() {
    await show("ai");
    fireEvent.change(await screen.findByLabelText("Model"), { target: { value: "claude-sonnet-5-5" } });
  }

  it("asks before leaving a section with a draft: stay keeps it", async () => {
    await editModel();
    fireEvent.click(screen.getByRole("button", { name: "Git" }));
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "ai" });
    expect(await screen.findByTestId("settings-leave")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Stay" }));
    await waitFor(() => expect(screen.queryByTestId("settings-leave")).toBeNull());
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("claude-sonnet-5-5");
  });

  it("discard leaves without saving", async () => {
    await editModel();
    useFleet.getState().openFleet(); // leaving Settings, from anywhere
    fireEvent.click(await screen.findByRole("button", { name: "Discard and leave" }));
    await waitFor(() => expect(useFleet.getState().route).toEqual({ name: "fleet" }));
    expect(b.saveAISettings).not.toHaveBeenCalled();
  });

  it("save stores the settings, then leaves; history asks too", async () => {
    await editModel();
    _resetHistoryForTest();
    const stop = initHistory();
    expect(back()).toBe(false); // nothing behind; but a section switch asks
    useFleet.getState().openSettings("data");
    fireEvent.click(await screen.findByRole("button", { name: "Save and leave" }));
    await waitFor(() => expect(useFleet.getState().route).toEqual({ name: "settings", section: "data" }));
    expect(b.saveAISettings).toHaveBeenCalledWith(expect.objectContaining({ model: "claude-sonnet-5-5" }));
    stop();
  });

  it("names a typed key on its own, and Save never stores it", async () => {
    await show("ai");
    fireEvent.change(await screen.findByLabelText("API key"), { target: { value: "sk-typed" } });
    useFleet.getState().openSettings("git");
    const dialog = await screen.findByTestId("settings-leave");
    expect(dialog.textContent).toContain("an unsaved API key");
    expect(dialog.textContent).toContain("never saved by Save");
    expect(screen.queryByRole("button", { name: "Save and leave" })).toBeNull(); // only a key: nothing for Save
    fireEvent.click(screen.getByRole("button", { name: "Discard and leave" }));
    await waitFor(() => expect(useFleet.getState().route).toEqual({ name: "settings", section: "git" }));
  });

  it("lets a save that is being read back finish first", async () => {
    await editModel();
    let readBack: (v: AISettingsState) => void = () => {};
    b.getAISettings.mockImplementationOnce(() => new Promise((r) => (readBack = r)));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(b.saveAISettings).toHaveBeenCalled());
    useFleet.getState().openSettings("git");
    expect(screen.queryByTestId("settings-leave")).toBeNull(); // waiting for the readback
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "ai" });
    await act(async () => { readBack(aiState({ model: "claude-sonnet-5-5" })); });
    await waitFor(() => expect(useFleet.getState().route).toEqual({ name: "settings", section: "git" }));
  });
});

describe("SettingField (S1 §4)", () => {
  it("associates the label, describes the control by its effect, and shows the chips and the default", () => {
    render(<SettingField id="ai.model" source="file">{(ids) => <input id={ids.id} aria-describedby={ids.describedBy} />}</SettingField>);
    const input = screen.getByLabelText("Model");
    const effect = document.getElementById(input.getAttribute("aria-describedby") ?? "");
    expect(effect?.textContent).toContain("The model every AI call uses");
    expect(screen.getByText("file")).toBeTruthy();
    expect(screen.getByText("on Save")).toBeTruthy();
    expect(screen.getByText("Default and allowed values")).toBeTruthy();
  });

  it("renders a file-only entry as a read-only row that says where to change it", () => {
    render(<SettingField id="clusters[].group" instance="aks-we-prd" value="prd" />);
    const row = screen.getByRole("group", { name: "Group" });
    expect(row.getAttribute("data-anchor")).toBe("clusters[].group@aks-we-prd");
    expect(row.textContent).toContain("prd");
    expect(row.textContent).toContain("fleet.yaml");
    expect(screen.getByText("file only")).toBeTruthy();
  });
});

describe("each section's sentence", () => {
  const sentence = () => screen.findByTestId("settings-sentence").then((el) => el.textContent ?? "");
  it.each<[SettingsSection, RegExp]>([
    ["fleet", /2 clusters running, 1 protected; fleet.yaml agrees/],
    ["observability", /Alertmanager configured or found for 1 cluster; Loki configured for 1/],
    ["ai", /Prompts go to https:\/\/api.anthropic.com/],
    ["git", /git 2.56.0 at \/usr\/bin\/git; 2 approved repositories/],
    ["notifications", /On, for protected clusters/],
    ["appearance", /theme, at 100% zoom/],
    ["data", /The Git cache holds 4.0 MiB for 2 repositories/],
    ["diagnostics", /fleet.yaml loaded \(the default location\)/],
  ])("%s", async (section, want) => {
    await show(section);
    await waitFor(async () => expect(await sentence()).toMatch(want));
  });
});

it("the snapshot's sections are the frontend's", () => {
  expect((snapshot as { sections: string[] }).sections).toEqual([...SETTINGS_SECTIONS]);
});
