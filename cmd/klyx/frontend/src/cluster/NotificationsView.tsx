import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { IconBell } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { RuleDTO } from "../store/fleet";
import { getCompositionDetail, getNotifications, type AlertDTO, type CompositionDetailDTO, type NotificationListDTO, type NotificationsDTO, type ProviderDTO, type ReceiverDTO } from "../bridge/fluxcomposition";
import { reconcile, setSuspend } from "../bridge/gitops";
import { useReceiverRows } from "../health/useCompositionRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { Age, formatAge, useNow } from "../chrome/Age";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { clock, kindEvidence, lastGoodAt } from "../layout/coverage";
import { useListKeys } from "../chrome/useListKeys";
import { ROW_MARK, VerdictMark } from "../health/VerdictView";
import { CountSentence, MarkedPhrase } from "../layout/CountSentence";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { useDescribe } from "../chrome/describe/useDescribe";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { LensParts } from "../chrome/LensParts";
import { useNavIntent } from "../chrome/useNavIntent";
import { expectPin } from "../nav/inspectorPin";
import { ownershipLine } from "./ownership";

// NotificationsView - the Flux lens's Notifications view (coverage S1c §2):
// Receivers from the Observer's shared "receivers" rows, with verdicts;
// Providers and Alerts read on demand when the view opens, as facts - no
// verdict, no count, no notification. A Receiver's webhook path, a
// Provider's address and proxy, and Alert and Provider messages never
// cross; dispatch evidence is a count and reasons from the events.

type Tab = "receivers" | "providers" | "alerts";
type Role = { kind: "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused"; label: string };
const RANK: Record<string, number> = { fault: 0, degraded: 1, progressing: 2, unknown: 3, paused: 4, ok: 5 };
const rankOf = (r: RuleDTO) => RANK[r.role] ?? RANK.unknown;
const roleOf = (r: RuleDTO): Role => ({ kind: (r.role in RANK ? r.role : "unknown") as Role["kind"], label: r.label });
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const roleTone = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const keyOf = (kind: string, ns: string, name: string) => `${kind}/${ns}/${name}`;
const unix = (iso?: string) => (iso ? Math.floor(Date.parse(iso) / 1000) : 0);
const secretNote = "named only, never read";

const COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "name", label: "Name", width: 220, priority: 0, flex: true },
  { id: "status", label: "Status", width: 220, priority: 0 },
  { id: "detail", label: "Detail", width: 260, priority: 2, grow: 0.5 },
  { id: "age", label: "Age", width: 56, priority: 1 },
];

// what is past a cap, said
const moreText = (l: NotificationListDTO, noun: string) => (l.more ? `+${l.more > 0 ? l.more : "more"} ${noun} past the ${500}-object read` : "");

type Verb = "reconcile" | "suspend" | "resume";
type Pending = { cluster: string; kind: "Receiver" | "Provider" | "Alert"; namespace: string; name: string; uid: string; verb: Verb; owner?: { kind: string; namespace?: string; name: string } };
function verbDetail(p: Pending): string {
  const what = `${p.kind} ${p.namespace}/${p.name}.`;
  const mech = p.verb === "reconcile" ? "Sets reconcile.fluxcd.io/requestedAt to a fresh token, so notification-controller re-reads its token and spec now."
    : p.verb === "suspend" ? (p.kind === "Receiver" ? "Sets spec.suspend: true. Webhooks to it are refused until it is resumed." : `Sets spec.suspend: true. ${p.kind === "Alert" ? "Its events" : "Events to it"} are not dispatched until it is resumed.`)
    : "Sets spec.suspend: false.";
  const owner = ownershipLine(p.owner, p.verb === "reconcile" ? "annotation" : "suspend");
  return [what, mech, owner, "Klyx reports the request; completion is the object's own status."].filter(Boolean).join(" ");
}

export function NotificationsView({ cluster }: { cluster: string }) {
  const rows = useReceiverRows(cluster);
  const nowS = Math.floor(useNow() / 1000);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const [tab, setTab] = useState<Tab>("receivers");
  const [facts, setFacts] = useState<{ cluster: string; d: NotificationsDTO } | null>(null);
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const [pending, setPending] = useState<Pending | null>(null);
  const listRef = useRef<VirtualListHandle>(null);

  // Providers and Alerts: read when the view opens, kept only while it is open
  const load = useCallback(() => {
    let live = true;
    void getNotifications(cluster).then((d) => { if (live) setFacts({ cluster, d }); });
    return () => { live = false; };
  }, [cluster]);
  useEffect(() => load(), [load]);
  useEffect(() => { setCursorKey(null); setPending(null); setFacts(null); }, [cluster]);
  const notes = facts && facts.cluster === cluster ? facts.d : null;

  const receivers = useMemo(() => [...rows.rows].sort((a, b) => rankOf(a.rule) - rankOf(b.rule) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)), [rows.rows]);
  const providers = notes?.providers ?? [];
  const alerts = notes?.alerts ?? [];
  const recEv = kindEvidence(!rows.loading, rows.coverage, "Receiver", rows.watchError);
  const recAbsent = rows.coverage.find((k) => k.kind === "Receiver")?.coverage === "absent";
  const faults = rows.counts ? rows.counts.fault : receivers.filter((r) => r.rule.role === "fault").length;

  const keys = tab === "receivers" ? receivers.map((r) => keyOf("Receiver", r.namespace, r.name))
    : tab === "providers" ? providers.map((p) => keyOf("Provider", p.namespace, p.name)) : alerts.map((a) => keyOf("Alert", a.namespace, a.name));
  const cursorIdx = cursorKey === null ? -1 : keys.indexOf(cursorKey);
  useListKeys({
    count: keys.length,
    selected: cursorIdx,
    onSelect: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); setSelKey(k); } },
    onEscape: () => setSelKey(null),
  });
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "flux" || intent.part !== "receivers") return "wait";
    const key = keyOf("Receiver", intent.ref.namespace ?? "", intent.ref.name);
    const found = receivers.some((r) => keyOf("Receiver", r.namespace, r.name) === key);
    if (intent.ref.uid) expectPin(cluster, "FluxReceivers", key, intent.ref.uid);
    else if (!found) return recEv === "complete" ? "missing" : "wait";
    setTab("receivers");
    setCursorKey(key);
    setSelKey(key);
    return "consumed";
  }, [receivers]);

  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const selKind = selKey?.split("/")[0] ?? "";
  const liveRec = useMemo(() => (selKind === "Receiver" ? receivers.find((r) => keyOf("Receiver", r.namespace, r.name) === selKey) : undefined), [receivers, selKey, selKind]);
  const pin = usePinnedSubject(cluster, selKind === "Receiver" ? selKey : null, liveRec, recEv === "complete", "FluxReceivers");
  const selProvider = selKind === "Provider" ? providers.find((p) => keyOf("Provider", p.namespace, p.name) === selKey) : undefined;
  const selAlert = selKind === "Alert" ? alerts.find((a) => keyOf("Alert", a.namespace, a.name) === selKey) : undefined;

  const listFor = tab === "receivers" ? null : tab === "providers" ? notes?.providersList : notes?.alertsList;
  let state: DatasetState = "ready";
  if (tab === "receivers") {
    if (recAbsent) state = "empty";
    else if (recEv === "failed" && receivers.length === 0) state = "unreadable";
    else if (recEv === "pending" && receivers.length === 0) state = "loading";
    else if (receivers.length === 0) state = "empty";
  } else if (!notes) state = "loading";
  else if (notes.error || listFor?.error) state = "unreadable";
  else if ((tab === "providers" ? providers : alerts).length === 0) state = "empty";
  const icon = <IconBell size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={4} label={`reading ${tab}`} />
    : state === "unreadable" ? <EmptyState icon={icon} title={`Could not read the ${tab}.`} hint={(tab === "receivers" ? rows.watchError : notes?.error || listFor?.error) ?? undefined} />
    : tab === "receivers" && recAbsent ? <EmptyState icon={icon} title="Flux notifications are not installed on this cluster." hint="Klyx observes no Receiver API here." />
    : listFor?.absent ? <EmptyState icon={icon} title={`The ${tab === "providers" ? "Provider" : "Alert"} API is not served on this cluster.`} />
    : <EmptyState icon={icon} title={`No ${tab} on this cluster.`} />;

  const notices: React.ReactNode[] = [];
  if (tab === "receivers" && rows.watchError && receivers.length > 0) {
    const lg = lastGoodAt(rows.coverage, ["Receiver"]);
    notices.push(<WatchErrorBanner key="watch" tool="Flux Receivers" failure={`read failed - showing the last good state${lg ? ` from ${clock(lg)}` : ""}`} error={rows.watchError} />);
  }
  if (tab !== "receivers" && notes) {
    const more = moreText(listFor ?? {}, tab);
    notices.push(<div key="asof" role="status" className="t-secondary" data-testid="notifications-asof" style={{ padding: "4px 0" }}>
      Read {notes.asOfUnix ? clock(new Date(notes.asOfUnix * 1000).toISOString()) : "just now"}, on demand: Providers and Alerts are facts, without a verdict.{more ? ` ${more}.` : ""}
      {" "}<button className="l-chip" onClick={() => load()}>Read again</button>
    </div>);
    if (notes.eventsError && tab === "alerts") notices.push(<div key="ev" role="status" className="t-secondary" style={{ color: "var(--color-text-warning)" }}>Dispatch evidence could not be read: {notes.eventsError}</div>);
    else if (notes.eventsTruncated && tab === "alerts") notices.push(<div key="evcap" role="status" data-testid="dispatch-capped" className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The events read was capped: dispatch counts are lower bounds, and an Alert with none may still have failures.</div>);
  }

  const rowFor = (key: string, mark: React.ReactNode, name: React.ReactNode, status: React.ReactNode, detail: string, created?: string) => (
    <div key={key} role="button" tabIndex={0} className="lens-row" style={gridStyle}
      aria-selected={key === cursorKey} data-cursor={key === cursorKey ? "true" : undefined} data-selected={key === selKey ? "true" : undefined}
      onClick={() => { setCursorKey(key); setSelKey(key === selKey ? null : key); }}
      onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(key); } }}>
      {show("mark") && mark}
      {show("name") && name}
      {show("status") && status}
      {show("detail") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{detail}</span>}
      {show("age") && <span style={{ color: "var(--color-text-tertiary)" }}>{created ? formatAge(Math.max(0, nowS - unix(created))) : "-"}</span>}
    </div>
  );
  const nameCell = (ns: string, name: string) => <span className="lens-cell-name"><span style={{ color: "var(--color-text-tertiary)" }}>{ns}/</span>{name}</span>;
  const recRow = (r: ReceiverDTO) => {
    const role = roleOf(r.rule);
    return rowFor(keyOf("Receiver", r.namespace, r.name), <VerdictMark health={role} />, nameCell(r.namespace, r.name),
      <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind] }}>{role.label}{role.kind === "fault" && r.reason ? ` · ${r.reason}` : ""}</span>,
      `${r.type ?? "-"} · ${r.resourceCount} resource${r.resourceCount === 1 ? "" : "s"}`, r.created);
  };
  const provRow = (p: ProviderDTO) => rowFor(keyOf("Provider", p.namespace, p.name), <span />, nameCell(p.namespace, p.name),
    <span style={{ ...cellStyle, color: "var(--color-text-tertiary)" }}>{p.suspended ? "suspended" : p.type ?? "-"}</span>,
    `${p.alerts.length}${p.alertsState === "incomplete" ? "+" : ""} alert${p.alerts.length === 1 ? "" : "s"}${p.channel ? ` · ${p.channel}` : ""}`);
  const alertRow = (a: AlertDTO) => rowFor(keyOf("Alert", a.namespace, a.name), <span />, nameCell(a.namespace, a.name),
    <span style={{ ...cellStyle, color: a.providerState === "found" ? "var(--color-text-tertiary)" : "var(--color-text-warning)" }}>
      {a.suspended ? "suspended" : `provider ${a.providerName}${a.providerState === "found" ? (a.providerSuspended ? " (suspended)" : "") : `: ${a.providerState}`}`}
    </span>, a.dispatchSummary);

  const tabChip = (t: Tab, label: string, n?: number) => (
    <button className="l-chip" aria-pressed={tab === t} onClick={() => { setTab(t); setSelKey(null); setCursorKey(null); }}>{label}{n ? <span style={{ color: "var(--color-text-danger)" }}> {n}</span> : null}</button>
  );

  const confirm = pending && pending.cluster === cluster ? pending : null;
  const send = (p: Pending) => {
    setPending(null);
    if (p.kind === "Receiver") {
      const cur = receivers.find((r) => r.namespace === p.namespace && r.name === p.name);
      if (!cur || cur.uid !== p.uid) {
        useFleet.getState().setActionStatus({ kind: "error", message: `Receiver ${p.namespace}/${p.name} was replaced since the action was armed - nothing was sent` });
        return;
      }
    }
    if (p.verb === "reconcile") void reconcile(cluster, p.kind, p.namespace, p.name, p.uid);
    else void setSuspend(cluster, p.kind, p.namespace, p.name, p.uid, p.verb === "suspend");
  };

  const inspector = selKind === "Receiver" && pin.subject ? <ReceiverInspector cluster={cluster} r={pin.subject} gone={pin.gone === "replaced" ? "removed" : pin.gone} replaced={pin.gone === "replaced" ? pin.adopt : undefined}
      onVerb={(verb) => setPending({ cluster, kind: "Receiver", namespace: pin.subject!.namespace, name: pin.subject!.name, uid: pin.subject!.uid, verb, owner: pin.subject!.owner })} onClose={() => setSelKey(null)} />
    : selKind === "Receiver" && selKey && pin.expected && pin.gone ? <PinnedGone cluster={cluster} kind="Receiver" name={selKey.split("/").slice(1).join("/")} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={() => setSelKey(null)} />
    : selProvider ? <ProviderInspector cluster={cluster} p={selProvider} onOpen={(key) => { setTab("alerts"); setCursorKey(key); setSelKey(key); }}
      onVerb={(verb) => setPending({ cluster, kind: "Provider", namespace: selProvider.namespace, name: selProvider.name, uid: selProvider.uid, verb, owner: selProvider.owner })} onClose={() => setSelKey(null)} />
    : selAlert ? <AlertInspector cluster={cluster} a={selAlert} onOpen={(key) => { setTab("providers"); setCursorKey(key); setSelKey(key); }}
      onVerb={(verb) => setPending({ cluster, kind: "Alert", namespace: selAlert.namespace, name: selAlert.name, uid: selAlert.uid, verb, owner: selAlert.owner })} onClose={() => setSelKey(null)} />
    : undefined;

  return (
    <>
      <LensLayout
        label="Flux"
        title="Flux CD"
        part="flux-notifications"
        summary={recAbsent && !notes?.providers.length && !notes?.alerts.length ? null : (
          <>
            <CountSentence total={rows.counts ? rows.counts.total : receivers.length} marker={recEv === "complete" ? "" : "+"} noun={["Receiver", "Receivers"]} />
            {faults > 0 ? <MarkedPhrase fault>{`${faults} failing`}</MarkedPhrase> : <span className="t-secondary">none failing</span>}
            {notes && <span className="t-secondary">{providers.length}{notes.providersList.more ? "+" : ""} Providers · {alerts.length}{notes.alertsList.more ? "+" : ""} Alerts</span>}
          </>
        )}
        controls={(
          <>
            <LensParts cluster={cluster} groupId="flux" />
            <div role="group" aria-label="Notification kinds" className="l-seg">
              {tabChip("receivers", "Receivers", faults)}
              {tabChip("providers", "Providers")}
              {tabChip("alerts", "Alerts")}
            </div>
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
            {tab === "receivers" ? <VirtualList ref={listRef} items={receivers} rowHeight={TABLE_ROW_HEIGHT} rowKey={(r) => r.uid || keyOf("Receiver", r.namespace, r.name)} style={{ flex: 1, minHeight: 0 }} render={recRow} />
              : tab === "providers" ? <VirtualList ref={listRef} items={providers} rowHeight={TABLE_ROW_HEIGHT} rowKey={(p) => keyOf("Provider", p.namespace, p.name)} style={{ flex: 1, minHeight: 0 }} render={provRow} />
              : <VirtualList ref={listRef} items={alerts} rowHeight={TABLE_ROW_HEIGHT} rowKey={(a) => keyOf("Alert", a.namespace, a.name)} style={{ flex: 1, minHeight: 0 }} render={alertRow} />}
          </LensTable>
        }
        footer={<span className="t-legend">j k navigate · ↵ inspect</span>}
        footerEnd={<span className="t-secondary">Receivers broken first · Providers and Alerts are facts</span>}
        inspector={inspector}
        inspectorLabel="Notification object details"
        onCloseInspector={() => setSelKey(null)}
      />
      {confirm && (
        <ConfirmDialog
          title={`${confirm.verb === "reconcile" ? "Reconcile" : confirm.verb === "suspend" ? "Suspend" : "Resume"} ${confirm.kind}`}
          cluster={confirm.cluster}
          detail={verbDetail(confirm)}
          protected={isProtected}
          danger={confirm.verb === "suspend"}
          confirmLabel={confirm.verb === "reconcile" ? "Reconcile" : confirm.verb === "suspend" ? "Suspend" : "Resume"}
          onCancel={() => setPending(null)}
          onConfirm={() => send(confirm)}
        />
      )}
    </>
  );
}

function ReceiverInspector({ cluster, r, gone, replaced, onVerb, onClose }: {
  cluster: string; r: ReceiverDTO; gone: "removed" | "unverified" | null; replaced?: () => void; onVerb: (v: Verb) => void; onClose: () => void;
}) {
  const role = roleOf(r.rule);
  const describe = useDescribe(cluster, !gone ? { kind: "Receiver", namespace: r.namespace, name: r.name, uid: r.uid } : null);
  const ai = useInspectorAI(r.uid ? { kind: "row" as const, task: "flux", part: "receivers", uid: r.uid } : null, `Receiver/${r.namespace}/${r.name}`, !gone, cluster);
  const [detail, setDetail] = useState<{ uid: string; d: CompositionDetailDTO } | null>(null);
  useEffect(() => {
    let live = true;
    setDetail(null);
    if (gone) return;
    const uid = r.uid;
    void getCompositionDetail(cluster, "Receiver", r.namespace, r.name, uid).then((d) => { if (live) setDetail({ uid, d }); });
    return () => { live = false; };
  }, [cluster, r.namespace, r.name, r.uid, r.generation, gone]);
  // shown only beside the revision it read (Codex's S1c implementation
  // review, finding 1)
  const mine = detail && detail.uid === r.uid ? detail.d : null;
  const readError = mine?.error ? mine.error : null;
  const d = mine && !readError && mine.generation === r.generation ? mine : null;
  const otherRevision = !!mine && !readError && !d;
  const verbs: MenuItem[] = gone ? [] : r.suspended ? [{ label: "Resume", onSelect: () => onVerb("resume") }] : [
    { label: "Reconcile", onSelect: () => onVerb("reconcile") },
    { label: "Suspend", danger: true, onSelect: () => onVerb("suspend") },
  ];
  return (
    <div data-testid="receiver-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: r.uid || undefined }} replaced={replaced} kind="Receiver" name={r.name} meta={r.namespace} onClose={onClose}
        verdict={gone ? (
          <div role="status" className="t-body"><span className="t-status">{gone === "removed" ? "Receiver removed." : "Not in the current Receiver read."}</span> The details below are its last evidence.</div>
        ) : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} reason={role.kind === "fault" ? r.reason : undefined} />
            {role.kind === "ok" && r.meaning && <div className="t-body" data-testid="receiver-meaning" style={{ color: "var(--color-text-secondary)", marginTop: -4 }}>Ready: {r.meaning}.</div>}
            <div className="t-secondary" style={{ marginTop: 4 }}>Klyx cannot see whether webhooks arrive; Ready means the token was found and the spec is valid. Its webhook path is not shown.</div>
            {r.suspended && <div className="t-body" style={{ color: "var(--color-text-secondary)", marginTop: 4 }}>Suspended - an intended pause. Webhooks to it are refused.</div>}
          </div>
        )}
        more={[...verbs, ...(verbs.length ? [{ kind: "separator" as const }] : []), ...describe.menu, ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="Receiver">
          <div className="insp-kv">
            <span>type</span><span>{r.type ?? "-"}</span>
            {r.secretRef && <><span>token</span><span>Secret {r.secretRef} ({secretNote})</span></>}
            {r.oidcProviders ? <><span>OIDC</span><span>{r.oidcProviders} provider{r.oidcProviders === 1 ? "" : "s"}</span></> : null}
            {r.events.length > 0 && <><span>events</span><span style={{ overflowWrap: "anywhere" }}>{r.events.join(", ")}</span></>}
            {r.interval && <><span>interval</span><span>{r.interval}</span></>}
            {r.readySince && <><span>ready since</span><span><Age unixSeconds={unix(r.readySince)} suffix=" ago" /></span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title={`Reconciles on a webhook · ${r.resourceCount}`}>
          {r.resources.map((x, i) => <div key={i} className="t-body" style={{ padding: "2px 0" }}>{x.kind} {x.namespace ? `${x.namespace}/` : ""}{x.name}{x.matchLabels ? ` (${x.matchLabels} label match${x.matchLabels === 1 ? "" : "es"})` : ""}</div>)}
          {r.filterCount > 0 && <div className="t-secondary">{r.filterCount} CEL filter{r.filterCount === 1 ? "" : "s"} (expressions not shown){r.resourceFilter ? " · a resource filter" : ""}</div>}
        </InspectorBlock>
        <InspectorBlock title="Events">
          {readError ? <div className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Could not be read: {readError}</div>
            : !d ? <div className="t-secondary">{otherRevision ? "The latest read describes another revision of this Receiver; it waits for the row to catch up." : "Reading…"}</div>
            : d.eventsError ? <div className="t-body" style={{ color: "var(--color-text-warning)" }}>Events could not be read: {d.eventsError}</div>
            : d.events.length === 0 ? <div className="t-secondary">No recent events (they age out after about an hour).</div>
            : d.events.slice(0, 8).map((e, i) => <div key={i} className="t-body" style={{ color: e.type === "Warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{e.reason}: {e.message}</div>)}
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

const linkStyle: React.CSSProperties = { display: "block", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)", overflowWrap: "anywhere" };

function ProviderInspector({ cluster, p, onOpen, onVerb, onClose }: { cluster: string; p: ProviderDTO; onOpen: (key: string) => void; onVerb: (v: Verb) => void; onClose: () => void }) {
  const describe = useDescribe(cluster, { kind: "Provider", namespace: p.namespace, name: p.name, uid: p.uid });
  const verbs: MenuItem[] = [p.suspended ? { label: "Resume", onSelect: () => onVerb("resume") } : { label: "Suspend", danger: true, onSelect: () => onVerb("suspend") }];
  return (
    <div data-testid="provider-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: p.uid || undefined }} kind="Provider" name={p.name} meta={p.namespace} onClose={onClose}
        verdict={<div className="t-body" style={{ color: "var(--color-text-secondary)" }}>A Provider is configuration, without a verdict{p.suspended ? "; it is suspended" : ""}.</div>}
        more={[...verbs, { kind: "separator" as const }, ...describe.menu]}
      >
        <InspectorBlock title="Provider">
          <div className="insp-kv">
            <span>type</span><span>{p.type ?? "-"}</span>
            {p.channel && <><span>channel</span><span>{p.channel}</span></>}
            <span>address</span><span>{p.hasAddress ? "set (not shown: it can carry the webhook secret)" : "not set"}</span>
            {p.hasProxy && <><span>proxy</span><span>set (not shown)</span></>}
            {[["secret", p.secretRef], ["certificate", p.certSecretRef], ["proxy secret", p.proxySecretRef]].filter(([, v]) => v).map(([k, v]) => <span key={k} style={{ display: "contents" }}><span>{k}</span><span>Secret {v} ({secretNote})</span></span>)}
            {p.serviceAccountName && <><span>account</span><span>{p.serviceAccountName}</span></>}
            {(p.interval || p.timeout) && <><span>timing</span><span>{[p.interval && `every ${p.interval}`, p.timeout && `timeout ${p.timeout}`].filter(Boolean).join(" · ")}</span></>}
          </div>
        </InspectorBlock>
        <InspectorBlock title={`Alerts using it · ${p.alerts.length}${p.alertsState === "incomplete" ? "+" : ""}`}>
          {p.alertsState === "not readable" ? <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The Alerts could not be read.</div>
            : p.alerts.length === 0 ? <div className="t-secondary">{p.alertsState === "incomplete" ? "None in the read; it is capped, so others may." : "No Alert in its namespace uses it."}</div>
            : p.alerts.map((a) => <button key={`${a.namespace}/${a.name}`} className="t-body" onClick={() => onOpen(keyOf("Alert", a.namespace, a.name))} style={linkStyle}>{a.namespace}/{a.name}</button>)}
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function AlertInspector({ cluster, a, onOpen, onVerb, onClose }: { cluster: string; a: AlertDTO; onOpen: (key: string) => void; onVerb: (v: Verb) => void; onClose: () => void }) {
  const describe = useDescribe(cluster, { kind: "Alert", namespace: a.namespace, name: a.name, uid: a.uid });
  const verbs: MenuItem[] = [a.suspended ? { label: "Resume", onSelect: () => onVerb("resume") } : { label: "Suspend", danger: true, onSelect: () => onVerb("suspend") }];
  return (
    <div data-testid="alert-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: a.uid || undefined }} kind="Alert" name={a.name} meta={a.namespace} onClose={onClose}
        verdict={<div className="t-body" style={{ color: "var(--color-text-secondary)" }}>An Alert is configuration, without a verdict{a.suspended ? "; it is suspended" : ""}.</div>}
        more={[...verbs, { kind: "separator" as const }, ...describe.menu]}
      >
        <InspectorBlock title="Provider">
          <div className="t-body" data-testid="alert-provider">
            {a.providerState === "found" ? <button onClick={() => onOpen(keyOf("Provider", a.namespace, a.providerName))} style={{ ...linkStyle, display: "inline" }}>Provider {a.namespace}/{a.providerName}</button> : <>Provider {a.namespace}/{a.providerName}</>}
            {a.providerState !== "found" && <span style={{ color: "var(--color-text-warning)" }}> · {a.providerState === "incomplete" ? "not in the capped read" : a.providerState}</span>}
            {a.providerSuspended && <span style={{ color: "var(--color-text-warning)" }}> · suspended: nothing is sent</span>}
          </div>
        </InspectorBlock>
        <InspectorBlock title="Events it sends">
          <div className="insp-kv">
            <span>severity</span><span>{a.eventSeverity || "info"}</span>
            {a.inclusionList.length > 0 && <><span>includes</span><span style={{ overflowWrap: "anywhere" }}>{a.inclusionList.join(", ")}</span></>}
            {a.exclusionList.length > 0 && <><span>excludes</span><span style={{ overflowWrap: "anywhere" }}>{a.exclusionList.join(", ")}</span></>}
            {a.metadataKeys.length > 0 && <><span>metadata</span><span>{a.metadataKeys.join(", ")} (values not shown)</span></>}
          </div>
          {a.eventSources.map((s, i) => <div key={i} className="t-body" style={{ padding: "2px 0" }}>{s.kind} {s.namespace ? `${s.namespace}/` : ""}{s.name}{s.matchLabels ? ` (${s.matchLabels} label match${s.matchLabels === 1 ? "" : "es"})` : ""}</div>)}
        </InspectorBlock>
        <InspectorBlock title="Dispatch evidence">
          <div className="t-body" data-testid="alert-dispatch" style={{ color: Object.keys(a.dispatch).length || a.dispatchPartial ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{a.dispatchSummary}</div>
          <div className="t-secondary" style={{ marginTop: 4 }}>A count and reasons from the controller's events, never a verdict: their messages can carry the webhook URL, so they are not shown.</div>
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}
