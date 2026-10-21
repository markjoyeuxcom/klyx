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
  investigationStart: vi.fn(),
  investigationState: vi.fn(),
  chatSetComparison: vi.fn(),
}));
vi.mock("../bridge/chat", () => bridge);

import { ChatDrawer } from "./ChatDrawer";
import { useChat, openChat, openInvestigation, invSlot } from "./chatStore";
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

  // The Line slice 8a (parent §5.3): the reads are a Rail in read order, each
  // step's segment its outcome; a read's redaction count stays on its line.
  it("draws the reads as a ledger: completed in route, failed in danger, cancelled dashed", async () => {
    bridge.chatState.mockResolvedValue(state({ version: 1000, exchanges: [exchange({
      answer: "done",
      reads: [
        { tool: "get_resource", target: "Pod shop/web-7f", outcome: "completed", redacted: 2 },
        { tool: "pod_logs", target: "shop/web-7f", outcome: "truncated", detail: "last 200 lines" },
        { tool: "list_events", target: "shop", outcome: "failed", detail: "forbidden" },
        { tool: "get_resource", target: "Service shop/web", outcome: "cancelled" },
      ],
    })] }));
    act(() => openChat("x"));
    const { findByTestId } = render(<ChatDrawer />);
    const reads = await findByTestId("chat-reads");
    const steps = reads.querySelectorAll("[data-rail] > li");
    expect([...steps].map((s) => s.getAttribute("data-segment"))).toEqual(["completed", "completed", "failed", "cancelled"]);
    expect(steps[0].textContent).toBe("completed: get_resource Pod shop/web-7f · 2 redacted");
    expect(steps[1].querySelector("[data-tag]")?.textContent).toBe("truncated");
    expect(steps[2].querySelector("[data-seg-words]")?.textContent).toBe("failed: ");
    expect(steps[2].textContent).toContain("(forbidden)");
    expect(steps[3].querySelector("[data-seg-words]")?.textContent).toBe("cancelled: ");
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

// AI A1 stage 1: each read's citation ID and Klyx's checks in the ledger, and
// an answer's [r1] or [r1.c1] a chip only when the ledger holds it.
describe("citations (AI A1 §4)", () => {
  beforeEach(() => {
    for (const f of Object.values(bridge)) f.mockReset?.();
    bridge.onChatRead.mockImplementation(() => () => {});
    useChat.setState({ open: false, cluster: null, states: {}, drafts: {}, seeds: {}, asking: {}, cleared: {}, outcomes: {}, reqs: {} });
  });

  it("shows read IDs and checks, and chips only the IDs the ledger holds", async () => {
    bridge.chatState.mockResolvedValue(state({ exchanges: [exchange({
      answer: "web crash-loops [r1.c1] after its last change [r1]; see also [r9] and [r1](https://example.com).",
      reads: [{ id: "r1", tool: "get_resource", target: "Pod shop/web", outcome: "completed",
        checks: [{ id: "r1.c1", class: "status", subject: { kind: "Pod", namespace: "shop", name: "web", uid: "u1", identity: "uid" }, statement: "container web of Pod shop/web: waiting CrashLoopBackOff" }] }],
    })] }));
    act(() => openChat("x"));
    const { findAllByTestId, getByTestId, getByText } = render(<ChatDrawer />);
    const chips = await findAllByTestId("chat-cite");
    expect(chips.map((c) => c.textContent)).toEqual(["r1.c1", "r1"]);
    expect(chips[0].getAttribute("title")).toBe("container web of Pod shop/web: waiting CrashLoopBackOff");
    // an ID the ledger does not hold stays text; a link stays a link
    expect(getByTestId("safe-markdown").textContent).toContain("[r9]");
    expect(getByText("r1", { selector: "button.md-link" })).toBeTruthy();
    expect(getByTestId("chat-checks").textContent).toContain("r1.c1container web of Pod shop/web: waiting CrashLoopBackOff");
    // a chip opens its read in the ledger
    const reads = getByTestId("chat-reads") as HTMLDetailsElement;
    expect(reads.open).toBe(false);
    fireEvent.click(chips[0]);
    expect(reads.open).toBe(true);
  });
});

describe("investigations (AI A1 §5)", () => {
  const seed = { kind: "row" as const, task: "workloads", part: "workloads", uid: "uid-d" };
  const reset = () => {
    for (const f of Object.values(bridge)) f.mockReset?.();
    bridge.onChatRead.mockImplementation(() => () => {});
    bridge.chatClear.mockImplementation(async () => {});
    useChat.setState({ open: false, cluster: null, slot: null, anchors: {}, states: {}, drafts: {}, seeds: {}, asking: {}, cleared: {}, outcomes: {}, reqs: {} });
    useFleet.setState({ clusters: ["x", "stg", "prd"].map((name) => ({ name })) as never });
  };
  beforeEach(reset);
  const inv = (over: Partial<ChatState> = {}) => state({ conversationId: "i1", kind: "investigation", anchor: seed, ...over });

  it("starts on its object, beside the chat, and investigates without a typed question", async () => {
    bridge.investigationState.mockResolvedValue(inv({ conversationId: undefined, anchor: undefined }));
    bridge.investigationStart.mockResolvedValue(inv());
    bridge.chatAsk.mockResolvedValue(inv({ exchanges: [exchange({ question: "Investigate why this object is failing, and end with your findings." })] }));
    act(() => openInvestigation("x", seed, "Deployment shop/web"));
    const { findByText, getByLabelText } = render(<ChatDrawer />);
    await waitFor(() => expect(bridge.investigationStart).toHaveBeenCalledWith("x", seed));
    expect(bridge.chatStart).not.toHaveBeenCalled();
    expect(getByLabelText("Investigate Deployment shop/web")).toBeTruthy();
    const go = await findByText("Investigate", { selector: "button" });
    await waitFor(() => expect((go as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(go);
    await waitFor(() => expect(bridge.chatAsk).toHaveBeenCalledWith("x", "i1", "Investigate why this object is failing, and end with your findings.", undefined));
    expect(useChat.getState().states[invSlot("x")]?.conversationId).toBe("i1");
    expect(useChat.getState().states["x"]).toBeUndefined();
  });

  it("labels findings with Klyx's words and chips only ledger IDs", async () => {
    bridge.investigationState.mockResolvedValue(inv({ exchanges: [exchange({
      answer: "web is failing [r1.c1].",
      reads: [{ id: "r1", tool: "get_resource", target: "Pod shop/web", outcome: "completed",
        checks: [{ id: "r1.c1", class: "status", subject: { kind: "Pod", namespace: "shop", name: "web", uid: "u1", identity: "uid" }, statement: "container web of Pod shop/web: waiting CrashLoopBackOff" }] }],
      findings: {
        window: "2026-10-21T12:05:00Z",
        findings: [
          { type: "observation", label: "reading", statement: "the pod crash-loops", subject: { kind: "Pod", namespace: "shop", name: "web" }, cites: ["r1.c1"],
            checked: [{ id: "r1.c1", class: "status", statement: "container web of Pod shop/web: waiting CrashLoopBackOff", before: true }] },
          { type: "cause", label: "suspected", statement: "the new image is broken", subject: { kind: "Pod", namespace: "shop", name: "web" }, cites: ["r1"] },
          { type: "cause", label: "unsupported", statement: "<b>api</b> is down", subject: { kind: "Deployment", namespace: "shop", name: "api" }, cites: ["r9"], notes: ["cites r9, not in this investigation's reads"] },
        ],
        missing: [{ from: "klyx", read: "r1", text: "get_metrics pod_cpu shop/web failed" }, { from: "model", text: "the registry" }],
        remedy: { git: "pin the image to 1.0", label: "proposed, not run" },
      },
    })] }));
    act(() => openInvestigation("x", seed, "Deployment shop/web"));
    const { findAllByTestId, getByTestId } = render(<ChatDrawer />);
    const items = await findAllByTestId("inv-finding");
    expect(items.map((i) => i.querySelector(".inv-label")?.textContent)).toEqual(["reading", "suspected", "unsupported"]);
    expect(items[0].textContent).toContain("checked");
    expect(items[0].textContent).toContain("observed before the failure was reported");
    // a statement is plain text, never markup
    expect(items[2].querySelector("b")).toBeNull();
    expect(items[2].textContent).toContain("<b>api</b> is down");
    // cites chip only an ID the ledger holds
    expect(items[1].querySelector("button.chat-cite")?.textContent).toBe("r1");
    expect(items[2].querySelector("button.chat-cite")).toBeNull();
    expect(getByTestId("inv-window").textContent).toContain("2026-10-21T12:05:00Z");
    expect(getByTestId("inv-missing").textContent).toContain("the model's note");
    expect(getByTestId("inv-remedy").textContent).toContain("proposed, not run");
    for (const l of document.querySelectorAll(".inv-finding > .inv-head > .inv-label")) {
      expect(["checked", "proven"]).not.toContain(l.textContent);
    }
  });

  it("fixes the comparison set before the first question and locks it after", async () => {
    bridge.investigationState.mockResolvedValue(inv());
    bridge.chatSetComparison.mockResolvedValue(inv({ comparison: ["stg"] }));
    act(() => openInvestigation("x", seed, "Deployment shop/web"));
    const view = render(<ChatDrawer />);
    const box = await view.findByLabelText("stg");
    expect(view.queryByLabelText("x")).toBeNull();
    fireEvent.click(box);
    fireEvent.click(view.getByText("Set comparison"));
    await waitFor(() => expect(bridge.chatSetComparison).toHaveBeenCalledWith("x", "i1", ["stg"]));
    view.unmount();
    reset();
    bridge.investigationState.mockResolvedValue(inv({ comparison: ["stg"], exchanges: [exchange()] }));
    act(() => openInvestigation("x", seed, "Deployment shop/web"));
    const after = render(<ChatDrawer />);
    const fixed = await after.findByTestId("inv-comparison");
    expect(fixed.textContent).toContain("Compared with stg");
    expect(after.queryByText("Set comparison")).toBeNull();
    expect(fixed.querySelector("input")).toBeNull();
  });

  it("says when another object's investigation is open", async () => {
    bridge.investigationState.mockResolvedValue(inv({ anchor: { kind: "object", group: "apps", version: "v1", plural: "deployments", namespace: "shop", name: "api", uid: "uid-api" } }));
    act(() => openInvestigation("x", seed, "Deployment shop/web"));
    const { findByTestId, getByText } = render(<ChatDrawer />);
    expect((await findByTestId("inv-other")).textContent).toContain("An investigation of api is open on x");
    expect((getByText("Investigate", { selector: "button" }) as HTMLButtonElement).disabled).toBe(true);
    expect(bridge.investigationStart).not.toHaveBeenCalled();
  });
});
