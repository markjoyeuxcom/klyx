import { useEffect, useMemo, useState } from "react";
import { useFleet } from "../store/fleet";
import type { ExpiryItemDTO, ExpiryScanResultDTO } from "../store/fleet";
import { getExpiryScan } from "../bridge/expiry";
import { btn, ellipsis, FilterButton } from "../chrome/primitives";
import { IconX } from "@tabler/icons-react";

// FleetExpiry is the "what dies next" horizon: every certificate with a
// lifetime, merged across the fleet, soonest first - cert-manager-managed
// certs (renewing, usually fine), raw TLS secrets (nobody renews these), and
// webhook CA bundles (an expired one breaks the API surface). Windows: 30/60/
// 90 days + everything. Read-only; public certificate material only.

const WINDOWS = [30, 60, 90, 0] as const; // 0 = all

type Row = ExpiryItemDTO & { cluster: string };

const sourceLabel: Record<string, string> = {
  "tls-secret": "tls secret",
  "webhook-ca": "webhook ca",
  "cert-manager": "cert-manager",
};

export function FleetExpiry({ onClose }: { onClose: () => void }) {
  const clusters = useFleet((s) => s.clusters);
  const [snaps, setSnaps] = useState<Record<string, ExpiryScanResultDTO | null>>({});
  const [windowDays, setWindowDays] = useState<number>(90);

  useEffect(() => {
    let live = true;
    for (const c of clusters) {
      void getExpiryScan(c.name).then((r) => {
        if (live) setSnaps((prev) => ({ ...prev, [c.name]: r }));
      });
    }
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const now = Math.floor(Date.now() / 1000);
  const rows: Row[] = useMemo(() => {
    const out: Row[] = [];
    for (const c of clusters) {
      for (const it of snaps[c.name]?.items ?? []) out.push({ ...it, cluster: c.name });
    }
    out.sort((a, b) => a.notAfterUnix - b.notAfterUnix);
    return out;
  }, [snaps, clusters]);

  const inWindow = rows.filter((r) => windowDays === 0 || r.notAfterUnix - now <= windowDays * 86400);
  const pending = clusters.filter((c) => snaps[c.name] == null).length;
  const errors = clusters.filter((c) => snaps[c.name]?.error);
  const warnings = clusters.flatMap((c) => (snaps[c.name]?.warnings ?? []).map((w) => `${c.name}: ${w}`));

  const daysLeft = (unix: number) => Math.floor((unix - now) / 86400);
  const tone = (d: number) => (d < 0 ? "var(--color-text-danger)" : d <= 14 ? "var(--color-text-danger)" : d <= 30 ? "var(--color-text-warning)" : "var(--color-text-secondary)");

  return (
    <div role="dialog" aria-label="expiry horizon" style={{ position: "fixed", inset: 0, background: "color-mix(in srgb, var(--color-background-primary) 72%, transparent)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 60 }}>
      <div style={{ width: "min(94vw, 1100px)", maxHeight: "88vh", overflow: "auto", background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-secondary)", borderRadius: "var(--border-radius-md)", padding: 16, display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
          <div style={{ fontSize: 13, fontWeight: 600 }}>Expiry horizon</div>
          <div style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>
            certs, raw TLS secrets, webhook CAs · fleet-wide · soonest first
          </div>
          {pending > 0 && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{pending} cluster{pending === 1 ? "" : "s"} scanning…</span>}
          <div style={{ flex: 1 }} />
          {WINDOWS.map((w) => (
            <FilterButton key={w} label={w === 0 ? "all" : `${w}d`} active={windowDays === w} onClick={() => setWindowDays(w)} />
          ))}
          <button onClick={onClose} aria-label="close expiry horizon" style={{ ...btn, display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
        </div>

        {errors.length > 0 && (
          <div style={{ fontSize: 11, color: "var(--color-text-danger)", display: "flex", flexDirection: "column", gap: 2 }}>
            {errors.map((c) => <span key={c.name} style={{ overflowWrap: "anywhere" }}>{c.name}: {snaps[c.name]?.error}</span>)}
          </div>
        )}
        {warnings.length > 0 && (
          <div style={{ fontSize: 10, color: "var(--color-text-warning)", display: "flex", flexDirection: "column", gap: 2 }}>
            {warnings.map((w) => <span key={w} style={{ overflowWrap: "anywhere" }}>partial scan — {w}</span>)}
          </div>
        )}

        {inWindow.length === 0 && pending === 0 ? (
          <div data-testid="expiry-clear" style={{ fontSize: 11, color: "var(--color-text-success)" }}>
            nothing expires within {windowDays === 0 ? "any horizon" : `${windowDays} days`} across {clusters.length - errors.length} scanned cluster{clusters.length - errors.length === 1 ? "" : "s"}
          </div>
        ) : (
          <div style={{ display: "grid", gridTemplateColumns: "70px 90px minmax(160px, 1fr) minmax(140px, 1fr) 110px", gap: "4px 10px", fontFamily: "var(--font-mono)", fontSize: 11, alignItems: "baseline" }}>
            <span style={{ fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>expires</span>
            <span style={{ fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>source</span>
            <span style={{ fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>object</span>
            <span style={{ fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>subject</span>
            <span style={{ fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>cluster</span>
            {inWindow.map((r) => {
              const d = daysLeft(r.notAfterUnix);
              return (
                <div key={`${r.cluster}/${r.source}/${r.namespace}/${r.name}/${r.detail}`} style={{ display: "contents" }}>
                  <span style={{ color: tone(d), fontWeight: d <= 14 ? 600 : undefined }}>
                    {d < 0 ? `${-d}d ago` : `${d}d`}
                  </span>
                  <span style={{ color: r.source === "cert-manager" ? "var(--color-text-tertiary)" : "var(--color-text-secondary)" }}>
                    {sourceLabel[r.source] ?? r.source}
                  </span>
                  <span style={{ ...ellipsis, color: "var(--color-text-primary)" }} title={`${r.namespace ? r.namespace + "/" : ""}${r.name}${r.detail ? ` (${r.detail})` : ""}`}>
                    {r.namespace ? `${r.namespace}/` : ""}{r.name}
                  </span>
                  <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={r.subject}>{r.subject || "—"}</span>
                  <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }}>{r.cluster}</span>
                </div>
              );
            })}
          </div>
        )}

        <div style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>
          cert-manager rows are renewal-managed (usually fine before expiry) · raw TLS secrets and webhook CAs have NO renewer — those are the ones that take clusters down · public certificate material only, keys never read
        </div>
      </div>
    </div>
  );
}
