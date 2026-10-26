import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import type { AISettingsState } from "../bridge/aiSettings";
import type { Plan } from "../bridge/plan";

const bridge = vi.hoisted(() => ({
  getAISettings: vi.fn(),
  planAI: vi.fn(),
  planAIFromFile: vi.fn(),
  planAIRevert: vi.fn(),
  applyAI: vi.fn(),
  applyAIQuiet: vi.fn(),
  retryAI: vi.fn(),
  setAPIKey: vi.fn(),
  clearAPIKey: vi.fn(),
  moveKeyToKeychain: vi.fn(),
  listAIModels: vi.fn(),
  testAIProvider: vi.fn(),
  getCLIStatus: vi.fn(),
}));
vi.mock("../bridge/aiSettings", async (orig) => ({ ...(await orig<typeof import("../bridge/aiSettings")>()), ...bridge }));
vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn() }));

import { AISettingsPanel } from "./AISettingsPanel";

function state(over: Partial<AISettingsState> = {}): AISettingsState {
  return {
    path: "/x/fleet.yaml",
    persisted: { provider: "off", model: "", keySource: "none", apiKeyEnv: "", baseUrl: "", allowProtectedEgress: false, tlsSkipVerify: false, refusalFallback: true, cliPath: "" },
    legacyKeyInFile: false,
    keysStored: {},
    active: { backend: "off", ready: false },
    activeRev: 1,
    diverged: false,
    ...over,
  };
}

const ok = { ok: true, error: "" };

function aPlan(over: Partial<Plan> = {}): Plan {
  return { section: "ai", kind: "save", digest: "d1", fileVersion: "v1", runtime: {}, changes: [{ id: "ai.model", label: "Model", from: "a", to: "b" }], ends: [], continues: [], writes: true, activates: true, ...over };
}

// Review and apply, then Apply (Settings slice F §6)
async function reviewAndApply() {
  fireEvent.click(screen.getByRole("button", { name: "Review and apply" }));
  fireEvent.click(await screen.findByRole("button", { name: "Apply" }));
}

beforeEach(() => {
  for (const f of Object.values(bridge)) f.mockReset();
  bridge.planAI.mockResolvedValue(aPlan());
  bridge.applyAI.mockResolvedValue(ok);
  bridge.applyAIQuiet.mockResolvedValue(ok);
  bridge.setAPIKey.mockResolvedValue(ok);
  bridge.clearAPIKey.mockResolvedValue(ok);
  bridge.moveKeyToKeychain.mockResolvedValue(ok);
});

describe("AISettingsPanel", () => {
  it("says AI is off, then sets up Claude through the keychain", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    render(<AISettingsPanel />);
    expect((await screen.findByTestId("ai-status")).textContent).toContain("AI is off");

    fireEvent.change(screen.getByLabelText("AI provider"), { target: { value: "anthropic" } });
    expect((screen.getByTestId("ai-key-state")).textContent).toContain("no key stored");
    expect(screen.getByLabelText("Model").getAttribute("placeholder")).toBe("claude-opus-5-5 (default)");

    fireEvent.change(screen.getByLabelText("API key"), { target: { value: "sk-ant-pasted" } });
    fireEvent.click(screen.getByRole("button", { name: "Save key" }));
    await waitFor(() => expect(bridge.setAPIKey).toHaveBeenCalledWith("anthropic", "sk-ant-pasted"));
    // The key field empties at once; the key never stays on the page.
    expect((screen.getByLabelText("API key") as HTMLInputElement).value).toBe("");

    await reviewAndApply();
    expect(bridge.planAI).toHaveBeenCalledWith(expect.objectContaining({ provider: "anthropic", keySource: "keychain" }));
    await waitFor(() => expect(bridge.applyAI).toHaveBeenCalledWith("d1", ""));
  });

  it("states exactly where prompts go, and why AI is unavailable", async () => {
    bridge.getAISettings.mockResolvedValueOnce(state({ active: { backend: "anthropic", ready: true, model: "claude-opus-5-5", origin: "https://api.anthropic.com" } }));
    const { unmount } = render(<AISettingsPanel />);
    expect((await screen.findByTestId("ai-status")).textContent).toContain("Prompts go to https://api.anthropic.com · Claude (Anthropic API key) · claude-opus-5-5");
    unmount();

    bridge.getAISettings.mockResolvedValueOnce(state({ active: { backend: "openai", ready: false, problem: "No API key is stored for this provider." } }));
    render(<AISettingsPanel />);
    expect((await screen.findByTestId("ai-status")).textContent).toContain("AI is unavailable: No API key is stored");
  });

  it("offers to move a plain-text key and reports divergence", async () => {
    bridge.getAISettings.mockResolvedValue(state({ legacyKeyInFile: true, diverged: true, persisted: { ...state().persisted, provider: "anthropic", keySource: "legacy" } }));
    render(<AISettingsPanel />);
    expect(await screen.findByTestId("ai-diverged")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Move to keychain" }));
    await waitFor(() => expect(bridge.moveKeyToKeychain).toHaveBeenCalled());
  });

  it("turns protected egress on only through the plan's typed interlock (Settings slice F §5)", async () => {
    bridge.getAISettings.mockResolvedValue(state({
      persisted: { ...state().persisted, provider: "anthropic", keySource: "keychain" },
      keysStored: { anthropic: true },
      active: { backend: "anthropic", ready: true, model: "claude-opus-5-5", origin: "https://api.anthropic.com" },
    }));
    bridge.planAI.mockResolvedValue(aPlan({ interlock: { phrase: "allow protected egress", why: "Protected clusters will send their state to Claude." } }));
    render(<AISettingsPanel />);
    fireEvent.click(await screen.findByLabelText("Allow protected-cluster egress"));
    fireEvent.click(screen.getByRole("button", { name: "Review and apply" }));
    expect((await screen.findByTestId("plan-interlock")).textContent).toContain("Protected clusters will send");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    const confirm = screen.getByRole("button", { name: "Confirm and apply" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    expect(bridge.applyAI).not.toHaveBeenCalled();
    fireEvent.change(screen.getByPlaceholderText("allow protected egress"), { target: { value: "allow protected egress" } });
    fireEvent.click(confirm);
    await waitFor(() => expect(bridge.applyAI).toHaveBeenCalledWith("d1", "allow protected egress"));
    expect(bridge.planAI).toHaveBeenCalledWith(expect.objectContaining({ allowProtectedEgress: true }));
  });

  // AI A3 §6: on by default; turning it off is saved like any setting.
  it("proposes actions in investigations unless turned off", async () => {
    const on = state({
      persisted: { ...state().persisted, provider: "anthropic", keySource: "keychain", proposeActions: true },
      keysStored: { anthropic: true },
      active: { backend: "anthropic", ready: true, model: "claude-opus-5-5", origin: "https://api.anthropic.com" },
    });
    bridge.getAISettings.mockResolvedValue(on);
    const view = render(<AISettingsPanel />);
    const box = (await screen.findByLabelText("Propose actions in investigations")) as HTMLInputElement;
    expect(box.checked).toBe(true);
    fireEvent.click(box);
    await reviewAndApply();
    expect(bridge.planAI).toHaveBeenCalledWith(expect.objectContaining({ proposeActions: false }));
    view.unmount();
    bridge.getAISettings.mockResolvedValue({ ...on, persisted: { ...on.persisted, proposeActions: false } });
    const off = render(<AISettingsPanel />);
    expect(((await screen.findByLabelText("Propose actions in investigations")) as HTMLInputElement).checked).toBe(false);
    off.unmount();
    const unset = { ...on.persisted };
    delete unset.proposeActions;
    bridge.getAISettings.mockResolvedValue({ ...on, persisted: unset });
    render(<AISettingsPanel />);
    expect(((await screen.findByLabelText("Propose actions in investigations")) as HTMLInputElement).checked).toBe(true);
  });

  it("discloses a custom origin for a vendor key", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "openai" } });
    fireEvent.change(screen.getByLabelText("Custom origin"), { target: { value: "https://gateway.example" } });
    expect((screen.getByTestId("ai-origin-disclosure")).textContent).toContain("Your OpenAI key will be sent to https://gateway.example, not to https://api.openai.com.");
  });

  it("loads models and runs a Test against the draft", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.listAIModels.mockResolvedValue({ models: [{ id: "gpt-x" }, { id: "gpt-y" }] });
    bridge.testAIProvider.mockResolvedValue({ ok: true, model: "gpt-x", origin: "https://api.openai.com", reply: "ready" });
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "openai" } });
    fireEvent.click(screen.getByRole("button", { name: "Load models" }));
    await waitFor(() => expect((screen.getByTestId("ai-message")).textContent).toContain("2 models available"));
    expect(bridge.listAIModels).toHaveBeenCalledWith(expect.objectContaining({ provider: "openai" }));
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    expect((await screen.findByTestId("ai-test-result")).textContent).toContain("Test answered via https://api.openai.com · gpt-x: “ready”");
    expect(bridge.planAI).not.toHaveBeenCalled();
    expect(bridge.applyAI).not.toHaveBeenCalled();
  });

  it("says a chosen provider is unsaved until it is applied (UI review 2026-10-13)", async () => {
    const saved = state({ persisted: { ...state().persisted, provider: "anthropic", keySource: "keychain" } });
    bridge.getAISettings.mockResolvedValueOnce(state()).mockResolvedValue(saved);
    render(<AISettingsPanel />);
    expect((await screen.findByTestId("ai-status")).textContent).toMatch(/^In use: AI is off/);
    expect(screen.queryByTestId("ai-unsaved")).toBeNull();

    fireEvent.change(screen.getByLabelText("AI provider"), { target: { value: "anthropic" } });
    const notice = screen.getByTestId("ai-unsaved");
    expect(notice.textContent).toContain("Unsaved changes - Review and apply to use Claude");
    expect(notice.textContent).toContain("Test checks the draft without saving it");
    // The status line still names what is in use, never the draft.
    expect(screen.getByTestId("ai-status").textContent).toContain("AI is off");

    await reviewAndApply();
    await waitFor(() => expect(screen.queryByTestId("ai-unsaved")).toBeNull());
  });

  it("shows a plan that cannot be applied, and keeps the draft", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.planAI.mockResolvedValue(aPlan({ digest: "", refused: "No API key is stored for this provider. Add one in Settings > AI." }));
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "gemini" } });
    fireEvent.click(screen.getByRole("button", { name: "Review and apply" }));
    expect((await screen.findByTestId("plan-refused")).textContent).toContain("No API key is stored");
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
    expect((screen.getByLabelText("AI provider") as HTMLInputElement).value).toBe("gemini");
  });

  it("says when the settings cannot be read, with a retry", async () => {
    bridge.getAISettings.mockResolvedValueOnce(null).mockResolvedValueOnce(state());
    render(<AISettingsPanel />);
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
    expect((await screen.findByTestId("ai-status")).textContent).toContain("AI is off");
  });
});

describe("AISettingsPanel draft fencing (Codex review of c488e34)", () => {
  function deferred<T>() {
    let resolve!: (v: T) => void;
    const promise = new Promise<T>((r) => (resolve = r));
    return { promise, resolve };
  }

  it("drops a model list that arrives after the provider changed", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    const late = deferred<{ models: { id: string }[] }>();
    bridge.listAIModels.mockReturnValueOnce(late.promise);
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "openai" } });
    fireEvent.click(screen.getByRole("button", { name: "Load models" }));
    fireEvent.change(screen.getByLabelText("AI provider"), { target: { value: "gemini" } });
    late.resolve({ models: [{ id: "gpt-x" }] });
    await waitFor(() => expect(screen.queryByText("working…")).toBeNull());
    expect(document.querySelector("datalist option")).toBeNull();
    expect(screen.queryByTestId("ai-message")).toBeNull();
  });

  it("drops a Test result that arrives after the origin changed", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    const late = deferred<{ ok: boolean; model: string; origin: string; reply: string }>();
    bridge.testAIProvider.mockReturnValueOnce(late.promise);
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "openaiCompatible" } });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    fireEvent.change(screen.getByLabelText("Server URL"), { target: { value: "http://192.168.1.10:11434" } });
    late.resolve({ ok: true, model: "m", origin: "http://localhost:11434", reply: "ready" });
    await waitFor(() => expect(screen.queryByText("working…")).toBeNull());
    expect(screen.queryByTestId("ai-test-result")).toBeNull();
  });

  it("clears loaded models when the origin changes", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.listAIModels.mockResolvedValue({ models: [{ id: "llama3.1" }] });
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "openaiCompatible" } });
    fireEvent.click(screen.getByRole("button", { name: "Load models" }));
    await waitFor(() => expect(document.querySelector("datalist option")).not.toBeNull());
    fireEvent.change(screen.getByLabelText("Server URL"), { target: { value: "http://other:11434" } });
    expect(document.querySelector("datalist option")).toBeNull();
  });

  it("lists models for a first-time setup with no model chosen", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.listAIModels.mockResolvedValue({ models: [{ id: "gemini-x" }] });
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "gemini" } });
    fireEvent.click(screen.getByRole("button", { name: "Load models" }));
    await waitFor(() => expect(bridge.listAIModels).toHaveBeenCalledWith(expect.objectContaining({ provider: "gemini", model: "" })));
    await waitFor(() => expect(document.querySelector("datalist option")?.getAttribute("value")).toBe("gemini-x"));
  });
});

describe("AISettingsPanel: Claude Code CLI", () => {
  const reviewed = { path: "/Users/o/.local/share/claude/versions/2.1.296", version: "2.1.296", sha256: "c9b5341637be", tested: true, admitted: false };

  it("sets up the CLI with no key and no origin, and shows what Klyx found", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.getCLIStatus.mockResolvedValue(reviewed);
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "claudeCLI" } });

    const status = await screen.findByTestId("ai-cli-status");
    await waitFor(() => expect(status.textContent).toContain("Claude Code 2.1.296 · sha256 c9b5341637be… · not admitted yet"));
    expect(screen.queryByLabelText("API key")).toBeNull();
    expect(screen.queryByLabelText("Key source")).toBeNull();
    expect(screen.queryByLabelText("Custom origin")).toBeNull();
    expect(screen.getByLabelText("Model").getAttribute("placeholder")).toBe("claude-opus-5-5 (default)");
    expect(screen.getByTestId("ai-settings").textContent).toContain("asks Claude Code to read a file and run a command");

    fireEvent.change(screen.getByLabelText("Claude Code path"), { target: { value: "/opt/claude" } });
    await reviewAndApply();
    expect(bridge.planAI).toHaveBeenCalledWith(expect.objectContaining({ provider: "claudeCLI", keySource: "none", cliPath: "/opt/claude", baseUrl: "" }));
  });

  it("after a revocation, says only Test admits it", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.getCLIStatus.mockResolvedValue({ ...reviewed, problem: "Claude Code attempted a tool or returned an unknown event; Klyx revoked the CLI's admission until the next Test" });
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "claudeCLI" } });
    await waitFor(() => expect(screen.getByTestId("ai-cli-status").textContent).toContain("not admitted (Test admits it)"));
    expect(screen.getByTestId("ai-cli-status").textContent).not.toContain("first call");
  });

  it("says a new build needs one Test", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.getCLIStatus.mockResolvedValue({ path: "/x/claude", version: "", sha256: "0123456789ab", tested: false, admitted: false, problem: "This Claude Code build (2.1.300, sha256 0123456789ab) has not been checked by Klyx on this machine yet. Press Test in Settings > AI to check and admit it" });
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "claudeCLI" } });
    await waitFor(() => expect(screen.getByTestId("ai-cli-status").textContent).toContain("Press Test"));
    expect(screen.getByTestId("ai-settings").textContent).toContain("each Claude Code update needs one Test");
  });

  it("drops a status reply for an older path, and re-reads after Test", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    let resolveOld!: (v: unknown) => void;
    bridge.getCLIStatus.mockReturnValueOnce(new Promise((r) => (resolveOld = r))).mockResolvedValue(reviewed);
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "claudeCLI" } });
    fireEvent.change(screen.getByLabelText("Claude Code path"), { target: { value: "/new/claude" } });
    await waitFor(() => expect(screen.getByTestId("ai-cli-status").textContent).toContain("2.1.296"));
    resolveOld({ ...reviewed, version: "OLD", problem: "old path" });
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.getByTestId("ai-cli-status").textContent).not.toContain("old path");

    const calls = bridge.getCLIStatus.mock.calls.length;
    bridge.testAIProvider.mockResolvedValue({ ok: true, model: "claude-opus-5-5", origin: "Anthropic, via the Claude Code CLI on your Claude sign-in", reply: "I cannot access files." });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    await waitFor(() => expect(bridge.getCLIStatus.mock.calls.length).toBeGreaterThan(calls));
    expect(screen.getByTestId("ai-test-result").textContent).toContain("I cannot access files.");
  });

  // Settings review slice 0 (Codex's probes): a typed key belongs to its
  // provider; a late Save reply keeps newer edits; a failed reread qualifies
  // what is in use.
  it("never moves a typed key to another provider", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    render(<AISettingsPanel />);
    await screen.findByTestId("ai-status");
    fireEvent.change(screen.getByLabelText("AI provider"), { target: { value: "anthropic" } });
    fireEvent.change(screen.getByLabelText("API key"), { target: { value: "anthropic-only-needle" } });
    fireEvent.change(screen.getByLabelText("AI provider"), { target: { value: "openai" } });
    expect((screen.getByLabelText("API key") as HTMLInputElement).value).toBe("");
    expect((screen.getByRole("button", { name: "Save key" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("AI provider"), { target: { value: "anthropic" } });
    expect((screen.getByLabelText("API key") as HTMLInputElement).value).toBe("");
    expect(bridge.setAPIKey).not.toHaveBeenCalled();
  });

  it("keeps an edit made while a Save was pending", async () => {
    const saved = { ...state().persisted, provider: "openai", keySource: "keychain", model: "model-1" };
    bridge.getAISettings.mockResolvedValueOnce(state({ persisted: saved }));
    let release: (v: typeof ok) => void = () => {};
    bridge.applyAI.mockImplementationOnce(() => new Promise((r) => (release = r)));
    render(<AISettingsPanel />);
    await screen.findByTestId("ai-status");
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "model-2" } });
    await reviewAndApply();
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "model-3-unsaved" } });
    bridge.getAISettings.mockResolvedValueOnce(state({ persisted: { ...saved, model: "model-2" } }));
    release(ok);
    await waitFor(() => expect(bridge.getAISettings).toHaveBeenCalledTimes(2));
    await waitFor(() => expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("model-3-unsaved"));
    expect(screen.getByTestId("ai-unsaved").textContent).toContain("model-3-unsaved");
  });

  it("says a lost Apply answer is not known, and reads what is in use again (F2b review, R3)", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.applyAI.mockResolvedValue({ ok: false, unknown: true, error: "lost bridge answer" });
    render(<AISettingsPanel />);
    await screen.findByTestId("ai-status");
    fireEvent.change(screen.getByLabelText("AI provider"), { target: { value: "anthropic" } });
    await reviewAndApply();
    await waitFor(() => expect(bridge.getAISettings).toHaveBeenCalledTimes(2));
    expect(screen.getByText(/whether it applied is not known: what is in use was read again/)).toBeTruthy();
    expect(screen.queryByText(/^Applied\./)).toBeNull();
  });

  it("qualifies what is in use when the reread after a Save fails", async () => {
    bridge.getAISettings.mockResolvedValueOnce(state({ persisted: { ...state().persisted, provider: "anthropic", keySource: "keychain" }, active: { backend: "anthropic", ready: true, model: "claude-opus-5-5", origin: "https://api.anthropic.com" } }));
    render(<AISettingsPanel />);
    await screen.findByTestId("ai-status");
    fireEvent.change(screen.getByLabelText("AI provider"), { target: { value: "openai" } });
    bridge.getAISettings.mockResolvedValueOnce(null);
    await reviewAndApply();
    expect(await screen.findByTestId("ai-reread-failed")).toBeTruthy();
    expect(screen.getByTestId("ai-status").textContent).toContain("In use, as last read:");
  });

  it("says skipping verification is insecure, and shows it in use", async () => {
    const local = { ...state().persisted, provider: "openaiCompatible", keySource: "none", baseUrl: "https://llm.lan:8443", tlsSkipVerify: true, model: "m" };
    bridge.getAISettings.mockResolvedValue(state({ persisted: local, active: { backend: "openaiCompatible", ready: true, model: "m", origin: "https://llm.lan:8443" } }));
    render(<AISettingsPanel />);
    expect((await screen.findByTestId("ai-tls-off")).textContent).toContain("certificate not verified");
    // the effect is the control's description (Settings S1 §4)
    const box = screen.getByLabelText("Skip certificate verification");
    expect(document.getElementById(box.getAttribute("aria-describedby") ?? "")?.textContent).toContain("Insecure");
  });

  // Codex's review of slice 0, finding 1: an edit made while the successful
  // Save is being read back survives that read, and only the latest read
  // lands.
  it("keeps an edit made while a Save is being read back", async () => {
    const saved = { ...state().persisted, provider: "openai", keySource: "keychain", model: "model-1" };
    bridge.getAISettings.mockResolvedValueOnce(state({ persisted: saved }));
    let readBack: (v: AISettingsState) => void = () => {};
    render(<AISettingsPanel />);
    await screen.findByTestId("ai-status");
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "model-2" } });
    bridge.getAISettings.mockImplementationOnce(() => new Promise((r) => (readBack = r)));
    await reviewAndApply();
    await waitFor(() => expect(bridge.getAISettings).toHaveBeenCalledTimes(2)); // the readback is in flight
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "model-3-unsaved" } });
    readBack(state({ persisted: { ...saved, model: "model-2" } }));
    await waitFor(() => expect(screen.getByTestId("ai-unsaved").textContent).toContain("model-3-unsaved"));
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("model-3-unsaved");
  });

  it("never lets an older readback replace a newer one", async () => {
    const saved = { ...state().persisted, provider: "openai", keySource: "keychain", model: "model-1" };
    bridge.getAISettings.mockResolvedValueOnce(state({ persisted: saved }));
    const reads: ((v: AISettingsState) => void)[] = [];
    bridge.getAISettings.mockImplementation(() => new Promise((r) => reads.push(r)));
    render(<AISettingsPanel />);
    await screen.findByTestId("ai-status");
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "model-2" } });
    await reviewAndApply();
    await waitFor(() => expect(reads).toHaveLength(1));
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "model-3" } });
    await reviewAndApply();
    await waitFor(() => expect(reads).toHaveLength(2));
    reads[1](state({ persisted: { ...saved, model: "model-3" } })); // the newer read lands first
    await waitFor(() => expect(screen.queryByTestId("ai-unsaved")).toBeNull());
    reads[0](state({ persisted: { ...saved, model: "model-2" } })); // then the older one
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("ai-unsaved")).toBeNull();
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("model-3");
  });
});

// Settings slice F §5, §6: plans, their replacements, drift and
// saved-not-active.
describe("AISettingsPanel: plans", () => {
  const running = () => state({
    persisted: { ...state().persisted, provider: "anthropic", keySource: "keychain" },
    keysStored: { anthropic: true },
    active: { backend: "anthropic", ready: true, model: "claude-opus-5-5", origin: "https://api.anthropic.com" },
  });

  it("shows what changes and what ends, and an edit voids the plan", async () => {
    bridge.getAISettings.mockResolvedValue(running());
    bridge.planAI.mockResolvedValue(aPlan({ ends: [{ id: "conv:c1", kind: "investigation", cluster: "lab", started: "2026-10-25T10:42:00Z", what: "an investigation on lab" }] }));
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("Model"), { target: { value: "claude-sonnet-5-5" } });
    fireEvent.click(screen.getByRole("button", { name: "Review and apply" }));
    expect((await screen.findByTestId("plan-changes")).textContent).toContain("Model: a → b");
    expect(screen.getByTestId("plan-ends").textContent).toContain("an investigation on lab, started");
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "claude-haiku-5-5" } });
    expect(screen.queryByTestId("settings-plan")).toBeNull();
    expect(bridge.applyAI).not.toHaveBeenCalled();
  });

  it("shows a replacement plan unconfirmed: the typed phrase never carries over", async () => {
    bridge.getAISettings.mockResolvedValue(running());
    const interlock = { phrase: "allow protected egress", why: "Protected clusters will send their state to Claude." };
    bridge.planAI.mockResolvedValue(aPlan({ interlock }));
    bridge.applyAI.mockResolvedValue({ ok: false, error: "Something changed since this was planned: review the new plan.", plan: aPlan({ digest: "d2", interlock, ends: [{ id: "turn:t1", kind: "turn", cluster: "lab", what: "a question being answered on lab" }] }) });
    render(<AISettingsPanel />);
    fireEvent.click(await screen.findByLabelText("Allow protected-cluster egress"));
    fireEvent.click(screen.getByRole("button", { name: "Review and apply" }));
    fireEvent.click(await screen.findByRole("button", { name: "Apply" }));
    fireEvent.change(screen.getByPlaceholderText("allow protected egress"), { target: { value: "allow protected egress" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm and apply" }));
    await waitFor(() => expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("d2"));
    expect(screen.queryByPlaceholderText("allow protected egress")).toBeNull(); // the dialog closed
    expect(screen.getByTestId("plan-ends").textContent).toContain("a question being answered on lab");
    expect(screen.getByTestId("ai-message").textContent).toContain("review the new plan");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect((screen.getByPlaceholderText("allow protected egress") as HTMLInputElement).value).toBe("");
  });

  it("offers the file's values on drift, as a plan that writes nothing", async () => {
    bridge.getAISettings.mockResolvedValue({ ...running(), diverged: true });
    bridge.planAIFromFile.mockResolvedValue(aPlan({ kind: "file", writes: false, digest: "f1" }));
    render(<AISettingsPanel />);
    fireEvent.click(await screen.findByRole("button", { name: "Use the file's values" }));
    expect((await screen.findByTestId("settings-plan")).textContent).toContain("fleet.yaml already says this");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(bridge.applyAI).toHaveBeenCalledWith("f1", ""));
  });

  it("says saved, not active, with Retry and Revert", async () => {
    bridge.getAISettings.mockResolvedValue({ ...running(), savedNotActive: "keychain unavailable: locked", active: { backend: "anthropic", ready: false, problem: "Saved, not active: keychain unavailable: locked" } });
    bridge.retryAI.mockResolvedValue({ ok: false, savedNotActive: "keychain unavailable: still locked" });
    bridge.planAIRevert.mockResolvedValue(aPlan({ kind: "revert", digest: "r1" }));
    render(<AISettingsPanel />);
    expect((await screen.findByTestId("ai-saved-not-active")).textContent).toContain("Saved, not active: keychain unavailable: locked");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.getByTestId("ai-message").textContent).toContain("Saved, not active: keychain unavailable: still locked"));
    fireEvent.click(screen.getByRole("button", { name: "Revert" }));
    expect((await screen.findByTestId("settings-plan")).textContent).toContain("Reverting writes the settings last in use");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(bridge.applyAI).toHaveBeenCalledWith("r1", ""));
  });
});

// The F2a implementation review, R6 (Codex's probe, adopted): a
// replacement plan for a draft edited away while its Apply ran is not
// shown again.
describe("AISettingsPanel: a late replacement", () => {
  it("does not restore a plan for an older, edited-away draft", async () => {
    bridge.getAISettings.mockResolvedValue(state({ persisted: { ...state().persisted, provider: "openaiCompatible", model: "m", baseUrl: "http://localhost:11434" }, active: { backend: "openaiCompatible", ready: true, model: "m", origin: "http://localhost:11434" } }));
    const p = aPlan({ digest: "old-draft", changes: [{ id: "ai.model", label: "Model", from: "m", to: "first-edit" }] });
    bridge.planAI.mockResolvedValue(p);
    let finish!: (v: unknown) => void;
    bridge.applyAI.mockReturnValue(new Promise((r) => { finish = r; }));
    render(<AISettingsPanel />);
    const input = await screen.findByLabelText("Model");
    fireEvent.change(input, { target: { value: "first-edit" } });
    await reviewAndApply();
    fireEvent.change(input, { target: { value: "newer-edit" } });
    expect(screen.queryByTestId("settings-plan")).toBeNull();
    await waitFor(() => expect(bridge.applyAI).toHaveBeenCalled());
    finish({ ok: false, error: "work changed", plan: { ...p, digest: "replan-old-draft" } });
    await waitFor(() => expect(screen.getByTestId("ai-message").textContent).toContain("The draft changed while this was being applied"));
    expect((input as HTMLInputElement).value).toBe("newer-edit");
    expect(screen.queryByTestId("settings-plan")).toBeNull();
  });
});
