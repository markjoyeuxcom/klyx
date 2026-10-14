import { useEffect, useRef, useState } from "react";
import { IconX, IconMessageChatbot } from "@tabler/icons-react";
import { useFleet, type ResourceRef } from "../store/fleet";
import { chatAsk, chatClear, chatStart, chatState, chatStop, onChatRead, type ChatExchange, type ChatRead, type ChatRef, type ChatState } from "../bridge/chat";
import { providerLabel } from "../app/AISettingsPanel";
import { SafeMarkdown } from "./SafeMarkdown";
import { useChat } from "./chatStore";

// ChatDrawer is the cluster chat (spec 2026-10-14 §8): one conversation per
// cluster, read-only, docked on the right. The backend owns the
// conversation; the drawer sends only the question text.

const QUESTION_MAX = 4096;

export function ChatDrawer() {
  const open = useChat((s) => s.open);
  const cluster = useChat((s) => s.cluster);
  if (!open || !cluster) return null;
  return <ChatPanel key={cluster} cluster={cluster} />;
}

function ChatPanel({ cluster }: { cluster: string }) {
  const st = useChat((s) => s.states[cluster]);
  const draft = useChat((s) => s.drafts[cluster] ?? "");
  const live = useChat((s) => s.live);
  const [pending, setPending] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const endRef = useRef<HTMLDivElement>(null);
  const { setState, setDraft, addLive, clearLive, close } = useChat.getState();

  useEffect(() => {
    let alive = true;
    void (async () => {
      let s = await chatState(cluster);
      if (!s.conversationId && !s.error) s = await chatStart(cluster);
      if (alive) setState(cluster, s);
    })();
    inputRef.current?.focus();
    return () => {
      alive = false;
    };
  }, [cluster, setState]);

  const conversationId = st?.conversationId;
  useEffect(() => {
    if (!conversationId) return;
    return onChatRead(conversationId, (ex, r) => addLive(ex, r));
  }, [conversationId, addLive]);

  useEffect(() => {
    endRef.current?.scrollIntoView?.({ block: "end" });
  }, [st?.exchanges.length, pending, live]);

  const ask = async () => {
    const q = draft.trim();
    if (!q || !conversationId || pending) return;
    setDraft(cluster, "");
    setPending(q);
    clearLive();
    const next = await chatAsk(cluster, conversationId, q);
    setPending(null);
    clearLive();
    setState(cluster, next);
    logTurn(cluster, next);
  };

  const restart = async () => {
    if (conversationId) await chatClear(cluster, conversationId);
    setState(cluster, await chatStart(cluster));
  };

  const busy = pending !== null || !!st?.active;
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
        {st && st.removed > 0 && <div className="chat-small">{st.removed} earlier exchange{st.removed === 1 ? " was" : "s were"} removed.</div>}
        {st?.trimmed && <div className="chat-small">Earlier messages are no longer sent to the model.</div>}
        {st?.mode === "snapshot" && st.exchanges.length === 0 && !pending && (
          <div className="chat-small">This provider answers from a snapshot Klyx assembles per question. Name objects as kind/name or namespace/name to bring them in.</div>
        )}
        {st?.exchanges.map((ex) => <Exchange key={ex.id} cluster={cluster} ex={ex} />)}
        {pending && (
          <div className="chat-exchange" aria-busy="true">
            <div className="chat-q">{pending}</div>
            <Reads reads={Object.values(live).flat()} open />
            <div className="chat-small">Answering…</div>
          </div>
        )}
        {st?.ended && (
          <div className="chat-notice" role="status">
            {st.ended}
            <div><button type="button" className="l-btn" onClick={() => void restart()}>Start a new conversation</button></div>
          </div>
        )}
        <div ref={endRef} />
      </div>

      <div className="chat-input">
        <textarea
          ref={inputRef}
          aria-label="question"
          value={draft}
          maxLength={QUESTION_MAX}
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
        <div className="chat-actions">
          {busy && conversationId
            ? <button type="button" className="l-btn" onClick={() => void chatStop(cluster, conversationId)}>Stop</button>
            : <button type="button" className="l-btn" disabled={!available || draft.trim() === ""} onClick={() => void ask()}>Ask</button>}
          <button type="button" className="l-btn" disabled={!conversationId || busy} onClick={() => void restart()}>Clear</button>
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

// logTurn writes one Activity entry per turn naming each model call:
// provider, model, mode and the tools it asked for - never question or
// answer text.
function logTurn(cluster: string, st: ChatState): void {
  const ex = st.exchanges[st.exchanges.length - 1];
  if (!ex) return;
  const status = useFleet.getState().setActionStatus;
  const calls = ex.calls.map((c) => `${providerLabel(c.provider)}${c.model ? ` (${c.model})` : ""}${c.tools && c.tools.length > 0 ? `: ${c.tools.join(", ")}` : ""}${c.outcome && c.outcome !== "completed" ? ` - ${c.outcome}` : ""}`).join("; ");
  const redacted = ex.reads.reduce((n, r) => n + (r.redacted ?? 0), 0);
  const what = `AI chat - ${cluster} · ${st.mode} · ${ex.calls.length} model call${ex.calls.length === 1 ? "" : "s"}${calls ? ` [${calls}]` : ""}${redacted ? ` · ${redacted} redacted` : ""}`;
  if (ex.outcome === "answered") status({ kind: "success", message: what });
  else if (ex.outcome === "failed") status({ kind: "error", message: `${what} - failed: ${ex.error ?? ""}` });
}
