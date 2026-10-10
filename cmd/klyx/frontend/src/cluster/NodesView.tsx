import { useEffect, useCallback, useMemo, useState, useRef, useLayoutEffect } from "react";
import { Events } from "@wailsio/runtime";
import { useFleet } from "../store/fleet";
import type { ConditionDTO, NodeDetailDTO, NodeSummaryDTO, PodOnNodeDTO } from "../store/fleet";
import { listNodes, openNodeDetail, cordonNode, startDrain, cancelDrain, getNodeHeadroom } from "../bridge/nodes";
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
import { useResizablePanel } from "../chrome/useResizablePanel";
import { useNavIntent } from "../chrome/useNavIntent";
import { useListKeys } from "../chrome/useListKeys";
import { FKeysBar } from "../chrome/FKeysBar";
import { CopyButton } from "../chrome/CopyButton";
import { YamlBlock } from "../chrome/YamlBlock";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt, useReceivedAt } from "../chrome/Age";
import { IconAlertTriangle, IconServer, IconX } from "@tabler/icons-react";
import { well, ellipsis, btn, FilterButton, MetersStrip, StatusDot } from "../chrome/primitives";
import { VerdictLabel, VerdictLamp, VerdictStatus } from "../health/VerdictView";
import { type Coverage, isAttention, kindOf, orderOf, summarize } from "../health/verdict";
import { verdictMeters } from "../health/meters";
import { InspectorIdle, InspectorConditions, InspectorEvents, InspectorSection, InspectorTabButton, MetadataChips, inspectorStateBox } from "./ResourceInspector";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";

function ago(s: number): string {
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m` : s < 86400 ? `${Math.floor(s / 3600)}h` : `${Math.floor(s / 86400)}d`;
}



// columns: dot | name | roles | status | version | cpu alloc/cap | mem alloc/cap | taints | age
const gridCols = "12px minmax(150px,1.5fr) 80px minmax(110px,1fr) 76px 104px 120px 104px 120px 44px 44px";
type NodeFilter = "all" | "attention" | "unknown" | "cordoned" | "tainted";

function conditionIsProblem(c: ConditionDTO): boolean {
  if (c.type === "Ready") return c.status !== "True";
  if (c.type === "MemoryPressure" || c.type === "DiskPressure" || c.type === "PIDPressure" || c.type === "NetworkUnavailable") {
    return c.status === "True" || c.status === "Unknown";
  }
  return c.status === "False" || c.status === "Unknown";
}

const condColor = (c: ConditionDTO) =>
  conditionIsProblem(c)
    ? "var(--color-text-danger)"
    : c.status === "Unknown"
      ? "var(--color-text-info)"
      : "var(--color-text-success)";

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

function DrainModal({ cluster, node, opts, onClose }: { cluster: string; node: string; opts: DrainOptions; onClose: () => void }) {
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

      const result = await startDrain(cluster, node, opts, streamId);
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
  }, [cluster, node, opts]);

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
          maxHeight: "80vh",
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

export function NodesView({ cluster }: { cluster: string }) {
  const nodes = useFleet((s) => s.nodes);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);

  // drainPending holds the node while the composer dialog is open; confirming
  // moves node + composed options into drainActive, which mounts the modal.
  // drainActive also pins the cluster the drain was confirmed on: the modal
  // must never restart the drain against another cluster if this view is
  // re-rendered for one (batch-3 review, non-blocking).
  const [drainActive, setDrainActive] = useState<{ cluster: string; node: string; opts: DrainOptions } | null>(null);
  const [cordonPending, setCordonPending] = useState<{ node: string; cordon: boolean } | null>(null);
  const [drainPending, setDrainPending] = useState<string | null>(null);
  const [filter, setFilter] = useState<NodeFilter>("all");
  const [roleFilter, setRoleFilter] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  useEffect(() => {
    void listNodes(cluster);
    return () => { useFleet.getState().clearNodes(); };
  }, [cluster]);

  // Live per-node usage (pod-attributed) - fetched alongside the list, joined
  // by node name at render. Unavailable metrics degrade the two usage columns
  // to "—" with a caption, never the whole board.
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

  // Palette verbs land here as one-shot intents once the node list is loaded.
  useNavIntent(cluster, ["drain-node", "cordon-node", "select-node"], (intent) => {
    if (nodes.loading || nodes.items.length === 0) return "wait";
    const name = "node" in intent ? intent.node : "";
    const n = nodes.items.find((x) => x.name === name);
    if (!n) return "missing";
    if (intent.kind === "drain-node") setDrainPending(n.name);
    else if (intent.kind === "cordon-node") setCordonPending({ node: n.name, cordon: intent.cordon });
    else void openNodeDetail(cluster, n.name);
    return "consumed";
  }, [nodes.items, nodes.loading]);

  const onRefresh = () => {
    void listNodes(cluster);
    void getNodeUtilization(cluster).then(setUtil);
  };
  const receivedAt = useReceivedAt(nodes.cluster === cluster && !nodes.loading && nodes.items.length > 0 ? nodes.items : null);
  // Only rows read for THIS cluster are shown or counted.
  const orderedNodes = useMemo(
    () => (nodes.rowsCluster === cluster ? [...nodes.items] : []).sort((a, b) => orderOf(kindOf(a.health)) - orderOf(kindOf(b.health)) || a.name.localeCompare(b.name)),
    [nodes.items, nodes.rowsCluster, cluster],
  );
  const visibleNodes = useMemo(
    () => orderedNodes.filter((n) => nodeMatchesFilter(n, filter) && nodeMatchesQuery(n, query, roleFilter)),
    [orderedNodes, filter, query, roleFilter],
  );

  // Keyboard parity with the triage views: j/k moves a key-stable selection,
  // Enter opens the node inspector, Esc closes it, / focuses the filter.
  const [kbName, setKbName] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const kbIdx = kbName === null ? -1 : visibleNodes.findIndex((n) => n.name === kbName);
  const handleKbSelect = useCallback((idx: number) => {
    const n = visibleNodes[idx];
    if (n) setKbName(n.name);
  }, [visibleNodes]);
  const handleKbActivate = useCallback((idx: number) => {
    const n = visibleNodes[idx];
    if (n) void openNodeDetail(cluster, n.name);
  }, [visibleNodes, cluster]);
  const handleKbEscape = useCallback(() => {
    if (useFleet.getState().nodes.selected) useFleet.getState().selectNode(null);
  }, []);
  useListKeys({
    count: visibleNodes.length,
    selected: kbIdx,
    onSelect: handleKbSelect,
    onActivate: handleKbActivate,
    onEscape: handleKbEscape,
    searchRef,
  });
  const roles = useMemo(
    () => [...new Set(nodes.items.flatMap((n) => n.roles))].sort(),
    [nodes.items],
  );
  // Nodes are cluster-scoped: rows count only when read for THIS cluster; a
  // failed refresh keeps last-good rows, labelled as such.
  const coverage: Coverage = nodes.rowsCluster !== cluster ? "unavailable" : nodes.error ? "stale" : "complete";
  const counts = summarize(coverage === "unavailable" ? [] : nodes.items, (n) => n.health);
  const attention = counts.attention;
  const cordonedCount = nodes.items.filter((n) => n.unschedulable).length;
  const taintedCount = nodes.items.filter((n) => n.taintCount > 0).length;
  const versionCount = new Set(nodes.items.map((n) => n.version).filter(Boolean)).size;
  const cpuAlloc = nodes.items.reduce((sum, n) => sum + n.cpuAllocatable, 0);
  const cpuCap = nodes.items.reduce((sum, n) => sum + n.cpuCapacity, 0);
  const memAlloc = nodes.items.reduce((sum, n) => sum + n.memAllocatable, 0);
  const memCap = nodes.items.reduce((sum, n) => sum + n.memCapacity, 0);
  const podCap = nodes.items.reduce((sum, n) => sum + n.podCapacity, 0);

  return (
    <div style={{ padding: "14px 16px", display: "flex", gap: 12, height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box", position: "relative" }}>
      <div style={{ flex: 1, minWidth: 0, minHeight: 0, display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 16, flexShrink: 0 }}>
          <div>
            <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 3 }}>Nodes</div>
            <div style={{ fontSize: 18, fontWeight: 600 }}>Node board</div>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <UpdatedAt at={receivedAt} />
            <button onClick={onRefresh} style={btn}>refresh</button>
          </div>
        </div>

        <MetersStrip
          note="faults first, then name"
          meters={[
            // fault = NotReady, degraded = pressure: the verdict owns both.
            ...verdictMeters({ noun: "nodes", counts, coverage }),
            cordonedCount > 0 && { label: "cordoned", value: cordonedCount },
            taintedCount > 0 && { label: "tainted", value: taintedCount },
            { label: "versions", value: versionCount },
            { label: "cpu alloc", value: `${cpuAlloc.toFixed(1)}/${cpuCap.toFixed(1)}` },
            { label: "mem alloc", value: `${fmtMem(memAlloc)}/${fmtMem(memCap)}` },
            { label: "pod cap", value: podCap },
          ]}
        />

        <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center", flexShrink: 0 }}>
          <FilterButton label="all" count={nodes.items.length} active={filter === "all"} onClick={() => setFilter("all")} />
          <FilterButton label="attention" count={attention} active={filter === "attention"} onClick={() => setFilter("attention")} />
          {(counts.unknown > 0 || filter === "unknown") && (
            <FilterButton label="unknown" count={counts.unknown} active={filter === "unknown"} onClick={() => setFilter("unknown")} />
          )}
          <FilterButton label="cordoned" count={cordonedCount} active={filter === "cordoned"} onClick={() => setFilter("cordoned")} />
          <FilterButton label="tainted" count={taintedCount} active={filter === "tainted"} onClick={() => setFilter("tainted")} />
          {roles.length > 0 && (
            <>
              <span style={{ fontSize: 9, color: "var(--color-text-tertiary)", marginLeft: 4 }}>role</span>
              <FilterButton label="all roles" count={nodes.items.length} active={roleFilter === null} onClick={() => setRoleFilter(null)} />
              {roles.slice(0, 5).map((role) => (
                <FilterButton key={role} label={role} count={nodes.items.filter((n) => n.roles.includes(role)).length} active={roleFilter === role} onClick={() => setRoleFilter(role)} />
              ))}
            </>
          )}
          <input
            ref={searchRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="filter nodes"
            aria-label="filter nodes"
            style={{ marginLeft: "auto", fontSize: 11, padding: "3px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", width: 170 }}
          />
          {visibleNodes.length !== nodes.items.length && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{visibleNodes.length} of {nodes.items.length}</span>}
        </div>

        <SurvivabilityModule cluster={cluster} nodes={nodes.items} />

        {util !== null && (
          util.available ? (
            <div style={{ fontSize: 9, color: "var(--color-text-tertiary)", flexShrink: 0 }}>
              cpu/mem used = pod usage vs allocatable (5m rate) — node OS/kubelet overhead not included
              {util.message ? ` · ${util.message}` : ""}
            </div>
          ) : (
            <div style={{ fontSize: 9, color: "var(--color-text-warning)", flexShrink: 0 }} data-testid="node-util-unavailable">
              live usage unavailable — {util.message || "no metrics source"}
            </div>
          )
        )}

        {/* Read failure - never pass as "no nodes" (review 2026-10-03 #10). */}
        {nodes.error && (
          <div style={{ marginBottom: 10, flexShrink: 0 }}>
            <WatchErrorBanner tool="Nodes" failure="read failed" error={nodes.error} />
          </div>
        )}

        {nodes.loading && nodes.items.length === 0 ? (
          <SkeletonRows rows={6} label="loading nodes" />
        ) : nodes.items.length === 0 ? (
          <EmptyState
            icon={<IconServer size={28} stroke={1.2} />}
            title="No nodes visible."
            hint="Either the list failed or RBAC hides nodes from this identity - refresh to retry."
          />
        ) : (
          <div style={{ ...well, overflow: "hidden", flex: 1, minHeight: 0, display: "flex", flexDirection: "column", fontFamily: "var(--font-mono)", fontSize: 12 }}>
            <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, padding: "5px 8px", fontSize: 9, color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-secondary)", flexShrink: 0 }}>
              <span /><span>node</span><span>roles</span><span>status</span><span>version</span><span>cpu used</span><span>mem used</span><span>cpu alloc/cap</span><span>mem alloc/cap</span><span>taints</span><span>age</span>
            </div>
            <div data-testid="nodes-list-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {visibleNodes.length === 0 ? (
                <EmptyState
                  title="No nodes match the current filter."
                  action={{ label: "Clear filters", onClick: () => { setFilter("all"); setRoleFilter(null); setQuery(""); } }}
                />
              ) : visibleNodes.map((n) => {
                const isSelected = nodes.selected?.name === n.name;
                const isKb = kbName === n.name && kbIdx >= 0;
                return (
                  <div
                    key={n.name}
                    data-rowhost="true"
                    aria-selected={isKb}
                    ref={isKb ? (el) => el?.scrollIntoView?.({ block: "nearest" }) : undefined}
                    onClick={() => void openNodeDetail(cluster, n.name)}
                    style={{
                      display: "grid", gridTemplateColumns: gridCols, gap: 10, alignItems: "center",
                      padding: "7px 8px", borderBottom: "0.5px solid var(--color-border-tertiary)",
                      cursor: "pointer",
                      background: isKb || isSelected ? "var(--color-background-secondary)" : undefined,
                      boxShadow: isKb ? "inset 2px 0 0 var(--bench-selection-edge, var(--color-text-info))" : undefined,
                    }}
                  >
                    <VerdictLamp health={n.health} size={8} />
                    <span style={{ display: "flex", alignItems: "center", gap: 2, minWidth: 0 }}>
                      <span style={{ fontWeight: 500, ...ellipsis }} title={n.name}>{n.name}</span>
                      <CopyButton text={n.name} label={`copy node name ${n.name}`} appearOnRowHover />
                    </span>
                    <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>
                      {n.roles.length > 0 ? n.roles.join(", ") : "-"}
                    </span>
                    <span style={ellipsis}><VerdictLabel health={n.health} /></span>
                    <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>{n.version}</span>
                    <UsageCell resource="cpu" usage={usageByNode.get(n.name)?.cpu ?? null} allocatable={n.cpuAllocatable} format={fmtCpu} />
                    <UsageCell resource="mem" usage={usageByNode.get(n.name)?.mem ?? null} allocatable={n.memAllocatable} format={fmtMem} />
                    <CapacityCell value={n.cpuAllocatable} total={n.cpuCapacity} format={(v) => v.toFixed(1)} />
                    <CapacityCell value={n.memAllocatable} total={n.memCapacity} format={fmtMem} />
                    <span style={{ color: n.taintCount > 0 ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>
                      {n.taintCount > 0 ? n.taintCount : "-"}
                    </span>
                    <span style={{ color: "var(--color-text-tertiary)" }}>{ago(n.ageSeconds)}</span>
                  </div>
                );
              })}
            </div>
            <FKeysBar
              keys={[["j k", "nav"], ["\u21B5", "inspect"], ["/", "filter"]]}
              status={`${visibleNodes.length} of ${nodes.items.length} \u00B7 broken first`}
            />
          </div>
        )}
      </div>

      {nodes.selected ? (
        <NodeDetailPanel
          cluster={cluster}
          name={nodes.selected.name}
          detail={nodes.detail}
          loading={nodes.detailLoading}
          isProtected={isProtected}
          onCordon={(node, cordon) => setCordonPending({ node, cordon })}
          onDrain={(node) => setDrainPending(node)}
          onClose={() => useFleet.getState().selectNode(null)}
        />
      ) : (
        <EmptyNodeInspector nodeCount={visibleNodes.length} attention={attention} />
      )}

      {/* Cordon/uncordon confirm dialog */}
      {cordonPending && (
        <ConfirmDialog
          title={cordonPending.cordon ? "Cordon node" : "Uncordon node"}
          cluster={cluster}
          detail={`node ${cordonPending.node} will be marked ${cordonPending.cordon ? "unschedulable" : "schedulable"}`}
          protected={isProtected}
          danger={cordonPending.cordon}
          confirmLabel={cordonPending.cordon ? "Cordon" : "Uncordon"}
          onConfirm={() => {
            const { node, cordon } = cordonPending;
            setCordonPending(null);
            void cordonNode(cluster, node, cordon);
          }}
          onCancel={() => setCordonPending(null)}
        />
      )}

      {/* Drain composer dialog */}
      {drainPending && (
        <DrainComposerDialog
          cluster={cluster}
          node={drainPending}
          isProtected={isProtected}
          onConfirm={(opts) => {
            const node = drainPending;
            setDrainPending(null);
            setDrainActive({ cluster, node, opts });
          }}
          onCancel={() => setDrainPending(null)}
        />
      )}

      {/* Drain modal */}
      {drainActive && (
        <DrainModal
          cluster={drainActive.cluster}
          node={drainActive.node}
          opts={drainActive.opts}
          onClose={() => {
            const drained = drainActive.cluster;
            setDrainActive(null);
            if (drained === cluster) void listNodes(cluster);
          }}
        />
      )}
    </div>
  );
}

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

function EmptyNodeInspector({ nodeCount, attention }: { nodeCount: number; attention: number }) {
  return (
    <InspectorIdle
      subject="nodes"
      attention={attention}
      hint={nodeCount === 0 ? "No nodes are visible with the current filters." : "Select a node for conditions, taints, pods, events, and day-2 actions."}
    />
  );
}

function NodeDetailPanel({
  cluster, name, detail, loading, isProtected, onCordon, onDrain, onClose,
}: {
  cluster: string;
  name: string;
  detail: NodeDetailDTO | null;
  loading: boolean;
  isProtected: boolean;
  onCordon: (node: string, cordon: boolean) => void;
  onDrain: (node: string) => void;
  onClose: () => void;
}) {
  const [tab, setTab] = useState<"info" | "yaml">("info");
  const { width, handleProps } = useResizablePanel();

  // suppress unused warning — isProtected propagated for cordon/drain callers above
  void isProtected;

  const handleKey = useCallback((e: KeyboardEvent) => {
    if (e.key === "Escape") onClose();
  }, [onClose]);
  useEffect(() => {
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [handleKey]);

  return (
    <div style={{
      width, flexShrink: 0, position: "relative", minHeight: 0,
      background: "var(--bench-well-bg, var(--color-background-secondary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))", borderRadius: "var(--radius-sm)", padding: "12px 16px",
      overflowY: "auto",
      fontFamily: "var(--font-mono)",
      fontSize: 12,
      paddingLeft: 16,
    }}>
      {/* Resize handle — drag left edge */}
      <div {...handleProps} />

      {/* Panel header */}
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 10, position: "sticky", top: 0, background: "var(--color-background-primary)", paddingTop: 2, paddingBottom: 6, borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
        <span style={{ fontWeight: 500, flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={name}>{name}</span>
        <button onClick={onClose} aria-label="close node detail panel" style={{ ...btn, padding: "2px 8px", display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
      </div>

      {/* Tabs */}
      <div style={{ display: "flex", gap: 2, marginBottom: 12 }}>
        <InspectorTabButton active={tab === "info"} onClick={() => setTab("info")}>info</InspectorTabButton>
        <InspectorTabButton active={tab === "yaml"} onClick={() => setTab("yaml")}>yaml</InspectorTabButton>
      </div>

      {loading && !detail ? (
        <div style={inspectorStateBox}>Loading detail...</div>
      ) : !detail ? (
        <div style={inspectorStateBox}>Could not load node detail.</div>
      ) : detail.error ? (
        // The backend returns a zero DTO WITH the reason on a failed read;
        // never render that as a real (empty) node.
        <div role="alert" style={{ ...inspectorStateBox, color: "var(--color-text-danger)" }}>Could not load node detail: {detail.error}</div>
      ) : (
        <>
          {tab === "info" && (
            <InfoTab
              detail={detail}
              cluster={cluster}
              onCordon={onCordon}
              onDrain={onDrain}
            />
          )}
          {tab === "yaml" && <YamlBlock text={detail.yaml} copyLabel="node YAML" />}
        </>
      )}
    </div>
  );
}

function InfoTab({ detail, cluster, onCordon, onDrain }: {
  detail: NodeDetailDTO;
  cluster: string;
  onCordon: (node: string, cordon: boolean) => void;
  onDrain: (node: string) => void;
}) {
  const n = detail.summary;
  // Verdict provenance: the headline is the selected list row's verdict; the
  // detail read's own summary is used only if the row has left the list.
  const rowHealth = useFleet((s) => s.nodes.items.find((x) => x.name === n.name)?.health);
  const setSection = useFleet((s) => s.setSection);
  const [describing, setDescribing] = useState(false);

  const handlePodClick = (pod: PodOnNodeDTO) => {
    // Cross-link straight to the pod: the parked select-pod intent survives
    // until PodsView's list has loaded, then opens the detail panel - no more
    // "navigate and re-find it yourself".
    setSection("pods");
    useFleet.getState().requestNavIntent({ kind: "select-pod", cluster, namespace: pod.namespace, name: pod.name });
  };

  return (
    <>
      {/* State first: the node's headline condition, scannable before any
          key-value rows. */}
      <VerdictStatus health={rowHealth ?? n.health} detail={[n.roles.join(", "), n.version, n.taintCount > 0 ? `${n.taintCount} taints` : ""].filter(Boolean).join(" · ")} />

      {/* Actions row */}
      <InspectorSection title="Actions">
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          {n.unschedulable ? (
            <button
              aria-label="uncordon"
              onClick={() => onCordon(n.name, false)}
              style={{ ...btn, color: "var(--color-text-success)", borderColor: "var(--color-text-success)" }}
            >
              uncordon
            </button>
          ) : (
            <button
              aria-label="cordon"
              onClick={() => onCordon(n.name, true)}
              style={{ ...btn, color: "var(--color-text-warning)", borderColor: "var(--color-text-warning)" }}
            >
              cordon
            </button>
          )}
          <button
            aria-label="drain"
            onClick={() => onDrain(n.name)}
            style={{ ...btn, color: "var(--color-text-danger)", borderColor: "var(--color-text-danger)" }}
          >
            drain
          </button>
          <button onClick={() => setDescribing(true)} style={btn}>describe</button>
        </div>
      </InspectorSection>

      {describing && (
        <DescribePane
          cluster={cluster}
          kindArg="node"
          namespace=""
          name={n.name}
          onClose={() => setDescribing(false)}
        />
      )}

      {/* Summary header */}
      <InspectorSection title="Summary">
        <NodeInfoRow label="ready">{n.ready ? "True" : "False"}</NodeInfoRow>
        {n.unschedulable && <NodeInfoRow label="cordoned"><span style={{ color: "var(--color-text-warning)" }}>yes</span></NodeInfoRow>}
        {n.roles.length > 0 && <NodeInfoRow label="roles">{n.roles.join(", ")}</NodeInfoRow>}
        <NodeInfoRow label="version">{n.version || "-"}</NodeInfoRow>
        <NodeInfoRow label="os">{n.os || "-"}</NodeInfoRow>
        <NodeInfoRow label="arch">{n.arch || "-"}</NodeInfoRow>
        <NodeInfoRow label="cpu">{n.cpuAllocatable.toFixed(1)} alloc / {n.cpuCapacity.toFixed(1)} cap</NodeInfoRow>
        <NodeInfoRow label="mem">{fmtMem(n.memAllocatable)} alloc / {fmtMem(n.memCapacity)} cap</NodeInfoRow>
        {n.podCapacity > 0 && <NodeInfoRow label="pods">{n.podCapacity} max</NodeInfoRow>}
        {n.problems.length > 0 && (
          <NodeInfoRow label="problems"><span style={{ color: "var(--color-text-danger)" }}>{n.problems.join(", ")}</span></NodeInfoRow>
        )}
      </InspectorSection>

      {/* Conditions - problems sort first; the node-specific severity rule
          (Unknown pressure is a problem) rides in via colorOf. */}
      {detail.conditions.length > 0 && (
        <InspectorSection title="Conditions" collapsible>
          <InspectorConditions
            conditions={[...detail.conditions].sort((a, b) => Number(conditionIsProblem(b)) - Number(conditionIsProblem(a)) || a.type.localeCompare(b.type))}
            colorOf={condColor}
          />
        </InspectorSection>
      )}

      {/* Taints */}
      {detail.taints.length > 0 && (
        <InspectorSection title="Taints" count={detail.taints.length} collapsible>
          <div style={{ display: "grid", gridTemplateColumns: "minmax(0,1fr) minmax(0,0.6fr) 80px", gap: 6, fontSize: 9, marginBottom: 2, color: "var(--color-text-tertiary)", textTransform: "uppercase" }}>
            <span>key</span><span>value</span><span>effect</span>
          </div>
          {detail.taints.map((t, i) => (
            <div key={i} style={{ display: "grid", gridTemplateColumns: "minmax(0,1fr) minmax(0,0.6fr) 80px", gap: 6, fontSize: 11, padding: "2px 0", alignItems: "center" }}>
              <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={t.key}>{t.key}</span>
              <span style={{ color: "var(--color-text-tertiary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{t.value || "-"}</span>
              <span style={{ color: "var(--color-text-secondary)" }}>{t.effect}</span>
            </div>
          ))}
        </InspectorSection>
      )}

      <MetadataChips title="Labels" values={detail.labels} defaultCollapsed />
      <MetadataChips title="Annotations" values={detail.annotations} defaultCollapsed />

      {/* Pods on node */}
      <InspectorSection title="Pods on node" count={detail.podsOnNode.length} collapsible>
        {detail.podsOnNode.length === 0 ? (
          <span style={{ color: "var(--color-text-tertiary)" }}>No pods scheduled on this node.</span>
        ) : (
          detail.podsOnNode.map((p) => (
            <div
              key={`${p.namespace}/${p.name}`}
              onClick={() => handlePodClick(p)}
              style={{ display: "grid", gridTemplateColumns: "82px minmax(0, 1fr) 72px", gap: 8, fontSize: 11, cursor: "pointer", padding: "2px 0", alignItems: "baseline" }}
            >
              <span style={{ color: "var(--color-text-tertiary)", minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={p.namespace}>{p.namespace}</span>
              <span style={{ fontWeight: 500, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={p.name}>{p.name}</span>
              <span style={{ color: p.phase === "Running" ? "var(--color-text-success)" : p.phase === "Failed" ? "var(--color-text-danger)" : "var(--color-text-tertiary)", textAlign: "right" }}>{p.phase}</span>
            </div>
          ))
        )}
      </InspectorSection>

      {/* Events */}
      <InspectorSection title="Events" count={detail.events.length} collapsible>
        <InspectorEvents events={detail.events} emptyText="No events for this node." />
      </InspectorSection>
    </>
  );
}

function NodeInfoRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ display: "flex", gap: 8, fontSize: 11 }}>
      <span style={{ color: "var(--color-text-tertiary)", width: 70, flexShrink: 0 }}>{label}</span>
      <span style={{ color: "var(--color-text-primary)" }}>{children}</span>
    </div>
  );
}
