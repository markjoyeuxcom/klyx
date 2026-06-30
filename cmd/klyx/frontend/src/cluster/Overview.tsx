import { useEffect, useState } from "react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, FleetBoardEntry, MetricsDTO } from "../store/fleet";
import { getClusterMetrics, getClusterSparklines } from "../bridge/metrics";
import type { SparklinesDTO } from "../bridge/metrics";
import { fetchOverviewSummary } from "../bridge/overview";
import { fetchFleetBoard } from "../bridge/fleetboard";
import { Sparkline } from "../chrome/Sparkline";
import { stateColor } from "./stateColors";
import { listInstancePage } from "../bridge/crd";
import { fetchEventsSnapshot } from "../bridge/events";
import { CLUSTER_RISK_REFS, riskFor } from "./resourceRisk";
import type { EventRowDTO, ResourceRef } from "../store/fleet";

type BoardEntry = FleetBoardEntry | undefined;
import { ellipsis, StatusDot } from "../chrome/primitives";

export function Overview({ c }: { c: ClusterDTO }) {
  const tags = [c.env, c.region, c.provider, c.group].filter(Boolean);
  const metrics = useFleet((s) => s.metrics);
  const loading = useFleet((s) => s.metrics.loading);
  const summary = useFleet((s) => s.overviewSummary);
  const board = useFleet((s) => s.fleetBoard[c.name]);
  // 30m cluster utilisation sparklines — fetched once per mount, local state.
  const [spark, setSpark] = useState<SparklinesDTO | null>(null);

  useEffect(() => {
    getClusterMetrics(c.name, false);
    fetchOverviewSummary(c.name);
    void fetchFleetBoard([c.name]);
    let cancelled = false;
    getClusterSparklines(c.name).then((dto) => {
      if (!cancelled) setSpark(dto);
    });
    return () => {
      cancelled = true;
      setSpark(null);
      useFleet.getState().clearMetrics();
      useFleet.getState().clearOverviewSummary();
    };
  }, [c.name]);

  const m: MetricsDTO | null = metrics.cluster === c.name ? metrics.dto : null;
  const s = summary.cluster === c.name ? summary : null;

  return (
    <div style={{ padding: "14px 16px", display: "grid", gap: 12 }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 16, borderBottom: "0.5px solid var(--color-border-tertiary)", paddingBottom: 12 }}>
        <div style={{ minWidth: 0 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 6 }}>
            <StatusDot size={8} color={stateColor[c.state] ?? "var(--color-text-tertiary)"} />
            <span style={{ fontFamily: "var(--font-mono)", fontWeight: 600, fontSize: 16 }}>{c.name}</span>
            {c.version && <Badge>{c.version}</Badge>}
          </div>
          <div style={{ display: "flex", gap: 4, flexWrap: "wrap" }}>
            {tags.map((t) => <Badge key={t}>{t}</Badge>)}
          </div>
        </div>
        <span style={{ fontFamily: "var(--font-mono)", color: freshnessTone(c), fontSize: 12, whiteSpace: "nowrap" }}>
          {freshnessText(c)}
        </span>
      </div>

      <AttentionStrip cluster={c.name} summary={s} board={board} clusterState={c.state} loading={summary.loading && summary.cluster === c.name} />

      <div style={{ display: "flex", flexWrap: "wrap", gap: 32, alignItems: "flex-start" }}>
        <div style={{ flex: "0 1 360px", minWidth: 260 }}>
          <Section title="Health">
            <Row label="state"><span style={{ color: stateColor[c.state] }}>{c.state}</span></Row>
            {c.reason && <Row label="reason">{c.reason}</Row>}
            <Row label="freshness">{freshnessText(c)}</Row>
          </Section>

          <Section title="Capacity">
            <Row label="nodes">{c.nodesReady}/{c.nodesTotal}</Row>
            <Row label="pods">{c.pods}</Row>
            {s && s.namespaces !== null && <Row label="namespaces">{s.namespaces}</Row>}
          </Section>

          <Section title="Capabilities">
            <Row label="flux">{fluxCapability(s)}</Row>
            {board?.argo && <Row label="argo">{board.argo.broken > 0 ? `${board.argo.broken} degraded / ${board.argo.total}` : `${board.argo.total} synced`}</Row>}
            <Row label="gateway">{gatewayCapability(c, board)}</Row>
          </Section>
        </div>

        <div style={{ flex: "1 1 560px", maxWidth: 900, minWidth: 360 }}>
          <Section title="Resources">
            <Row label="cpu used">
              <span style={{ display: "inline-flex", alignItems: "center", gap: 10 }}>
                <Usage frac={m?.cpuFraction ?? null} />
                {spark?.available && <Sparkline points={spark.cpu} height={16} width={90} />}
              </span>
            </Row>
            <Row label="mem used">
              <span style={{ display: "inline-flex", alignItems: "center", gap: 10 }}>
                <Usage frac={m?.memFraction ?? null} />
                {spark?.available && <Sparkline points={spark.mem} height={16} width={90} />}
              </span>
            </Row>
            <MonitoringLine dto={m} loading={loading} onRefresh={() => getClusterMetrics(c.name, true)} />
          </Section>

          <NextActions summary={s} board={board} clusterState={c.state} />
          <CriticalEvents cluster={c.name} />
          <ClusterRisks cluster={c.name} />
        </div>
      </div>
    </div>
  );
}

// ---- Attention strip -----------------------------------------------------------

type SummaryForStrip = {
  unhealthyWorkloads: number | null;
  podsNotReady: number | null;
  warningEvents: number | null;
  nodeProblems: number | null;
  helmAvailable: boolean;
  failedReleases: number | null;
  flux: { present: boolean; notReady: number; suspended: number } | null;
};

function AttentionStrip({
  cluster,
  summary,
  board,
  clusterState,
  loading,
}: {
  cluster: string;
  summary: SummaryForStrip | null;
  board: BoardEntry;
  clusterState: string;
  loading: boolean;
}) {
  const nav = useFleet.getState();

  function goWorkloads() {
    nav.setSection("workloads");
    nav.setWorkloadsNeedsAttention(true);
  }
  function goPods() {
    nav.setSection("pods");
    nav.setPodsNeedsAttention(true);
  }
  function goEvents() {
    nav.setSection("events");
    nav.setWarningsOnly(true);
  }
  function goNodes() {
    nav.setSection("nodes");
  }
  function goHelm() {
    nav.setSection("helm");
  }
  function goGitOps() {
    nav.setSection("gitops");
  }
  function goArgo() {
    nav.setSection("argo");
  }
  function goNetwork() {
    nav.setSection("gatewayApi");
  }

  const fluxTileTitle = (() => {
    const suspended = summary?.flux?.suspended ?? 0;
    return suspended > 0 ? `${suspended} suspended` : undefined;
  })();

  const tiles: Array<{
    key: string;
    value: string;
    label: string;
    variant: "danger" | "warning" | "success" | "info";
    onClick?: () => void;
    hidden?: boolean;
    title?: string;
  }> = [
    { key: "workloads", value: tileValue(summary?.unhealthyWorkloads ?? null, loading), label: "unhealthy workloads", variant: toneFor(summary?.unhealthyWorkloads, "danger", loading), onClick: goWorkloads },
    { key: "pods", value: tileValue(summary?.podsNotReady ?? null, loading), label: "pods not ready", variant: toneFor(summary?.podsNotReady, "danger", loading), onClick: goPods },
    { key: "events", value: tileValue(summary?.warningEvents ?? null, loading), label: "warning events", variant: toneFor(summary?.warningEvents, "warning", loading), onClick: goEvents },
    { key: "nodes", value: tileValue(summary?.nodeProblems ?? null, loading), label: "node problems", variant: toneFor(summary?.nodeProblems, "danger", loading), onClick: goNodes },
    { key: "helm", value: tileValue(summary?.failedReleases ?? null, loading), label: "failed releases", variant: toneFor(summary?.failedReleases, "danger", loading), onClick: goHelm, hidden: !(summary?.helmAvailable ?? false) },
    { key: "flux", value: tileValue(summary?.flux != null ? summary.flux.notReady : null, loading), label: "flux not ready", variant: toneFor(summary?.flux?.notReady, "danger", loading), onClick: goGitOps, hidden: !(summary?.flux?.present ?? false), title: fluxTileTitle },
    { key: "argo", value: board?.argo ? String(board.argo.broken) : "—", label: "argo degraded", variant: board?.argo?.broken ? "warning" : "success", onClick: goArgo, hidden: !board?.argo },
    { key: "gateway", value: gatewayTileValue(board), label: "gateway issues", variant: gatewayTileTone(board), onClick: goNetwork, hidden: board?.gateway?.served === false || (!board?.gateway && clusterState === "Synced") },
    { key: "freshness", value: clusterState === "Synced" ? "live" : clusterState.toLowerCase(), label: "watch state", variant: clusterState === "Synced" ? "success" : "warning", title: cluster },
  ];

  const visibleTiles = tiles.filter((t) => !t.hidden);

  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginBottom: 16 }}>
      {visibleTiles.map((tile) => (
        <StatTile
          key={tile.key}
          value={tile.value}
          label={tile.label}
          variant={tile.variant}
          onClick={tile.onClick}
          title={tile.title}
        />
      ))}
    </div>
  );
}

function StatTile({
  value,
  label,
  variant,
  onClick,
  title: tileTitle,
}: {
  value: string;
  label: string;
  variant: "danger" | "warning" | "success" | "info";
  onClick?: () => void;
  title?: string;
}) {
  const isLoading = value === "—";
  const countColor = isLoading
    ? "var(--color-text-tertiary)"
    : variant === "danger"
      ? "var(--color-text-danger)"
      : variant === "warning"
        ? "var(--color-text-warning)"
        : variant === "success"
          ? "var(--color-text-success)"
          : "var(--color-text-info)";

  return (
    <button
      onClick={onClick}
      title={tileTitle ?? (value === "—" ? "failed to load" : undefined)}
      disabled={!onClick}
      style={{
        display: "flex",
        flexDirection: "column",
        alignItems: "flex-start",
        gap: 2,
        padding: "6px 10px",
        border: "0.5px solid var(--color-border-tertiary)",
        borderRadius: 3,
        background: "var(--color-background-primary)",
        cursor: onClick ? "pointer" : "default",
        minWidth: 104,
      }}
    >
      <span style={{ fontSize: 17, fontWeight: 500, color: countColor, lineHeight: 1 }}>
        {value}
      </span>
      <span style={{ fontSize: 10, color: "var(--color-text-tertiary)", lineHeight: 1 }}>{label}</span>
    </button>
  );
}

function tileValue(count: number | null | undefined, loading: boolean): string {
  if (loading || count == null) return "—";
  return String(count);
}

function toneFor(count: number | null | undefined, alertTone: "danger" | "warning", loading: boolean): "danger" | "warning" | "success" | "info" {
  if (loading || count == null) return "info";
  return count > 0 ? alertTone : "success";
}

function gatewayTileValue(board: BoardEntry): string {
  const g = board?.gateway;
  if (!g) return "—";
  const broken = (g.brokenRoutes ?? 0) + g.unprogrammed;
  return String(broken);
}

function gatewayTileTone(board: BoardEntry): "danger" | "warning" | "success" | "info" {
  const g = board?.gateway;
  if (!g) return "info";
  return (g.brokenRoutes ?? 0) + g.unprogrammed > 0 ? "warning" : "success";
}

function NextActions({
  summary,
  board,
  clusterState,
}: {
  summary: SummaryForStrip | null;
  board: BoardEntry;
  clusterState: string;
}) {
  const nav = useFleet.getState();
  const actions: Array<{ label: string; detail: string; run: () => void; tone: string }> = [];
  if ((summary?.unhealthyWorkloads ?? 0) > 0) actions.push({ label: "workloads", detail: `${summary?.unhealthyWorkloads} need attention`, run: () => { nav.setSection("workloads"); nav.setWorkloadsNeedsAttention(true); }, tone: "var(--color-text-danger)" });
  if ((summary?.podsNotReady ?? 0) > 0) actions.push({ label: "pods", detail: `${summary?.podsNotReady} not ready`, run: () => { nav.setSection("pods"); nav.setPodsNeedsAttention(true); }, tone: "var(--color-text-danger)" });
  if ((summary?.warningEvents ?? 0) > 0) actions.push({ label: "events", detail: `${summary?.warningEvents} warnings`, run: () => { nav.setSection("events"); nav.setWarningsOnly(true); }, tone: "var(--color-text-warning)" });
  if ((summary?.flux?.notReady ?? 0) > 0 || (summary?.flux?.suspended ?? 0) > 0) actions.push({ label: "flux", detail: `${summary?.flux?.notReady ?? 0} not ready · ${summary?.flux?.suspended ?? 0} suspended`, run: () => nav.setSection("gitops"), tone: "var(--color-text-warning)" });
  if ((board?.argo?.broken ?? 0) > 0) actions.push({ label: "argo", detail: `${board?.argo?.broken} degraded`, run: () => nav.setSection("argo"), tone: "var(--color-text-warning)" });
  if (((board?.gateway?.brokenRoutes ?? 0) + (board?.gateway?.unprogrammed ?? 0)) > 0) actions.push({ label: "gateway", detail: `${(board?.gateway?.brokenRoutes ?? 0) + (board?.gateway?.unprogrammed ?? 0)} issues`, run: () => nav.setSection("gatewayApi"), tone: "var(--color-text-warning)" });
  if (clusterState !== "Synced") actions.push({ label: "watch", detail: clusterState.toLowerCase(), run: () => nav.setSection("overview"), tone: "var(--color-text-warning)" });

  return (
    <Section title="Needs Attention">
      {actions.length === 0 ? (
        <span style={{ color: "var(--color-text-success)", fontSize: 12, fontFamily: "var(--font-mono)" }}>nothing urgent</span>
      ) : actions.slice(0, 5).map((a) => (
        <button key={a.label} onClick={a.run} style={{ display: "grid", gridTemplateColumns: "84px 1fr", gap: 10, alignItems: "center", border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-secondary)", padding: "5px 8px", cursor: "pointer", fontFamily: "var(--font-mono)", fontSize: 11, textAlign: "left" }}>
          <span style={{ color: a.tone }}>{a.label}</span>
          <span>{a.detail}</span>
        </button>
      ))}
    </Section>
  );
}

function CriticalEvents({ cluster }: { cluster: string }) {
  const [events, setEvents] = useState<EventRowDTO[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    setEvents(null);
    fetchEventsSnapshot(cluster, "")
      .then((result) => {
        if (cancelled) return;
        const warnings = (result.events ?? [])
          .filter((e) => e.type === "Warning")
          .sort((a, b) => b.lastSeenUnix - a.lastSeenUnix)
          .slice(0, 5);
        setEvents(warnings);
      })
      .catch(() => {
        if (!cancelled) setEvents([]);
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
    <Section title="Critical Events">
      {events === null ? (
        <span style={{ color: "var(--color-text-tertiary)", fontSize: 12, fontFamily: "var(--font-mono)" }}>checking events…</span>
      ) : events.length === 0 ? (
        <span style={{ color: "var(--color-text-success)", fontSize: 12, fontFamily: "var(--font-mono)" }}>no warning events</span>
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
          <span style={{ color: "var(--color-text-warning)" }}>{event.count > 1 ? `x${event.count}` : "warn"}</span>
          <span style={{ ...ellipsis }}>{event.reason}</span>
          <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>{event.namespace}/{event.name}</span>
          <span style={{ minWidth: 0, ...ellipsis }}>{event.message}</span>
        </button>
      ))}
      {events && events.length > 0 && (
        <button onClick={() => openEvents()} style={{ ...quietBtn, alignSelf: "flex-start" }}>open warnings</button>
      )}
    </Section>
  );
}

type RiskSummary = { ref: ResourceRef; count: number; sample: string; reason: string };

function ClusterRisks({ cluster }: { cluster: string }) {
  const [risks, setRisks] = useState<RiskSummary[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    setRisks(null);
    Promise.all(CLUSTER_RISK_REFS.map(async (ref) => {
      const page = await listInstancePage(cluster, ref);
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
        setRisks(items.filter((item): item is RiskSummary => item !== null).sort((a, b) => b.count - a.count || a.ref.kind.localeCompare(b.ref.kind)));
      }
    }).catch(() => {
      if (!cancelled) setRisks([]);
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
    <Section title="Cluster Risks">
      {risks === null ? (
        <span style={{ color: "var(--color-text-tertiary)", fontSize: 12, fontFamily: "var(--font-mono)" }}>checking resources…</span>
      ) : risks.length === 0 ? (
        <span style={{ color: "var(--color-text-success)", fontSize: 12, fontFamily: "var(--font-mono)" }}>no resource risks found</span>
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
    </Section>
  );
}

// ---- shared sub-components (unchanged) ----------------------------------------

function Badge({ children }: { children: React.ReactNode }) {
  return <span style={{ background: "var(--color-background-secondary)", color: "var(--color-text-secondary)", fontSize: 11, padding: "1px 6px", borderRadius: 3 }}>{children}</span>;
}
function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 6 }}>{title}</div>
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{children}</div>
    </div>
  );
}

function freshnessText(c: ClusterDTO): string {
  if (c.state === "Synced") return "watch-backed";
  if (c.state === "Stale") return `stale watch${c.ageSeconds > 0 ? ` · last good ${formatAge(c.ageSeconds)}` : ""}`;
  if (c.state === "Connecting") return "connecting";
  if (c.state === "Degraded") return "degraded";
  return c.state.toLowerCase();
}

function freshnessTone(c: ClusterDTO): string {
  return c.state === "Synced" ? "var(--color-text-success)" : c.state === "Failed" ? "var(--color-text-danger)" : "var(--color-text-warning)";
}

function formatAge(seconds: number): string {
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

function fluxCapability(summary: SummaryForStrip | null): string {
  if (!summary?.flux?.present) return "not detected";
  const bits = [`${summary.flux.notReady} not ready`];
  if (summary.flux.suspended > 0) bits.push(`${summary.flux.suspended} suspended`);
  return bits.join(" · ");
}

function gatewayCapability(c: ClusterDTO, board: BoardEntry): string {
  const g = board?.gateway;
  if (!g) return c.networkTier + (c.networkReason ? ` - ${c.networkReason}` : "");
  if (!g.served) return "not served";
  const issues = (g.brokenRoutes ?? 0) + g.unprogrammed;
  if (issues > 0) return `${issues} issues · ${g.routes ?? "?"} routes`;
  return g.routes != null ? `${g.routes} routes` : `${g.gateways} gateways`;
}
function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ display: "flex", gap: 8, fontSize: 12 }}>
      <span style={{ color: "var(--color-text-tertiary)", width: 72 }}>{label}</span>
      <span style={{ fontWeight: 500 }}>{children}</span>
    </div>
  );
}

function Usage({ frac }: { frac: number | null }) {
  if (frac == null) return <span style={{ color: "var(--color-text-tertiary)" }}>—</span>;
  const pct = Math.max(0, Math.round(frac * 100));
  const color = pct >= 90 ? "var(--color-text-danger)" : pct >= 75 ? "var(--color-text-warning)" : "var(--color-text-success)";
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
      <span>{pct}%</span>
      <span style={{ width: 80, height: 4, background: "var(--color-background-secondary)", borderRadius: 2, overflow: "hidden" }}>
        <span style={{ display: "block", height: "100%", width: `${Math.min(100, Math.max(0, pct))}%`, background: color }} />
      </span>
    </span>
  );
}

const quietBtn: React.CSSProperties = {
  fontSize: 10,
  padding: "2px 8px",
  borderRadius: 4,
  cursor: "pointer",
  border: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-primary)",
  color: "var(--color-text-secondary)",
};

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
