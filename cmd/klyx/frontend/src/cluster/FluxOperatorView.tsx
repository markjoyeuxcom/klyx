import { useEffect, useMemo, useRef, useState } from "react";
import { IconTopologyStar3 } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { RuleDTO } from "../store/fleet";
import { fluxOpAct, getFluxOpDetail, type FluxInstanceDTO, type FluxOpCommonDTO, type FluxOpDetailDTO, type FluxOpVerb, type FluxProviderDTO, type FluxReportDTO, type FluxSetDTO } from "../bridge/fluxop";
import { useFluxOperatorRows } from "../health/useFluxOperatorRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { Age, formatAge, useNow } from "../chrome/Age";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { clock, coverageGaps, kindEvidence, lastGoodAt, type KindEvidence } from "../layout/coverage";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { CountSentence, MarkedPhrase } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useDescribe } from "../chrome/describe/useDescribe";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { LensParts } from "../chrome/LensParts";
import { useNavIntent } from "../chrome/useNavIntent";
import { kindOfRef } from "../nav/reveal";
import { expectPin } from "../nav/inspectorPin";

// FluxOperatorView - the Flux lens's Operator view (coverage S1a §2): the
// FluxInstance, the ResourceSets and their input providers from the
// Observer's shared rows, broken-first, with the FluxReport as the
// operator's own account. Roles are the Go rule's (internal/gitops/fluxop):
// a suspended object is intended; a resumed one is "awaiting evaluation"
// until the operator evaluates it, and is not counted meanwhile.

type Kind = "FluxInstance" | "ResourceSet" | "ResourceSetInputProvider";
type Item = { key: string; kind: Kind; c: FluxOpCommonDTO; instance?: FluxInstanceDTO; set?: FluxSetDTO; provider?: FluxProviderDTO };
// Target is what a verb is armed against: an Item, or a FluxReport.
type Target = { key: string; kind: Kind | "FluxReport"; c: Pick<FluxOpCommonDTO, "uid" | "namespace" | "name" | "suspended" | "owner">; provider?: FluxProviderDTO };
const reportTarget = (r: FluxReportDTO): Target => ({ key: `FluxReport/${r.namespace}/${r.name}`, kind: "FluxReport", c: { uid: r.uid, namespace: r.namespace, name: r.name, suspended: false } });
type Role = { kind: "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused"; label: string };

const KIND_SHORT: Record<Kind, string> = { FluxInstance: "instance", ResourceSet: "resourceset", ResourceSetInputProvider: "provider" };
const KIND_ORDER: Record<Kind, number> = { FluxInstance: 0, ResourceSet: 1, ResourceSetInputProvider: 2 };
const RANK: Record<string, number> = { fault: 0, degraded: 1, progressing: 2, unknown: 3, paused: 4, ok: 5 };
const rankOf = (r: RuleDTO) => RANK[r.role] ?? RANK.unknown; // fail closed: an unknown role ranks as unknown
const roleOf = (r: RuleDTO): Role => ({ kind: (r.role in RANK ? r.role : "unknown") as Role["kind"], label: r.label });
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const roleTone = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const keyOf = (c: FluxOpCommonDTO) => `${c.kind}/${c.namespace}/${c.name}`;
const unix = (iso?: string) => (iso ? Math.floor(Date.parse(iso) / 1000) : 0);

const COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "kind", label: "Kind", width: 96, priority: 1 },
  { id: "name", label: "Name", width: 220, priority: 0, flex: true },
  { id: "status", label: "Status", width: 220, priority: 0 },
  { id: "detail", label: "Detail", width: 220, priority: 2, grow: 0.5 },
  { id: "age", label: "Age", width: 56, priority: 1 },
];

function detailText(it: Item, nowS: number): string {
  if (it.instance) return [it.instance.version && `Flux ${it.instance.version}`, it.instance.syncName && `sync ${it.instance.syncName}`].filter(Boolean).join(" · ") || "-";
  if (it.set) return it.c.progress || [it.set.inputCount ? `${it.set.inputCount} inline input${it.set.inputCount === 1 ? "" : "s"}` : "", it.set.inputsFrom.length ? `${it.set.inputsFrom.length} provider ref${it.set.inputsFrom.length === 1 ? "" : "s"}` : "", it.set.inventoryCount ? `${it.set.inventoryCount} objects` : ""].filter(Boolean).join(" · ") || "-";
  if (it.provider) {
    const next = it.provider.nextSchedule?.when ? ` · next ${formatAge(Math.max(0, unix(it.provider.nextSchedule.when) - nowS))}` : "";
    return `${it.provider.type} · ${it.provider.exportedInputCount} input set${it.provider.exportedInputCount === 1 ? "" : "s"}${next}`;
  }
  return "-";
}

export function FluxOperatorView({ cluster }: { cluster: string }) {
  const rows = useFluxOperatorRows(cluster);
  const nowS = Math.floor(useNow() / 1000);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const [kindFilter, setKindFilter] = useState<"" | Kind>("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const [pending, setPending] = useState<{ cluster: string; item: Target; verb: FluxOpVerb } | null>(null);
  const listRef = useRef<VirtualListHandle>(null);

  const evidenceOf = (kind: string): KindEvidence => kindEvidence(!rows.loading, rows.coverage, kind, rows.watchError);
  const kindCov = (kind: string) => rows.coverage.find((k) => k.kind === kind)?.coverage;
  const absent = ["FluxInstance", "ResourceSet", "ResourceSetInputProvider"].every((k) => kindCov(k) === "absent");
  const items = useMemo<Item[]>(() => {
    const out: Item[] = [];
    for (const i of rows.instances) out.push({ key: keyOf(i), kind: "FluxInstance", c: i, instance: i });
    for (const r of rows.sets) {
      if (r.set) out.push({ key: keyOf(r.set), kind: "ResourceSet", c: r.set, set: r.set });
      else if (r.provider) out.push({ key: keyOf(r.provider), kind: "ResourceSetInputProvider", c: r.provider, provider: r.provider });
    }
    return out.sort((a, b) => rankOf(a.c.rule) - rankOf(b.c.rule) || KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || a.c.namespace.localeCompare(b.c.namespace) || a.c.name.localeCompare(b.c.name));
  }, [rows.instances, rows.sets]);
  const visible = useMemo(() => items.filter((it) => !kindFilter || it.kind === kindFilter), [items, kindFilter]);
  // A FluxReport belongs to the instance of its own namespace and name, and
  // only when one operator is proven to reconcile it (Codex's S1a
  // implementation review, finding 10): never the first report found.
  const reportFor = (i: FluxInstanceDTO): FluxReportDTO | undefined =>
    i.binding === "bound" ? rows.reports.find((r) => r.namespace === i.namespace && r.name === i.name) : undefined;
  const headReport: FluxReportDTO | undefined = rows.reports.length === 1 ? rows.reports[0] : undefined;

  const rowsRefreshing = rows.rowsState.refreshing;
  const rowsAsOf = rows.rowsState.evaluatedAt ? clock(rows.rowsState.evaluatedAt) : "the last read";
  const lastGood = lastGoodAt(rows.coverage, ["FluxInstance", "ResourceSet", "ResourceSetInputProvider"]);
  const discoveryGap = coverageGaps(rows.coverage);
  const capped = rows.coverage.filter((k) => k.truncated);

  const counted = (rows.partCounts.fluxinstance?.total ?? 0) + (rows.partCounts.resourcesets?.total ?? 0);
  const attention = (rows.partCounts.fluxinstance?.included ?? 0) + (rows.partCounts.resourcesets?.included ?? 0);
  const faults = items.filter((i) => i.c.rule.role === "fault").length;
  const awaiting = items.filter((i) => i.c.resume === "awaiting" || i.c.rule.label === "Awaiting evaluation").length;
  const suspended = items.filter((i) => i.c.suspended).length;
  const unbound = rows.instances.filter((i) => i.binding === "orphan" || i.binding === "ambiguous");

  const keys = visible.map((i) => i.key);
  const cursorIdx = cursorKey === null ? -1 : keys.indexOf(cursorKey);
  useListKeys({
    count: keys.length,
    selected: cursorIdx,
    onSelect: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); setSelKey(k); } },
    onEscape: () => setSelKey(null),
  });
  useEffect(() => { setCursorKey(null); setPending(null); }, [cluster]);
  // A summary Top link (health 1c slice 4 §5.4): select the object - selection
  // only, never an action. By UID it is pinned at once; a missing row waits
  // until its kind's read is complete.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "flux" || (intent.part !== "fluxinstance" && intent.part !== "resourcesets")) return "wait";
    const kind = kindOfRef(intent.ref);
    const key = `${kind}/${intent.ref.namespace ?? ""}/${intent.ref.name}`;
    const found = items.find((i) => i.key === key);
    if (intent.ref.uid) expectPin(cluster, "FluxOperator", key, intent.ref.uid);
    else if (!found) return kind && evidenceOf(kind) === "complete" ? "missing" : "wait";
    setKindFilter("");
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [items]);

  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const selKind = (selKey?.split("/")[0] ?? "") as Kind;
  // The whole selected Item is pinned and held (Codex's S1a implementation
  // review, finding 8): a same-name replacement never shows its data as the
  // pinned object's last evidence until it is explicitly adopted.
  const liveSel = useMemo(() => {
    const it = selKey ? items.find((i) => i.key === selKey) : undefined;
    return it ? { ...it, uid: it.c.uid } : undefined;
  }, [items, selKey]);
  const pin = usePinnedSubject(cluster, selKey, liveSel, selKind ? evidenceOf(selKind) === "complete" : false, "FluxOperator");
  const selItem: Item | undefined = pin.subject ?? undefined;
  const gone: "removed" | "unverified" | null = pin.gone === "replaced" ? "removed" : pin.gone;

  let state: DatasetState = "ready";
  const anyEv = (["FluxInstance", "ResourceSet", "ResourceSetInputProvider"] as const).map(evidenceOf);
  if (absent) state = "empty";
  else if (anyEv.every((e) => e === "failed") && items.length === 0) state = "unreadable";
  else if (anyEv.some((e) => e === "pending") && items.length === 0) state = "loading";
  else if (visible.length === 0) state = "empty";
  const icon = <IconTopologyStar3 size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading the Flux Operator's objects" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not read the Flux Operator's objects." hint={rows.watchError ?? undefined} />
    : absent ? <EmptyState icon={icon} title="The Flux Operator is not installed on this cluster." hint="Klyx observes no fluxcd.controlplane.io FluxInstances, ResourceSets or providers here." />
    : items.length > 0 ? <EmptyState icon={icon} title="Nothing of this kind." hint={`${items.length} operator objects on this cluster.`} action={{ label: "Clear filter", onClick: () => setKindFilter("") }} />
    : <EmptyState icon={icon} title="No FluxInstance, ResourceSet or provider yet." hint="The operator is installed; it reconciles nothing until a FluxInstance or ResourceSet exists." />;

  const notices: React.ReactNode[] = [];
  if (rows.watchError && items.length > 0) notices.push(<WatchErrorBanner key="watch" tool="Flux Operator" failure={`read failed - showing the last good state${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={rows.watchError} />);
  if (capped.length > 0) notices.push(<div key="capped" role="status" data-testid="fluxop-capped" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)" }}>
    Capped: {capped.map((k) => `${k.notRetained ?? "some"} ${k.kind}${(k.notRetained ?? 2) === 1 ? "" : "s"} not retained`).join(", ")} - Klyx keeps a bounded amount per cluster, so counts are lower bounds and lookups past the cap are incomplete.
  </div>);
  if (unbound.length > 0) notices.push(<div key="unbound" role="status" data-testid="fluxop-unbound" className="t-secondary" style={{ padding: "4px 0" }}>
    {unbound.map((i) => `${i.namespace}/${i.name}: ${i.binding === "orphan" ? "no operator reconciles this namespace" : "several operators reconcile this namespace"}`).join("; ")} - not counted.
  </div>);
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={{ padding: "4px 0" }}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);

  const row = (it: Item) => {
    const role = roleOf(it.c.rule);
    return (
      <div key={it.key} role="button" tabIndex={0} className="lens-row" style={gridStyle}
        aria-selected={it.key === cursorKey} data-cursor={it.key === cursorKey ? "true" : undefined} data-selected={it.key === selKey ? "true" : undefined}
        onClick={() => { setCursorKey(it.key); setSelKey(it.key === selKey ? null : it.key); }}
        onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(it.key); } }}>
        {show("mark") && <VerdictMark health={role} />}
        {show("kind") && <span style={{ ...cellStyle, color: "var(--color-text-tertiary)" }}>{KIND_SHORT[it.kind]}</span>}
        {show("name") && <span className="lens-cell-name"><span style={{ color: "var(--color-text-tertiary)" }}>{it.c.namespace}/</span>{it.c.name}</span>}
        {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontFamily: "var(--font-sans)" }} title={it.c.message || undefined}>{role.label}{role.kind === "fault" && it.c.reason ? ` · ${it.c.reason}` : ""}</span>}
        {show("detail") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{detailText(it, nowS)}</span>}
        {show("age") && <span style={{ color: "var(--color-text-tertiary)" }}>{it.c.created ? formatAge(Math.max(0, nowS - unix(it.c.created))) : "-"}</span>}
      </div>
    );
  };

  const chip = (k: "" | Kind, label: string) => {
    const n = k ? items.filter((i) => i.kind === k && i.c.rule.included).length : 0;
    return <button className="l-chip" aria-pressed={kindFilter === k} onClick={() => { setKindFilter(k); setSelKey(null); setCursorKey(null); }}>{label}{n > 0 && <span style={{ color: "var(--color-text-danger)" }}> {n}</span>}</button>;
  };

  const confirm = pending && pending.cluster === cluster ? pending : null;
  const send = (p: { item: Target; verb: FluxOpVerb }) => {
    setPending(null);
    const curUID = p.item.kind === "FluxReport"
      ? rows.reports.find((r) => r.namespace === p.item.c.namespace && r.name === p.item.c.name)?.uid
      : items.find((i) => i.key === p.item.key)?.c.uid;
    if (!curUID || curUID !== p.item.c.uid) {
      useFleet.getState().setActionStatus({ kind: "error", message: `${p.item.kind} ${p.item.c.namespace}/${p.item.c.name} was replaced since the action was armed - nothing was sent` });
      return;
    }
    void fluxOpAct(cluster, p.item.kind, p.item.c.namespace, p.item.c.name, p.item.c.uid, p.verb).then((r) => {
      useFleet.getState().setActionStatus(r.ok
        ? { kind: "success", message: `${verbWord(p.verb)} requested for ${p.item.kind} ${p.item.c.namespace}/${p.item.c.name}${p.item.c.suspended && p.verb !== "resume" ? " - the object is suspended" : ""}` }
        : { kind: "error", message: r.error });
    });
  };

  return (
    <>
      <LensLayout
        label="Flux"
        title="Flux CD"
        part="flux-operator"
        summary={absent || items.length === 0 ? null : (
          <>
            <CountSentence total={counted} marker={anyEv.every((e) => e === "complete") ? "" : "+"} noun={["operator object", "operator objects"]} />
            {faults > 0 ? <MarkedPhrase fault>{`${faults} failing`}</MarkedPhrase> : attention > 0 ? <span className="t-secondary">{attention} not ready</span> : <span className="t-secondary">none failing</span>}
            {awaiting > 0 && <span className="t-secondary" data-testid="fluxop-awaiting">{awaiting} awaiting evaluation</span>}
            {suspended > 0 && <span className="t-secondary">{suspended} suspended</span>}
          </>
        )}
        headerEnd={headReport ? <span className="t-secondary" data-testid="fluxop-report-head" title={`The FluxReport ${headReport.namespace}/${headReport.name} is the operator's own account, not Klyx's counts.`}>
          {headReport.distributionVersion ? `Flux ${headReport.distributionVersion}` : "Flux"}{headReport.operatorVersion ? ` · operator ${headReport.operatorVersion}` : ""} · as reported by the operator in {headReport.namespace}
        </span> : rows.reports.length > 1 ? <span className="t-secondary" data-testid="fluxop-report-head" title="Each FluxReport is its operator's own account; select an instance to read its own.">
          {rows.reports.length} FluxReports · one per operator namespace
        </span> : undefined}
        controls={(
          <>
            <LensParts cluster={cluster} groupId="flux" />
            {!absent && (
              <div role="group" aria-label="Operator kinds" className="l-seg">
                {chip("", "All")}
                {chip("FluxInstance", "Instance")}
                {chip("ResourceSet", "ResourceSets")}
                {chip("ResourceSetInputProvider", "Providers")}
              </div>
            )}
          </>
        )}
        notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2 }}>{notices}</div> : undefined}
        state={state}
        stateView={stateView}
        body={
          <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
            <div className="lens-table-head" role="row" style={gridStyle}>
              {COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
            </div>
            <VirtualList ref={listRef} items={visible} rowHeight={TABLE_ROW_HEIGHT} rowKey={(i) => i.key} style={{ flex: 1, minHeight: 0 }} render={row} />
          </LensTable>
        }
        footer={<span className="t-legend">j k navigate · ↵ inspect</span>}
        footerEnd={<span className="t-secondary">broken first · suspended is intended · a resumed object is not counted until evaluated</span>}
        inspector={selItem ? <OperatorInspector cluster={cluster} it={selItem} report={selItem.instance ? reportFor(selItem.instance) : undefined} reportReadAt={rows.reportReadAt}
            items={items} nowS={nowS} gone={gone} replaced={pin.gone === "replaced" ? pin.adopt : undefined}
            onVerb={(verb) => setPending({ cluster, item: selItem, verb })}
            onReportVerb={(r) => setPending({ cluster, item: reportTarget(r), verb: "refresh" })} onClose={() => setSelKey(null)} />
          : selKey && pin.expected && pin.gone ? <PinnedGone cluster={cluster} kind={selKind} name={selKey.split("/").slice(1).join("/")} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={() => setSelKey(null)} />
          : undefined}
        inspectorLabel="Operator object details"
        onCloseInspector={() => setSelKey(null)}
      />
      {confirm && (
        <ConfirmDialog
          title={verbTitle(confirm.verb, confirm.item)}
          cluster={confirm.cluster}
          detail={verbDetail(confirm.verb, confirm.item)}
          protected={isProtected}
          danger={confirm.verb === "suspend"}
          confirmLabel={verbWord(confirm.verb)}
          onCancel={() => setPending(null)}
          onConfirm={() => send(confirm)}
        />
      )}
    </>
  );
}

function verbWord(v: FluxOpVerb): string {
  return { reconcile: "Reconcile", fetch: "Fetch now", suspend: "Suspend", resume: "Resume", refresh: "Refresh the report" }[v];
}

function verbTitle(v: FluxOpVerb, it: Target): string {
  if (v === "fetch") return "Fetch now, outside its schedule";
  return `${verbWord(v)} ${it.kind}`;
}

// The confirmation names the object, the operator's mechanism, and who
// applies it (roadmap C4): an owner's next apply may undo the annotation.
function verbDetail(v: FluxOpVerb, it: Target): string {
  const what = `${it.kind} ${it.c.namespace}/${it.c.name}`;
  const mech = {
    reconcile: "Sets reconcile.fluxcd.io/requestedAt to a fresh token.",
    fetch: "Sets requestedAt and forceAt to the same fresh token, so the operator fetches outside the schedule.",
    suspend: "Sets fluxcd.controlplane.io/reconcile: disabled. Its conditions freeze until it is resumed.",
    resume: "Sets fluxcd.controlplane.io/reconcile: enabled and a fresh requestedAt. Klyx reads it as awaiting evaluation until the operator's status changes.",
    refresh: "Sets reconcile.fluxcd.io/requestedAt on the report, so the operator recomputes it now rather than at its next interval. The report is the operator's own object; nothing it reports about is changed.",
  }[v];
  const owner = it.c.owner ? ` Owned by ${it.c.owner.kind} ${it.c.owner.namespace ? `${it.c.owner.namespace}/` : ""}${it.c.owner.name}: an annotation set here may be removed when it applies.` : "";
  const suspendedNote = it.c.suspended && v !== "resume" ? " The object is suspended: the request waits until it is resumed." : "";
  return `${what}. ${mech}${owner}${suspendedNote}`;
}

function OperatorInspector({ cluster, it, report, reportReadAt, items, nowS, gone, replaced, onVerb, onReportVerb, onClose }: {
  cluster: string; it: Item; report?: FluxReportDTO; reportReadAt: string | null; items: Item[]; nowS: number;
  gone: "removed" | "unverified" | null; replaced?: () => void; onVerb: (v: FluxOpVerb) => void; onReportVerb: (r: FluxReportDTO) => void; onClose: () => void;
}) {
  const role = roleOf(it.c.rule);
  const describe = useDescribe(cluster, !gone ? { kind: it.kind, namespace: it.c.namespace, name: it.c.name, uid: it.c.uid } : null);
  const [detail, setDetail] = useState<{ uid: string; d: FluxOpDetailDTO } | null>(null);
  useEffect(() => {
    let live = true;
    setDetail(null);
    if (gone) return;
    const uid = it.c.uid;
    void getFluxOpDetail(cluster, it.kind, it.c.namespace, it.c.name, uid).then((d) => { if (live) setDetail({ uid, d }); });
    return () => { live = false; };
  }, [cluster, it.kind, it.c.namespace, it.c.name, it.c.uid, gone]);
  // the reply is for the UID asked about, or the bridge's refusal of a
  // replacement: never another object's data under this one's identity
  const d = detail && detail.uid === it.c.uid ? detail.d : null;

  const verbs: MenuItem[] = gone ? [] : [
    ...(it.c.suspended
      ? [{ label: "Resume", onSelect: () => onVerb("resume") }]
      : [
        { label: "Reconcile", onSelect: () => onVerb("reconcile") },
        ...(it.provider && it.provider.schedule.length > 0 ? [{ label: "Fetch now, outside its schedule", onSelect: () => onVerb("fetch") }] : []),
        { label: "Suspend", danger: true, onSelect: () => onVerb("suspend") },
      ]),
    // the report's own verb (S1a §5), armed with the report's UID
    ...(report ? [{ label: "Refresh the report", onSelect: () => onReportVerb(report) }] : []),
  ];
  const frozen = it.c.ready ? `${it.c.ready}${it.c.reason ? ` (${it.c.reason})` : ""}${it.c.message ? `: ${it.c.message}` : ""}` : "no Ready condition";
  const note = it.c.suspended ? `Suspended - an intended pause. Its conditions are frozen; last known before suspension: ${frozen}.`
    : it.c.resume === "awaiting" ? (it.provider && it.provider.schedule.length > 0
      ? `Resumed, awaiting its schedule. Last known before suspension: ${frozen}. A scheduled provider is not evaluated until its window opens; "Fetch now" evaluates it. It is not counted meanwhile.`
      : `Resumed, awaiting evaluation. Last known before suspension: ${frozen}. It is not counted until the operator evaluates it.`)
    : it.c.resume === "unproven" ? `Klyx lost track of this object's suspension while it was out of its reads (its budget), so it cannot tell whether the operator has evaluated it since. Last known: ${frozen}. It is not counted until its status changes.`
    : it.c.rule.label === "Awaiting evaluation" ? `Not yet evaluated: the operator initialized it while reconciliation was disabled (${frozen}).`
    : it.c.resume === "evaluating" ? "Resumed - the operator is evaluating it now."
    : it.instance?.binding === "orphan" ? "No operator reconciles this namespace: the operator only reconciles a FluxInstance in its runtime namespace. Not counted."
    : it.instance?.binding === "ambiguous" ? "Several operators reconcile this namespace - Klyx does not pick one. Not counted."
    : null;

  return (
    <div data-testid="fluxop-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: it.c.uid || undefined }} replaced={replaced} kind={it.kind} name={it.c.name} meta={it.c.namespace} onClose={onClose}
        verdict={gone ? (
          <div role="status" className="t-body"><span className="t-status">{gone === "removed" ? `${it.kind} removed.` : `Not in the current ${it.kind} read.`}</span> The details below are its last evidence.</div>
        ) : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} reason={role.kind === "fault" ? it.c.reason : undefined} />
            {role.kind === "fault" && it.c.message && <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>{it.c.message}</div>}
            {it.c.progress && <div className="t-body" style={{ color: "var(--color-text-secondary)", marginTop: 4 }}>{it.c.progress}</div>}
            {note && <div className="t-body" data-testid="fluxop-note" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: 4 }}>{note}</div>}
            {it.c.clipped.length > 0 && <div className="t-secondary" style={{ marginTop: 4 }}>Some long fields are shortened in this view: {it.c.clipped.slice(0, 4).join(", ")}{it.c.clipped.length > 4 ? "…" : ""}.</div>}
          </div>
        )}
        more={[...verbs, ...(verbs.length ? [{ kind: "separator" as const }] : []), ...describe.menu]}
      >
        {it.instance && <InstanceBlocks i={it.instance} />}
        {it.instance && report && <ReportBlock r={report} readAt={reportReadAt} />}
        {it.instance && !report && it.instance.binding !== "bound" && <InspectorBlock title="Report"><div className="t-secondary">Its FluxReport is not shown: no single operator is proven to reconcile this namespace.</div></InspectorBlock>}
        {it.set && <SetBlocks s={it.set} readyExprs={d?.readyExprs} />}
        {it.provider && <ProviderBlocks p={it.provider} nowS={nowS} consumers={items.filter((x) => x.set && x.set.inputsFrom.some((r) => r.name === it.c.name && x.c.namespace === it.c.namespace))} />}
        {d?.error ? <InspectorBlock title="History"><div className="t-body" style={{ color: "var(--color-text-warning)" }}>Could not read its history: {d.error}</div></InspectorBlock> : d && (
          <>
            {d.history.length > 0 && (
              <InspectorBlock title="History (newest first)">
                {d.history.map((h, i) => (
                  <div key={i} className="t-body" style={{ color: "var(--color-text-secondary)" }}>
                    {h.status || "unknown"} · {h.totalReconciliations} run{h.totalReconciliations === 1 ? "" : "s"}{h.lastReconciled ? <> · last <Age unixSeconds={unix(h.lastReconciled)} suffix=" ago" /></> : null}{h.metadata ? ` · ${Object.entries(h.metadata).map(([k, v]) => `${k} ${v}`).join(", ")}` : ""}
                  </div>
                ))}
              </InspectorBlock>
            )}
            {(it.set || it.instance) && d.inventoryTotal > 0 && (
              <InspectorBlock title={`Inventory · ${d.inventoryTotal} object${d.inventoryTotal === 1 ? "" : "s"}`}>
                {d.inventory.slice(0, 12).map((e) => <div key={`${e.group}/${e.kind}/${e.namespace}/${e.name}`} className="t-body"><span style={{ color: "var(--color-text-tertiary)" }}>{e.kind} </span>{e.namespace ? `${e.namespace}/` : ""}{e.name}</div>)}
                {d.inventoryTotal > 12 && <div className="t-secondary">and {d.inventoryTotal - 12} more</div>}
              </InspectorBlock>
            )}
            <InspectorBlock title="Events">
              {d.eventsError ? <div className="t-body" style={{ color: "var(--color-text-warning)" }}>Events could not be read: {d.eventsError}</div>
                : d.events.length === 0 ? <div className="t-secondary">No recent events (they age out after about an hour).</div>
                : d.events.slice(0, 8).map((e, i) => <div key={i} className="t-body" style={{ color: e.type === "Warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{e.reason}: {e.message}</div>)}
            </InspectorBlock>
          </>
        )}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function InstanceBlocks({ i }: { i: FluxInstanceDTO }) {
  return (
    <>
      <InspectorBlock title="Distribution">
        <div className="insp-kv">
          <span>declared</span><span>{[i.version, i.variant, i.registry].filter(Boolean).join(" · ") || "-"}</span>
          {i.artifact && <><span>artifact</span><span style={{ overflowWrap: "anywhere" }}>{i.artifact}</span></>}
          <span>components</span><span>{i.components.length ? i.components.join(", ") : "the default set"}</span>
          {i.clusterType && <><span>cluster</span><span>{[i.clusterType, i.multitenant && "multitenant", i.networkPolicy && "network policy", i.clusterSize && `size ${i.clusterSize}`].filter(Boolean).join(" · ")}</span></>}
          {i.shards.length > 0 && <><span>sharding</span><span>{i.shardKey ? `${i.shardKey}: ` : ""}{i.shards.join(", ")}</span></>}
          {i.patchCount ? <><span>patches</span><span>{i.patchCount} (contents not shown: they can carry secrets)</span></> : null}
        </div>
      </InspectorBlock>
      <InspectorBlock title="Running components">
        {i.running.length === 0 ? <div className="t-secondary">None reported yet.</div> : i.running.map((c) => (
          <div key={c.name} className="t-body"><span>{c.name}</span> <span style={{ color: "var(--color-text-tertiary)" }}>{c.tag}{c.digest ? ` @${c.digest.replace(/^sha256:/, "").slice(0, 12)}` : ""}</span></div>
        ))}
      </InspectorBlock>
      {i.sync && (
        <InspectorBlock title="Sync">
          <div className="insp-kv">
            <span>source</span><span style={{ overflowWrap: "anywhere" }}>{i.sync.kind} {i.sync.url}</span>
            {i.sync.ref && <><span>ref</span><span>{i.sync.ref}</span></>}
            {i.sync.path && <><span>path</span><span>{i.sync.path}</span></>}
            <span>creates</span><span>{i.sync.kind} and Kustomization {i.namespace}/{i.syncName}</span>
            {i.lastApplied && <><span>applied</span><span style={{ overflowWrap: "anywhere" }}>{i.lastApplied}</span></>}
            {i.lastAttempted && i.lastAttempted !== i.lastApplied && <><span>attempted</span><span style={{ overflowWrap: "anywhere" }}>{i.lastAttempted}</span></>}
          </div>
        </InspectorBlock>
      )}
    </>
  );
}

// The FluxReport: the operator's numbers, never Klyx's. Its Ready says
// reporting succeeded, never that Flux is healthy; two times kept apart.
function ReportBlock({ r, readAt }: { r: FluxReportDTO; readAt: string | null }) {
  return (
    <InspectorBlock title="Report · as reported by the operator">
      <div className="insp-kv" data-testid="fluxop-report">
        <span>reporting</span><span>{r.reportingSucceeded ? "reporting succeeded" : "reporting did not succeed"}</span>
        <span>times</span><span>{r.conditionChanged ? <>condition last changed <Age unixSeconds={unix(r.conditionChanged)} suffix=" ago" /></> : "condition time not reported"}{readAt ? ` · read by Klyx ${clock(readAt)}` : ""}</span>
        {r.distributionStatus && <><span>distribution</span><span>{[r.distributionVersion, r.distributionStatus, r.entitlement].filter(Boolean).join(" · ")}</span></>}
        {r.managedBy && <><span>managed by</span><span>{r.managedBy}</span></>}
        {r.operatorVersion && <><span>operator</span><span>{[r.operatorVersion, r.operatorPlatform].filter(Boolean).join(" · ")}</span></>}
        {r.serverVersion && <><span>cluster</span><span>{[r.serverVersion, r.platform, r.nodes ? `${r.nodes} nodes` : ""].filter(Boolean).join(" · ")}</span></>}
      </div>
      {r.reconcilers.length > 0 && (
        <div style={{ marginTop: 6 }}>
          {r.reconcilers.map((x) => (
            <div key={x.kind} className="t-body" style={{ color: x.failing > 0 ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>
              {x.kind}: {x.running} running · {x.failing} failing · {x.suspended} suspended{x.totalSize ? ` · ${x.totalSize}` : ""}
            </div>
          ))}
        </div>
      )}
      {r.components.some((c) => !c.ready) && <div className="t-body" style={{ color: "var(--color-text-warning)", marginTop: 6 }}>Not ready as reported: {r.components.filter((c) => !c.ready).map((c) => c.name).join(", ")}</div>}
      <div className="t-secondary" style={{ marginTop: 6 }}>These are the operator's numbers. Klyx's own counts are the Reconciliation and Sources views.</div>
    </InspectorBlock>
  );
}

function SetBlocks({ s, readyExprs }: { s: FluxSetDTO; readyExprs?: Record<string, string> }) {
  return (
    <>
      <InspectorBlock title="Inputs">
        <div className="insp-kv">
          {s.inputStrategy && <><span>strategy</span><span>{s.inputStrategy}</span></>}
          <span>inline</span><span>{s.inputCount ? `${s.inputCount} input set${s.inputCount === 1 ? "" : "s"} (values not shown)` : "none"}</span>
          <span>providers</span><span>{s.inputsFrom.length === 0 ? "none" : s.inputsFrom.map((r) => r.name || (r.selector ? `selector ${r.selector}` : "any")).join(", ")}</span>
          <span>resources</span><span>{[s.resourceCount ? `${s.resourceCount} inline` : "", s.hasTemplate ? "a template" : ""].filter(Boolean).join(" and ") || "none"} (contents not shown: they can carry secrets)</span>
          {s.serviceAccountName && <><span>account</span><span>{s.serviceAccountName}</span></>}
        </div>
      </InspectorBlock>
      {s.dependsOn.length > 0 && (
        <InspectorBlock title="Depends on">
          {s.dependsOn.map((dep, i) => (
            <div key={i} className="t-body" style={{ overflowWrap: "anywhere" }}>
              <span style={{ color: "var(--color-text-tertiary)" }}>{dep.kind} </span>{dep.namespace ? `${dep.namespace}/` : ""}{dep.name}{dep.ready ? " · must be Ready" : ""}
              {dep.hasReadyExpr && <span style={{ color: "var(--color-text-secondary)" }}> · when {readyExprs?.[String(i)] ?? "(reading…)"}</span>}
            </div>
          ))}
        </InspectorBlock>
      )}
      {s.steps.length > 0 && <InspectorBlock title={`Steps · ${s.stepCount ?? s.steps.length}`}><div className="t-body">{s.steps.join(" → ")}</div></InspectorBlock>}
    </>
  );
}

function ProviderBlocks({ p, nowS, consumers }: { p: FluxProviderDTO; nowS: number; consumers: Item[] }) {
  return (
    <>
      <InspectorBlock title="Provider">
        <div className="insp-kv">
          <span>type</span><span>{p.type}</span>
          {p.url && <><span>url</span><span style={{ overflowWrap: "anywhere" }}>{p.url}</span></>}
          {p.secretRef && <><span>secret</span><span>{p.secretRef} (not read)</span></>}
          {p.certSecretRef && <><span>certificates</span><span>{p.certSecretRef} (not read)</span></>}
          <span>exported</span><span>{p.exportedInputCount} input set{p.exportedInputCount === 1 ? "" : "s"}{p.exportedInputCount > 0 ? ` (${Array.from({ length: Math.min(p.exportedInputCount, 10) }, (_, i) => i + 1).join(", ")}${p.exportedInputCount > 10 ? ", …" : ""}; values not shown)` : ""}</span>
          {p.lastExportedRevision && <><span>revision</span><span style={{ overflowWrap: "anywhere" }}>{p.lastExportedRevision.replace(/^sha256:/, "").slice(0, 16)}</span></>}
        </div>
      </InspectorBlock>
      {p.schedule.length > 0 && (
        <InspectorBlock title="Schedule">
          {p.schedule.map((s, i) => <div key={i} className="t-body">{s.cron}{s.timeZone ? ` ${s.timeZone}` : ""}{s.window ? ` · window ${s.window}` : ""}</div>)}
          {p.nextSchedule?.when && <div className="t-secondary" style={{ marginTop: 4 }}>next {unix(p.nextSchedule.when) > nowS ? `in ${formatAge(unix(p.nextSchedule.when) - nowS)}` : "due"} (as the operator computed it)</div>}
        </InspectorBlock>
      )}
      <InspectorBlock title="Used by">
        {consumers.length === 0 ? <div className="t-secondary">No ResourceSet names it. Selectors are not evaluated here.</div>
          : consumers.map((c) => <div key={c.key} className="t-body">{c.c.namespace}/{c.c.name}</div>)}
      </InspectorBlock>
    </>
  );
}
