import { create } from "zustand";
import type { ChatSeed, ChatState } from "../bridge/chat";

// The chat drawer's UI state. The conversation itself lives in the backend;
// this keeps what the drawer shows: which cluster it is open for, the newest
// state read back per cluster (replies are ordered by the backend's
// version, never by arrival - Codex review of 143dc6d), the unsent draft,
// a follow-up's target, a question in flight, and whether the operator
// cleared the conversation (Clear never starts another by itself).
export type ChatAsking = { conversationId: string; question: string };
// ChatOutcome is what became of the cluster's latest request when it did
// not simply succeed: a refusal or a bridge failure (error), or a
// recoverable notice. It is kept apart from the conversation state.
export type ChatOutcome = { kind: "error" | "notice"; text: string };
export type ChatFollowUp = { seed: ChatSeed; label: string };
// ChatAnchor is the object an investigation is opened on (AI A1 §1).
export type ChatAnchor = { seed: ChatSeed; label: string };

// The drawer shows one slot: a cluster's chat (its slot is the cluster's
// name) or its investigation. Every map below is keyed by slot.
export const invSlot = (cluster: string) => `investigation\u0000${cluster}`;
const slotKind = (slot: string) => (slot.startsWith("investigation\u0000") ? "investigation" : "chat");

type ChatUI = {
  open: boolean;
  cluster: string | null;
  slot: string | null;
  anchors: Record<string, ChatAnchor | undefined>;
  states: Record<string, ChatState>;
  drafts: Record<string, string>;
  seeds: Record<string, ChatFollowUp | undefined>;
  asking: Record<string, ChatAsking | undefined>;
  cleared: Record<string, boolean>;
  // outcomes hold each cluster's latest request outcome, shown beside the
  // state and never as one; reqs numbers requests per cluster so only the
  // latest request settles it (an older request answering later says
  // nothing), and state reads never touch it (Codex review of c4ae925).
  outcomes: Record<string, ChatOutcome | undefined>;
  reqs: Record<string, number>;
  nextReq: (cluster: string) => number;
  settle: (cluster: string, req: number, outcome: ChatOutcome | undefined) => void;
  openFor: (cluster: string, followUp?: ChatFollowUp) => void;
  openInvestigationFor: (cluster: string, anchor: ChatAnchor) => void;
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
  slot: null,
  anchors: {},
  states: {},
  drafts: {},
  seeds: {},
  asking: {},
  cleared: {},
  outcomes: {},
  reqs: {},
  nextReq: (cluster) => {
    const n = (get().reqs[cluster] ?? 0) + 1;
    set((s) => ({ reqs: { ...s.reqs, [cluster]: n } }));
    return n;
  },
  settle: (cluster, req, outcome) => {
    if (get().reqs[cluster] === req) set((s) => ({ outcomes: { ...s.outcomes, [cluster]: outcome } }));
  },
  openFor: (cluster, followUp) =>
    set((s) => ({ open: true, cluster, slot: cluster, seeds: followUp ? { ...s.seeds, [cluster]: followUp } : s.seeds })),
  openInvestigationFor: (cluster, anchor) =>
    set((s) => ({ open: true, cluster, slot: invSlot(cluster), anchors: { ...s.anchors, [invSlot(cluster)]: anchor } })),
  close: () => set({ open: false }),
  // apply keeps the newest state per cluster: an older reply (a lower
  // version) is dropped whatever order replies arrive in. A reply with an
  // error is a refusal, not a conversation: it is never applied, whatever
  // its version, so it cannot replace the conversation on screen. Nor is a
  // reply for another kind of conversation: a late investigation reply
  // never turns the slot into the chat (AI A1 implementation review,
  // finding 11).
  apply: (cluster, st) => {
    const cur = get().states[cluster];
    if (st.error) return false;
    if (st.kind && st.kind !== slotKind(cluster)) return false;
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

// openInvestigation opens the drawer on a cluster's investigation of one
// object (AI A1 §5).
export function openInvestigation(cluster: string, seed: ChatSeed, label: string): void {
  useChat.getState().openInvestigationFor(cluster, { seed, label });
}
