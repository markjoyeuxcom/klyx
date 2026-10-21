import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { openChat, openInvestigation } from "./chatStore";
import type { ChatSeed } from "../bridge/chat";
import { create } from "zustand";
import { aiAvailability, aiEgressAllowed, explain, type ExplainTarget } from "../bridge/ai";
import { useFleet, type AIResultDTO, type AIAvailabilityDTO } from "../store/fleet";
import { InspectorBlock, type MenuItem } from "../layout/InspectorLayout";

// The shared Explain affordance (spec 2026-10-13 §4.3): one hook and one
// block, used by every lens inspector. A result is keyed by cluster and
// target identity and dropped when the selection changes; the backend fences
// the object by UID before any model call.

/** targetKey is the identity an explanation belongs to. */
export function targetKey(cluster: string, t: ExplainTarget | null): string {
  if (!t) return "";
  switch (t.kind) {
    case "row":
      return `${cluster}|row|${t.task}/${t.part}|${t.uid}`;
    case "object":
      return `${cluster}|object|${t.group}/${t.version}/${t.plural}|${t.namespace ?? ""}/${t.name}|${t.uid}`;
    case "helm":
      return `${cluster}|helm|${t.namespace}/${t.name}`;
  }
}

// A palette "Explain" parks its target here; the inspector that shows that
// target runs it once when AI is ready. A request expires after
// pendingExplainTTL, so one that never found its inspector cannot fire later.
export const pendingExplainTTL = 30_000;
// seq numbers every request, so a second request for the same target runs
// again, and an inspector that is already open sees it (it subscribes).
type PendingExplain = { key: string | null; at: number; seq: number; request: (key: string) => void; take: (key: string) => boolean };
export const usePendingExplain = create<PendingExplain>((set, get) => ({
  key: null,
  at: 0,
  seq: 0,
  request: (key) => set((s) => ({ key, at: Date.now(), seq: s.seq + 1 })),
  take: (key) => {
    const { key: pending, at } = get();
    if (pending !== key) return false;
    set({ key: null, at: 0 });
    return Date.now() - at <= pendingExplainTTL;
  },
}));

// A palette "Investigate" parks its target the same way (AI A1 §5): the
// inspector showing it opens the investigation once AI is ready.
export const usePendingInvestigate = create<PendingExplain>((set, get) => ({
  key: null,
  at: 0,
  seq: 0,
  request: (key) => set((s) => ({ key, at: Date.now(), seq: s.seq + 1 })),
  take: (key) => {
    const { key: pending, at } = get();
    if (pending !== key) return false;
    set({ key: null, at: 0 });
    return Date.now() - at <= pendingExplainTTL;
  },
}));

// investigable: an investigation is pinned by UID, so a Helm release (no
// UID) is never one.
export function investigable(t: ExplainTarget | null): t is Exclude<ExplainTarget, { kind: "helm" }> {
  return !!t && t.kind !== "helm" && !!t.uid;
}

export type AIReadiness = {
  loading: boolean;
  configured: boolean; // a provider is set (it may still be unavailable)
  ready: boolean; // a call may be made for this cluster
  reason?: string; // why not, when configured but not ready
};

/** useAIReadiness reports whether Explain can run for a cluster. */
export function useAIReadiness(cluster: string): AIReadiness {
  const [state, setState] = useState<AIReadiness>({ loading: true, configured: false, ready: false });
  useEffect(() => {
    let live = true;
    (async () => {
      const av: AIAvailabilityDTO | null = await aiAvailability().catch(() => null);
      if (!live) return;
      if (!av || av.backend === "off" || av.backend === "") {
        setState({ loading: false, configured: false, ready: false });
        return;
      }
      if (!av.ready) {
        setState({ loading: false, configured: true, ready: false, reason: av.problem ? `AI is unavailable: ${av.problem}` : "AI is unavailable" });
        return;
      }
      const egress = await aiEgressAllowed(cluster).catch(() => false);
      if (!live) return;
      setState(egress
        ? { loading: false, configured: true, ready: true }
        : { loading: false, configured: true, ready: false, reason: `AI is disabled for protected cluster ${cluster}: the provider is not on this machine. Allow protected-cluster egress in Settings > AI to use it here.` });
    })();
    return () => {
      live = false;
    };
  }, [cluster]);
  return state;
}

export type AIExplainState = { status: "idle" | "loading" | "done"; result?: AIResultDTO };

/** useAIExplain runs and holds one explanation for the inspector's target. */
export function useAIExplain(cluster: string, target: ExplainTarget | null, readiness: AIReadiness, describe: string) {
  const key = targetKey(cluster, target);
  const [state, setState] = useState<AIExplainState & { key: string }>({ status: "idle", key });
  const token = useRef(0);
  const targetRef = useRef(target);
  targetRef.current = target;

  // A new selection drops the previous explanation and any reply in flight.
  if (state.key !== key) {
    token.current++;
    setState({ status: "idle", key });
  }

  const run = useCallback(() => {
    const t = targetRef.current;
    if (!t) return;
    const mine = ++token.current;
    setState({ status: "loading", key });
    void explain(cluster, t).then((r) => {
      if (mine !== token.current) return; // the selection changed meanwhile
      setState({ status: "done", result: r, key });
      const status = useFleet.getState().setActionStatus;
      if (r.ok) status({ kind: "success", message: `AI explain - ${describe} via ${r.backend}${r.model ? ` (${r.model})` : ""}${r.origin ? ` to ${r.origin}` : ""}` });
      else status({ kind: "error", message: `Explain failed: ${r.error}` });
    });
  }, [cluster, key, describe]);

  // A palette "Explain" for this target runs once when it is ready - also
  // when this inspector is already open, and again for a repeated request.
  const pendingSeq = usePendingExplain((p) => p.seq);
  useEffect(() => {
    if (key && readiness.ready && usePendingExplain.getState().take(key)) run();
  }, [key, readiness.ready, run, pendingSeq]);

  // An unmounted inspector drops its reply: no block, no Activity entry
  // (Codex review of f83c7cd).
  useEffect(() => () => {
    token.current++;
  }, []);

  const dismiss = useCallback(() => {
    token.current++;
    setState({ status: "idle", key });
  }, [key]);

  return { state, run, dismiss };
}

/** aiExplainMenuItem is the inspector's More-menu entry. */
export function aiExplainMenuItem(readiness: AIReadiness, state: AIExplainState, run: () => void, enabled = true): MenuItem[] {
  if (readiness.loading || !enabled) return [];
  if (!readiness.configured) return [{ label: "Set up AI…", onSelect: () => useFleet.getState().openSettings(), hint: "Explain needs an AI provider" }];
  if (!readiness.ready) return [{ label: "Explain with AI", disabled: true, hint: readiness.reason, onSelect: () => {} }];
  return [{ label: state.status === "loading" ? "Explaining…" : "Explain with AI", disabled: state.status === "loading", onSelect: run }];
}

/** aiInvestigateMenuItem is the inspector's "Investigate with AI" entry. */
export function aiInvestigateMenuItem(readiness: AIReadiness, open: (() => void) | undefined): MenuItem[] {
  if (readiness.loading || !readiness.ready || !open) return [];
  return [{ label: "Investigate with AI", hint: "a read-only investigation with cited findings", onSelect: open }];
}

/** AIExplainBlock renders the explanation, with what it rests on. */
export function AIExplainBlock({ state, onDismiss, onFollowUp, onInvestigate }: { state: AIExplainState; onDismiss: () => void; onFollowUp?: () => void; onInvestigate?: () => void }) {
  if (state.status === "idle") return null;
  const r = state.result;
  return (
    <InspectorBlock title="AI explanation" extra={<span role="button" tabIndex={0} className="t-legend" style={{ marginLeft: "auto", cursor: "pointer" }} onClick={(e) => { e.stopPropagation(); onDismiss(); }} onKeyDown={(e) => { if (e.key === "Enter") { e.stopPropagation(); onDismiss(); } }}>dismiss</span>}>
      <div data-testid="ai-explain" className="t-body" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        {state.status === "loading" && <div style={{ color: "var(--color-text-tertiary)" }}>Explaining…</div>}
        {r && !r.ok && <div style={{ color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>▲ {r.error}</div>}
        {r && r.ok && (
          <>
            <div style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{r.explanation}</div>
            {r.groundedOn && <div className="t-legend" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>Grounded on: {r.groundedOn}</div>}
            <div className="t-legend" style={{ color: "var(--color-text-tertiary)", overflowWrap: "anywhere" }}>
              {r.model}{r.origin ? ` via ${r.origin}` : ""}
              {r.scrubbed ? ` · ${r.scrubbed} value${r.scrubbed === 1 ? "" : "s"} scrubbed or trimmed before sending` : ""}
              {r.redacted ? ` · ${r.redacted} value${r.redacted === 1 ? "" : "s"} masked` : ""}
            </div>
            <div className="t-legend" style={{ color: "var(--color-text-tertiary)" }}>AI-generated from Klyx's resolved state - verify before acting.</div>
            {(onFollowUp || onInvestigate) && (
              <div style={{ display: "flex", gap: 6 }}>
                {onFollowUp && <button type="button" className="l-btn" onClick={onFollowUp}>Ask a follow-up</button>}
                {onInvestigate && <button type="button" className="l-btn" onClick={onInvestigate}>Investigate</button>}
              </div>
            )}
          </>
        )}
      </div>
    </InspectorBlock>
  );
}

/**
 * useInspectorAI is the one-line integration for a lens inspector: it returns
 * the More-menu entry and the block to render. The cluster is the inspector's
 * own, always: there is no route fallback (The Line slice 4 §6.3), so an
 * inspector never explains or follows up on the open cluster by mistake.
 * Pass enabled=false for a removed or unverified selection.
 */
export function useInspectorAI(target: ExplainTarget | null, describe: string, enabled: boolean, cluster: string): {
  menu: MenuItem[];
  investigate: MenuItem[]; // the Investigate entry alone, for an inspector that shows Explain elsewhere
  block: ReactNode;
  readiness: AIReadiness;
  state: AIExplainState;
  run: () => void;
} {
  const readiness = useAIReadiness(cluster);
  const active = enabled && !!cluster && !!target;
  const { state, run, dismiss } = useAIExplain(cluster, active ? target : null, readiness, describe);
  // the investigation opens on the target as it is now, read through a ref,
  // so the callback stays stable across renders
  const key = targetKey(cluster, active ? target : null);
  const targetNow = useRef(target);
  targetNow.current = target;
  const canInvestigate = active && investigable(target);
  const investigate = useMemo(
    () => (canInvestigate ? () => openInvestigation(cluster, targetNow.current as ChatSeed, describe) : undefined),
    [canInvestigate, cluster, describe],
  );
  // a palette "Investigate" for this target opens once AI is ready
  const pendingInv = usePendingInvestigate((p) => p.seq);
  useEffect(() => {
    if (key && readiness.ready && investigate && usePendingInvestigate.getState().take(key)) investigate();
  }, [key, readiness.ready, investigate, pendingInv]);
  const invItem = active ? aiInvestigateMenuItem(readiness, investigate) : [];
  return {
    menu: active ? [...aiExplainMenuItem(readiness, state, run), ...invItem] : [],
    investigate: invItem,
    // "Ask a follow-up" opens this cluster's chat seeded with Explain's own
    // target - the row, object or release it explained - so the backend
    // reads that exact object and refuses a replacement by UID.
    block: <AIExplainBlock state={state} onDismiss={dismiss} onFollowUp={cluster && active && target ? () => openChat(cluster, { seed: target as ChatSeed, label: describe }) : undefined}
      onInvestigate={readiness.ready ? investigate : undefined} />,
    readiness,
    state,
    run,
  };
}
