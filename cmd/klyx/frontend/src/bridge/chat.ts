import { Events } from "@wailsio/runtime";
import { ChatService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The cluster chat bridge (spec 2026-10-14 §8). The backend owns every
// conversation: the drawer sends only the operator's question text and reads
// the conversation back. Failures fold into the state's error.

export type ChatRef = { gvr: string; namespace?: string; name: string; uid?: string };
// AI A1 §2: a check is an observation Klyx evaluated itself on a read, by
// UID or by name only; id is its citation ("r4.c1").
export type ChatCheckSubject = { kind: string; namespace?: string; name: string; uid?: string; identity: "uid" | "name" };
// failing: the check reads a failure in force when read, not a past one.
export type ChatCheck = { id: string; class: string; subject: ChatCheckSubject; statement: string; observed?: string; onset?: string; coverage?: string; failing?: boolean };
// id is the read's citation ("r4") within its conversation; gaps are the
// parts it could not read; about, what a by-name read is about; observed*,
// when a historical read's evidence describes the world.
export type ChatRead = {
  id?: string; tool: string; target?: string; outcome: string; detail?: string; redacted?: number; refs?: ChatRef[]; checks?: ChatCheck[];
  gaps?: string[]; about?: ChatCheckSubject[]; observedFrom?: string; observedTo?: string; observedUnknown?: boolean;
};
export type ChatModelCall = { provider: string; model: string; mode: string; tools?: string[]; outcome: string };
// AI A1 §4: an investigation turn's findings as Klyx labels them. A finding
// is the model's; a check it cites is Klyx's, shown as checked.
export type FindingCheck = { id: string; class: string; statement: string; observed?: string; before?: boolean };
export type Finding = {
  type: "observation" | "cause";
  label: "reading" | "suspected" | "unsupported";
  statement: string;
  subject: { kind: string; namespace?: string; name: string; uid?: string; identity?: string };
  cites: string[];
  checked?: FindingCheck[];
  notes?: string[];
};
export type Missing = { from: "klyx" | "model"; read?: string; text: string };
export type Remedy = { git?: string; mitigation?: string; label: string };
export type Findings = { window: string; findings: Finding[]; missing: Missing[]; remedy?: Remedy; note?: string; scope?: string };

// AI A3: a turn's proposal as Klyx decided it. A step names a matrix verb on
// an object Klyx read by UID; its state is Klyx's (offered, or refused,
// unsupported, not offered, withdrawn or superseded, with the reason); a
// step that ran carries its request's outcome and its effect.
export type StepRunState = {
  request?: "written" | "refused" | "unverified" | string;
  notSent?: boolean;
  error?: string;
  effect?: "pending" | "established" | "unverified" | string;
  effectWhy?: string;
  ranAt?: string;
};
export type ActionStep = {
  n: number;
  verb: string;
  target: ChatCheckSubject;
  replicas?: number;
  revision?: number;
  why: string;
  cites: string[];
  state: string;
  reason?: string;
  danger?: boolean;
  run?: StepRunState;
  factsRead?: string;
  factsAt?: string;
  gvr?: string; // the bound object's API, for its chip
};
export type IncidentSubject = { kind: string; namespace?: string; name: string; uid: string; reading: string; verdict?: string; at?: string };
export type Incident = { state: "pending" | "resolved" | "not resolved" | "unverified" | string; why?: string; subjects: IncidentSubject[] };
export type ActionPlan = {
  steps: ActionStep[];
  note?: string;
  status?: "" | "superseded" | "withdrawn" | "declined" | "held" | string;
  statusWhy?: string;
  incident?: Incident;
};
// What the ConfirmDialog shows for one step, read again by Klyx, with the
// token RunStep requires (§4.1).
export type StepReview = {
  offered: boolean;
  reason?: string;
  token?: string;
  expiresAt?: string;
  n: number;
  verb: string;
  target: ChatCheckSubject;
  change?: string;
  consequence?: string[];
  expected?: string;
  danger: boolean;
  observed?: { what: string; at: string }[];
};
export type StepResult = { ok?: boolean; error?: string; outcome?: string; notSent?: boolean };
export type StepRun = { ran: boolean; reason?: string; result?: StepResult; plan?: ActionPlan };
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
  findings?: Findings;
  plan?: ActionPlan;
};
export type ChatState = {
  conversationId?: string;
  cluster: string;
  kind?: "chat" | "investigation";
  anchor?: ChatSeed; // an investigation's object, pinned by its UID
  comparison?: string[]; // an investigation's comparison clusters (A-C2)
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

// An investigation (AI A1 §1): one per cluster beside its chat, pinned to
// one object by UID; Ask, Stop and Clear work on it by its conversation ID.
export async function investigationStart(cluster: string, anchor: ChatSeed): Promise<ChatState> {
  return (await ChatService.StartInvestigation(cluster, anchor as never)) as ChatState;
}

export async function investigationState(cluster: string): Promise<ChatState> {
  return (await ChatService.InvestigationState(cluster)) as ChatState;
}

// chatSetComparison fixes an investigation's comparison set before its
// first question (A-C2).
export async function chatSetComparison(cluster: string, conversationId: string, members: string[]): Promise<ChatState> {
  return (await ChatService.SetComparison(cluster, conversationId, members)) as ChatState;
}

// The plan's steps run only through these (AI A3 §4): Review reads the step
// again and returns a token; Run consumes the protected grant the dialog
// armed, re-reads, and calls the verb's own code; Dismiss declines the
// plan. Each throws only when the bridge itself failed.
export async function reviewStep(cluster: string, conversationId: string, exchangeId: string, n: number): Promise<StepReview> {
  return (await ChatService.ReviewStep(cluster, conversationId, exchangeId, n)) as StepReview;
}

export async function runStep(cluster: string, conversationId: string, exchangeId: string, n: number, token: string): Promise<StepRun> {
  return (await ChatService.RunStep(cluster, conversationId, exchangeId, n, token)) as StepRun;
}

export async function dismissPlan(cluster: string, conversationId: string, exchangeId: string): Promise<StepRun> {
  return (await ChatService.DismissPlan(cluster, conversationId, exchangeId)) as StepRun;
}

// onChatPlan subscribes to a conversation's plan changes made in the
// background: a step's effect settled, its incident read.
export function onChatPlan(conversationId: string, f: () => void): () => void {
  return Events.On(`chat-plan:${conversationId}`, () => f());
}

// onChatRead subscribes to a conversation's ledger as reads finish.
export function onChatRead(conversationId: string, f: (exchangeId: string, read: ChatRead) => void): () => void {
  return Events.On(`chat:${conversationId}`, (ev: { data: { exchangeId: string; read: ChatRead } }) => f(ev.data.exchangeId, ev.data.read));
}
