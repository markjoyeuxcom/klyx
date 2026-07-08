import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useFleet } from "../store/fleet";
import type { DependencyRefDTO, EventRowDTO, FluxResourceDTO, FluxSourceDTO, InventoryEntryDTO, ResourceDetailDTO } from "../store/fleet";
import { openGitOps, closeGitOps, getResourceDetail, reconcile, reconcileWithSource, setSuspend, resolveGitLink, fluxDiff } from "../bridge/gitops";
import type { FluxDiffDTO } from "../bridge/gitops";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import { MetaTile, OperationDialog, OptionTile } from "../chrome/OperationDialog";
import { useListKeys } from "../chrome/useListKeys";
import { FKeysBar } from "../chrome/FKeysBar";
import { useNavIntent } from "../chrome/useNavIntent";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { IconGitBranch } from "@tabler/icons-react";
import { wellKnownResourceRef } from "./resourceRefs";
import { inputWell, MetersStrip, well, Badge, FilterButton, StatusDot, readyColor, ellipsis, actionBtn, type Tone } from "../chrome/primitives";
import { compactCount } from "../chrome/format";
import { InspectorConditions, InspectorSection, InspectorStatus, Kbd } from "./ResourceInspector";
import { SyncingChip, staleStyle } from "./staleness";

type FluxFilter = "all" | "attention" | "suspended" | "kustomizations" | "helmreleases" | "sources";

const keyOf = (r: { kind: string; namespace: string; name: string }) => `${r.kind}/${r.namespace}/${r.name}`;

const sourceKindLabel: Record<string, string> = {
  GitRepository: "git",
  OCIRepository: "oci",
  Bucket: "bucket",
  HelmRepository: "helmrepo",
  HelmChart: "chart",
};

function shortRev(rev: string): string {
  if (!rev) return "";
  const s = rev.replace(/^refs\/heads\//, "");
  const at = s.indexOf("@");
  if (at < 0) return s;
  const branch = s.slice(0, at);
  const sha = s.slice(at + 1).replace(/^sha1:/, "").replace(/^sha256:/, "");
  return `${branch}@${sha.slice(0, 7)}`;
}

function ago(sec: number): string {
  if (sec <= 0) return "";
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  return `${Math.floor(sec / 3600)}h ago`;
}

function needsAttention(r: FluxResourceDTO): boolean {
  // Suspended is an intended paused state, never a fault - it must not drive the
  // attention badge/meter/filter (matches gitops_summary.go and argo.Broken()).
  // It stays visible via its own suspended count/filter and warning color.
  return !r.suspended && r.ready !== "Ready";
}

function rank(r: FluxResourceDTO): number {
  if (r.ready === "Failed") return 0;
  if (r.ready === "Reconciling") return 1;
  if (r.suspended) return 2;
  if (r.ready !== "Ready") return 3;
  return 4;
}

function srcRank(s: FluxSourceDTO): number {
  if (s.ready === "Failed") return 0;
  if (s.ready === "Reconciling") return 1;
  if (s.suspended) return 2;
  if (s.ready !== "Ready") return 3;
  return 4;
}

function statusText(r: FluxResourceDTO): string {
  return r.suspended ? "suspended" : r.ready.toLowerCase();
}

function sourceText(r: FluxResourceDTO): string {
  return [r.sourceKind, r.sourceName].filter(Boolean).join(" ");
}

function matchesFilter(r: FluxResourceDTO, filter: FluxFilter): boolean {
  if (filter === "attention") return needsAttention(r);
  if (filter === "suspended") return r.suspended;
  if (filter === "kustomizations") return r.kind === "Kustomization";
  if (filter === "helmreleases") return r.kind === "HelmRelease";
  return true;
}

function matchesQuery(r: FluxResourceDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [
    r.kind,
    r.namespace,
    r.name,
    r.message,
    r.revision,
    r.sourceKind,
    r.sourceName,
  ].some((v) => v.toLowerCase().includes(q));
}

export function GitOps({ cluster }: { cluster: string }) {
  const tier = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.gitopsTier ?? "Unknown");
  const gitops = useFleet((s) => s.gitops);
  const expand = useFleet((s) => s.expand);
  const collapse = useFleet((s) => s.collapse);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  // suspend/resume stay on ConfirmDialog (no options to compose); reconcile
  // opens the composer dialog where re-fetching the source is a per-run choice.
  const [pending, setPending] = useState<null | { verb: "suspend" | "resume"; r: FluxResourceDTO }>(null);
  const [reconcileTarget, setReconcileTarget] = useState<FluxResourceDTO | null>(null);
  const [filter, setFilter] = useState<FluxFilter>("all");
  const [query, setQuery] = useState("");
  const [kbKey, setKbKey] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const absent = tier === "Absent";

  useEffect(() => {
    if (absent) return;
    // If cleanup runs before openGitOps resolves, invoke the late-arriving
    // unsubscriber immediately - otherwise its listener leaks for the app's
    // lifetime and duplicates store writes on every reopen.
    let cancelled = false;
    let off = () => {};
    openGitOps(cluster).then((u) => (cancelled ? u() : (off = u))).catch((e) => console.error("openGitOps", e));
    return () => {
      cancelled = true;
      off();
      void closeGitOps(cluster);
    };
  }, [cluster, absent]);

  const rows = gitops.cluster === cluster ? gitops.resources : [];

  // Palette verbs / cross-links land here as one-shot intents once the watch
  // has synced. Reconcile opens the composer; suspend/resume their confirm.
  useNavIntent(cluster, ["reconcile-flux", "suspend-flux", "select-flux"], (intent) => {
    if (gitops.loading || rows.length === 0) return "wait";
    if (!("fluxKind" in intent)) return "missing";
    const r = rows.find((x) => x.kind === intent.fluxKind && x.namespace === intent.namespace && x.name === intent.name);
    if (!r) return "missing";
    expand(keyOf(r));
    void getResourceDetail(cluster, r.kind, r.namespace, r.name);
    if (intent.kind === "reconcile-flux") setReconcileTarget(r);
    else if (intent.kind === "suspend-flux") setPending({ verb: intent.suspend ? "suspend" : "resume", r });
    return "consumed";
  }, [rows, gitops.loading]);

  const orderedRows = useMemo(
    () => [...rows].sort((a, b) => rank(a) - rank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [rows],
  );
  const visibleRows = useMemo(
    () => orderedRows.filter((r) => matchesFilter(r, filter) && matchesQuery(r, query)),
    [orderedRows, filter, query],
  );
  const byKey = useMemo(() => {
    const m = new Map<string, FluxResourceDTO>();
    for (const r of rows) m.set(keyOf(r), r);
    return m;
  }, [rows]);
  const allSources = gitops.cluster === cluster ? (gitops.sources ?? []) : [];
  const orderedSources = useMemo(
    () => [...allSources].sort((a, b) => srcRank(a) - srcRank(b) || a.kind.localeCompare(b.kind) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [allSources],
  );

  // Keyboard nav (j/k/Enter/Esc, "/" focuses the filter) over the visible
  // rows. Selection is keyed by keyOf: when the selected resource leaves the
  // visible set (filter change) this resolves to -1 — no selection — instead
  // of silently pointing at whatever row took its index.
  const kbIdx = kbKey === null ? -1 : visibleRows.findIndex((r) => keyOf(r) === kbKey);

  const handleSelect = useCallback((idx: number) => {
    const r = visibleRows[idx];
    if (r) setKbKey(keyOf(r));
  }, [visibleRows]);

  const handleActivate = useCallback((idx: number) => {
    const r = visibleRows[idx];
    if (!r) return;
    const k = keyOf(r);
    if (gitops.expandedKey === k) collapse();
    else expand(k);
  }, [visibleRows, gitops.expandedKey, collapse, expand]);

  const handleEscape = useCallback(() => {
    if (gitops.expandedKey) collapse();
  }, [gitops.expandedKey, collapse]);

  // r / s drive the SAME dialogs the panel buttons open (ReconcileDialog and
  // the suspend/resume ConfirmDialog) - the keys never bypass a confirm flow.
  // useListKeys keeps them inert while no row is keyboard-selected.
  const handleReconcileKey = useCallback((idx: number) => {
    const r = visibleRows[idx];
    if (r) setReconcileTarget(r);
  }, [visibleRows]);

  const handleSuspendKey = useCallback((idx: number) => {
    const r = visibleRows[idx];
    if (r) setPending({ verb: r.suspended ? "resume" : "suspend", r });
  }, [visibleRows]);

  useListKeys({
    // The sources filter renders a different list; suspend row nav there.
    count: filter === "sources" ? 0 : visibleRows.length,
    selected: kbIdx,
    onSelect: handleSelect,
    onActivate: handleActivate,
    onEscape: handleEscape,
    searchRef,
    extraKeys: { r: handleReconcileKey, s: handleSuspendKey },
  });

  useEffect(() => {
    if (!gitops.expandedKey) return;
    const r = rows.find((x) => keyOf(x) === gitops.expandedKey);
    if (r) void getResourceDetail(cluster, r.kind, r.namespace, r.name);
  }, [cluster, gitops.expandedKey, rows]);

  if (absent) {
    return <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>No Flux or Argo installed on this cluster.</div>;
  }

  const ks = rows.filter((r) => r.kind === "Kustomization").length;
  const hr = rows.filter((r) => r.kind === "HelmRelease").length;
  const ready = rows.filter((r) => r.ready === "Ready" && !r.suspended).length;
  const attention = rows.filter(needsAttention).length;
  const suspended = rows.filter((r) => r.suspended).length;
  const reconciling = rows.filter((r) => r.ready === "Reconciling").length;
  const selectedResource = visibleRows.find((r) => keyOf(r) === gitops.expandedKey) ?? null;
  const selectedDetail = selectedResource && gitops.detail && keyOf(gitops.detail) === keyOf(selectedResource) ? gitops.detail : null;

  return (
    <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 12, height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 16 }}>
        <div>
          <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 3 }}>Flux</div>
          <div style={{ fontSize: 18, fontWeight: 600 }}>Reconciliation</div>
        </div>
        <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
          {isProtected && <Badge tone="warning" variant="filled">prd lock</Badge>}
          <Badge tone={attention > 0 ? "warning" : "success"} variant="filled">{attention > 0 ? `${attention} need attention` : "ready"}</Badge>
        </div>
      </div>

      {gitops.cluster === cluster && gitops.watchError && (
        <WatchErrorBanner tool="Flux" error={gitops.watchError} />
      )}

      <MetersStrip
        note="broken first"
        meters={[
          { label: "kustomizations", value: ks },
          { label: "helmreleases", value: hr },
          attention > 0 && { label: "attention", value: attention, tone: "danger" as const },
          reconciling > 0 && { label: "reconciling", value: reconciling, tone: "info" as const },
          suspended > 0 && { label: "suspended", value: suspended, tone: "warning" as const },
          { label: "ready", value: ready, tone: "success" as const },
        ]}
      />

      <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center" }}>
        <FilterButton label="all" active={filter === "all"} count={rows.length} onClick={() => setFilter("all")} />
        <FilterButton label="needs attention" active={filter === "attention"} count={attention} onClick={() => setFilter("attention")} />
        <FilterButton label="suspended" active={filter === "suspended"} count={suspended} onClick={() => setFilter("suspended")} />
        <FilterButton label="ks" active={filter === "kustomizations"} count={ks} onClick={() => setFilter("kustomizations")} />
        <FilterButton label="hr" active={filter === "helmreleases"} count={hr} onClick={() => setFilter("helmreleases")} />
        <FilterButton label="sources" active={filter === "sources"} count={allSources.length} onClick={() => setFilter("sources")} />
        <input
          ref={searchRef}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="filter flux"
          aria-label="filter flux"
          style={{ ...inputWell, marginLeft: "auto", width: 170 }}
        />
        {visibleRows.length !== rows.length && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{visibleRows.length} of {rows.length}</span>}
      </div>

      {gitops.loading && rows.length === 0 && allSources.length === 0 ? (
        <SkeletonRows rows={7} label="loading reconciliation state" />
      ) : filter === "sources" ? (
        <SourcesPanel sources={orderedSources} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<IconGitBranch size={28} stroke={1.2} />}
          title="No Flux resources on this cluster."
          hint="Flux is installed but no Kustomizations or HelmReleases exist yet."
        />
      ) : (
        <div style={{ flex: 1, minHeight: 0, display: "grid", gridTemplateColumns: "minmax(440px, 1.25fr) minmax(320px, 0.75fr)", gap: 12, alignItems: "stretch", overflow: "hidden" }}>
          <div style={{ ...well, overflow: "hidden", minHeight: 0, display: "flex", flexDirection: "column" }}>
            <div style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.2fr) 120px minmax(110px,0.9fr) 84px 92px", gap: 10, alignItems: "center", padding: "5px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)", fontSize: 10, color: "var(--color-text-tertiary)", flexShrink: 0 }}>
              <span />
              <span>resource</span>
              <span>source</span>
              <span>revision</span>
              <span>since</span>
              <span>status</span>
            </div>
            <div data-testid="flux-resource-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {visibleRows.length === 0 ? (
                <EmptyState
                  title="No Flux resources match the current filter."
                  action={{ label: "Clear filters", onClick: () => { setFilter("all"); setQuery(""); } }}
                />
              ) : visibleRows.map((r, i) => {
                const k = keyOf(r);
                const open = gitops.expandedKey === k;
                return (
                  <RowSummary
                    key={k}
                    r={r}
                    open={open}
                    kbSelected={i === kbIdx}
                    onClick={() => {
                      // A click also moves the keyboard selection so the r/s
                      // hints on the panel act on the row the panel shows.
                      setKbKey(k);
                      if (open) collapse(); else expand(k);
                    }}
                  />
                );
              })}
            </div>
            <FKeysBar
              keys={[["j k", "nav"], ["\u21B5", "expand"], ["r", "reconcile"], ["s", "suspend"], ["/", "filter"], ["esc", "collapse"]]}
              status={`${visibleRows.length} of ${rows.length} \u00B7 broken first`}
            />
          </div>

          <div data-testid="flux-inspector-scroll" style={{ minHeight: 0, overflowY: "auto" }}>
            {selectedResource ? (
              <DetailPanel
                key={keyOf(selectedResource)}
                cluster={cluster}
                resource={selectedResource}
                detail={selectedDetail}
                deps={byKey}
                onReconcile={() => setReconcileTarget(selectedResource)}
                onToggleSuspend={(isSuspended) => setPending({ verb: isSuspended ? "resume" : "suspend", r: selectedResource })}
                onViewGit={() => void resolveGitLink(cluster, selectedResource.kind, selectedResource.namespace, selectedResource.name)}
              />
            ) : (
              <EmptyInspector attention={attention} />
            )}
          </div>
        </div>
      )}

      {pending && (
        <ConfirmDialog
          title={pending.verb === "suspend" ? "Suspend reconciliation" : "Resume reconciliation"}
          cluster={cluster}
          detail={`${pending.r.kind} ${pending.r.namespace}/${pending.r.name}`}
          protected={isProtected}
          danger={pending.verb === "suspend"}
          confirmLabel={pending.verb === "suspend" ? "Suspend" : "Resume"}
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const { verb, r } = pending;
            setPending(null);
            void setSuspend(cluster, r.kind, r.namespace, r.name, verb === "suspend");
          }}
        />
      )}

      {reconcileTarget && (
        <ReconcileDialog
          cluster={cluster}
          resource={reconcileTarget}
          isProtected={isProtected}
          onCancel={() => setReconcileTarget(null)}
          onConfirm={(withSource) => {
            const r = reconcileTarget;
            setReconcileTarget(null);
            if (withSource) void reconcileWithSource(cluster, r.kind, r.namespace, r.name);
            else void reconcile(cluster, r.kind, r.namespace, r.name);
          }}
        />
      )}
    </div>
  );
}

// ReconcileDialog is the one reconcile verb: the former separate "reconcile
// with source" action is now a per-run option here, because whether the
// source needs a re-fetch is a property of this reconcile, not a different
// operation.
function ReconcileDialog({ cluster, resource, isProtected, onCancel, onConfirm }: {
  cluster: string;
  resource: FluxResourceDTO;
  isProtected: boolean;
  onCancel: () => void;
  onConfirm: (withSource: boolean) => void;
}) {
  const [withSource, setWithSource] = useState(false);
  return (
    <OperationDialog
      title={`Reconcile "${resource.namespace}/${resource.name}"`}
      ariaLabel={`Reconcile ${resource.namespace}/${resource.name}`}
      subtitle="Annotates the resource so Flux reconciles it now instead of waiting for the next interval."
      cluster={cluster}
      protected={isProtected}
      confirmLabel="Reconcile"
      onCancel={onCancel}
      onConfirm={() => onConfirm(withSource)}
    >
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <MetaTile label="resource" value={`${resource.kind} ${resource.namespace}/${resource.name}`} />
        <MetaTile label="bound source" value={sourceText(resource) || "—"} />
      </div>
      <OptionTile
        active={withSource}
        title="Re-fetch source first"
        body="Also annotate the bound source so Flux pulls a fresh artifact before reconciling; use when the source itself is stale."
        onClick={() => setWithSource((v) => !v)}
      />
    </OperationDialog>
  );
}

function RowSummary({ r, open, kbSelected, onClick }: { r: FluxResourceDTO; open: boolean; kbSelected: boolean; onClick: () => void }) {
  const isBad = r.ready === "Failed";
  const isReconciling = r.ready === "Reconciling";
  const color = r.suspended ? "var(--color-text-warning)" : (readyColor[r.ready] ?? "var(--color-text-tertiary)");
  // Keep the kb-selected row visible - the list scrolls, there is no
  // VirtualList handle here. Optional call: jsdom has no scrollIntoView.
  const ref = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (kbSelected) ref.current?.scrollIntoView?.({ block: "nearest" });
  }, [kbSelected]);
  return (
    <button
      ref={ref}
      aria-selected={kbSelected}
      onClick={onClick}
      style={{
        width: "100%",
        display: "grid",
        gridTemplateColumns: "3px minmax(180px,1.2fr) 120px minmax(110px,0.9fr) 84px 92px",
        gap: 10,
        alignItems: "center",
        padding: "8px 12px",
        border: 0,
        borderTop: "0.5px solid var(--color-border-tertiary)",
        fontSize: 12,
        cursor: "pointer",
        textAlign: "left",
        color: "var(--color-text-primary)",
        background: kbSelected || open ? "var(--color-background-secondary)" : "transparent",
        boxShadow: kbSelected ? "inset 2px 0 0 var(--bench-selection-edge, var(--color-text-info))" : undefined,
        ...staleStyle(r.stale),
      }}
    >
      <span style={{ alignSelf: "stretch", background: isBad ? "var(--color-text-danger)" : isReconciling ? "var(--color-text-info)" : r.suspended ? "var(--color-text-warning)" : "transparent" }} />
      <div style={{ minWidth: 0 }}>
        <span style={{ fontFamily: "var(--font-mono)" }}>{r.namespace}/{r.name}</span>{" "}
        <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>{r.kind === "Kustomization" ? "ks" : "hr"}</span>
        {needsAttention(r) && r.reason && <ReasonChip reason={r.reason} bad={isBad} />}
        {r.message && needsAttention(r) && (
          <div style={{ color: isBad ? "var(--color-text-danger)" : "var(--color-text-warning)", fontSize: 11, marginTop: 2, ...ellipsis }} title={r.message}>{r.message}</div>
        )}
      </div>
      <div style={{ color: "var(--color-text-secondary)", ...ellipsis }} title={sourceText(r)}>{sourceText(r) || "—"}</div>
      <div style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)", ...ellipsis }} title={r.revision}>{shortRev(r.revision) || "—"}</div>
      <div style={{ color: "var(--color-text-tertiary)", fontSize: 11, ...ellipsis }}>{ago(r.readySinceSeconds) || "—"}</div>
      <div className={!r.stale && r.ready === "Failed" ? "klyx-danger-prefix" : undefined} style={{ ...ellipsis, color, fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: !r.stale && r.ready === "Failed" ? 700 : undefined }}>
        {r.stale ? <SyncingChip generation={r.generation} observedGeneration={r.observedGeneration} /> : statusText(r)}
      </div>
    </button>
  );
}

function SourcesPanel({ sources }: { sources: FluxSourceDTO[] }) {
  if (sources.length === 0) {
    return <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No Flux sources found.</div>;
  }
  return (
    <div style={{ flex: 1, minHeight: 0, ...well, overflow: "hidden", display: "flex", flexDirection: "column" }}>
      <div style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.4fr) 90px minmax(120px,1fr) 92px", gap: 10, alignItems: "center", padding: "5px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)", fontSize: 10, color: "var(--color-text-tertiary)", flexShrink: 0 }}>
        <span />
        <span>source</span>
        <span>kind</span>
        <span>revision</span>
        <span>status</span>
      </div>
      <div data-testid="flux-sources-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
        {sources.map((s) => <SourceRow key={keyOf(s)} s={s} />)}
      </div>
    </div>
  );
}

function SourceRow({ s }: { s: FluxSourceDTO }) {
  const isBad = s.ready === "Failed";
  const isReconciling = s.ready === "Reconciling";
  const color = s.suspended ? "var(--color-text-warning)" : (readyColor[s.ready] ?? "var(--color-text-tertiary)");
  const notReady = s.ready !== "Ready";
  return (
    <div style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.4fr) 90px minmax(120px,1fr) 92px", gap: 10, alignItems: "center", padding: "8px 12px", borderTop: "0.5px solid var(--color-border-tertiary)", fontSize: 12 }}>
      <span style={{ alignSelf: "stretch", background: isBad ? "var(--color-text-danger)" : isReconciling ? "var(--color-text-info)" : s.suspended ? "var(--color-text-warning)" : "transparent" }} />
      <div style={{ minWidth: 0 }}>
        <span style={{ fontFamily: "var(--font-mono)" }}>{s.namespace}/{s.name}</span>
        {s.message && notReady && (
          <div style={{ color: isBad ? "var(--color-text-danger)" : "var(--color-text-warning)", fontSize: 11, marginTop: 2, ...ellipsis }} title={s.message}>{s.message}</div>
        )}
      </div>
      <div style={{ color: "var(--color-text-tertiary)", fontSize: 10, fontFamily: "var(--font-mono)" }}>{sourceKindLabel[s.kind] ?? s.kind}</div>
      <div style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)", ...ellipsis }} title={s.revision}>{shortRev(s.revision) || "—"}</div>
      <div style={{ ...ellipsis, color }}>{s.suspended ? "suspended" : s.ready.toLowerCase()}</div>
    </div>
  );
}

function DetailPanel({ cluster, resource, detail, deps, onReconcile, onToggleSuspend, onViewGit }: {
  cluster: string;
  resource: FluxResourceDTO;
  detail: ResourceDetailDTO | null;
  deps: Map<string, FluxResourceDTO>;
  onReconcile: () => void;
  onToggleSuspend: (suspended: boolean) => void;
  onViewGit: () => void;
}) {
  if (!detail) {
    return (
      <div style={{ ...well, padding: "10px 12px", fontSize: 12, color: "var(--color-text-secondary)" }}>
        <InspectorHeader resource={resource} />
        <div style={{ marginTop: 12 }}>Loading detail…</div>
      </div>
    );
  }
  // M10-f: a real diff only helps when Flux isn't auto-healing (suspended) or is
  // stuck (apply-failing); a healthy resource's diff is empty/misleading.
  const canDiff = resource.kind === "Kustomization" && (detail.suspended || resource.ready === "Failed");
  return (
    <div style={{ ...well, padding: "10px 12px", fontSize: 12 }}>
      <InspectorHeader resource={resource} />
      <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 12, flexWrap: "wrap" }}>
        {/* r / s are live via useListKeys on the keyboard-selected row */}
        <button onClick={onReconcile} style={{ ...actionBtn, display: "inline-flex", alignItems: "center", gap: 5 }}>reconcile <Kbd k="r" /></button>
        <button onClick={() => onToggleSuspend(detail.suspended ?? false)} style={{ ...actionBtn, display: "inline-flex", alignItems: "center", gap: 5 }}>
          {detail.suspended ? "resume" : "suspend"} <Kbd k="s" />
        </button>
        {resource.kind === "Kustomization" && (
          <button onClick={onViewGit} style={actionBtn}>view in git</button>
        )}
        {detail.suspended && (
          <span style={{ color: "var(--color-text-warning)", fontSize: 11, fontWeight: 500 }}>suspended</span>
        )}
      </div>
      {detail.source && <SourceSection source={detail.source} />}
      {detail.dependsOn && detail.dependsOn.length > 0 && (
        <DependsOnSection deps={detail.dependsOn} kind={resource.kind} resolve={deps} blocked={detail.reason === "DependencyNotReady"} />
      )}
      {detail.events && <EventsSection events={detail.events} />}
      {detail.applyFailed && (
        <div style={{ color: "var(--color-text-danger)", marginBottom: 8 }}>apply failed at <span style={{ fontFamily: "var(--font-mono)" }}>{shortRev(detail.attemptedRevision)}</span></div>
      )}
      <InspectorSection title="Conditions">
        <InspectorConditions conditions={detail.conditions} />
      </InspectorSection>
      {resource.kind === "Kustomization" ? (
        <InspectorSection title="Inventory" count={detail.inventory.length}>
          {detail.inventory.length === 0 ? <Muted>no managed objects</Muted> : <InventoryList entries={detail.inventory} />}
        </InspectorSection>
      ) : (
        <InspectorSection title="Inventory"><Muted>no inventory in the HelmRelease CR</Muted></InspectorSection>
      )}
      {canDiff && <DiffSection cluster={cluster} namespace={resource.namespace} name={resource.name} />}
    </div>
  );
}

// Inventory entries carry no health in the Kustomization CR (group/kind/ns/name
// only), so the list truncates without claiming "all healthy" and the dots stay
// unlit - a health suffix or green lamp here would be fabricated.
const INVENTORY_SHOWN = 8;

function InventoryList({ entries }: { entries: InventoryEntryDTO[] }) {
  const shown = entries.slice(0, INVENTORY_SHOWN);
  const hidden = entries.length - shown.length;
  return (
    <>
      {shown.map((e) => {
        const key = `${e.kind}/${e.namespace}/${e.name}`;
        const name = `${e.namespace ? `${e.namespace}/` : ""}${e.name}`;
        // Argo's app resources walk to the live object; Flux's inventory
        // does the same for every kind Klyx can route to.
        const ref = wellKnownResourceRef(e.group, e.kind);
        return (
          <div key={key} style={{ display: "flex", gap: 7, alignItems: "center", minWidth: 0 }}>
            <StatusDot size={6} color="var(--color-text-tertiary)" />
            <span style={{ color: "var(--color-text-tertiary)", fontSize: 11, flexShrink: 0 }}>{e.kind}</span>
            {ref ? (
              <button
                onClick={() => {
                  const nav = useFleet.getState();
                  nav.openResource(ref);
                  nav.openInstance(ref.scope === "Cluster" ? "" : e.namespace, e.name);
                }}
                title={`open ${e.kind} ${name}`}
                style={{
                  background: "transparent", border: "none", padding: 0, cursor: "pointer",
                  textAlign: "left", fontFamily: "var(--font-mono)", fontSize: "inherit", minWidth: 0,
                  color: "var(--color-text-info)", ...ellipsis,
                }}
              >
                {name}
              </button>
            ) : (
              <span style={{ fontFamily: "var(--font-mono)", color: "var(--color-text-secondary)", ...ellipsis }}>{name}</span>
            )}
          </div>
        );
      })}
      {hidden > 0 && (
        <div style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-tertiary)" }}>+{hidden} more</div>
      )}
    </>
  );
}

function DiffSection({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }) {
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<FluxDiffDTO | null>(null);
  const run = () => {
    setRunning(true);
    fluxDiff(cluster, namespace, name, "")
      .then((r) => setResult(r))
      .catch((e) => setResult({ available: true, hasChanges: false, output: "", error: String(e) }))
      .finally(() => setRunning(false));
  };
  return (
    <InspectorSection title="Live diff (flux diff)">
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <button onClick={run} disabled={running} style={{ ...actionBtn, opacity: running ? 0.6 : 1 }}>
          {running ? "Computing…" : "Compute diff"}
        </button>
        <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>shells out to <span style={{ fontFamily: "var(--font-mono)" }}>flux diff</span> with your local credentials</span>
      </div>
      {result && result.error && (
        <div style={{ color: "var(--color-text-danger)", marginTop: 6, ...ellipsis }} title={result.error}>{result.error}</div>
      )}
      {result && !result.error && !result.hasChanges && (
        <div style={{ color: "var(--color-text-success)", marginTop: 6 }}>no changes — live matches Git</div>
      )}
      {result && result.hasChanges && (
        <pre style={{ marginTop: 6, padding: 8, maxHeight: 280, overflow: "auto", background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, fontFamily: "var(--font-mono)", fontSize: 11, whiteSpace: "pre", lineHeight: 1.4 }}>{result.output}</pre>
      )}
    </InspectorSection>
  );
}

function SourceSection({ source }: { source: FluxSourceDTO }) {
  // A suspended source is paused, not failed: its Ready condition goes stale by
  // design, so it must not surface the red "source not ready" root-cause headline
  // (the status dot already shows paused). Suspended-is-not-a-fault invariant.
  const ready = source.suspended || source.ready === "Ready";
  const dot = source.suspended ? "var(--color-text-warning)" : (readyColor[source.ready] ?? "var(--color-text-tertiary)");
  const label = sourceKindLabel[source.kind] ?? source.kind;
  return (
    <InspectorSection title="Source">
      {!ready && (
        <div style={{ color: "var(--color-text-danger)", fontWeight: 500, marginBottom: 4 }}>
          source not ready{source.reason ? `: ${source.reason}` : ""}
        </div>
      )}
      <div style={{ display: "grid", gridTemplateColumns: "9px minmax(0,1fr)", gap: 8, alignItems: "baseline" }}>
        <StatusDot color={dot} />
        <div style={{ minWidth: 0 }}>
          <span style={{ fontFamily: "var(--font-mono)" }}>{label} · {source.namespace}/{source.name}</span>
          {source.revision && (
            <span style={{ color: "var(--color-text-tertiary)", fontSize: 11, marginLeft: 6, fontFamily: "var(--font-mono)" }}>{shortRev(source.revision)}</span>
          )}
          {source.message && !ready && (
            <div style={{ color: "var(--color-text-secondary)", fontSize: 11, marginTop: 2, ...ellipsis }} title={source.message}>{source.message}</div>
          )}
        </div>
      </div>
    </InspectorSection>
  );
}

function DependsOnSection({ deps, kind, resolve, blocked }: {
  deps: DependencyRefDTO[];
  kind: string;
  resolve: Map<string, FluxResourceDTO>;
  blocked: boolean;
}) {
  // Dependencies are implicitly the same kind as the resource.
  const stateOf = (d: DependencyRefDTO): string | null => resolve.get(`${kind}/${d.namespace}/${d.name}`)?.ready ?? null;
  const blocker = blocked ? deps.find((d) => stateOf(d) !== "Ready") : undefined;
  return (
    <InspectorSection title="Depends on">
      {blocker && (
        <div style={{ color: "var(--color-text-danger)", fontWeight: 500, marginBottom: 4 }}>
          blocked by <span style={{ fontFamily: "var(--font-mono)" }}>{blocker.namespace}/{blocker.name}</span>
        </div>
      )}
      {deps.map((d) => {
        const state = stateOf(d);
        const dot = state ? (readyColor[state] ?? "var(--color-text-tertiary)") : "var(--color-text-tertiary)";
        return (
          <div key={`${d.namespace}/${d.name}`} style={{ display: "grid", gridTemplateColumns: "9px minmax(0,1fr) auto", gap: 8, alignItems: "baseline" }}>
            <StatusDot color={dot} />
            <span style={{ fontFamily: "var(--font-mono)", ...ellipsis }}>{d.namespace}/{d.name}</span>
            <span style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>{state ? state.toLowerCase() : "not found"}</span>
          </div>
        );
      })}
    </InspectorSection>
  );
}

function isDriftEvent(e: EventRowDTO): boolean {
  const s = `${e.reason} ${e.message}`.toLowerCase();
  return s.includes("drift") || s.includes("configured");
}

// EventsSection mirrors the InspectorEvents evline (dot, reason, ellipsized
// message, ×count, age right) but cannot reuse it: EventRowDTO ages are unix
// (not ISO) and drift events carry the M10 drift chip, which the shared
// component has no slot for.
function EventsSection({ events }: { events: EventRowDTO[] }) {
  const nowSec = Math.floor(Date.now() / 1000);
  return (
    <InspectorSection title="Drift / events">
      {events.length === 0 ? <Muted>no recent events</Muted> : events.map((e, i) => {
        const warn = e.type === "Warning";
        const drift = isDriftEvent(e);
        const dot = warn ? "var(--color-text-danger)" : drift ? "var(--color-text-info)" : "var(--color-text-tertiary)";
        const sec = e.lastSeenUnix > 0 ? Math.max(0, nowSec - e.lastSeenUnix) : 0;
        return (
          <div key={i} style={{ display: "flex", gap: 6, alignItems: "baseline", fontSize: 11, padding: "1px 0", color: warn ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>
            <span style={{ width: 7, height: 7, borderRadius: "50%", background: dot, display: "inline-block", flexShrink: 0, alignSelf: "center" }} />
            <span style={{ fontWeight: 500, flexShrink: 0, maxWidth: 130, ...ellipsis }} title={e.reason}>{e.reason}</span>
            {drift && <span style={{ fontFamily: "var(--font-mono)", fontSize: 9, padding: "1px 5px", borderRadius: 3, border: "0.5px solid var(--color-border-info)", color: "var(--color-text-info)", background: "var(--color-background-info)", flexShrink: 0, alignSelf: "center" }}>drift</span>}
            <span style={{ ...ellipsis, flex: 1, minWidth: 0 }} title={e.message}>{e.message}</span>
            {e.count > 1 && <span title={`${e.count.toLocaleString()} occurrences`} style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>×{compactCount(e.count)}</span>}
            <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{ago(sec) || "now"}</span>
          </div>
        );
      })}
    </InspectorSection>
  );
}

// fluxTone maps readyColor semantics onto the verdict tone: Failed=danger,
// Ready=success, Reconciling=info, suspended=warning (an intended pause).
function fluxTone(r: FluxResourceDTO): Tone {
  if (r.suspended) return "warning";
  if (r.ready === "Failed") return "danger";
  if (r.ready === "Ready") return "success";
  if (r.ready === "Reconciling") return "info";
  return "muted";
}

function InspectorHeader({ resource }: { resource: FluxResourceDTO }) {
  return (
    <div style={{ minWidth: 0 }}>
      <div style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>{resource.kind} / {resource.namespace}</div>
      <div style={{ display: "flex", gap: 8, alignItems: "center", minWidth: 0, marginTop: 2, marginBottom: 10 }}>
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 15, fontWeight: 600, ...ellipsis }}>{resource.name}</span>
        <CopyButton text={resource.name} label={`copy name ${resource.name}`} />
      </div>
      <InspectorStatus
        tone={fluxTone(resource)}
        label={resource.suspended ? "Suspended" : resource.ready}
        reason={needsAttention(resource) ? resource.reason || undefined : undefined}
        detail={shortRev(resource.revision) || undefined}
      />
      {/* The controller's message hangs directly under the verdict (the -6
          eats half the verdict's built-in 12px margin). */}
      {resource.message && needsAttention(resource) && (
        <div
          style={{ color: resource.ready === "Failed" ? "var(--color-text-danger)" : "var(--color-text-warning)", fontSize: 11, marginTop: -6, marginBottom: 10, ...ellipsis }}
          title={resource.message}
        >{resource.message}</div>
      )}
    </div>
  );
}

function ReasonChip({ reason, bad }: { reason: string; bad: boolean }) {
  const tone = bad ? "danger" : "warning";
  return (
    <span
      title={reason}
      style={{
        fontFamily: "var(--font-mono)",
        fontSize: 9,
        padding: "1px 5px",
        borderRadius: 3,
        marginLeft: 6,
        verticalAlign: "middle",
        border: `0.5px solid var(--color-border-${tone})`,
        color: `var(--color-text-${tone})`,
        background: `var(--color-background-${tone})`,
        whiteSpace: "nowrap",
      }}
    >
      {reason}
    </span>
  );
}

function EmptyInspector({ attention }: { attention: number }) {
  return (
    <div style={{ ...well, padding: "10px 12px", fontSize: 12, color: "var(--color-text-secondary)" }}>
      <div style={{ color: attention > 0 ? "var(--color-text-warning)" : "var(--color-text-success)", fontFamily: "var(--font-mono)", fontWeight: 600 }}>
        {attention > 0 ? `${attention} resources need attention` : "all reconciliations ready"}
      </div>
      <div style={{ marginTop: 8 }}>Select a Flux resource to inspect conditions, inventory, and day-2 actions.</div>
    </div>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <span style={{ color: "var(--color-text-tertiary)" }}>{children}</span>;
}
