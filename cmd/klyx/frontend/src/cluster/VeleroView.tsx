import { useMemo, useRef, useState } from "react";
import { IconDatabaseExport } from "@tabler/icons-react";
import type { RuleDTO } from "../store/fleet";
import type { VeleroBackupDTO, VeleroLocationDTO, VeleroRunDTO, VeleroScheduleDTO } from "../bridge/velero";
import { useVeleroBackups, useVeleroLocations, useVeleroSchedules, type VeleroPartRows } from "../health/useVeleroRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { Age, formatAge, useNow } from "../chrome/Age";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { clock, kindEvidence, lastGoodAt } from "../layout/coverage";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { Aspect, aspectOfVerdict } from "../chrome/primitives/Aspect";
import { CountSentence, MarkedPhrase } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useDescribe } from "../chrome/describe/useDescribe";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { LensParts } from "../chrome/LensParts";
import { useNavIntent } from "../chrome/useNavIntent";
import { expectPin } from "../nav/inspectorPin";

// VeleroView - the Velero lens (coverage S2a §2): Schedules judged against
// the run Velero owes, Storage locations, and the last day's unscheduled
// backups, from the Observer's shared rows. Read-only. Velero's text never
// crosses: every message is Klyx's fixed class.

type Tab = "schedules" | "locations" | "backups";
type RoleKind = "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused";
const RANK: Record<string, number> = { fault: 0, degraded: 1, unknown: 2, progressing: 3, paused: 4, ok: 5 };
const rankOf = (r: RuleDTO) => RANK[r.role] ?? RANK.unknown;
const roleKind = (r: RuleDTO): RoleKind => (r.role in RANK ? r.role : "unknown") as RoleKind;
const role = (r: RuleDTO) => ({ kind: roleKind(r), label: r.label });
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const toneOf = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
const cell: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const keyOf = (kind: string, ns: string, name: string) => `${kind}/${ns}/${name}`;
const unix = (iso?: string) => (iso ? Math.floor(Date.parse(iso) / 1000) : 0);
// the reason without its leading label ("Missed: Velero created ...")
const why = (reason: string | undefined, label: string) => (reason ? (reason.startsWith(label + ": ") ? reason.slice(label.length + 2) : reason) : "");
const when = (iso?: string) => {
  if (!iso) return "-";
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay ? clock(iso) : d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
};
const secretNote = "named only, never read";

// A run's phase as a mark: Velero's phases, kept as they are.
export function phaseKind(phase: string): RoleKind {
  switch (phase) {
    case "Completed": return "ok";
    case "PartiallyFailed": return "degraded";
    case "Failed":
    case "FailedValidation": return "fault";
    default: return "progressing";
  }
}

const COLUMNS: Record<Tab, (ColumnDef & { label: string })[]> = {
  schedules: [
    ROW_MARK,
    { id: "name", label: "Name", width: 200, priority: 0, flex: true },
    { id: "status", label: "Status", width: 220, priority: 0 },
    { id: "cron", label: "Schedule", width: 170, priority: 1 },
    { id: "next", label: "Next run", width: 120, priority: 2 },
    { id: "location", label: "Location", width: 130, priority: 2 },
  ],
  locations: [
    ROW_MARK,
    { id: "name", label: "Name", width: 180, priority: 0, flex: true },
    { id: "status", label: "Status", width: 200, priority: 0 },
    { id: "bucket", label: "Provider · bucket", width: 220, priority: 1, grow: 0.5 },
    { id: "validated", label: "Validated", width: 96, priority: 1 },
    { id: "mode", label: "Mode", width: 120, priority: 2 },
  ],
  backups: [
    ROW_MARK,
    { id: "name", label: "Name", width: 220, priority: 0, flex: true },
    { id: "status", label: "Phase", width: 200, priority: 0 },
    { id: "created", label: "Created", width: 96, priority: 1 },
    { id: "errors", label: "Errors · warnings", width: 130, priority: 2 },
    { id: "location", label: "Location", width: 130, priority: 2 },
  ],
};

const PARTS: Record<Tab, { kind: string; noun: [string, string]; lens: string }> = {
  schedules: { kind: "Schedule", noun: ["schedule", "schedules"], lens: "VeleroSchedules" },
  locations: { kind: "BackupStorageLocation", noun: ["storage location", "storage locations"], lens: "VeleroLocations" },
  backups: { kind: "Backup", noun: ["recent unscheduled backup", "recent unscheduled backups"], lens: "VeleroBackups" },
};
const TAB_OF_PART: Record<string, Tab> = { schedules: "schedules", locations: "locations", backups: "backups" };

type Item = { key: string; uid: string; namespace: string; name: string; rule: RuleDTO };
function sorted<T extends Item>(rows: T[]): T[] {
  return [...rows].sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name));
}

export function VeleroView({ cluster }: { cluster: string }) {
  const sched = useVeleroSchedules(cluster);
  const locs = useVeleroLocations(cluster);
  const bks = useVeleroBackups(cluster);
  const nowS = Math.floor(useNow() / 1000);
  const [tab, setTab] = useState<Tab>("schedules");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const listRef = useRef<VirtualListHandle>(null);

  const schedules = useMemo(() => sorted(sched.rows.map((s) => ({ ...s, key: keyOf("Schedule", s.namespace, s.name) }))), [sched.rows]);
  const locations = useMemo(() => sorted(locs.rows.map((l) => ({ ...l, key: keyOf("BackupStorageLocation", l.namespace, l.name) }))), [locs.rows]);
  const backups = useMemo(() => sorted(bks.rows.map((b) => ({ ...b, key: keyOf("Backup", b.namespace, b.name) }))), [bks.rows]);
  const part: Record<Tab, VeleroPartRows<unknown>> = { schedules: sched, locations: locs, backups: bks };
  const items: Record<Tab, Item[]> = { schedules, locations, backups };
  const cur = part[tab];
  const list = items[tab];
  const ev = kindEvidence(!cur.loading, cur.coverage, PARTS[tab].kind, cur.watchError);
  const absent = cur.coverage.find((k) => k.kind === PARTS[tab].kind)?.coverage === "absent";
  const faults = (t: Tab) => (part[t].counts ? part[t].counts!.fault : items[t].filter((r) => r.rule.role === "fault").length);

  const keys = list.map((r) => r.key);
  const cursorIdx = cursorKey === null ? -1 : keys.indexOf(cursorKey);
  useListKeys({
    count: keys.length,
    selected: cursorIdx,
    onSelect: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); setSelKey(k); } },
    onEscape: () => setSelKey(null),
  });
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "velero") return "wait";
    const t = TAB_OF_PART[intent.part];
    if (!t) return "wait";
    const key = keyOf(PARTS[t].kind, intent.ref.namespace ?? "", intent.ref.name);
    const found = items[t].some((r) => r.key === key);
    const tev = kindEvidence(!part[t].loading, part[t].coverage, PARTS[t].kind, part[t].watchError);
    if (intent.ref.uid) expectPin(cluster, PARTS[t].lens, key, intent.ref.uid);
    else if (!found) return tev === "complete" ? "missing" : "wait";
    setTab(t);
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [schedules, locations, backups]);

  const fitted = useMemo(() => fitColumns(COLUMNS[tab], bodyWidth), [tab, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const selKind = selKey?.split("/")[0] ?? "";
  const selTab: Tab | null = selKind === "Schedule" ? "schedules" : selKind === "BackupStorageLocation" ? "locations" : selKind === "Backup" ? "backups" : null;
  const live = selTab ? items[selTab].find((r) => r.key === selKey) : undefined;
  const selEv = selTab ? kindEvidence(!part[selTab].loading, part[selTab].coverage, PARTS[selTab].kind, part[selTab].watchError) : "pending";
  const pin = usePinnedSubject(cluster, selTab ? selKey : null, live, selEv === "complete", selTab ? PARTS[selTab].lens : undefined);

  let state: DatasetState = "ready";
  if (absent) state = "empty";
  else if (ev === "failed" && list.length === 0) state = "unreadable";
  else if (ev === "pending" && list.length === 0) state = "loading";
  else if (list.length === 0) state = "empty";
  const icon = <IconDatabaseExport size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={4} label={`reading ${PARTS[tab].noun[1]}`} />
    : state === "unreadable" ? <EmptyState icon={icon} title={`Could not read the ${PARTS[tab].noun[1]}.`} hint={cur.watchError ?? undefined} />
    : absent ? <EmptyState icon={icon} title="Velero is not installed on this cluster." hint={`Klyx observes no ${PARTS[tab].kind} API here.`} />
    : <EmptyState icon={icon} title={tab === "backups" ? "No unscheduled backups in the last 24 hours." : `No ${PARTS[tab].noun[1]} on this cluster.`}
      hint={tab === "backups" ? "Scheduled runs are judged on their schedule; S2b lists every backup." : undefined} />;

  const notices: React.ReactNode[] = [];
  if (cur.watchError && list.length > 0) {
    const lg = lastGoodAt(cur.coverage, [PARTS[tab].kind]);
    notices.push(<WatchErrorBanner key="watch" tool="Velero" failure={`read failed - showing the last good state${lg ? ` from ${clock(lg)}` : ""}`} error={cur.watchError} />);
  }
  const capped = cur.coverage.find((k) => k.kind === "Backup" && k.truncated);
  if (capped) notices.push(<div key="cap" role="status" className="t-secondary" data-testid="velero-capped" style={{ color: "var(--color-text-warning)" }}>
    Backups are capped{capped.notRetained ? ` (${capped.notRetained}+ left out)` : ""}: Klyx holds the newest runs and every run in progress, and says where a verdict needs one it does not hold.
  </div>);

  const rowFor = (it: Item, cells: Record<string, React.ReactNode>) => (
    <div key={it.key} role="button" tabIndex={0} className="lens-row" style={gridStyle} data-testid={`velero-row-${it.name}`}
      aria-selected={it.key === cursorKey} data-cursor={it.key === cursorKey ? "true" : undefined} data-selected={it.key === selKey ? "true" : undefined}
      onClick={() => { setCursorKey(it.key); setSelKey(it.key === selKey ? null : it.key); }}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSelKey(it.key); } }}>
      {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id} style={c.id === "mark" ? undefined : cell}>{cells[c.id]}</span>)}
    </div>
  );
  const name = (ns: string, n: string) => <span className="lens-cell-name"><span style={{ color: "var(--color-text-tertiary)" }}>{ns}/</span>{n}</span>;
  const status = (r: RuleDTO, reason?: string) => {
    const ro = role(r);
    const w = ro.kind === "fault" || ro.kind === "degraded" ? why(reason, ro.label) : "";
    return <span style={{ ...cell, color: ROLE_TEXT[ro.kind] }}>{ro.label}{w ? ` · ${w}` : ""}</span>;
  };
  const muted = (s: React.ReactNode) => <span style={{ color: "var(--color-text-secondary)" }}>{s}</span>;
  const render = (it: Item) => {
    if (tab === "schedules") {
      const s = it as VeleroScheduleDTO & Item;
      return rowFor(it, { mark: <VerdictMark health={role(s.rule)} />, name: name(s.namespace, s.name), status: status(s.rule, s.reason),
        cron: muted(s.cronTooLong ? "(too long)" : `${s.cron}${s.zone && s.zone !== "the cron's own zone" && s.zone !== "UTC" ? ` · ${s.zone}` : ""}`),
        next: muted(s.paused ? "paused" : s.dueZero ? "every check" : s.due ? when(s.due) : "-"),
        location: muted(s.location.name ?? (s.location.state === "server default" ? "server default" : "-")) });
    }
    if (tab === "locations") {
      const l = it as VeleroLocationDTO & Item;
      return rowFor(it, { mark: <VerdictMark health={role(l.rule)} />, name: name(l.namespace, l.name), status: status(l.rule, l.reason),
        bucket: muted(`${l.provider ?? "-"} · ${l.bucket ?? "-"}${l.prefix ? `/${l.prefix}` : ""}`),
        validated: muted(l.lastValidation ? formatAge(Math.max(0, nowS - unix(l.lastValidation))) : "never"),
        mode: muted(`${l.default ? "default · " : ""}${l.accessMode ?? "ReadWrite"}`) });
    }
    const b = it as VeleroBackupDTO & Item;
    return rowFor(it, { mark: <VerdictMark health={role(b.rule)} />, name: name(b.namespace, b.name), status: status(b.rule, b.reason),
      created: muted(b.created ? formatAge(Math.max(0, nowS - unix(b.created))) : "-"), errors: muted(`${b.errors} · ${b.warnings}`),
      location: muted(b.storageLocation ?? (b.storageLocationInvalid ? "(invalid)" : "-")) });
  };

  const tabChip = (t: Tab, label: string) => {
    const n = faults(t);
    return <button className="l-chip" aria-pressed={tab === t} onClick={() => { setTab(t); setSelKey(null); setCursorKey(null); }}>{label}{n ? <span style={{ color: "var(--color-text-danger)" }}> {n}</span> : null}</button>;
  };

  const open = (k: string) => { const t: Tab = k.startsWith("Schedule/") ? "schedules" : "locations"; setTab(t); setCursorKey(k); setSelKey(k); };
  const subject = pin.subject as (Item & (VeleroScheduleDTO | VeleroLocationDTO | VeleroBackupDTO)) | null;
  const gone = pin.gone === "replaced" ? "removed" : pin.gone;
  const inspector = selTab && subject ? (
    selTab === "schedules" ? <ScheduleInspector cluster={cluster} s={subject as VeleroScheduleDTO} gone={gone} onOpen={open} onClose={() => setSelKey(null)} />
      : selTab === "locations" ? <LocationInspector cluster={cluster} l={subject as VeleroLocationDTO} gone={gone} onOpen={open} onClose={() => setSelKey(null)} />
      : <BackupInspector cluster={cluster} b={subject as VeleroBackupDTO} gone={gone} onClose={() => setSelKey(null)} />
  ) : selTab && selKey && pin.expected && pin.gone
    ? <PinnedGone cluster={cluster} kind={PARTS[selTab].kind} name={selKey.split("/").slice(1).join("/")} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={() => setSelKey(null)} />
    : undefined;

  const total = cur.counts ? cur.counts.total : list.length;
  return (
    <LensLayout
      label="Velero"
      title="Velero"
      part="velero"
      summary={absent ? null : (
        <>
          <CountSentence total={total} marker={ev === "complete" ? "" : "+"} noun={PARTS[tab].noun} />
          {faults(tab) > 0 ? <MarkedPhrase fault>{`${faults(tab)} ${tab === "schedules" ? "missed or failed" : tab === "locations" ? "unavailable" : "failed"}`}</MarkedPhrase> : <span className="t-secondary">none failing</span>}
        </>
      )}
      controls={(
        <>
          <LensParts cluster={cluster} groupId="velero" />
          <div role="group" aria-label="Velero objects" className="l-seg">
            {tabChip("schedules", "Schedules")}
            {tabChip("locations", "Storage locations")}
            {tabChip("backups", "Unscheduled backups")}
          </div>
        </>
      )}
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2 }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList ref={listRef} items={list} rowHeight={TABLE_ROW_HEIGHT} rowKey={(r) => r.uid || r.key} style={{ flex: 1, minHeight: 0 }} render={render} />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect</span>}
      footerEnd={<span className="t-secondary">Broken first · read-only · Velero records no events for these objects</span>}
      inspector={inspector}
      inspectorLabel="Velero object details"
      onCloseInspector={() => setSelKey(null)}
    />
  );
}

// ---- the run rail ----------------------------------------------------------

// VeleroRail is the schedule's drawing (S2a §2): its recent runs as stations
// on a line, oldest on the left, and the run Velero owes as the last one.
export function VeleroRail({ s }: { s: VeleroScheduleDTO }) {
  const runs = [...s.runs].reverse();
  const owedKind: "fault" | "attention" | "checking" | null = s.paused || s.dueZero || !s.due ? null
    : s.rule.label === "Missed" ? "fault" : s.rule.label === "Deferred" || s.rule.label === "Overdue" ? "attention" : "checking";
  const owedText = s.rule.label === "Missed" ? "missed" : s.rule.label === "Deferred" ? "deferred" : s.rule.label === "Overdue" ? "overdue" : "owed";
  if (runs.length === 0 && !owedKind) return <div className="t-secondary">No run of this schedule is retained.</div>;
  return (
    <div data-testid="velero-rail" role="list" aria-label="Recent runs" style={{ display: "flex", alignItems: "flex-start", gap: 0, overflowX: "auto", padding: "4px 0 2px" }}>
      {runs.map((r) => <Station key={r.uid || r.name} r={r} />)}
      {owedKind && (
        <div role="listitem" data-testid="velero-owed" title={`the run Velero owes, due ${s.due}`} style={stationStyle}>
          <div style={trackStyle(true)}><Aspect kind={owedKind} size={10} label={`${owedText}: due ${s.due}`} /></div>
          <div className="t-legend" style={{ color: owedKind === "fault" ? "var(--color-text-danger)" : owedKind === "attention" ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>{owedText}</div>
          <div className="t-legend" style={{ color: "var(--color-text-tertiary)" }}>{when(s.due)}</div>
        </div>
      )}
    </div>
  );
}

const stationStyle: React.CSSProperties = { display: "grid", justifyItems: "center", gap: 2, minWidth: 64 };
const trackStyle = (owed: boolean): React.CSSProperties => ({
  display: "flex", alignItems: "center", justifyContent: "center", height: 14, width: "100%",
  backgroundImage: `linear-gradient(var(--color-border-secondary), var(--color-border-secondary))`, backgroundSize: "100% 1px",
  backgroundRepeat: "no-repeat", backgroundPosition: "center", ...(owed ? { backgroundSize: "50% 1px", backgroundPosition: "left center" } : {}),
});

function Station({ r }: { r: VeleroRunDTO }) {
  const kind = phaseKind(r.phase);
  const aspect = aspectOfVerdict(kind);
  const title = `${r.name} · ${r.phase}${r.errors ? ` · ${r.errors} errors` : ""}${r.warnings ? ` · ${r.warnings} warnings` : ""}${r.proven ? "" : " · matched by name"}${r.class ? ` · ${r.class}` : ""}`;
  return (
    <div role="listitem" title={title} style={stationStyle}>
      <div style={trackStyle(false)}>
        {aspect === "none" ? <span aria-label={`${r.name}: Completed`} role="img" style={{ width: 2, height: 10, background: "var(--color-text-secondary)" }} /> : <Aspect kind={aspect} size={10} label={`${r.name}: ${r.phase}`} />}
      </div>
      <div className="t-legend" style={{ color: ROLE_TEXT[kind] }}>{r.phase === "Completed" ? "ok" : r.phase}</div>
      <div className="t-legend" style={{ color: "var(--color-text-tertiary)" }}>{when(r.at ?? r.created)}{r.proven ? "" : " ·n"}</div>
    </div>
  );
}

// ---- inspectors ------------------------------------------------------------

type Gone = "removed" | "unverified" | null;
const linkStyle: React.CSSProperties = { display: "block", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)", overflowWrap: "anywhere" };

function goneVerdict(kind: string, gone: Gone) {
  return <div role="status" className="t-body"><span className="t-status">{gone === "removed" ? `${kind} removed.` : `Not in the current ${kind} read.`}</span> The details below are its last evidence.</div>;
}

function runLine(label: string, r: VeleroRunDTO | undefined) {
  if (!r) return null;
  return <><span>{label}</span><span style={{ overflowWrap: "anywhere" }}>{r.name} · {r.phase}{r.at ? ` · ${when(r.at)}` : ""}{r.proven ? "" : " · matched by name only"}{r.class ? ` · ${r.class}` : ""}</span></>;
}

const N_STATE: Record<string, string> = {
  found: "found", unseen: "created, not yet seen", "not-retained": "not retained by Klyx", unidentified: "cannot be identified (the server's zone is unresolved)", gone: "not in the cluster (deleted or expired)",
};

function ScheduleInspector({ cluster, s, gone, onOpen, onClose }: { cluster: string; s: VeleroScheduleDTO; gone: Gone; onOpen: (key: string) => void; onClose: () => void }) {
  const ro = role(s.rule);
  const describe = useDescribe(cluster, !gone ? { kind: "Schedule", namespace: s.namespace, name: s.name, uid: s.uid } : null);
  const ai = useInspectorAI(s.uid ? { kind: "row" as const, task: "velero", part: "schedules", uid: s.uid } : null, `Schedule/${s.namespace}/${s.name}`, !gone, cluster);
  const t = s.template;
  const decided = s.deciding ? "the run it decided on" : s.previous ? "the previous outcome, while a newer run is in progress" : "";
  const loc = s.location;
  return (
    <div data-testid="velero-schedule-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: s.uid || undefined }} kind="Schedule" name={s.name} meta={s.namespace} onClose={onClose}
        verdict={gone ? goneVerdict("Schedule", gone) : (
          <div>
            <InspectorStatus tone={toneOf(ro.kind)} kind={ro.kind} label={ro.label} reason={why(s.reason, ro.label) || undefined} />
            <div className="t-secondary" style={{ marginTop: 4 }} data-testid="velero-proof">
              {s.useOwnerReferences ? "Runs are proven by their owner reference." : "Only the run Velero last created is proven; other runs are matched by name since this schedule was created."}
              {decided ? ` The verdict reads ${decided}.` : ""}
            </div>
          </div>
        )}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Recent runs">
          <VeleroRail s={s} />
          <div className="insp-kv" style={{ marginTop: 6 }}>
            {s.lastBackup && <><span>last created</span><span>Velero last created a run at {when(s.lastBackup)} (a creation, never a success){s.nName ? ` · ${s.nName}: ${N_STATE[s.nState ?? ""] ?? "-"}` : ""}</span></>}
            {s.lastSkipped && <><span>skipped</span><span>{when(s.lastSkipped)}</span></>}
            {runLine("decided by", s.deciding)}
            {runLine(s.previousNamed ? "previous (named)" : "previous", s.previous)}
            {runLine("waits for", s.blocker)}
            {runLine("retained", s.fact)}
          </div>
          {s.newer.length > 0 && <div className="t-secondary" style={{ marginTop: 4 }}>
            {s.newer.length} backup{s.newer.length === 1 ? "" : "s"} named for this schedule ran after its last controller run; not counted, not proven to be its runs: {s.newer.map((n) => `${n.name} (${n.phase})`).join(", ")}.
          </div>}
          {s.synced > 0 && <div className="t-secondary" data-testid="velero-synced" style={{ marginTop: 4 }}>{s.synced} backup{s.synced === 1 ? "" : "s"} from object storage carry this schedule's name; they look synced and are not counted.</div>}
          {!s.useOwnerReferences && <div className="t-secondary" style={{ marginTop: 4 }}>A bucket shared with another cluster that runs a same-named schedule can bring its backups in by name; they shape this rail, never the verdict. useOwnerReferencesInBackup makes every run proven.</div>}
        </InspectorBlock>
        <InspectorBlock title="Storage location">
          <div className="t-body" data-testid="velero-location-block">
            {loc.state === "found" ? (
              <button style={{ ...linkStyle, display: "inline" }} onClick={() => onOpen(keyOf("BackupStorageLocation", s.namespace, loc.name ?? ""))}>
                {loc.byDefault ? "Default · " : ""}{loc.name}
              </button>
            ) : <span>{loc.name ?? (loc.state === "server default" ? "the server's default (by flag; Klyx does not read flags)" : "-")}</span>}
            {loc.state === "found" ? <span style={{ color: ROLE_TEXT[loc.role ?? "unknown"] }}>: {loc.label}{loc.validated ? `, validated ${formatAge(Math.max(0, Math.floor(Date.now() / 1000) - unix(loc.validated)))} ago` : ""}</span>
              : loc.state === "invalid" ? <span style={{ color: "var(--color-text-warning)" }}> · the template names an invalid location</span>
              : loc.state !== "server default" ? <span style={{ color: "var(--color-text-warning)" }}> · {loc.state}</span> : null}
          </div>
          <div className="t-secondary">Its own verdict, shown beside the schedule's: never folded into it.</div>
        </InspectorBlock>
        <InspectorBlock title="Schedule">
          <div className="insp-kv">
            <span>cron</span><span style={{ overflowWrap: "anywhere" }}>{s.cronTooLong ? "(too long to read)" : s.cron}</span>
            {s.zone && <><span>zone</span><span>{s.zone}</span></>}
            {s.library && <><span>read with</span><span>{s.library}</span></>}
            {s.due && <><span>next run</span><span>{when(s.due)}</span></>}
            {s.dueUnknown && <><span>next run</span><span style={{ color: "var(--color-text-tertiary)" }}>{s.dueUnknown}</span></>}
            <span>phase</span><span>{s.phase || "(not yet validated)"}{s.paused ? " · paused" : ""}</span>
            {s.validation.length > 0 && <><span>validation</span><span>{s.validation.join("; ")}</span></>}
            {s.resumed && <><span>resumed</span><span>awaiting Velero's first run since it was unpaused</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title="Template, as configured now">
          <div className="insp-kv">
            <span>location</span><span>{t.storageLocation ?? (t.storageLocationInvalid ? "(invalid name)" : "the default")}</span>
            {t.ttl && <><span>ttl</span><span>{t.ttl}</span></>}
            <span>namespaces</span><span style={{ overflowWrap: "anywhere" }}>{t.includedCount ? `${t.includedNamespaces.join(", ")}${t.includedCount > t.includedNamespaces.length ? ` +${t.includedCount - t.includedNamespaces.length}` : ""}` : "all"}{t.excludedCount ? ` · excluding ${t.excludedNamespaces.join(", ")}` : ""}</span>
            {(t.snapshotVolumes !== undefined || t.defaultVolumesToFsBackup !== undefined || t.snapshotMoveData !== undefined) && <><span>volumes</span><span>{[
              t.snapshotVolumes !== undefined && `snapshots ${t.snapshotVolumes ? "on" : "off"}`, t.defaultVolumesToFsBackup && "file-system backup by default", t.snapshotMoveData && "snapshot data moved",
            ].filter(Boolean).join(" · ")}</span></>}
            {t.volumeSnapshotLocations.length > 0 && <><span>snapshot locations</span><span>{t.volumeSnapshotLocations.join(", ")}</span></>}
            {t.hooks > 0 && <><span>hooks</span><span>{t.hooks} hook{t.hooks === 1 ? "" : "s"} (commands withheld)</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title="Events">
          <div className="t-secondary">Velero records no Kubernetes events for schedules.</div>
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function LocationInspector({ cluster, l, gone, onOpen, onClose }: { cluster: string; l: VeleroLocationDTO; gone: Gone; onOpen: (key: string) => void; onClose: () => void }) {
  const ro = role(l.rule);
  const describe = useDescribe(cluster, !gone ? { kind: "BackupStorageLocation", namespace: l.namespace, name: l.name, uid: l.uid } : null);
  const ai = useInspectorAI(l.uid ? { kind: "row" as const, task: "velero", part: "locations", uid: l.uid } : null, `BackupStorageLocation/${l.namespace}/${l.name}`, !gone, cluster);
  return (
    <div data-testid="velero-location-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: l.uid || undefined }} kind="BackupStorageLocation" name={l.name} meta={l.namespace} onClose={onClose}
        verdict={gone ? goneVerdict("BackupStorageLocation", gone) : (
          <div>
            <InspectorStatus tone={toneOf(ro.kind)} kind={ro.kind} label={ro.label} reason={why(l.reason, ro.label) || undefined} />
            {l.messageWithheld && <div className="t-secondary" data-testid="velero-message-withheld" style={{ marginTop: 4 }}>Velero's message is withheld: provider errors can carry credentials. Klyx shows only fixed words found in it.</div>}
          </div>
        )}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Validation">
          <div className="insp-kv">
            <span>phase</span><span>{l.phase || "(not yet validated)"}</span>
            <span>validated</span><span>{l.lastValidation ? <><Age unixSeconds={unix(l.lastValidation)} suffix=" ago" /> · {when(l.lastValidation)}</> : "never"}</span>
            <span>every</span><span>{l.validationFrequency ?? "the server's frequency (1m by default; Klyx does not read flags)"}</span>
            {l.lastSynced && <><span>synced</span><span><Age unixSeconds={unix(l.lastSynced)} suffix=" ago" />{l.syncPeriod ? ` · every ${l.syncPeriod}` : ""}</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title="Storage">
          <div className="insp-kv">
            <span>provider</span><span>{l.provider ?? "-"}</span>
            <span>bucket</span><span style={{ overflowWrap: "anywhere" }}>{l.bucket ?? "-"}{l.prefix ? ` / ${l.prefix}` : ""}</span>
            <span>access</span><span>{l.accessMode ?? "ReadWrite"}{l.accessMode === "ReadOnly" ? " (restore-only: backups to it fail validation)" : ""}</span>
            <span>default</span><span>{l.default ? "yes" : "no"}{l.severalDefaults ? " · several locations are marked default; Velero keeps the newest and unsets the others" : ""}</span>
            {l.credential && <><span>credential</span><span>Secret {l.credential}{l.credentialKey ? ` key ${l.credentialKey}` : ""} ({secretNote})</span></>}
            {(l.caCertRef || l.caCertSet) && <><span>CA</span><span>{l.caCertRef ? `Secret ${l.caCertRef} (${secretNote})` : "a CA certificate is set"}</span></>}
            {l.region && <><span>region</span><span>{l.region}</span></>}
            {l.s3Url && <><span>endpoint</span><span>{l.s3Url}</span></>}
            {l.publicUrl && <><span>public</span><span>{l.publicUrl}</span></>}
            {l.configCount > 0 && <><span>config</span><span style={{ overflowWrap: "anywhere" }}>{l.configKeys.join(", ")}{l.configCount > l.configKeys.length ? ` +${l.configCount - l.configKeys.length}` : ""} (values not shown)</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title={`Schedules targeting it · ${l.schedules.length}${l.schedulesState === "incomplete" ? "+" : ""}`}>
          {l.schedulesState === "not readable" ? <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The schedules could not be read.</div>
            : l.schedules.length === 0 ? <div className="t-secondary">No schedule in its namespace targets it.</div>
            : l.schedules.map((s) => <button key={s.name} className="t-body" onClick={() => onOpen(keyOf("Schedule", s.namespace, s.name))} style={linkStyle}>
              {s.name} <span style={{ color: ROLE_TEXT[s.role] ?? ROLE_TEXT.unknown }}>· {s.label}</span>
            </button>)}
        </InspectorBlock>
        {l.expired > 0 && <InspectorBlock title="Expired backups">
          <div className="t-body" style={{ color: "var(--color-text-warning)" }}>{l.expired}{l.expiredState === "incomplete" ? "+" : ""} expired backup{l.expired === 1 ? "" : "s"} await deletion{l.gcFailures ? `: ${Object.entries(l.gcFailures).map(([k, v]) => `${v} ${k}`).join(", ")}` : ""}.</div>
          <div className="t-secondary">Velero's garbage collection needs the location Available and not read-only.</div>
        </InspectorBlock>}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

const MEMBERSHIP: Record<string, string> = {
  unattributed: "not a run of any current schedule",
  "named, newer than N": "named for its schedule but run after the schedule's last controller run, so not proven to be its run",
  "named, no N": "named for a schedule that has no controller run yet",
  "proven, order unknown": "a proven run of its schedule whose order is unknown, counted until a later finished run supersedes it",
};

function BackupInspector({ cluster, b, gone, onClose }: { cluster: string; b: VeleroBackupDTO; gone: Gone; onClose: () => void }) {
  const ro = role(b.rule);
  const describe = useDescribe(cluster, !gone ? { kind: "Backup", namespace: b.namespace, name: b.name, uid: b.uid } : null);
  const ai = useInspectorAI(b.uid ? { kind: "row" as const, task: "velero", part: "backups", uid: b.uid } : null, `Backup/${b.namespace}/${b.name}`, !gone, cluster);
  return (
    <div data-testid="velero-backup-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: b.uid || undefined }} kind="Backup" name={b.name} meta={b.namespace} onClose={onClose}
        verdict={gone ? goneVerdict("Backup", gone) : (
          <div>
            <InspectorStatus tone={toneOf(ro.kind)} kind={ro.kind} label={ro.label} reason={why(b.reason, ro.label) || undefined} />
            <div className="t-secondary" style={{ marginTop: 4 }}>Counted here because it is {MEMBERSHIP[b.membership] ?? b.membership}{b.membershipSchedule ? ` (${b.membershipSchedule})` : ""}. It leaves this list 24 hours after it ran.</div>
          </div>
        )}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Backup">
          <div className="insp-kv">
            <span>phase</span><span>{b.phase || "New"}</span>
            {b.failure && <><span>failure</span><span>{b.failure}</span></>}
            {b.validation.length > 0 && <><span>validation</span><span>{b.validation.join("; ")}{b.validationCount > b.validation.length ? ` +${b.validationCount - b.validation.length}` : ""}</span></>}
            <span>created</span><span>{when(b.created)}</span>
            {b.started && <><span>started</span><span>{when(b.started)}</span></>}
            {b.completed && <><span>completed</span><span>{when(b.completed)}</span></>}
            {b.expires && <><span>expires</span><span>{when(b.expires)}</span></>}
            <span>items</span><span>{b.itemsTotal ? `${b.itemsBackedUp ?? 0} of ${b.itemsTotal}` : "-"} · {b.errors} errors · {b.warnings} warnings</span>
            {b.hooksFailed ? <><span>hooks</span><span>{b.hooksFailed} of {b.hooksAttempted} failed</span></> : null}
            {b.operationsFailed ? <><span>operations</span><span>{b.operationsFailed} failed</span></> : null}
            <span>location</span><span>{b.storageLocation ?? (b.storageLocationInvalid ? "(invalid name)" : "-")}</span>
            {b.scheduleLabel && <><span>schedule label</span><span>{b.scheduleLabel}</span></>}
            {b.disputed && <><span>timing</span><span>it may have been synced from object storage (it started shortly before it existed here)</span></>}
          </div>
          <div className="t-secondary" style={{ marginTop: 4 }}>Error and warning texts are in the backup's log in object storage, which Klyx does not read.</div>
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}
