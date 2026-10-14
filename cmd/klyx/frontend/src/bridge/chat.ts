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
};

function failed(cluster: string, e: unknown): ChatState {
  return { cluster, protected: false, trimmed: false, removed: 0, active: false, exchanges: [], error: e instanceof Error ? e.message : String(e) };
}

export async function chatStart(cluster: string): Promise<ChatState> {
  try {
    return (await ChatService.Start(cluster)) as ChatState;
  } catch (e) {
    return failed(cluster, e);
  }
}

export async function chatState(cluster: string): Promise<ChatState> {
  try {
    return (await ChatService.State(cluster)) as ChatState;
  } catch (e) {
    return failed(cluster, e);
  }
}

export async function chatAsk(cluster: string, conversationId: string, question: string): Promise<ChatState> {
  try {
    return (await ChatService.Ask(cluster, conversationId, question)) as ChatState;
  } catch (e) {
    return failed(cluster, e);
  }
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
