import { useEffect, useCallback, useMemo, useState, useRef, useLayoutEffect } from "react";
import { Events } from "@wailsio/runtime";
import { useFleet } from "../store/fleet";
import type { ConditionDTO, NodeDetailDTO, NodeSummaryDTO, PodOnNodeDTO } from "../store/fleet";
import { listNodes, openNodeDetail, cordonNode, startDrain, cancelDrain } from "../bridge/nodes";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { fmtMem } from "./saturation";
import { useResizablePanel } from "../chrome/useResizablePanel";
import { ellipsis, frame, btn, FilterButton, SummaryCell, StatusDot } from "../chrome/primitives";
import { InspectorSection, InspectorTabButton, MetadataChips, inspectorStateBox } from "./ResourceInspector";

function ago(s: number): string {
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m` : s < 86400 ? `${Math.floor(s / 3600)}h` : `${Math.floor(s / 86400)}d`;
}

function nodeDotColor(n: NodeSummaryDTO): string {
  if (!n.ready || n.problems.some((p) => p === "NotReady")) return "var(--color-text-danger)";
  if (n.problems.length > 0) return "var(--color-text-danger)"; // pressure
  if (n.unschedulable) return "var(--color-text-warning)";
  return "var(--color-text-tertiary)";
}


// columns: dot | name | roles | status | version | cpu alloc/cap | mem alloc/cap | taints | age
const gridCols = "12px minmax(170px,1.5fr) 86px minmax(120px,1fr) 82px 112px 132px 48px 44px";
type NodeFilter = "all" | "attention" | "cordoned" | "tainted";

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

function nodeNeedsAttention(n: NodeSummaryDTO): boolean {
  return !n.ready || n.problems.length > 0;
}

function nodeRank(n: NodeSummaryDTO): number {
  if (!n.ready || n.problems.some((p) => p === "NotReady")) return 0;
  if (n.problems.length > 0) return 1;
  if (n.unschedulable) return 2;
  if (n.taintCount > 0) return 3;
  return 4;
}

function nodeStatusText(n: NodeSummaryDTO): string {
  if (n.problems.length > 0) return n.problems.join(", ");
  if (!n.ready) return "NotReady";
  if (n.unschedulable) return "cordoned";
  if (n.taintCount > 0) return `${n.taintCount} taints`;
  return "ready";
}

function nodeStatusColor(n: NodeSummaryDTO): string {
  if (!n.ready || n.problems.length > 0) return "var(--color-text-danger)";
  if (n.unschedulable || n.taintCount > 0) return "var(--color-text-warning)";
  return "var(--color-text-success)";
}

function nodeMatchesFilter(n: NodeSummaryDTO, filter: NodeFilter): boolean {
  if (filter === "attention") return nodeNeedsAttention(n);
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

interface LogChunkDTO {
  lines: string[];
  eof: boolean;
  error?: string;
}

type DrainStatus = "starting" | "running" | "ended" | "error";

function DrainModal({ cluster, node, onClose }: { cluster: string; node: string; onClose: () => void }) {
  const [status, setStatus] = useState<DrainStatus>("starting");
  const [errorMsg, setErrorMsg] = useState<string | undefined>(undefined);
  const streamIdRef = useRef<string>("");
  const offRef = useRef<(() => void) | null>(null);
  const bufRef = useRef<string[]>([]);
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

  useEffect(() => {
    let stale = false;

    async function open() {
      const result = await startDrain(cluster, node);
      if (stale) return;
      if (result.error || !result.streamId) {
        setStatus("error");
        setErrorMsg(result.error || "failed to start drain");
        return;
      }
      streamIdRef.current = result.streamId;
      setStatus("running");

      const eventName = `nodedrain:${result.streamId}`;
      const off = Events.On(eventName, (ev: { data: LogChunkDTO }) => {
        if (stale) return;
        const chunk = ev.data;
        if (chunk.lines && chunk.lines.length > 0) {
          bufRef.current = [...bufRef.current, ...chunk.lines];
          setVersion((v) => v + 1);
        }
        if (chunk.eof) {
          if (chunk.error) {
            setStatus("error");
            setErrorMsg(chunk.error);
          } else {
            setStatus("ended");
          }
          off();
          offRef.current = null;
          streamIdRef.current = "";
        }
      });
      offRef.current = typeof off === "function" ? off : () => {};
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
  }, [cluster, node]);

  const handleCancel = useCallback(() => {
    if (offRef.current) { offRef.current(); offRef.current = null; }
    const sid = streamIdRef.current;
    if (sid) {
      streamIdRef.current = "";
      void cancelDrain(sid);
    }
    onClose();
  }, [onClose]);

  const handleKey = useCallback((e: KeyboardEvent) => {
    if (e.key === "Escape" && (status === "ended" || status === "error")) onClose();
  }, [status, onClose]);
  useEffect(() => {
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [handleKey]);

  const lines = bufRef.current;

  return (
    <div
      onClick={(e) => { if (e.target === e.currentTarget && (status === "ended" || status === "error")) onClose(); }}
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
          {lines.map((line, i) => (
            <div key={i} style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{line || "​"}</div>
          ))}
        </div>

        {/* Footer buttons */}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
          {(status === "running" || status === "starting") && (
            <button onClick={handleCancel} style={{ ...btn, color: "var(--color-text-danger)", borderColor: "var(--color-text-danger)" }}>
              Cancel drain
            </button>
          )}
          {(status === "ended" || status === "error") && (
            <button onClick={onClose} style={btn}>Close</button>
          )}
        </div>
      </div>
    </div>
  );
}

// --- Main view -------------------------------------------------------------------

export function NodesView({ cluster }: { cluster: string }) {
  const nodes = useFleet((s) => s.nodes);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);

  const [drainNode, setDrainNode] = useState<string | null>(null);
  const [cordonPending, setCordonPending] = useState<{ node: string; cordon: boolean } | null>(null);
  const [drainPending, setDrainPending] = useState<string | null>(null);
  const [filter, setFilter] = useState<NodeFilter>("all");
  const [roleFilter, setRoleFilter] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  useEffect(() => {
    void listNodes(cluster);
    return () => { useFleet.getState().clearNodes(); };
  }, [cluster]);

  const onRefresh = () => { void listNodes(cluster); };
  const orderedNodes = useMemo(
    () => [...nodes.items].sort((a, b) => nodeRank(a) - nodeRank(b) || a.name.localeCompare(b.name)),
    [nodes.items],
  );
  const visibleNodes = useMemo(
    () => orderedNodes.filter((n) => nodeMatchesFilter(n, filter) && nodeMatchesQuery(n, query, roleFilter)),
    [orderedNodes, filter, query, roleFilter],
  );
  const roles = useMemo(
    () => [...new Set(nodes.items.flatMap((n) => n.roles))].sort(),
    [nodes.items],
  );
  const ready = nodes.items.filter((n) => n.ready).length;
  const attention = nodes.items.filter(nodeNeedsAttention).length;
  const cordonedCount = nodes.items.filter((n) => n.unschedulable).length;
  const taintedCount = nodes.items.filter((n) => n.taintCount > 0).length;
  const pressureCount = nodes.items.filter((n) => n.problems.some((p) => p !== "NotReady")).length;
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
          <button onClick={onRefresh} style={btn}>refresh</button>
        </div>

        <div style={{ ...frame, display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(132px, 1fr))", overflow: "hidden", flexShrink: 0 }}>
          <SummaryCell label="ready" value={`${ready}/${nodes.items.length}`} tone={attention > 0 ? "warning" : "success"} />
          <SummaryCell label="attention" value={String(attention)} tone={attention > 0 ? "danger" : "muted"} />
          <SummaryCell label="cordoned" value={String(cordonedCount)} tone={cordonedCount > 0 ? "warning" : "muted"} />
          <SummaryCell label="tainted" value={String(taintedCount)} tone={taintedCount > 0 ? "warning" : "muted"} />
          <SummaryCell label="pressure" value={String(pressureCount)} tone={pressureCount > 0 ? "danger" : "muted"} />
          <SummaryCell label="versions" value={String(versionCount)} />
          <SummaryCell label="cpu alloc" value={`${cpuAlloc.toFixed(1)}/${cpuCap.toFixed(1)}`} />
          <SummaryCell label="mem alloc" value={`${fmtMem(memAlloc)}/${fmtMem(memCap)}`} />
          <SummaryCell label="pod cap" value={String(podCap)} />
        </div>

        <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center", flexShrink: 0 }}>
          <FilterButton label="all" count={nodes.items.length} active={filter === "all"} onClick={() => setFilter("all")} />
          <FilterButton label="needs attention" count={attention} active={filter === "attention"} onClick={() => setFilter("attention")} />
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
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="filter nodes"
            aria-label="filter nodes"
            style={{ marginLeft: "auto", fontSize: 11, padding: "3px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", width: 170 }}
          />
          {visibleNodes.length !== nodes.items.length && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{visibleNodes.length} of {nodes.items.length}</span>}
        </div>

        {nodes.loading && nodes.items.length === 0 ? (
          <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>Loading nodes…</div>
        ) : nodes.items.length === 0 ? (
          <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No nodes found.</div>
        ) : (
          <div style={{ ...frame, overflow: "hidden", flex: 1, minHeight: 0, display: "flex", flexDirection: "column", fontFamily: "var(--font-mono)", fontSize: 12 }}>
            <div style={{ display: "grid", gridTemplateColumns: gridCols, gap: 10, padding: "5px 8px", fontSize: 9, color: "var(--color-text-tertiary)", borderBottom: "0.5px solid var(--color-border-secondary)", flexShrink: 0 }}>
              <span /><span>node</span><span>roles</span><span>status</span><span>version</span><span>cpu alloc/cap</span><span>mem alloc/cap</span><span>taints</span><span>age</span>
            </div>
            <div data-testid="nodes-list-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {visibleNodes.length === 0 ? (
                <div style={{ padding: 14, color: "var(--color-text-secondary)", fontSize: 13 }}>No nodes match the current filter.</div>
              ) : visibleNodes.map((n) => {
                const isSelected = nodes.selected?.name === n.name;
                const dotColor = nodeDotColor(n);
                return (
                  <div
                    key={n.name}
                    onClick={() => void openNodeDetail(cluster, n.name)}
                    style={{
                      display: "grid", gridTemplateColumns: gridCols, gap: 10, alignItems: "center",
                      padding: "7px 8px", borderBottom: "0.5px solid var(--color-border-tertiary)",
                      cursor: "pointer",
                      background: isSelected ? "var(--color-background-secondary)" : undefined,
                    }}
                  >
                    <StatusDot size={8} color={dotColor} />
                    <span style={{ fontWeight: 500, ...ellipsis }} title={n.name}>{n.name}</span>
                    <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>
                      {n.roles.length > 0 ? n.roles.join(", ") : "-"}
                    </span>
                    <span style={{ color: nodeStatusColor(n), ...ellipsis }}>{nodeStatusText(n)}</span>
                    <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>{n.version}</span>
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

      {/* Drain confirm dialog */}
      {drainPending && (
        <ConfirmDialog
          title="Drain node"
          cluster={cluster}
          detail={`kubectl drain ${drainPending} --ignore-daemonsets --delete-emptydir-data`}
          protected={isProtected}
          danger
          confirmLabel="Drain"
          onConfirm={() => {
            const node = drainPending;
            setDrainPending(null);
            setDrainNode(node);
          }}
          onCancel={() => setDrainPending(null)}
        />
      )}

      {/* Drain modal */}
      {drainNode && (
        <DrainModal
          cluster={cluster}
          node={drainNode}
          onClose={() => {
            setDrainNode(null);
            void listNodes(cluster);
          }}
        />
      )}
    </div>
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
      <span style={{ height: 4, background: "var(--color-background-secondary)", borderRadius: 2, overflow: "hidden" }}>
        <span style={{ display: "block", height: "100%", width: `${pct}%`, background: pct < 80 ? "var(--color-text-success)" : "var(--color-text-warning)" }} />
      </span>
    </span>
  );
}

function EmptyNodeInspector({ nodeCount, attention }: { nodeCount: number; attention: number }) {
  return (
    <aside style={{ width: 360, flexShrink: 0, borderLeft: "0.5px solid var(--color-border-tertiary)", paddingLeft: 16, fontFamily: "var(--font-mono)", fontSize: 12, color: "var(--color-text-secondary)" }}>
      <div style={{ position: "sticky", top: 0, background: "var(--color-background-primary)", paddingTop: 2, paddingBottom: 6, borderBottom: "0.5px solid var(--color-border-tertiary)", marginBottom: 12 }}>
        <div style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>selected node</div>
        <div style={{ color: attention > 0 ? "var(--color-text-warning)" : "var(--color-text-success)", fontWeight: 600, marginTop: 2 }}>
          {attention > 0 ? `${attention} need attention` : "all nodes quiet"}
        </div>
      </div>
      <div>{nodeCount === 0 ? "No nodes are visible with the current filters." : "Select a node to inspect conditions, taints, pods, events, and day-2 actions."}</div>
    </aside>
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
      borderLeft: "0.5px solid var(--color-border-tertiary)",
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
        <button onClick={onClose} style={{ ...btn, padding: "2px 8px", fontSize: 12 }}>✕</button>
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
          {tab === "yaml" && (
            <pre style={{
              margin: 0, padding: 10,
              background: "var(--color-background-secondary)",
              border: "0.5px solid var(--color-border-tertiary)",
              borderRadius: 4,
              fontFamily: "var(--font-mono)", fontSize: 11, lineHeight: 1.5,
              overflowX: "auto", whiteSpace: "pre-wrap", wordBreak: "break-all",
              color: "var(--color-text-primary)",
            }}>{detail.yaml}</pre>
          )}
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
  const setSection = useFleet((s) => s.setSection);

  const handlePodClick = (pod: PodOnNodeDTO) => {
    // Cross-link to pods section: set section and let PodsView load
    setSection("pods");
    // Note: actual pod detail opening requires a full pods load first.
    // We navigate to pods section; the user can click the pod from there.
    void cluster; // cluster is used by the parent; this nav is section-only
  };

  return (
    <>
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
        </div>
      </InspectorSection>

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

      {/* Conditions */}
      {detail.conditions.length > 0 && (
        <InspectorSection title="Conditions" collapsible>
          {[...detail.conditions].sort((a, b) => Number(conditionIsProblem(b)) - Number(conditionIsProblem(a)) || a.type.localeCompare(b.type)).map((c) => {
            const conditionMessage = c.message || c.reason || "-";
            return (
              <div key={c.type} style={{ display: "grid", gridTemplateColumns: "7px minmax(130px, 0.46fr) minmax(0, 1fr)", gap: 8, alignItems: "baseline", fontSize: 11 }}>
                <StatusDot color={condColor(c)} />
                <span style={{ fontWeight: 500, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={c.type}>{c.type}</span>
                <span style={{ color: "var(--color-text-secondary)", minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={conditionMessage}>{conditionMessage}</span>
              </div>
            );
          })}
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
        {detail.events.length === 0 ? (
          <span style={{ color: "var(--color-text-tertiary)" }}>No events for this node.</span>
        ) : (
          detail.events.map((e, i) => (
            <div key={i} style={{ display: "flex", gap: 6, alignItems: "baseline", fontSize: 11, color: e.type === "Warning" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>
              <span style={{ width: 50, fontSize: 9, textTransform: "uppercase", flexShrink: 0 }}>{e.type}</span>
              <span style={{ fontWeight: 500, width: 100, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", flexShrink: 0 }}>{e.reason}</span>
              <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", flex: 1 }} title={e.message}>{e.message}</span>
              {e.count > 1 && <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>×{e.count}</span>}
            </div>
          ))
        )}
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
