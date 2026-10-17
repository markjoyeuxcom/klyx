import { useEffect, useMemo, useRef, useState } from "react";
import { IconStack2 } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { RuleDTO } from "../store/fleet";
import { getCompositionDetail, type ArtifactDTO, type CompositionCommonDTO, type CompositionDetailDTO, type CompositionRefDTO, type CompositionRowDTO, type GeneratorDTO } from "../bridge/fluxcomposition";
import { fluxOpAct } from "../bridge/fluxop";
import { useCompositionRows, COMPOSITION_PART_KINDS } from "../health/useCompositionRows";
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
import { attributed, producerText } from "./producer";

// CompositionView - the Flux lens's Composition view (coverage S1c §2):
// ArtifactGenerators and ExternalArtifacts from the Observer's shared
// "composition" rows, broken-first, beside the Sources view. Each inspector
// leads with the selected object's own verdict; an artifact's producer is
// a separate block after it, with a health only when its identity is
// established. An artifact's URL shows as its origin only.

type Kind = "ArtifactGenerator" | "ExternalArtifact";
type Item = { key: string; uid: string; kind: Kind; c: CompositionCommonDTO; generator?: GeneratorDTO; artifact?: ArtifactDTO };
type Role = { kind: "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused"; label: string };

const KIND_SHORT: Record<Kind, string> = { ArtifactGenerator: "generator", ExternalArtifact: "artifact" };
const KIND_ORDER: Record<Kind, number> = { ArtifactGenerator: 0, ExternalArtifact: 1 };
const RANK: Record<string, number> = { fault: 0, degraded: 1, progressing: 2, unknown: 3, paused: 4, ok: 5 };
const rankOf = (r: RuleDTO) => RANK[r.role] ?? RANK.unknown;
const roleOf = (r: RuleDTO): Role => ({ kind: (r.role in RANK ? r.role : "unknown") as Role["kind"], label: r.label });
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const roleTone = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const linkStyle: React.CSSProperties = { display: "block", background: "none", border: 0, padding: "2px 0", cursor: "pointer", textAlign: "left", color: "var(--color-text-info)", overflowWrap: "anywhere" };
const keyOf = (kind: string, ns: string, name: string) => `${kind}/${ns}/${name}`;
const unix = (iso?: string) => (iso ? Math.floor(Date.parse(iso) / 1000) : 0);
// latest@sha256:0123456789ab…
const shortRev = (r?: string) => (r ? r.replace(/(sha256:|sha1:)([0-9a-f]{12})[0-9a-f]+$/, "$1$2…") : "");
const AWAITING = "Resumed · awaiting evaluation";

const COLUMNS: (ColumnDef & { label: string })[] = [
  ROW_MARK,
  { id: "kind", label: "Kind", width: 88, priority: 1 },
  { id: "name", label: "Name", width: 220, priority: 0, flex: true },
  { id: "status", label: "Status", width: 200, priority: 0 },
  { id: "detail", label: "Detail", width: 260, priority: 2, grow: 0.5 },
  { id: "age", label: "Age", width: 56, priority: 1 },
];

function detailText(it: Item, nowS: number): string {
  if (it.generator) {
    const g = it.generator;
    return `${g.sources.length} source${g.sources.length === 1 ? "" : "s"} → ${g.generated.length} artifact${g.generated.length === 1 ? "" : "s"}`;
  }
  const a = it.artifact;
  if (!a) return "-";
  if (!a.hasArtifact) return "no artifact yet";
  return `${shortRev(a.revision)}${a.lastUpdate ? ` · updated ${formatAge(Math.max(0, nowS - unix(a.lastUpdate)))} ago` : ""}`;
}

function itemOf(r: CompositionRowDTO): Item | null {
  const c = r.generator ?? r.artifact;
  if (!c) return null;
  return { key: keyOf(r.kind, c.namespace, c.name), uid: r.uid, kind: r.kind, c, generator: r.generator, artifact: r.artifact };
}

// verbs (S1c §5): a generator's own annotations; an artifact has none (its
// producer reconciles it).
type Verb = "reconcile" | "suspend" | "resume";
const verbWord = (v: Verb) => (v === "reconcile" ? "Reconcile" : v === "suspend" ? "Suspend" : "Resume");
function verbDetail(v: Verb, it: Item): string {
  const what = `ArtifactGenerator ${it.c.namespace}/${it.c.name}.`;
  const mech = v === "reconcile" ? "Sets reconcile.fluxcd.io/requestedAt to a fresh token, so source-watcher regenerates its artifacts now."
    : v === "suspend" ? "Sets source.extensions.fluxcd.io/reconcile: disabled. Its artifacts stay as they are until it is resumed."
    : "Sets source.extensions.fluxcd.io/reconcile: enabled and a fresh requestedAt. A run that finds nothing changed writes no condition, so it can stay awaiting evaluation.";
  return [what, mech, "Klyx reports the request; completion is the object's own status."].join(" ");
}

export function CompositionView({ cluster }: { cluster: string }) {
  const rows = useCompositionRows(cluster);
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
  const absent = COMPOSITION_PART_KINDS.every((k) => kindCov(k) === "absent");
  const items = useMemo<Item[]>(() => rows.rows.map(itemOf).filter((x): x is Item => x !== null)
    .sort((a, b) => rankOf(a.c.rule) - rankOf(b.c.rule) || KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || a.c.namespace.localeCompare(b.c.namespace) || a.c.name.localeCompare(b.c.name)), [rows.rows]);
  const visible = useMemo(() => items.filter((it) => !kindFilter || it.kind === kindFilter), [items, kindFilter]);

  const lastGood = lastGoodAt(rows.coverage, COMPOSITION_PART_KINDS);
  const discoveryGap = coverageGaps(rows.coverage);
  const capped = rows.coverage.filter((k) => k.truncated);
  const counted = rows.counts ? rows.counts.total : items.length;
  const attention = rows.counts ? rows.counts.included : items.filter((i) => i.c.rule.included).length;
  const faults = rows.counts ? rows.counts.fault : items.filter((i) => i.c.rule.role === "fault").length;
  const suspended = items.filter((i) => i.generator?.suspended).length;

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
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "flux" || intent.part !== "composition") return "wait";
    const kind = kindOfRef(intent.ref);
    const key = keyOf(kind, intent.ref.namespace ?? "", intent.ref.name);
    const found = items.find((i) => i.key === key);
    if (intent.ref.uid) expectPin(cluster, "FluxComposition", key, intent.ref.uid);
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
  const liveSel = useMemo(() => (selKey ? items.find((i) => i.key === selKey) : undefined), [items, selKey]);
  const pin = usePinnedSubject(cluster, selKey, liveSel, selKind ? evidenceOf(selKind) === "complete" : false, "FluxComposition");
  const selItem: Item | undefined = pin.subject ?? undefined;
  const gone: "removed" | "unverified" | null = pin.gone === "replaced" ? "removed" : pin.gone;

  let state: DatasetState = "ready";
  const anyEv = COMPOSITION_PART_KINDS.map(evidenceOf);
  if (absent) state = "empty";
  else if (anyEv.every((e) => e === "failed") && items.length === 0) state = "unreadable";
  else if (anyEv.some((e) => e === "pending") && items.length === 0) state = "loading";
  else if (visible.length === 0) state = "empty";
  const icon = <IconStack2 size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading source composition" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not read the ArtifactGenerators and ExternalArtifacts." hint={rows.watchError ?? undefined} />
    : absent ? <EmptyState icon={icon} title="Source composition is not installed on this cluster." hint="Klyx observes no ArtifactGenerator or ExternalArtifact API here." />
    : items.length > 0 ? <EmptyState icon={icon} title="Nothing of this kind." hint={`${items.length} composition objects on this cluster.`} action={{ label: "Clear filter", onClick: () => setKindFilter("") }} />
    : <EmptyState icon={icon} title="No ArtifactGenerator or ExternalArtifact yet." hint="The API is served; nothing composes sources here." />;

  const notices: React.ReactNode[] = [];
  if (rows.watchError && items.length > 0) notices.push(<WatchErrorBanner key="watch" tool="source composition" failure={`read failed - showing the last good state${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={rows.watchError} />);
  if (capped.length > 0) notices.push(<div key="capped" role="status" data-testid="composition-capped" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)" }}>
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
          {role.label}{role.kind === "fault" && it.c.reason ? ` · ${it.c.reason}` : ""}{it.artifact?.producer?.failing ? " · producer failing" : ""}
        </span>}
        {show("detail") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }} title={it.artifact?.revision}>{detailText(it, nowS)}</span>}
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
    void fluxOpAct(cluster, p.item.kind, p.item.c.namespace, p.item.c.name, p.item.uid, p.verb).then((r) => {
      useFleet.getState().setActionStatus(r.ok
        ? { kind: "success", message: `${verbWord(p.verb)} requested for ${p.item.kind} ${p.item.c.namespace}/${p.item.c.name}` }
        : { kind: "error", message: r.error });
    });
  };

  return (
    <>
      <LensLayout
        label="Flux"
        title="Flux CD"
        part="flux-composition"
        summary={absent || items.length === 0 ? null : (
          <>
            <CountSentence total={counted} marker={anyEv.every((e) => e === "complete") ? "" : "+"} noun={["composition object", "composition objects"]} />
            {faults > 0 ? <MarkedPhrase fault>{`${faults} failing`}</MarkedPhrase> : attention > 0 ? <span className="t-secondary">{attention} not ready</span> : <span className="t-secondary">none failing</span>}
            {suspended > 0 && <span className="t-secondary">{suspended} suspended</span>}
          </>
        )}
        controls={(
          <>
            <LensParts cluster={cluster} groupId="flux" />
            {!absent && (
              <div role="group" aria-label="Composition kinds" className="l-seg">
                {chip("", "All")}
                {chip("ArtifactGenerator", "Generators")}
                {chip("ExternalArtifact", "Artifacts")}
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
        footerEnd={<span className="t-secondary">broken first · an artifact's producer is never its verdict</span>}
        inspector={selItem ? <CompositionInspector cluster={cluster} it={selItem} items={items} nowS={nowS} gone={gone} replaced={pin.gone === "replaced" ? pin.adopt : undefined}
            onVerb={(verb) => setPending({ cluster, item: selItem, verb })} onOpen={(key) => { setKindFilter(""); setCursorKey(key); setSelKey(key); }} onClose={() => setSelKey(null)} />
          : selKey && pin.expected && pin.gone ? <PinnedGone cluster={cluster} kind={selKind} name={selKey.split("/").slice(1).join("/")} uid={pin.expected} gone={pin.gone} onAdopt={pin.next ? pin.adopt : undefined} onClose={() => setSelKey(null)} />
          : undefined}
        inspectorLabel="Composition object details"
        onCloseInspector={() => setSelKey(null)}
      />
      {confirm && (
        <ConfirmDialog
          title={`${verbWord(confirm.verb)} ArtifactGenerator`}
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

// joinCount is a join's count, qualified by its read's state.
const joinCount = (n: number, state?: string) => (state === "not readable" ? "not readable" : state === "incomplete" ? `${n}+` : String(n));

function RefLine({ r, items, onOpen }: { r: CompositionRefDTO; items: Item[]; onOpen: (key: string) => void }) {
  const key = keyOf(r.kind ?? "", r.namespace, r.name);
  const here = items.some((i) => i.key === key);
  const text = <>{r.alias ? `${r.alias}: ` : ""}{r.kind ? `${r.kind} ` : ""}{r.namespace}/{r.name}</>;
  const tail = r.found ? <span style={{ color: ROLE_TEXT[r.role ?? "unknown"] ?? ROLE_TEXT.unknown }}> · {r.label}</span>
    : <span style={{ color: "var(--color-text-warning)" }}> · {r.state ?? "not found"}</span>;
  return here ? <button className="t-body" onClick={() => onOpen(key)} style={linkStyle}>{text}{tail}</button>
    : <div className="t-body" style={{ padding: "2px 0", overflowWrap: "anywhere" }}>{text}{tail}</div>;
}

function CompositionInspector({ cluster, it, items, nowS, gone, replaced, onVerb, onOpen, onClose }: {
  cluster: string; it: Item; items: Item[]; nowS: number; gone: "removed" | "unverified" | null; replaced?: () => void;
  onVerb: (v: Verb) => void; onOpen: (key: string) => void; onClose: () => void;
}) {
  const role = roleOf(it.c.rule);
  const describe = useDescribe(cluster, !gone ? { kind: it.kind, namespace: it.c.namespace, name: it.c.name, uid: it.uid } : null);
  const ai = useInspectorAI(it.uid ? { kind: "row" as const, task: "flux", part: "composition", uid: it.uid } : null, `${it.kind}/${it.c.namespace}/${it.c.name}`, !gone, cluster);
  const [detail, setDetail] = useState<{ uid: string; d: CompositionDetailDTO } | null>(null);
  useEffect(() => {
    let live = true;
    setDetail(null);
    if (gone) return;
    const uid = it.uid;
    void getCompositionDetail(cluster, it.kind, it.c.namespace, it.c.name, uid).then((d) => { if (live) setDetail({ uid, d }); });
    return () => { live = false; };
  }, [cluster, it.kind, it.c.namespace, it.c.name, it.uid, it.c.generation, gone]);
  // a failed read is its request's (the UID asked for), shown as it is
  const mine = detail && detail.uid === it.uid ? detail.d : null;
  const readError = mine?.error ? mine.error : null;
  const d = mine && !readError ? mine : null;

  const g = it.generator;
  const a = it.artifact;
  const verbs: MenuItem[] = gone || !g ? [] : g.suspended ? [{ label: "Resume", onSelect: () => onVerb("resume") }] : [
    { label: "Reconcile", onSelect: () => onVerb("reconcile") },
    { label: "Suspend", danger: true, onSelect: () => onVerb("suspend") },
  ];
  const lagging = it.c.rule.label === "Syncing…";
  const awaiting = it.c.rule.label === AWAITING;
  const producerKey = a?.producer && attributed(a.producer) ? keyOf("ArtifactGenerator", a.producer.namespace ?? "", a.producer.name ?? "") : "";

  return (
    <div data-testid="composition-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: it.uid || undefined }} replaced={replaced} kind={it.kind} name={it.c.name} meta={it.c.namespace} onClose={onClose}
        verdict={gone ? (
          <div role="status" className="t-body"><span className="t-status">{gone === "removed" ? `${it.kind} removed.` : `Not in the current ${it.kind} read.`}</span> The details below are its last evidence.</div>
        ) : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label}
              reason={lagging ? `generation ${it.c.generation} not yet observed` : role.kind === "fault" || role.kind === "degraded" ? it.c.reason : undefined} />
            {(role.kind === "fault" || role.kind === "degraded") && it.c.message && <div className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere", marginTop: -4 }}>{it.c.message}</div>}
            {awaiting && <div className="t-body" data-testid="composition-awaiting" style={{ color: "var(--color-text-secondary)", marginTop: 4 }}>Nothing has changed since suspension, and the generator records nothing when nothing drifts. Reconcile to have it evaluate now.</div>}
            {g?.suspended && <div className="t-body" style={{ color: "var(--color-text-secondary)", marginTop: 4 }}>Suspended by its annotation - an intended pause. Its artifacts stay as they are.</div>}
            {a && !a.hasArtifact && <div className="t-body" style={{ color: "var(--color-text-secondary)", marginTop: 4 }}>No artifact yet: its consumers cannot apply.</div>}
            {it.c.incomplete.length > 0 && <div className="t-secondary" style={{ marginTop: 4, color: "var(--color-text-warning)" }}>Too large to keep whole: {it.c.incomplete.join(", ")}. Joins that need it claim nothing.</div>}
          </div>
        )}
        more={[...verbs, ...(verbs.length ? [{ kind: "separator" as const }] : []), ...describe.menu, ...ai.menu]}
      >
        {ai.block}
        {a && (
          <>
            <InspectorBlock title="Artifact">
              {a.hasArtifact ? (
                <div className="insp-kv" data-testid="composition-artifact">
                  <span>revision</span><span style={{ overflowWrap: "anywhere" }}>{a.revision || "-"}{a.revision && <CopyButton text={a.revision} label="copy revision" />}</span>
                  {a.digest && <><span>digest</span><span style={{ overflowWrap: "anywhere" }}>{shortRev(a.digest)}</span></>}
                  {a.origin && <><span>served from</span><span title="the URL's scheme and host only; its path is not shown">{a.origin}</span></>}
                  {a.originRevision && <><span>origin revision</span><span style={{ overflowWrap: "anywhere" }} title="as the producer recorded it">{a.originRevision}</span></>}
                  {a.lastUpdate && <><span>updated</span><span><Age unixSeconds={unix(a.lastUpdate)} suffix=" ago" /></span></>}
                  {a.size ? <><span>size</span><span>{a.size} bytes</span></> : null}
                </div>
              ) : <div className="t-secondary">No artifact recorded.</div>}
            </InspectorBlock>
            <InspectorBlock title="Producer">
              {!a.producer ? <div className="t-secondary">Not read.</div> : (
                <div className="t-body" data-testid="composition-producer" style={{ overflowWrap: "anywhere" }}>
                  {producerKey && items.some((i) => i.key === producerKey)
                    ? <button onClick={() => onOpen(producerKey)} style={{ ...linkStyle, display: "inline" }}>{producerText(a.producer)}</button>
                    : producerText(a.producer)}
                  {attributed(a.producer) && a.producer.label && <span style={{ color: ROLE_TEXT[a.producer.role ?? "unknown"] ?? ROLE_TEXT.unknown }}> · {a.producer.label}</span>}
                  {attributed(a.producer) && a.producer.failing && <div style={{ color: "var(--color-text-warning)" }}>The producer is failing; this artifact is its last good one{a.lastUpdate ? ` (updated ${formatAge(Math.max(0, nowS - unix(a.lastUpdate)))} ago)` : ""}.</div>}
                  <div className="t-secondary" style={{ marginTop: 4 }}>The producer's state is its own; it never stands in for this artifact's verdict. Its verbs are in its own inspector.</div>
                </div>
              )}
            </InspectorBlock>
            <InspectorBlock title={`Consumers · ${d ? joinCount(d.consumers.length, d.consumersState) : "…"}`}>
              {readError ? <div className="t-body" style={{ color: "var(--color-text-warning)" }}>Could not be read: {readError}</div>
                : !d ? <div className="t-secondary">Reading…</div>
                : d.consumersState === "not readable" ? <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The Kustomizations and HelmReleases could not be read, so which use it is not known.</div>
                : d.consumers.length === 0 ? <div className="t-secondary">{d.consumersState === "incomplete" ? "None among those Klyx retains; the read is capped, so others may." : "No Kustomization or HelmRelease is bound to it."}</div>
                : d.consumers.map((c) => <RefLine key={`${c.kind}/${c.namespace}/${c.name}`} r={c} items={items} onOpen={onOpen} />)}
            </InspectorBlock>
          </>
        )}
        {g && (
          <>
            <InspectorBlock title={`Sources · ${d ? joinCount(d.sources.length, d.sourcesState) : g.sources.length}`}>
              {(d ? d.sources : g.sources).map((s) => <RefLine key={`${s.alias}/${s.kind}/${s.namespace}/${s.name}`} r={d ? s : { ...s, state: "reading…" }} items={items} onOpen={onOpen} />)}
              {g.sourcesIncomplete && <div className="t-secondary">Too many sources to keep whole; the list is incomplete.</div>}
            </InspectorBlock>
            <InspectorBlock title={`Artifacts it declares · ${g.outputs.length}`}>
              {g.outputs.map((o) => (
                <div key={o.name} className="t-body" style={{ padding: "2px 0", overflowWrap: "anywhere" }}>
                  {o.name}{o.revision ? ` · revision ${o.revision}` : ""}
                  {o.copy.map((c, i) => <div key={i} className="t-secondary" style={{ fontFamily: "var(--font-mono)" }}>{c}</div>)}
                </div>
              ))}
            </InspectorBlock>
            <InspectorBlock title={`Generated · ${joinCount(g.generated.length, g.generatedState)}`}>
              {g.generated.length === 0 ? <div className="t-secondary">No ExternalArtifact recorded in its inventory.</div>
                : g.generated.map((r) => <RefLine key={`${r.namespace}/${r.name}`} r={{ ...r, state: r.found ? undefined : g.generatedState === "found" ? "not found" : g.generatedState }} items={items} onOpen={onOpen} />)}
            </InspectorBlock>
          </>
        )}
        <InspectorBlock title="Events">
          {readError ? <div className="t-body" data-testid="composition-read-error" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Could not be read: {readError}</div>
            : !d ? <div className="t-secondary">Reading…</div>
            : d.eventsError ? <div className="t-body" style={{ color: "var(--color-text-warning)" }}>Events could not be read: {d.eventsError}</div>
            : d.events.length === 0 ? <div className="t-secondary">No recent events (they age out after about an hour).</div>
            : d.events.slice(0, 8).map((e, i) => <div key={i} className="t-body" style={{ color: e.type === "Warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{e.reason}: {e.message}</div>)}
        </InspectorBlock>
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}
