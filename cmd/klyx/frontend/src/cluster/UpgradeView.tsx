import { useEffect, useMemo, useRef, useState } from "react";
import { IconArrowUpCircle } from "@tabler/icons-react";
import type { DeprecatedUseDTO, SkewFindingDTO, UpgradeReportDTO } from "../store/fleet";
import { useFleet } from "../store/fleet";
import { getUpgradeReadiness } from "../bridge/upgrade";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt } from "../chrome/Age";
import { useListKeys } from "../chrome/useListKeys";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { useClusterKey } from "./useClusterKey";

// UpgradeView - "can I upgrade this cluster today?" on the workspace template
// (spec §6 step 5): control-plane version + upstream EOL runway, deprecated
// APIs the cluster still SERVES framed against a selectable target minor, and
// kubelet skew. A read-only report snapshot with Refresh, never live.
//
// Honesty rules: object counts are "reachable via this API" (the server
// converts on read); EOL dates are UPSTREAM (provider windows differ, paid LTS
// extends them); no findings means "clean against Klyx's curated table", not a
// guarantee, and never "clean" at all when an input could not be read. A
// removal at the target is a planning blocker, not a live fault: it gets the
// danger shape (▲) and words, never a lamp.

type View = "apis" | "skew";
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const apiKey = (d: DeprecatedUseDTO) => `${d.group}/${d.version}/${d.resource}`;

export function daysUntil(dateISO: string, nowMs = Date.now()): number | null {
  if (!dateISO) return null;
  const t = Date.parse(dateISO + "T00:00:00Z");
  if (Number.isNaN(t)) return null;
  return Math.floor((t - nowMs) / 86400000);
}

const API_COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "api", label: "API", width: 260, priority: 0, flex: true },
  { id: "removal", label: "Removal", width: 170, priority: 0 },
  { id: "objects", label: "Objects", width: 80, priority: 1 },
  { id: "replacement", label: "Migrate to", width: 200, priority: 2, grow: 0.6 },
];
const SKEW_COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "node", label: "Node", width: 240, priority: 0, flex: true },
  { id: "version", label: "Kubelet", width: 110, priority: 0 },
  { id: "detail", label: "Finding", width: 320, priority: 1, grow: 1 },
];

function objectsText(d: DeprecatedUseDTO): string {
  if (!d.countKnown) return "?";
  return `${d.count}${d.countCapped ? "+" : ""}`;
}

export function UpgradeView({ cluster }: { cluster: string }) {
  const [pinned, setPinned] = useState<{ cluster: string; report: UpgradeReportDTO; at: number } | null>(null);
  const [loading, setLoading] = useState(false);
  const [target, setTarget] = useState<number | null>(null);
  const [view, setView] = useState<View>("apis");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const gen = useRef(0);

  // A report answers one cluster at one time: a late report for another
  // cluster or an older request is dropped; a refresh keeps the previous
  // report visible until the new one lands.
  const fetchReport = async () => {
    const g = ++gen.current;
    const origin = cluster;
    setLoading(true);
    const r = await getUpgradeReadiness(origin);
    if (g !== gen.current) return;
    setLoading(false);
    setPinned({ cluster: origin, report: r, at: Date.now() });
    // keep the chosen target only while it is still one of +1..+3
    if (r.serverMinor > 0) setTarget((t) => (t !== null && t > r.serverMinor && t <= r.serverMinor + 3 ? t : r.serverMinor + 1));
  };
  useEffect(() => {
    setTarget(null); setSelKey(null); setCursorKey(null); setPinned(null);
    void fetchReport();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster]);

  const report = pinned && pinned.cluster === cluster ? pinned.report : null;
  const ok = report && !report.error ? report : null;
  const targetMinor = target ?? (ok && ok.serverMinor > 0 ? ok.serverMinor + 1 : 0);
  const eolDays = ok ? daysUntil(ok.eolDate) : null;
  const warnings = ok?.warnings ?? [];
  const partial = warnings.length > 0;

  const apis = useMemo(() => {
    const list = ok?.deprecated ?? [];
    const gone = (d: DeprecatedUseDTO) => d.removedInMinor > 0 && targetMinor >= d.removedInMinor;
    return [...list].sort((a, b) => Number(gone(b)) - Number(gone(a)) || (a.removedInMinor || 999) - (b.removedInMinor || 999) || apiKey(a).localeCompare(apiKey(b)));
  }, [ok, targetMinor]);
  const removedAtTarget = apis.filter((d) => d.removedInMinor > 0 && targetMinor >= d.removedInMinor);
  const skew = useMemo(() => [...(ok?.skew ?? [])].sort((a, b) => Number(b.level === "danger") - Number(a.level === "danger") || a.node.localeCompare(b.node)), [ok]);
  const skewDanger = skew.filter((s) => s.level === "danger").length;
  const skewWarn = skew.length - skewDanger;

  const rows: { key: string }[] = view === "apis" ? apis.map((d) => ({ key: apiKey(d) })) : skew.map((s) => ({ key: s.node }));
  const selectedApi = view === "apis" && selKey ? apis.find((d) => apiKey(d) === selKey) : undefined;
  const selectedSkew = view === "skew" && selKey ? skew.find((s) => s.node === selKey) : undefined;
  const cursorIdx = cursorKey === null ? -1 : rows.findIndex((r) => r.key === cursorKey);
  useListKeys({
    count: rows.length,
    selected: cursorIdx,
    onSelect: (idx) => { const r = rows[idx]; if (r) setCursorKey(r.key); },
    onActivate: (idx) => { const r = rows[idx]; if (r) { setCursorKey(r.key); setSelKey(r.key); } },
    onEscape: () => setSelKey(null),
  });

  const columns = view === "apis" ? API_COLUMNS : SKEW_COLUMNS;
  const fitted = useMemo(() => fitColumns(columns, bodyWidth), [columns, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  let state: DatasetState = "ready";
  if (!report) state = "loading";
  else if (report.error) state = "unreadable";
  else if (rows.length === 0) state = "empty";

  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading cluster version, node skew and served APIs" />
    : state === "unreadable" ? <EmptyState icon={<IconArrowUpCircle size={28} stroke={1.2} />} title="Could not read upgrade readiness." hint={report?.error} action={{ label: "Retry", onClick: () => void fetchReport() }} />
    : view === "apis"
      ? <EmptyState icon={<IconArrowUpCircle size={28} stroke={1.2} />} title={partial ? "No deprecated API found in what could be read." : "No deprecated API from Klyx's curated table is served."} hint={partial ? "Part of the report could not be read (see above) - this is not a clean bill." : "Clean against the curated table for any target - not a guarantee; check your provider's upgrade notes."} />
      : <EmptyState icon={<IconArrowUpCircle size={28} stroke={1.2} />} title={partial ? "No kubelet skew found in what could be read." : "No kubelet skew findings."} hint={partial ? "Part of the report could not be read (see above)." : "Every node's kubelet is within the supported skew of the CURRENT control plane. Skew at the target is not evaluated - upgrade nodes after the control plane."} />;

  const switchView = (v: View) => { setView(v); setSelKey(null); setCursorKey(null); };

  return (
    <LensLayout
      label="Upgrade"
      title="Upgrade check"
      summary={!ok ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{ok.serverVersion || "?"}</span>
          {eolDays !== null && (
            <span className={eolDays < 60 ? "t-status" : "t-secondary"} style={{ color: eolDays < 0 ? "var(--color-text-danger)" : eolDays < 60 ? "var(--color-text-warning)" : undefined }}>
              {eolDays < 0 ? `▲ upstream EOL ${-eolDays}d ago` : `upstream EOL in ${eolDays}d`}
            </span>
          )}
          {removedAtTarget.length > 0 && <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {removedAtTarget.length} API{removedAtTarget.length === 1 ? "" : "s"} removed by 1.{targetMinor}</span>}
          {removedAtTarget.length === 0 && <span className="t-secondary">{partial ? `no API removal found for 1.${targetMinor} in what could be read` : `no API removal blocker for 1.${targetMinor} (curated table)`}</span>}
          {/* skew is measured against the CURRENT control plane, never the
              target: it is a prerequisite, not target readiness */}
          {skewDanger > 0 && <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {skewDanger} kubelet skew violation{skewDanger === 1 ? "" : "s"} now</span>}
          {skewWarn > 0 && <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{skewWarn} node{skewWarn === 1 ? "" : "s"} to upgrade before the next control-plane bump</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary" title="Upstream dates; AKS/EKS/GKE windows differ and paid LTS extends them.">report</span>
          <UpdatedAt at={pinned && pinned.cluster === cluster ? pinned.at : null} />
          <button className="l-btn" onClick={() => void fetchReport()} disabled={loading}>{loading ? "Reading…" : "Refresh"}</button>
        </>
      }
      controls={
        <>
          <div role="group" aria-label="Upgrade view" style={{ display: "inline-flex", gap: 4 }}>
            <button className="l-chip" aria-pressed={view === "apis"} onClick={() => switchView("apis")}>Deprecated APIs {ok && <span style={{ color: removedAtTarget.length > 0 ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>{apis.length}</span>}</button>
            <button className="l-chip" aria-pressed={view === "skew"} onClick={() => switchView("skew")}>Node skew (current control plane) {ok && <span style={{ color: skewDanger > 0 ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>{skew.length}</span>}</button>
          </div>
          {ok && ok.serverMinor > 0 && (
            <label style={{ display: "flex", alignItems: "center", gap: 6, marginLeft: "auto" }}>
              <span className="t-secondary">Target</span>
              <select className="l-input" value={targetMinor} onChange={(e) => setTarget(Number(e.target.value))} aria-label="target minor">
                {[1, 2, 3].map((step) => <option key={step} value={ok.serverMinor + step}>1.{ok.serverMinor + step}</option>)}
              </select>
            </label>
          )}
        </>
      }
      notice={partial ? (
        <div data-testid="upgrade-warnings" role="status" className="t-body" style={{ color: "var(--color-text-warning)", display: "grid", gap: 2, padding: "4px 0" }}>
          {warnings.map((w) => <span key={w} style={{ overflowWrap: "anywhere" }}>Partial report - {w}. An empty section below is not a clean result.</span>)}
        </div>
      ) : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {columns.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <div style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
            {view === "apis" ? apis.map((d) => {
              const k = apiKey(d);
              const gone = d.removedInMinor > 0 && targetMinor >= d.removedInMinor;
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" style={{ ...gridStyle, height: TABLE_ROW_HEIGHT }}
                  aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
                  onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(k); } }}>
                  {show("mark") && <VerdictMark health={gone ? { kind: "fault", label: `removed in 1.${d.removedInMinor}` } : { kind: "degraded", label: d.removedInMinor === 0 ? "deprecated, removal unscheduled" : `removed in 1.${d.removedInMinor}, after the target` }} />}
                  {show("api") && <span className="lens-cell-name" title={`${d.group}/${d.version} ${d.kind}`}><span style={{ color: "var(--color-text-tertiary)" }}>{d.group}/{d.version} </span>{d.kind}</span>}
                  {show("removal") && <span style={{ ...cellStyle, color: gone ? "var(--color-text-danger)" : "var(--color-text-secondary)", fontWeight: gone ? 600 : 400 }}>{d.removedInMinor === 0 ? "unscheduled" : `removed in 1.${d.removedInMinor}`}</span>}
                  {show("objects") && <span style={{ color: "var(--color-text-secondary)" }} title={d.countKnown ? undefined : "count not readable"}>{objectsText(d)}</span>}
                  {show("replacement") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{d.replacedBy || "-"}</span>}
                </div>
              );
            }) : skew.map((s) => {
              const danger = s.level === "danger";
              return (
                <div key={s.node} role="button" tabIndex={0} className="lens-row" style={{ ...gridStyle, height: TABLE_ROW_HEIGHT }}
                  aria-selected={s.node === cursorKey} data-cursor={s.node === cursorKey ? "true" : undefined} data-selected={s.node === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(s.node); setSelKey(s.node === selKey ? null : s.node); }}
                  onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(s.node); } }}>
                  {show("mark") && <VerdictMark health={danger ? { kind: "fault", label: "Skew violation" } : { kind: "degraded", label: "Skew warning" }} />}
                  {show("node") && <span className="lens-cell-name">{s.node}</span>}
                  {show("version") && <span style={{ color: danger ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{s.version}</span>}
                  {show("detail") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{s.detail}</span>}
                </div>
              );
            })}
          </div>
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect</span>}
      footerEnd={<span className="t-secondary">{view === "apis" ? "removed at target first" : "against the current control plane, not the target · violations first"}</span>}
      inspector={selectedApi ? <ApiInspector cluster={cluster} d={selectedApi} targetMinor={targetMinor} onClose={() => setSelKey(null)} />
        : selectedSkew ? <SkewInspector cluster={cluster} s={selectedSkew} serverVersion={ok?.serverVersion ?? ""} onClose={() => setSelKey(null)} />
        : undefined}
      inspectorLabel={view === "apis" ? "Deprecated API details" : "Node skew details"}
      onCloseInspector={() => setSelKey(null)}
    />
  );
}

function ApiInspector({ cluster, d, targetMinor, onClose }: { cluster: string; d: DeprecatedUseDTO; targetMinor: number; onClose: () => void }) {
  const gone = d.removedInMinor > 0 && targetMinor >= d.removedInMinor;
  return (
    <div data-testid="upgrade-api-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster }} kind="Deprecated API" name={`${d.kind} ${d.group}/${d.version}`} meta={`resource ${d.resource}`} onClose={onClose}
        verdict={
          <div className="t-body" style={{ display: "grid", gap: 4 }}>
            <span className="t-status" style={{ color: gone ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
              {d.removedInMinor === 0 ? "Deprecated, removal unscheduled" : gone ? `▲ Removed in 1.${d.removedInMinor} - at or before the 1.${targetMinor} target` : `Removed in 1.${d.removedInMinor} - after the 1.${targetMinor} target`}
            </span>
            {gone && <span>Clients and manifests still requesting {d.group}/{d.version} break when the cluster reaches 1.{d.removedInMinor}.</span>}
          </div>
        }>
        <InspectorBlock title="Migration">
          <div className="insp-kv">
            <span>migrate to</span><span style={{ overflowWrap: "anywhere" }}>{d.replacedBy || "no replacement named"}</span>
            <span>objects</span><span>{d.countKnown ? `${d.count}${d.countCapped ? "+ (count capped)" : ""}` : "unknown - the count could not be read"}</span>
          </div>
          <div className="t-secondary" style={{ marginTop: 8 }}>Counts are objects reachable via the deprecated API: the server converts on read, so it cannot tell which version a client wrote. The risk is the clients and manifests (Helm charts, Kustomize overlays, controllers) that still request this version.</div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}

function SkewInspector({ cluster, s, serverVersion, onClose }: { cluster: string; s: SkewFindingDTO; serverVersion: string; onClose: () => void }) {
  const danger = s.level === "danger";
  const openNode = () => { const nav = useFleet.getState(); nav.setSection("nodes"); nav.requestNavIntent({ kind: "select-node", cluster, node: s.node }); };
  return (
    <div data-testid="upgrade-skew-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster }} kind="Node" name={s.node} meta={`kubelet ${s.version} · control plane ${serverVersion || "?"}`} onClose={onClose}
        verdict={<div className="t-body" style={{ color: danger ? "var(--color-text-danger)" : "var(--color-text-warning)", overflowWrap: "anywhere" }}><span className="t-status">{danger ? "▲ Skew violation" : "Skew warning"}</span> - {s.detail}</div>}
        primary={{ label: "Open node", onSelect: openNode }}>
        <InspectorBlock title="Versions">
          <div className="insp-kv">
            <span>kubelet</span><span>{s.version}</span>
            <span>control plane</span><span>{serverVersion || "?"}</span>
            <span>minor delta</span><span>{s.delta}</span>
          </div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}
