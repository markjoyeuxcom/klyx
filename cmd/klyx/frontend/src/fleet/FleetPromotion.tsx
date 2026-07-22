import { useEffect, useMemo, useState } from "react";
import { useFleet } from "../store/fleet";
import type { PromotionSnapshotDTO, PromotionUnitDTO } from "../store/fleet";
import { getPromotion } from "../bridge/promotion";
import { btn, ellipsis, FilterButton } from "../chrome/primitives";
import { IconX } from "@tabler/icons-react";

// FleetPromotion is the propagation tracker: every GitOps unit (Flux
// Kustomization/HelmRelease, Argo Application) as a row, clusters as
// env-ranked columns (dev -> stg -> prd), each cell that cluster's APPLIED
// revision + health. The daily question it answers: "did my change land
// everywhere - and if not, where is it stuck?"
//
// Honesty rules: revisions are opaque strings, so drift is "cells differ
// within the row" - the REFERENCE is the leftmost (dev-most) column, marked
// as such, because which side is 'behind' cannot be derived from the strings.
// A failed cluster read renders its error in that column, never an empty cell.

const envRank: Record<string, number> = { dev: 0, development: 0, stg: 1, staging: 1, prd: 2, prod: 2, production: 2 };
const rankOfEnv = (env: string): number => envRank[env.toLowerCase()] ?? 3;

// shortRev compacts "main@sha1:abcdef012345..." to "main@abcdef0".
function shortRev(rev: string): string {
  if (rev === "") return "—";
  const m = rev.match(/^(.*)@sha1:([0-9a-f]{7,})$/);
  if (m) return `${m[1]}@${m[2].slice(0, 7)}`;
  return rev.length > 24 ? rev.slice(0, 24) + "…" : rev;
}

const unitKey = (u: PromotionUnitDTO) => `${u.kind}/${u.namespace}/${u.name}`;

type Row = {
  key: string;
  kind: string;
  namespace: string;
  name: string;
  cells: Record<string, PromotionUnitDTO | undefined>;
  drift: boolean;    // applied revisions differ across present cells
  unhealthy: boolean; // any present cell not-ready (suspended excluded - intended)
};

export function FleetPromotion({ onClose }: { onClose: () => void }) {
  const clusters = useFleet((s) => s.clusters);
  const ordered = useMemo(() => [...clusters].sort((a, b) =>
    rankOfEnv(a.env) - rankOfEnv(b.env) || a.env.localeCompare(b.env) || a.name.localeCompare(b.name)), [clusters]);
  const [snaps, setSnaps] = useState<Record<string, PromotionSnapshotDTO | null>>({});
  const [showAll, setShowAll] = useState(false);

  useEffect(() => {
    let live = true;
    setSnaps({});
    for (const c of clusters) {
      void getPromotion(c.name).then((r) => {
        if (live) setSnaps((prev) => ({ ...prev, [c.name]: r }));
      });
    }
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const loaded = ordered.filter((c) => snaps[c.name] != null);
  const pendingCount = ordered.length - loaded.length;

  const rows: Row[] = useMemo(() => {
    const byKey = new Map<string, Row>();
    for (const c of ordered) {
      const snap = snaps[c.name];
      if (!snap || snap.error) continue;
      for (const u of snap.units) {
        const k = unitKey(u);
        let row = byKey.get(k);
        if (!row) {
          row = { key: k, kind: u.kind, namespace: u.namespace, name: u.name, cells: {}, drift: false, unhealthy: false };
          byKey.set(k, row);
        }
        row.cells[c.name] = u;
      }
    }
    const out = [...byKey.values()];
    for (const row of out) {
      const present = ordered.map((c) => row.cells[c.name]).filter((u): u is PromotionUnitDTO => !!u);
      const revs = new Set(present.map((u) => u.revision));
      row.drift = revs.size > 1;
      row.unhealthy = present.some((u) => u.ready === "not-ready" && !u.suspended);
    }
    out.sort((a, b) => {
      const aw = (a.unhealthy ? 0 : a.drift ? 1 : 2);
      const bw = (b.unhealthy ? 0 : b.drift ? 1 : 2);
      if (aw !== bw) return aw - bw;
      return a.key.localeCompare(b.key);
    });
    return out;
  }, [snaps, ordered]);

  const attention = rows.filter((r) => r.drift || r.unhealthy);
  const visible = showAll ? rows : attention;
  const errors = ordered.filter((c) => snaps[c.name]?.error);

  return (
    <div role="dialog" aria-label="promotion tracker" style={{ position: "fixed", inset: 0, background: "color-mix(in srgb, var(--color-background-primary) 72%, transparent)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 60 }}>
      <div style={{ width: "min(94vw, 1280px)", maxHeight: "88vh", overflow: "auto", background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-secondary)", borderRadius: "var(--border-radius-md)", padding: 16, display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
          <div style={{ fontSize: 13, fontWeight: 600 }}>Promotion tracker</div>
          <div style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>
            applied revisions per cluster · reference = leftmost column · read-only
          </div>
          {pendingCount > 0 && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{pendingCount} cluster{pendingCount === 1 ? "" : "s"} loading…</span>}
          <div style={{ flex: 1 }} />
          <FilterButton label="needs attention" count={attention.length} active={!showAll} onClick={() => setShowAll(false)} />
          <FilterButton label="all units" count={rows.length} active={showAll} onClick={() => setShowAll(true)} />
          <button onClick={onClose} aria-label="close promotion tracker" style={{ ...btn, display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
        </div>

        {errors.length > 0 && (
          <div style={{ fontSize: 11, color: "var(--color-text-danger)", display: "flex", flexDirection: "column", gap: 2 }}>
            {errors.map((c) => <span key={c.name} style={{ overflowWrap: "anywhere" }}>{c.name}: {snaps[c.name]?.error}</span>)}
          </div>
        )}

        <div style={{ overflowX: "auto" }}>
          <div style={{ display: "grid", gridTemplateColumns: `minmax(220px, 1.2fr) repeat(${ordered.length}, minmax(170px, 1fr))`, gap: "4px 10px", fontFamily: "var(--font-mono)", fontSize: 11, alignItems: "baseline" }}>
            <span />
            {ordered.map((c) => (
              <span key={c.name} style={{ fontSize: 10, fontWeight: 600, display: "flex", gap: 5, alignItems: "baseline", minWidth: 0 }}>
                <span style={{ ...ellipsis }}>{c.name}</span>
                {c.env && <span style={{ fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)", flexShrink: 0 }}>{c.env}</span>}
              </span>
            ))}

            {visible.length === 0 && loaded.length > 0 ? (
              <span style={{ gridColumn: "1 / -1", fontSize: 11, color: showAll ? "var(--color-text-tertiary)" : "var(--color-text-success)", padding: "8px 0" }}>
                {showAll ? "no GitOps units found on the loaded clusters" : `all ${rows.length} units at matching revisions and healthy across the loaded fleet`}
              </span>
            ) : visible.map((row) => {
              const refCluster = ordered.find((c) => row.cells[c.name]);
              const refRev = refCluster ? row.cells[refCluster.name]!.revision : "";
              return (
                <div key={row.key} style={{ display: "contents" }}>
                  <span data-testid={`prom-row-${row.key}`} style={{ display: "flex", gap: 6, alignItems: "baseline", minWidth: 0 }}>
                    <span style={{ fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)", flexShrink: 0 }}>
                      {row.kind === "Kustomization" ? "ks" : row.kind === "HelmRelease" ? "hr" : "app"}
                    </span>
                    <span style={{ ...ellipsis, color: "var(--color-text-primary)" }} title={row.key}>{row.namespace}/{row.name}</span>
                    {row.drift && <span style={{ fontSize: 9, color: "var(--color-text-warning)", flexShrink: 0 }}>drift</span>}
                  </span>
                  {ordered.map((c) => {
                    const u = row.cells[c.name];
                    if (snaps[c.name] == null) return <span key={c.name} style={{ color: "var(--color-text-tertiary)" }}>…</span>;
                    if (!u) return <span key={c.name} style={{ color: "var(--color-text-tertiary)" }}>absent</span>;
                    const differs = u.revision !== refRev;
                    const color = u.suspended ? "var(--color-text-tertiary)"
                      : u.ready === "not-ready" ? "var(--color-text-danger)"
                      : differs ? "var(--color-text-warning)"
                      : "var(--color-text-secondary)";
                    return (
                      <span key={c.name} style={{ display: "flex", gap: 5, alignItems: "baseline", minWidth: 0 }} title={`${u.revision || "no revision"}${u.message ? `\n${u.message}` : ""}`}>
                        <span style={{ ...ellipsis, color, opacity: u.stale ? 0.6 : 1 }}>{shortRev(u.revision)}</span>
                        {u.suspended && <span style={{ fontSize: 9, color: "var(--color-text-tertiary)", flexShrink: 0 }}>suspended</span>}
                        {!u.suspended && u.ready === "not-ready" && <span style={{ fontSize: 9, color: "var(--color-text-danger)", flexShrink: 0 }}>not ready</span>}
                        {u.stale && <span style={{ fontSize: 9, color: "var(--color-text-tertiary)", flexShrink: 0 }}>syncing…</span>}
                      </span>
                    );
                  })}
                </div>
              );
            })}
          </div>
        </div>

        <SourcesStrip ordered={ordered.map((c) => c.name)} snaps={snaps} />

        <div style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>
          Applied revision per unit · a differing cell is marked against the leftmost present column (which side is newer cannot be derived from opaque revisions) · suspended and syncing states shown as-is
        </div>
      </div>
    </div>
  );
}

// SourcesStrip: each source's FETCHED head per cluster - a cluster whose
// source-controller is stuck shows an older head here before any unit drifts.
function SourcesStrip({ ordered, snaps }: { ordered: string[]; snaps: Record<string, PromotionSnapshotDTO | null> }) {
  const keys = new Map<string, { kind: string; namespace: string; name: string }>();
  for (const cl of ordered) {
    for (const s of snaps[cl]?.sources ?? []) {
      keys.set(`${s.kind}/${s.namespace}/${s.name}`, { kind: s.kind, namespace: s.namespace, name: s.name });
    }
  }
  if (keys.size === 0) return null;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>source heads (fetched per cluster)</div>
      <div style={{ display: "grid", gridTemplateColumns: `minmax(220px, 1.2fr) repeat(${ordered.length}, minmax(170px, 1fr))`, gap: "3px 10px", fontFamily: "var(--font-mono)", fontSize: 11, alignItems: "baseline" }}>
        {[...keys.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([k, meta]) => {
          const revs = ordered.map((cl) => (snaps[cl]?.sources ?? []).find((s) => `${s.kind}/${s.namespace}/${s.name}` === k));
          const present = revs.filter((s): s is NonNullable<typeof s> => !!s);
          const differ = new Set(present.map((s) => s.revision)).size > 1;
          return (
            <div key={k} style={{ display: "contents" }}>
              <span style={{ display: "flex", gap: 6, alignItems: "baseline", minWidth: 0 }}>
                <span style={{ fontSize: 9, textTransform: "uppercase", color: "var(--color-text-tertiary)", flexShrink: 0 }}>{meta.kind === "GitRepository" ? "git" : "oci"}</span>
                <span style={{ ...ellipsis }} title={k}>{meta.namespace}/{meta.name}</span>
                {differ && <span style={{ fontSize: 9, color: "var(--color-text-warning)", flexShrink: 0 }}>heads differ</span>}
              </span>
              {ordered.map((cl, i) => {
                const s = revs[i];
                if (snaps[cl] == null) return <span key={cl} style={{ color: "var(--color-text-tertiary)" }}>…</span>;
                if (!s) return <span key={cl} style={{ color: "var(--color-text-tertiary)" }}>absent</span>;
                return (
                  <span key={cl} title={s.revision} style={{ ...ellipsis, color: s.ready === "not-ready" ? "var(--color-text-danger)" : differ ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>
                    {shortRev(s.revision)}
                  </span>
                );
              })}
            </div>
          );
        })}
      </div>
    </div>
  );
}
