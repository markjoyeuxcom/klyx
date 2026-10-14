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
import type { ChatState } from "../bridge/chat";

function state(over: Partial<ChatState> = {}): ChatState {
  return { conversationId: "c1", cluster: "x", mode: "tools", provider: "anthropic", model: "claude-opus-5-5", origin: "https://api.anthropic.com", protected: false, trimmed: false, removed: 0, active: false, exchanges: [], ...over };
}

describe("ChatDrawer", () => {
  beforeEach(() => {
    for (const f of Object.values(bridge)) f.mockReset?.();
    bridge.onChatRead.mockImplementation(() => () => {});
    bridge.chatStop.mockImplementation(async () => {});
    bridge.chatClear.mockImplementation(async () => {});
    useChat.setState({ open: false, cluster: null, states: {}, drafts: {}, live: {} });
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
    bridge.chatAsk.mockResolvedValue(state({ exchanges: [{
      id: "e1", question: "why is web failing?", outcome: "answered", at: "2026-10-14T12:00:00Z",
      answer: "Pod **web-7f** is crash-looping; `api` is fine. Also see orphan-x.",
      reads: [
        { tool: "get_resource", target: "Pod shop/web-7f", outcome: "completed", redacted: 2, refs: [{ gvr: "v1/pods", namespace: "shop", name: "web-7f", uid: "u1" }] },
        { tool: "list_resources", target: "Pod in shop", outcome: "completed", refs: [{ gvr: "v1/pods", namespace: "shop", name: "unmentioned", uid: "u2" }] },
      ],
      calls: [{ provider: "anthropic", model: "claude-opus-5-5", mode: "tools", tools: ["get_resource"] }],
    }] }));
    const status = vi.spyOn(useFleet.getState(), "setActionStatus");
    act(() => openChat("x"));
    const { findByLabelText, findByTestId, getByTestId } = render(<ChatDrawer />);
    const input = await findByLabelText("question");
    fireEvent.change(input, { target: { value: "why is web failing?" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(bridge.chatAsk).toHaveBeenCalledWith("x", "c1", "why is web failing?"));
    expect((await findByTestId("chat-reads")).textContent).toContain("2 reads · 2 redacted");
    const chips = getByTestId("chat-chips");
    expect(chips.textContent).toBe("shop/web-7f"); // orphan-x is text; an unmentioned ref has no chip
    expect(getByTestId("safe-markdown").querySelector("strong")?.textContent).toBe("web-7f");
    expect(status).toHaveBeenCalledWith(expect.objectContaining({ kind: "success", message: expect.stringContaining("AI chat - x · tools · 1 model call") }));
    expect(status.mock.calls[0][0].message).not.toContain("why is web failing");
  });

  it("says why chat is unavailable, with the fix", async () => {
    bridge.chatState.mockResolvedValue(state({ conversationId: undefined }));
    bridge.chatStart.mockResolvedValue({ cluster: "x", protected: true, trimmed: false, removed: 0, active: false, exchanges: [], error: "AI is disabled for protected cluster \"x\": the configured AI endpoint is not on this machine" });
    act(() => openChat("x"));
    const { findByRole, getByLabelText } = render(<ChatDrawer />);
    expect((await findByRole("alert")).textContent).toContain("protected cluster");
    expect((getByLabelText("question") as HTMLTextAreaElement).disabled).toBe(true);
  });

  it("an ended conversation offers a new one", async () => {
    bridge.chatState.mockResolvedValue(state({ ended: "AI settings changed; start a new conversation" }));
    bridge.chatStart.mockResolvedValue(state({ conversationId: "c2" }));
    act(() => openChat("x"));
    const { findByText } = render(<ChatDrawer />);
    fireEvent.click(await findByText("Start a new conversation"));
    await waitFor(() => expect(bridge.chatClear).toHaveBeenCalledWith("x", "c1"));
    await waitFor(() => expect(useChat.getState().states.x.conversationId).toBe("c2"));
  });

  it("a seeded follow-up opens with the object named", async () => {
    bridge.chatState.mockResolvedValue(state());
    act(() => openChat("x", "About pod shop/web: "));
    const { findByLabelText } = render(<ChatDrawer />);
    expect(((await findByLabelText("question")) as HTMLTextAreaElement).value).toBe("About pod shop/web: ");
  });
});
