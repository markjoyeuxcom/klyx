import { failureStatus } from "../admission/finding";
import { useEffect, useMemo, useRef, useState } from "react";
import { IconArrowsMaximize } from "@tabler/icons-react";
import { useFleet, type RuleDTO } from "../store/fleet";
import {
  inspectScaler, scaledJobJobs, setScalerPaused,
  type HPAConditionDTO, type HPARowDTO, type KEDAAuthDTO, type KEDAConditionDTO, type KEDAFindingDTO, type KEDAInspectionDTO, type KEDAJobsDTO,
  type KEDAPauseDTO, type KEDATriggerDTO, type ScaledJobDTO, type ScaledObjectDTO,
} from "../bridge/keda";
import { useHPAs, useKEDAAuths, useScaledJobs, useScaledObjects, type AutoscalingPartRows } from "../health/useAutoscalingRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { clock, combineEvidence, kindEvidence, lastGoodAt } from "../layout/coverage";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
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
import { useNavIntent } from "../chrome/useNavIntent";
import { expectPin } from "../nav/inspectorPin";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { ownershipLine } from "./ownership";
import { formatAge, useNow } from "../chrome/Age";

// AutoscalingView - the Autoscaling lens (coverage S9 §9): standalone HPAs
// judged by the HPA controller's own conditions, KEDA's ScaledObjects and
// ScaledJobs in KEDA's vocabulary (Ready, Active, Fallback, Paused), with
// every finding behind the verdict, and the authentications as facts. A
// KEDA-owned HPA is listed as a fact, judged under its ScaledObject. The one
// write is Pause/Resume through autoscaling.keda.sh/paused (§8).

type Tab = "hpas" | "scaledobjects" | "scaledjobs" | "auths";
type RoleKind = "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused";
const RANK: Record<string, number> = { fault: 0, degraded: 1, unknown: 2, progressing: 3, paused: 4, ok: 5 };
const FACT_RANK = 6; // fact rows sort after every verdict
const rankOf = (r?: RuleDTO) => (r ? RANK[r.role] ?? RANK.unknown : FACT_RANK);
const roleKind = (role: string): RoleKind => (role in RANK ? role : "unknown") as RoleKind;
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const toneOf = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
const cell: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const why = (reason: string | undefined, label: string) => (reason ? (reason.startsWith(label + ": ") ? reason.slice(label.length + 2) : reason) : "");
const muted = (s: React.ReactNode) => <span style={{ ...cell, color: "var(--color-text-secondary)" }}>{s}</span>;

const COLUMNS: Record<Tab, (ColumnDef & { label: string })[]> = {
  hpas: [
    ROW_MARK,
    { id: "name", label: "Name", width: 230, priority: 0, flex: true },
    { id: "status", label: "Status", width: 260, priority: 0 },
    { id: "target", label: "Target", width: 190, priority: 1 },
    { id: "replicas", label: "Replicas", width: 150, priority: 1 },
  ],
  scaledobjects: [
    ROW_MARK,
    { id: "name", label: "Name", width: 220, priority: 0, flex: true },
    { id: "status", label: "Status", width: 260, priority: 0 },
    { id: "target", label: "Target", width: 180, priority: 1 },
    { id: "triggers", label: "Triggers", width: 150, priority: 2 },
    { id: "hpa", label: "HPA", width: 150, priority: 2 },
  ],
  scaledjobs: [
    ROW_MARK,
    { id: "name", label: "Name", width: 240, priority: 0, flex: true },
    { id: "status", label: "Status", width: 280, priority: 0 },
    { id: "triggers", label: "Triggers", width: 180, priority: 1 },
  ],
  auths: [
    ROW_MARK,
    { id: "name", label: "Name", width: 240, priority: 0, flex: true },
    { id: "kind", label: "Kind", width: 200, priority: 0 },
    { id: "sources", label: "Sources", width: 220, priority: 1 },
    { id: "users", label: "Used by", width: 140, priority: 1 },
  ],
};

const PARTS: Record<Tab, { noun: [string, string]; lens: string; kind: string; kinds: string[] }> = {
  hpas: { noun: ["HPA", "HPAs"], lens: "HPAs", kind: "HorizontalPodAutoscaler", kinds: ["HorizontalPodAutoscaler"] },
  scaledobjects: { noun: ["ScaledObject", "ScaledObjects"], lens: "KEDAScaledObjects", kind: "ScaledObject", kinds: ["ScaledObject"] },
  scaledjobs: { noun: ["ScaledJob", "ScaledJobs"], lens: "KEDAScaledJobs", kind: "ScaledJob", kinds: ["ScaledJob"] },
  auths: { noun: ["authentication", "authentications"], lens: "KEDAAuths", kind: "TriggerAuthentication", kinds: ["TriggerAuthentication", "ClusterTriggerAuthentication"] },
};
const KEY_KIND: Record<Tab, string> = { hpas: "HPA", scaledobjects: "SO", scaledjobs: "SJ", auths: "Auth" };
const TAB_OF_KEY: Record<string, Tab> = { HPA: "hpas", SO: "scaledobjects", SJ: "scaledjobs", Auth: "auths" };
const keyOf = (tab: Tab, ...parts: string[]) => `${KEY_KIND[tab]}/${parts.join("/")}`;

type Item = { key: string; uid: string; name: string };

// KEDA versions apply autoscaling.keda.sh/paused from 2.12 on a
// ScaledObject and 2.11 on a ScaledJob (S9 §8); the backend refuses the
// same, for the handling operator.
const versionAtLeast = (v: string, minor: number) => {
  const m = /^(\d+)\.(\d+)/.exec(v);
  return !!m && (Number(m[1]) > 2 || (Number(m[1]) === 2 && Number(m[2]) >= minor));
};

export function AutoscalingView({ cluster, initialTab }: { cluster: string; initialTab?: Tab }) {
  const hpas = useHPAs(cluster);
  const sos = useScaledObjects(cluster);
  const sjs = useScaledJobs(cluster);
  const auths = useKEDAAuths(cluster);
  const kedaPresent = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.kedaPresent ?? false);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const now = useNow();
  const [tab, setTab] = useState<Tab>(initialTab ?? (kedaPresent ? "scaledobjects" : "hpas"));
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const [pending, setPending] = useState<{ cluster: string; resource: "scaledobjects" | "scaledjobs"; ns: string; name: string; uid: string; pause: boolean; owner?: ScaledObjectDTO["owner"]; hpa?: string; target?: string } | null>(null);
  const listRef = useRef<VirtualListHandle>(null);
  useEffect(() => { setPending(null); }, [cluster]);

  const hpaItems = useMemo(() => hpas.rows.map((h) => ({ ...h, key: keyOf("hpas", h.namespace, h.name) }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || `${a.namespace}/${a.name}`.localeCompare(`${b.namespace}/${b.name}`)), [hpas.rows]);
  const soItems = useMemo(() => sos.rows.map((s) => ({ ...s, key: keyOf("scaledobjects", s.namespace, s.name) }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || `${a.namespace}/${a.name}`.localeCompare(`${b.namespace}/${b.name}`)), [sos.rows]);
  const sjItems = useMemo(() => sjs.rows.map((s) => ({ ...s, key: keyOf("scaledjobs", s.namespace, s.name) }))
    .sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || `${a.namespace}/${a.name}`.localeCompare(`${b.namespace}/${b.name}`)), [sjs.rows]);
  const authItems = useMemo(() => auths.rows.map((a) => ({ ...a, key: keyOf("auths", a.cluster ? "cluster" : a.namespace ?? "", a.name) }))
    .sort((a, b) => `${a.namespace ?? ""}/${a.name}`.localeCompare(`${b.namespace ?? ""}/${b.name}`)), [auths.rows]);
  const part: Record<Tab, AutoscalingPartRows<unknown>> = { hpas, scaledobjects: sos, scaledjobs: sjs, auths };
  const items: Record<Tab, Item[]> = { hpas: hpaItems, scaledobjects: soItems, scaledjobs: sjItems, auths: authItems };
  const cur = part[tab];
  const list = items[tab];
  const evOf = (t: Tab, kinds = PARTS[t].kinds) => combineEvidence(kinds.map((k) => kindEvidence(!part[t].loading, part[t].coverage, k, part[t].watchError)));
  const ev = evOf(tab);
  const attention = (t: Tab) => (part[t].counts ? part[t].counts!.included : 0);
  const faults = (t: Tab) => (part[t].counts ? part[t].counts!.fault : 0);

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
    if (intent.kind !== "reveal" || (intent.task !== "keda" && intent.task !== "autoscaling")) return "wait";
    const t: Tab = intent.task === "autoscaling" ? "hpas" : intent.part === "scaledjobs" ? "scaledjobs" : intent.part === "authentications" ? "auths" : "scaledobjects";
    const key = keyOf(t, intent.ref.namespace ?? "", intent.ref.name);
    const found = items[t].some((r) => r.key === key);
    if (intent.ref.uid) expectPin(cluster, PARTS[t].lens, key, intent.ref.uid);
    else if (!found) return evOf(t) === "complete" ? "missing" : "wait";
    setTab(t);
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [hpaItems, soItems, sjItems, authItems]);

  const fitted = useMemo(() => fitColumns(COLUMNS[tab], bodyWidth), [tab, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const selTab: Tab | null = TAB_OF_KEY[selKey?.split("/")[0] ?? ""] ?? null;
  const live = selTab ? items[selTab].find((r) => r.key === selKey) : undefined;
  const selEv = selTab ? evOf(selTab) : "pending";
  const pin = usePinnedSubject(cluster, selTab ? selKey : null, live, selEv === "complete", selTab ? PARTS[selTab].lens : undefined);

  let state: DatasetState = "ready";
  if (ev === "failed" && list.length === 0) state = "unreadable";
  else if (ev === "pending" && list.length === 0) state = "loading";
  else if (list.length === 0) state = "empty";
  const icon = <IconArrowsMaximize size={28} stroke={1.2} />;
  const emptyHint: Record<Tab, string> = {
    hpas: "A HorizontalPodAutoscaler appears here once one exists.",
    scaledobjects: "KEDA's ScaledObjects appear here once one exists.",
    scaledjobs: "KEDA's ScaledJobs appear here once one exists.",
    auths: "TriggerAuthentications and ClusterTriggerAuthentications appear here once one exists.",
  };
  const stateView = state === "loading" ? <SkeletonRows rows={4} label={`reading ${PARTS[tab].noun[1]}`} />
    : state === "unreadable" ? <EmptyState icon={icon} title={`Could not read the ${PARTS[tab].noun[1]}.`} hint={cur.watchError ?? undefined} />
    : <EmptyState icon={icon} title={`No ${PARTS[tab].noun[1]} on this cluster.`} hint={emptyHint[tab]} />;

  const notices: React.ReactNode[] = [];
  if (cur.watchError && list.length > 0) {
    const lg = lastGoodAt(cur.coverage, PARTS[tab].kinds);
    notices.push(<WatchErrorBanner key="watch" tool="Autoscaling" failure={`read failed - showing the last good state${lg ? ` from ${clock(lg)}` : ""}`} error={cur.watchError} />);
  }

  const rowFor = (it: Item, cells: Record<string, React.ReactNode>) => (
    <div key={it.key} role="button" tabIndex={0} className="lens-row" style={gridStyle} data-testid={`as-row-${it.name}`}
      aria-selected={it.key === cursorKey} data-cursor={it.key === cursorKey ? "true" : undefined} data-selected={it.key === selKey ? "true" : undefined}
      onClick={() => { setCursorKey(it.key); setSelKey(it.key === selKey ? null : it.key); }}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSelKey(it.key); } }}>
      {COLUMNS[tab].filter((c) => show(c.id)).map((c) => <span key={c.id} style={c.id === "mark" ? undefined : cell}>{cells[c.id]}</span>)}
    </div>
  );
  const status = (r: RuleDTO, reason?: string) => {
    const k = roleKind(r.role);
    const w = k === "fault" || k === "degraded" || k === "unknown" ? why(reason, r.label) : "";
    return <span style={{ ...cell, color: ROLE_TEXT[k] }}>{r.label}{w ? ` · ${w}` : ""}</span>;
  };
  const mark = (r?: RuleDTO) => r ? <VerdictMark health={{ kind: roleKind(r.role), label: r.label }} /> : <span />;
  const render = (it: Item) => {
    if (tab === "hpas") {
      const h = it as HPARowDTO & Item;
      const statusCell = h.rule ? status(h.rule, h.reason) : muted(`KEDA's · judged under ScaledObject ${h.kedaOwner}`);
      return rowFor(it, { mark: mark(h.rule), name: <span className="lens-cell-name">{h.namespace}/{h.name}</span>, status: statusCell,
        target: muted(`${h.target.kind}/${h.target.name}`), replicas: muted(`${h.current} → ${h.desired} (${h.min ?? 1}-${h.max})`) });
    }
    if (tab === "scaledobjects") {
      const s = it as ScaledObjectDTO & Item;
      return rowFor(it, { mark: mark(s.rule), name: <span className="lens-cell-name">{s.namespace}/{s.name}</span>, status: status(s.rule, s.reason),
        target: muted(`${s.targetKind}/${s.targetName}`), triggers: muted(s.triggers.map((t) => t.type).join(", ") + (s.triggersCut ? ` +${s.triggerCount - s.triggers.length}` : "")),
        hpa: muted(s.hpa ? s.hpa.name : s.pause.kind === "full" ? "none (paused)" : "-") });
    }
    if (tab === "scaledjobs") {
      const j = it as ScaledJobDTO & Item;
      return rowFor(it, { mark: mark(j.rule), name: <span className="lens-cell-name">{j.namespace}/{j.name}</span>, status: status(j.rule, j.reason),
        triggers: muted(j.triggers.map((t) => t.type).join(", ")) });
    }
    const a = it as KEDAAuthDTO & Item;
    return rowFor(it, { mark: mark(undefined), name: <span className="lens-cell-name">{a.cluster ? a.name : `${a.namespace}/${a.name}`}</span>,
      kind: muted(a.cluster ? "ClusterTriggerAuthentication" : "TriggerAuthentication"),
      sources: muted([...a.sources.map((s) => s.kind), ...(a.podIdentity ? [`podIdentity ${a.podIdentity.provider}`] : [])].join(", ") || "-"),
      users: muted(`${a.users.length}${a.usersComplete ? "" : "+"}`) });
  };

  const tabChip = (t: Tab, label: string) => {
    const n = t === "auths" ? 0 : attention(t);
    const f = t === "auths" ? 0 : faults(t);
    return <button key={t} className="l-chip" aria-pressed={tab === t} onClick={() => { setTab(t); setSelKey(null); setCursorKey(null); }}>
      {label}{n ? <span style={{ color: f ? "var(--color-text-danger)" : "var(--color-text-warning)" }}> {n}</span> : null}</button>;
  };
  const subject = pin.subject as (Item & (HPARowDTO | ScaledObjectDTO | ScaledJobDTO | KEDAAuthDTO)) | null;
  const gone = pin.gone === "replaced" ? "removed" : pin.gone;
  const onPause = (resource: "scaledobjects" | "scaledjobs", s: ScaledObjectDTO | ScaledJobDTO, pause: boolean) =>
    setPending({ cluster, resource, ns: s.namespace, name: s.name, uid: s.uid, pause, owner: s.owner,
      hpa: "hpa" in s && s.hpa ? s.hpa.name : undefined, target: "targetKind" in s ? `${s.targetKind}/${s.targetName}` : undefined });
  const inspector = selTab && subject ? (
    selTab === "hpas" ? <HPAInspector cluster={cluster} h={subject as HPARowDTO} gone={gone} onClose={() => setSelKey(null)} />
      : selTab === "scaledobjects" ? <ScaledObjectInspector cluster={cluster} s={subject as ScaledObjectDTO} now={now} gone={gone} onPause={(p) => onPause("scaledobjects", subject as ScaledObjectDTO, p)} onClose={() => setSelKey(null)} />
      : selTab === "scaledjobs" ? <ScaledJobInspector cluster={cluster} j={subject as ScaledJobDTO} gone={gone} onPause={(p) => onPause("scaledjobs", subject as ScaledJobDTO, p)} onClose={() => setSelKey(null)} />
      : <AuthInspector cluster={cluster} a={subject as KEDAAuthDTO} gone={gone} onClose={() => setSelKey(null)} />
  ) : selTab && selKey && pin.expected && pin.gone
    ? <PinnedGone cluster={cluster} kind={PARTS[selTab].kind} name={selKey.split("/").slice(-1)[0]} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={() => setSelKey(null)} />
    : undefined;

  const confirm = pending && pending.cluster === cluster ? pending : null;
  const send = (p: NonNullable<typeof pending>) => {
    setPending(null);
    const rows = p.resource === "scaledobjects" ? soItems : sjItems;
    const curUID = rows.find((r) => r.namespace === p.ns && r.name === p.name)?.uid;
    const kind = p.resource === "scaledobjects" ? "ScaledObject" : "ScaledJob";
    if (!curUID || curUID !== p.uid) {
      useFleet.getState().setActionStatus({ kind: "error", message: `${kind} ${p.ns}/${p.name} was replaced since the action was armed - nothing was sent` });
      return;
    }
    void setScalerPaused(cluster, p.resource, p.ns, p.name, p.uid, p.pause).then((r) => {
      useFleet.getState().setActionStatus(r.ok
        ? { kind: "success", message: `${p.pause ? "Pause" : "Resume"} requested for ${kind} ${p.ns}/${p.name}` }
        : failureStatus(r, `${p.pause ? "Pause" : "Resume"} failed`));
    });
  };

  const total = cur.counts ? cur.counts.total : list.length;
  const word: Record<Tab, string> = { hpas: "cannot scale", scaledobjects: "failing or not scaling", scaledjobs: "failing", auths: "" };
  return (
    <>
      <LensLayout
        label="Autoscaling"
        title="Autoscaling"
        part="autoscaling"
        summary={(
          <>
            <CountSentence total={total} marker={ev === "complete" ? "" : "+"} noun={PARTS[tab].noun} />
            {tab === "auths" ? <span className="t-secondary">facts, never verdicts</span>
              : attention(tab) > 0 ? <MarkedPhrase fault={faults(tab) > 0}>{`${attention(tab)} ${word[tab]}`}</MarkedPhrase> : <span className="t-secondary">none failing</span>}
          </>
        )}
        controls={(
          <div role="group" aria-label="Autoscaling objects" className="l-seg">
            {tabChip("hpas", "HPAs")}
            {(kedaPresent || sos.rows.length > 0) && tabChip("scaledobjects", "ScaledObjects")}
            {(kedaPresent || sjs.rows.length > 0) && tabChip("scaledjobs", "ScaledJobs")}
            {(kedaPresent || auths.rows.length > 0) && tabChip("auths", "Authentications")}
          </div>
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
        footerEnd={<span className="t-secondary">Broken first · {tab === "hpas" ? "a KEDA-owned HPA is judged under its ScaledObject" : "KEDA's vocabulary"}</span>}
        inspector={inspector}
        inspectorLabel="Autoscaling details"
        onCloseInspector={() => setSelKey(null)}
      />
      {confirm && (
        <ConfirmDialog
          title={`${confirm.pause ? "Pause" : "Resume"} ${confirm.resource === "scaledobjects" ? "ScaledObject" : "ScaledJob"} ${confirm.ns}/${confirm.name}?`}
          cluster={confirm.cluster}
          detail={pauseDetail(confirm)}
          protected={isProtected}
          danger={confirm.pause}
          confirmLabel={confirm.pause ? "Pause" : "Resume"}
          onCancel={() => setPending(null)}
          onConfirm={() => send(confirm)}
        />
      )}
    </>
  );
}

// The verb's consequence copy (S9 §8): what KEDA does, and the GitOps line.
export function pauseDetail(p: { resource: "scaledobjects" | "scaledjobs"; pause: boolean; owner?: ScaledObjectDTO["owner"]; hpa?: string; target?: string }): string {
  const mech = p.pause ? "Sets autoscaling.keda.sh/paused: \"true\"." : "Removes autoscaling.keda.sh/paused.";
  const effect = p.resource === "scaledobjects"
    ? p.pause ? `KEDA deletes its HPA${p.hpa ? ` (${p.hpa})` : ""} and holds ${p.target ?? "the target"} at its current replicas until resumed.`
      : `KEDA recreates its HPA and resumes scaling ${p.target ?? "the target"}.`
    : p.pause ? "KEDA stops creating Jobs; Jobs already running continue." : "KEDA resumes creating Jobs from its triggers.";
  const owner = p.owner ? ` ${ownershipLine(p.owner, "annotation")}` : "";
  return `${mech} ${effect}${owner} Klyx says "requested": the pause is complete only when KEDA's status shows it.`;
}

type Gone = "removed" | "unverified" | null;

function goneVerdict(kind: string, gone: Gone) {
  return <div role="status" className="t-body"><span className="t-status">{gone === "removed" ? `${kind} removed.` : `Not in the current ${kind} read.`}</span> The details below are its last evidence.</div>;
}

const condText = (c: { type: string; status: string; reason?: string; message?: string }) => `${c.type}=${c.status}${c.reason ? ` (${c.reason})` : ""}${c.message ? `: ${c.message}` : ""}`;

// KEDA's conditions as chips, each "not reported" when absent (S9 §9).
function KEDAConditions({ conds, types }: { conds: KEDAConditionDTO[]; types: string[] }) {
  return (
    <div data-testid="keda-conditions" style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
      {types.map((t) => {
        const c = conds.find((x) => x.type === t);
        const color = !c ? "var(--color-text-tertiary)" : c.status === "True" ? (t === "Fallback" ? "var(--color-text-warning)" : "var(--color-text-secondary)")
          : c.status === "False" ? (t === "Ready" || t === "HPAActive" ? "var(--color-text-danger)" : "var(--color-text-secondary)") : "var(--color-text-tertiary)";
        return <span key={t} className="l-chip" title={c ? condText(c) : `${t}: not reported`} style={{ color }}>{t} {c ? c.status : "not reported"}{c?.reason ? ` · ${c.reason}` : ""}</span>;
      })}
    </div>
  );
}

function Findings({ findings }: { findings: KEDAFindingDTO[] }) {
  if (findings.length <= 1) return null;
  return (
    <InspectorBlock title="Every finding">
      <div data-testid="keda-findings" style={{ display: "grid", gap: 2 }}>
        {findings.map((f, i) => (
          <div key={i} className="t-body" style={{ color: f.note ? "var(--color-text-tertiary)" : ROLE_TEXT[roleKind(f.role)] }}>
            {f.note ? "note" : f.role}: {f.label}{f.text && f.text !== f.label ? <span className="t-secondary"> · {f.text}</span> : null}
          </div>
        ))}
      </div>
    </InspectorBlock>
  );
}

function Triggers({ triggers, cut, count, metrics, tracked }: { triggers: KEDATriggerDTO[]; cut?: boolean; count: number; metrics?: ScaledObjectDTO["metrics"]; tracked?: boolean }) {
  return (
    <InspectorBlock title={`Triggers · ${count}`}>
      <div data-testid="keda-triggers" style={{ display: "grid", gap: 2 }}>
        {triggers.map((t, i) => {
          const m = metrics?.find((x) => x.metric.startsWith(`s${i}-`));
          return (
            <div key={i} className="t-body">
              {t.type}{t.name ? ` ${t.name}` : ""}{t.metricType ? <span className="t-secondary"> · {t.metricType}</span> : null}
              {m?.active !== undefined && <span className="t-secondary"> · {m.active ? "active" : "not active"}</span>}
              {metrics && <span className="t-secondary"> · {m?.failures !== undefined ? `${m.failures} failures (${m.status})` : tracked ? "no failures recorded" : "failures not tracked without a fallback"}</span>}
              {t.authName && <span className="t-secondary"> · authentication {t.authKind === "ClusterTriggerAuthentication" ? "ClusterTriggerAuthentication " : ""}{t.authName}</span>}
            </div>
          );
        })}
        {cut && <div className="t-secondary">+{count - triggers.length} more triggers not kept: an authentication used only by them is not listed.</div>}
        <div className="t-secondary">Trigger metadata is configuration: it shows in the object's YAML, and is never kept or sent to AI.</div>
      </div>
    </InspectorBlock>
  );
}

function PauseBlock({ p, resource, version, onPause, gone }: { p: KEDAPauseDTO; resource: "scaledobjects" | "scaledjobs"; version: string; onPause: (pause: boolean) => void; gone: Gone }) {
  const min = resource === "scaledobjects" ? 12 : 11;
  const known = versionAtLeast(version, 0);
  const applies = versionAtLeast(version, min);
  const others = [p.pausedReplicas !== undefined && "autoscaling.keda.sh/paused-replicas", p.scaleIn !== undefined && "autoscaling.keda.sh/paused-scale-in",
    p.scaleOut !== undefined && "autoscaling.keda.sh/paused-scale-out"].filter(Boolean) as string[];
  const canResume = p.paused !== undefined && others.length === 0;
  const canPause = p.paused === undefined && p.kind !== "full";
  return (
    <InspectorBlock title="Pause">
      <div className="insp-kv">
        <span>state</span><span data-testid="keda-pause-state">{p.completed ? "paused (completed)" : p.kind === "full" ? "pause requested" : p.kind === "directional" ? `${p.directions.join(" and ")} paused` : "not paused"}</span>
        {p.annotation && <><span>by</span><span>{p.annotation}</span></>}
        {p.requested !== undefined && <><span>count</span><span>{p.applied !== undefined && p.applied !== p.requested ? `applied ${p.applied}, ${p.requested} requested` : `${p.requested}`}</span></>}
        {p.malformed && <><span>malformed</span><span style={{ color: "var(--color-text-warning)" }}>paused-replicas is not a number: KEDA refuses the object</span></>}
        {p.notApplied && <><span>not applied</span><span>KEDA {version} does not apply {p.notApplied}</span></>}
        {p.unverified && <><span>unverified</span><span>{p.unverified}: whether this KEDA applies it is unverified (version unknown)</span></>}
      </div>
      {!gone && (
        <div style={{ display: "flex", gap: 6, marginTop: 6, alignItems: "center" }}>
          {canPause && <button className="l-btn" disabled={!applies} onClick={() => onPause(true)} data-testid="keda-pause">Pause</button>}
          {canResume && <button className="l-btn" disabled={!applies} onClick={() => onPause(false)} data-testid="keda-resume">Resume</button>}
          {!canResume && p.kind === "full" && others.length > 0 && <span className="t-secondary">Paused by {others.join(", ")}, set outside Klyx: Klyx does not change it.</span>}
          {(canPause || canResume) && !applies && <span className="t-secondary" data-testid="keda-verb-limit">{known ? `KEDA ${version} does not apply autoscaling.keda.sh/paused.` : "KEDA version unknown: Klyx cannot prove this KEDA applies the pause annotation."}</span>}
        </div>
      )}
    </InspectorBlock>
  );
}

// evidenceKey is the revision of what the on-demand evidence reads from a
// scaler: the backend's (generation, labels, target) and the references
// the row shows.
const evidenceKey = (rev: string | undefined, triggers: KEDATriggerDTO[], count: number, cut?: boolean) =>
  `${rev ?? ""}|${count}|${cut ? 1 : 0}|${triggers.map((t) => `${t.authKind ?? ""}/${t.authName ?? ""}`).join(",")}`;

// The on-demand authentication evidence (S9 §6): the handling operator,
// each authentication's existence and visibility, the referenced objects'
// existence where KEDA reads them, and the subject. It re-reads when the
// scaler's evidence revision changes, and shows a reply only for its own
// object and revision (implementation review 1, finding 7).
function AuthEvidence({ cluster, resource, ns, name, uid, rev, refs }: { cluster: string; resource: "scaledobjects" | "scaledjobs"; ns: string; name: string; uid: string; rev: string; refs: number }) {
  const [held, setHeld] = useState<{ uid: string; rev: string; r: KEDAInspectionDTO } | null>(null);
  useEffect(() => {
    let live = true;
    setHeld(null);
    void inspectScaler(cluster, resource, ns, name, uid).then((r) => { if (live && r.uid === uid) setHeld({ uid, rev, r }); });
    return () => { live = false; };
  }, [cluster, resource, ns, name, uid, rev]);
  const res = held && held.uid === uid && held.rev === rev ? held.r : null;
  return (
    <InspectorBlock title="Operator and authentication">
      {!res ? <div className="t-secondary">reading…</div> : res.error ? <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>Not read: {res.error}</div> : (
        <div data-testid="keda-auth-evidence" style={{ display: "grid", gap: 6 }}>
          <div className="t-body">
            {res.handler ? <>Handled by the KEDA operator {res.handler}{res.version ? ` (KEDA ${res.version})` : ""}</>
              : res.none ? <span style={{ color: "var(--color-text-warning)" }}>No KEDA operator watches this object (its namespace or labels are outside every operator's scope).</span>
              : <span className="t-secondary">The handling operator is unverified: {res.candidates.join(", ") || "no candidate could be read"}{res.candidatesCut ? " (more operators than Klyx reads)" : ""}.</span>}
          </div>
          {res.disagree.length > 0 && <div className="t-secondary">The operator's running pods disagree on {res.disagree.join(", ")} (a rollout).</div>}
          {refs === 0 && <div className="t-secondary">No trigger names an authentication.</div>}
          {res.auths.map((a) => (
            <div key={`${a.kind}/${a.name}`} style={{ borderTop: "0.5px solid var(--color-border-tertiary)", paddingTop: 4 }}>
              <div className="t-body">{a.kind} {a.name}: <span style={{ color: a.state === "not found" ? "var(--color-text-danger)" : undefined }}>{a.state}</span>{a.why ? <span className="t-secondary"> ({a.why})</span> : null}</div>
              {Object.entries(a.visible).map(([op, v]) => <div key={op} className="t-secondary">{v === "excluded" ? <span style={{ color: "var(--color-text-danger)" }}>outside {op}'s authentication selector: KEDA cannot read it</span> : `${v} to ${op}`}</div>)}
              {a.refs.map((r, i) => <div key={i} className="t-secondary">{r.kind} {r.name}{r.key ? ` · key ${r.key}` : ""}{r.namespace ? ` in ${r.namespace}` : ""}: <span style={{ color: r.state === "not found" ? "var(--color-text-danger)" : undefined }}>{r.state}</span>{r.why ? ` (${r.why})` : ""}</div>)}
              {(a.refsOmitted ?? 0) > 0 && <div className="t-secondary">{a.refsOmitted} more reference{a.refsOmitted === 1 ? " is" : "s are"} not listed (past the 16 per source Klyx keeps, or the reply's limit): unverified.</div>}
              {a.refsCut && !a.refsOmitted && <div className="t-secondary">More references than Klyx reads: the rest are unverified.</div>}
              {a.subject && <div className="t-secondary" data-testid="keda-subject">
                Authenticates as {a.subject.base}{a.subject.serviceAccount ? ` (ServiceAccount ${a.subject.serviceAccount})` : ""}
                {a.subject.requested.length > 0 ? ` · ${a.subject.requested.join(", ")}` : ""}
                {a.subject.refusals.map((x) => <div key={x} style={{ color: "var(--color-text-danger)" }}>{x}</div>)}
                {a.subject.unresolved.map((x) => <div key={x}>{x}</div>)}
                {a.subject.notes.map((x) => <div key={x}>{x}</div>)}
              </div>}
            </div>
          ))}
          <div className="t-secondary">Values are never read: existence is a metadata-only read, at the namespace KEDA reads each from.</div>
        </div>
      )}
    </InspectorBlock>
  );
}

function ScaledObjectInspector({ cluster, s, now, gone, onPause, onClose }: { cluster: string; s: ScaledObjectDTO; now: number; gone: Gone; onPause: (p: boolean) => void; onClose: () => void }) {
  const ro = roleKind(s.rule.role);
  const describe = useDescribe(cluster, !gone ? { kind: "ScaledObject", namespace: s.namespace, name: s.name, uid: s.uid } : null);
  const ai = useInspectorAI(s.uid ? { kind: "row" as const, task: "keda", part: "scaledobjects", uid: s.uid } : null, `ScaledObject/${s.namespace}/${s.name}`, !gone, cluster);
  const refs = s.triggers.filter((t) => t.authName).length;
  return (
    <div data-testid="keda-so-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: s.uid || undefined }} kind="ScaledObject" name={s.name} meta={s.namespace} onClose={onClose}
        verdict={gone ? goneVerdict("ScaledObject", gone) : <InspectorStatus tone={toneOf(ro)} kind={ro} label={s.rule.label} reason={why(s.reason, s.rule.label) || undefined} />}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="KEDA's conditions">
          <KEDAConditions conds={s.conditions} types={["Ready", "Active", "Fallback", "Paused", "HPAActive"]} />
          {s.lastActive && <div className="t-secondary" style={{ marginTop: 4 }}>last active {formatAge((now - Date.parse(s.lastActive)) / 1000)} ago</div>}
          <div className="t-secondary" style={{ marginTop: 4 }}>KEDA writes no condition times and no observed generation: the age of a state is Klyx's own observation.</div>
        </InspectorBlock>
        <Findings findings={s.findings} />
        <InspectorBlock title="Target and HPA">
          <div className="insp-kv">
            <span>target</span><span>{s.targetKind}/{s.targetName}{s.targetGroup ? <span className="t-secondary"> · {s.targetGroup}</span> : null}</span>
            <span>replicas</span><span>min {s.min ?? 0} · max {s.max ?? 100}{s.idle !== undefined ? ` · idle ${s.idle}` : ""}</span>
            <span>HPA</span><span data-testid="keda-hpa">{s.hpa ? <>{s.hpa.name}: {s.hpa.current} → {s.hpa.desired} (min {s.hpa.min ?? 1}, max {s.hpa.max})</> : s.pause.kind === "full" ? "none: a full pause deletes it" : s.hpaComplete ? "none controlled by this ScaledObject" : "not established: the HPA read is incomplete"}</span>
            {s.hpa && !s.hpaCurrent && <><span>as of</span><span>the HPA read is not current: dated evidence</span></>}
            {s.hpaName && s.hpa && s.hpaName !== s.hpa.name && <><span>status names</span><span style={{ color: "var(--color-text-warning)" }}>{s.hpaName}</span></>}
            {s.others.length > 0 && <><span>also scaling it</span><span style={{ color: "var(--color-text-danger)" }}>{s.others.join(", ")}</span></>}
          </div>
          {s.hpa && s.hpa.conditions.map((c) => <div key={c.type} className="t-secondary" style={{ overflowWrap: "anywhere" }}>{condText(c)}</div>)}
          <div className="t-secondary" style={{ marginTop: 4 }}>The HPA is KEDA's by its controller reference to this ScaledObject, never by its name.</div>
        </InspectorBlock>
        {s.fallback && <InspectorBlock title="Fallback">
          <div className="insp-kv">
            <span>behavior</span><span>{s.fallback.behavior}</span>
            <span>replicas</span><span>{s.fallback.replicas}</span>
            <span>after</span><span>more than {s.fallback.failureThreshold} failures</span>
          </div>
          <div className="t-secondary">An active fallback feeds the HPA a replacement metric; it does not prove which count results.</div>
        </InspectorBlock>}
        <Triggers triggers={s.triggers} cut={s.triggersCut} count={s.triggerCount} metrics={s.metrics} tracked={s.healthTracked} />
        <PauseBlock p={s.pause} resource="scaledobjects" version={s.version} onPause={onPause} gone={gone} />
        {!gone && <AuthEvidence cluster={cluster} resource="scaledobjects" ns={s.namespace} name={s.name} uid={s.uid} rev={evidenceKey(s.evidenceRev, s.triggers, s.triggerCount, s.triggersCut)} refs={refs} />}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function ScaledJobInspector({ cluster, j, gone, onPause, onClose }: { cluster: string; j: ScaledJobDTO; gone: Gone; onPause: (p: boolean) => void; onClose: () => void }) {
  const ro = roleKind(j.rule.role);
  const describe = useDescribe(cluster, !gone ? { kind: "ScaledJob", namespace: j.namespace, name: j.name, uid: j.uid } : null);
  const ai = useInspectorAI(j.uid ? { kind: "row" as const, task: "keda", part: "scaledjobs", uid: j.uid } : null, `ScaledJob/${j.namespace}/${j.name}`, !gone, cluster);
  const [jobs, setJobs] = useState<KEDAJobsDTO | null>(null);
  useEffect(() => {
    let live = true;
    setJobs(null);
    if (!gone) void scaledJobJobs(cluster, j.namespace, j.name, j.uid).then((r) => { if (live) setJobs(r); });
    return () => { live = false; };
  }, [cluster, j.namespace, j.name, j.uid, gone]);
  return (
    <div data-testid="keda-sj-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: j.uid || undefined }} kind="ScaledJob" name={j.name} meta={j.namespace} onClose={onClose}
        verdict={gone ? goneVerdict("ScaledJob", gone) : <InspectorStatus tone={toneOf(ro)} kind={ro} label={j.rule.label} reason={why(j.reason, j.rule.label) || undefined} />}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="KEDA's conditions"><KEDAConditions conds={j.conditions} types={["Ready", "Active", "Paused"]} /></InspectorBlock>
        <Findings findings={j.findings} />
        <InspectorBlock title="Jobs">
          {!jobs ? <div className="t-secondary">{gone ? "not read" : "reading…"}</div> : jobs.error ? <div className="t-secondary">Not read: {jobs.error}</div> : (
            <div data-testid="keda-jobs" style={{ display: "grid", gap: 2 }}>
              {jobs.jobs.length === 0 && <div className="t-secondary">{jobs.complete ? "No Jobs of this ScaledJob." : `None among the first ${jobs.read} read.`}</div>}
              {jobs.jobs.map((x) => <div key={x.name} className="t-body">{x.name}: <span style={{ color: x.failedOut ? "var(--color-text-danger)" : undefined }}>{x.failedOut ? "failed" : x.complete ? "complete" : x.active ? "running" : "pending"}</span>
                {x.created ? <span className="t-secondary"> · {clock(x.created)}</span> : null}</div>)}
              <div className="t-secondary">{jobs.complete ? `The newest ${jobs.jobs.length} of ${jobs.owned}.` : `The newest among the first ${jobs.read} read; more exist${jobs.remaining !== undefined ? ` (${jobs.remaining} more)` : " (count unknown)"}.`}
                {j.failedLimit !== undefined ? ` KEDA keeps ${j.failedLimit} failed Jobs.` : " KEDA keeps 100 failed Jobs by default."}</div>
              <div className="t-secondary">A Job's failure is the workload's, not KEDA's: Jobs are not part of the verdict.</div>
            </div>
          )}
        </InspectorBlock>
        <Triggers triggers={j.triggers} cut={j.triggersCut} count={j.triggerCount} />
        <PauseBlock p={j.pause} resource="scaledjobs" version={j.version} onPause={onPause} gone={gone} />
        {!gone && <AuthEvidence cluster={cluster} resource="scaledjobs" ns={j.namespace} name={j.name} uid={j.uid} rev={evidenceKey(j.evidenceRev, j.triggers, j.triggerCount, j.triggersCut)} refs={j.triggers.filter((t) => t.authName).length} />}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function AuthInspector({ cluster, a, gone, onClose }: { cluster: string; a: KEDAAuthDTO; gone: Gone; onClose: () => void }) {
  const kind = a.cluster ? "ClusterTriggerAuthentication" : "TriggerAuthentication";
  const describe = useDescribe(cluster, !gone ? { kind, namespace: a.namespace ?? "", name: a.name, uid: a.uid } : null);
  return (
    <div data-testid="keda-auth-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: a.uid || undefined }} kind={kind} name={a.name} meta={a.namespace || "cluster-scoped"} onClose={onClose}
        verdict={gone ? goneVerdict(kind, gone) : <div role="status" className="t-body">A fact: KEDA reports no conditions on its authentications. A failure shows on the scaler that uses it.</div>}
        more={describe.menu}
      >
        <InspectorBlock title="Sources">
          {a.sources.length === 0 && !a.podIdentity && <div className="t-secondary">none</div>}
          {a.sources.map((s) => (
            <div key={s.kind} className="t-body">{s.kind}{s.method ? ` (${s.method})` : ""}{s.detail ? <span className="t-secondary"> · {s.detail}</span> : null}
              {s.refs.map((r, i) => <div key={i} className="t-secondary">{r.kind} {r.name}{r.key ? ` · key ${r.key}` : ""}{r.parameter ? ` → ${r.parameter}` : ""}</div>)}
              {s.cut && <div className="t-secondary">+{s.refCount - s.refs.length} more</div>}
            </div>
          ))}
          {a.podIdentity && <div className="t-body">podIdentity {a.podIdentity.provider}{a.podIdentity.identityOwner ? ` · owner ${a.podIdentity.identityOwner}` : ""}
            {a.podIdentity.roleArn ? <span className="t-secondary"> · role {a.podIdentity.roleArn}</span> : null}</div>}
          <div className="t-secondary">Named only: no value is read. KEDA always authenticates as its operator pod; the subject is resolved in each scaler's inspector.</div>
        </InspectorBlock>
        <InspectorBlock title={`Used by · ${a.users.length}${a.usersComplete ? "" : "+"}`}>
          {a.users.map((u) => <div key={u} className="t-body">{u}</div>)}
          {a.users.length === 0 && <div className="t-secondary">{a.usersComplete ? "No scaler references it." : "None found, but the scalers' read is incomplete or a trigger list was cut: never unused."}</div>}
          {a.users.length > 0 && !a.usersComplete && <div className="t-secondary">The scalers' read is incomplete or a trigger list was cut: there may be more.</div>}
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function HPAInspector({ cluster, h, gone, onClose }: { cluster: string; h: HPARowDTO; gone: Gone; onClose: () => void }) {
  const ro = h.rule ? roleKind(h.rule.role) : "unknown";
  const describe = useDescribe(cluster, !gone ? { kind: "HorizontalPodAutoscaler", namespace: h.namespace, name: h.name, uid: h.uid } : null);
  const ai = useInspectorAI(h.uid && h.rule ? { kind: "row" as const, task: "autoscaling", part: "hpas", uid: h.uid } : null, `HorizontalPodAutoscaler/${h.namespace}/${h.name}`, !gone, cluster);
  return (
    <div data-testid="hpa-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: h.uid || undefined }} kind="HorizontalPodAutoscaler" name={h.name} meta={h.namespace} onClose={onClose}
        verdict={gone ? goneVerdict("HPA", gone) : h.rule ? <InspectorStatus tone={toneOf(ro)} kind={ro} label={h.rule.label} reason={why(h.reason, h.rule.label) || undefined} />
          : <div role="status" className="t-body" data-testid="hpa-keda-owned">KEDA's: this HPA is judged under ScaledObject {h.kedaOwner}, never counted twice.</div>}
        more={[...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Scaling">
          <div className="insp-kv">
            <span>target</span><span>{h.target.kind}/{h.target.name} <span className="t-secondary">· {h.target.apiVersion}</span></span>
            <span>replicas</span><span>{h.current} → {h.desired} (min {h.min ?? 1}, max {h.max})</span>
            <span>metrics</span><span>{h.metrics}{h.metricTypes.length ? ` · ${h.metricTypes.join(", ")}` : ""}</span>
            {h.others.length > 0 && <><span>also scaling it</span><span style={{ color: "var(--color-text-danger)" }}>{h.others.join(", ")}</span></>}
            <span>generation</span><span>{h.generationReported ? `${h.generation}, observed ${h.observedGeneration}` : `${h.generation} · the HPA controller does not report which generation it observed`}</span>
          </div>
        </InspectorBlock>
        <InspectorBlock title={`Conditions · ${h.conditions.length}${h.conditionsCut ? "+" : ""}`}>
          {h.conditions.map((c: HPAConditionDTO) => <div key={c.type} className="t-body" style={{ overflowWrap: "anywhere" }}>{condText(c)}{c.at ? <span className="t-secondary"> · since {clock(c.at)}</span> : null}</div>)}
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}
