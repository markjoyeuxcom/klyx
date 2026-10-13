import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import type { AISettingsState } from "../bridge/aiSettings";

const bridge = vi.hoisted(() => ({
  getAISettings: vi.fn(),
  saveAISettings: vi.fn(),
  setAPIKey: vi.fn(),
  clearAPIKey: vi.fn(),
  moveKeyToKeychain: vi.fn(),
  armAIEgress: vi.fn(),
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

beforeEach(() => {
  for (const f of Object.values(bridge)) f.mockReset();
  bridge.saveAISettings.mockResolvedValue(ok);
  bridge.setAPIKey.mockResolvedValue(ok);
  bridge.clearAPIKey.mockResolvedValue(ok);
  bridge.moveKeyToKeychain.mockResolvedValue(ok);
  bridge.armAIEgress.mockResolvedValue(ok);
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

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(bridge.saveAISettings).toHaveBeenCalledWith(expect.objectContaining({ provider: "anthropic", keySource: "keychain" })));
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

  it("turns protected egress on only through the typed confirmation, then saves", async () => {
    bridge.getAISettings.mockResolvedValue(state({
      persisted: { ...state().persisted, provider: "anthropic", keySource: "keychain" },
      keysStored: { anthropic: true },
      active: { backend: "anthropic", ready: true, model: "claude-opus-5-5", origin: "https://api.anthropic.com" },
    }));
    render(<AISettingsPanel />);
    fireEvent.click(await screen.findByLabelText("Allow protected-cluster egress"));
    expect(bridge.saveAISettings).not.toHaveBeenCalled();
    const confirm = screen.getByRole("button", { name: "Allow and save" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByPlaceholderText("allow protected egress"), { target: { value: "allow protected egress" } });
    fireEvent.click(confirm);
    await waitFor(() => expect(bridge.armAIEgress).toHaveBeenCalledWith("allow protected egress"));
    await waitFor(() => expect(bridge.saveAISettings).toHaveBeenCalledWith(expect.objectContaining({ allowProtectedEgress: true })));
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
    expect(bridge.saveAISettings).not.toHaveBeenCalled();
  });

  it("shows a save failure and keeps the draft", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.saveAISettings.mockResolvedValue({ ok: false, error: "No API key is stored for this provider. Add one in Settings > AI." });
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "gemini" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findByTestId("ai-message")).textContent).toContain("No API key is stored");
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
  const reviewed = { path: "/Users/o/.local/share/claude/versions/2.1.296", version: "2.1.296", sha256: "c9b5341637be", reviewed: true, admitted: false };

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
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(bridge.saveAISettings).toHaveBeenCalledWith(expect.objectContaining({ provider: "claudeCLI", keySource: "none", cliPath: "/opt/claude", baseUrl: "" })));
  });

  it("shows why an installed build is refused", async () => {
    bridge.getAISettings.mockResolvedValue(state());
    bridge.getCLIStatus.mockResolvedValue({ path: "/x/claude", version: "", sha256: "", reviewed: false, admitted: false, problem: "This Claude Code build (2.1.300, sha256 0123456789ab) has no reviewed Klyx contract" });
    render(<AISettingsPanel />);
    fireEvent.change(await screen.findByLabelText("AI provider"), { target: { value: "claudeCLI" } });
    await waitFor(() => expect(screen.getByTestId("ai-cli-status").textContent).toContain("no reviewed Klyx contract"));
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
});
