import { useEffect, useState } from "react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, EventRowDTO, FleetBoardEntry, MetricsDTO, ResourceRef } from "../store/fleet";
import { getClusterMetrics, getClusterSparklines } from "../bridge/metrics";
import type { SparklinesDTO } from "../bridge/metrics";
import { fetchFleetBoard } from "../bridge/fleetboard";
import { fetchAttention } from "../bridge/attention";
import { Sparkline } from "../chrome/Sparkline";
import { UpdatedAt } from "../chrome/Age";
import { listInstancePage } from "../bridge/crd";
import { fetchEventsSnapshot } from "../bridge/events";
import { CLUSTER_RISK_REFS, riskFor } from "./resourceRisk";
import { ellipsis } from "../chrome/primitives";
import { LensLayout } from "../layout/LensLayout";
import { attentionAreas, uncoveredDomains, type Area, type Uncovered } from "../nav/attention";
import { NAV_GROUPS, navigate } from "../nav/registry";

type BoardEntry = FleetBoardEntry | undefined;

// Overview on the workspace template (redesign step 7e). Its attention
// section reads the shared cluster summary - the same reading the navigation
// tree and the mast use - so the three never disagree and nothing here is a
// second ledger with its own fetch. Nothing is green: a quiet area is a
// neutral fact scoped to what was read.
export function Overview({ c }: { c: ClusterDTO }) {
  const tags = [c.env, c.region, c.provider, c.group].filter(Boolean);
  const metrics = useFleet((s) => s.metrics);
  const loading = useFleet((s) => s.metrics.loading);
  const attention = useFleet((s) => s.attention);
  const board = useFleet((s) => s.fleetBoard[c.name]);
  // 30m cluster utilisation sparklines — fetched once per mount, local state.
  const [spark, setSpark] = useState<SparklinesDTO | null>(null);

  useEffect(() => {
    getClusterMetrics(c.name, false);
    void fetchFleetBoard([c.name]);
    let cancelled = false;
    getClusterSparklines(c.name).then((dto) => {
      if (!cancelled) setSpark(dto);
    });
    return () => {
      cancelled = true;
      setSpark(null);
      useFleet.getState().clearMetrics();
    };
  }, [c.name]);

  const m: MetricsDTO | null = metrics.cluster === c.name ? metrics.dto : null;
  const areas = attentionAreas(attention, c.name);
  const mine = attention.cluster === c.name;
  const stateTone = c.state === "Failed" ? "var(--color-text-danger)" : c.state === "Synced" ? "var(--color-text-secondary)" : "var(--color-text-warning)";
  const refresh = () => {
    void fetchAttention(c.name);
    void fetchFleetBoard([c.name]);
    getClusterMetrics(c.name, true);
  };

  return (
    <LensLayout
      label="Overview"
      title="Overview"
      summary={
        <>
          <span className="t-status" style={{ color: stateTone }}>{c.state === "Failed" ? "▲ " : ""}{stateWord(c)}</span>
          {c.version && <span className="t-secondary">{c.version}</span>}
          {tags.length > 0 && <span className="t-secondary">{tags.join(" · ")}</span>}
        </>
      }
      headerEnd={
        <>
          <UpdatedAt at={mine ? attention.at : null} />
          <button className="l-btn" onClick={refresh} disabled={mine && attention.loading}>{mine && attention.loading ? "Reading…" : "Refresh"}</button>
        </>
      }
      notice={c.reason ? (
        <div className="t-body" role="status" style={{ color: stateTone, overflowWrap: "anywhere" }}>{c.state === "Failed" ? "▲ " : ""}{c.reason}</div>
      ) : undefined}
      state="ready"
      body={
        <div className="lens-canvas" data-testid="overview">
          <AttentionSection cluster={c.name} areas={areas} uncovered={uncoveredDomains(c)} />
          <div className="ov-columns">
            <section aria-label="Cluster facts" className="ov-section">
              <h3 className="t-section-label">Cluster</h3>
              <div className="insp-kv">
                <span>connection</span><span style={{ color: stateTone }}>{freshnessText(c)}</span>
                <span>nodes</span><span>{c.nodesReady}/{c.nodesTotal} ready</span>
                <span>pods</span><span>{c.pods}</span>
                <span>namespaces</span><span>{mine && attention.counts.namespaces != null ? attention.counts.namespaces : "—"}</span>
                <span>flux</span><span>{fluxCapability(c, attention.counts.flux)}</span>
                {board?.argo && <><span>argo cd</span><span>{board.argo.broken > 0 ? `${board.argo.broken} of ${board.argo.total} need attention` : `${board.argo.total} applications`}</span></>}
                <span>gateway</span><span>{gatewayCapability(c, board)}</span>
              </div>
            </section>
            <section aria-label="Resources" className="ov-section">
              <h3 className="t-section-label">Resources</h3>
              <div className="insp-kv">
                <span>cpu used</span>
                <span style={{ display: "inline-flex", alignItems: "center", gap: 10 }}>
                  <Usage frac={m?.cpuFraction ?? null} />
                  {spark?.available && <Sparkline points={spark.cpu} height={16} width={90} />}
                </span>
                <span>mem used</span>
                <span style={{ display: "inline-flex", alignItems: "center", gap: 10 }}>
                  <Usage frac={m?.memFraction ?? null} />
                  {spark?.available && <Sparkline points={spark.mem} height={16} width={90} />}
                </span>
              </div>
              <MonitoringLine dto={m} loading={loading} onRefresh={() => getClusterMetrics(c.name, true)} />
            </section>
          </div>
          <CriticalEvents cluster={c.name} />
          <ClusterRisks cluster={c.name} />
        </div>
      }
    />
  );
}

function stateWord(c: ClusterDTO): string {
  if (c.state === "Synced") return "Connected";
  if (c.state === "Degraded") return "Connected with notes";
  if (c.state === "Stale") return "Stale";
  return c.state;
}

// ---- attention ---------------------------------------------------------------------

// Each area links to the lens it came from, with that lens's attention filter.
function goArea(id: Area["id"]) {
  const nav = useFleet.getState();
  switch (id) {
    case "workloads": nav.setSection("workloads"); nav.setWorkloadsNeedsAttention(true); break;
    case "pods": nav.setSection("pods"); nav.setPodsNeedsAttention(true); break;
    case "events": nav.setSection("events"); nav.setWarningsOnly(true); break;
    case "nodes": nav.setSection("nodes"); break;
    case "helm": nav.setSection("helm"); break;
    case "flux": nav.openFlux("reconciliation"); break;
    case "argo": nav.openArgo("applications"); break;
  }
}

function goUncovered(u: Uncovered) {
  const nav = useFleet.getState();
  if (nav.route.name !== "cluster") return;
  const d = NAV_GROUPS.flatMap((g) => g.destinations).find((x) => x.id === u.destinationId);
  if (d) navigate(nav.route.cluster, d);
}

function AttentionSection({ cluster, areas, uncovered }: { cluster: string; areas: Area[]; uncovered: Uncovered[] }) {
  const lit = areas.filter((a) => a.state === "attention");
  const gaps = areas.filter((a) => a.gap || a.state === "unreadable" || a.state === "unverified");
  const loadingAll = areas.every((a) => a.state === "loading");
  return (
    <section aria-label="Attention" className="ov-section">
      <h3 className="t-section-label">Attention</h3>
      {loadingAll ? <span className="t-secondary">Reading {cluster}…</span> : (
        <div className="ov-areas">
          {areas.map((a) => (
            <button key={a.id} className="ov-area" data-state={a.state} onClick={() => goArea(a.id)} title={`Open ${a.label}`}>
              <span className="ov-area-label">{a.label}</span>
              <span className="ov-area-text" style={{ color: a.state === "attention" ? (a.tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)") : a.state === "unreadable" || a.stale ? "var(--color-text-warning)" : undefined }}>
                {a.state === "attention" && a.tone === "danger" ? "▲ " : ""}{a.text}
              </span>
            </button>
          ))}
          {/* Installed domains the summary does not read are named, so no
              quiet line implies they were checked. */}
          {uncovered.map((u) => (
            <button key={u.id} className="ov-area" data-state="uncovered" onClick={() => goUncovered(u)} title={`Open ${u.label}`}>
              <span className="ov-area-label">{u.label}</span>
              <span className="ov-area-text">not covered by the summary - open to check</span>
            </button>
          ))}
        </div>
      )}
      {!loadingAll && lit.length === 0 && (
        <span className="t-secondary">
          {gaps.length > 0
            ? `No attention found in the ${areas.length - gaps.length} summarized area${areas.length - gaps.length === 1 ? "" : "s"} read; ${gaps.length} could not be established.`
            : `No attention found in the ${areas.length} summarized areas read.`}
          {uncovered.length > 0 ? ` ${uncovered.length} installed view${uncovered.length === 1 ? " is" : "s are"} not covered by the summary.` : ""}
        </span>
      )}
    </section>
  );
}

function CriticalEvents({ cluster }: { cluster: string }) {
  const [events, setEvents] = useState<EventRowDTO[] | null>(null);
  // A failed read is not "no warning events".
  const [failed, setFailed] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setEvents(null);
    setFailed(null);
    fetchEventsSnapshot(cluster, "")
      .then((result) => {
        if (cancelled) return;
        const err = (result as { error?: string }).error;
        if (err) { setFailed(err); setEvents([]); return; }
        const warnings = (result.events ?? [])
          .filter((e) => e.type === "Warning")
          .sort((a, b) => b.lastSeenUnix - a.lastSeenUnix)
          .slice(0, 5);
        setEvents(warnings);
      })
      .catch((e) => {
        if (!cancelled) { setFailed(String(e?.message ?? e ?? "read failed")); setEvents([]); }
      });
    return () => {
      cancelled = true;
    };
  }, [cluster]);

  const openEvents = (event?: EventRowDTO) => {
    const nav = useFleet.getState();
    nav.setSection("events");
    nav.setWarningsOnly(true);
    if (event) nav.setEventsSearch(event.name || event.reason || event.namespace);
  };

  return (
    <Section title="Recent warning events">
      {events === null ? (
        <span className="t-secondary">Reading events…</span>
      ) : failed ? (
        <span className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Events could not be read: {failed}</span>
      ) : events.length === 0 ? (
        <span className="t-secondary">No warning events in the current read.</span>
      ) : events.map((event, i) => (
        <button
          key={`${event.namespace}/${event.kind}/${event.name}/${event.reason}/${i}`}
          onClick={() => openEvents(event)}
          title={`${event.namespace} ${event.kind}/${event.name}: ${event.message}`}
          style={{
            display: "grid",
            gridTemplateColumns: "44px minmax(110px, 0.7fr) minmax(120px, 0.8fr) minmax(0, 1.4fr)",
            gap: 10,
            alignItems: "baseline",
            border: "0.5px solid var(--color-border-tertiary)",
            background: "var(--color-background-primary)",
            color: "var(--color-text-secondary)",
            padding: "5px 8px",
            cursor: "pointer",
            fontFamily: "var(--font-mono)",
            fontSize: 11,
            textAlign: "left",
          }}
        >
          <span style={{ color: "var(--color-text-secondary)" }}>{event.count > 1 ? `×${event.count}` : "warning"}</span>
          <span style={{ ...ellipsis }}>{event.reason}</span>
          <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>{event.namespace}/{event.name}</span>
          <span style={{ minWidth: 0, ...ellipsis }}>{event.message}</span>
        </button>
      ))}
      {events && events.length > 0 && (
        <button className="l-btn" onClick={() => openEvents()} style={{ alignSelf: "flex-start" }}>Open warning events</button>
      )}
    </Section>
  );
}

type RiskSummary = { ref: ResourceRef; count: number; sample: string; reason: string };

function ClusterRisks({ cluster }: { cluster: string }) {
  const [risks, setRisks] = useState<RiskSummary[] | null>(null);
  // Kinds whose read FAILED. "no resource risks found" is only claimed when
  // every kind was actually read (review 2026-10-03 #10): one failed page used
  // to reject the whole scan into a confident empty result.
  const [unreadable, setUnreadable] = useState<string[]>([]);

  useEffect(() => {
    let cancelled = false;
    setRisks(null);
    setUnreadable([]);
    const failed: string[] = [];
    Promise.all(CLUSTER_RISK_REFS.map(async (ref) => {
      let page;
      try {
        page = await listInstancePage(cluster, ref);
      } catch {
        failed.push(ref.kind);
        return null;
      }
      const bad = (page.items ?? [])
        .map((row) => ({ row, risk: riskFor(ref, row) }))
        .filter((item) => item.risk.bad);
      if (bad.length === 0) return null;
      const first = bad[0];
      return {
        ref,
        count: bad.length,
        sample: first.row.namespace ? `${first.row.namespace}/${first.row.name}` : first.row.name,
        reason: first.risk.reason,
      } satisfies RiskSummary;
    })).then((items) => {
      if (!cancelled) {
        setUnreadable([...failed].sort());
        setRisks(items.filter((item): item is RiskSummary => item !== null).sort((a, b) => b.count - a.count || a.ref.kind.localeCompare(b.ref.kind)));
      }
    });
    return () => {
      cancelled = true;
    };
  }, [cluster]);

  const openRisk = (ref: ResourceRef) => {
    const nav = useFleet.getState();
    nav.setSection("resources");
    nav.openResource(ref);
    nav.setInstanceRiskOnly(true);
  };

  return (
    <Section title="Resource risks">
      {risks === null ? (
        <span className="t-secondary">Reading resources…</span>
      ) : risks.length === 0 && unreadable.length === 0 ? (
        <span className="t-secondary">No resource risks found in the {CLUSTER_RISK_REFS.length} kinds read.</span>
      ) : risks.slice(0, 6).map((r) => (
        <button
          key={`${r.ref.group}/${r.ref.plural}`}
          onClick={() => openRisk(r.ref)}
          title={`${r.ref.kind}: ${r.count} - ${r.reason} - ${r.sample}`}
          style={{
            display: "flex",
            flexWrap: "wrap",
            gap: 10,
            alignItems: "baseline",
            border: "0.5px solid var(--color-border-tertiary)",
            background: "var(--color-background-primary)",
            color: "var(--color-text-secondary)",
            padding: "5px 8px",
            cursor: "pointer",
            fontFamily: "var(--font-mono)",
            fontSize: 11,
            textAlign: "left",
          }}
        >
          <span style={{ flex: "0 0 150px", color: "var(--color-text-warning)", ...ellipsis }}>{r.ref.kind}</span>
          <span style={{ flex: "0 0 28px", color: "var(--color-text-primary)" }}>{r.count}</span>
          <span style={{ flex: "0 1 190px", minWidth: 120, ...ellipsis }}>{r.reason}</span>
          <span style={{ flex: "1 1 220px", minWidth: 0, color: "var(--color-text-tertiary)", ...ellipsis }}>{r.sample}</span>
        </button>
      ))}
      {risks !== null && risks.length > 6 && <span className="t-secondary">+{risks.length - 6} more kinds with risks</span>}
      {risks !== null && unreadable.length > 0 && (
        <span
          data-testid="risks-unreadable"
          title={unreadable.join(", ")}
          className="t-body"
          style={{ color: "var(--color-text-warning)" }}
        >
          {unreadable.length} kind{unreadable.length === 1 ? "" : "s"} could not be read - risks there are unknown
        </span>
      )}
    </Section>
  );
}

// ---- shared sub-components (unchanged) ----------------------------------------

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section aria-label={title} className="ov-section">
      <h3 className="t-section-label">{title}</h3>
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{children}</div>
    </section>
  );
}

function freshnessText(c: ClusterDTO): string {
  if (c.state === "Synced") return "watch-backed";
  if (c.state === "Stale") return `stale watch${c.ageSeconds > 0 ? ` · last good ${formatAge(c.ageSeconds)}` : ""}`;
  if (c.state === "Connecting") return "connecting";
  if (c.state === "Degraded") return "degraded";
  return c.state.toLowerCase();
}

function formatAge(seconds: number): string {
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

function fluxCapability(c: ClusterDTO, flux: import("../store/fleet").LensCount): string {
  if (flux === "unreadable") return c.fluxPresent ? "installed · summary could not be read" : "summary could not be read";
  if (flux === "absent") return "not detected";
  return flux > 0 ? `${flux} not ready` : "none not ready";
}

function gatewayCapability(c: ClusterDTO, board: BoardEntry): string {
  const g = board?.gateway;
  if (!g) return c.networkTier + (c.networkReason ? ` - ${c.networkReason}` : "");
  if (!g.served) return "not served";
  const issues = (g.brokenRoutes ?? 0) + g.unprogrammed;
  if (issues > 0) return `${issues} issues · ${g.routes ?? "?"} routes`;
  return g.routes != null ? `${g.routes} routes` : `${g.gateways} gateways`;
}
function Usage({ frac }: { frac: number | null }) {
  if (frac == null) return <span style={{ color: "var(--color-text-tertiary)" }}>—</span>;
  const pct = Math.max(0, Math.round(frac * 100));
  // Used is a measurement: neutral until it is high - never a green "fine".
  const color = pct >= 90 ? "var(--color-text-danger)" : pct >= 75 ? "var(--color-text-warning)" : "var(--color-text-tertiary)";
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
      <span>{pct >= 90 ? "▲ " : ""}{pct}%</span>
      <span style={{ width: 80, height: 4, background: "var(--color-background-secondary)", borderRadius: 2, overflow: "hidden" }}>
        <span style={{ display: "block", height: "100%", width: `${Math.min(100, Math.max(0, pct))}%`, background: color }} />
      </span>
    </span>
  );
}

function MonitoringLine({ dto, loading, onRefresh }: { dto: MetricsDTO | null; loading: boolean; onRefresh: () => void }) {
  let text: string;
  let color = "var(--color-text-tertiary)";
  if (loading && !dto) {
    text = "monitoring: checking…";
  } else if (!dto) {
    text = "monitoring: —";
  } else if (!dto.available) {
    text = `monitoring unavailable: ${dto.reason || "unknown"}`;
    color = "var(--color-text-warning)";
  } else {
    const where = dto.mode === "explicit-endpoint" ? `endpoint ${dto.source}` : `svc ${dto.source}`;
    const label = dto.mode === "discovered-service" ? "discovered" : dto.mode === "explicit-service-ref" ? "service" : "endpoint";
    text = `monitoring: ${label} · ${where}`;
    if (dto.warning) text += ` ⚠︎ ${dto.warning}`;
  }
  return (
    <div style={{ display: "flex", gap: 8, fontSize: 11, alignItems: "center", marginTop: 2 }}>
      <span style={{ color }}>{text}</span>
      <button
        onClick={onRefresh}
        disabled={loading}
        title="re-probe Prometheus"
        style={{ fontSize: 10, padding: "1px 6px", borderRadius: 4, cursor: loading ? "default" : "pointer", border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-secondary)", opacity: loading ? 0.6 : 1 }}
      >
        {loading ? "…" : "refresh"}
      </button>
    </div>
  );
}
