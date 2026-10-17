import { useEffect, useMemo, useRef, useState } from "react";
import { IconPhoto } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { RuleDTO } from "../store/fleet";
import { getImageDetail, type ImageAutomationDTO, type ImageCommonDTO, type ImageDetailDTO, type ImagePolicyDTO, type ImageRepositoryDTO, type ImageRowDTO } from "../bridge/fluximages";
import { reconcile, setSuspend } from "../bridge/gitops";
import { useImageRows } from "../health/useImageRows";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { Age, formatAge, useNow } from "../chrome/Age";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { CopyButton } from "../chrome/CopyButton";
import { clock, coverageGaps, kindEvidence, lastGoodAt, type KindEvidence } from "../layout/coverage";
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
import { kindOfRef } from "../nav/reveal";
import { expectPin } from "../nav/inspectorPin";
import { ownershipLine } from "./ownership";

// ImagesView - the Flux lens's Images view (coverage S1b §2): image
// repositories, policies and update automations from the Observer's shared
// "images" rows, broken-first. Roles are the Go rule's
// (internal/gitops/imageauto). Every inspector leads with the selected
// object's own verdict; a repository, source or Kustomization is a
// separate block beside it, never its verdict. A push is never called
// deployed.

type Kind = "ImageRepository" | "ImagePolicy" | "ImageUpdateAutomation";
type Item = { key: string; uid: string; kind: Kind; c: ImageCommonDTO; repository?: ImageRepositoryDTO; policy?: ImagePolicyDTO; automation?: ImageAutomationDTO };
type Role = { kind: "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused"; label: string };

const KIND_SHORT: Record<Kind, string> = { ImageRepository: "repository", ImagePolicy: "policy", ImageUpdateAutomation: "automation" };
const KIND_ORDER: Record<Kind, number> = { ImageRepository: 0, ImagePolicy: 1, ImageUpdateAutomation: 2 };
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
const shortSHA = (s?: string) => (s ? s.slice(0, 7) : "");
// a digest abbreviated: name:tag@sha256:0123456789ab…
const shortImage = (t: string) => t.replace(/@(sha256:)?([0-9a-f]{12})[0-9a-f]+$/, "@$1$2…");

const COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "kind", label: "Kind", width: 96, priority: 1 },
  { id: "name", label: "Name", width: 220, priority: 0, flex: true },
  { id: "status", label: "Status", width: 200, priority: 0 },
  { id: "detail", label: "Detail", width: 260, priority: 2, grow: 0.5 },
  { id: "age", label: "Age", width: 56, priority: 1 },
];

function detailText(it: Item, nowS: number): string {
  if (it.repository) {
    const r = it.repository;
    return r.scanned && r.scanTime ? `scanned ${formatAge(Math.max(0, nowS - unix(r.scanTime)))} ago · ${r.tagCount} tag${r.tagCount === 1 ? "" : "s"}` : "not scanned yet";
  }
  if (it.policy) return it.policy.latest ? shortImage(it.policy.latest.text) : "no image resolved";
  if (it.automation) {
    const a = it.automation;
    return a.lastPushCommit ? `pushed ${shortSHA(a.lastPushCommit)}${a.lastPushTime ? ` ${formatAge(Math.max(0, nowS - unix(a.lastPushTime)))} ago` : ""}` : "no push recorded";
  }
  return "-";
}

function itemOf(r: ImageRowDTO): Item | null {
  const c = r.repository ?? r.policy ?? r.automation;
  if (!c) return null;
  return { key: keyOf(r.kind, c.namespace, c.name), uid: r.uid, kind: r.kind, c, repository: r.repository, policy: r.policy, automation: r.automation };
}

// verbs (S1b §5): the request annotation and spec.suspend; an ImagePolicy
// served at v1beta2 has neither.
type Verb = "reconcile" | "suspend" | "resume";
const isV1 = (c: ImageCommonDTO) => /\/v1\//.test(c.gvr);
function verbWord(v: Verb, kind: Kind): string {
  if (v === "reconcile") return kind === "ImageRepository" ? "Scan now" : kind === "ImageUpdateAutomation" ? "Run now" : "Reconcile";
  return v === "suspend" ? "Suspend" : "Resume";
}
function verbDetail(v: Verb, it: Item): string {
  const what = `${it.kind} ${it.c.namespace}/${it.c.name}`;
  const mech = v === "reconcile"
    ? `Sets reconcile.fluxcd.io/requestedAt to a fresh token${it.kind === "ImageRepository" ? ", so the controller scans whatever the interval" : it.kind === "ImageUpdateAutomation" ? ", so the controller runs the automation now" : ""}.`
    : v === "suspend" ? "Sets spec.suspend: true. Its conditions freeze until it is resumed." : "Sets spec.suspend: false. Klyx reads it as Syncing… until the controller writes Ready for the new generation.";
  const owner = ownershipLine(it.c.owner, v === "reconcile" ? "annotation" : "suspend");
  return [`${what}. ${mech}`, owner, "Klyx reports the request; completion is the object's own status."].filter(Boolean).join(" ");
}

export function ImagesView({ cluster }: { cluster: string }) {
  const rows = useImageRows(cluster);
  const nowS = Math.floor(useNow() / 1000);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const [kindFilter, setKindFilter] = useState<"" | Kind>("");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useClusterKey(cluster);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const [pending, setPending] = useState<{ cluster: string; item: Item; verb: Verb } | null>(null);
  const listRef = useRef<VirtualListHandle>(null);

  const evidenceOf = (kind: string): KindEvidence => kindEvidence(!rows.loading, rows.coverage, kind, rows.watchError);
  const kindCov = (kind: string) => rows.coverage.find((k) => k.kind === kind)?.coverage;
  const absent = (["ImageRepository", "ImagePolicy", "ImageUpdateAutomation"] as const).every((k) => kindCov(k) === "absent");
  const items = useMemo<Item[]>(() => rows.rows.map(itemOf).filter((x): x is Item => x !== null)
    .sort((a, b) => rankOf(a.c.rule) - rankOf(b.c.rule) || KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || a.c.namespace.localeCompare(b.c.namespace) || a.c.name.localeCompare(b.c.name)), [rows.rows]);
  const visible = useMemo(() => items.filter((it) => !kindFilter || it.kind === kindFilter), [items, kindFilter]);

  const lastGood = lastGoodAt(rows.coverage, ["ImageRepository", "ImagePolicy", "ImageUpdateAutomation"]);
  const discoveryGap = coverageGaps(rows.coverage);
  const capped = rows.coverage.filter((k) => k.truncated);
  // One reading for the headline (Codex's S1b implementation review,
  // finding 5): the summary's counts when it has them, else the rows alone.
  const counted = rows.counts ? rows.counts.total : items.length;
  const attention = rows.counts ? rows.counts.included : items.filter((i) => i.c.rule.included).length;
  const faults = rows.counts ? rows.counts.fault : items.filter((i) => i.c.rule.role === "fault").length;
  const overdue = items.filter((i) => i.c.rule.label === "Scan overdue" || i.c.rule.label === "Run overdue").length;
  const suspended = items.filter((i) => i.c.suspended).length;

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
  // A summary Top link: select the object, pinned by UID when it has one.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "flux" || intent.part !== "images") return "wait";
    const kind = kindOfRef(intent.ref);
    const key = keyOf(kind, intent.ref.namespace ?? "", intent.ref.name);
    const found = items.find((i) => i.key === key);
    if (intent.ref.uid) expectPin(cluster, "FluxImages", key, intent.ref.uid);
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
  // the whole selected item is held: a replacement shows nothing of its
  // own until it is adopted
  const liveSel = useMemo(() => (selKey ? items.find((i) => i.key === selKey) : undefined), [items, selKey]);
  const pin = usePinnedSubject(cluster, selKey, liveSel, selKind ? evidenceOf(selKind) === "complete" : false, "FluxImages");
  const selItem: Item | undefined = pin.subject ?? undefined;
  const gone: "removed" | "unverified" | null = pin.gone === "replaced" ? "removed" : pin.gone;

  let state: DatasetState = "ready";
  const anyEv = (["ImageRepository", "ImagePolicy", "ImageUpdateAutomation"] as const).map(evidenceOf);
  if (absent) state = "empty";
  else if (anyEv.every((e) => e === "failed") && items.length === 0) state = "unreadable";
  else if (anyEv.some((e) => e === "pending") && items.length === 0) state = "loading";
  else if (visible.length === 0) state = "empty";
  const icon = <IconPhoto size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading image automation" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not read Flux image automation." hint={rows.watchError ?? undefined} />
    : absent ? <EmptyState icon={icon} title="Flux image automation is not installed on this cluster." hint="Klyx observes no image.toolkit.fluxcd.io repositories, policies or automations here." />
    : items.length > 0 ? <EmptyState icon={icon} title="Nothing of this kind." hint={`${items.length} image objects on this cluster.`} action={{ label: "Clear filter", onClick: () => setKindFilter("") }} />
    : <EmptyState icon={icon} title="No image repository, policy or automation yet." hint="The image controllers are installed; they act once an ImageRepository exists." />;

  const notices: React.ReactNode[] = [];
  if (rows.watchError && items.length > 0) notices.push(<WatchErrorBanner key="watch" tool="Flux image automation" failure={`read failed - showing the last good state${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={rows.watchError} />);
  if (capped.length > 0) notices.push(<div key="capped" role="status" data-testid="images-capped" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)" }}>
    Capped: {capped.map((k) => `${k.notRetained ?? "some"} ${k.kind}${(k.notRetained ?? 2) === 1 ? "" : "s"} not retained`).join(", ")} - Klyx keeps a bounded amount per cluster, so counts are lower bounds and joins past the cap are incomplete.
  </div>);
  if (rows.rowsState.refreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={{ padding: "4px 0" }}>Rows as of {rows.rowsState.evaluatedAt ? clock(rows.rowsState.evaluatedAt) : "the last read"} - refreshing; the totals are the cluster summary's.</div>);
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
        {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontFamily: "var(--font-sans)" }} title={it.c.message || undefined}>
          {role.label}{role.kind === "fault" && it.c.reason ? ` · ${it.c.reason}` : ""}{it.policy?.blockedBy ? ` · blocked by ImageRepository ${it.policy.blockedBy}` : ""}
        </span>}
        {show("detail") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }} title={it.policy?.latest?.text}>{detailText(it, nowS)}</span>}
        {show("age") && <span style={{ color: "var(--color-text-tertiary)" }}>{it.c.created ? formatAge(Math.max(0, nowS - unix(it.c.created))) : "-"}</span>}
      </div>
    );
  };

  const chip = (k: "" | Kind, label: string) => {
    const n = k ? items.filter((i) => i.kind === k && i.c.rule.included).length : 0;
    return <button className="l-chip" aria-pressed={kindFilter === k} onClick={() => { setKindFilter(k); setSelKey(null); setCursorKey(null); }}>{label}{n > 0 && <span style={{ color: "var(--color-text-danger)" }}> {n}</span>}</button>;
  };

  const confirm = pending && pending.cluster === cluster ? pending : null;
  const send = (p: { item: Item; verb: Verb }) => {
    setPending(null);
    const cur = items.find((i) => i.key === p.item.key);
    if (!cur || cur.uid !== p.item.uid) {
      useFleet.getState().setActionStatus({ kind: "error", message: `${p.item.kind} ${p.item.c.namespace}/${p.item.c.name} was replaced since the action was armed - nothing was sent` });
      return;
    }
    if (p.verb === "reconcile") void reconcile(cluster, p.item.kind, p.item.c.namespace, p.item.c.name, p.item.uid);
    else void setSuspend(cluster, p.item.kind, p.item.c.namespace, p.item.c.name, p.item.uid, p.verb === "suspend");
  };

  return (
    <>
      <LensLayout
        label="Flux"
        title="Flux CD"
        part="flux-images"
        summary={absent || items.length === 0 ? null : (
          <>
            <CountSentence total={counted} marker={anyEv.every((e) => e === "complete") ? "" : "+"} noun={["image object", "image objects"]} />
            {faults > 0 ? <MarkedPhrase fault>{`${faults} failing`}</MarkedPhrase> : attention > 0 ? <span className="t-secondary">{attention} not ready</span> : <span className="t-secondary">none failing</span>}
            {overdue > 0 && <span className="t-secondary" data-testid="images-overdue">{overdue} overdue</span>}
            {suspended > 0 && <span className="t-secondary">{suspended} suspended</span>}
          </>
        )}
        controls={(
          <>
            <LensParts cluster={cluster} groupId="flux" />
            {!absent && (
              <div role="group" aria-label="Image kinds" className="l-seg">
                {chip("", "All")}
                {chip("ImageRepository", "Repositories")}
                {chip("ImagePolicy", "Policies")}
                {chip("ImageUpdateAutomation", "Automations")}
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
        footerEnd={<span className="t-secondary">broken first · suspended is intended · a push is not a deployment</span>}
        inspector={selItem ? <ImageInspector cluster={cluster} it={selItem} nowS={nowS} gone={gone} replaced={pin.gone === "replaced" ? pin.adopt : undefined}
            onVerb={(verb) => setPending({ cluster, item: selItem, verb })} onOpen={(key) => { setKindFilter(""); setCursorKey(key); setSelKey(key); }} onClose={() => setSelKey(null)} />
          : selKey && pin.expected && pin.gone ? <PinnedGone cluster={cluster} kind={selKind} name={selKey.split("/").slice(1).join("/")} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={() => setSelKey(null)} />
          : undefined}
        inspectorLabel="Image object details"
        onCloseInspector={() => setSelKey(null)}
      />
      {confirm && (
        <ConfirmDialog
          title={`${verbWord(confirm.verb, confirm.item.kind)} ${confirm.item.kind}`}
          cluster={confirm.cluster}
          detail={verbDetail(confirm.verb, confirm.item)}
          protected={isProtected}
          danger={confirm.verb === "suspend"}
          confirmLabel={verbWord(confirm.verb, confirm.item.kind)}
          onCancel={() => setPending(null)}
          onConfirm={() => send(confirm)}
        />
      )}
    </>
  );
}

function ImageInspector({ cluster, it, nowS, gone, replaced, onVerb, onOpen, onClose }: {
  cluster: string; it: Item; nowS: number; gone: "removed" | "unverified" | null; replaced?: () => void;
  onVerb: (v: Verb) => void; onOpen: (key: string) => void; onClose: () => void;
}) {
  const role = roleOf(it.c.rule);
  const describe = useDescribe(cluster, !gone ? { kind: it.kind, namespace: it.c.namespace, name: it.c.name, uid: it.uid } : null);
  const ai = useInspectorAI(it.uid ? { kind: "row" as const, task: "flux", part: "images", uid: it.uid } : null, `${it.kind}/${it.c.namespace}/${it.c.name}`, !gone, cluster);
  const [detail, setDetail] = useState<{ uid: string; d: ImageDetailDTO } | null>(null);
  useEffect(() => {
    let live = true;
    setDetail(null);
    if (gone) return;
    const uid = it.uid;
    void getImageDetail(cluster, it.kind, it.c.namespace, it.c.name, uid).then((d) => { if (live) setDetail({ uid, d }); });
    return () => { live = false; };
  }, [cluster, it.kind, it.c.namespace, it.c.name, it.uid, it.c.generation, it.automation?.lastPushCommit, it.automation?.sourceNamespace, it.automation?.sourceName, gone]);
  // the evidence is shown only beside the revision it describes (Codex's
  // S1b implementation review, finding 1): the same UID, generation, push
  // and source as the row; a read of another revision waits for the row
  const fresh = (x: ImageDetailDTO) => x.uid === it.uid && (!!x.error || x.generation === it.c.generation) &&
    (!x.push || ((x.push.lastPushCommit ?? "") === (it.automation?.lastPushCommit ?? "") &&
      x.push.sourceNamespace === it.automation?.sourceNamespace && x.push.sourceName === it.automation?.sourceName));
  const d = detail && detail.uid === it.uid && fresh(detail.d) ? detail.d : null;
  const otherRevision = !!detail && detail.uid === it.uid && !fresh(detail.d);

  const v1Policy = it.kind !== "ImagePolicy" || isV1(it.c);
  const verbs: MenuItem[] = gone ? [] : [
    ...(v1Policy ? (it.c.suspended ? [{ label: "Resume", onSelect: () => onVerb("resume") }] : [
      { label: verbWord("reconcile", it.kind), onSelect: () => onVerb("reconcile") },
      { label: "Suspend", danger: true, onSelect: () => onVerb("suspend") },
    ]) : []),
  ];
  const lagging = it.c.rule.label === "Syncing…";

  return (
    <div data-testid="images-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: it.uid || undefined }} replaced={replaced} kind={it.kind} name={it.c.name} meta={it.c.namespace} onClose={onClose}
        verdict={gone ? (
          <div role="status" className="t-body"><span className="t-status">{gone === "removed" ? `${it.kind} removed.` : `Not in the current ${it.kind} read.`}</span> The details below are its last evidence.</div>
        ) : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label}
              reason={lagging ? `generation ${it.c.generation} not yet observed` : role.kind === "fault" || role.kind === "degraded" ? it.c.reason : undefined} />
            {(role.kind === "fault" || role.kind === "degraded") && it.c.message && <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>{it.c.message}</div>}
            {overdueText(it) && <div className="t-body" data-testid="images-clock" style={{ color: "var(--color-text-secondary)", marginTop: 4 }}>{overdueText(it)}</div>}
            {it.c.suspended && <div className="t-body" style={{ color: "var(--color-text-secondary)", marginTop: 4 }}>Suspended - an intended pause. The controller returns early, so its conditions are frozen.</div>}
            {it.kind === "ImagePolicy" && !isV1(it.c) && <div className="t-secondary" style={{ marginTop: 4 }}>Served at an older API version: reconcile and suspend are not offered (no lastHandledReconcileAt, no spec.suspend).</div>}
            {it.c.incomplete.length > 0 && <div className="t-secondary" style={{ marginTop: 4, color: "var(--color-text-warning)" }}>Too large to keep whole: {it.c.incomplete.join(", ")}. Joins that need it claim nothing.</div>}
          </div>
        )}
        more={[...verbs, ...(verbs.length ? [{ kind: "separator" as const }] : []), ...describe.menu, ...ai.menu]}
      >
        {ai.block}
        {it.repository && <RepositoryBlocks r={it.repository} onOpen={onOpen} />}
        {it.policy && <PolicyBlocks p={it.policy} nowS={nowS} onOpen={onOpen} />}
        {it.automation && <AutomationBlocks a={it.automation} d={d} onOpen={onOpen} />}
        <InspectorBlock title="Events">
          {!d ? <div className="t-secondary">{otherRevision ? "The latest read describes another revision of this object; it waits for the row to catch up." : "Reading…"}</div>
            : d.error ? <div className="t-body" style={{ color: "var(--color-text-warning)" }}>{d.error}</div>
            : d.eventsError ? <div className="t-body" style={{ color: "var(--color-text-warning)" }}>Events could not be read: {d.eventsError}</div>
            : d.events.length === 0 ? <div className="t-secondary">No recent events (they age out after about an hour).</div>
            : d.events.slice(0, 8).map((e, i) => <div key={i} className="t-body" style={{ color: e.type === "Warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{e.reason}: {e.message}</div>)}
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

const secretNote = "named only, never read";
// a cross-link inside an inspector, as the Flux lens draws them
const linkStyle: React.CSSProperties = { display: "block", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)", overflowWrap: "anywhere" };

// overdueText explains the two clocks (S1b §4.3), from the row's own times.
function overdueText(it: Item): string {
  if (it.c.rule.label === "Scan overdue" && it.repository) {
    return `No completed scan for over twice its interval (${it.repository.interval}) while Ready; last scan ${it.repository.scanTime ?? "never"} by the controller's clock.`;
  }
  if (it.c.rule.label === "Run overdue" && it.automation) {
    return `No newer run recorded for over twice its interval (${it.automation.interval}) while Ready; a run may be in progress. Last run started ${it.automation.lastRun ?? "never"} by the controller's clock.`;
  }
  return "";
}

function RepositoryBlocks({ r, onOpen }: { r: ImageRepositoryDTO; onOpen: (key: string) => void }) {
  return (
    <>
      <InspectorBlock title="Image">
        <div className="insp-kv">
          <span>image</span><span style={{ overflowWrap: "anywhere" }}>{r.image}</span>
          {r.canonicalImage && r.canonicalImage !== r.image && <><span>canonical</span><span style={{ overflowWrap: "anywhere" }}>{r.canonicalImage}</span></>}
          {r.provider && <><span>provider</span><span>{r.provider}</span></>}
          {r.serviceAccountName && <><span>account</span><span>{r.serviceAccountName}</span></>}
          {[["secret", r.secretRef], ["certificate", r.certSecretRef], ["proxy", r.proxySecretRef]].filter(([, v]) => v).map(([k, v]) => <span key={k} style={{ display: "contents" }}><span>{k}</span><span>Secret {v} ({secretNote})</span></span>)}
          {r.insecure && <><span>transport</span><span style={{ color: "var(--color-text-warning)" }}>insecure (plain HTTP allowed)</span></>}
          {r.accessFromSelectors ? <><span>access from</span><span>{r.accessFromSelectors} namespace selector{r.accessFromSelectors === 1 ? "" : "s"}</span></> : null}
        </div>
      </InspectorBlock>
      <InspectorBlock title="Scan">
        <div className="insp-kv">
          <span>interval</span><span>{r.interval || "-"}{r.timeout ? ` · timeout ${r.timeout}` : ""}</span>
          <span>last scan</span><span title={r.scanTime ? `${r.scanTime} - the controller's clock at the last successful scan` : undefined}>{r.scanned && r.scanTime ? <>scanned <Age unixSeconds={unix(r.scanTime)} suffix=" ago" /> · {r.tagCount} tags</> : "no successful scan yet"}</span>
          {r.exclusionList.length > 0 && <><span>excludes</span><span style={{ overflowWrap: "anywhere" }}>{r.exclusionList.join(", ")}</span></>}
        </div>
        {r.latestTags.length > 0 && <div className="t-body" style={{ marginTop: 6, overflowWrap: "anywhere" }}><span className="t-section-label">the ten highest tags by reverse string order: </span>{r.latestTags.join(", ")}</div>}
      </InspectorBlock>
      <InspectorBlock title={`Policies using it · ${joinCount(r.policies.length, r.policiesState)}`}>
        {r.policiesState === "not readable" ? <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The policies could not be read, so which use it is not known.</div>
          : r.policies.length === 0 ? <div className="t-secondary">{r.policiesState === "incomplete" ? "None among the policies Klyx retains; the read is capped, so others may." : "No ImagePolicy names this repository in the current read."}</div>
          : r.policies.map((p) => (
            <button key={`${p.namespace}/${p.name}`} className="t-body" onClick={() => onOpen(keyOf("ImagePolicy", p.namespace, p.name))} style={linkStyle}>
              {p.namespace}/{p.name} <span style={{ color: ROLE_TEXT[p.role] ?? ROLE_TEXT.unknown }}>· {p.label}</span>
            </button>
          ))}
      </InspectorBlock>
    </>
  );
}

// joinCount is a reverse join's count, qualified by its read's state.
const joinCount = (n: number, state: string) => (state === "not readable" ? "not readable" : state === "incomplete" ? `${n}+` : String(n));

const useLabel: Record<string, string> = { used: "used in its last successful run", pending: "pending: not yet in a successful run", incomplete: "not known: the automation's record could not be kept whole" };

function PolicyBlocks({ p, nowS, onOpen }: { p: ImagePolicyDTO; nowS: number; onOpen: (key: string) => void }) {
  const repo = p.repository;
  const repoKey = keyOf("ImageRepository", p.repositoryNamespace, p.repositoryName);
  return (
    <>
      <InspectorBlock title="Repository">
        <div className="t-body" data-testid="images-repo-block">
          <button onClick={() => onOpen(repoKey)} style={{ ...linkStyle, display: "inline" }}>Repository {p.repositoryNamespace}/{p.repositoryName}</button>
          {": "}
          {!repo || repo.state !== "found" ? <span style={{ color: "var(--color-text-warning)" }}>{repo?.state ?? "not found"}</span>
            : <span style={{ color: ROLE_TEXT[repo.role ?? "unknown"] ?? ROLE_TEXT.unknown }}>{repo.label}{repo.scanTime ? `, scanned ${formatAge(Math.max(0, nowS - unix(repo.scanTime)))} ago` : ""}</span>}
        </div>
        <div className="t-secondary" style={{ marginTop: 4 }}>The repository's state is its own; it never stands in for this policy's verdict. Scan it from its own inspector.</div>
      </InspectorBlock>
      <InspectorBlock title="Image">
        <div className="insp-kv">
          <span>{p.ready === "Ready" ? "resolved" : "last resolved"}</span>
          <span style={{ overflowWrap: "anywhere" }}>{p.latest ? <>{p.latest.text}<CopyButton text={p.latest.text} label="copy image" /></> : "none resolved"}{p.latest && p.ready !== "Ready" && <span className="t-secondary"> (last resolved, before the failure)</span>}</span>
          {p.previous && <><span>previous</span><span style={{ overflowWrap: "anywhere" }}>{p.previous.text}</span></>}
          <span>policy</span><span>{p.policyType ? `${p.policyType}${p.policyValue ? ` ${p.policyValue}` : ""}` : "-"}</span>
          {(p.filterPattern || p.filterExtract) && <><span>filter</span><span style={{ overflowWrap: "anywhere" }}>{p.filterPattern}{p.filterExtract ? ` → ${p.filterExtract}` : ""}</span></>}
          {p.digestReflection && <><span>digests</span><span>{p.digestReflection}{p.interval ? ` · every ${p.interval}` : ""}</span></>}
        </div>
      </InspectorBlock>
      <InspectorBlock title={`Automations using it · ${joinCount(p.automations.length, p.automationsState)}`}>
        {p.automationsState === "not readable" ? <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The automations could not be read, so which use it is not known.</div>
          : p.automations.length === 0 ? <div className="t-secondary">{p.automationsState === "incomplete" ? "None among the automations Klyx retains; the read is capped, so others may." : "No ImageUpdateAutomation in its namespace selects it in the current read."}</div>
          : p.automations.map((a) => (
            <button key={`${a.namespace}/${a.name}`} className="t-body" onClick={() => onOpen(keyOf("ImageUpdateAutomation", a.namespace, a.name))} style={linkStyle}>
              {a.namespace}/{a.name} <span className="t-secondary">· {useLabel[a.state] ?? a.state}</span>
            </button>
          ))}
      </InspectorBlock>
    </>
  );
}

function checkoutText(c?: { branch?: string; tag?: string; semver?: string; name?: string; commit?: string }): string {
  if (!c) return "";
  return [c.commit && `commit ${c.commit}`, c.name && `ref ${c.name}`, c.semver && `semver ${c.semver}`, c.tag && `tag ${c.tag}`, c.branch && `branch ${c.branch}`].filter(Boolean).join(", ");
}

function AutomationBlocks({ a, d, onOpen }: { a: ImageAutomationDTO; d: ImageDetailDTO | null; onOpen: (key: string) => void }) {
  const push = d?.push;
  const branch = !push ? "reading…"
    : push.branchState === "bound" ? `pushed to ${push.branch}, from the controller's event`
    : push.branchState === "ambiguous" ? "branch ambiguous: the controller's events disagree"
    : push.branchState === "unreadable" ? "branch not known: events could not be read"
    : push.branchState === "no push" ? "" : "branch not recorded (the event has expired)";
  const source = !push ? "reading…"
    : push.sourceState !== "found" ? `GitRepository ${a.sourceNamespace}/${a.sourceName}: ${push.sourceState}`
    : push.sourceLine === "fetched" ? "the source has fetched this commit"
    : push.sourceLine === "no push" ? "" : `the source is at ${push.sourceRevision || "no artifact"}; whether it includes the push is not known here`;
  return (
    <>
      <InspectorBlock title="Source">
        <div className="t-body" data-testid="images-source-block">
          {a.sourceKind || "GitRepository"} {a.sourceNamespace}/{a.sourceName}
          {push && push.sourceState !== "found" && <span style={{ color: "var(--color-text-warning)" }}> · {push.sourceState}</span>}
        </div>
        <div className="t-secondary" style={{ marginTop: 4 }}>The source's state is its own; a healthy source never stands in for a failed push.</div>
      </InspectorBlock>
      <InspectorBlock title="Last push">
        {!a.lastPushCommit ? <div className="t-secondary">No push recorded.</div> : (
          <div data-testid="images-push">
            <div className="insp-kv">
              <span>commit</span><span>{shortSHA(a.lastPushCommit)}<CopyButton text={a.lastPushCommit} label="copy commit" /></span>
              {a.lastPushTime && <><span>pushed</span><span title={`${a.lastPushTime} - the controller's clock just after the push`}><Age unixSeconds={unix(a.lastPushTime)} suffix=" ago" /></span></>}
              {branch && <><span>branch</span><span>{branch}</span></>}
            </div>
            <div className="t-body" style={{ marginTop: 6 }}><span className="t-section-label">Source: </span>{source}</div>
            {push?.sourceBranchNote && <div className="t-body" style={{ color: "var(--color-text-secondary)" }}>{push.sourceBranchNote}</div>}
            <div className="t-body" style={{ marginTop: 6 }}><span className="t-section-label">Applied: </span>
              {!push ? "reading…" : push.appliedState === "not readable" ? "the Kustomizations could not be read"
                : push.applied.length === 0 ? `no Kustomization is bound to this GitRepository${push.appliedState === "incomplete" ? " in the retained read" : ""}` : null}
            </div>
            {push && push.applied.map((k) => (
              <div key={`${k.namespace}/${k.name}`} className="t-body" style={{ color: "var(--color-text-secondary)" }}>
                Kustomization {k.namespace}/{k.name}: {k.applied ? "applied this commit" : `at another revision (${k.revision || "none applied"})`}
              </div>
            ))}
            {push && push.appliedState === "incomplete" && push.applied.length > 0 && <div className="t-secondary">More Kustomizations may be bound: the read is capped.</div>}
            <div className="t-secondary" style={{ marginTop: 4 }}>Each line compares the push with today's objects. Workloads are not inferred from images, and nothing here is called deployed.</div>
          </div>
        )}
      </InspectorBlock>
      <InspectorBlock title="Push target, as configured now">
        <div className="insp-kv" data-testid="images-target">
          {push?.target.repositoryURL && <><span>repository</span><span style={{ overflowWrap: "anywhere" }}>{push.target.repositoryURL}</span></>}
          <span>checkout</span><span>{push ? `${checkoutText(push.target.checkout) || "the default branch"} (from ${push.target.checkoutFrom === "automation" ? "the automation" : push.target.checkoutFrom === "source" ? "the GitRepository" : "neither: Git's default"})` : checkoutText(a.checkout) || "-"}</span>
          <span>push branch</span><span>{push ? (push.target.error ? <span style={{ color: "var(--color-text-warning)" }}>{push.target.error}</span> : push.target.branch) : a.pushBranch || "-"}</span>
          {a.pushRefspec && <><span>refspec</span><span style={{ overflowWrap: "anywhere" }}>{a.pushRefspec}</span></>}
          {a.pushOptionCount ? <><span>push options</span><span>{a.pushOptionCount} (values not shown)</span></> : null}
        </div>
        <div className="t-secondary" style={{ marginTop: 4 }}>This describes the next run, never where the last push went.</div>
      </InspectorBlock>
      <InspectorBlock title="Runs">
        <div className="insp-kv">
          <span>interval</span><span>{a.interval || "-"}</span>
          <span>last run</span><span title="lastAutomationRunTime is set when a run starts: an attempt, never a success">{a.lastRun ? <>started <Age unixSeconds={unix(a.lastRun)} suffix=" ago" /></> : "none recorded"}</span>
          {a.observedSourceRevision && <><span>observed</span><span style={{ overflowWrap: "anywhere" }}>{a.observedSourceRevision}</span></>}
          {(a.updateStrategy || a.updatePath) && <><span>update</span><span>{[a.updateStrategy, a.updatePath].filter(Boolean).join(" · ")}</span></>}
          {(a.authorName || a.authorEmail) && <><span>author</span><span>{[a.authorName, a.authorEmail && `<${a.authorEmail}>`].filter(Boolean).join(" ")}</span></>}
          {a.signingKeySecret && <><span>signing key</span><span>Secret {a.signingKeySecret} ({secretNote})</span></>}
        </div>
      </InspectorBlock>
      <InspectorBlock title={`Policies it uses now · ${a.uses.length}`}>
        <div className="t-secondary" style={{ marginBottom: 4 }}>Selector: {a.selectorIncomplete ? "too large to keep whole" : a.selector || "-"}{a.usesState === "incomplete" ? " · the list may be incomplete" : a.usesState === "not readable" ? " · the policies could not be read" : ""}</div>
        {a.uses.map((u) => (
          <button key={`${u.namespace}/${u.name}`} className="t-body" onClick={() => onOpen(keyOf("ImagePolicy", u.namespace, u.name))} style={linkStyle}>
            {u.name}: {shortImage(u.resolved.text)} <span className="t-secondary">· {u.state === "used" ? "in its last successful run" : u.state === "pending" ? `pending${u.observed ? ` (last run used ${shortImage(u.observed.text)})` : ""}` : useLabel.incomplete}</span>
          </button>
        ))}
      </InspectorBlock>
    </>
  );
}
