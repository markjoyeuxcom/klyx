import { describe, it, expect } from "vitest";
import { useChat, invSlot } from "./chatStore";
import type { ChatState } from "../bridge/chat";

// AI A3 stage 2 re-review: a withdrawal publishes a newer version, so a
// delayed older reply that still shows the offer is never applied.
describe("chat store reply order", () => {
  it("keeps a withdrawn plan when an older offered reply arrives late", () => {
    const slot = invSlot("x");
    const state = (version: number, stepState: string) =>
      ({ conversationId: "inv-1", cluster: "x", kind: "investigation", version, exchanges: [{ id: "e1", plan: { steps: [{ n: 1, state: stepState }] } }] }) as unknown as ChatState;
    expect(useChat.getState().apply(slot, state(10, "withdrawn"))).toBe(true);
    expect(useChat.getState().apply(slot, state(9, "offered"))).toBe(false);
    const shown = useChat.getState().states[slot] as unknown as { exchanges: { plan: { steps: { state: string }[] } }[] };
    expect(shown.exchanges[0].plan.steps[0].state).toBe("withdrawn");
  });
});
