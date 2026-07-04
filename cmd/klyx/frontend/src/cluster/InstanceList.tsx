import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useFleet, ResourceRef, InstanceDTO } from "../store/fleet";
import { loadInstances, listInstancePage } from "../bridge/crd";
import { VirtualList } from "../chrome/VirtualList";
import type { VirtualListHandle } from "../chrome/VirtualList";
import { useListKeys } from "../chrome/useListKeys";
import { CopyButton } from "../chrome/CopyButton";
import { Age } from "../chrome/Age";
import { EmptyState, SkeletonRows } from "../chrome/primitives";
import { IconStack2 } from "@tabler/icons-react";
import { riskFor, supportsRiskFilter } from "./resourceRisk";

const ellipsis: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" };

// Fixed row height (px, incl. border) — must match the VirtualList rowHeight so
// the windowing math lines up with the real DOM rows. Fleet scope merges first
// pages from every connected cluster, so the list can reach thousands of rows.
const ROW_HEIGHT = 28;

type ExtraColumn = { key: string; label: string; width: string };
type BrowserScope = "cluster" | "fleet";
type FleetInstanceRow = InstanceDTO & { cluster: string };
type FleetInstanceState = { rows: FleetInstanceRow[]; loading: boolean; errors: string[] };

export function InstanceList({ cluster, resource }: { cluster: string; resource: ResourceRef }) {
  const instances = useFleet((s) => s.instances);
  const clusters = useFleet((s) => s.clusters);
  const setFilter = useFleet((s) => s.setInstanceFilter);
  const setRiskOnly = useFleet((s) => s.setInstanceRiskOnly);
  const openInstance = useFleet((s) => s.openInstance);
  const closeResource = useFleet((s) => s.closeResource);
  const [scope, setScope] = useState<BrowserScope>("cluster");
  const [fleetClusterFilter, setFleetClusterFilter] = useState("");
  const [fleetState, setFleetState] = useState<FleetInstanceState>({ rows: [], loading: false, errors: [] });

  useEffect(() => {
    void loadInstances(cluster, resource);
    return () => useFleet.getState().clearInstances();
  }, [cluster, resource.group, resource.version, resource.plural]);

  const fleetTargetKey = useMemo(() => {
    const connected = clusters.filter((c) => c.state !== "Failed" && c.state !== "Unconnected").map((c) => c.name);
    return (connected.length > 0 ? connected : [cluster]).join("\n");
  }, [clusters, cluster]);
  const fleetTargets = useMemo(() => fleetTargetKey.split("\n").filter(Boolean), [fleetTargetKey]);

  useEffect(() => {
    if (fleetClusterFilter && !fleetTargets.includes(fleetClusterFilter)) setFleetClusterFilter("");
  }, [fleetClusterFilter, fleetTargets]);

  useEffect(() => {
    if (scope !== "fleet") return;
    let cancelled = false;
    setFleetState({ rows: [], loading: true, errors: [] });

    void Promise.all(fleetTargets.map(async (target) => {
      try {
        const page = await listInstancePage(target, resource);
        return { target, rows: page.items.map((row) => ({ ...row, cluster: target })), error: "" };
      } catch {
        return { target, rows: [] as FleetInstanceRow[], error: target };
      }
    })).then((results) => {
      if (cancelled) return;
      setFleetState({
        rows: results.flatMap((r) => r.rows),
        loading: false,
        errors: results.filter((r) => r.error).map((r) => r.error),
      });
    });

    return () => { cancelled = true; };
  }, [scope, fleetTargets, resource.group, resource.version, resource.plural]);

  const namespaced = resource.scope === "Namespaced";
  const extraColumns = columnsFor(resource);
  const fleetMode = scope === "fleet";
  const cols = [
    fleetMode ? "minmax(110px, 0.7fr)" : null,
    namespaced ? "minmax(110px, 0.7fr)" : null,
    "minmax(150px, 1.1fr)",
    ...extraColumns.map((c) => c.width),
    "70px",
  ].filter(Boolean).join(" ");
  const tableMinWidth = extraColumns.length > 0
    ? `${Math.max(760, (fleetMode ? 120 : 0) + (namespaced ? 320 : 220) + extraColumns.length * 130)}px`
    : undefined;

  const isCurrent = instances.ref && instances.ref.group === resource.group && instances.ref.plural === resource.plural;
  const fleetRows = fleetClusterFilter
    ? fleetState.rows.filter((row) => row.cluster === fleetClusterFilter)
    : fleetState.rows;
  const all: FleetInstanceRow[] = fleetMode
    ? fleetRows
    : (isCurrent ? instances.rows.map((row) => ({ ...row, cluster })) : []);
  const fleetCounts = fleetState.rows.reduce<Record<string, number>>((acc, row) => {
    acc[row.cluster] = (acc[row.cluster] ?? 0) + 1;
    return acc;
  }, {});
  const q = instances.filter.toLowerCase();
  const riskSupported = supportsRiskFilter(resource);
  const rowsWithRisk = all.map((row) => ({ row, risk: riskFor(resource, row) }));
  const attentionCount = rowsWithRisk.filter((r) => r.risk.bad).length;
  const rows = rowsWithRisk
    .filter(({ row, risk }) => {
      if (instances.riskOnly && !risk.bad) return false;
      if (!q) return true;
      return row.name.toLowerCase().includes(q) ||
        row.cluster.toLowerCase().includes(q) ||
        row.namespace.toLowerCase().includes(q) ||
        risk.reason.toLowerCase().includes(q) ||
        Object.values(row.fields ?? {}).some((v) => v.toLowerCase().includes(q));
    })
    .sort((a, b) => {
      if (a.risk.bad !== b.risk.bad) return a.risk.bad ? -1 : 1;
      if (a.row.cluster !== b.row.cluster) return a.row.cluster.localeCompare(b.row.cluster);
      return a.row.namespace === b.row.namespace
        ? a.row.name.localeCompare(b.row.name)
        : a.row.namespace.localeCompare(b.row.namespace);
    });
  const loading = fleetMode ? fleetState.loading : instances.loading;
  const loadErrors = fleetMode ? fleetState.errors : [];

  function openRow(r: FleetInstanceRow) {
    if (!fleetMode) {
      openInstance(r.namespace, r.name);
      return;
    }
    const nav = useFleet.getState();
    const route = nav.route;
    const section = route.name === "cluster" && route.section === "crds" ? "crds" : "resources";
    nav.openCluster(r.cluster);
    nav.setSection(section);
    nav.openResource(resource);
    nav.openInstance(r.namespace, r.name);
  }

  // Keyboard parity for the generic table backing every Resources/CRDs list:
  // j/k moves the selection, Enter opens the instance, / focuses the filter.
  const [kbIdx, setKbIdx] = useState(-1);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);
  const effectiveKbIdx = rows.length === 0 ? -1 : Math.min(kbIdx, rows.length - 1);
  const handleKbSelect = useCallback((idx: number) => {
    setKbIdx(idx);
    listRef.current?.scrollToIndex(idx);
  }, []);
  const handleKbActivate = useCallback((idx: number) => {
    const r = rows[idx];
    if (r) openRow(r.row);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows]);
  useListKeys({
    count: rows.length,
    selected: effectiveKbIdx,
    onSelect: handleKbSelect,
    onActivate: handleKbActivate,
    searchRef,
  });

  return (
    <div style={{ padding: "14px 16px", height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box", display: "flex", flexDirection: "column" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12, flexShrink: 0 }}>
        <button
          aria-label="back to resources"
          onClick={closeResource}
          style={{ ...linkBtn, color: "var(--color-text-info)" }}
        >
          {resource.scope === "Cluster" ? "cluster resources" : "resources"}
        </button>
        <span style={{ color: "var(--color-text-tertiary)" }}>/</span>
        <div style={{ fontSize: 15, fontWeight: 500 }}>{resource.kind}</div>
        <span style={{ background: "var(--color-background-secondary)", color: "var(--color-text-secondary)", fontSize: 9, padding: "1px 6px", borderRadius: 3 }}>{resource.scope.toLowerCase()}</span>
        <span style={{ fontSize: 12, color: "var(--color-text-tertiary)" }}>{rows.length === all.length ? `${all.length} loaded` : `${rows.length} of ${all.length}`}</span>
        <div role="group" aria-label="resource browsing scope" style={{ display: "inline-flex", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, overflow: "hidden", marginLeft: 4 }}>
          <button
            onClick={() => setScope("cluster")}
            style={scopeButton(scope === "cluster")}
          >
            cluster
          </button>
          <button
            onClick={() => setScope("fleet")}
            style={scopeButton(scope === "fleet")}
          >
            fleet
          </button>
        </div>
        {loadErrors.length > 0 && <span style={{ fontSize: 11, color: "var(--color-text-warning)" }}>{loadErrors.length} cluster{loadErrors.length === 1 ? "" : "s"} unreadable</span>}
        <div style={{ flex: 1 }} />
        {riskSupported && (
          <button
            onClick={() => setRiskOnly(!instances.riskOnly)}
            style={{
              ...btn,
              color: instances.riskOnly ? "var(--color-text-warning)" : "var(--color-text-secondary)",
              background: instances.riskOnly ? "var(--color-background-warning)" : "var(--color-background-primary)",
              border: instances.riskOnly ? "0.5px solid var(--color-border-warning)" : "0.5px solid var(--color-border-tertiary)",
            }}
          >
            needs attention {attentionCount}
          </button>
        )}
        <input
          ref={searchRef}
          value={instances.filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder={fleetMode ? "cluster, name, namespace…" : "name, namespace…"}
          style={{ width: 200, height: 28, paddingLeft: 10, fontSize: 12, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" }}
        />
      </div>

      {fleetMode && fleetTargets.length > 1 && (
        <div role="tablist" aria-label="fleet cluster scope" style={{ display: "flex", flexWrap: "wrap", gap: 6, margin: "-4px 0 10px", flexShrink: 0 }}>
          <button
            role="tab"
            aria-selected={!fleetClusterFilter}
            onClick={() => setFleetClusterFilter("")}
            style={clusterTab(!fleetClusterFilter)}
          >
            all <span style={tabCount}>{fleetState.rows.length}</span>
          </button>
          {fleetTargets.map((target) => (
            <button
              key={target}
              role="tab"
              aria-selected={fleetClusterFilter === target}
              onClick={() => setFleetClusterFilter(target)}
              style={clusterTab(fleetClusterFilter === target)}
            >
              {target} <span style={tabCount}>{fleetCounts[target] ?? 0}</span>
            </button>
          ))}
        </div>
      )}

      {loading && all.length === 0 ? (
        <SkeletonRows rows={8} label={`loading ${resource.kind} instances`} />
      ) : all.length === 0 ? (
        <EmptyState
          icon={<IconStack2 size={28} stroke={1.2} />}
          title={`No ${resource.kind} instances found${fleetMode ? " across the fleet" : ""}.`}
        />
      ) : (
        <div data-testid="instance-list-scroll" style={{ border: "0.5px solid var(--color-border-tertiary)", borderRadius: "var(--border-radius-md)", overflowY: "auto", overflowX: "auto", flex: 1, minHeight: 0, display: "flex", flexDirection: "column" }}>
          {/* minWidth wrapper: header + rows share it, so horizontal scrolling
              (on the outer div) moves them together while the VirtualList owns
              vertical scrolling for the windowed rows. */}
          <div style={{ minWidth: tableMinWidth, flex: 1, minHeight: 0, display: "flex", flexDirection: "column" }}>
            <div
              data-testid="instance-list-header"
              style={{ display: "grid", gridTemplateColumns: cols, gap: 10, padding: "6px 12px", background: "var(--color-background-secondary)", fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", position: "sticky", top: 0, zIndex: 1, flexShrink: 0, borderBottom: "0.5px solid var(--color-border-tertiary)" }}
            >
              {fleetMode && <span>cluster</span>}
              {namespaced && <span>namespace</span>}
              <span>name</span>
              {extraColumns.map((c) => <span key={c.key}>{c.label}</span>)}
              <span>age</span>
            </div>
            {rows.length === 0 ? (
              <EmptyState
                title={`No ${resource.kind} instances match the current filter.`}
                action={{ label: "Clear filters", onClick: () => { setFilter(""); setRiskOnly(false); } }}
              />
            ) : (
              <VirtualList
                ref={listRef}
                items={rows}
                rowHeight={ROW_HEIGHT}
                rowKey={({ row }) => `${row.cluster}/${row.namespace}/${row.name}`}
                style={{ flex: 1, minHeight: 0 }}
                render={({ row: r, risk }, i) => (
                  <div
                    key={`${r.cluster}/${r.namespace}/${r.name}`}
                    data-rowhost="true"
                    aria-selected={i === effectiveKbIdx}
                    onClick={() => openRow(r)}
                    title={risk.bad ? risk.reason : undefined}
                    style={{
                      display: "grid", gridTemplateColumns: cols, gap: 10, alignItems: "center", padding: "6px 12px",
                      height: ROW_HEIGHT, boxSizing: "border-box",
                      borderTop: "0.5px solid var(--color-border-tertiary)", fontSize: 11, cursor: "pointer",
                      background: i === effectiveKbIdx ? "var(--color-background-secondary)" : undefined,
                      boxShadow: i === effectiveKbIdx ? "inset 2px 0 0 var(--color-text-info)" : risk.bad ? "inset 2px 0 0 var(--color-text-warning)" : undefined,
                    }}
                  >
                    {fleetMode && <span style={{ color: "var(--color-text-info)", fontFamily: "var(--font-mono)", ...ellipsis }}>{r.cluster}</span>}
                    {namespaced && <span style={{ color: "var(--color-text-secondary)", ...ellipsis }}>{r.namespace}</span>}
                    <span style={{ display: "flex", alignItems: "center", gap: 2, minWidth: 0 }}>
                      <span style={{ fontFamily: "var(--font-mono)", ...ellipsis }}>{r.name}</span>
                      <CopyButton text={r.name} label={`copy name ${r.name}`} size={11} appearOnRowHover />
                    </span>
                    {extraColumns.map((c) => {
                      const value = r.fields?.[c.key] || "-";
                      return <span key={c.key} style={{ color: "var(--color-text-secondary)", fontFamily: "var(--font-mono)", ...ellipsis }} title={value}>{value}</span>;
                    })}
                    <span style={{ color: "var(--color-text-tertiary)" }}><Age iso={r.created} fallback="" /></span>
                  </div>
                )}
              />
            )}
          </div>
        </div>
      )}

      {!fleetMode && isCurrent && instances.nextToken && (
        <button
          onClick={() => void loadInstances(cluster, resource, instances.nextToken)}
          style={{ marginTop: 10, padding: "5px 12px", fontSize: 12, borderRadius: 4, cursor: "pointer", border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", alignSelf: "flex-start", flexShrink: 0 }}
        >
          Load more
        </button>
      )}
    </div>
  );
}

function scopeButton(active: boolean): React.CSSProperties {
  return {
    border: 0,
    borderRight: active ? 0 : "0.5px solid var(--color-border-tertiary)",
    padding: "3px 10px",
    fontSize: 11,
    cursor: "pointer",
    background: active ? "var(--color-background-info)" : "var(--color-background-primary)",
    color: active ? "var(--color-text-info)" : "var(--color-text-secondary)",
  };
}

function clusterTab(active: boolean): React.CSSProperties {
  return {
    padding: "4px 10px",
    borderRadius: 4,
    border: active ? "0.5px solid var(--color-border-info)" : "0.5px solid var(--color-border-tertiary)",
    background: active ? "var(--color-background-info)" : "var(--color-background-primary)",
    color: active ? "var(--color-text-info)" : "var(--color-text-secondary)",
    cursor: "pointer",
    fontSize: 11,
    fontFamily: "var(--font-mono)",
  };
}

const tabCount: React.CSSProperties = {
  color: "var(--color-text-tertiary)",
  marginLeft: 4,
};

const btn: React.CSSProperties = {
  padding: "3px 10px", fontSize: 11, borderRadius: 4, cursor: "pointer",
  border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)",
};

function columnsFor(resource: ResourceRef): ExtraColumn[] {
  if (resource.group === "" && resource.version === "v1" && resource.plural === "services") {
    return [
      { key: "type", label: "type", width: "86px" },
      { key: "clusterIP", label: "cluster ip", width: "120px" },
      { key: "externalIP", label: "external ip", width: "minmax(130px, 0.9fr)" },
      { key: "ports", label: "ports", width: "minmax(180px, 1.2fr)" },
    ];
  }
  if (resource.group === "discovery.k8s.io" && resource.version === "v1" && resource.plural === "endpointslices") {
    return [
      { key: "service", label: "service", width: "minmax(130px, 0.8fr)" },
      { key: "addressType", label: "addr", width: "74px" },
      { key: "endpoints", label: "ready", width: "70px" },
      { key: "addresses", label: "addresses", width: "minmax(170px, 1fr)" },
      { key: "ports", label: "ports", width: "minmax(130px, 0.8fr)" },
    ];
  }
  if (resource.group === "" && resource.version === "v1" && resource.plural === "persistentvolumeclaims") {
    return [
      { key: "status", label: "status", width: "86px" },
      { key: "class", label: "class", width: "minmax(110px, 0.8fr)" },
      { key: "size", label: "size", width: "82px" },
      { key: "modes", label: "modes", width: "86px" },
      { key: "volume", label: "volume", width: "minmax(150px, 1fr)" },
    ];
  }
  if (resource.group === "" && resource.version === "v1" && resource.plural === "persistentvolumes") {
    return [
      { key: "status", label: "status", width: "86px" },
      { key: "class", label: "class", width: "minmax(110px, 0.8fr)" },
      { key: "size", label: "size", width: "82px" },
      { key: "modes", label: "modes", width: "86px" },
      { key: "claim", label: "claim", width: "minmax(150px, 1fr)" },
      { key: "reclaim", label: "reclaim", width: "94px" },
    ];
  }
  if (resource.group === "networking.k8s.io" && resource.version === "v1" && resource.plural === "ingresses") {
    return [
      { key: "class", label: "class", width: "minmax(110px, 0.7fr)" },
      { key: "hosts", label: "hosts", width: "minmax(170px, 1fr)" },
      { key: "address", label: "address", width: "minmax(130px, 0.8fr)" },
      { key: "tls", label: "tls", width: "minmax(100px, 0.6fr)" },
      { key: "backends", label: "backends", width: "minmax(160px, 1fr)" },
    ];
  }
  if (resource.group === "autoscaling" && resource.version === "v2" && resource.plural === "horizontalpodautoscalers") {
    return [
      { key: "target", label: "target", width: "minmax(150px, 1fr)" },
      { key: "replicas", label: "min/current/desired/max", width: "160px" },
      { key: "metrics", label: "metrics", width: "minmax(190px, 1.2fr)" },
    ];
  }
  if (resource.group === "policy" && resource.version === "v1" && resource.plural === "poddisruptionbudgets") {
    return [
      { key: "allowed", label: "allowed", width: "80px" },
      { key: "healthy", label: "healthy", width: "90px" },
      { key: "expected", label: "expected", width: "90px" },
    ];
  }
  if (resource.group === "batch" && resource.version === "v1" && resource.plural === "jobs") {
    return [
      { key: "active", label: "active", width: "72px" },
      { key: "succeeded", label: "succeeded", width: "86px" },
      { key: "failed", label: "failed", width: "72px" },
      { key: "completions", label: "complete", width: "90px" },
    ];
  }
  if (resource.group === "batch" && resource.version === "v1" && resource.plural === "cronjobs") {
    return [
      { key: "schedule", label: "schedule", width: "minmax(130px, 0.8fr)" },
      { key: "suspended", label: "suspend", width: "74px" },
      { key: "active", label: "active", width: "70px" },
      { key: "lastSchedule", label: "last", width: "128px" },
      { key: "lastSucceeded", label: "success", width: "128px" },
    ];
  }
  if (resource.group === "" && resource.version === "v1" && resource.plural === "secrets") {
    return [
      { key: "type", label: "type", width: "minmax(170px, 1fr)" },
      { key: "keys", label: "keys", width: "70px" },
      { key: "immutable", label: "immutable", width: "90px" },
    ];
  }
  if (resource.group === "" && resource.version === "v1" && resource.plural === "configmaps") {
    return [
      { key: "keys", label: "keys", width: "70px" },
      { key: "immutable", label: "immutable", width: "90px" },
    ];
  }
  if (resource.group === "cert-manager.io" && resource.plural === "certificates") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "issuer", label: "issuer", width: "minmax(140px, 0.9fr)" },
      { key: "expires", label: "expires", width: "100px" },
      { key: "renew", label: "renew", width: "100px" },
      { key: "dns", label: "dns", width: "minmax(180px, 1.2fr)" },
    ];
  }
  if (resource.group === "cert-manager.io" && resource.plural === "certificaterequests") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "issuer", label: "issuer", width: "minmax(140px, 0.9fr)" },
      { key: "approved", label: "approved", width: "90px" },
      { key: "denied", label: "denied", width: "80px" },
      { key: "duration", label: "duration", width: "90px" },
    ];
  }
  if (resource.group === "cert-manager.io" && (resource.plural === "issuers" || resource.plural === "clusterissuers")) {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "type", label: "type", width: "100px" },
      { key: "server", label: "server", width: "minmax(200px, 1.2fr)" },
    ];
  }
  if (resource.group === "external-secrets.io" && resource.plural === "externalsecrets") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "store", label: "store", width: "minmax(140px, 0.9fr)" },
      { key: "target", label: "target secret", width: "minmax(140px, 0.9fr)" },
      { key: "refresh", label: "refresh", width: "90px" },
      { key: "synced", label: "synced", width: "108px" },
    ];
  }
  if (resource.group === "external-secrets.io" && (resource.plural === "secretstores" || resource.plural === "clustersecretstores")) {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "provider", label: "provider", width: "120px" },
      { key: "controller", label: "controller", width: "minmax(140px, 0.9fr)" },
    ];
  }
  if (resource.group === "helm.toolkit.fluxcd.io" && resource.plural === "helmreleases") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "suspended", label: "suspend", width: "78px" },
      { key: "chart", label: "chart", width: "minmax(150px, 1fr)" },
      { key: "source", label: "source", width: "minmax(150px, 1fr)" },
      { key: "revision", label: "revision", width: "minmax(160px, 1fr)" },
    ];
  }
  if (resource.group === "kustomize.toolkit.fluxcd.io" && resource.plural === "kustomizations") {
    return [
      { key: "ready", label: "ready", width: "90px" },
      { key: "suspended", label: "suspend", width: "78px" },
      { key: "source", label: "source", width: "minmax(150px, 1fr)" },
      { key: "path", label: "path", width: "minmax(140px, 0.9fr)" },
      { key: "revision", label: "revision", width: "minmax(160px, 1fr)" },
    ];
  }
  if (resource.group === "networking.k8s.io" && resource.version === "v1" && resource.plural === "networkpolicies") {
    return [
      { key: "selector", label: "selector", width: "minmax(170px, 1fr)" },
      { key: "policyTypes", label: "types", width: "110px" },
      { key: "ingress", label: "ingress", width: "76px" },
      { key: "egress", label: "egress", width: "76px" },
    ];
  }
  if (resource.group === "cilium.io" && (resource.plural === "ciliumnetworkpolicies" || resource.plural === "ciliumclusterwidenetworkpolicies")) {
    return [
      { key: "selector", label: "selector", width: "minmax(170px, 1fr)" },
      { key: "ingress", label: "ingress", width: "90px" },
      { key: "egress", label: "egress", width: "90px" },
      { key: "scope", label: "scope", width: "90px" },
    ];
  }
  return [];
}

const linkBtn: React.CSSProperties = {
  background: "transparent",
  border: 0,
  padding: 0,
  cursor: "pointer",
  fontSize: 12,
};
