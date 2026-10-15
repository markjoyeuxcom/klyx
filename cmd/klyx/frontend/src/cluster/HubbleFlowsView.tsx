import { Fragment, useEffect, useRef, useState } from "react";
import { IconChartDots3 } from "@tabler/icons-react";
import type { HubbleStatsDTO } from "../store/fleet";
import { getHubbleFlowStats } from "../bridge/gateway";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt } from "../chrome/Age";
import { useFleet } from "../store/fleet";
import { capState } from "../nav/registry";
import { LensLayout, type DatasetState } from "../layout/LensLayout";
import { MarkedPhrase } from "../layout/CountSentence";

// HubbleFlowsView - Cilium Hubble flow triage as a Network destination of its
// own (redesign step 7a). It was a second view inside the Gateway lens, which
// left a Cilium cluster without Gateway API no way to reach it.

export function HubbleFlowsView({ cluster }: { cluster: string }) {
  // The same resolver as the navigation: absence is claimed only once
  // detection ran. A restored route while the cluster connects says
  // "checking"; an unreadable cluster says it cannot tell.
  const cap = useFleet((s) => capState(s.clusters.find((c) => c.name === cluster), "hubble"));
  const flows = useHubble(cluster, cap === "present");
  if (cap !== "present") {
    const icon = <IconChartDots3 size={28} stroke={1.2} />;
    const view = cap === "checking"
      ? <EmptyState icon={icon} title="Checking for Hubble…" hint="Capability detection runs once the cluster connects." />
      : cap === "unknown"
        ? <EmptyState icon={icon} title="Cannot tell whether Hubble is installed." hint="The cluster could not be read, so capability detection has no answer." />
        : <EmptyState icon={icon} title="Hubble is not detected on this cluster." hint="Klyx found no Cilium Hubble metrics source here." />;
    return <LensLayout label="Hubble flows" title="Hubble flows" state={cap === "checking" ? "loading" : cap === "unknown" ? "unreadable" : "empty"} stateView={view} body={null} />;
  }
  return <FlowsLayout key={cluster} flows={flows} />;
}

type Flows = { stats: HubbleStatsDTO | null; failed: boolean; loading: boolean; at: number | null; refresh: () => void };

// useHubble reads Hubble's Prometheus series on demand while the Flows view
// is open; a late answer from an earlier request (or cluster) is dropped.
function useHubble(cluster: string, active: boolean): Flows {
  const [pinned, setPinned] = useState<{ cluster: string; stats: HubbleStatsDTO | null; at: number } | null>(null);
  const [loading, setLoading] = useState(false);
  const gen = useRef(0);
  const refresh = () => {
    const g = ++gen.current;
    const origin = cluster;
    setLoading(true);
    getHubbleFlowStats(origin).then((s) => s, () => null).then((s) => {
      if (g !== gen.current) return;
      setLoading(false);
      setPinned({ cluster: origin, stats: s, at: Date.now() });
    });
  };
  useEffect(() => { if (active) refresh(); }, [active, cluster]); // eslint-disable-line react-hooks/exhaustive-deps
  const mine = pinned && pinned.cluster === cluster ? pinned : null;
  return { stats: mine?.stats ?? null, failed: !!mine && mine.stats === null, loading, at: mine && mine.stats ? mine.at : null, refresh };
}

const fmtRate = (v: number | null | undefined) => (v == null ? "-" : v >= 10 ? `${Math.round(v)}` : v.toFixed(2));

function FlowsLayout({ flows }: { flows: Flows }) {
  const s = flows.stats;
  const hasAny = !!s && (s.forwardRate !== null || s.dropRate !== null || s.dropReasons.length > 0 || s.dnsTotalRate !== null || s.httpTotalRate !== null);
  let state: DatasetState = "ready";
  if (flows.failed) state = "unreadable";
  else if (!s) state = "loading";
  else if (!s.status.available || !hasAny) state = "empty";
  const icon = <IconChartDots3 size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={4} label="reading Hubble metrics" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not read Hubble metrics." hint="The metrics call failed." action={{ label: "Retry", onClick: flows.refresh }} />
    : s && !s.status.available ? <EmptyState icon={icon} title="Hubble metrics are unavailable." hint={s.status.message} />
    : <EmptyState icon={icon} title="No Hubble flow series yet." hint={s?.status.message} />;
  const drops = s?.dropRate ?? null;
  return (
    <LensLayout
      label="Hubble flows"
      title="Hubble flows"
      summary={state !== "ready" || !s ? null : (
        <>
          {drops !== null && drops > 0
            ? <MarkedPhrase fault>{fmtRate(drops)} dropped/s</MarkedPhrase>
            : drops !== null ? <span className="t-secondary">no drops</span> : null}
          {s.forwardRate !== null && <span className="t-secondary">{fmtRate(s.forwardRate)} forwarded/s</span>}
          <span className="t-secondary">5m rates</span>
        </>
      )}
      headerEnd={
        <>
          <UpdatedAt at={flows.at} />
          <button className="l-btn" onClick={flows.refresh} disabled={flows.loading}>{flows.loading ? "Reading…" : "Refresh"}</button>
        </>
      }
      state={state}
      stateView={stateView}
      body={s ? (
        <div className="lens-canvas" data-testid="hubble-flows">
          <div className="gw-node">
            <span className="t-legend">rates</span>
            <div className="gw-facts">
              <span>dropped</span><span style={{ color: (drops ?? 0) > 0 ? "var(--color-text-danger)" : undefined }}>{drops === null ? "not collected" : `${fmtRate(drops)}/s`}</span>
              <span>forwarded</span><span>{s.forwardRate === null ? "not collected" : `${fmtRate(s.forwardRate)}/s`}</span>
              <span>DNS errors</span><span style={{ color: (s.dnsErrorRate ?? 0) > 0 ? "var(--color-text-warning)" : undefined }}>{s.dnsErrorRate === null ? "not collected" : `${fmtRate(s.dnsErrorRate)}/s of ${fmtRate(s.dnsTotalRate)}/s`}</span>
              <span>HTTP 5xx</span><span style={{ color: (s.httpErrorRate ?? 0) > 0 ? "var(--color-text-danger)" : undefined }}>{s.httpErrorRate === null ? "not collected" : `${fmtRate(s.httpErrorRate)}/s of ${fmtRate(s.httpTotalRate)}/s`}</span>
            </div>
          </div>
          <div className="gw-node">
            <span className="t-legend">drop reasons · highest rate first</span>
            {s.dropReasons.length === 0 ? <span className="t-secondary">{drops === null ? "Drop metrics are not collected." : "No drops in the window."}</span> : (
              <div style={{ display: "grid", gridTemplateColumns: "88px minmax(0, 1fr) auto", gap: "3px 12px", font: "400 12px/16px var(--font-mono)" }}>
                {s.dropReasons.map((r) => (
                  <Fragment key={`${r.reason}/${r.protocol}`}>
                    <span style={{ color: "var(--color-text-danger)", textAlign: "right" }}>{fmtRate(r.rate)}/s</span>
                    <span style={{ color: "var(--color-text-primary)", overflowWrap: "anywhere" }}>{r.reason}</span>
                    <span style={{ color: "var(--color-text-tertiary)" }}>{r.protocol}</span>
                  </Fragment>
                ))}
              </div>
            )}
          </div>
        </div>
      ) : null}
      footerEnd={<span className="t-secondary">Hubble's Prometheus metrics through the cluster's metrics source · read on demand</span>}
    />
  );
}
