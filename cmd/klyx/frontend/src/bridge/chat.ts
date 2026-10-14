import { Events } from "@wailsio/runtime";
import { ChatService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The cluster chat bridge (spec 2026-10-14 §8). The backend owns every
// conversation: the drawer sends only the operator's question text and reads
// the conversation back. Failures fold into the state's error.

export type ChatRef = { gvr: string; namespace?: string; name: string; uid?: string };
export type ChatRead = { tool: string; target?: string; outcome: string; detail?: string; redacted?: number; refs?: ChatRef[] };
export type ChatModelCall = { provider: string; model: string; mode: string; tools?: string[]; outcome: string };
export type ChatExchange = {
  id: string;
  question: string;
  answer?: string;
  outcome: "answered" | "failed" | "cancelled" | "";
  error?: string;
  reads: ChatRead[];
  calls: ChatModelCall[];
  snapshot?: string[];
  note?: string;
  at: string;
};
export type ChatState = {
  conversationId?: string;
  cluster: string;
  mode?: "tools" | "snapshot" | string;
  provider?: string;
  model?: string;
  origin?: string;
  protected: boolean;
  ended?: string;
  error?: string;
  trimmed: boolean;
  removed: number;
  active: boolean;
  exchanges: ChatExchange[];
  notice?: string; // a recoverable problem with the last request
  version: number; // orders replies: an older state is never applied over a newer one
};

// ChatSeed is what an Explain follow-up is about, as Explain targeted it.
// The backend resolves and reads it, fenced by its UID.
export type ChatSeed =
  | { kind: "row"; task: string; part: string; uid: string }
  | { kind: "object"; group: string; version: string; plural: string; namespace?: string; name: string; uid: string }
  | { kind: "helm"; namespace: string; name: string };

// The calls below return the backend's state, or throw when the bridge
// itself failed: a transport failure is never turned into a conversation
// state (Codex review of 0725e47) - the drawer shows it beside the state.
export async function chatStart(cluster: string): Promise<ChatState> {
  return (await ChatService.Start(cluster)) as ChatState;
}

export async function chatState(cluster: string): Promise<ChatState> {
  return (await ChatService.State(cluster)) as ChatState;
}

export async function chatAsk(cluster: string, conversationId: string, question: string, seed?: ChatSeed): Promise<ChatState> {
  return (await ChatService.Ask(cluster, conversationId, question, (seed ?? null) as never)) as ChatState;
}

export async function chatStop(cluster: string, conversationId: string): Promise<void> {
  await ChatService.Stop(cluster, conversationId);
}

export async function chatClear(cluster: string, conversationId: string): Promise<void> {
  await ChatService.Clear(cluster, conversationId);
}

// onChatRead subscribes to a conversation's ledger as reads finish.
export function onChatRead(conversationId: string, f: (exchangeId: string, read: ChatRead) => void): () => void {
  return Events.On(`chat:${conversationId}`, (ev: { data: { exchangeId: string; read: ChatRead } }) => f(ev.data.exchangeId, ev.data.read));
}
