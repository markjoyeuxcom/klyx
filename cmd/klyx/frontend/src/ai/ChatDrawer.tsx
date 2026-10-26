import { useEffect, useRef, useState, useMemo } from "react";
import { IconX, IconMessageChatbot } from "@tabler/icons-react";
import { useFleet, type ResourceRef } from "../store/fleet";
import { chatAsk, chatClear, chatSetComparison, chatStart, chatState, chatStop, investigationStart, investigationState, onChatPlan, onChatRead, type ActionStep, type ChatCheck, type ChatExchange, type ChatRead, type ChatRef, type ChatSeed, type ChatState } from "../bridge/chat";
import { providerLabel } from "../app/AISettingsPanel";
import { WELL_KNOWN_RESOURCE_REFS } from "../cluster/resourceRefs";
import { SafeMarkdown, type Cite } from "./SafeMarkdown";
import { useChat } from "./chatStore";
import { FindingsPanel } from "./FindingsPanel";
import { ActionCard } from "./ActionCard";
import { Rail, type RailSegment, type RailStep } from "../chrome/primitives";

// ChatDrawer is the cluster chat (spec 2026-10-14 §8): one conversation per
// cluster, read-only, docked on the right. The backend owns the
// conversation; the drawer sends only the question text.

// The backend caps a question at 4 KiB of UTF-8; the editor counts the same
// bytes, so a valid-looking CJK question is never refused after sending.
const QUESTION_MAX_BYTES = 4096;
const utf8Bytes = (s: string) => new TextEncoder().encode(s).length;

// The first turn of an investigation needs no question of the operator's
// (AI A1 §1); the drawer sends the backend's own default when none is typed.
const INVESTIGATE_Q = "Investigate why this object is failing, and end with your findings.";

export function ChatDrawer() {
  const open = useChat((s) => s.open);
  const cluster = useChat((s) => s.cluster);
  const slot = useChat((s) => s.slot) ?? cluster;
  if (!open || !cluster || !slot) return null;
  return <ChatPanel key={slot} cluster={cluster} slot={slot} />;
}

const seedUID = (s: ChatSeed | undefined) => (s && "uid" in s ? s.uid : "");
const seedName = (s: ChatSeed | undefined) => (s && "name" in s ? s.name : "the object");

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

// request runs one bridge call: its state goes through the version guard
// (a refusal is never applied), and its outcome - a refusal, a notice or a
// bridge failure - settles the outcome slot only if it is the cluster's
// latest request (Codex reviews of 0725e47 and c4ae925).
// A slot is a cluster's chat (its name) or its investigation: each is read
// back through its own call.
async function request(slot: string, cluster: string, call: () => Promise<ChatState>): Promise<ChatState | null> {
  const n = useChat.getState().nextReq(slot);
  try {
    const st = await call();
    useChat.getState().apply(slot, st);
    useChat.getState().settle(slot, n, st.error ? { kind: "error", text: st.error } : st.notice ? { kind: "notice", text: st.notice } : undefined);
    logCalls(cluster, useChat.getState().states[slot]);
    return st;
  } catch (e) {
    useChat.getState().settle(slot, n, { kind: "error", text: `Klyx could not reach its chat service: ${messageOf(e)}` });
    return null;
  }
}

const reader = (slot: string, cluster: string) => (slot === cluster ? () => chatState(cluster) : () => investigationState(cluster));

// refresh reads the conversation back as a request of its own.
function refresh(slot: string, cluster: string): Promise<ChatState | null> {
  return request(slot, cluster, reader(slot, cluster));
}

// background reads the conversation back while something else is the
// latest request (a running Ask, a read event): its state still goes
// through the version guard, but it never claims or settles the outcome
// slot, so a request's own outcome is not hidden by it.
async function background(slot: string, cluster: string): Promise<void> {
  try {
    useChat.getState().apply(slot, await reader(slot, cluster)());
    logCalls(cluster, useChat.getState().states[slot]);
  } catch {
    // the next request reports a lasting failure
  }
}

function ChatPanel({ cluster, slot }: { cluster: string; slot: string }) {
  const isInv = slot !== cluster;
  const st = useChat((s) => s.states[slot]);
  const draft = useChat((s) => s.drafts[slot] ?? "");
  const followUp = useChat((s) => s.seeds[slot]);
  const asking = useChat((s) => s.asking[slot]);
  const cleared = useChat((s) => s.cleared[slot] ?? false);
  const outcome = useChat((s) => s.outcomes[slot]);
  const anchor = useChat((s) => s.anchors[slot]);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const endRef = useRef<HTMLDivElement>(null);
  const { setDraft, setSeed, setAsking, setCleared, close } = useChat.getState();

  useEffect(() => {
    let alive = true;
    void (async () => {
      const s = await refresh(slot, cluster);
      // Opening the drawer starts a conversation - unless the operator
      // cleared it, which never starts another by itself. An investigation
      // starts on the object it was opened for.
      if (alive && s && !s.conversationId && !useChat.getState().cleared[slot]) {
        const a = useChat.getState().anchors[slot];
        if (!isInv) await request(slot, cluster, () => chatStart(cluster));
        else if (a) await request(slot, cluster, () => investigationStart(cluster, a.seed));
      }
    })();
    inputRef.current?.focus();
    return () => {
      alive = false;
    };
  }, [cluster, slot, isInv]);

  const conversationId = st?.conversationId;
  useEffect(() => {
    if (!conversationId) return;
    return onChatRead(conversationId, () => void background(slot, cluster));
  }, [conversationId, cluster, slot]);

  // a plan's step settles and its incident is read in the background (AI
  // A3 §5): the conversation is read back, ordered by version as ever
  useEffect(() => {
    if (!conversationId || !isInv) return;
    return onChatPlan(conversationId, () => void background(slot, cluster));
  }, [conversationId, cluster, slot, isInv]);

  useEffect(() => {
    endRef.current?.scrollIntoView?.({ block: "end" });
  }, [st?.exchanges.length, asking]);

  // the conversation's citations resolve against its whole ledger (AI A1 §4)
  const exchanges = st?.exchanges;
  const cite = useMemo(() => citeFrom(ledgerOf(exchanges ?? [])), [exchanges]);

  const bytes = utf8Bytes(draft);
  const tooLong = bytes > QUESTION_MAX_BYTES;

  // an investigation's first turn may go without a typed question
  const firstInv = isInv && !!st && st.exchanges.length === 0;

  const ask = async () => {
    const typed = draft.trim();
    const q = typed || (firstInv ? INVESTIGATE_Q : "");
    if (!q || !conversationId || asking || tooLong) return;
    const seed = followUp?.seed;
    setDraft(slot, "");
    setSeed(slot, undefined);
    setAsking(slot, { conversationId, question: q });
    const pending = request(slot, cluster, () => chatAsk(cluster, conversationId, q, seed));
    void background(slot, cluster); // shows the question as soon as the backend has it
    const next = await pending;
    if (useChat.getState().asking[slot]?.question === q) setAsking(slot, undefined);
    const refused = next === null || ((!!next.notice || !!next.error) && !next.exchanges.some((e) => e.question === q));
    if (refused && !useChat.getState().drafts[slot]) {
      // A refusal (too long, one already running, the conversation gone):
      // the question comes back to the editor, and its follow-up target too.
      setDraft(slot, typed);
      if (seed && followUp) setSeed(slot, followUp);
    }
  };

  const clear = async () => {
    if (!conversationId) return;
    await chatClear(cluster, conversationId);
    setCleared(slot, true);
    await refresh(slot, cluster);
  };

  const start = async () => {
    if (conversationId) await chatClear(cluster, conversationId);
    setCleared(slot, false);
    if (!isInv) await request(slot, cluster, () => chatStart(cluster));
    else if (anchor) await request(slot, cluster, () => investigationStart(cluster, anchor.seed));
  };

  // the drawer was opened on another object than the open investigation's
  const otherAnchor = isInv && !!anchor && !!st?.anchor && seedUID(st.anchor) !== seedUID(anchor.seed);
  const title = isInv ? `Investigate ${anchor?.label ?? seedName(st?.anchor)}` : `Ask about ${cluster}`;

  const inFlight = st?.exchanges.find((e) => e.outcome === "");
  const busy = !!asking || !!st?.active;
  const available = !!st && !st.ended && !!conversationId;
  const failure = outcome?.kind === "error" ? outcome.text : undefined;
  const notice = outcome?.kind === "notice" ? outcome.text : undefined;

  return (
    <aside className="chat-drawer" role="complementary" aria-label={title} data-testid="chat-drawer"
      onKeyDown={(e) => { if (e.key === "Escape") { e.stopPropagation(); close(); } }}>
      <div className="chat-head">
        <IconMessageChatbot size={15} stroke={1.5} />
        <div style={{ minWidth: 0, flex: 1 }}>
          <div className="chat-title">{title}</div>
          {st?.provider && (
            <div className="t-legend chat-sub">
              {isInv ? `${cluster} · investigation · ` : ""}{providerLabel(st.provider)}{st.model ? ` · ${st.model}` : ""} · {st.mode === "snapshot" ? "snapshot" : "tools"} · read-only
            </div>
          )}
        </div>
        <button type="button" className="l-btn" aria-label="close chat" onClick={close}><IconX size={13} stroke={1.5} /></button>
      </div>

      {st?.provider && (
        <div className="chat-small chat-guarantee" data-testid="chat-guarantee">
          {isInv ? "An investigation" : "Chat"} sends what it reads to {providerLabel(st.provider)}{st.origin ? ` (${st.origin})` : ""}. Logs and text are scrubbed, not guaranteed secret-free. Secret values, Helm values and ConfigMap values are never sent.
          {st.protected && <> This is a protected cluster; Settings &gt; AI allows its data to go to this provider.</>}
        </div>
      )}

      <div className="chat-body" data-testid="chat-body">
        {!st && <div className="chat-small">Loading…</div>}
        {failure && !conversationId && (
          <div className="chat-notice" role="alert" data-testid="chat-failure">
            ▲ {failure}
            {/Settings > AI|AI is off|unavailable|protected/.test(failure) && (
              <div><button type="button" className="l-btn" onClick={() => useFleet.getState().openSettings("ai")}>Open Settings &gt; AI</button></div>
            )}
          </div>
        )}
        {st && !st.conversationId && !failure && cleared && (
          <div className="chat-notice" role="status">
            {isInv ? "The investigation was cleared." : "The conversation was cleared."}
            <div><button type="button" className="l-btn" onClick={() => void start()}>{isInv ? `Investigate ${anchor?.label ?? "again"}` : "Start a conversation"}</button></div>
          </div>
        )}
        {st && st.removed > 0 && <div className="chat-small">{st.removed} earlier exchange{st.removed === 1 ? " was" : "s were"} removed.</div>}
        {st?.trimmed && <div className="chat-small">Earlier messages are no longer sent to the model.</div>}
        {otherAnchor && conversationId && (
          <div className="chat-notice" role="status" data-testid="inv-other">
            An investigation of {seedName(st?.anchor)} is open on {cluster}. Clear it to investigate {anchor?.label}.
            <div><button type="button" className="l-btn" onClick={() => void start()}>Clear and investigate {anchor?.label}</button></div>
          </div>
        )}
        {st?.mode === "snapshot" && st.exchanges.length === 0 && !asking && (
          <div className="chat-small">{isInv
            ? "This provider answers from a fixed set of evidence Klyx reads for the object; you cannot ask it to read more."
            : "This provider answers from a snapshot Klyx assembles per question. Name objects as kind/name or namespace/name to bring them in."}</div>
        )}
        {firstInv && st?.mode !== "snapshot" && !asking && !otherAnchor && (
          <div className="chat-small" data-testid="inv-intro">
            Investigate reads {anchor?.label ?? "the object"} first, then lets the model read more of {cluster} through Klyx's read-only tools, and ends with findings that Klyx checks and labels.
          </div>
        )}
        {isInv && st && conversationId && (
          <Comparison cluster={cluster} slot={slot} st={st} editable={firstInv && !busy && !otherAnchor} />
        )}
        {st?.exchanges.map((ex) => (
          <Exchange key={ex.id} cluster={cluster} ex={ex} cite={cite}
            plan={isInv && conversationId ? { conversationId, protected: st.protected, onChanged: () => void background(slot, cluster) } : undefined} />
        ))}
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
            <button type="button" className="l-btn" aria-label="remove follow-up target" onClick={() => setSeed(slot, undefined)}><IconX size={11} stroke={1.5} /></button>
          </div>
        )}
        <textarea
          ref={inputRef}
          aria-label="question"
          value={draft}
          disabled={!available}
          placeholder={!available ? "" : firstInv ? "Optional: add what you know, then Investigate (Enter)" : isInv ? "Ask about this investigation… (Enter to send)" : "Ask about this cluster… (Enter to send, Shift+Enter for a new line)"}
          onChange={(e) => setDraft(slot, e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void ask();
            }
          }}
        />
        {failure && conversationId && <div className="chat-small" role="alert" style={{ color: "var(--color-text-danger)" }} data-testid="chat-failure">▲ {failure}</div>}
        {(tooLong || notice) && (
          <div className="chat-small" role="status" style={{ color: "var(--color-text-warning)" }} data-testid="chat-input-notice">
            {tooLong ? `The question is ${bytes} bytes; the limit is ${QUESTION_MAX_BYTES} (UTF-8). Shorten it.` : notice}
          </div>
        )}
        <div className="chat-actions">
          {busy && conversationId
            ? <button type="button" className="l-btn" onClick={() => void chatStop(cluster, conversationId)}>Stop</button>
            : <button type="button" className="l-btn" disabled={!available || (draft.trim() === "" && !firstInv) || tooLong || otherAnchor} onClick={() => void ask()}>{firstInv ? "Investigate" : "Ask"}</button>}
          <button type="button" className="l-btn" disabled={!conversationId || busy} onClick={() => void clear()}>Clear</button>
        </div>
      </div>
    </aside>
  );
}

// Comparison is an investigation's comparison set (A-C2): chosen from the
// fleet's other clusters before the first question, fixed after.
function Comparison({ cluster, slot, st, editable }: { cluster: string; slot: string; st: ChatState; editable: boolean }) {
  const fleet = useFleet((s) => s.clusters);
  const set = st.comparison ?? [];
  const [picked, setPicked] = useState<string[]>(set);
  const others = fleet.map((c) => c.name).filter((n) => n !== cluster);
  if (!editable) {
    return (
      <div className="chat-small" data-testid="inv-comparison">
        {set.length > 0 ? `Compared with ${set.join(", ")}: each checked before its read and before every send that carries it.` : "No comparison clusters."}
      </div>
    );
  }
  if (others.length === 0) return null;
  const toggle = (n: string) => setPicked((p) => (p.includes(n) ? p.filter((x) => x !== n) : [...p, n]));
  const changed = picked.length !== set.length || picked.some((n) => !set.includes(n));
  return (
    <fieldset className="inv-compare" data-testid="inv-comparison">
      <legend className="t-legend">Compare with (fixed once the investigation begins)</legend>
      {others.map((n) => (
        <label key={n} className="chat-small"><input type="checkbox" checked={picked.includes(n)} onChange={() => toggle(n)} /> {n}</label>
      ))}
      <button type="button" className="l-btn" disabled={!changed || !st.conversationId}
        onClick={() => void request(slot, cluster, () => chatSetComparison(cluster, st.conversationId!, picked))}>Set comparison</button>
    </fieldset>
  );
}

// AI A1 §4: the conversation's ledger by citation ID, and a citation's chip
// - only for an ID the ledger holds; anything else stays the answer's text.
export function ledgerOf(exchanges: ChatExchange[]): Map<string, { read: ChatRead; check?: ChatCheck }> {
  const m = new Map<string, { read: ChatRead; check?: ChatCheck }>();
  for (const ex of exchanges) {
    for (const read of ex.reads) {
      if (!read.id) continue;
      m.set(read.id, { read });
      for (const check of read.checks ?? []) m.set(check.id, { read, check });
    }
  }
  return m;
}

function openRead(id: string) {
  const el = document.getElementById(`chat-read-${id.split(".")[0]}`);
  if (!el) return;
  const details = el.closest("details");
  if (details) details.open = true;
  el.scrollIntoView?.({ block: "nearest" });
  el.classList.add("chat-read-hit");
  window.setTimeout(() => el.classList.remove("chat-read-hit"), 1600);
}

export function citeFrom(ledger: Map<string, { read: ChatRead; check?: ChatCheck }>): Cite {
  return (id, key) => {
    const e = ledger.get(id);
    if (!e) return null;
    const title = e.check ? e.check.statement : `${e.read.tool}${e.read.target ? ` ${e.read.target}` : ""}`;
    return <button key={key} type="button" className="chat-cite" data-testid="chat-cite" title={title} onClick={() => openRead(id)}>{id}</button>;
  };
}

// PlanContext is what an investigation exchange's action card needs.
type PlanContext = { conversationId: string; protected: boolean; onChanged: () => void };

function Exchange({ cluster, ex, cite, plan }: { cluster: string; ex: ChatExchange; cite?: Cite; plan?: PlanContext }) {
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
          <SafeMarkdown text={ex.answer} cite={cite} />
          <RefChips cluster={cluster} answer={ex.answer} reads={ex.reads} />
          <div className="chat-small" style={{ color: "var(--color-text-tertiary)" }}>
            {last ? `${providerLabel(last.provider)}${last.model ? ` · ${last.model}` : ""} · ` : ""}AI-generated from Klyx's reads - verify before acting.
          </div>
        </>
      )}
      {ex.findings && <FindingsPanel findings={ex.findings} reads={ex.reads} cite={cite} />}
      {ex.plan && plan && (
        <ActionCard cluster={cluster} conversationId={plan.conversationId} exchangeId={ex.id} plan={ex.plan} protected={plan.protected} cite={cite}
          openObject={(st: ActionStep) => st.gvr && openRef(cluster, { gvr: st.gvr, namespace: st.target.namespace, name: st.target.name, uid: st.target.uid })}
          onChanged={plan.onChanged} />
      )}
      {ex.outcome === "failed" && <div className="chat-err">▲ {ex.error}</div>}
      {ex.note && <div className="chat-small">{ex.note}</div>}
      {ex.outcome === "cancelled" && <div className="chat-small">Stopped.</div>}
      {ex.outcome === "" && <div className="chat-small" aria-busy="true">Answering…</div>}
    </div>
  );
}

// Reads is the answer's read ledger (The Line slice 8a, parent §5.3): a Rail
// in read order, each step's segment its outcome - completed (and
// truncated, tagged) in route, failed in danger, cancelled dashed. A read's
// redaction count stays on its line.
const readSegment: Record<string, RailSegment> = { completed: "completed", truncated: "completed", failed: "failed", cancelled: "cancelled" };

function Reads({ reads, open }: { reads: ChatRead[]; open?: boolean }) {
  if (reads.length === 0) return null;
  const redacted = reads.reduce((n, r) => n + (r.redacted ?? 0), 0);
  const steps: RailStep[] = reads.map((r, i) => {
    const seg = readSegment[r.outcome] ?? "chain";
    const tags = r.outcome === "truncated" ? ["truncated"] : seg === "chain" && r.outcome ? [r.outcome] : undefined;
    return {
      key: String(i),
      segment: seg,
      name: `${r.tool}${r.target ? ` ${r.target}` : ""}`,
      tags,
      label: (
        <span className="chat-small" id={r.id ? `chat-read-${r.id}` : undefined}>
          {r.id && <span className="chat-read-id">{r.id}</span>}<code>{r.tool}</code>{r.target ? ` ${r.target}` : ""}{r.detail ? ` (${r.detail})` : ""}{r.redacted ? ` · ${r.redacted} redacted` : ""}
        </span>
      ),
      // the observations Klyx evaluated on the read, in its own words
      body: r.checks && r.checks.length > 0 ? (
        <ul className="chat-checks" data-testid="chat-checks">
          {r.checks.map((c) => <li key={c.id}><span className="chat-read-id">{c.id}</span>{c.statement}</li>)}
        </ul>
      ) : undefined,
    };
  });
  return (
    <details className="chat-reads" open={open} data-testid="chat-reads">
      <summary className="chat-small">{reads.length} read{reads.length === 1 ? "" : "s"}{redacted ? ` · ${redacted} redacted` : ""}</summary>
      <Rail label="Reads, in order" steps={steps} leadIn />
    </details>
  );
}

// RefChips are the answer's links into Klyx: built only from validated
// references in this exchange's reads (same cluster, GVR, namespace, name,
// UID) whose name the answer mentions. Text that merely looks like a
// reference stays text (spec §8).
// A chip names its kind, so two objects that share a namespace and name
// (Deployment shop/api, HTTPRoute shop/api) never look alike, and the row
// says how many it holds back instead of dropping them silently.
const CHIPS_SHOWN = 12;

export function RefChips({ cluster, answer, reads }: { cluster: string; answer: string; reads: ChatRead[] }) {
  const [all, setAll] = useState(false);
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
  const shown = all ? refs : refs.slice(0, CHIPS_SHOWN);
  const more = refs.length - shown.length;
  return (
    <div className="chat-chips" data-testid="chat-chips">
      {shown.map((ref) => {
        const label = `${kindOf(ref.gvr)} ${ref.namespace ? `${ref.namespace}/` : ""}${ref.name}`;
        return (
          <button key={`${ref.gvr}|${ref.namespace}|${ref.name}`} type="button" className="l-chip" title={`Open ${label} (${ref.gvr}) in Klyx`}
            onClick={() => openRef(cluster, ref)}>
            {label}
          </button>
        );
      })}
      {more > 0 && (
        <button type="button" className="l-btn" data-testid="chat-chips-more" onClick={() => setAll(true)}>+{more} more</button>
      )}
    </div>
  );
}

// splitGVR reads a ledger GVR: "group/version/plural", or "version/plural"
// for the core group.
function splitGVR(gvr: string): [string, string, string] {
  const parts = gvr.split("/");
  return parts.length === 3 ? [parts[0], parts[1], parts[2]] : ["", parts[0], parts[1]];
}

// kindOf names a chip's kind from its GVR: the well-known kind, else the
// plural as served (a CRD such as nodeclaims).
function kindOf(gvr: string): string {
  const [group, , plural] = splitGVR(gvr);
  return Object.values(WELL_KNOWN_RESOURCE_REFS).find((r) => r.group === group && r.plural === plural)?.kind ?? plural;
}

function mentions(text: string, name: string): boolean {
  if (name.length < 2) return false;
  const esc = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(^|[^A-Za-z0-9-])${esc}($|[^A-Za-z0-9-])`).test(text);
}

function openRef(cluster: string, ref: ChatRef): void {
  const [group, version, plural] = splitGVR(ref.gvr);
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
