import { create } from "zustand";
import type { ChatSeed, ChatState } from "../bridge/chat";

// The chat drawer's UI state. The conversation itself lives in the backend;
// this keeps what the drawer shows: which cluster it is open for, the newest
// state read back per cluster (replies are ordered by the backend's
// version, never by arrival - Codex review of 143dc6d), the unsent draft,
// a follow-up's target, a question in flight, and whether the operator
// cleared the conversation (Clear never starts another by itself).
export type ChatAsking = { conversationId: string; question: string };
export type ChatFollowUp = { seed: ChatSeed; label: string };

type ChatUI = {
  open: boolean;
  cluster: string | null;
  states: Record<string, ChatState>;
  drafts: Record<string, string>;
  seeds: Record<string, ChatFollowUp | undefined>;
  asking: Record<string, ChatAsking | undefined>;
  cleared: Record<string, boolean>;
  // failures are bridge failures, shown beside the state, never as one;
  // reqs numbers requests per cluster so only the latest one's failure
  // shows (an older request failing later says nothing).
  failures: Record<string, string | undefined>;
  reqs: Record<string, number>;
  nextReq: (cluster: string) => number;
  fail: (cluster: string, req: number, message: string) => void;
  succeed: (cluster: string, req: number) => void;
  openFor: (cluster: string, followUp?: ChatFollowUp) => void;
  close: () => void;
  apply: (cluster: string, st: ChatState) => boolean;
  setDraft: (cluster: string, text: string) => void;
  setSeed: (cluster: string, f: ChatFollowUp | undefined) => void;
  setAsking: (cluster: string, a: ChatAsking | undefined) => void;
  setCleared: (cluster: string, v: boolean) => void;
};

export const useChat = create<ChatUI>((set, get) => ({
  open: false,
  cluster: null,
  states: {},
  drafts: {},
  seeds: {},
  asking: {},
  cleared: {},
  failures: {},
  reqs: {},
  nextReq: (cluster) => {
    const n = (get().reqs[cluster] ?? 0) + 1;
    set((s) => ({ reqs: { ...s.reqs, [cluster]: n } }));
    return n;
  },
  fail: (cluster, req, message) => {
    if (get().reqs[cluster] === req) set((s) => ({ failures: { ...s.failures, [cluster]: message } }));
  },
  succeed: (cluster, req) => {
    if (get().reqs[cluster] === req) set((s) => ({ failures: { ...s.failures, [cluster]: undefined } }));
  },
  openFor: (cluster, followUp) =>
    set((s) => ({ open: true, cluster, seeds: followUp ? { ...s.seeds, [cluster]: followUp } : s.seeds })),
  close: () => set({ open: false }),
  // apply keeps the newest state per cluster: an older reply (a lower
  // version) is dropped whatever order replies arrive in.
  apply: (cluster, st) => {
    const cur = get().states[cluster];
    if (cur && (st.version ?? 0) < (cur.version ?? 0)) return false;
    set((s) => ({ states: { ...s.states, [cluster]: st } }));
    return true;
  },
  setDraft: (cluster, text) => set((s) => ({ drafts: { ...s.drafts, [cluster]: text } })),
  setSeed: (cluster, f) => set((s) => ({ seeds: { ...s.seeds, [cluster]: f } })),
  setAsking: (cluster, a) => set((s) => ({ asking: { ...s.asking, [cluster]: a } })),
  setCleared: (cluster, v) => set((s) => ({ cleared: { ...s.cleared, [cluster]: v } })),
}));

// openChat opens the drawer for a cluster, optionally as a follow-up about
// one object.
export function openChat(cluster: string, followUp?: ChatFollowUp): void {
  useChat.getState().openFor(cluster, followUp);
}
