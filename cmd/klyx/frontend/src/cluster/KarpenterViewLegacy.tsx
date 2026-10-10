import { useEffect, useState } from "react";
import type { KarpenterSnapshotDTO, KarpenterPoolDTO, KarpenterClaimDTO } from "../store/fleet";
import { getKarpenterState } from "../bridge/karpenter";
import { btn, frame, well, ellipsis, mono, MetersStrip } from "../chrome/primitives";
import { fmtCpu, fmtMem } from "./saturation";

// KarpenterView is the autoscaling diagnosis lens - the M10/M11/M12 pattern
// applied to Karpenter, speaking its vocabulary (design principle 8):
// controller health as the headline, NodePools broken-first with limits vs
// provisioned capacity, NodeClaims stuck-first with the FAILING launch-chain
// link named (Launched -> Registered -> Initialized), drift/consolidatable as
// informational states, and the disruption feed answering "why did my node
// just get replaced". Read-only.

const CLAIMS_SHOWN = 40;
const EVENTS_SHOWN = 12;

function ago(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`;
  return `${Math.floor(sec / 86400)}d`;
}

const readyColor = (r: string) =>
  r === "ready" ? "var(--color-text-success)" : r === "not-ready" ? "var(--color-text-danger)" : "var(--color-text-tertiary)";

function PoolRow({ p }: { p: KarpenterPoolDTO }) {
  const cpuPct = p.cpuLimit != null && p.cpuLimit > 0 && p.cpuUsed != null ? p.cpuUsed / p.cpuLimit : null;
  const memPct = p.memLimit != null && p.memLimit > 0 && p.memUsed != null ? p.memUsed / p.memLimit : null;
  const capBar = (pct: number | null, label: string) => {
    if (pct === null) return null;
    const clamped = Math.max(0, Math.min(1, pct));
    return (
      <span style={{ display: "inline-flex", flexDirection: "column", gap: 2, minWidth: 90 }}>
        <span style={{ fontSize: 10, color: pct >= 0.9 ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>
          {label} {Math.round(pct * 100)}% of limit
        </span>
        <span style={{ height: 4, background: "var(--color-background-secondary)", borderRadius: 2, overflow: "hidden" }}>
          <span style={{ display: "block", height: "100%", width: `${clamped * 100}%`, background: pct >= 0.9 ? "var(--color-text-warning)" : "var(--color-text-success)" }} />
        </span>
      </span>
    );
  };
  return (
    <div data-testid={`karp-pool-${p.name}`} style={{ display: "flex", alignItems: "baseline", gap: 12, padding: "6px 0", borderBottom: "0.5px solid var(--color-border-tertiary)", fontFamily: "var(--font-mono)", fontSize: 11, flexWrap: "wrap" }}>
      <span style={{ fontWeight: 600, color: "var(--color-text-primary)", minWidth: 120, ...ellipsis }}>{p.name}</span>
      <span style={{ color: readyColor(p.ready), flexShrink: 0 }}>{p.ready}{p.ready === "not-ready" && p.reason ? ` · ${p.reason}` : ""}</span>
      <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{p.nodes} node{p.nodes === 1 ? "" : "s"}</span>
      {p.cpuUsed != null && (
        <span style={{ color: "var(--color-text-secondary)", flexShrink: 0 }}>
          {fmtCpu(p.cpuUsed)} cpu{p.cpuLimit != null ? ` / ${fmtCpu(p.cpuLimit)} limit` : " (no limit)"}
        </span>
      )}
      {p.memUsed != null && (
        <span style={{ color: "var(--color-text-secondary)", flexShrink: 0 }}>
          {fmtMem(p.memUsed)}{p.memLimit != null ? ` / ${fmtMem(p.memLimit)} limit` : ""}
        </span>
      )}
      {capBar(cpuPct, "cpu")}
      {capBar(memPct, "mem")}
      {p.ready === "not-ready" && p.message && (
        <span style={{ flexBasis: "100%", fontSize: 10, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>{p.message}</span>
      )}
    </div>
  );
}

function ClaimRow({ c }: { c: KarpenterClaimDTO }) {
  return (
    <div data-testid={`karp-claim-${c.name}`} style={{ display: "flex", alignItems: "baseline", gap: 8, padding: "4px 0", fontFamily: "var(--font-mono)", fontSize: 11, flexWrap: "wrap" }}>
      <span style={{ color: readyColor(c.ready), minWidth: 64, flexShrink: 0 }}>{c.ready}</span>
      <span style={{ color: "var(--color-text-primary)", minWidth: 150, ...ellipsis }} title={c.nodeName ? `node ${c.nodeName}` : "no node yet"}>{c.name}</span>
      <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{c.pool || "—"}</span>
      {c.instanceType && <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{c.instanceType}</span>}
      {c.capacityType && (
        <span style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: c.capacityType === "spot" ? "var(--color-text-info)" : "var(--color-text-tertiary)", flexShrink: 0 }}>
          {c.capacityType}
        </span>
      )}
      <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{ago(c.ageSeconds)}</span>
      {c.drifted && <span style={{ fontSize: 9, color: "var(--color-text-warning)", flexShrink: 0 }} title="requirements drifted - Karpenter will replace this node">drifted</span>}
      {c.consolidatable && <span style={{ fontSize: 9, color: "var(--color-text-tertiary)", flexShrink: 0 }} title="eligible for consolidation">consolidatable</span>}
      {c.failingType && (
        <span style={{ flexBasis: "100%", fontSize: 10, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>
          stuck at {c.failingType}{c.failingReason ? `: ${c.failingReason}` : ""}{c.failingMessage ? ` — ${c.failingMessage}` : ""}
        </span>
      )}
    </div>
  );
}

// KarpenterViewLegacy - the pre-redesign Karpenter lens, kept behind
// klyx.layout.v1.legacyKarpenter=1 until the migrated lens is accepted.
export function KarpenterViewLegacy({ cluster }: { cluster: string }) {
  const [snap, setSnap] = useState<KarpenterSnapshotDTO | null>(null);

  const fetchState = async () => {
    setSnap(null);
    setSnap(await getKarpenterState(cluster));
  };
  useEffect(() => {
    void fetchState();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster]);

  const stuck = snap?.claims.filter((c) => c.ready === "not-ready").length ?? 0;
  const drifted = snap?.claims.filter((c) => c.drifted).length ?? 0;
  const spot = snap?.claims.filter((c) => c.capacityType === "spot").length ?? 0;

  return (
    <div style={{ height: "100%", minHeight: 0, overflowY: "auto", boxSizing: "border-box", padding: 16 }}>
      <div style={{ maxWidth: 980, margin: "0 auto", display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ ...frame, padding: 14, display: "flex", flexDirection: "column", gap: 10 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
            <span style={{ fontSize: 11, fontWeight: 600, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-secondary)" }}>karpenter</span>
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>node autoscaling triage · read-only</span>
            <div style={{ flex: 1 }} />
            <button onClick={() => void fetchState()} style={{ ...btn, fontSize: 10 }}>refresh</button>
          </div>

          {snap === null ? (
            <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>reading nodepools and nodeclaims…</div>
          ) : snap.error ? (
            <div style={{ fontSize: 11, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>{snap.error}</div>
          ) : !snap.detected ? (
            <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>Karpenter not detected on this cluster (no karpenter.sh CRDs served)</div>
          ) : (
            <>
              {!snap.controllerHealthy && (
                <div data-testid="karp-controller-down" style={{ fontSize: 11, color: "var(--color-text-danger)" }}>
                  karpenter controller is NOT ready — no provisioning, disruption, or drift replacement happens until it recovers
                </div>
              )}
              <MetersStrip
                note="stuck first"
                meters={[
                  { label: "pools", value: snap.pools.length },
                  { label: "nodeclaims", value: snap.claims.length },
                  stuck > 0 && { label: "stuck", value: stuck, tone: "danger" as const },
                  drifted > 0 && { label: "drifted", value: drifted, tone: "warning" as const },
                  spot > 0 && { label: "spot", value: spot, tone: "info" as const },
                ]}
              />
            </>
          )}
        </div>

        {snap && !snap.error && snap.detected && (
          <>
            <div style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 2 }}>
              <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>
                nodepools · {snap.pools.length}
              </div>
              {snap.pools.length === 0 ? (
                <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>no NodePools defined</div>
              ) : snap.pools.map((p) => <PoolRow key={p.name} p={p} />)}
            </div>

            <div style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 2 }}>
              <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>
                nodeclaims · {snap.claims.length}{stuck > 0 ? ` · ${stuck} stuck` : ""}
              </div>
              {snap.claims.length === 0 ? (
                <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>no NodeClaims — Karpenter has provisioned nothing</div>
              ) : (
                <>
                  {snap.claims.slice(0, CLAIMS_SHOWN).map((c) => <ClaimRow key={c.name} c={c} />)}
                  {snap.claims.length > CLAIMS_SHOWN && (
                    <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>+{snap.claims.length - CLAIMS_SHOWN} more (healthy tail)</span>
                  )}
                </>
              )}
            </div>

            {snap.events.length > 0 && (
              <div data-testid="karp-events" style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 3 }}>
                <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>
                  disruption activity (recent events)
                </div>
                {snap.events.slice(0, EVENTS_SHOWN).map((e, i) => (
                  <div key={i} style={{ display: "flex", gap: 8, alignItems: "baseline", fontFamily: "var(--font-mono)", fontSize: 11, minWidth: 0 }}>
                    <span style={{ color: e.type === "Warning" ? "var(--color-text-warning)" : "var(--color-text-tertiary)", minWidth: 150, flexShrink: 0, ...ellipsis }}>{e.reason}</span>
                    <span style={{ ...mono, color: "var(--color-text-secondary)", minWidth: 120, flexShrink: 0, ...ellipsis }}>{e.kind}/{e.name}</span>
                    <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }} title={e.message}>{e.message}</span>
                  </div>
                ))}
              </div>
            )}

            <div style={{ fontSize: 9, color: "var(--color-text-tertiary)" }}>
              drifted and consolidatable are Karpenter's intended behavior, not faults · the launch chain (Launched → Registered → Initialized) names where a stuck claim stopped
            </div>
          </>
        )}
      </div>
    </div>
  );
}
