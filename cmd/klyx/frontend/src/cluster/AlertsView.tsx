import { Fragment, useMemo, useRef, useState } from "react";
import { IconBellRinging } from "@tabler/icons-react";
import { Age, absoluteTime } from "../chrome/Age";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { useNavIntent } from "../chrome/useNavIntent";
import { Aspect } from "../chrome/primitives";
import { useFleet } from "../store/fleet";
import { useRows } from "../health/useRows";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useClusterKey } from "./useClusterKey";
import { alertsFacts, causeText, groupOf, sevClass, sortAlerts, subjectNote, subjectText, type AMAlertDTO, type AMSubjectDTO, type AlertGroup } from "./alertsModel";

// The Alerts lens (coverage S8a §7): Alertmanager's alerts for this
// cluster, read-only. Active alerts first, then unprocessed, then
// suppressed with their cause. Every string is a display value the backend
// redacted; a subject is a label match, never a UID join. No alert is ever
// called resolved: an alert is listed as of its read, and a failed read
// keeps the last good list.

// The state and its cause are the reading: never dropped at a narrow width.
export const COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "sev", label: "Severity", width: 76, priority: 0 },
  { id: "alert", label: "Alert", width: 200, priority: 0, grow: 0.5 },
  { id: "state", label: "State", width: 180, priority: 0, flex: true },
  { id: "subject", label: "Subject", width: 220, priority: 1, grow: 0.5 },
  { id: "started", label: "Started", width: 72, priority: 2 },
  { id: "summary", label: "Summary", width: 240, priority: 3, grow: 0.6 },
];
const cell: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
type StateFilter = "all" | AlertGroup;
type SevFilter = "all" | "critical" | "warning" | "other";
const unix = (iso?: string) => (iso ? Math.floor(Date.parse(iso) / 1000) : 0);
const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

function SevMark({ a }: { a: AMAlertDTO }) {
  if (a.state !== "active") return <span className="t-secondary">{a.severity || "none"}</span>;
  const sev = sevClass(a);
  return (
    <span style={{ display: "inline-flex", gap: 6, alignItems: "center", color: sev === "critical" ? "var(--color-text-danger)" : sev === "warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>
      {sev !== "other" && <Aspect kind={sev === "critical" ? "fault" : "attention"} />}{a.severity || "none"}
    </span>
  );
}

/** openSubject: where a found subject opens (selection only). */
export function openSubject(cluster: string, s: AMSubjectDTO): (() => void) | null {
  if (s.state !== "found" || !s.name) return null;
  const nav = useFleet.getState();
  switch (s.kind) {
    case "Pod": return () => { nav.setSection("pods"); nav.requestNavIntent({ kind: "select-pod", cluster, namespace: s.namespace ?? "", name: s.name!, uid: s.uid }); };
    case "Node": return () => { nav.setSection("nodes"); nav.requestNavIntent({ kind: "select-node", cluster, node: s.name!, uid: s.uid }); };
    case "Deployment": case "StatefulSet": case "DaemonSet":
      return () => { nav.setSection("workloads"); nav.requestNavIntent({ kind: "select-workload", cluster, workloadKind: s.kind as "Deployment", namespace: s.namespace ?? "", name: s.name!, uid: s.uid }); };
  }
  return null;
}

export function AlertsView({ cluster }: { cluster: string }) {
  const lr = useRows<AMAlertDTO>(cluster, "alerts", "alerts");
  const part = lr.part ?? lr.counts.part;
  const kind = part?.kinds.find((k) => k.kind === "alerts");
  const facts = alertsFacts(lr.facts);
  const counts = part?.alerts;
  const [stateF, setStateF] = useState<StateFilter>("all");
  const [sevF, setSevF] = useState<SevFilter>("all");
  const [search, setSearch] = useState("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  const all = useMemo(() => sortAlerts(lr.rows ?? []), [lr.rows]);
  const byFp = useMemo(() => new Map(all.map((a) => [a.fingerprint, a])), [all]);
  const q = search.trim().toLowerCase();
  const visible = useMemo(() => all.filter((a) => {
    if (stateF !== "all" && groupOf(a) !== stateF) return false;
    if (sevF !== "all" && sevClass(a) !== sevF) return false;
    if (!q) return true;
    return [a.name, a.summary, subjectText(a.subject), a.severity, ...a.labels.map((l) => `${l.name}=${l.value}`)].some((v) => (v ?? "").toLowerCase().includes(q));
  }), [all, stateF, sevF, q]);

  const selected = selKey ? all.find((a) => a.fingerprint === selKey) : undefined;
  const close = () => setSelKey(null);
  const cursorIdx = cursorKey === null ? -1 : visible.findIndex((a) => a.fingerprint === cursorKey);
  useListKeys({
    count: visible.length, selected: cursorIdx,
    onSelect: (i) => { const a = visible[i]; if (a) { setCursorKey(a.fingerprint); listRef.current?.scrollToIndex(i); } },
    onActivate: (i) => { const a = visible[i]; if (a) { setCursorKey(a.fingerprint); setSelKey(a.fingerprint); } },
    onEscape: close, searchRef,
  });
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "alerts") return "wait";
    const fp = intent.ref.fingerprint;
    if (!fp) return "consumed";
    if (!byFp.has(fp)) return lr.rows === null ? "wait" : "missing";
    setStateF("all"); setSevF("all"); setSearch("");
    setCursorKey(fp); setSelKey(fp);
    return "consumed";
  }, [byFp, lr.rows]);

  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };
  const icon = <IconBellRinging size={28} stroke={1.2} />;
  const absent = part?.coverage === "absent";
  const unreadable = kind?.coverage === "unreadable";
  const stale = kind?.coverage === "stale";
  const truncated = facts?.truncated || kind?.truncated;
  const plus = truncated ? "+" : "";
  let state: DatasetState = "ready";
  if (absent) state = "empty";
  else if (lr.rows === null && unreadable) state = "unreadable";
  else if (lr.rows === null || (kind?.coverage === "checking" && all.length === 0)) state = "loading";
  else if (unreadable && all.length === 0) state = "unreadable";
  else if (all.length === 0) state = "empty";
  else if (visible.length === 0) state = "no-match";
  const clearFilters = () => { setStateF("all"); setSevF("all"); setSearch(""); };
  const stateView = state === "loading" ? <SkeletonRows rows={6} label="reading alerts" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not read alerts." hint={kind?.error ?? lr.error ?? undefined} />
    : absent ? <EmptyState icon={icon} title="No Alertmanager is configured or found on this cluster." hint="Set alertmanager in fleet.yaml, or install the Prometheus Operator's Alertmanager. Klyx reads alerts and never manages them." />
    : state === "empty" ? <EmptyState icon={icon} title="No alerts on this Alertmanager for this cluster." hint={facts?.readAt ? `As of ${clock(facts.readAt)}.` : undefined} />
    : <EmptyState icon={icon} title="No alert matches these filters." action={{ label: "Clear filters", onClick: clearFilters }} />;

  const notices: React.ReactNode[] = [];
  if (stale && all.length > 0) notices.push(<WatchErrorBanner key="stale" tool="Alertmanager" failure={`read failed - showing the last good read${kind?.lastGoodAt ? ` from ${clock(kind.lastGoodAt)}` : ""}; nothing here is called resolved`} error={kind?.error ?? ""} />);
  if (truncated) notices.push(<div key="cap" role="status" className="t-secondary" style={{ color: "var(--color-text-warning)" }} data-testid="alerts-capped">The first 5,000 alerts only: every count is at least, and an alert not listed is not established either way.</div>);
  if (facts?.note) notices.push(<div key="note" role="status" className="t-secondary" style={{ color: "var(--color-text-warning)" }}>{facts.note}</div>);

  const matchText = facts?.match && Object.keys(facts.match).length > 0
    ? Object.entries(facts.match).map(([k, v]) => `${k}="${v}"`).join(", ")
    : "every alert on this Alertmanager is read as this cluster's";
  return (
    <LensLayout
      label="Alerts"
      title="Alerts"
      summary={state === "loading" || state === "unreadable" || absent ? null : (
        <span className="t-secondary" data-testid="alerts-summary">
          {counts ? `${counts.counted}${plus} active critical or warning` : `${all.length}${plus} alerts`}
          {counts && counts.uncounted > 0 ? ` · ${counts.uncounted}${plus} active without a counted severity` : ""}
          {counts && counts.unprocessed > 0 ? ` · ${counts.unprocessed}${plus} unprocessed` : ""}
          {counts && counts.suppressed > 0 ? ` · ${counts.suppressed}${plus} suppressed` : ""}
        </span>
      )}
      headerEnd={facts ? <span className="t-secondary" data-testid="alerts-source" title={`match: ${matchText}`}>{facts.source}{facts.readAt ? ` · as of ${clock(facts.readAt)}` : ""}</span> : undefined}
      controls={
        <>
          <div role="group" aria-label="Alert state" className="l-seg">
            {(["all", "active", "unprocessed", "suppressed"] as const).map((s) => (
              <button key={s} className="l-chip" aria-pressed={stateF === s} onClick={() => setStateF(s)}>{s === "all" ? "All" : s[0].toUpperCase() + s.slice(1)}</button>
            ))}
          </div>
          <div role="group" aria-label="Severity" className="l-seg">
            {(["all", "critical", "warning", "other"] as const).map((s) => (
              <button key={s} className="l-chip" aria-pressed={sevF === s} onClick={() => setSevF(s)}>{s === "all" ? "Any severity" : s[0].toUpperCase() + s.slice(1)}</button>
            ))}
          </div>
          <label className="lens-search">
            <span className="sr-only">Search alerts</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false}
              value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search alert, subject, label, summary" />
          </label>
        </>
      }
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 4 }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList ref={listRef} items={visible} rowHeight={TABLE_ROW_HEIGHT} rowKey={(a) => a.fingerprint} style={{ flex: 1, minHeight: 0 }}
            render={(a) => {
              const k = a.fingerprint;
              const cause = causeText(a, byFp);
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle} data-testid={`alert-row-${a.name}`} data-state={a.state}
                  aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
                  onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(k); } }}>
                  {show("sev") && <span style={cell}><SevMark a={a} /></span>}
                  {show("alert") && <span className="lens-cell-name" title={a.name}>{a.name}</span>}
                  {show("state") && <span style={{ ...cell, color: a.state === "active" ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }} title={cause}>{a.state}{cause ? ` · ${cause}` : ""}</span>}
                  {show("subject") && <span style={{ ...cell, color: a.subject.kind ? undefined : "var(--color-text-tertiary)" }} title={subjectNote(a.subject)}>{subjectText(a.subject)}</span>}
                  {show("started") && <span style={{ color: "var(--color-text-tertiary)" }}>{a.startsAt ? <Age unixSeconds={unix(a.startsAt)} /> : "-"}</span>}
                  {show("summary") && <span style={{ ...cell, color: "var(--color-text-secondary)" }}>{a.summary ?? ""}</span>}
                </div>
              );
            }} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length} of {all.length}{plus} alerts · match {matchText}</span>}
      inspector={selKey && selected ? (
        <AlertInspector key={selKey} cluster={cluster} a={selected} byFp={byFp} readAt={facts?.readAt} stale={stale} onClose={close} />
      ) : undefined}
      inspectorLabel="Alert details"
      onCloseInspector={close}
    />
  );
}

function AlertInspector({ cluster, a, byFp, readAt, stale, onClose }: { cluster: string; a: AMAlertDTO; byFp: Map<string, AMAlertDTO>; readAt?: string; stale: boolean; onClose: () => void }) {
  const cause = causeText(a, byFp);
  const open = openSubject(cluster, a.subject);
  const more: MenuItem[] = [{ label: "Copy fingerprint", onSelect: () => void navigator.clipboard?.writeText(a.fingerprint) }];
  if (a.generatorURL) more.push({ label: "Copy generator URL", onSelect: () => void navigator.clipboard?.writeText(a.generatorURL!) });
  if (a.runbookURL) more.push({ label: "Copy runbook URL", onSelect: () => void navigator.clipboard?.writeText(a.runbookURL!) });
  const seen = readAt ? `${stale ? "last seen" : "seen"} ${a.state} at ${clock(readAt)}${stale ? " (reads failing since)" : ""}` : "";
  return (
    <div data-testid="alert-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster }} kind="Alert" name={a.name} meta={subjectText(a.subject)} onClose={onClose}
        verdict={
          <div className="t-body" style={{ display: "grid", gap: 4 }}>
            <div><SevMark a={a} /> <span className="t-secondary">· {a.state}{cause ? ` · ${cause}` : ""}</span></div>
            <div className="t-secondary">{a.startsAt ? `started ${absoluteTime(Date.parse(a.startsAt))}` : "start not reported"}{seen ? ` · ${seen}` : ""}</div>
            {!a.counted && a.state === "active" && <div className="t-secondary">Not counted in Needs you: only active critical and warning alerts are.</div>}
          </div>
        }
        primary={open ? { label: `Open ${a.subject.kind}`, onSelect: open } : undefined}
        more={more}>
        {(a.summary || a.description) && (
          <InspectorBlock title="Summary">
            {a.summary && <div className="t-body" style={{ overflowWrap: "anywhere" }}>{a.summary}</div>}
            {a.description && <div className="t-secondary" style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", marginTop: 4 }}>{a.description}</div>}
          </InspectorBlock>
        )}
        <InspectorBlock title="Subject">
          <div className="insp-kv">
            <span>names</span><span style={{ overflowWrap: "anywhere" }}>{subjectText(a.subject)}{a.subject.container ? ` · container ${a.subject.container}` : ""}</span>
            <span>basis</span><span style={{ overflowWrap: "anywhere" }}>{subjectNote(a.subject)}</span>
            {(a.subject.alsoNames ?? []).length > 0 && <><span>also names</span><span>{(a.subject.alsoNames ?? []).map(subjectText).join(", ")}</span></>}
            {a.subject.createdAt && <><span>object created</span><span>{absoluteTime(Date.parse(a.subject.createdAt))}</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title="Labels" count={a.labels.length + (a.labelsMore ?? 0)}>
          <div className="insp-kv">{a.labels.map((l) => <Fragment key={l.name}><span>{l.name}</span><span style={{ overflowWrap: "anywhere" }}>{l.value}</span></Fragment>)}</div>
          {(a.labelsMore ?? 0) > 0 && <div className="t-secondary">+{a.labelsMore} more not shown</div>}
        </InspectorBlock>
        {a.annotations.length > 0 && (
          <InspectorBlock title="Annotations" count={a.annotations.length + (a.annotationsMore ?? 0)} defaultOpen={false}>
            <div className="insp-kv">{a.annotations.map((l) => <Fragment key={l.name}><span>{l.name}</span><span style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{l.value}</span></Fragment>)}</div>
          </InspectorBlock>
        )}
        <InspectorBlock title="Alertmanager" defaultOpen={false}>
          <div className="insp-kv">
            <span>fingerprint</span><span>{a.fingerprint}</span>
            <span>receivers</span><span>{(a.receivers ?? []).join(", ") || "none reported"}</span>
            {a.updatedAt && <><span>updated</span><span>{absoluteTime(Date.parse(a.updatedAt))}</span></>}
            {a.endsAt && <><span>end as last reported</span><span>{absoluteTime(Date.parse(a.endsAt))}{stale ? " (as of the last good read)" : ""}</span></>}
            {a.generatorURL && <><span>generator</span><span style={{ overflowWrap: "anywhere" }}>{a.generatorURL}</span></>}
            {a.runbookURL && <><span>runbook</span><span style={{ overflowWrap: "anywhere" }}>{a.runbookURL}</span></>}
          </div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}
