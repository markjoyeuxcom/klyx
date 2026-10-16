import { useMemo, useState } from "react";
import type { PromotionSnapshotDTO, PromotionUnitDTO } from "../store/fleet";
import { getPromotion } from "../bridge/promotion";
import { ellipsis } from "../chrome/primitives";
import { IconGitMerge } from "@tabler/icons-react";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { LensLayout, type DatasetState } from "../layout/LensLayout";
import { UpdatedAt } from "../chrome/Age";
import { useFleetBatch } from "./useFleetBatch";
import { MembershipNote } from "./MembershipNote";

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

const failedSnap = (e: unknown): PromotionSnapshotDTO => ({ fluxPresent: false, argoPresent: false, units: [], sources: [], error: e instanceof Error ? e.message : "promotion read failed" });

export function FleetPromotion() {
  // One read = one pinned membership (useFleetBatch): joined/left clusters
  // are named, Refresh reads the current fleet, late replies are dropped.
  const batch = useFleetBatch(getPromotion, failedSnap);
  const ordered = batch.members;
  const snaps: Record<string, PromotionSnapshotDTO | null> = batch.results;
  const [showAll, setShowAll] = useState(false);

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
  const notReady = rows.filter((r) => r.unhealthy).length;
  // Units whose readiness the cluster did not establish are unverified, never
  // folded into a quiet reading.
  const unknownReady = rows.filter((r) => !r.unhealthy && Object.values(r.cells).some((u) => u && !u.suspended && u.ready === "unknown")).length;
  const visible = showAll ? rows : attention;
  const errors = ordered.filter((c) => snaps[c.name]?.error);
  const readable = loaded.length - errors.length;

  // Step 7d: a Fleet view on the workspace template. Nothing here claims the
  // fleet is in sync - only what the clusters that answered show.
  // Source heads are evidence on their own, so a fleet without units but with
  // sources still renders the body.
  const anySources = ordered.some((c) => (snaps[c.name]?.sources ?? []).length > 0);
  let state: DatasetState = "ready";
  if (ordered.length === 0) state = "empty";
  else if (loaded.length === 0) state = "loading";
  else if (readable === 0 && pendingCount === 0) state = "unreadable";
  else if (rows.length === 0 && !anySources && pendingCount === 0) state = "empty";
  const scope = `${readable} of ${ordered.length} cluster${ordered.length === 1 ? "" : "s"} read`;
  const icon = <IconGitMerge size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading applied revisions" />
    : state === "unreadable" ? <EmptyState icon={icon} title="No cluster's GitOps state could be read." hint="Each cluster's error is listed above." action={{ label: "Retry", onClick: batch.refresh }} />
    : ordered.length === 0 ? <EmptyState icon={icon} title="No clusters to read." hint="The fleet had no clusters when this read started." action={{ label: "Refresh", onClick: batch.refresh }} />
    : <EmptyState icon={icon} title="No GitOps units found." hint={`No Flux Kustomization, HelmRelease or Argo Application on the ${scope}.`} />;
  // Neutral, scoped: never "the fleet is in sync".
  const quiet = pendingCount === 0 && visible.length === 0 ? (
    <div className="t-body" style={{ color: "var(--color-text-secondary)", display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
      {rows.length === 0
        ? `No GitOps units found on the ${scope}.`
        : `No unit differs or is not ready among ${rows.length} unit${rows.length === 1 ? "" : "s"} on the ${scope}${unknownReady > 0 ? `; ${unknownReady} with readiness unknown` : ""}. Clusters that did not answer are not covered.`}
      {rows.length > 0 && !showAll && <button className="l-btn" onClick={() => setShowAll(true)}>Show all units</button>}
    </div>
  ) : null;

  return (
    <LensLayout
      label="Promotion tracker"
      title="Promotion tracker"
      summary={state === "loading" || state === "unreadable" ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{rows.length}</span>
          <span className="t-secondary">units · {scope}</span>
          {notReady > 0 && <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {notReady} not ready</span>}
          {attention.length - notReady > 0 && <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{attention.length - notReady} differ</span>}
          {unknownReady > 0 && <span className="t-secondary">{unknownReady} readiness unknown</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary">{pendingCount > 0 ? `${pendingCount} cluster${pendingCount === 1 ? "" : "s"} loading… · ` : ""}read-only</span>
          <UpdatedAt at={batch.readAt} />
          <button className="l-btn" onClick={batch.refresh}>Refresh</button>
        </>
      }
      controls={rows.length > 0 ? (
        <div role="group" aria-label="Promotion filter" className="l-seg">
          <button className="l-chip" aria-pressed={!showAll} onClick={() => setShowAll(false)}>Needs attention {attention.length > 0 && <span style={{ color: "var(--color-text-warning)" }}>{attention.length}</span>}</button>
          <button className="l-chip" aria-pressed={showAll} onClick={() => setShowAll(true)}>All units {rows.length}</button>
        </div>
      ) : undefined}
      notice={errors.length > 0 || batch.joined.length > 0 || batch.left.length > 0 ? (
        <div className="t-body" role="status" style={{ display: "flex", flexDirection: "column", gap: 2 }}>
          {errors.map((c) => <span key={c.name} style={{ color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>▲ Could not read {c.name}: {snaps[c.name]?.error}</span>)}
          <MembershipNote joined={batch.joined} left={batch.left} />
        </div>
      ) : undefined}
      state={state}
      stateView={stateView}
      body={
        <div className="ov-page">
          {quiet}
          <div style={{ overflowX: "auto" }}>
            <div style={{ display: "grid", gridTemplateColumns: `minmax(220px, 1.2fr) repeat(${ordered.length}, minmax(170px, 1fr))`, gap: "4px 10px", fontFamily: "var(--font-mono)", fontSize: 12, alignItems: "baseline" }}>
              <span className="t-section-label">Unit</span>
              {ordered.map((c) => (
                <span key={c.name} className="t-section-label" style={{ display: "flex", gap: 5, alignItems: "baseline", minWidth: 0 }}>
                  <span style={{ ...ellipsis }} title={c.name}>{c.name}</span>
                  {c.env && <span className="t-secondary" style={{ flexShrink: 0 }}>{c.env}</span>}
                </span>
              ))}
              {visible.map((row) => {
                const refCluster = ordered.find((c) => row.cells[c.name]);
                const refRev = refCluster ? row.cells[refCluster.name]!.revision : "";
                return (
                  <div key={row.key} style={{ display: "contents" }}>
                    <span data-testid={`prom-row-${row.key}`} style={{ display: "flex", gap: 6, alignItems: "baseline", minWidth: 0 }}>
                      <span className="t-secondary" style={{ flexShrink: 0 }}>
                        {row.kind === "Kustomization" ? "ks" : row.kind === "HelmRelease" ? "hr" : "app"}
                      </span>
                      <span style={{ ...ellipsis, color: "var(--color-text-primary)" }} title={row.key}>{row.namespace}/{row.name}</span>
                      {row.drift && <span className="t-secondary" style={{ color: "var(--color-text-warning)", flexShrink: 0 }}>≠ differs</span>}
                    </span>
                    {ordered.map((c) => {
                      const u = row.cells[c.name];
                      if (snaps[c.name] == null) return <span key={c.name} style={{ color: "var(--color-text-tertiary)" }}>…</span>;
                      if (snaps[c.name]?.error) return <span key={c.name} style={{ color: "var(--color-text-tertiary)" }}>unread</span>;
                      if (!u) return <span key={c.name} style={{ color: "var(--color-text-tertiary)" }}>absent</span>;
                      const differs = u.revision !== refRev;
                      const color = u.suspended ? "var(--color-text-tertiary)"
                        : u.ready === "not-ready" ? "var(--color-text-danger)"
                        : differs ? "var(--color-text-warning)"
                        : "var(--color-text-secondary)";
                      return (
                        <span key={c.name} style={{ display: "flex", gap: 5, alignItems: "baseline", minWidth: 0 }} title={`${u.revision || "no revision"}${u.message ? `\n${u.message}` : ""}`}>
                          <span style={{ ...ellipsis, color, opacity: u.stale ? 0.6 : 1 }}>{differs ? "≠ " : ""}{shortRev(u.revision)}</span>
                          {u.suspended && <span className="t-secondary" style={{ flexShrink: 0 }}>suspended</span>}
                          {!u.suspended && u.ready === "not-ready" && <span className="t-secondary" style={{ color: "var(--color-text-danger)", flexShrink: 0 }}>▲ not ready</span>}
                          {!u.suspended && u.ready === "unknown" && <span className="t-secondary" style={{ flexShrink: 0 }}>readiness unknown</span>}
                          {u.stale && <span className="t-secondary" style={{ flexShrink: 0 }}>syncing…</span>}
                        </span>
                      );
                    })}
                  </div>
                );
              })}
            </div>
          </div>
          <SourcesStrip ordered={ordered.map((c) => c.name)} snaps={snaps} />
        </div>
      }
      footerEnd={<span className="t-secondary">Applied revision per unit · ≠ marks a cell that differs from the leftmost present column (which side is newer cannot be derived from opaque revisions)</span>}
    />
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
      <h3 className="t-section-label">Source heads (fetched per cluster)</h3>
      <div style={{ display: "grid", gridTemplateColumns: `minmax(220px, 1.2fr) repeat(${ordered.length}, minmax(170px, 1fr))`, gap: "3px 10px", fontFamily: "var(--font-mono)", fontSize: 11, alignItems: "baseline" }}>
        {[...keys.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([k, meta]) => {
          // Only clusters whose read succeeded are evidence; an unread one is
          // neither "absent" nor part of the comparison.
          const revs = ordered.map((cl) => (snaps[cl] && !snaps[cl]!.error ? (snaps[cl]!.sources ?? []).find((s) => `${s.kind}/${s.namespace}/${s.name}` === k) : undefined));
          const present = revs.filter((s): s is NonNullable<typeof s> => !!s);
          const differ = new Set(present.map((s) => s.revision)).size > 1;
          return (
            <div key={k} style={{ display: "contents" }}>
              <span style={{ display: "flex", gap: 6, alignItems: "baseline", minWidth: 0 }}>
                <span className="t-secondary" style={{ flexShrink: 0 }}>{meta.kind === "GitRepository" ? "git" : "oci"}</span>
                <span style={{ ...ellipsis }} title={k}>{meta.namespace}/{meta.name}</span>
                {differ && <span className="t-secondary" style={{ color: "var(--color-text-warning)", flexShrink: 0 }}>≠ heads differ</span>}
              </span>
              {ordered.map((cl, i) => {
                const s = revs[i];
                if (snaps[cl] == null) return <span key={cl} style={{ color: "var(--color-text-tertiary)" }}>…</span>;
                if (snaps[cl]?.error) return <span key={cl} style={{ color: "var(--color-text-tertiary)" }}>unread</span>;
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
