import { create } from "zustand";
import type { ChatRead, ChatState } from "../bridge/chat";

// The chat drawer's UI state. The conversation itself lives in the backend;
// this keeps only what the drawer shows: which cluster it is open for, the
// last state read back, the operator's unsent draft, and reads that arrive
// while a turn runs.
type ChatUI = {
  open: boolean;
  cluster: string | null;
  states: Record<string, ChatState>;
  drafts: Record<string, string>;
  live: Record<string, ChatRead[]>; // exchange id -> reads so far
  openFor: (cluster: string, draft?: string) => void;
  close: () => void;
  setState: (cluster: string, st: ChatState) => void;
  setDraft: (cluster: string, text: string) => void;
  addLive: (exchangeId: string, read: ChatRead) => void;
  clearLive: () => void;
};

export const useChat = create<ChatUI>((set) => ({
  open: false,
  cluster: null,
  states: {},
  drafts: {},
  live: {},
  openFor: (cluster, draft) =>
    set((s) => ({ open: true, cluster, drafts: draft !== undefined ? { ...s.drafts, [cluster]: draft } : s.drafts })),
  close: () => set({ open: false }),
  setState: (cluster, st) => set((s) => ({ states: { ...s.states, [cluster]: st } })),
  setDraft: (cluster, text) => set((s) => ({ drafts: { ...s.drafts, [cluster]: text } })),
  addLive: (exchangeId, read) => set((s) => ({ live: { ...s.live, [exchangeId]: [...(s.live[exchangeId] ?? []), read] } })),
  clearLive: () => set({ live: {} }),
}));

// openChat opens the drawer for a cluster, optionally with a seeded draft.
export function openChat(cluster: string, draft?: string): void {
  useChat.getState().openFor(cluster, draft);
}
