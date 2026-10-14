import { useEffect, useRef } from "react";
import { IconX, IconMessageChatbot } from "@tabler/icons-react";
import { useFleet, type ResourceRef } from "../store/fleet";
import { chatAsk, chatClear, chatStart, chatState, chatStop, onChatRead, type ChatExchange, type ChatRead, type ChatRef, type ChatState } from "../bridge/chat";
import { providerLabel } from "../app/AISettingsPanel";
import { SafeMarkdown } from "./SafeMarkdown";
import { useChat } from "./chatStore";

// ChatDrawer is the cluster chat (spec 2026-10-14 §8): one conversation per
// cluster, read-only, docked on the right. The backend owns the
// conversation; the drawer sends only the question text.

// The backend caps a question at 4 KiB of UTF-8; the editor counts the same
// bytes, so a valid-looking CJK question is never refused after sending.
const QUESTION_MAX_BYTES = 4096;
const utf8Bytes = (s: string) => new TextEncoder().encode(s).length;

export function ChatDrawer() {
  const open = useChat((s) => s.open);
  const cluster = useChat((s) => s.cluster);
  if (!open || !cluster) return null;
  return <ChatPanel key={cluster} cluster={cluster} />;
}

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

// request runs one bridge call: its state goes through the version guard,
// and a bridge failure is shown beside the state - only if it is the
// cluster's latest request. A refusal is that request's outcome, not a
// conversation state: when its state loses to a newer one, its error or
// notice still shows, fenced by request order (Codex review of 0725e47).
async function request(cluster: string, call: () => Promise<ChatState>): Promise<ChatState | null> {
  const chat = useChat.getState();
  const n = chat.nextReq(cluster);
  try {
    const st = await call();
    const outcome = st.error || st.notice;
    if (useChat.getState().apply(cluster, st) || !outcome) useChat.getState().succeed(cluster, n);
    else useChat.getState().fail(cluster, n, outcome);
    logCalls(cluster, useChat.getState().states[cluster]);
    return st;
  } catch (e) {
    useChat.getState().fail(cluster, n, `Klyx could not reach its chat service: ${messageOf(e)}`);
    return null;
  }
}

// refresh reads the conversation back as a request of its own.
function refresh(cluster: string): Promise<ChatState | null> {
  return request(cluster, () => chatState(cluster));
}

// background reads the conversation back while something else is the
// latest request (a running Ask, a read event): its state still goes
// through the version guard, but it never claims or clears the failure
// slot, so an Ask's own failure is not hidden by it.
async function background(cluster: string): Promise<void> {
  try {
    useChat.getState().apply(cluster, await chatState(cluster));
    logCalls(cluster, useChat.getState().states[cluster]);
  } catch {
    // the next request reports a lasting failure
  }
}

function ChatPanel({ cluster }: { cluster: string }) {
  const st = useChat((s) => s.states[cluster]);
  const draft = useChat((s) => s.drafts[cluster] ?? "");
  const followUp = useChat((s) => s.seeds[cluster]);
  const asking = useChat((s) => s.asking[cluster]);
  const cleared = useChat((s) => s.cleared[cluster] ?? false);
  const failure = useChat((s) => s.failures[cluster]);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const endRef = useRef<HTMLDivElement>(null);
  const { setDraft, setSeed, setAsking, setCleared, close } = useChat.getState();

  useEffect(() => {
    let alive = true;
    void (async () => {
      const s = await refresh(cluster);
      // Opening the drawer starts a conversation - unless the operator
      // cleared it, which never starts another by itself.
      if (alive && s && !s.conversationId && !s.error && !useChat.getState().cleared[cluster]) {
        await request(cluster, () => chatStart(cluster));
      }
    })();
    inputRef.current?.focus();
    return () => {
      alive = false;
    };
  }, [cluster]);

  const conversationId = st?.conversationId;
  useEffect(() => {
    if (!conversationId) return;
    return onChatRead(conversationId, () => void background(cluster));
  }, [conversationId, cluster]);

  useEffect(() => {
    endRef.current?.scrollIntoView?.({ block: "end" });
  }, [st?.exchanges.length, asking]);

  const bytes = utf8Bytes(draft);
  const tooLong = bytes > QUESTION_MAX_BYTES;

  const ask = async () => {
    const q = draft.trim();
    if (!q || !conversationId || asking || tooLong) return;
    const seed = followUp?.seed;
    setDraft(cluster, "");
    setSeed(cluster, undefined);
    setAsking(cluster, { conversationId, question: q });
    const pending = request(cluster, () => chatAsk(cluster, conversationId, q, seed));
    void background(cluster); // shows the question as soon as the backend has it
    const next = await pending;
    if (useChat.getState().asking[cluster]?.question === q) setAsking(cluster, undefined);
    const refused = next === null || ((!!next.notice || !!next.error) && !next.exchanges.some((e) => e.question === q));
    if (refused && !useChat.getState().drafts[cluster]) {
      // A refusal (too long, one already running, the conversation gone):
      // the question comes back to the editor, and its follow-up target too.
      setDraft(cluster, q);
      if (seed && followUp) setSeed(cluster, followUp);
    }
  };

  const clear = async () => {
    if (!conversationId) return;
    await chatClear(cluster, conversationId);
    setCleared(cluster, true);
    await refresh(cluster);
  };

  const start = async () => {
    if (conversationId) await chatClear(cluster, conversationId);
    setCleared(cluster, false);
    await request(cluster, () => chatStart(cluster));
  };

  const inFlight = st?.exchanges.find((e) => e.outcome === "");
  const busy = !!asking || !!st?.active;
  const available = !!st && !st.error && !st.ended && !!conversationId;

  return (
    <aside className="chat-drawer" role="complementary" aria-label={`Ask about ${cluster}`} data-testid="chat-drawer"
      onKeyDown={(e) => { if (e.key === "Escape") { e.stopPropagation(); close(); } }}>
      <div className="chat-head">
        <IconMessageChatbot size={15} stroke={1.5} />
        <div style={{ minWidth: 0, flex: 1 }}>
          <div className="chat-title">Ask about {cluster}</div>
          {st?.provider && (
            <div className="t-legend chat-sub">
              {providerLabel(st.provider)}{st.model ? ` · ${st.model}` : ""} · {st.mode === "snapshot" ? "snapshot" : "tools"} · read-only
            </div>
          )}
        </div>
        <button type="button" className="l-btn" aria-label="close chat" onClick={close}><IconX size={13} stroke={1.5} /></button>
      </div>

      {st?.provider && (
        <div className="chat-small chat-guarantee" data-testid="chat-guarantee">
          Chat sends what it reads to {providerLabel(st.provider)}{st.origin ? ` (${st.origin})` : ""}. Logs and text are scrubbed, not guaranteed secret-free. Secret values, Helm values and ConfigMap values are never sent.
          {st.protected && <> This is a protected cluster; Settings &gt; AI allows its data to go to this provider.</>}
        </div>
      )}

      <div className="chat-body" data-testid="chat-body">
        {!st && <div className="chat-small">Loading…</div>}
        {st?.error && (
          <div className="chat-notice" role="alert">
            ▲ {st.error}
            {/Settings > AI|AI is off|unavailable|protected/.test(st.error) && (
              <div><button type="button" className="l-btn" onClick={() => useFleet.getState().openSettings()}>Open Settings</button></div>
            )}
          </div>
        )}
        {st && !st.conversationId && !st.error && cleared && (
          <div className="chat-notice" role="status">
            The conversation was cleared.
            <div><button type="button" className="l-btn" onClick={() => void start()}>Start a conversation</button></div>
          </div>
        )}
        {st && st.removed > 0 && <div className="chat-small">{st.removed} earlier exchange{st.removed === 1 ? " was" : "s were"} removed.</div>}
        {st?.trimmed && <div className="chat-small">Earlier messages are no longer sent to the model.</div>}
        {st?.mode === "snapshot" && st.exchanges.length === 0 && !asking && (
          <div className="chat-small">This provider answers from a snapshot Klyx assembles per question. Name objects as kind/name or namespace/name to bring them in.</div>
        )}
        {st?.exchanges.map((ex) => <Exchange key={ex.id} cluster={cluster} ex={ex} />)}
        {asking && asking.conversationId === conversationId && !inFlight && !st?.exchanges.some((e) => e.question === asking.question) && (
          <div className="chat-exchange" aria-busy="true">
            <div className="chat-q">{asking.question}</div>
            <div className="chat-small">Answering…</div>
          </div>
        )}
        {st?.ended && (
          <div className="chat-notice" role="status">
            {st.ended}
            <div><button type="button" className="l-btn" onClick={() => void start()}>Start a new conversation</button></div>
          </div>
        )}
        <div ref={endRef} />
      </div>

      <div className="chat-input">
        {followUp && (
          <div className="chat-chips" data-testid="chat-followup">
            <span className="l-chip">About {followUp.label}</span>
            <button type="button" className="l-btn" aria-label="remove follow-up target" onClick={() => setSeed(cluster, undefined)}><IconX size={11} stroke={1.5} /></button>
          </div>
        )}
        <textarea
          ref={inputRef}
          aria-label="question"
          value={draft}
          disabled={!available}
          placeholder={available ? "Ask about this cluster… (Enter to send, Shift+Enter for a new line)" : ""}
          onChange={(e) => setDraft(cluster, e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void ask();
            }
          }}
        />
        {failure && <div className="chat-small" role="alert" style={{ color: "var(--color-text-danger)" }} data-testid="chat-failure">{failure}</div>}
        {(tooLong || st?.notice) && (
          <div className="chat-small" role="status" style={{ color: "var(--color-text-warning)" }} data-testid="chat-input-notice">
            {tooLong ? `The question is ${bytes} bytes; the limit is ${QUESTION_MAX_BYTES} (UTF-8). Shorten it.` : st?.notice}
          </div>
        )}
        <div className="chat-actions">
          {busy && conversationId
            ? <button type="button" className="l-btn" onClick={() => void chatStop(cluster, conversationId)}>Stop</button>
            : <button type="button" className="l-btn" disabled={!available || draft.trim() === "" || tooLong} onClick={() => void ask()}>Ask</button>}
          <button type="button" className="l-btn" disabled={!conversationId || busy} onClick={() => void clear()}>Clear</button>
        </div>
      </div>
    </aside>
  );
}

function Exchange({ cluster, ex }: { cluster: string; ex: ChatExchange }) {
  const last = ex.calls[ex.calls.length - 1];
  return (
    <div className="chat-exchange" data-testid="chat-exchange">
      <div className="chat-q">{ex.question}</div>
      {ex.snapshot && ex.snapshot.length > 0 && (
        <details className="chat-small">
          <summary>Snapshot: what Klyx sent</summary>
          <ul>{ex.snapshot.map((s) => <li key={s}>{s}</li>)}</ul>
        </details>
      )}
      <Reads reads={ex.reads} />
      {ex.outcome === "answered" && ex.answer && (
        <>
          <SafeMarkdown text={ex.answer} />
          <RefChips cluster={cluster} answer={ex.answer} reads={ex.reads} />
          <div className="chat-small" style={{ color: "var(--color-text-tertiary)" }}>
            {last ? `${providerLabel(last.provider)}${last.model ? ` · ${last.model}` : ""} · ` : ""}AI-generated from Klyx's reads - verify before acting.
          </div>
        </>
      )}
      {ex.outcome === "failed" && <div className="chat-err">▲ {ex.error}</div>}
      {ex.note && <div className="chat-small">{ex.note}</div>}
      {ex.outcome === "cancelled" && <div className="chat-small">Stopped.</div>}
      {ex.outcome === "" && <div className="chat-small" aria-busy="true">Answering…</div>}
    </div>
  );
}

function Reads({ reads, open }: { reads: ChatRead[]; open?: boolean }) {
  if (reads.length === 0) return null;
  const redacted = reads.reduce((n, r) => n + (r.redacted ?? 0), 0);
  return (
    <details className="chat-reads" open={open} data-testid="chat-reads">
      <summary className="chat-small">{reads.length} read{reads.length === 1 ? "" : "s"}{redacted ? ` · ${redacted} redacted` : ""}</summary>
      <ul>
        {reads.map((r, i) => (
          <li key={i} className="chat-small">
            <code>{r.tool}</code>{r.target ? ` ${r.target}` : ""} - {r.outcome}{r.detail ? ` (${r.detail})` : ""}{r.redacted ? ` · ${r.redacted} redacted` : ""}
          </li>
        ))}
      </ul>
    </details>
  );
}

// RefChips are the answer's links into Klyx: built only from validated
// references in this exchange's reads (same cluster, GVR, namespace, name,
// UID) whose name the answer mentions. Text that merely looks like a
// reference stays text (spec §8).
export function RefChips({ cluster, answer, reads }: { cluster: string; answer: string; reads: ChatRead[] }) {
  const seen = new Set<string>();
  const refs: ChatRef[] = [];
  for (const r of reads) {
    for (const ref of r.refs ?? []) {
      const k = `${ref.gvr}|${ref.namespace ?? ""}|${ref.name}`;
      if (seen.has(k) || !mentions(answer, ref.name)) continue;
      seen.add(k);
      refs.push(ref);
    }
  }
  if (refs.length === 0) return null;
  return (
    <div className="chat-chips" data-testid="chat-chips">
      {refs.slice(0, 12).map((ref) => (
        <button key={`${ref.gvr}|${ref.namespace}|${ref.name}`} type="button" className="l-chip" title={`Open ${ref.gvr} ${ref.namespace ? `${ref.namespace}/` : ""}${ref.name} in Klyx`}
          onClick={() => openRef(cluster, ref)}>
          {ref.namespace ? `${ref.namespace}/` : ""}{ref.name}
        </button>
      ))}
    </div>
  );
}

function mentions(text: string, name: string): boolean {
  if (name.length < 2) return false;
  const esc = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(^|[^A-Za-z0-9-])${esc}($|[^A-Za-z0-9-])`).test(text);
}

function openRef(cluster: string, ref: ChatRef): void {
  const parts = ref.gvr.split("/");
  const [group, version, plural] = parts.length === 3 ? parts : ["", parts[0], parts[1]];
  const resource: ResourceRef = { group, version, plural, kind: plural, scope: ref.namespace ? "Namespaced" : "Cluster" };
  const nav = useFleet.getState();
  nav.openCluster(cluster);
  nav.openResource(resource);
  nav.openInstance(ref.namespace ?? "", ref.name);
}

// logCalls writes one Activity entry per attempted model call once it has
// an outcome - completed, failed, cancelled or tools-unsupported - and
// never twice. Metadata only: never question, answer or error text (Codex
// review of 143dc6d); the conversation keeps the diagnostics.
const logged = new Set<string>();

export function logCalls(cluster: string, st: ChatState | undefined): void {
  if (!st?.conversationId) return;
  const log = useFleet.getState().logActivity;
  for (const ex of st.exchanges) {
    ex.calls.forEach((c, i) => {
      if (c.outcome === "sent") return;
      const k = `${st.conversationId}:${ex.id}:${i}`;
      if (logged.has(k)) return;
      logged.add(k);
      if (logged.size > 2000) logged.delete(logged.values().next().value as string);
      const tools = c.tools && c.tools.length > 0 ? ` · tools: ${c.tools.join(", ")}` : "";
      log({
        kind: c.outcome === "completed" ? "success" : "error",
        message: `AI chat call - ${cluster} · ${c.mode} · ${providerLabel(c.provider)}${c.model ? ` (${c.model})` : ""}${tools} · ${c.outcome}`,
      });
    });
  }
}
