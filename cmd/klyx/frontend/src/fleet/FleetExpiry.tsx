import { useMemo, useState } from "react";
import type { ExpiryItemDTO, ExpiryScanResultDTO } from "../store/fleet";
import { useExpiryScans } from "../store/expiry";
import { ellipsis } from "../chrome/primitives";
import { IconCertificate } from "@tabler/icons-react";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { LensLayout, LensTable, type DatasetState } from "../layout/LensLayout";
import { UpdatedAt } from "../chrome/Age";
import { MembershipNote } from "./MembershipNote";

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

export function FleetExpiry() {
  // The session's shared scans (The Line slice 5 §6, with the fleet
  // screen's Horizon): each cluster once, a joining cluster once when it
  // joins, a leaving one dropped; Refresh scans the current fleet anew and
  // late replies are dropped. No membership is pinned, so none is named.
  const scans = useExpiryScans();
  const batch = { ...scans, joined: [] as string[], left: [] as string[] };
  const clusters = batch.members;
  const snaps: Record<string, ExpiryScanResultDTO | null> = batch.results;
  const [windowDays, setWindowDays] = useState<number>(90);

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
  const readable = clusters.length - pending - errors.length;
  const partial = clusters.filter((c) => (snaps[c.name]?.warnings ?? []).length > 0).length;
  const expired = inWindow.filter((r) => r.notAfterUnix <= now).length;
  const soon = inWindow.filter((r) => r.notAfterUnix > now && daysLeft(r.notAfterUnix) <= 14).length;
  const horizon = windowDays === 0 ? "at any time" : `within ${windowDays} days`;
  // A Subject column with no value in this scan is left out, and the footer
  // says no subject was reported (an empty value, e.g. a SAN-only cert - not
  // an unread one). Never a column of dashes.
  const hasSubject = inWindow.some((r) => !!r.subject);
  const grid = hasSubject ? "80px 110px minmax(180px, 1.4fr) minmax(140px, 1fr) minmax(110px, 0.6fr)" : "80px 110px minmax(180px, 1.6fr) minmax(110px, 0.6fr)";

  // Step 7d: a Fleet view on the workspace template. An empty window is
  // scoped to the clusters that were scanned - partial and unreadable scans
  // are named, never folded into "nothing expires".
  let state: DatasetState = "ready";
  if (clusters.length === 0) state = "empty";
  else if (readable === 0 && pending > 0) state = "loading";
  else if (readable === 0 && errors.length > 0) state = "unreadable";
  else if (rows.length === 0 && pending === 0) state = "empty";
  else if (inWindow.length === 0 && pending === 0) state = "no-match";
  const scope = `${readable} of ${clusters.length} cluster${clusters.length === 1 ? "" : "s"} scanned${partial > 0 ? `, ${partial} partially` : ""}`;
  const icon = <IconCertificate size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="scanning certificates" />
    : state === "unreadable" ? <EmptyState icon={icon} title="No cluster could be scanned." hint="Each cluster's error is listed above." action={{ label: "Retry", onClick: batch.refresh }} />
    : state === "empty" && clusters.length === 0 ? <EmptyState icon={icon} title="No clusters to scan." hint="The fleet had no clusters when this scan started." action={{ label: "Refresh", onClick: batch.refresh }} />
    : state === "empty" ? <EmptyState icon={icon} title="No certificate lifetimes found." hint={`Nothing with an expiry was read on the ${scope}.`} />
    : <EmptyState icon={icon} title={`Nothing found expiring ${horizon}.`} hint={`On the ${scope}. Clusters and objects that were not read are not covered.`} action={windowDays !== 0 ? { label: "Show all", onClick: () => setWindowDays(0) } : undefined} />;

  return (
    <LensLayout
      label="Expiry horizon"
      title="Expiry horizon"
      summary={state === "loading" || state === "unreadable" ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{inWindow.length}</span>
          <span className="t-secondary">expiring {horizon} · {scope}</span>
          {expired > 0 && <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {expired} expired</span>}
          {soon > 0 && <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {soon} within 14 days</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary">{pending > 0 ? `${pending} cluster${pending === 1 ? "" : "s"} scanning… · ` : ""}soonest first · read-only</span>
          <UpdatedAt at={batch.readAt} />
          <button className="l-btn" onClick={batch.refresh}>Refresh</button>
        </>
      }
      controls={
        <div role="group" aria-label="Expiry window" style={{ display: "inline-flex", gap: 4 }}>
          {WINDOWS.map((w) => (
            <button key={w} className="l-chip" aria-pressed={windowDays === w} onClick={() => setWindowDays(w)}>{w === 0 ? "All" : `${w} days`}</button>
          ))}
        </div>
      }
      notice={errors.length > 0 || warnings.length > 0 || batch.joined.length > 0 || batch.left.length > 0 ? (
        <div className="t-body" role="status" style={{ display: "flex", flexDirection: "column", gap: 2 }}>
          <MembershipNote joined={batch.joined} left={batch.left} />
          {errors.map((c) => <span key={c.name} style={{ color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>▲ Could not scan {c.name}: {snaps[c.name]?.error}</span>)}
          {warnings.map((w) => <span key={w} style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Partial scan - {w}</span>)}
        </div>
      ) : undefined}
      state={state}
      stateView={stateView}
      body={
        // The shared lens table (UI review 2026-10-13): one header, the
        // standard row height and hover. Rows are read-only - nothing opens.
        <LensTable minWidth={560}>
          <div className="lens-table-head" role="row" style={{ gridTemplateColumns: grid }}>
            <span>Expires</span><span>Source</span><span>Object</span>{hasSubject && <span>Subject</span>}<span>Cluster</span>
          </div>
          <div style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
            {inWindow.map((r) => {
              const d = daysLeft(r.notAfterUnix);
              return (
                <div key={`${r.cluster}/${r.source}/${r.namespace}/${r.name}/${r.detail}`} role="row" className="lens-row" style={{ gridTemplateColumns: grid, cursor: "default" }}>
                  <span style={{ color: tone(d), fontWeight: d <= 14 ? 600 : undefined }}>
                    {d <= 14 ? "▲ " : ""}{d < 0 ? `${-d}d ago` : `${d}d`}
                  </span>
                  <span style={{ color: r.source === "cert-manager" ? "var(--color-text-tertiary)" : "var(--color-text-secondary)" }}>
                    {sourceLabel[r.source] ?? r.source}
                  </span>
                  <span style={{ ...ellipsis, color: "var(--color-text-primary)" }} title={`${r.namespace ? r.namespace + "/" : ""}${r.name}${r.detail ? ` (${r.detail})` : ""}`}>
                    {r.namespace ? `${r.namespace}/` : ""}{r.name}
                  </span>
                  {hasSubject && <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={r.subject}>{r.subject || "-"}</span>}
                  <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={r.cluster}>{r.cluster}</span>
                </div>
              );
            })}
          </div>
        </LensTable>
      }
      footerEnd={<span className="t-secondary">{hasSubject ? "" : "no subject reported for these rows · "}cert-manager rows are renewal-managed · raw TLS secrets and webhook CAs have no renewer · public certificate material only, keys never read</span>}
    />
  );
}
