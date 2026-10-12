import { useEffect, useState } from "react";
import { useFleet, podsText } from "../store/fleet";
import type { ClusterDTO, EventRowDTO, FleetBoardEntry, MetricsDTO, ResourceRef } from "../store/fleet";
import { getClusterMetrics, getClusterSparklines } from "../bridge/metrics";
import type { SparklinesDTO } from "../bridge/metrics";
import { fetchFleetBoard } from "../bridge/fleetboard";
import { Sparkline } from "../chrome/Sparkline";
import { UpdatedAt } from "../chrome/Age";
import { listInstancePage } from "../bridge/crd";
import { fetchEventsSnapshot } from "../bridge/events";
import { CLUSTER_RISK_REFS, riskFor } from "./resourceRisk";
import { ellipsis } from "../chrome/primitives";
import { LensLayout } from "../layout/LensLayout";
import { NAV_GROUPS, navigate } from "../nav/registry";
import { useHealth } from "../store/health";
import { lineText, mastReading, provenance, type Area, type ClusterAttention, type Line, type NotCovered } from "../nav/clusterAttention";
import { useAttentionModel } from "../nav/useAttentionModel";
import { reveal, selectsOnReveal } from "../nav/reveal";

type BoardEntry = FleetBoardEntry | undefined;

// Overview on the workspace template (redesign step 7e). Its attention
// section reads the cluster's attention model (health 1c slice 4) - the same
// reading the navigation tree and the mast use - so the three never disagree
// and nothing here is a second ledger with its own fetch. Nothing is green:
// a quiet area is a neutral fact scoped to what was read.
export function Overview({ c }: { c: ClusterDTO }) {
  const tags = [c.env, c.region, c.provider, c.group].filter(Boolean);
  const metrics = useFleet((s) => s.metrics);
  const loading = useFleet((s) => s.metrics.loading);
  const model = useAttentionModel(c.name);
  // the current incarnation's summary: the namespace count and its time
  // (the store keeps only that one - a newer incarnation and a Final drop it)
  const summary = useHealth((s) => s.summaries[c.name]);
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
  const stateTone = c.state === "Failed" ? "var(--color-text-danger)" : c.state === "Synced" ? "var(--color-text-secondary)" : "var(--color-text-warning)";
  // the attention is pushed (the summary and lens rows); a refresh re-reads
  // what is still fetched here
  const refresh = () => {
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
          <UpdatedAt at={summary ? Date.parse(summary.evaluatedAt) : null} />
          <button className="l-btn" onClick={refresh}>Refresh</button>
        </>
      }
      notice={c.reason ? (
        <div className="t-body" role="status" style={{ color: stateTone, overflowWrap: "anywhere" }}>{c.state === "Failed" ? "▲ " : ""}{c.reason}</div>
      ) : undefined}
      state="ready"
      body={
        <div className="lens-canvas" data-testid="overview">
          <AttentionSection cluster={c.name} m={model} />
          <div className="ov-columns">
            <section aria-label="Cluster facts" className="ov-section">
              <h3 className="t-section-label">Cluster</h3>
              <div className="insp-kv">
                <span>connection</span><span style={{ color: stateTone }}>{freshnessText(c)}</span>
                <span>nodes</span><span>{c.nodesReady}/{c.nodesTotal} ready</span>
                <span>pods</span><span>{podsText(c)}</span>
                <span>namespaces</span><span>{summary?.namespaces != null ? summary.namespaces : "—"}</span>
                <span>flux</span><span>{fluxFact(c, model)}</span>
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

// Each area links to its task's lens, with that lens's attention filter
// where it has one.
function goArea(cluster: string, a: Area) {
  const nav = useFleet.getState();
  switch (a.id) {
    case "workloads": nav.setSection("workloads"); nav.setWorkloadsNeedsAttention(true); return;
    case "pods": nav.setSection("pods"); nav.setPodsNeedsAttention(true); return;
    case "events": nav.setSection("events"); nav.setWarningsOnly(true); return;
  }
  goDestination(cluster, a.destinationId);
}

function goDestination(cluster: string, destinationId: string) {
  const d = NAV_GROUPS.flatMap((g) => g.destinations).find((x) => x.id === destinationId);
  if (d) navigate(cluster, d);
}

// A Top ref's link (slice 4 §5.4): the object's name when its lens selects
// it, else the part's view, said as such.
function TopLink({ cluster, l, r }: { cluster: string; l: Line; r: Line["top"][number] }) {
  const selects = selectsOnReveal(l.task, l.part);
  const name = `${r.namespace ? `${r.namespace}/` : ""}${r.name}`;
  return (
    <button type="button" className="ov-top" data-testid="ov-top"
      onClick={() => reveal(cluster, l.task, l.part, { gvr: r.gvr, namespace: r.namespace, name: r.name, uid: r.uid, parent: r.parent })}
      title={`${selects ? `Open ${name}` : `Open ${l.label}`}${r.label ? ` - ${r.label}` : ""}${r.parent ? ` (via ${r.parent})` : ""}`}>
      {selects ? name : `open ${l.label}`}{r.label ? <span className="t-secondary"> {r.label}</span> : null}
    </button>
  );
}

function AttentionSection({ cluster, m }: { cluster: string; m: ClusterAttention | null }) {
  if (!m) return null;
  const r = mastReading(m);
  const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  return (
    <section aria-label="Attention" className="ov-section">
      <h3 className="t-section-label">Attention</h3>
      {m.ended ? (
        <span className="t-secondary" data-testid="ov-ended">Observation ended at {clock(m.ended)} - nothing here is current.</span>
      ) : r.state === "checking" ? <span className="t-secondary">Reading {cluster}…</span> : (
        <div className="ov-areas">
          {m.areas.map((a) => (
            <div key={a.id} className="ov-area" data-state={a.state} data-testid={`ov-area-${a.id}`}>
              <button type="button" className="ov-area-label" onClick={() => goArea(cluster, a)} title={`Open ${a.label}`}>{a.label}</button>
              {a.controller && (
                <span className="ov-area-text" style={{ color: a.controller.state === "not ready" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
                  {a.controller.state === "not ready" ? "▲ controller not ready" : "controller health unknown"}{a.controller.reason ? ` - ${a.controller.reason}` : ""}
                </span>
              )}
              {a.lines.map((l) => (
                <span key={l.part} className="ov-area-text" title={provenance(a, l)}
                  style={{ color: l.state === "attention" ? (l.tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)") : l.state === "unreadable" || l.gaps.length > 0 ? "var(--color-text-warning)" : undefined }}>
                  {a.lines.length > 1 ? `${l.label}: ` : ""}{l.state === "attention" && l.tone === "danger" ? "▲ " : ""}{lineText(l)}
                  {l.state === "attention" && l.top.length > 0 && (
                    <span className="ov-tops">{l.top.slice(0, 3).map((t) => <TopLink key={`${t.gvr}/${t.namespace ?? ""}/${t.name}`} cluster={cluster} l={l} r={t} />)}</span>
                  )}
                </span>
              ))}
            </div>
          ))}
          {/* Expected parts the summary leaves out are named, so no quiet
              line implies they were checked (fail-closed until slice 6). */}
          {m.notCovered.map((u: NotCovered) => (
            <button key={`${u.task}/${u.part ?? ""}`} className="ov-area" data-state="uncovered" onClick={() => goDestination(cluster, u.destinationId)} title={`Open ${u.label}`}>
              <span className="ov-area-label">{u.label}</span>
              <span className="ov-area-text">{u.capability === "present" ? "not covered by the summary - open to check" : "capability not yet known - not covered by the summary"}</span>
            </button>
          ))}
        </div>
      )}
      {!m.ended && r.state === "quiet" && (
        <span className="t-secondary">
          No attention in the {r.read} area{r.read === 1 ? "" : "s"} read{r.gaps > 0 ? `; ${r.gaps} with gaps` : ""}{r.notCovered > 0 ? `; ${r.notCovered} not covered by the summary` : ""}.
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

// fluxFact: the Flux reconciliations line of the attention model.
function fluxFact(c: ClusterDTO, m: ClusterAttention | null): string {
  const l = m?.areas.find((a) => a.id === "flux")?.lines.find((x) => x.part === "reconciliations");
  if (l) return lineText(l);
  if (m?.notCovered.some((u) => u.task === "flux")) return "installed · not covered by the summary";
  return c.fluxPresent ? "checking…" : "not detected";
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
