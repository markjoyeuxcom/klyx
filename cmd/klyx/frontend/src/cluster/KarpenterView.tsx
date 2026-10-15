import { useEffect, useMemo, useRef, useState } from "react";
import { IconServerBolt } from "@tabler/icons-react";
import { useNavIntent } from "../chrome/useNavIntent";
import { useFleet } from "../store/fleet";
import type { KarpenterClaimDTO, KarpenterEventDTO, KarpenterPoolDTO, KarpenterSnapshotDTO, RuleDTO } from "../store/fleet";
import { getKarpenterState } from "../bridge/karpenter";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { Age, UpdatedAt, formatAge, useNow } from "../chrome/Age";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { useKarpenterRows } from "../health/useKarpenterRows";
import { clock, coverageGaps, joinCoverage, kindEvidence, lastGoodAt, type JoinCoverage, type KindEvidence } from "../layout/coverage";
import { useListKeys } from "../chrome/useListKeys";
import { VerdictLamp } from "../health/VerdictView";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { fmtCpu, fmtMem } from "./saturation";
import { useInspectorAI } from "../ai/AIExplain";
import { useClusterKey } from "./useClusterKey";
import { usePinnedSubject } from "./usePinnedSubject";
import { PinnedGone } from "./PinnedGone";
import { expectPin } from "../nav/inspectorPin";

// KarpenterView - the node-autoscaling diagnosis lens on the workspace
// template (spec §6 step 5), in Karpenter's vocabulary. Read-only. NodePools
// and NodeClaims are the health Observer's shared rows (1c slice 3 §5.5); the
// controller probe and the disruption feed are a snapshot read on demand
// (Refresh). Controller health is its own headline fault; NodeClaims are
// stuck-first with the failing launch-chain link named (Launched ->
// Registered -> Initialized); NodePools show provisioned capacity against
// limits; drift and consolidation are Karpenter's intended behaviour, never
// faults; disruption activity is evidence (events), never a lamp.
//
// Roles come from the relocated rule computed in Go (internal/karpenter
// rules.go, 1c addendum §2): a claim whose chain has a False condition is a
// fault; one still Unknown is launching (progressing) until Karpenter's own
// 15m registration TTL, Klyx's attention heuristic, after which it "appears
// stuck". Elapsed time is not proof.

type Role = { kind: "fault" | "degraded" | "progressing" | "ok" | "unknown"; label: string };
type View = "claims" | "pools" | "activity";

// A link is named as THE failure only when its own status is False; an
// overall Ready=False with no proven link stays adverse but unattributed.
// A claim with a node has registered: what remains is initialization.
export const provenLink = (c: KarpenterClaimDTO) => (c.failingStatus === "False" ? c.failingType : "");
const roleOf = (x: { rule: RuleDTO }): Role => ({ kind: x.rule.role as Role["kind"], label: x.rule.label });
const RANK: Record<Role["kind"], number> = { fault: 0, degraded: 1, progressing: 2, unknown: 3, ok: 4 };
// An unrecognised role ranks with unknown (fail closed, never as ok).
const rankOf = (x: { rule: RuleDTO }) => RANK[x.rule.role as Role["kind"]] ?? RANK.unknown;
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)",
};
const roleTone = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
function Lamp({ role }: { role: Role }) {
  return <span role="img" aria-label={role.label} style={{ display: "inline-flex", justifySelf: "center" }}><VerdictLamp health={role} size={8} /></span>;
}
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const pct = (used: number | null, limit: number | null) => (used != null && limit != null && limit > 0 ? used / limit : null);
const eventKey = (e: KarpenterEventDTO) => `${e.kind}/${e.name}/${e.reason}/${e.lastSeenUnix}`;
// ageOf ticks a claim's age from its creation time (rows are pushed only when
// they change - slice 3 §3); the capture-relative age is the fallback.
const ageOf = (c: KarpenterClaimDTO, nowS: number) => (c.createdUnix ? Math.max(0, nowS - c.createdUnix) : c.ageSeconds);
// countText qualifies a join count by the kind it reads: "?" unread, "*" stale.
const countText = (n: number, cov: JoinCoverage) => (cov === "unknown" ? "?" : `${n}${cov === "stale" ? "*" : ""}`);

const CLAIM_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "NodeClaim", width: 220, priority: 0, flex: true },
  { id: "status", label: "Status", width: 190, priority: 0 },
  // The NodePool name is an identifier: it shares spare width with the
  // claim name rather than clipping beside a short Instance value
  // (UI review 2026-10-13).
  { id: "pool", label: "NodePool", width: 170, priority: 1, grow: 0.5 },
  { id: "type", label: "Instance", width: 110, priority: 2 },
  { id: "capacity", label: "Capacity", width: 80, priority: 3 },
  { id: "age", label: "Age", width: 56, priority: 1 },
];
const POOL_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "NodePool", width: 200, priority: 0, flex: true },
  { id: "status", label: "Status", width: 160, priority: 0 },
  { id: "nodes", label: "Nodes", width: 60, priority: 1 },
  { id: "cpu", label: "CPU vs limit", width: 150, priority: 1 },
  { id: "mem", label: "Memory vs limit", width: 160, priority: 2 },
];
const EVENT_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "type", label: "Type", width: 64, priority: 1 },
  { id: "age", label: "Last seen", width: 64, priority: 0 },
  { id: "reason", label: "Reason", width: 170, priority: 0 },
  { id: "object", label: "Object", width: 200, priority: 1 },
  { id: "message", label: "Message", width: 260, priority: 0, flex: true },
];

function capText(used: number | null, limit: number | null, fmt: (n: number) => string): { text: string; near: boolean } {
  if (used == null) return { text: "-", near: false };
  if (limit == null) return { text: `${fmt(used)} (no limit)`, near: false };
  const p = pct(used, limit);
  return { text: `${fmt(used)} / ${fmt(limit)}${p != null ? ` · ${Math.round(p * 100)}%` : ""}`, near: p != null && p >= 0.9 };
}

export function KarpenterView({ cluster }: { cluster: string }) {
  const rows = useKarpenterRows(cluster);
  const nowS = Math.floor(useNow() / 1000);
  const [pinned, setPinned] = useState<{ cluster: string; snap: KarpenterSnapshotDTO; at: number } | null>(null);
  const [loading, setLoading] = useState(false);
  const [view, setView] = useState<View>("claims");
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKeyRaw] = useClusterKey(cluster);
  // One-shot auto-select of the first NodeClaim needing attention, like the
  // other lenses (UI review 2026-10-13): once, from the first complete read,
  // never over a selection the operator made, a dismissal, a view switch or a
  // reveal. Every selection below goes through setSelKey, which ends it.
  const autoSelect = useRef<"pending" | "done">("pending");
  const setSelKey = (k: string | null) => { autoSelect.current = "done"; setSelKeyRaw(k); };
  const [bodyWidth, setBodyWidth] = useState(1100);
  const listRef = useRef<VirtualListHandle>(null);
  const gen = useRef(0);

  // The controller/feed snapshot answers one cluster at one time; a refresh
  // keeps the previous snapshot until the new one lands; a late one is dropped.
  const fetchState = async () => {
    const g = ++gen.current;
    const origin = cluster;
    setLoading(true);
    const s = await getKarpenterState(origin);
    if (g !== gen.current) return;
    setLoading(false);
    setPinned({ cluster: origin, snap: s, at: Date.now() });
  };
  useEffect(() => {
    setPinned(null); setSelKeyRaw(null); setCursorKey(null);
    autoSelect.current = "pending";
    void fetchState();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster]);

  const snap = pinned && pinned.cluster === cluster ? pinned.snap : null;
  const ok = snap && !snap.error && snap.detected ? snap : null;
  // Availability (Codex review of 442d42a #3): the rows' own per-kind
  // discovery owns it for the NodePools and NodeClaims - only a confirmed
  // absence of both is "not installed"; checking is not absence. The
  // controller/feed snapshot's capability read speaks for the activity view
  // alone and never hides observed rows.
  const kindCov = (kind: string) => rows.coverage.find((k) => k.kind === kind)?.coverage;
  const rowsAbsent = kindCov("NodePool") === "absent" && kindCov("NodeClaim") === "absent";
  const feedDetected = !!snap && !snap.error && snap.detected;
  const evidenceOf = (kind: string): KindEvidence => kindEvidence(!rows.loading, rows.coverage, kind, rows.watchError);
  const claimsEv = evidenceOf("NodeClaim");
  const poolsEv = evidenceOf("NodePool");
  const claims = useMemo(() => [...rows.claims].sort((a, b) => rankOf(a) - rankOf(b) || (b.createdUnix ?? 0) - (a.createdUnix ?? 0) || a.name.localeCompare(b.name)), [rows.claims]);
  const pools = useMemo(() => [...rows.pools].sort((a, b) => rankOf(a) - rankOf(b) || a.name.localeCompare(b.name)), [rows.pools]);
  const events = useMemo(() => [...(ok?.events ?? [])].sort((a, b) => b.lastSeenUnix - a.lastSeenUnix), [ok]);
  // A4 (slice 3 §4) and the evidence beside the rows.
  const rowsRefreshing = rows.rowsState.refreshing;
  const rowsAsOf = rows.rowsState.evaluatedAt ? clock(rows.rowsState.evaluatedAt) : "the last read";
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";
  const lastGood = lastGoodAt(rows.coverage, ["NodePool", "NodeClaim"]);
  const discoveryGap = coverageGaps(rows.coverage);

  // global: the count selector's (slice 3 §4a); row-scoped: the breakdown
  const claimTotal = rows.partCounts.nodeclaims?.total ?? claims.length;
  const claimAttention = rows.partCounts.nodeclaims?.included ?? claims.filter((c) => c.rule.included).length;
  const poolAttention = rows.partCounts.nodepools?.included ?? pools.filter((p) => p.rule.included).length;
  const failed = claims.filter((c) => roleOf(c).kind === "fault").length;
  const stuck = claims.filter((c) => roleOf(c).kind === "degraded").length;
  const launching = claims.filter((c) => roleOf(c).kind === "progressing").length;
  const poolsDown = pools.filter((p) => roleOf(p).kind === "fault").length;
  const poolsUnknown = pools.filter((p) => roleOf(p).kind === "unknown").length;
  const drifted = claims.filter((c) => c.drifted).length;
  // "all ready" is a claim about the whole present (Codex review of 442d42a
  // #2): both lists read in full and current, every role ready, and no
  // summary ahead of the rows.
  const allReady = claims.length > 0 && claimsEv === "complete" && poolsEv === "complete" && !rows.rowsState.updating && !rows.rowsState.refreshing
    && claims.every((c) => roleOf(c).kind === "ok") && pools.every((p) => roleOf(p).kind === "ok");
  // A pool's node count is a join read in the NodePools capture: it carries
  // that capture's own NodeClaim dependency coverage - never the NodeClaims
  // list's (Codex review of 442d42a #1).
  const claimsCov = joinCoverage(rows.poolNodesDep ? kindEvidence(true, [rows.poolNodesDep], "NodeClaim", null) : "pending");


  const keys: string[] = view === "claims" ? claims.map((c) => c.name) : view === "pools" ? pools.map((p) => p.name) : events.map(eventKey);
  const cursorIdx = cursorKey === null ? -1 : keys.indexOf(cursorKey);
  useListKeys({
    count: keys.length,
    selected: cursorIdx,
    onSelect: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const k = keys[idx]; if (k) { setCursorKey(k); setSelKey(k); } },
    onEscape: () => setSelKey(null),
  });

  const columns = view === "claims" ? CLAIM_COLUMNS : view === "pools" ? POOL_COLUMNS : EVENT_COLUMNS;
  const fitted = useMemo(() => fitColumns(columns, bodyWidth), [columns, bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };
  const switchView = (v: View) => { setView(v); setSelKey(null); setCursorKey(null); };
  useEffect(() => {
    if (autoSelect.current === "done" || view !== "claims" || claimsEv !== "complete") return;
    autoSelect.current = "done";
    const c = claims.find((x) => x.rule.included);
    if (c) { setCursorKey(c.name); setSelKeyRaw(c.name); }
  }, [view, claimsEv, claims, setSelKeyRaw]);
  // A summary Top link (health 1c slice 4 §5.4): open the part's view and
  // select the object - selection only, never an action.
  useNavIntent(cluster, ["reveal"], (intent) => {
    if (intent.kind !== "reveal" || intent.task !== "karpenter") return "wait";
    const pool = intent.part === "nodepools";
    const row = pool ? pools.find((p) => p.name === intent.ref.name) : claims.find((c) => c.name === intent.ref.name);
    // An opening by UID (slice 9 note §4.2): pinned to that UID at once; a
    // different or missing row is said, never substituted.
    if (intent.ref.uid) expectPin(cluster, intent.ref.name, intent.ref.uid);
    else if (!row) return (pool ? poolsEv : claimsEv) === "complete" ? "missing" : "wait";
    setView(pool ? "pools" : "claims");
    setCursorKey(intent.ref.name);
    setSelKey(intent.ref.name);
    return "consumed";
  }, [claims, pools, claimsEv, poolsEv]);

  const eventsFailed = !!ok?.eventsError;
  const viewEv = view === "claims" ? claimsEv : poolsEv;
  let state: DatasetState = "ready";
  if (view === "activity") {
    if (!snap) state = "loading";
    else if (!snap.error && !snap.detected) state = "empty";
    else if (snap.error || (eventsFailed && keys.length === 0)) state = "unreadable";
    else if (keys.length === 0) state = "empty";
  } else if (rowsAbsent) state = "empty";
  else if (viewEv === "failed" && keys.length === 0) state = "unreadable";
  else if (viewEv === "pending" && keys.length === 0) state = "loading";
  else if (keys.length === 0) state = "empty";

  const icon = <IconServerBolt size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label={view === "activity" ? "reading disruption activity" : "reading NodePools and NodeClaims"} />
    : state === "unreadable" && view === "activity" && !snap?.error ? <EmptyState icon={icon} title="Could not read disruption activity." hint={ok?.eventsError} action={{ label: "Retry", onClick: () => void fetchState() }} />
    : state === "unreadable" && view === "activity" ? <EmptyState icon={icon} title="Could not read Karpenter state." hint={snap?.error} action={{ label: "Retry", onClick: () => void fetchState() }} />
    : state === "unreadable" ? <EmptyState icon={icon} title={`Could not read the ${view === "claims" ? "NodeClaims" : "NodePools"}.`} hint={rows.watchError ?? undefined} />
    : view === "activity" && snap && !snap.detected ? <EmptyState icon={icon} title="Disruption activity was not read." hint="The controller probe did not detect Karpenter on this cluster." />
    : view !== "activity" && rowsAbsent ? <EmptyState icon={icon} title="Karpenter is not installed on this cluster." hint="Klyx observes no karpenter.sh NodePools or NodeClaims here." />
    : view !== "activity" && rowsRefreshing && (view === "claims" ? claimTotal : (rows.partCounts.nodepools?.total ?? 0)) > 0 ? <EmptyState icon={icon} title={`No ${view === "claims" ? "NodeClaims" : "NodePools"} as of ${rowsAsOf}.`} hint="Refreshing - the cluster summary counts more." />
    : view !== "activity" && viewEv === "stale" ? <EmptyState icon={icon} title={`No ${view === "claims" ? "NodeClaims" : "NodePools"} in the last good read.`} hint={`The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${rows.watchError ?? "unverified"}.`} />
    : view === "claims" ? <EmptyState icon={icon} title="No NodeClaims." hint="Karpenter has provisioned nothing on this cluster right now." />
    : view === "pools" ? <EmptyState icon={icon} title="No NodePools defined." hint="Without a NodePool, Karpenter provisions nothing." />
    : <EmptyState icon={icon} title="No recent disruption activity." hint="Events age out of the API server after about an hour - quiet can be real." />;

  // Independent inputs compose: controller status AND the feed's own
  // evidence are both shown; neither hides the other.
  const feedIncomplete = !!ok && (eventsFailed || !!ok.eventsTruncated);
  const controllerNotice = !ok ? null : ok.controllerVerified !== false && !ok.controllerHealthy ? (
    <div data-testid="karp-controller-down" role="status" className="t-body" style={{ color: "var(--color-text-danger)", padding: "4px 0" }}>
      <span className="t-status">▲ The Karpenter controller is not ready.</span> No provisioning, disruption or drift replacement happens until it recovers - the states below are its last word.
    </div>
  ) : ok.controllerVerified === false ? (
    <div data-testid="karp-controller-unverified" role="status" className="t-body" style={{ color: "var(--color-text-tertiary)", padding: "4px 0", overflowWrap: "anywhere" }}>
      <span className="t-status" style={{ color: "var(--color-text-secondary)" }}>Controller health unverified.</span> {ok.controllerError ? ok.controllerError.charAt(0).toUpperCase() + ok.controllerError.slice(1) : "The controller Deployment could not be read."}
    </div>
  ) : null;
  // a capped read is the newest among the events READ - never a claim about
  // unread pages, and a full page alone does not prove more exist
  const activityNotice = view === "activity" && feedIncomplete && events.length > 0 ? (
    <div data-testid="karp-feed-partial" role="status" className="t-body" style={{ color: "var(--color-text-warning)", padding: "4px 0", overflowWrap: "anywhere" }}>
      {[eventsFailed ? `Partial read - ${ok!.eventsError}.` : "", ok!.eventsTruncated ? "Capped read - showing the newest among the events read." : ""].filter(Boolean).join(" ")}
    </div>
  ) : null;

  // Selections keep their last row - removed only on a complete read of their
  // own kind, otherwise unverified (slice 3 §5a) - and are pinned to their
  // UID: a same-name object with a new UID on a complete read is "replaced"
  // (The Line slice 4 §6.3), never silently followed.
  const claimPin = usePinnedSubject(cluster, view === "claims" ? selKey : null, view === "claims" && selKey ? claims.find((c) => c.name === selKey) : undefined, claimsEv === "complete");
  const poolPin = usePinnedSubject(cluster, view === "pools" ? selKey : null, view === "pools" && selKey ? pools.find((p) => p.name === selKey) : undefined, poolsEv === "complete");
  const selClaim = claimPin.subject ?? undefined;
  // Notices stack: each is its own evidence, none hides another.
  const notices: React.ReactNode[] = [];
  if (snap?.error) notices.push(<div key="snap" role="status" data-testid="karp-snapshot-error" className="t-body" style={{ color: "var(--color-text-tertiary)", padding: "4px 0", overflowWrap: "anywhere" }}><span className="t-status" style={{ color: "var(--color-text-secondary)" }}>Controller health and disruption activity could not be read.</span> {snap.error}</div>);
  if (controllerNotice) notices.push(<div key="ctrl">{controllerNotice}</div>);
  if (rows.watchError && (claims.length + pools.length) > 0) notices.push(<WatchErrorBanner key="watch" tool="Karpenter" failure={`read failed - showing the last good state${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={rows.watchError} />);
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={{ padding: "4px 0" }}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{rows.rowsState.refreshError ? ` Last re-read failed: ${rows.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ padding: "4px 0", color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);
  if (activityNotice) notices.push(<div key="feed">{activityNotice}</div>);
  const selPool = poolPin.subject ?? undefined;
  const claimGone: "removed" | "unverified" | null = claimPin.gone === "replaced" ? "removed" : claimPin.gone;
  const poolGone: "removed" | "unverified" | null = poolPin.gone === "replaced" ? "removed" : poolPin.gone;
  const selEvent = view === "activity" && selKey ? events.find((e) => eventKey(e) === selKey) : undefined;

  const row = (k: string, children: React.ReactNode) => (
    <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
      aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
      onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
      onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(k); } }}>
      {children}
    </div>
  );

  return (
    <LensLayout
      label="Karpenter"
      title="Karpenter"
      summary={rowsAbsent || claimsEv === "pending" && claims.length === 0 || claimsEv === "failed" && claims.length === 0 ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{claimTotal}{claimsEv === "complete" ? "" : claimsEv === "stale" ? "*" : "+"}</span>
          <span className="t-secondary">NodeClaims</span>
          {rowsRefreshing && claimAttention > 0 && <span className="t-status" style={{ color: "var(--color-text-warning)" }} data-testid="karp-global-attention">{claimAttention} not ready or appear stuck</span>}
          {failed + stuck + poolsDown > 0
            ? <span className="t-status" style={{ color: failed + poolsDown > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{failed + poolsDown > 0 ? "▲ " : ""}{[failed ? `${failed} not ready` : "", stuck ? `${stuck} appear${stuck === 1 ? "s" : ""} stuck` : "", poolsDown ? `${poolsDown} NodePool${poolsDown === 1 ? "" : "s"} not ready` : ""].filter(Boolean).join(" · ")}</span>
            : launching > 0 ? <span className="t-secondary">{launching} in progress</span>
            : allReady ? <span className="t-secondary">all ready</span>
            : claims.length > 0 && poolsUnknown === 0 ? <span className="t-secondary" data-testid="karp-not-all-ready">{claimsEv === "stale" || poolsEv === "stale" ? "none known not ready (last observed)" : rowsRefreshing || rows.rowsState.updating ? "none known not ready (summary changed - re-reading)" : "none known not ready (reads incomplete)"}</span> : null}
          {poolsUnknown > 0 && <span className="t-secondary">{poolsUnknown} NodePool{poolsUnknown === 1 ? "" : "s"} unverified</span>}
          {drifted > 0 && <span className="t-secondary">{drifted} drifted (eligible for replacement)</span>}
          {(failed + stuck + poolsDown + launching + drifted) > 0 && asOf && <span className="t-secondary" data-testid="karp-asof">{asOf.trim()}</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary" title="The controller probe and disruption feed are read on demand; NodePools and NodeClaims are live.">controller and events: snapshot</span>
          <UpdatedAt at={pinned && pinned.cluster === cluster ? pinned.at : null} />
          <button className="l-btn" onClick={() => void fetchState()} disabled={loading}>{loading ? "Reading…" : "Refresh"}</button>
        </>
      }
      controls={!rowsAbsent || feedDetected ? (
        <div role="group" aria-label="Karpenter view" style={{ display: "inline-flex", gap: 4 }}>
          <button className="l-chip" aria-pressed={view === "claims"} onClick={() => switchView("claims")}>NodeClaims {claimAttention > 0 && <span style={{ color: failed ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{claimAttention}</span>}</button>
          <button className="l-chip" aria-pressed={view === "pools"} onClick={() => switchView("pools")}>NodePools {poolAttention > 0 && <span style={{ color: "var(--color-text-danger)" }}>{poolAttention}</span>}</button>
          <button className="l-chip" aria-pressed={view === "activity"} onClick={() => switchView("activity")}>Disruption activity {(events.length > 0 || feedIncomplete) && <span style={{ color: "var(--color-text-secondary)" }}>{events.length}{feedIncomplete ? "+" : ""}</span>}</button>
        </div>
      ) : undefined}
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2 }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {columns.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          {view === "claims" ? (
            <VirtualList ref={listRef} items={claims} rowHeight={TABLE_ROW_HEIGHT} rowKey={(c) => c.name} style={{ flex: 1, minHeight: 0 }}
              render={(c) => {
                const role = roleOf(c);
                return row(c.name, <>
                  {show("lamp") && <Lamp role={role} />}
                  {show("name") && <span className="lens-cell-name" title={c.nodeName ? `node ${c.nodeName}` : "no node yet"}>{c.name}</span>}
                  {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontWeight: role.kind === "fault" ? 600 : 400 }}>{role.kind === "fault" ? "▲ " : ""}{role.label}{c.drifted ? " · drifted" : ""}</span>}
                  {show("pool") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{c.pool || "-"}</span>}
                  {show("type") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{c.instanceType || "-"}</span>}
                  {show("capacity") && <span style={{ color: "var(--color-text-tertiary)" }}>{c.capacityType || "-"}</span>}
                  {show("age") && <span style={{ color: "var(--color-text-tertiary)" }}>{formatAge(ageOf(c, nowS))}</span>}
                </>);
              }} />
          ) : view === "pools" ? (
            <VirtualList ref={listRef} items={pools} rowHeight={TABLE_ROW_HEIGHT} rowKey={(p) => p.name} style={{ flex: 1, minHeight: 0 }}
              render={(p) => {
                const role = roleOf(p);
                const cpu = capText(p.cpuUsed, p.cpuLimit, fmtCpu);
                const mem = capText(p.memUsed, p.memLimit, fmtMem);
                return row(p.name, <>
                  {show("lamp") && <Lamp role={role} />}
                  {show("name") && <span className="lens-cell-name">{p.name}</span>}
                  {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind] }}>{role.kind === "fault" ? "▲ " : ""}{role.label}{p.ready === "not-ready" && p.reason ? ` · ${p.reason}` : ""}</span>}
                  {show("nodes") && <span style={{ color: "var(--color-text-secondary)" }} title={claimsCov === "unknown" ? "NodeClaim read unavailable" : claimsCov === "stale" ? "from the last observed NodeClaim list" : undefined}>{countText(p.nodes, claimsCov)}</span>}
                  {show("cpu") && <span style={{ ...cellStyle, color: cpu.near ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{cpu.text}</span>}
                  {show("mem") && <span style={{ ...cellStyle, color: mem.near ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{mem.text}</span>}
                </>);
              }} />
          ) : (
            <VirtualList ref={listRef} items={events} rowHeight={TABLE_ROW_HEIGHT} rowKey={eventKey} style={{ flex: 1, minHeight: 0 }}
              render={(e) => row(eventKey(e), <>
                {show("type") && <span style={{ color: e.type === "Warning" ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>{e.type}</span>}
                {show("age") && <span style={{ color: "var(--color-text-tertiary)" }}><Age unixSeconds={e.lastSeenUnix} /></span>}
                {show("reason") && <span style={{ ...cellStyle, color: e.type === "Warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)", fontWeight: 500 }}>{e.reason}</span>}
                {show("object") && <span className="lens-cell-name"><span style={{ color: "var(--color-text-tertiary)" }}>{e.kind}/</span>{e.name}</span>}
                {show("message") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{e.message}</span>}
              </>)} />
          )}
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect</span>}
      footerEnd={<span className="t-secondary">{view === "claims" ? `stuck first · drift and consolidation are intended behaviour, not faults${asOf}` : view === "pools" ? `broken first${asOf}` : feedIncomplete ? "incomplete read · newest among the events read" : "newest first"}</span>}
      inspector={selClaim ? <ClaimInspector cluster={cluster} c={selClaim} nowS={nowS} gone={claimGone} replaced={claimPin.gone === "replaced" ? claimPin.adopt : undefined} onClose={() => setSelKey(null)} />
        : selPool ? <PoolInspector cluster={cluster} p={selPool} gone={poolGone} replaced={poolPin.gone === "replaced" ? poolPin.adopt : undefined} claimsCov={claimsCov} onClose={() => setSelKey(null)} />
        : selEvent ? <EventPanel cluster={cluster} e={selEvent} onClose={() => setSelKey(null)} />
        : selKey && view === "claims" && claimPin.expected && claimPin.gone ? <PinnedGone cluster={cluster} kind="NodeClaim" name={selKey} uid={claimPin.expected} gone={claimPin.gone} onAdopt={claimPin.next ? claimPin.adopt : undefined} onClose={() => setSelKey(null)} />
        : selKey && view === "pools" && poolPin.expected && poolPin.gone ? <PinnedGone cluster={cluster} kind="NodePool" name={selKey} uid={poolPin.expected} gone={poolPin.gone} onAdopt={poolPin.next ? poolPin.adopt : undefined} onClose={() => setSelKey(null)} />
        : undefined}
      inspectorLabel={view === "claims" ? "NodeClaim details" : view === "pools" ? "NodePool details" : "Event details"}
      onCloseInspector={() => setSelKey(null)}
    />
  );
}

function GoneNotice({ kind, gone }: { kind: string; gone: "removed" | "unverified" }) {
  return gone === "removed" ? (
    <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
      <span className="t-status">{kind} removed.</span> It is no longer in the current read. The details below are its last evidence.
    </div>
  ) : (
    <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
      <span className="t-status">Not in the current {kind} read.</span> That read is incomplete, so this is unverified - it may still exist. The details below are its last observed row.
    </div>
  );
}

function ClaimInspector({ cluster, c, nowS, gone, replaced, onClose }: { cluster: string; c: KarpenterClaimDTO; nowS: number; gone: "removed" | "unverified" | null; replaced?: () => void; onClose: () => void }) {
  const ai = useInspectorAI(c.uid ? { kind: "row" as const, task: "karpenter", part: "nodeclaims", uid: c.uid } : null, `NodeClaim/${c.name}`, !gone, cluster);
  const role = roleOf(c);
  const age = formatAge(ageOf(c, nowS));
  const openNode = () => { const nav = useFleet.getState(); nav.setSection("nodes"); nav.requestNavIntent({ kind: "select-node", cluster, node: c.nodeName }); };
  const link = provenLink(c);
  const pending = c.failingType && c.failingStatus !== "False" ? `${c.failingType} is ${c.failingStatus || "not reported"} (unverified)` : "";
  const detail = role.kind === "fault"
    ? link
      ? `The launch chain failed at ${link}${c.failingReason ? `: ${c.failingReason}` : ""}.`
      : `Ready is False, but no launch-chain link is confirmed failed${pending ? ` - ${pending}` : ""}.`
    : role.kind === "degraded"
      ? c.nodeName
        ? `Registered as node ${c.nodeName} but not initialized after ${age} - past Klyx's 15m attention heuristic${pending ? `; ${pending}` : ""}. Check the node's readiness, startup taints and daemonsets.`
        : `Not registered after ${age} - past Karpenter's 15m registration TTL (Klyx's attention heuristic here). Karpenter normally deletes a claim that fails to register in that window; check the controller and the cloud provider.`
      : role.kind === "progressing" ? `${c.nodeName ? "Initializing" : "Launching"} for ${age}${pending ? ` - ${pending}` : " - the launch chain has not finished yet"}.` : null;
  return (
    <div data-testid="karp-claim-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: c.uid || undefined }} replaced={replaced} kind="NodeClaim" name={c.name} meta={[c.pool && `pool ${c.pool}`, c.instanceType, c.capacityType].filter(Boolean).join(" · ")} onClose={onClose}
        verdict={gone ? <GoneNotice kind="NodeClaim" gone={gone} /> : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} />
            {detail && <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: -4 }}>{detail}</div>}
            {role.kind === "fault" && link && c.failingMessage && <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: 4 }}>{c.failingMessage}</div>}
          </div>
        )}
        primary={c.nodeName && gone !== "removed" ? { label: "Open node", onSelect: openNode } : undefined}
        more={ai.menu}
      >
        {ai.block}
        <InspectorBlock title="Claim">
          <div className="insp-kv">
            <span>node</span><span style={{ overflowWrap: "anywhere" }}>{c.nodeName || "not registered yet"}</span>
            <span>pool</span><span>{c.pool || "-"}</span>
            <span>instance</span><span>{c.instanceType || "-"}</span>
            <span>capacity</span><span>{c.capacityType || "-"}</span>
            <span>age</span><span>{age}</span>
          </div>
        </InspectorBlock>
        {(c.drifted || c.consolidatable) && (
          <InspectorBlock title="Disruption">
            {c.drifted && <div className="t-body">Drifted - its requirements no longer match the NodePool or NodeClass; it is eligible for replacement (disruption budgets can delay it).</div>}
            {c.consolidatable && <div className="t-body">Consolidatable - Karpenter may remove or replace it to save capacity.</div>}
            <div className="t-secondary" style={{ marginTop: 6 }}>Intended behaviour, not a fault.</div>
          </InspectorBlock>
        )}
      </InspectorLayout>
    </div>
  );
}

function PoolInspector({ cluster, p, gone, replaced, claimsCov, onClose }: { cluster: string; p: KarpenterPoolDTO; gone: "removed" | "unverified" | null; replaced?: () => void; claimsCov: JoinCoverage; onClose: () => void }) {
  const ai = useInspectorAI(p.uid ? { kind: "row" as const, task: "karpenter", part: "nodepools", uid: p.uid } : null, `NodePool/${p.name}`, !gone, cluster);
  const role = roleOf(p);
  const cpu = capText(p.cpuUsed, p.cpuLimit, fmtCpu);
  const mem = capText(p.memUsed, p.memLimit, fmtMem);
  return (
    <div data-testid="karp-pool-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster, uid: p.uid || undefined }} replaced={replaced} kind="NodePool" name={p.name} meta={`${claimsCov === "unknown" ? "nodes unverified (NodeClaim read unavailable)" : `${countText(p.nodes, claimsCov)} node${p.nodes === 1 ? "" : "s"}`} · weight ${p.weight}`} onClose={onClose}
        verdict={gone ? <GoneNotice kind="NodePool" gone={gone} /> : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} reason={p.ready === "not-ready" ? p.reason || undefined : undefined} />
            {p.ready === "not-ready" && p.message && <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: -4 }}>{p.message}</div>}
          </div>
        )}
        more={ai.menu}
      >
        {ai.block}
        <InspectorBlock title="Capacity vs limits">
          <div className="insp-kv">
            <span>cpu</span><span style={{ color: cpu.near ? "var(--color-text-warning)" : undefined }}>{cpu.text}</span>
            <span>memory</span><span style={{ color: mem.near ? "var(--color-text-warning)" : undefined }}>{mem.text}</span>
          </div>
          {(cpu.near || mem.near) && <div className="t-body" style={{ color: "var(--color-text-warning)", marginTop: 6 }}>At 90% or more of a limit - Karpenter stops provisioning from this pool once a limit is reached.</div>}
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}

function EventPanel({ cluster, e, onClose }: { cluster: string; e: KarpenterEventDTO; onClose: () => void }) {
  return (
    <div data-testid="karp-event-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster }} kind="Event" name={e.reason} meta={`${e.kind}/${e.name}`} onClose={onClose}
        verdict={<div className="t-body"><span className="t-status" style={{ color: e.type === "Warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{e.type}</span><span className="t-secondary"> · last <Age unixSeconds={e.lastSeenUnix} suffix=" ago" /></span></div>}>
        <InspectorBlock title="Message">
          <div className="t-body" style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{e.message || "No message."}</div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}
