import { IconChevronRight } from "@tabler/icons-react";
import { placeName } from "../line/place";
import { useEffect, useState } from "react";
import { useFleet, podsText } from "../store/fleet";
import type { ClusterDTO, EventRowDTO, MetricsDTO, ResourceRef } from "../store/fleet";
import { getClusterMetrics, getClusterSparklines } from "../bridge/metrics";
import type { SparklinesDTO } from "../bridge/metrics";
import { Sparkline } from "../chrome/Sparkline";
import { UpdatedAt } from "../chrome/Age";
import { listInstancePage } from "../bridge/crd";
import { fetchEventsSnapshot } from "../bridge/events";
import { CLUSTER_RISK_REFS, riskFor } from "./resourceRisk";
import { Aspect, ellipsis } from "../chrome/primitives";
import { LensLayout } from "../layout/LensLayout";
import { NAV_GROUPS, navigate } from "../nav/registry";
import { useHealth } from "../store/health";
import { checkingText, lineText, mastReading, provenance, type Area, type ClusterAttention, type Line } from "../nav/clusterAttention";
import { useAttentionModel } from "../nav/useAttentionModel";
import { reveal, selectsOnReveal } from "../nav/reveal";
import { StationVerdict } from "./StationVerdict";
import { clusterDisplayName } from "../fleet/clusterName";
import { NeedsYou } from "../fleet/NeedsYou";
import { ChartSource, SourceLine } from "./SourceLine";


// Overview on the workspace template (redesign step 7e). Its attention
// section reads the cluster's attention model (health 1c slice 4) - the same
// reading the navigation tree and the mast use - so the three never disagree
// and nothing here is a second ledger with its own fetch. Nothing is green:
// a quiet area is a neutral fact scoped to what was read.
export function Overview({ c }: { c: ClusterDTO }) {
  // what the station is; where it stands on the line is the verdict's
  // second line (The Line slice 11c)
  const where = [c.provider, c.region ? placeName(c.region) : "", c.group].filter(Boolean).join(" · ");
  const metrics = useFleet((s) => s.metrics);
  const loading = useFleet((s) => s.metrics.loading);
  const model = useAttentionModel(c.name);
  // the current incarnation's summary: the namespace count and its time
  // (the store keeps only that one - a newer incarnation and a Final drop it)
  const summary = useHealth((s) => s.summaries[c.name]);
  // 30m cluster utilisation sparklines — fetched once per mount, local state.
  const [spark, setSpark] = useState<SparklinesDTO | null>(null);

  useEffect(() => {
    getClusterMetrics(c.name, false);
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
  // what is still fetched here - the metrics
  const refresh = () => {
    getClusterMetrics(c.name, true);
  };

  return (
    <LensLayout
      label="Overview"
      // The station page's title is the station (slice 11 ruling 5); the
      // destination stays Overview. Its state leads the verdict below.
      title={clusterDisplayName(c.name)}
      summary={
        <>
          {c.version && <span className="t-secondary">{c.version}</span>}
          {where && <span className="t-secondary">{where}</span>}
        </>
      }
      headerEnd={
        <>
          <UpdatedAt at={summary ? Date.parse(summary.evaluatedAt) : null} />
          <button className="l-btn" onClick={refresh}>Refresh</button>
        </>
      }
      state="ready"
      body={
        // The station page (The Line slice 11c): the station's verdict and
        // its "Needs you" rows, then the Overview's sections in the parent's
        // order - utilisation, monitoring, warning events, risks, the areas,
        // and the connection's notes last.
        <div className="ov-page" data-testid="overview">
          <StationVerdict c={c} m={model} />
          <NeedsYou station clusters={[c]} models={model ? { [c.name]: model } : {}} />
          <div className="ov-columns">
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
              <MonitoringLine dto={m} loading={loading} stale={metrics.cluster === c.name && !!metrics.stale} onRefresh={() => getClusterMetrics(c.name, true)} />
              {/* the charts' own read: another source than the readout's now, or no data under its scope (Settings S3b §3.2, §3.4) */}
              <ChartSource spark={spark} readout={m?.available ? m : null} testid="sparkline-source" />
            </section>
            <section aria-label="Cluster facts" className="ov-section">
              <h3 className="t-section-label">Cluster</h3>
              <div className="insp-kv">
                <span>connection</span><span style={{ color: stateTone }}>{freshnessText(c)}</span>
                <span>nodes</span><span>{c.nodesReady}/{c.nodesTotal} ready</span>
                <span>pods</span><span>{podsText(c)}</span>
                <span>namespaces</span><span>{summary?.namespaces != null ? summary.namespaces : "—"}</span>
                <span>flux</span><ToolFact m={model} task="flux" />
                <span>argo cd</span><ToolFact m={model} task="argo" />
                <span>gateway</span><ToolFact m={model} task="gateway" />
              </div>
            </section>
          </div>
          <CriticalEvents cluster={c.name} />
          <ClusterRisks cluster={c.name} />
          <AttentionSection cluster={c.name} m={model} />
          {c.reason && (
            <section aria-label="Connection" className="ov-section" data-testid="ov-connection">
              <h3 className="t-section-label">Connection</h3>
              <ConnectionNotice c={c} m={model} tone={stateTone} />
            </section>
          )}
        </div>
      }
    />
  );
}

// ConnectionNotice: a connection that is not merely "connected with notes"
// (failed, stale, connecting) shows its reason in full. A connection with
// notes shows one line: its first note that is NOT a controller note, or -
// when every note is a controller's - the controllers named; the full notes
// are one click away (UI review 2026-10-13: the same long controller
// sentences were repeated above the grid and inside it). A controller
// summary never stands in for a different problem (Codex review of da40ac6).
function ConnectionNotice({ c, m, tone }: { c: ClusterDTO; m: ClusterAttention | null; tone: string }) {
  if (c.state !== "Degraded") {
    return <div className="t-body ov-marked" role="status" style={{ color: tone, overflowWrap: "anywhere" }}>{c.state === "Failed" && <Aspect kind="fault" size={8} />}<span>{c.reason}</span></div>;
  }
  const notes = c.reason.split(/;\s+/).map((n) => n.trim()).filter(Boolean);
  const areas = m?.areas.filter((a) => a.controller) ?? [];
  const controllerReasons = new Set(areas.map((a) => a.controller?.reason).filter(Boolean));
  const isControllerNote = (n: string) => controllerReasons.has(n) || [...controllerReasons].some((r) => !!r && (n.includes(r) || r.includes(n)));
  const other = notes.filter((n) => !isControllerNote(n));
  const named = areas.filter((a) => notes.some((n) => isControllerNote(n) && (!a.controller?.reason || n.includes(a.controller.reason) || a.controller.reason.includes(n))));
  const notReady = named.filter((a) => a.controller?.state === "not ready").map((a) => a.label);
  const unverified = named.filter((a) => a.controller?.state === "unknown").map((a) => a.label);
  const controllerLine = [notReady.length > 0 ? `Controller not ready: ${notReady.join(", ")}` : "", unverified.length > 0 ? `Controller health unverified: ${unverified.join(", ")}` : ""].filter(Boolean).join(" · ");
  const headline = other.length > 0 ? other[0] : controllerLine || notes[0];
  // the summary names a controller that is not ready: its fault mark, where
  // the retired ▲ stood (13d review). An unverified-only summary stays
  // unmarked, and a different problem that leads carries none.
  const faultLeads = other.length === 0 && notReady.length > 0;
  if (notes.length <= 1 && other.length === notes.length) {
    return <div className="t-body" role="status" style={{ color: tone, overflowWrap: "anywhere" }}>{c.reason}</div>;
  }
  return (
    <details className="ov-notice" role="status" style={{ color: tone }}>
      <summary className="t-body">{faultLeads && <span className="ov-notice-mark"><Aspect kind="fault" size={8} /></span>}{headline}<span className="t-secondary"> · {notes.length === 1 ? "details" : `${notes.length} notes`}</span><Chevron /></summary>
      <ul className="ov-notice-notes t-secondary">{notes.map((n) => <li key={n}>{n}</li>)}</ul>
    </details>
  );
}

// controllerWords: a controller's state in an area's one line.
function controllerWords(state: string) {
  if (state === "not ready") return <><Aspect kind="fault" size={8} /> controller not ready</>;
  if (state === "managed") return "controller managed by the platform";
  return "controller health unverified";
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
      <span className="ov-top-name">{selects ? name : `open ${l.label}`}</span>{r.label ? <span className="ov-top-label"> {r.label}</span> : null}
    </button>
  );
}

// A disclosure's chevron sits after its words, as InspectorBlock's does; it
// turns when the details open (slice 13d.4: no native ▶ marker).
function Chevron() {
  return <IconChevronRight className="ov-chev" size={12} stroke={1.8} aria-hidden="true" />;
}

function AttentionSection({ cluster, m }: { cluster: string; m: ClusterAttention | null }) {
  if (!m) return null;
  const r = mastReading(m);
  const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  return (
    <section aria-label="By area" className="ov-section">
      <h3 className="t-section-label">By area</h3>
      {m.ended ? (
        <span className="t-secondary" data-testid="ov-ended">Observation ended at {clock(m.ended)} - nothing here is current.</span>
      ) : r.state === "checking" ? <span className="t-secondary">Reading {cluster}…</span> : (
        <div className="ov-areas">
          {m.areas.map((a) => (
            <div key={a.id} className="ov-area" data-state={a.state} data-testid={`ov-area-${a.id}`}>
              <button type="button" className="ov-area-label t-section-label" onClick={() => goArea(cluster, a)} title={`Open ${a.label}`}>{a.label}</button>
              {a.controller && (
                // One line; the controller's reason is a click away, not a
                // paragraph repeated in every area. An unverified controller
                // is a gap, said quietly; one that is not ready is a fault;
                // a managed one (S10 §5) is a note, neither.
                <span className="ov-area-text ov-ctl" data-tone={a.controller.state === "not ready" ? "danger" : a.controller.state === "managed" ? undefined : "gap"}
                  data-testid={`ov-ctl-${a.id}`}>
                  {a.controller.reason ? (
                    <details className="ov-why">
                      <summary>{controllerWords(a.controller.state)}<Chevron /></summary>
                      <span className="t-secondary">{a.controller.reason}</span>
                    </details>
                  ) : controllerWords(a.controller.state)}
                </span>
              )}
              {a.lines.map((l) => (
                <span key={l.part} className="ov-area-text" title={provenance(a, l)}
                  data-tone={l.state === "attention" ? (l.tone === "danger" ? "danger" : "warning") : l.state === "unreadable" || l.gaps.length > 0 ? "warning" : undefined}>
                  {a.lines.length > 1 && <span className="ov-line-label">{`${l.label}: `}</span>}
                  <span className="ov-line-words">{l.state === "attention" && <><Aspect kind={l.tone === "danger" ? "fault" : "attention"} size={8} />{" "}</>}{lineText(l)}</span>
                  {l.state === "attention" && l.top.length > 0 && (
                    <span className="ov-tops">{l.top.slice(0, 3).map((t) => <TopLink key={`${t.gvr}/${t.namespace ?? ""}/${t.name}`} cluster={cluster} l={l} r={t} />)}</span>
                  )}
                </span>
              ))}
            </div>
          ))}
        </div>
      )}
      {!m.ended && r.state === "quiet" && (
        <span className="t-secondary">
          No attention in the {r.read} area{r.read === 1 ? "" : "s"} read{r.gaps > 0 ? `; ${r.gaps} unverified` : ""}.
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
          className="ov-row ov-event"
          onClick={() => openEvents(event)}
          title={`${event.namespace} ${event.kind}/${event.name}: ${event.message}`}
        >
          <span className="ov-num">{event.count > 1 ? `×${event.count}` : "warning"}</span>
          <span style={{ ...ellipsis, color: "var(--color-text-warning)" }}>{event.reason}</span>
          <span className="ov-mono" style={{ ...ellipsis, color: "var(--color-text-tertiary)" }}>{event.namespace}/{event.name}</span>
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
          className="ov-row ov-risk"
          onClick={() => openRisk(r.ref)}
          title={`${r.ref.kind}: ${r.count} - ${r.reason} - ${r.sample}`}
        >
          <span style={{ ...ellipsis, color: "var(--color-text-secondary)" }}>{r.ref.kind}</span>
          <span className="ov-num" style={{ color: "var(--color-text-primary)" }}>{r.count}</span>
          <span style={{ ...ellipsis }}>{r.reason}</span>
          <span className="ov-mono" style={{ minWidth: 0, color: "var(--color-text-tertiary)", ...ellipsis }}>{r.sample}</span>
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
      <div className="ov-section-body">{children}</div>
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

// toolFact: a tool's reading from the attention model (health 1c slice 5
// §5), lifecycle first: an ended observation, then its lines in its own
// vocabulary, "checking…" while nothing has answered (qualified while
// capability detection has not established the tool - unknown is never
// "installed"), and "not detected" only when the model confirms the
// absence - never from a presence flag that may lag.
// Short words in the facts (slice 13d.3): the tool's lines, then its
// controller's state; the controller's reason stays one hover away here and
// in By area and Connection, where it is written out.
function toolFact(m: ClusterAttention | null, task: string): { text: string; title?: string } {
  if (!m) return { text: "checking…" };
  if (m.ended) return { text: `observation ended at ${new Date(m.ended).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })}` };
  const a = m.areas.find((x) => x.id === task);
  if (a) {
    const checking = checkingText(a);
    if (checking) return { text: checking };
    const lines = a.lines.map((l) => `${a.lines.length > 1 ? `${l.label.toLowerCase()} ` : ""}${lineText(l)}`);
    const ctl = a.controller ? [`controller ${a.controller.state === "unknown" ? "unverified" : a.controller.state === "managed" ? "managed by the platform" : a.controller.state}`] : [];
    return { text: [...lines, ...ctl].join(" · "), title: a.controller?.reason ? `controller ${a.controller.state}: ${a.controller.reason}` : undefined };
  }
  return { text: "not detected" };
}

function ToolFact({ m, task }: { m: ClusterAttention | null; task: string }) {
  const f = toolFact(m, task);
  return <span title={f.title}>{f.text}</span>;
}

function Usage({ frac }: { frac: number | null }) {
  if (frac == null) return <span style={{ color: "var(--color-text-tertiary)" }}>—</span>;
  const pct = Math.max(0, Math.round(frac * 100));
  // Used is a measurement: neutral until it is high - never a green "fine".
  const color = pct >= 90 ? "var(--color-text-danger)" : pct >= 75 ? "var(--color-text-warning)" : "var(--color-text-tertiary)";
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
      <span className="ov-marked">{pct >= 90 && <Aspect kind="attention" size={8} />}<span>{pct}%</span></span>
      <span style={{ width: 80, height: 4, background: "var(--color-background-secondary)", borderRadius: 2, overflow: "hidden" }}>
        <span style={{ display: "block", height: "100%", width: `${Math.min(100, Math.max(0, pct))}%`, background: color }} />
      </span>
    </span>
  );
}

function MonitoringLine({ dto, loading, stale, onRefresh }: { dto: MetricsDTO | null; loading: boolean; stale: boolean; onRefresh: () => void }) {
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
    // a subscription names its installation and where it is read (Settings S3b §3.1)
    const where = dto.mode === "explicit-endpoint" ? `endpoint ${dto.source}` : dto.mode === "subscribed" ? dto.source : `svc ${dto.source}`;
    const label = dto.mode === "discovered-service" ? "discovered" : dto.mode === "explicit-service-ref" ? "service" : dto.mode === "subscribed" ? "installation" : "endpoint";
    text = `monitoring: ${label} · ${where}`;
    if (dto.warning) text += ` ⚠︎ ${dto.warning}`;
  }
  return (
    <>
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
    {/* where the readout comes from, and no data under its scope said as such (Settings S3b §3.3, §3.4) */}
    {dto?.available && <SourceLine sentence={dto.sentence} noData={dto.noData} stale={stale} testid="monitoring-source" />}
    </>
  );
}
