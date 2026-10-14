import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor, act } from "@testing-library/react";

vi.mock("@wailsio/runtime", async (orig) => ({
  ...(await orig<typeof import("@wailsio/runtime")>()),
  Browser: { OpenURL: vi.fn() },
  Events: { On: vi.fn(() => () => {}) },
}));
const bridge = vi.hoisted(() => ({
  chatStart: vi.fn(),
  chatState: vi.fn(),
  chatAsk: vi.fn(),
  chatStop: vi.fn(async () => {}),
  chatClear: vi.fn(async () => {}),
  onChatRead: vi.fn(() => () => {}),
}));
vi.mock("../bridge/chat", () => bridge);

import { ChatDrawer } from "./ChatDrawer";
import { useChat, openChat } from "./chatStore";
import { useFleet } from "../store/fleet";
import type { ChatExchange, ChatState } from "../bridge/chat";

let version = 10;
function state(over: Partial<ChatState> = {}): ChatState {
  return { conversationId: "c1", cluster: "x", mode: "tools", provider: "anthropic", model: "claude-opus-5-5", origin: "https://api.anthropic.com", protected: false, trimmed: false, removed: 0, active: false, exchanges: [], version: version++, ...over };
}

function exchange(over: Partial<ChatExchange> = {}): ChatExchange {
  return { id: "e1", question: "why is web failing?", outcome: "answered", at: "2026-10-14T12:00:00Z", reads: [], calls: [], ...over };
}

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

describe("ChatDrawer", () => {
  beforeEach(() => {
    for (const f of Object.values(bridge)) f.mockReset?.();
    bridge.onChatRead.mockImplementation(() => () => {});
    bridge.chatStop.mockImplementation(async () => {});
    bridge.chatClear.mockImplementation(async () => {});
    useChat.setState({ open: false, cluster: null, states: {}, drafts: {}, seeds: {}, asking: {}, cleared: {}, outcomes: {}, reqs: {} });
  });

  it("starts a conversation and states what it sends and to whom", async () => {
    bridge.chatState.mockResolvedValue(state({ conversationId: undefined }));
    bridge.chatStart.mockResolvedValue(state());
    act(() => openChat("x"));
    const { findByTestId, getByText } = render(<ChatDrawer />);
    const g = await findByTestId("chat-guarantee");
    expect(g.textContent).toContain("Chat sends what it reads to Claude");
    expect(g.textContent).toContain("not guaranteed secret-free");
    expect(g.textContent).toContain("Secret values, Helm values and ConfigMap values are never sent");
    expect(getByText(/tools · read-only/)).toBeTruthy();
  });

  it("asks, shows the reads and the answer, and links only validated references the answer names", async () => {
    bridge.chatState.mockResolvedValue(state());
    bridge.chatAsk.mockResolvedValue(state({ version: 1000, exchanges: [exchange({
      answer: "Pod **web-7f** is crash-looping; `api` is fine. Also see orphan-x.",
      reads: [
        { tool: "get_resource", target: "Pod shop/web-7f", outcome: "completed", redacted: 2, refs: [{ gvr: "v1/pods", namespace: "shop", name: "web-7f", uid: "u1" }] },
        { tool: "list_resources", target: "Pod in shop", outcome: "completed", refs: [{ gvr: "v1/pods", namespace: "shop", name: "unmentioned", uid: "u2" }] },
      ],
      calls: [{ provider: "anthropic", model: "claude-opus-5-5", mode: "tools", tools: ["get_resource"], outcome: "completed" }],
    })] }));
    const log = vi.spyOn(useFleet.getState(), "logActivity");
    act(() => openChat("x"));
    const { findByLabelText, findByTestId, getByTestId } = render(<ChatDrawer />);
    const input = await findByLabelText("question");
    fireEvent.change(input, { target: { value: "why is web failing?" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(bridge.chatAsk).toHaveBeenCalledWith("x", "c1", "why is web failing?", undefined));
    expect((await findByTestId("chat-reads")).textContent).toContain("2 reads · 2 redacted");
    expect(getByTestId("chat-chips").textContent).toBe("Pod shop/web-7f");
    expect(log).toHaveBeenCalledWith(expect.objectContaining({ kind: "success", message: expect.stringContaining("AI chat call - x · tools · ") }));
    expect(log.mock.calls[0][0].message).toContain("tools: get_resource · completed");
  });

  // Codex review of 143dc6d D1: an older State reply never overwrites a newer
  // Ask result, whatever order they arrive in.
  it("orders replies by version, not arrival", async () => {
    const slow = deferred<ChatState>();
    bridge.chatState.mockResolvedValueOnce(state({ version: 2000 })).mockReturnValueOnce(slow.promise);
    bridge.chatAsk.mockResolvedValue(state({ version: 4000, exchanges: [exchange({ answer: "final answer" })] }));
    act(() => openChat("x"));
    const { findByLabelText, findByText } = render(<ChatDrawer />);
    const input = await findByLabelText("question");
    fireEvent.change(input, { target: { value: "why is web failing?" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(await findByText("final answer")).toBeTruthy();
    await act(async () => slow.resolve(state({ version: 3000, active: true, exchanges: [exchange({ outcome: "" })] })));
    expect(useChat.getState().states.x.version).toBe(4000);
    expect(await findByText("final answer")).toBeTruthy();
  });

  // D3: every attempted call is logged once, metadata only - a stopped call
  // too - and never question or error text.
  it("logs every attempted call once, without text", async () => {
    bridge.chatState.mockResolvedValue(state({ exchanges: [exchange({
      id: "e-log", outcome: "cancelled", question: "PRIVATE_QUESTION_SENTINEL", error: "provider said PRIVATE_QUESTION_SENTINEL",
      calls: [{ provider: "anthropic", model: "m", mode: "tools", outcome: "cancelled" }, { provider: "anthropic", model: "m", mode: "tools", outcome: "sent" }],
    })] }));
    const log = vi.spyOn(useFleet.getState(), "logActivity");
    log.mockClear();
    act(() => openChat("x"));
    render(<ChatDrawer />);
    await waitFor(() => expect(log).toHaveBeenCalledTimes(1));
    expect(log.mock.calls[0][0].message).toContain("· cancelled");
    expect(JSON.stringify(log.mock.calls)).not.toContain("PRIVATE_QUESTION_SENTINEL");
  });

  // D4: the limit is UTF-8 bytes, like the backend's; a long question keeps
  // its draft and the editor stays usable.
  it("counts UTF-8 bytes and keeps a too-long draft editable", async () => {
    bridge.chatState.mockResolvedValue(state());
    act(() => openChat("x"));
    const { findByLabelText, getByTestId, getByText } = render(<ChatDrawer />);
    const input = (await findByLabelText("question")) as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: "問".repeat(2000) } });
    expect(getByTestId("chat-input-notice").textContent).toContain("6000 bytes");
    expect((getByText("Ask") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.keyDown(input, { key: "Enter" });
    expect(bridge.chatAsk).not.toHaveBeenCalled();
    expect(input.disabled).toBe(false);
    expect(input.value.length).toBe(2000);
  });

  it("a recoverable notice returns the question to the editor", async () => {
    bridge.chatState.mockResolvedValue(state());
    bridge.chatAsk.mockResolvedValue(state({ version: 5000, notice: "a question is already being answered; stop it or wait" }));
    act(() => openChat("x"));
    const { findByLabelText, findByTestId } = render(<ChatDrawer />);
    const input = (await findByLabelText("question")) as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: "again?" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect((await findByTestId("chat-input-notice")).textContent).toContain("already being answered");
    await waitFor(() => expect(input.value).toBe("again?"));
    expect(input.disabled).toBe(false);
  });

  // D5: Clear frees the slot; only an explicit Start opens another.
  it("Clear does not start a new conversation by itself", async () => {
    bridge.chatState.mockResolvedValueOnce(state()).mockResolvedValue(state({ conversationId: undefined, version: 6000 }));
    bridge.chatStart.mockResolvedValue(state({ conversationId: "c2", version: 7000 }));
    act(() => openChat("x"));
    const { findByText } = render(<ChatDrawer />);
    fireEvent.click(await findByText("Clear"));
    await waitFor(() => expect(bridge.chatClear).toHaveBeenCalledWith("x", "c1"));
    expect(await findByText("The conversation was cleared.")).toBeTruthy();
    expect(bridge.chatStart).not.toHaveBeenCalled();
    fireEvent.click(await findByText("Start a conversation"));
    await waitFor(() => expect(bridge.chatStart).toHaveBeenCalledTimes(1));
  });

  // D2: a follow-up carries Explain's own target to the backend.
  it("an Explain follow-up sends its target with the question", async () => {
    bridge.chatState.mockResolvedValue(state());
    bridge.chatAsk.mockResolvedValue(state({ version: 8000 }));
    const seed = { kind: "row" as const, task: "pods", part: "pods", uid: "uid-api" };
    act(() => openChat("x", { seed, label: "Pod/shop/api" }));
    const { findByLabelText, getByTestId } = render(<ChatDrawer />);
    const input = await findByLabelText("question");
    expect(getByTestId("chat-followup").textContent).toContain("About Pod/shop/api");
    fireEvent.change(input, { target: { value: "why is it failing?" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(bridge.chatAsk).toHaveBeenCalledWith("x", "c1", "why is it failing?", seed));
  });

  it("says why chat is unavailable, with the fix", async () => {
    bridge.chatState.mockResolvedValue(state({ conversationId: undefined }));
    bridge.chatStart.mockResolvedValue(state({ conversationId: undefined, protected: true, error: "AI is disabled for protected cluster \"x\": the configured AI endpoint is not on this machine" }));
    act(() => openChat("x"));
    const { findByRole, getByLabelText } = render(<ChatDrawer />);
    expect((await findByRole("alert")).textContent).toContain("protected cluster");
    expect((getByLabelText("question") as HTMLTextAreaElement).disabled).toBe(true);
  });

  // Codex review of 0725e47: a Start refusal is a current state, and a
  // bridge failure shows beside the conversation without replacing it.
  it("shows a Start refusal that follows a newer-looking empty State", async () => {
    bridge.chatState.mockResolvedValue(state({ conversationId: undefined, version: 9000 }));
    bridge.chatStart.mockResolvedValue(state({ conversationId: undefined, error: "the cluster is not connected", version: 9001 }));
    act(() => openChat("x"));
    const { findByRole } = render(<ChatDrawer />);
    expect((await findByRole("alert")).textContent).toContain("not connected");
  });

  // A refusal is its request's outcome: it shows even when its state is
  // older than the one on screen, and the conversation stays.
  it("shows a refusal whose state loses the version check", async () => {
    bridge.chatState.mockResolvedValue(state({ conversationId: undefined, version: 9050 }));
    bridge.chatStart.mockResolvedValue(state({ conversationId: undefined, version: 0, error: "the cluster is not connected" }));
    act(() => openChat("x"));
    const { findByRole } = render(<ChatDrawer />);
    expect((await findByRole("alert")).textContent).toContain("not connected");
    expect(useChat.getState().states.x.version).toBe(9050);
  });

  it("an Ask refusal with an older state keeps the conversation and gives the question back", async () => {
    bridge.chatState.mockResolvedValue(state({ version: 9060 }));
    bridge.chatAsk.mockResolvedValue(state({ conversationId: undefined, version: 0, error: "this conversation was cleared" }));
    act(() => openChat("x"));
    const { findByLabelText, findByTestId } = render(<ChatDrawer />);
    const input = (await findByLabelText("question")) as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: "anyone?" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect((await findByTestId("chat-failure")).textContent).toContain("was cleared");
    await waitFor(() => expect(input.value).toBe("anyone?"));
    expect(useChat.getState().states.x.conversationId).toBe("c1");
  });

  it("a failed Ask call keeps the conversation, says so, and gives the question back", async () => {
    bridge.chatState.mockResolvedValue(state({ version: 9100 }));
    bridge.chatAsk.mockRejectedValue(new Error("bridge closed"));
    act(() => openChat("x"));
    const { findByLabelText, findByTestId } = render(<ChatDrawer />);
    const input = (await findByLabelText("question")) as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: "still there?" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect((await findByTestId("chat-failure")).textContent).toContain("bridge closed");
    await waitFor(() => expect(input.value).toBe("still there?"));
    expect(input.disabled).toBe(false);
    expect(useChat.getState().states.x.conversationId).toBe("c1");
  });

  // The first real-model run (2026-10-14, kind lab): a Deployment and an
  // HTTPRoute both named shop/api showed as two identical chips, and the
  // row stopped at 12 of the objects the answer named without saying so.
  it("chips name their kind and say how many they hold back", async () => {
    const names = Array.from({ length: 14 }, (_, i) => `obj-${i}`);
    bridge.chatState.mockResolvedValue(state({ version: 9150, exchanges: [exchange({
      answer: `api is failing; route api too; also ${names.join(", ")}.`,
      reads: [{ tool: "list_attention", outcome: "completed", refs: [
        { gvr: "apps/v1/deployments", namespace: "shop", name: "api", uid: "d1" },
        { gvr: "gateway.networking.k8s.io/v1/httproutes", namespace: "shop", name: "api", uid: "r1" },
        ...names.map((n, i) => ({ gvr: "karpenter.sh/v1/nodeclaims", name: n, uid: `n${i}` })),
      ] }],
    })] }));
    act(() => openChat("x"));
    const { findByTestId, getByTestId } = render(<ChatDrawer />);
    const chips = await findByTestId("chat-chips");
    expect(chips.textContent).toContain("Deployment shop/api");
    expect(chips.textContent).toContain("HTTPRoute shop/api");
    expect(chips.textContent).toContain("nodeclaims obj-0");
    expect(chips.querySelectorAll(".l-chip")).toHaveLength(12);
    expect(getByTestId("chat-chips-more").textContent).toBe("+4 more");
    fireEvent.click(getByTestId("chat-chips-more"));
    expect(getByTestId("chat-chips").querySelectorAll(".l-chip")).toHaveLength(16);
  });

  // Codex review of c4ae925: a refusal never replaces the conversation on
  // screen, even at an equal version, and a state read never erases the
  // latest request's outcome.
  it("a late refusal for an older request never replaces the current conversation", async () => {
    const old = deferred<ChatState>();
    bridge.chatState.mockResolvedValue(state({ version: 9200 }));
    bridge.chatAsk.mockReturnValue(old.promise);
    act(() => openChat("x"));
    const { findByLabelText, queryByTestId } = render(<ChatDrawer />);
    const input = (await findByLabelText("question")) as HTMLTextAreaElement;
    await waitFor(() => expect(input.disabled).toBe(false));
    fireEvent.change(input, { target: { value: "old question" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(bridge.chatAsk).toHaveBeenCalled());
    act(() => {
      useChat.getState().nextReq("x"); // a newer request is now the latest
      useChat.getState().apply("x", state({ version: 9300, conversationId: "c2" }));
    });
    await act(async () => old.resolve(state({ version: 9300, conversationId: undefined, error: "that conversation was cleared" })));
    expect(useChat.getState().states.x.conversationId).toBe("c2");
    expect(input.disabled).toBe(false);
    expect(queryByTestId("chat-failure")).toBeNull();
  });

  it("a background read keeps the latest request's notice", async () => {
    const bg = deferred<ChatState>();
    bridge.chatState.mockResolvedValueOnce(state({ version: 9400, active: true })).mockReturnValueOnce(bg.promise);
    bridge.chatAsk.mockResolvedValue(state({ version: 9400, active: true, notice: "a question is already being answered; stop it or wait" }));
    act(() => openChat("x"));
    const { findByLabelText, findByTestId, getByTestId } = render(<ChatDrawer />);
    const input = (await findByLabelText("question")) as HTMLTextAreaElement;
    await waitFor(() => expect(input.disabled).toBe(false));
    fireEvent.change(input, { target: { value: "my next question" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect((await findByTestId("chat-input-notice")).textContent).toContain("already being answered");
    await act(async () => bg.resolve(state({ version: 9400, active: true })));
    expect(getByTestId("chat-input-notice").textContent).toContain("already being answered");
  });
});
