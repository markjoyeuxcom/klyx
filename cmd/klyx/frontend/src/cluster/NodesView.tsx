import { Fragment, useEffect, useCallback, useMemo, useState, useRef, useLayoutEffect } from "react";
import { Events } from "@wailsio/runtime";
import { useFleet } from "../store/fleet";
import type { ConditionDTO, NodeDetailDTO, NodeSummaryDTO, PodOnNodeDTO } from "../store/fleet";
import { openNodeDetail, cordonNode, startDrain, cancelDrain, getNodeHeadroom } from "../bridge/nodes";
import type { HeadroomReportDTO } from "../store/fleet";
import type { DrainOptions } from "../bridge/nodes";
import { newStreamId } from "../bridge/streams";
import { getNodeUtilization } from "../bridge/metrics";
import type { NodeUtilizationResultDTO } from "../store/fleet";
import { saturation } from "./saturation";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { DescribePane } from "../chrome/DescribePane";
import { MetaTile, OperationDialog, OptionTile } from "../chrome/OperationDialog";
import { fmtMem, fmtCpu } from "./saturation";
import { useNavIntent } from "../chrome/useNavIntent";
import { useListKeys } from "../chrome/useListKeys";
import { CopyButton } from "../chrome/CopyButton";
import { YamlBlock } from "../chrome/YamlBlock";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt, formatAge, useNow, useReceivedAt } from "../chrome/Age";
import { IconAlertTriangle, IconServer } from "@tabler/icons-react";
import { ellipsis, btn, StatusDot } from "../chrome/primitives";
import { VerdictLabel, VerdictLamp, VerdictStatus } from "../health/VerdictView";
import { type VerdictCounts, attentionTone, isAttention, kindOf, orderOf, summarize } from "../health/verdict";
import { useNodeRows } from "../health/useNodeRows";
import { clock, coverageGaps, kindEvidence, lastGoodAt } from "../layout/coverage";
import { InspectorConditions, InspectorEvents } from "./ResourceInspector";
import { wellKnownResourceRef } from "./resourceRefs";
import { LensLayout, LensTable, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { vh } from "../app/zoom";

// columns: dot | name | roles | status | version | cpu alloc/cap | mem alloc/cap | taints | age
type NodeFilter = "all" | "attention" | "unknown" | "cordoned" | "tainted";

// Condition evidence, matching the node verdict (internal/health N-rules):
// confirmed adverse (Ready=False is a fault, pressure=True degraded) is kept
// apart from unverified (Unknown, or a status outside True/False), which never
// glows as a fault. A condition type with no known polarity (custom/NPD) is
// neutral native evidence: neither its True nor its False proves health.
type CondTone = "fault" | "degraded" | "unknown" | "ok" | "neutral";
const PRESSURE = new Set(["MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable"]);
function conditionTone(c: ConditionDTO): CondTone {
  const known = c.type === "Ready" || PRESSURE.has(c.type);
  if (!known) return "neutral";
  if (c.status !== "True" && c.status !== "False") return "unknown";
  if (c.type === "Ready") return c.status === "True" ? "ok" : "fault";
  return c.status === "True" ? "degraded" : "ok";
}
const isAdverse = (c: ConditionDTO) => { const t = conditionTone(c); return t === "fault" || t === "degraded"; };
const TONE_ORDER: Record<CondTone, number> = { fault: 0, degraded: 1, unknown: 2, neutral: 3, ok: 4 };
const condColor = (c: ConditionDTO) => ({
  fault: "var(--color-text-danger)",
  degraded: "var(--color-text-warning)",
  unknown: "var(--color-text-tertiary)",
  neutral: "var(--color-text-secondary)",
  ok: "var(--color-text-success)",
})[conditionTone(c)];

// Node health is the backend verdict (internal/health): fault (NotReady),
// degraded (pressure), unknown (Ready missing/Unknown), paused (cordoned), ok.
// Taints are a fact, never a verdict.
function nodeNeedsAttention(n: NodeSummaryDTO): boolean {
  return isAttention(kindOf(n.health));
}

function nodeMatchesFilter(n: NodeSummaryDTO, filter: NodeFilter): boolean {
  if (filter === "attention") return nodeNeedsAttention(n);
  if (filter === "unknown") return kindOf(n.health) === "unknown";
  if (filter === "cordoned") return n.unschedulable;
  if (filter === "tainted") return n.taintCount > 0;
  return true;
}

function nodeMatchesQuery(n: NodeSummaryDTO, query: string, role: string | null): boolean {
  if (role && !n.roles.includes(role)) return false;
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [
    n.name,
    n.version,
    n.os,
    n.arch,
    n.roles.join(" "),
    n.problems.join(" "),
    n.unschedulable ? "cordoned" : "",
  ].some((v) => v.toLowerCase().includes(q));
}

// --- Drain modal -----------------------------------------------------------------

// Drain output is bounded in practice, but cap the buffer so a chatty drain
// (many evicted pods) can never grow the DOM unbounded — mirrors LogsPane.
const DRAIN_BUF_CAP = 2_000;

interface LogChunkDTO {
  lines: string[];
  eof: boolean;
  error?: string;
  // A drain the operator cancelled: kubectl was killed part-way and the node
  // stays cordoned - neither a clean finish nor a failure (review 2026-10-03, H).
  cancelled?: boolean;
}

// cancelling: Cancel was pressed and the backend is killing kubectl; the modal
// stays open for the terminal "cancelled" report instead of vanishing.
type DrainStatus = "starting" | "running" | "cancelling" | "ended" | "cancelled" | "error";

function DrainModal({ cluster, node, uid, opts, onClose }: { cluster: string; node: string; uid: string; opts: DrainOptions; onClose: () => void }) {
  const [status, setStatus] = useState<DrainStatus>("starting");
  const [errorMsg, setErrorMsg] = useState<string | undefined>(undefined);
  const streamIdRef = useRef<string>("");
  const offRef = useRef<(() => void) | null>(null);
  const bufRef = useRef<string[]>([]);
  const droppedRef = useRef(false);
  const [version, setVersion] = useState(0);
  const scrollRef = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);

  useLayoutEffect(() => {
    if (follow && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [version, follow]);

  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const fromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    if (fromBottom > 80) setFollow(false);
  }, []);

  // Cancel pressed while the start RPC was still in flight: the drain is not
  // registered yet, so the cancel is re-sent once it is.
  const cancelRequestedRef = useRef(false);

  useEffect(() => {
    let stale = false;

    async function open() {
      // Subscribe FIRST, on an id we choose: kubectl output is emitted from the
      // moment the process starts, before startDrain returns (review 2026-10-03
      // #8). ended guards the status against a terminal chunk that beats it.
      const streamId = newStreamId("drain");
      let ended = false;
      const off = Events.On(`nodedrain:${streamId}`, (ev: { data: LogChunkDTO }) => {
        if (stale) return;
        const chunk = ev.data;
        if (chunk.lines && chunk.lines.length > 0) {
          const buf = [...bufRef.current, ...chunk.lines];
          if (buf.length > DRAIN_BUF_CAP) {
            buf.splice(0, buf.length - DRAIN_BUF_CAP);
            droppedRef.current = true;
          }
          bufRef.current = buf;
          setVersion((v) => v + 1);
        }
        if (chunk.eof) {
          ended = true;
          if (chunk.cancelled) {
            setStatus("cancelled");
            setErrorMsg(chunk.error);
          } else if (chunk.error) {
            setStatus("error");
            setErrorMsg(chunk.error);
          } else {
            setStatus("ended");
          }
          unsubscribe();
          streamIdRef.current = "";
        }
      });
      const unsubscribe = () => {
        if (typeof off === "function") off();
        if (offRef.current === unsubscribe) offRef.current = null;
      };
      offRef.current = unsubscribe;
      // Known before startDrain returns, so a cancel or unmount mid-start can
      // reach it.
      streamIdRef.current = streamId;

      const result = await startDrain(cluster, node, uid, opts, streamId);
      if (stale) {
        // Unmounted while the start was in flight: the cleanup's cancel ran
        // before the drain was registered (a no-op), so cancel now - never
        // leave a drain running with no UI (review 2026-10-03, G).
        unsubscribe();
        if (result.streamId) void cancelDrain(streamId);
        return;
      }
      if (result.error || !result.streamId) {
        unsubscribe();
        streamIdRef.current = "";
        setStatus("error");
        setErrorMsg(result.error || "failed to start drain");
        return;
      }
      if (cancelRequestedRef.current) {
        void cancelDrain(streamId);
        return;
      }
      if (!ended) setStatus("running");
    }

    void open();

    return () => {
      stale = true;
      if (offRef.current) { offRef.current(); offRef.current = null; }
      const sid = streamIdRef.current;
      if (sid) {
        streamIdRef.current = "";
        void cancelDrain(sid);
      }
    };
    // opts is composed once in the drain dialog and set with the node in one
    // state write, so its identity is stable for this modal's lifetime.
  }, [cluster, node, uid, opts]);

  // Cancel keeps the subscription: the terminal chunk reports the drain as
  // cancelled with the node still cordoned, and the operator closes the modal
  // after reading it (review 2026-10-03, H).
  const handleCancel = useCallback(() => {
    const sid = streamIdRef.current;
    if (!sid) {
      onClose();
      return;
    }
    cancelRequestedRef.current = true;
    setStatus("cancelling");
    void cancelDrain(sid);
  }, [onClose]);

  const handleKey = useCallback((e: KeyboardEvent) => {
    if (e.key === "Escape" && (status === "ended" || status === "cancelled" || status === "error")) onClose();
  }, [status, onClose]);
  useEffect(() => {
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [handleKey]);

  const lines = bufRef.current;

  return (
    <div
      onClick={(e) => { if (e.target === e.currentTarget && (status === "ended" || status === "cancelled" || status === "error")) onClose(); }}
      style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,0.5)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 1000 }}
    >
      <div
        style={{
          background: "var(--color-background-primary)",
          border: "0.5px solid var(--color-border-secondary)",
          borderRadius: "var(--border-radius-md)",
          padding: 20,
          width: 620,
          maxHeight: vh(80),
          display: "flex",
          flexDirection: "column",
          fontSize: 13,
        }}
      >
        {/* Header */}
        <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12 }}>
          <span style={{ fontWeight: 600, flex: 1 }}>Drain node: <span style={{ fontFamily: "var(--font-mono)", fontSize: 12 }}>{node}</span></span>
          <div style={{ display: "flex", alignItems: "center", gap: 5, fontSize: 11 }}>
            {status === "running" && (
              <>
                <StatusDot size={6} color="var(--color-text-success)" />
                <span style={{ color: "var(--color-text-success)" }}>draining</span>
              </>
            )}
            {status === "starting" && <span style={{ color: "var(--color-text-tertiary)" }}>starting…</span>}
            {status === "ended" && <span style={{ color: "var(--color-text-success)" }}>complete</span>}
            {status === "cancelling" && <span style={{ color: "var(--color-text-tertiary)" }}>cancelling…</span>}
            {status === "cancelled" && <span style={{ color: "var(--color-text-warning)" }}>cancelled</span>}
            {status === "error" && <span style={{ color: "var(--color-text-danger)" }} title={errorMsg}>failed: {errorMsg}</span>}
          </div>
        </div>

        {/* Log area */}
        <div
          ref={scrollRef}
          onScroll={onScroll}
          style={{
            flex: 1, minHeight: 0, maxHeight: 400,
            overflowY: "auto",
            background: "var(--color-background-secondary)",
            border: "0.5px solid var(--color-border-tertiary)",
            borderRadius: 4,
            padding: "6px 8px",
            fontFamily: "var(--font-mono)",
            fontSize: 11,
            lineHeight: 1.55,
            color: "var(--color-text-primary)",
            marginBottom: 12,
          }}
        >
          {lines.length === 0 && status === "starting" && (
            <span style={{ color: "var(--color-text-tertiary)" }}>starting…</span>
          )}
          {droppedRef.current && (
            <div style={{ color: "var(--color-text-tertiary)", fontStyle: "italic" }}>
              · older lines dropped (buffer capped at {DRAIN_BUF_CAP})
            </div>
          )}
          {lines.map((line, i) => (
            <div key={i} style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{line || "​"}</div>
          ))}
        </div>

        {status === "cancelled" && (
          <div data-testid="drain-cancelled" style={{ display: "flex", alignItems: "flex-start", gap: 6, fontSize: 11, color: "var(--color-text-warning)", marginBottom: 12 }}>
            <IconAlertTriangle size={13} stroke={1.5} style={{ flexShrink: 0, marginTop: 1 }} />
            <span>{errorMsg || `drain cancelled - ${node} stays cordoned`}</span>
          </div>
        )}

        {/* Footer buttons */}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
          {(status === "running" || status === "starting") && (
            <button onClick={handleCancel} style={{ ...btn, color: "var(--color-text-danger)", borderColor: "var(--color-text-danger)" }}>
              Cancel drain
            </button>
          )}
          {(status === "ended" || status === "cancelled" || status === "error") && (
            <button onClick={onClose} style={btn}>Close</button>
          )}
        </div>
      </div>
    </div>
  );
}

// --- Drain composer ----------------------------------------------------------------

const drainTimeouts = [60, 120, 300, 600];

// DrainComposerDialog assembles the kubectl drain flags per drain. The safe
// default only tolerates DaemonSets; destroying emptyDir data and forcing
// eviction of controller-less pods are explicit per-drain opt-ins
// (deleteEmptyDirData was silently always-on until 2026-07-03 - this dialog
// exists so that choice is made by the operator, every time).
function DrainComposerDialog({ cluster, node, isProtected, onCancel, onConfirm }: {
  cluster: string;
  node: string;
  isProtected: boolean;
  onCancel: () => void;
  onConfirm: (opts: DrainOptions) => void;
}) {
  const [ignoreDaemonSets, setIgnoreDaemonSets] = useState(true);
  const [deleteEmptyDirData, setDeleteEmptyDirData] = useState(false);
  const [force, setForce] = useState(false);
  const [timeoutSeconds, setTimeoutSeconds] = useState(120);

  // Honesty: the preview line is exactly the command the backend runs.
  const preview = [
    "kubectl drain",
    node,
    ...(ignoreDaemonSets ? ["--ignore-daemonsets"] : []),
    ...(deleteEmptyDirData ? ["--delete-emptydir-data"] : []),
    ...(force ? ["--force"] : []),
    `--timeout=${timeoutSeconds}s`,
  ].join(" ");

  return (
    <OperationDialog
      title={`Drain node "${node}"`}
      ariaLabel={`Drain node ${node}`}
      subtitle="Evicts every pod from the node and cordons it (unschedulable) until you uncordon it."
      cluster={cluster}
      protected={isProtected}
      confirmLabel="Drain"
      danger
      onCancel={onCancel}
      onConfirm={() => onConfirm({ ignoreDaemonSets, deleteEmptyDirData, force, timeoutSeconds })}
    >
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <MetaTile label="node" value={node} />
        <MetaTile label="cluster" value={cluster} />
      </div>

      <div style={{ display: "grid", gap: 8 }}>
        <div style={composerLabel}>options</div>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 10 }}>
          <OptionTile
            active={ignoreDaemonSets}
            title="Tolerate DaemonSets"
            body="DaemonSet pods can't be evicted and respawn anyway; without this, drain fails on any node running DaemonSets."
            onClick={() => setIgnoreDaemonSets((v) => !v)}
          />
          <OptionTile
            active={deleteEmptyDirData}
            title="Delete emptyDir data"
            body="Pods' local emptyDir scratch data is DESTROYED. Without this, drain refuses to evict pods that use emptyDir."
            onClick={() => setDeleteEmptyDirData((v) => !v)}
          />
          <OptionTile
            active={force}
            title="Force"
            body="Evict pods with no controller; they will NOT be recreated."
            onClick={() => setForce((v) => !v)}
          />
        </div>
      </div>

      <label style={{ display: "flex", alignItems: "center", gap: 10 }}>
        <span style={composerLabel}>timeout</span>
        <select
          aria-label="drain timeout"
          value={timeoutSeconds}
          onChange={(e) => setTimeoutSeconds(Number(e.target.value))}
          style={composerSelect}
        >
          {drainTimeouts.map((t) => <option key={t} value={t}>{t}s</option>)}
        </select>
      </label>

      <div data-testid="drain-command-preview" style={commandPreview}>{preview}</div>
    </OperationDialog>
  );
}

const composerLabel: React.CSSProperties = { color: "var(--color-text-tertiary)", fontSize: 10, textTransform: "uppercase", fontWeight: 600, letterSpacing: 0.2 };
const composerSelect: React.CSSProperties = { padding: "4px 8px", fontSize: 12, fontFamily: "var(--font-mono)", background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" };
const commandPreview: React.CSSProperties = { fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)", background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, padding: "7px 10px", whiteSpace: "pre-wrap", wordBreak: "break-all" };

// --- Main view -------------------------------------------------------------------

// Node columns, most important first after identity. fitColumns keeps the
// node name at its 240px floor and adds the rest by priority while they fit;
// the usage columns are dropped entirely when no metrics source exists
// (an unavailable measurement never takes width from the name).
const NODE_COLUMNS: ColumnDef[] = [
  { id: "lamp", width: 16, priority: 0 },
  { id: "name", width: 240, priority: 0, flex: true },
  { id: "status", width: 140, priority: 1 },
  { id: "cpuUsed", width: 104, priority: 2 },
  { id: "memUsed", width: 120, priority: 2 },
  { id: "roles", width: 110, priority: 3 },
  { id: "version", width: 84, priority: 4 },
  { id: "taints", width: 52, priority: 5 },
  { id: "cpuAlloc", width: 104, priority: 6 },
  { id: "memAlloc", width: 120, priority: 6 },
  { id: "age", width: 44, priority: 7 },
];
const COLUMN_LABEL: Record<string, string> = {
  lamp: "", name: "Node", status: "Status", cpuUsed: "CPU used", memUsed: "Memory used", roles: "Roles",
  version: "Version", taints: "Taints", cpuAlloc: "CPU alloc / cap", memAlloc: "Mem alloc / cap", age: "Age",
};

// A verb's target: the cluster + node OBJECT (its UID) it was armed against.
type NodeVerb = { cluster: string; node: string; uid: string };

// The age ticks from the creation time (1c slice 3 §3); ageSeconds is the
// age at the capture.
const ageOf = (n: NodeSummaryDTO, nowS: number) => (n.createdUnix ? Math.max(0, nowS - n.createdUnix) : n.ageSeconds);

// What a selected row's detail is re-read on: its identity and the facts the
// detail explains. A heartbeat alone re-reads nothing.
const detailSig = (cluster: string, n: NodeSummaryDTO) =>
  [cluster, n.name, n.uid ?? "", n.generation ?? 0, kindOf(n.health), n.health?.label ?? "", n.unschedulable, n.taintCount].join("|");

export function NodesView({ cluster }: { cluster: string }) {
  const nodes = useFleet((s) => s.nodes);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const nowS = Math.floor(useNow() / 1000);

  // Verbs are pinned to the cluster + node OBJECT (its UID, design §5b) they
  // were raised on, independent of selection: dropped on a cluster change,
  // when a same-name replacement takes the row, or when the node leaves a
  // complete read, and re-checked at dispatch. The backend writes - or
  // drains - only that UID. drainPending holds the composer; confirming moves
  // the target + composed options into drainActive, which mounts the modal.
  const [drainActive, setDrainActive] = useState<(NodeVerb & { opts: DrainOptions }) | null>(null);
  const [cordonPending, setCordonPending] = useState<(NodeVerb & { cordon: boolean }) | null>(null);
  const [drainPending, setDrainPending] = useState<NodeVerb | null>(null);
  const [filter, setFilter] = useState<NodeFilter>("all");
  const [roleFilter, setRoleFilter] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [simulating, setSimulating] = useState(false);
  const [bodyWidth, setBodyWidth] = useState(1100);

  // The nodes are the Observer's shared rows (1c slice 3 §5.7) - live, not a
  // snapshot. Their evidence is the Node kind's own coverage.
  useNodeRows(cluster);
  const mine = nodes.cluster === cluster;
  const ev = kindEvidence(mine && !nodes.loading, nodes.coverage, "Node", nodes.error);
  const error = mine ? nodes.error ?? null : null;
  const rowsRefreshing = mine && !!nodes.rowsState?.refreshing;
  const rowsAsOf = mine && nodes.rowsState?.evaluatedAt ? clock(nodes.rowsState.evaluatedAt) : "the last read";
  const asOf = rowsRefreshing ? ` (as of ${rowsAsOf})` : "";
  const lastGood = mine ? lastGoodAt(nodes.coverage, ["Node"]) : null;
  const discoveryGap = mine ? coverageGaps(nodes.coverage ?? []) : null;
  const mark = ev === "complete" ? "" : ev === "stale" ? "*" : "+";

  // Live per-node usage (pod-attributed), joined by node name at render.
  const [util, setUtil] = useState<NodeUtilizationResultDTO | null>(null);
  useEffect(() => {
    let live = true;
    setUtil(null);
    void getNodeUtilization(cluster).then((r) => { if (live) setUtil(r); });
    return () => { live = false; };
  }, [cluster]);
  const usageByNode = useMemo(() => {
    const m = new Map<string, { cpu: number | null; mem: number | null }>();
    for (const it of util?.items ?? []) m.set(it.node, { cpu: it.cpu, mem: it.mem });
    return m;
  }, [util]);
  const refreshUsage = () => { void getNodeUtilization(cluster).then((r) => { if (useFleet.getState().nodes.cluster === cluster) setUtil(r); }); };

  // Only rows read for THIS cluster are shown or counted.
  const scopedNodes = useMemo(() => (mine ? nodes.items : []), [nodes.items, mine]);
  const receivedAt = useReceivedAt(scopedNodes.length > 0 ? scopedNodes : null);
  const orderedNodes = useMemo(
    () => [...scopedNodes].sort((a, b) => orderOf(kindOf(a.health)) - orderOf(kindOf(b.health)) || a.name.localeCompare(b.name)),
    [scopedNodes],
  );
  const visibleNodes = useMemo(
    () => orderedNodes.filter((n) => nodeMatchesFilter(n, filter) && nodeMatchesQuery(n, query, roleFilter)),
    [orderedNodes, filter, query, roleFilter],
  );
  // global: the count selector's verdict counts (slice 3 §4a) - the rows'
  // own, or the summary's past the A4 deadline; row-scoped: cordoned and
  // tainted, the filter chips and the footer ("as of" while refreshing).
  const globalCounts = mine ? nodes.counts : null;
  const rowCounts = summarize(scopedNodes, (n) => n.health);
  const counts: VerdictCounts = globalCounts ? { ...globalCounts, attention: globalCounts.fault + globalCounts.degraded } : rowCounts;
  const cordonedCount = scopedNodes.filter((n) => n.unschedulable).length;
  const taintedCount = scopedNodes.filter((n) => n.taintCount > 0).length;
  const roles = useMemo(() => [...new Set(scopedNodes.flatMap((n) => n.roles))].sort(), [scopedNodes]);
  const cpuAlloc = scopedNodes.reduce((sum, n) => sum + n.cpuAllocatable, 0);
  const memAlloc = scopedNodes.reduce((sum, n) => sum + n.memAllocatable, 0);
  const podCap = scopedNodes.reduce((sum, n) => sum + n.podCapacity, 0);
  // "none need attention" is a claim about the present: a complete, current read
  const current = ev === "complete" && !nodes.rowsState?.updating && !rowsRefreshing;

  // Selection follows the node NAME; its detail and verbs follow the UID. A
  // node missing from the rows keeps its last row: removed only on a complete
  // read of the Node kind, otherwise unverified (slice 3 §5a).
  const sel = mine ? nodes.selected : null;
  const live = sel ? scopedNodes.find((n) => n.name === sel.name) : undefined;
  const lastRow = useRef<{ cluster: string; n: NodeSummaryDTO } | null>(null);
  if (live) lastRow.current = { cluster, n: live };
  const prior = sel && lastRow.current?.cluster === cluster && lastRow.current.n.name === sel.name ? lastRow.current.n : null;
  const subject = live ?? prior;
  const gone: "removed" | "unverified" | null = sel && !live ? (ev === "complete" ? "removed" : "unverified") : null;

  // Opening a row reads its detail; the row's later changes re-read it, and a
  // same-name replacement is read afresh - the old object's detail never
  // becomes the new one's (design §5b).
  const readSig = useRef<string | null>(null);
  const openRow = useCallback((n: NodeSummaryDTO) => {
    readSig.current = detailSig(cluster, n);
    void openNodeDetail(cluster, n.name, n.uid ?? "");
  }, [cluster]);
  const liveSig = live ? detailSig(cluster, live) : null;
  useEffect(() => {
    if (!sel || !live || !liveSig || readSig.current === liveSig) return;
    readSig.current = liveSig;
    void openNodeDetail(cluster, live.name, live.uid ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, sel, liveSig]);

  // Keyboard: j/k cursor (key-stable), Enter opens, Esc closes, / searches.
  const [kbName, setKbName] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const kbIdx = kbName === null ? -1 : visibleNodes.findIndex((n) => n.name === kbName);
  const closeInspector = useCallback(() => {
    autoSelect.current = "done";
    useFleet.getState().selectNode(null);
  }, []);
  const activate = (n: NodeSummaryDTO) => { autoSelect.current = "done"; setKbName(n.name); openRow(n); };
  useListKeys({
    count: visibleNodes.length,
    selected: kbIdx,
    onSelect: (idx) => { const n = visibleNodes[idx]; if (n) setKbName(n.name); },
    onActivate: (idx) => { const n = visibleNodes[idx]; if (n) activate(n); },
    onEscape: () => { if (useFleet.getState().nodes.selected) closeInspector(); },
    searchRef,
  });

  useNavIntent(cluster, ["drain-node", "cordon-node", "select-node"], (intent) => {
    if (scopedNodes.length === 0) return ev === "complete" ? "missing" : "wait";
    const name = "node" in intent ? intent.node : "";
    const n = scopedNodes.find((x) => x.name === name);
    if (!n) return "missing";
    const target = { cluster, node: n.name, uid: n.uid ?? "" };
    if (intent.kind === "drain-node") setDrainPending(target);
    else if (intent.kind === "cordon-node") setCordonPending({ ...target, cordon: intent.cordon });
    else activate(n);
    return "consumed";
  }, [scopedNodes, ev]);

  // One-shot auto-select of the first attention node from a FRESH complete
  // read after route entry (same contract as Pods).
  const autoSelect = useRef<"pending" | "done">("pending");
  const itemsAtEntry = useRef<NodeSummaryDTO[] | null>(null);
  if (itemsAtEntry.current === null) itemsAtEntry.current = nodes.items;
  useEffect(() => { autoSelect.current = "pending"; itemsAtEntry.current = useFleet.getState().nodes.items; }, [cluster]);
  const freshRead = mine && ev === "complete" && nodes.items !== itemsAtEntry.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead) return;
    autoSelect.current = "done";
    if (useFleet.getState().nodes.selected) return;
    const n = visibleNodes.find((x) => isAttention(kindOf(x.health)));
    if (!n) return;
    setKbName(n.name);
    openRow(n);
  }, [freshRead, visibleNodes, openRow]);

  // Operator filter edits win: one that hides the selection closes it. A
  // read change never does - the selection keeps its last evidence.
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (live && !visibleNodes.includes(live)) useFleet.getState().selectNode(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filter, query, roleFilter]);

  // A pending verb whose target is gone (another cluster, a same-name
  // replacement, or absent from a complete read) must not stay one click
  // from dispatch; dispatch re-checks against the current rows.
  const targetLive = useCallback((p: NodeVerb | null) => {
    if (!p || p.cluster !== cluster) return false;
    const cur = scopedNodes.find((x) => x.name === p.node);
    if (cur) return cur.uid === p.uid;
    return ev !== "complete";
  }, [cluster, ev, scopedNodes]);
  useEffect(() => {
    setCordonPending((p) => (p && !targetLive(p) ? null : p));
    setDrainPending((p) => (p && !targetLive(p) ? null : p));
  }, [targetLive]);
  const dispatchArmed = (p: NodeVerb, send: () => void) => {
    if (!targetLive(p)) {
      useFleet.getState().setActionStatus({ kind: "error", message: `node ${p.node} was replaced or removed since the action was armed - nothing was sent` });
      return;
    }
    send();
  };

  const usageAvailable = util?.available !== false;
  const fitted = useMemo(
    () => fitColumns(NODE_COLUMNS.filter((c) => usageAvailable || (c.id !== "cpuUsed" && c.id !== "memUsed")), bodyWidth),
    [bodyWidth, usageAvailable],
  );
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  let state: DatasetState = "ready";
  if (ev === "failed" && scopedNodes.length === 0) state = "unreadable";
  else if (ev === "pending" && scopedNodes.length === 0) state = "loading";
  else if (scopedNodes.length === 0) state = "empty";
  else if (visibleNodes.length === 0) state = "no-match";
  const clearFilters = () => { setFilter("all"); setRoleFilter(null); setQuery(""); };
  const icon = <IconServer size={28} stroke={1.2} />;
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={6} label="loading nodes" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="Nodes could not be read." hint={error ?? undefined} />
    : state === "empty" && rowsRefreshing && counts.total > 0 ? <EmptyState icon={icon} title={`No nodes as of ${rowsAsOf}.`} hint={`Refreshing - the cluster summary now counts ${counts.total}.`} />
    : state === "empty" && ev === "stale" ? <EmptyState icon={icon} title="No nodes in the last good read." hint={`The read is failing now${lastGood ? ` - last good ${clock(lastGood)}` : ""}: ${error ?? "unverified"}.`} />
    : state === "empty" ? <EmptyState icon={icon} title="The read returned no nodes." hint="RBAC may hide nodes from this identity." />
    : <EmptyState icon={icon} title="No nodes match the current filters." hint={`${scopedNodes.length} node${scopedNodes.length === 1 ? "" : "s"} in this cluster${asOf}.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  // Notices stack: each is its own evidence, none hides another.
  const notices: React.ReactNode[] = [];
  if (error && scopedNodes.length > 0) notices.push(<WatchErrorBanner key="read" tool="Nodes" failure={`read failed - showing the last good read${lastGood ? ` from ${clock(lastGood)}` : ""}`} error={error} />);
  if (rowsRefreshing) notices.push(<div key="a4" role="status" data-testid="rows-refreshing" className="t-secondary" style={statusLine}>Rows as of {rowsAsOf} - refreshing; the totals are the cluster summary's.{nodes.rowsState?.refreshError ? ` Last re-read failed: ${nodes.rowsState.refreshError}` : ""}</div>);
  if (discoveryGap) notices.push(<div key="gap" role="status" data-testid="discovery-gap" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Discovery re-check failing - {discoveryGap}. A capability gap only; the rows keep their own state.</div>);
  if (util && !util.available) notices.push(<div key="util" data-testid="node-util-unavailable" className="t-secondary" style={statusLine}>Live usage unavailable - {util.message || "no metrics source"}. Usage columns are hidden.</div>);
  if (util && util.available && util.message) notices.push(<div key="caveat" role="status" className="t-secondary" style={{ ...statusLine, color: "var(--color-text-warning)" }}>Usage caveat: {util.message}</div>);
  if (simulating) notices.push(<SurvivabilityModule key="sim" cluster={cluster} nodes={scopedNodes} />);

  const tone = attentionTone(counts);
  const filterLabel = filter !== "all" ? filter[0].toUpperCase() + filter.slice(1) : roleFilter ? `role ${roleFilter}` : query ? "search" : "";

  return (
    <div style={{ height: "100%", minHeight: 0, position: "relative" }}>
      <LensLayout
        label="Nodes"
        title="Nodes"
        summary={state === "loading" || state === "unreadable" ? null : (
          <>
            <span className="t-number" data-testid="nodes-total" style={{ color: "var(--color-text-secondary)" }}>{counts.total}{mark}</span>
            {counts.attention > 0 ? (
              <span className="t-status" data-testid="nodes-attention" style={{ color: tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
                {tone === "danger" ? "▲ " : ""}{counts.attention}{mark} need attention
              </span>
            ) : <span className="t-secondary">{counts.unknown > 0 ? "no known attention" : current ? "none need attention" : "none known to need attention"}</span>}
            <span className="t-secondary">
              {[counts.fault ? `${counts.fault}${mark} not ready` : "", counts.degraded ? `${counts.degraded}${mark} under pressure` : "", counts.unknown ? `${counts.unknown}${mark} unverified` : "", cordonedCount ? `${cordonedCount}${mark} cordoned${asOf}` : ""].filter(Boolean).join(" · ")}
            </span>
          </>
        )}
        headerEnd={
          <>
            <UpdatedAt at={receivedAt} />
            <button className="l-btn" aria-pressed={simulating} onClick={() => setSimulating(!simulating)} title="Does this cluster survive losing a node, zone or pool?">Simulate loss</button>
            <button className="l-btn" onClick={refreshUsage} title="The nodes are live; this re-reads the usage columns">Refresh usage</button>
          </>
        }
        controls={
          <>
            <button className="l-chip" aria-pressed={filter === "all"} onClick={() => setFilter("all")}>All <span style={{ color: "var(--color-text-tertiary)" }}>{scopedNodes.length || ""}</span></button>
            <button className="l-chip" aria-pressed={filter === "attention"} onClick={() => setFilter("attention")}>
              Attention {rowCounts.attention > 0 && <span style={{ color: tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{rowCounts.attention}</span>}
            </button>
            {(rowCounts.unknown > 0 || filter === "unknown") && (
              <button className="l-chip" aria-pressed={filter === "unknown"} onClick={() => setFilter("unknown")}>Unknown <span style={{ color: "var(--color-text-tertiary)" }}>{rowCounts.unknown || ""}</span></button>
            )}
            <button className="l-chip" aria-pressed={filter === "cordoned"} onClick={() => setFilter("cordoned")}>Cordoned <span style={{ color: "var(--color-text-tertiary)" }}>{cordonedCount || ""}</span></button>
            <button className="l-chip" aria-pressed={filter === "tainted"} onClick={() => setFilter("tainted")}>Tainted <span style={{ color: "var(--color-text-tertiary)" }}>{taintedCount || ""}</span></button>
            {roles.length > 0 && (
              <label style={{ display: "flex" }}>
                <span className="sr-only">Role</span>
                <select className="l-input" value={roleFilter ?? ""} onChange={(e) => setRoleFilter(e.target.value || null)}>
                  <option value="">All roles</option>
                  {roles.map((r) => <option key={r} value={r}>{r}</option>)}
                </select>
              </label>
            )}
            <label style={{ display: "flex", flex: "0 1 280px", minWidth: 160 }}>
              <span className="sr-only">Search nodes</span>
              <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={query} placeholder="Search nodes" onChange={(e) => setQuery(e.target.value)} />
            </label>
          </>
        }
        notice={notices.length > 0 ? <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{notices}</div> : undefined}
        state={state}
        stateView={stateView}
        body={
          <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
            <div className="lens-table-head" role="row" style={gridStyle}>
              {NODE_COLUMNS.filter((c) => show(c.id)).map((c) => (
                <span key={c.id} title={c.id === "cpuUsed" || c.id === "memUsed" ? "pod usage vs allocatable (5m rate); node OS/kubelet overhead not included" : undefined}>{COLUMN_LABEL[c.id]}</span>
              ))}
            </div>
            <div data-testid="nodes-list-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {visibleNodes.map((n) => {
                const usage = usageByNode.get(n.name);
                return (
                  <div
                    key={n.uid || n.name}
                    role="button"
                    tabIndex={0}
                    data-rowhost="true"
                    className="lens-row"
                    style={gridStyle}
                    aria-selected={kbName === n.name}
                    data-cursor={kbName === n.name ? "true" : undefined}
                    data-selected={sel?.name === n.name ? "true" : undefined}
                    ref={kbName === n.name ? (el) => el?.scrollIntoView?.({ block: "nearest" }) : undefined}
                    onClick={() => activate(n)}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); activate(n); } }}
                  >
                    {show("lamp") && <VerdictLamp health={n.health} size={8} />}
                    {show("name") && (
                      <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
                        <span className="lens-cell-name" title={n.name} style={{ fontWeight: sel?.name === n.name ? 600 : 400 }}>{n.name}</span>
                        <CopyButton text={n.name} label={`copy node name ${n.name}`} appearOnRowHover />
                      </span>
                    )}
                    {show("status") && <span style={ellipsis}><VerdictLabel health={n.health} />{n.unschedulable && kindOf(n.health) !== "paused" ? <span className="t-secondary"> · cordoned</span> : null}</span>}
                    {show("cpuUsed") && <UsageCell resource="cpu" usage={usage?.cpu ?? null} allocatable={n.cpuAllocatable} format={fmtCpu} />}
                    {show("memUsed") && <UsageCell resource="mem" usage={usage?.mem ?? null} allocatable={n.memAllocatable} format={fmtMem} />}
                    {show("roles") && <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }}>{n.roles.length > 0 ? n.roles.join(", ") : "-"}</span>}
                    {show("version") && <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }}>{n.version}</span>}
                    {show("taints") && <span style={{ color: n.taintCount > 0 ? "var(--color-text-secondary)" : "var(--color-text-tertiary)" }}>{n.taintCount > 0 ? n.taintCount : "-"}</span>}
                    {show("cpuAlloc") && <CapacityCell value={n.cpuAllocatable} total={n.cpuCapacity} format={(v) => v.toFixed(1)} />}
                    {show("memAlloc") && <CapacityCell value={n.memAllocatable} total={n.memCapacity} format={fmtMem} />}
                    {show("age") && <span style={{ color: "var(--color-text-tertiary)" }}>{formatAge(ageOf(n, nowS))}</span>}
                  </div>
                );
              })}
            </div>
          </LensTable>
        }
        footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
        footerEnd={
          <>
            <span className="t-secondary">
              {visibleNodes.length} of {scopedNodes.length}{mark} nodes{filterLabel ? ` · filter: ${filterLabel}` : ""}{asOf} · allocatable {cpuAlloc.toFixed(1)} CPU · {fmtMem(memAlloc)} · {podCap} pod slots
            </span>
            {filterLabel && <button className="l-chip" style={{ height: 20, color: "var(--color-text-info)" }} onClick={clearFilters}>Show all {scopedNodes.length} nodes</button>}
          </>
        }
        inspector={sel && subject ? (
          <NodeInspector
            key={`${cluster}/${sel.name}`}
            cluster={cluster}
            row={subject}
            uid={subject.uid ?? sel.uid}
            nowS={nowS}
            detail={nodes.detail}
            loading={nodes.detailLoading}
            gone={gone}
            onCordon={(cordon) => setCordonPending({ cluster, node: subject.name, uid: subject.uid ?? "", cordon })}
            onDrain={() => setDrainPending({ cluster, node: subject.name, uid: subject.uid ?? "" })}
            onClose={closeInspector}
          />
        ) : undefined}
        inspectorLabel="Node details"
        onCloseInspector={closeInspector}
      />

      {cordonPending && targetLive(cordonPending) && (
        <ConfirmDialog
          title={cordonPending.cordon ? "Cordon node" : "Uncordon node"}
          cluster={cordonPending.cluster}
          detail={`node ${cordonPending.node} will be marked ${cordonPending.cordon ? "unschedulable" : "schedulable"}`}
          protected={isProtected}
          danger={cordonPending.cordon}
          confirmLabel={cordonPending.cordon ? "Cordon" : "Uncordon"}
          onConfirm={() => {
            const p = cordonPending;
            setCordonPending(null);
            dispatchArmed(p, () => void cordonNode(p.cluster, p.node, p.uid, p.cordon));
          }}
          onCancel={() => setCordonPending(null)}
        />
      )}

      {drainPending && targetLive(drainPending) && (
        <DrainComposerDialog
          cluster={drainPending.cluster}
          node={drainPending.node}
          isProtected={isProtected}
          onConfirm={(opts) => {
            const p = drainPending;
            setDrainPending(null);
            dispatchArmed(p, () => setDrainActive({ ...p, opts }));
          }}
          onCancel={() => setDrainPending(null)}
        />
      )}

      {drainActive && (
        <DrainModal
          cluster={drainActive.cluster}
          node={drainActive.node}
          uid={drainActive.uid}
          opts={drainActive.opts}
          onClose={() => setDrainActive(null)}
        />
      )}
    </div>
  );
}

const statusLine: React.CSSProperties = { minHeight: 24, display: "flex", alignItems: "center", padding: "0 10px" };

// SurvivabilityModule: "does this cluster survive losing X?" - a requests-
// based bin-pack simulation (backend workloads.SimulateLoss). Targets are
// derived from the LOADED nodes (zones + pools + single nodes); the verdict
// states its caveats: requests-based upper bound, affinity/taints not
// modeled, DaemonSet pods excluded, PDB blockers listed separately.
function SurvivabilityModule({ cluster, nodes }: { cluster: string; nodes: NodeSummaryDTO[] }) {
  // Autoscaler honesty: with Karpenter present, lost capacity would be
  // re-provisioned - the fixed-capacity verdict is a FLOOR, not the outcome.
  const hasKarpenter = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.karpenterPresent ?? false);
  const targets = useMemo(() => {
    const zones = [...new Set(nodes.map((n) => n.zone ?? "").filter(Boolean))].sort();
    const pools = [...new Set(nodes.map((n) => n.pool ?? "").filter(Boolean))].sort();
    const out: Array<{ kind: string; name: string; label: string }> = [];
    for (const z of zones) out.push({ kind: "zone", name: z, label: `zone ${z}` });
    for (const p of pools) out.push({ kind: "pool", name: p, label: `pool ${p}` });
    for (const n of nodes) out.push({ kind: "node", name: n.name, label: `node ${n.name}` });
    return out;
  }, [nodes]);
  const [sel, setSel] = useState(0);
  const [report, setReport] = useState<HeadroomReportDTO | null>(null);
  const [busy, setBusy] = useState(false);

  if (targets.length === 0) return null;
  const target = targets[Math.min(sel, targets.length - 1)];

  const run = async () => {
    setBusy(true);
    try {
      setReport(await getNodeHeadroom(cluster, target.kind, target.name));
    } finally {
      setBusy(false);
    }
  };

  const r = report?.result;
  const fits = r ? r.unplaced.length === 0 : false;
  return (
    <div data-testid="survivability" style={{ display: "flex", flexDirection: "column", gap: 6, flexShrink: 0, padding: "8px 10px", background: "var(--bench-well-bg, var(--color-background-secondary))", borderRadius: "var(--radius-sm)", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
        <span style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>survivability</span>
        <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>if we lose</span>
        <select value={sel} onChange={(e) => { setSel(Number(e.target.value)); setReport(null); }} aria-label="loss target" style={{ fontSize: 11, fontFamily: "var(--font-mono)", padding: "2px 6px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)" }}>
          {targets.map((t, i) => <option key={`${t.kind}/${t.name}`} value={i}>{t.label}</option>)}
        </select>
        <button onClick={() => void run()} disabled={busy} style={{ ...btn, fontSize: 10 }}>{busy ? "packing…" : "simulate"}</button>
        {report && !report.error && r && (
          <span data-testid="survivability-verdict" style={{ fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: 600, color: fits ? "var(--color-text-success)" : "var(--color-text-danger)" }}>
            {fits
              ? `fits — ${r.displaced} displaced pod${r.displaced === 1 ? "" : "s"} reschedule onto ${r.survivorNodes} node${r.survivorNodes === 1 ? "" : "s"}`
              : `${r.unplaced.length} pod${r.unplaced.length === 1 ? "" : "s"} won't fit (short ${fmtCpu(r.cpuShortfall)} cpu / ${fmtMem(r.memShortfall)})`}
          </span>
        )}
        {report?.error && <span style={{ fontSize: 11, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>{report.error}</span>}
      </div>
      {report && !report.error && r && (
        <>
          {r.unplaced.length > 0 && (
            <div style={{ display: "flex", flexWrap: "wrap", gap: 6, fontFamily: "var(--font-mono)", fontSize: 10 }}>
              {r.unplaced.slice(0, 6).map((p) => (
                <span key={`${p.namespace}/${p.name}`} style={{ color: "var(--color-text-danger)" }} title={`requests ${fmtCpu(p.cpuReq)} cpu / ${fmtMem(p.memReq)}`}>
                  {p.namespace}/{p.name}
                </span>
              ))}
              {r.unplaced.length > 6 && <span style={{ color: "var(--color-text-tertiary)" }}>+{r.unplaced.length - 6} more</span>}
            </div>
          )}
          {report.pdbBlockers.length > 0 && (
            <div style={{ fontSize: 10, color: "var(--color-text-warning)" }}>
              drain blockers: {report.pdbBlockers.slice(0, 4).map((b) => `${b.namespace}/${b.name}`).join(", ")}
              {report.pdbBlockers.length > 4 ? ` +${report.pdbBlockers.length - 4} more` : ""} — PDBs at 0 allowed disruptions stall the drain even when capacity fits
            </div>
          )}
          <div style={{ fontSize: 9, color: "var(--color-text-tertiary)" }}>
            requests-based upper bound — affinity, taints, and topology spread not modeled; DaemonSet pods excluded
            {hasKarpenter ? " · Karpenter present: lost capacity would be re-provisioned, this verdict is a floor" : ""}
            {report.podListCapped ? " · pod list capped: the verdict may be optimistic" : ""}
          </div>
        </>
      )}
    </div>
  );
}

// UsageCell renders live pod-attributed usage against allocatable with the
// shared saturation tiers (Signal Bench: only a hot node glows). No sample
// renders "—", never a zero bar.
function UsageCell({ resource, usage, allocatable, format }: {
  resource: "cpu" | "mem";
  usage: number | null;
  allocatable: number;
  format: (v: number) => string;
}) {
  if (usage == null) {
    return <span style={{ color: "var(--color-text-tertiary)" }}>—</span>;
  }
  const sat = saturation(resource, usage, allocatable > 0 ? allocatable : null);
  const pct = sat.pct == null ? 0 : Math.max(0, Math.min(100, sat.pct * 100));
  const barColor = sat.tier === "danger" ? "var(--color-text-danger)" : sat.tier === "warn" ? "var(--color-text-warning)" : "var(--color-text-success)";
  const textColor = sat.tier === "danger" ? "var(--color-text-danger)" : sat.tier === "warn" ? "var(--color-text-warning)" : "var(--color-text-secondary)";
  return (
    <span style={{ color: textColor, display: "inline-flex", flexDirection: "column", gap: 2, minWidth: 0 }}>
      <span style={{ ...ellipsis }} title={`${format(usage)} of ${format(allocatable)} allocatable`}>
        {format(usage)}
        <span style={{ color: "var(--color-text-tertiary)" }}> ({sat.pct == null ? "?" : Math.round(sat.pct * 100)}%)</span>
      </span>
      <span style={{ height: 4, background: "var(--color-background-secondary)", borderRadius: 2, overflow: "hidden" }}>
        <span style={{ display: "block", height: "100%", width: `${pct}%`, background: barColor }} />
      </span>
    </span>
  );
}

function CapacityCell({ value, total, format }: { value: number; total: number; format: (v: number) => string }) {
  const pct = total > 0 ? Math.max(0, Math.min(100, (value / total) * 100)) : 0;
  return (
    <span style={{ color: "var(--color-text-secondary)", display: "inline-flex", flexDirection: "column", gap: 2, minWidth: 0 }}>
      <span style={{ ...ellipsis }}>
        {format(value)}
        <span style={{ color: "var(--color-text-tertiary)" }}> / {format(total)}</span>
      </span>
      {/* Allocatable/capacity is a fact about the node (system reservations),
          not pressure: neutral, never a warning colour. Real pressure lives in
          the "used" columns and the verdict. */}
      <span style={{ height: 4, background: "var(--color-background-secondary)", borderRadius: 2, overflow: "hidden" }}>
        <span style={{ display: "block", height: "100%", width: `${pct}%`, background: "var(--color-text-tertiary)" }} />
      </span>
    </span>
  );
}

// NodeInspector - investigation order: verdict, actions (pods on the node
// first, cordon/drain safeguarded), failing conditions, pods, events, capacity,
// then taints and collapsed metadata. The row (live, or the selection's last)
// is the verdict and the facts; the detail is shown only for the row's UID
// (design §5b) - a same-name replacement's detail never becomes this one's.
function NodeInspector({ cluster, row, uid, nowS, detail, loading, gone, onCordon, onDrain, onClose }: {
  cluster: string;
  row: NodeSummaryDTO;
  uid: string;
  nowS: number;
  detail: NodeDetailDTO | null;
  loading: boolean;
  gone: "removed" | "unverified" | null;
  onCordon: (cordon: boolean) => void;
  onDrain: () => void;
  onClose: () => void;
}) {
  const [tab, setTab] = useState<"overview" | "manifest">("overview");
  const [describing, setDescribing] = useState(false);
  const podsRef = useRef<HTMLDivElement>(null);
  const name = row.name;
  const live = gone !== "removed";
  const manifestRef = wellKnownResourceRef("", "Node");

  const detailError = detail?.error || null;
  const forObject = detail && !detail.error && detail.summary.uid === uid ? detail : null;
  const detailOfOther = !!detail && !detail.error && !forObject;
  // the last detail read for this object, by UID: when the node leaves the
  // read or a re-read fails it is the object's last evidence, said as such -
  // never a recreation's
  const lastDetail = useRef<{ uid: string; d: NodeDetailDTO } | null>(null);
  if (forObject) lastDetail.current = { uid, d: forObject };
  const shown = forObject ?? (lastDetail.current?.uid === uid ? lastDetail.current.d : null);
  const lastEvidence = !!gone || !!detailError;
  const readAt = shown?.asOfUnix ? new Date(shown.asOfUnix * 1000).toLocaleTimeString() : null;

  const openPod = (pod: PodOnNodeDTO) => {
    useFleet.getState().setSection("pods");
    useFleet.getState().requestNavIntent({ kind: "select-pod", cluster, namespace: pod.namespace, name: pod.name });
  };

  const more: MenuItem[] = live ? [
    { label: `Drain ${name}…`, danger: true, onSelect: onDrain },
    { label: "Describe", onSelect: () => setDescribing(true) },
    ...(manifestRef ? [{ label: "Edit manifest in Resources", onSelect: () => { const nav = useFleet.getState(); nav.openResource(manifestRef); nav.openInstance("", name); } }] : []),
  ] : [];

  const problems = shown?.conditions.filter(isAdverse) ?? [];
  const unverifiedConds = shown?.conditions.filter((c) => conditionTone(c) === "unknown") ?? [];

  return (
    <div data-testid="node-detail-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout
        kind="Node"
        name={name}
        meta={[row.roles.join(", "), row.version, `${formatAge(ageOf(row, nowS))} old`, row.pool ? `pool ${row.pool}` : "", row.zone ? `zone ${row.zone}` : ""].filter(Boolean).join(" · ")}
        onClose={onClose}
        verdict={gone === "removed" ? (
          <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
            <span className="t-status">Node removed.</span> It is no longer in the current read. The details below are its last evidence; actions are disabled.
          </div>
        ) : gone === "unverified" ? (
          <div role="status" data-testid="selection-unverified" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            <span className="t-status">Not in the current Node read.</span> That read is incomplete, so this is unverified - it may still exist. The details below are its last observed row.
          </div>
        ) : <VerdictStatus health={row.health} detail={[row.unschedulable ? "cordoned" : "", row.taintCount > 0 ? `${row.taintCount} taints` : ""].filter(Boolean).join(" · ")} />}
        primary={shown && live ? { label: `Pods on node · ${shown.podsOnNode.length}`, onSelect: () => { setTab("overview"); requestAnimationFrame(() => podsRef.current?.scrollIntoView({ block: "start" })); } } : undefined}
        secondary={live ? (row.unschedulable ? { label: "Uncordon", onSelect: () => onCordon(false) } : { label: "Cordon", onSelect: () => onCordon(true) }) : undefined}
        more={more}
        tab={tab}
        onTab={setTab}
        manifest={shown ? <YamlBlock text={shown.yaml} copyLabel="node YAML" /> : null}
      >
        {describing && live && <DescribePane cluster={cluster} kindArg="node" namespace="" name={name} onClose={() => setDescribing(false)} />}
        {!shown ? (
          detailError && !loading ? (
            <div role="alert" className="t-body" style={{ color: "var(--color-text-danger)" }}>Could not load node detail: {detailError}</div>
          ) : (
            <div className="t-body" data-testid="node-detail-unloaded" style={{ color: "var(--color-text-tertiary)" }}>
              {gone === "removed" ? "No detail was loaded before it was removed."
                : detailOfOther && !loading ? "Detail not loaded for this object - the last read found another object with this name."
                : "Loading node detail…"}
            </div>
          )
        ) : (
          <>
            {readAt && (
              <span className="t-secondary" data-testid="node-detail-asof" style={{ color: detailError ? "var(--color-text-warning)" : undefined }}>
                {lastEvidence
                  ? `Last read ${readAt}${gone === "removed" ? " - before it was removed." : detailError ? ` - the re-read failed: ${detailError}` : "."}`
                  : `Detail read ${readAt}.`}
              </span>
            )}

            {problems.length > 0 && (
              <InspectorBlock title="Failing conditions" count={problems.length}>
                <InspectorConditions conditions={problems} colorOf={condColor} />
              </InspectorBlock>
            )}
            {unverifiedConds.length > 0 && (
              <InspectorBlock title="Unverified conditions" count={unverifiedConds.length}>
                <InspectorConditions conditions={unverifiedConds} colorOf={condColor} />
              </InspectorBlock>
            )}

            <div ref={podsRef}>
              <InspectorBlock title="Pods on node" count={shown.podsOnNode.length}>
                {shown.podsOnNode.length === 0 ? (
                  <span className="t-secondary">No pods scheduled on this node.</span>
                ) : (
                  <div style={{ display: "flex", flexDirection: "column" }}>
                    {shown.podsOnNode.map((p) => (
                      <button
                        key={`${p.namespace}/${p.name}`}
                        onClick={() => openPod(p)}
                        className="t-identifier"
                        style={{ display: "grid", gridTemplateColumns: "minmax(0,1fr) auto", gap: 8, padding: "3px 0", background: "none", border: 0, textAlign: "left", cursor: "pointer", color: "var(--color-text-primary)" }}
                        title={`open ${p.namespace}/${p.name} in Pods`}
                      >
                        <span style={{ minWidth: 0, overflowWrap: "anywhere" }}><span style={{ color: "var(--color-text-tertiary)" }}>{p.namespace} / </span>{p.name}</span>
                        <span style={{ color: "var(--color-text-tertiary)" }} title="native pod phase - lifecycle, not a health verdict">phase {p.phase}</span>
                      </button>
                    ))}
                  </div>
                )}
              </InspectorBlock>
            </div>

            <InspectorBlock title="Recent events" count={shown.events.length}>
              <InspectorEvents events={shown.events} emptyText="No events for this node." wrap />
            </InspectorBlock>

            <InspectorBlock title="Capacity">
              <div className="insp-kv">
                <span>CPU</span><span>{row.cpuAllocatable.toFixed(1)} allocatable / {row.cpuCapacity.toFixed(1)} capacity</span>
                <span>memory</span><span>{fmtMem(row.memAllocatable)} allocatable / {fmtMem(row.memCapacity)} capacity</span>
                {row.podCapacity > 0 && <><span>pods</span><span>{row.podCapacity} max</span></>}
                <span>OS / arch</span><span>{row.os || "-"} / {row.arch || "-"}</span>
              </div>
            </InspectorBlock>

            {shown.taints.length > 0 && (
              <InspectorBlock title="Taints" count={shown.taints.length}>
                <div className="insp-kv" style={{ gridTemplateColumns: "minmax(0,1fr) 120px" }}>
                  {shown.taints.map((t, i) => (
                    <Fragment key={i}>
                      <span style={{ overflowWrap: "anywhere", color: "var(--color-text-primary)" }}>{t.key}{t.value ? `=${t.value}` : ""}</span>
                      <span>{t.effect}</span>
                    </Fragment>
                  ))}
                </div>
              </InspectorBlock>
            )}

            <InspectorBlock title="Conditions" count={shown.conditions.length} defaultOpen={false}>
              <InspectorConditions
                conditions={[...shown.conditions].sort((a, b) => TONE_ORDER[conditionTone(a)] - TONE_ORDER[conditionTone(b)] || a.type.localeCompare(b.type))}
                colorOf={condColor}
              />
            </InspectorBlock>
            <InspectorBlock title="Labels" count={Object.keys(shown.labels ?? {}).length} defaultOpen={false}>
              <NodeChips values={shown.labels} />
            </InspectorBlock>
            <InspectorBlock title="Annotations" count={Object.keys(shown.annotations ?? {}).length} defaultOpen={false}>
              <NodeChips values={shown.annotations} />
            </InspectorBlock>
          </>
        )}
      </InspectorLayout>
    </div>
  );
}

function NodeChips({ values }: { values?: Record<string, string> }) {
  const entries = Object.entries(values ?? {});
  if (entries.length === 0) return <span className="t-secondary">None.</span>;
  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
      {entries.map(([k, v]) => (
        <span key={k} className="t-identifier" style={{ fontSize: 11, padding: "1px 6px", borderRadius: 3, background: "var(--color-background-secondary)", overflowWrap: "anywhere", maxWidth: "100%" }}>{k}={v}</span>
      ))}
    </div>
  );
}
