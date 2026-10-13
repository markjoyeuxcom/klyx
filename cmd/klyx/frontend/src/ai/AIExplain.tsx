import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
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
type PendingExplain = { key: string | null; at: number; request: (key: string) => void; take: (key: string) => boolean };
export const usePendingExplain = create<PendingExplain>((set, get) => ({
  key: null,
  at: 0,
  request: (key) => set({ key, at: Date.now() }),
  take: (key) => {
    const { key: pending, at } = get();
    if (pending !== key) return false;
    set({ key: null, at: 0 });
    return Date.now() - at <= pendingExplainTTL;
  },
}));

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

  // A palette "Explain" for this target runs once when it is ready.
  useEffect(() => {
    if (key && readiness.ready && usePendingExplain.getState().take(key)) run();
  }, [key, readiness.ready, run]);

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

/** AIExplainBlock renders the explanation, with what it rests on. */
export function AIExplainBlock({ state, onDismiss }: { state: AIExplainState; onDismiss: () => void }) {
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
          </>
        )}
      </div>
    </InspectorBlock>
  );
}

/**
 * useInspectorAI is the one-line integration for a lens inspector: it returns
 * the More-menu entry and the block to render. The cluster is the inspector's
 * own when it has one, else the open route's. Pass enabled=false for a removed
 * or unverified selection.
 */
export function useInspectorAI(target: ExplainTarget | null, describe: string, enabled: boolean, clusterProp?: string): {
  menu: MenuItem[];
  block: ReactNode;
  readiness: AIReadiness;
  state: AIExplainState;
  run: () => void;
} {
  const routeCluster = useFleet((s) => (s.route.name === "cluster" ? s.route.cluster : ""));
  const cluster = clusterProp ?? routeCluster;
  const readiness = useAIReadiness(cluster);
  const active = enabled && !!cluster && !!target;
  const { state, run, dismiss } = useAIExplain(cluster, active ? target : null, readiness, describe);
  return {
    menu: active ? aiExplainMenuItem(readiness, state, run) : [],
    block: <AIExplainBlock state={state} onDismiss={dismiss} />,
    readiness,
    state,
    run,
  };
}
