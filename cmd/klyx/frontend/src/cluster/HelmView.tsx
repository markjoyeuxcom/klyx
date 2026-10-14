import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { IconAnchor } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { HelmReleaseDTO, HelmHistoryEntryDTO } from "../store/fleet";
import { listHelmReleases, openHelmRelease, helmRollback, HELM_CLI_MISSING } from "../bridge/helm";
import { useListKeys } from "../chrome/useListKeys";
import { CopyButton } from "../chrome/CopyButton";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";
import { IN_FLIGHT, PENDING_DEADLINE_S, releaseRule, type Role } from "./helmHealth";
import { VirtualList, type VirtualListHandle } from "../chrome/VirtualList";
import { YamlBlock } from "../chrome/YamlBlock";
import { Age, UpdatedAt, formatAge, useNow } from "../chrome/Age";
import { InspectorStatus } from "./ResourceInspector";
import { VerdictLamp } from "../health/VerdictView";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";
import { loadClusterPrefs, loadSearch, saveHelmViewPrefs, saveSearch } from "../store/persist";
import { RollbackComposerDialog } from "./HelmRollbackDialog";
import { useInspectorAI } from "../ai/AIExplain";

// HelmView - Helm releases on the workspace template (spec §6 step 5). Helm is
// a bounded CLI snapshot (`helm list` on demand), never a live watch: the
// header names the snapshot time, a failed re-read keeps the last good
// snapshot marked as such, and a missing CLI or a failed read is never an
// empty list. Release inspection and rollback only - no chart catalogue, no
// install. Documented domain variant of the action band: History is the
// inspector's first section (the spec's Helm primary is history, which is
// content here, not a verb), so the primary action re-reads it; rollback to
// the last good revision is the safeguarded secondary and every other
// revision is in More. All rollbacks go through the composer dialog with its
// protected-cluster arming.

const RANK: Record<Role["kind"], number> = { fault: 0, degraded: 1, progressing: 2, unknown: 3, ok: 4 };
const rankOf = (role: Role) => RANK[role.kind] ?? RANK.unknown;
const inFlight = (r: HelmReleaseDTO) => IN_FLIGHT.has(r.status);

const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)",
  degraded: "var(--color-text-warning)",
  progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)",
  unknown: "var(--color-text-tertiary)",
};
const roleTone = (k: string) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";
function Lamp({ role }: { role: Role }) {
  return <span role="img" aria-label={role.label} style={{ display: "inline-flex", justifySelf: "center" }}><VerdictLamp health={role} size={8} /></span>;
}
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
const keyOf = (r: { namespace: string; name: string }) => `${r.namespace}/${r.name}`;

type HelmFilter = "all" | "failed" | "pending";

function matchesQuery(r: HelmReleaseDTO, q: string): boolean {
  if (!q) return true;
  return [r.name, r.namespace, r.chart, r.appVersion, r.status].some((v) => (v ?? "").toLowerCase().includes(q));
}

const COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "lamp", label: "", width: 16, priority: 0 },
  { id: "name", label: "Release", width: 240, priority: 0, flex: true },
  { id: "status", label: "Status", width: 170, priority: 1 },
  { id: "chart", label: "Chart", width: 180, priority: 2, grow: 0.6 },
  { id: "app", label: "App version", width: 96, priority: 3 },
  { id: "rev", label: "Rev", width: 44, priority: 2 },
  { id: "updated", label: "Updated", width: 76, priority: 1 },
];

export function HelmView({ cluster }: { cluster: string }) {
  const helm = useFleet((s) => s.helm);
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const nowS = Math.floor(useNow() / 1000);

  // namespace survives a relaunch, the query survives section switches
  const [filter, setFilter] = useState<HelmFilter>("all");
  const [namespace, setNamespace] = useState<string>(() => loadClusterPrefs(cluster).helm?.namespace ?? "");
  const [query, setQuery] = useState<string>(() => loadSearch(cluster, "helm"));
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<VirtualListHandle>(null);

  useEffect(() => {
    void listHelmReleases(cluster);
    return () => { useFleet.getState().clearHelm(); };
  }, [cluster]);
  useEffect(() => {
    saveHelmViewPrefs(cluster, { namespace });
    saveSearch(cluster, "helm", query);
  }, [cluster, namespace, query]);

  const mine = helm.cluster === cluster;
  const releases = useMemo(() => (mine ? helm.releases : []), [mine, helm.releases]);
  const snapshotAt = mine ? helm.snapshotAt ?? null : null;
  const listError = mine ? helm.error ?? null : null;
  const cliMissing = mine && !helm.loading && !helm.available && helm.message === HELM_CLI_MISSING;
  // evidence of the current release set: a successful snapshot is complete;
  // a failed re-read leaves the last good snapshot (stale); nothing read yet
  // is pending, and a failure with no prior snapshot is failed
  const evidence: "complete" | "stale" | "pending" | "failed" = snapshotAt != null ? (listError ? "stale" : "complete") : listError || cliMissing ? "failed" : "pending";

  // The verdict is the Go rule (internal/helmcli rules.go, 1c addendum §2) at
  // this second: its read-time value, or the step Go computed for a pending
  // release's deadline. rule.included is the lens's inclusion (failed or
  // appears stuck); role/label the presentation.
  const ruleOf = useCallback((r: HelmReleaseDTO) => releaseRule(r, nowS), [nowS]);
  const roleOf = useCallback((r: HelmReleaseDTO): Role => { const x = ruleOf(r); return { kind: x.role as Role["kind"], label: x.label }; }, [ruleOf]);
  const q = query.trim().toLowerCase();
  const ordered = useMemo(
    () => [...releases].sort((a, b) => rankOf(roleOf(a)) - rankOf(roleOf(b)) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [releases, roleOf],
  );
  const namespaces = useMemo(() => Array.from(new Set(releases.map((r) => r.namespace))).sort(), [releases]);
  const visible = useMemo(() => ordered.filter((r) => {
    if (namespace && r.namespace !== namespace) return false;
    if (filter === "failed" && ruleOf(r).role !== "fault") return false;
    if (filter === "pending" && !inFlight(r)) return false;
    return matchesQuery(r, q);
  }), [ordered, namespace, filter, q, ruleOf]);

  const failed = releases.filter((r) => ruleOf(r).role === "fault").length;
  const pending = releases.filter(inFlight).length;
  const stuck = releases.filter((r) => roleOf(r).kind === "degraded").length;
  const attention = releases.filter((r) => ruleOf(r).included).length;

  // selection: the store owns it (the palette opens releases too)
  const sel = mine ? helm.selected : null;
  const selKey = sel ? keyOf(sel) : null;
  const selected = selKey ? releases.find((r) => keyOf(r) === selKey) : undefined;
  const lastRelease = useRef<HelmReleaseDTO | null>(null);
  if (selected) lastRelease.current = selected;
  if (lastRelease.current && keyOf(lastRelease.current) !== selKey) lastRelease.current = null;
  // removed only when a complete snapshot no longer lists it
  const removed = !!selKey && evidence === "complete" && !selected;
  const select = useCallback((r: HelmReleaseDTO | null) => {
    if (r) void openHelmRelease(cluster, r.namespace, r.name);
    else useFleet.getState().selectHelmRelease(null);
  }, [cluster]);

  // Selection lifecycle. A newly requested selection (row, palette) stays
  // pending until its row arrives; the first time it is present, filters that
  // hide it are cleared once (direct navigation wins over stale filters).
  // After that, later operator edits win: a filter that excludes the selected
  // release closes its inspector.
  const navTarget = useRef<{ key: string; revealed: boolean } | null>(null);
  useEffect(() => {
    if (!selKey) { navTarget.current = null; return; }
    if (navTarget.current?.key !== selKey) navTarget.current = { key: selKey, revealed: false };
    if (!selected) return;
    if (!navTarget.current.revealed) {
      navTarget.current.revealed = true;
      setCursorKey(selKey);
      if (!visible.includes(selected)) { setFilter("all"); setNamespace(""); setQuery(""); }
      return;
    }
    if (!visible.includes(selected)) select(null);
  }, [selKey, selected, visible, select]);

  // one-shot auto-select of the first release needing attention, from the
  // first complete snapshot of this visit (never from a cached one)
  const autoSelect = useRef<"pending" | "done">("pending");
  const entrySnapshot = useRef<number | null | undefined>(undefined);
  if (entrySnapshot.current === undefined) entrySnapshot.current = snapshotAt;
  // the same component instance is reused across clusters: a new cluster is a
  // new visit with its own one-shot selection and no carried-over evidence
  const enteredCluster = useRef(cluster);
  if (enteredCluster.current !== cluster) {
    enteredCluster.current = cluster;
    autoSelect.current = "pending";
    entrySnapshot.current = useFleet.getState().helm.cluster === cluster ? useFleet.getState().helm.snapshotAt ?? null : null;
    navTarget.current = null;
    lastRelease.current = null;
  }
  useEffect(() => { setCursorKey(null); }, [cluster]);
  const freshRead = evidence === "complete" && snapshotAt !== entrySnapshot.current;
  useEffect(() => {
    if (autoSelect.current === "done" || !freshRead) return;
    autoSelect.current = "done";
    if (useFleet.getState().helm.selected) return;
    const r = visible.find((x) => ruleOf(x).included);
    if (r) { setCursorKey(keyOf(r)); select(r); }
  }, [freshRead, visible, ruleOf, select]);

  const cursorIdx = cursorKey === null ? -1 : visible.findIndex((r) => keyOf(r) === cursorKey);
  const close = () => { autoSelect.current = "done"; select(null); };
  useListKeys({
    count: visible.length,
    selected: cursorIdx,
    onSelect: (idx) => { const r = visible[idx]; if (r) { setCursorKey(keyOf(r)); listRef.current?.scrollToIndex(idx); } },
    onActivate: (idx) => { const r = visible[idx]; if (r) { setCursorKey(keyOf(r)); autoSelect.current = "done"; select(r); } },
    onEscape: close,
    searchRef,
  });

  const fitted = useMemo(() => fitColumns(COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const clearFilters = () => { setFilter("all"); setNamespace(""); setQuery(""); };
  let state: DatasetState = "ready";
  if (evidence === "failed" && releases.length === 0) state = "unreadable";
  else if (evidence === "pending" && releases.length === 0) state = "loading";
  else if (releases.length === 0) state = "empty";
  else if (visible.length === 0) state = "no-match";
  // "*" = the last good snapshot; never an unqualified count
  const marker = evidence === "stale" ? "*" : "";

  const stateView = state === "loading" ? <SkeletonRows rows={6} label="reading Helm releases" />
    : state === "unreadable" ? (cliMissing
      ? <EmptyState icon={<IconAnchor size={28} stroke={1.2} />} title="helm CLI not found." hint="Klyx reads releases with helm list. Install helm or set KLYX_HELM_PATH, then refresh." />
      : <EmptyState icon={<IconAnchor size={28} stroke={1.2} />} title="Could not read Helm releases." hint={listError ?? undefined} action={{ label: "Retry", onClick: () => void listHelmReleases(cluster) }} />)
    : state === "empty"
      ? <EmptyState icon={<IconAnchor size={28} stroke={1.2} />} title="No Helm releases on this cluster." hint="helm list across all namespaces found no deployed, failed, pending or uninstalling release. Releases installed by Flux helm-controller are Helm releases and appear here too." />
      : <EmptyState icon={<IconAnchor size={28} stroke={1.2} />} title="No releases match the current filters." hint={`${releases.length}${marker} releases on this cluster.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  const inspected = selected ?? (removed ? lastRelease.current : null);

  return (
    <LensLayout
      label="Helm"
      title="Helm releases"
      summary={state === "loading" || state === "unreadable" ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{releases.length}{marker}</span>
          {attention > 0
            ? <span className="t-status" style={{ color: failed > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{failed > 0 ? "▲ " : ""}{[failed ? `${failed} failed` : "", stuck ? `${stuck} pending, ${stuck === 1 ? "appears" : "appear"} stuck` : ""].filter(Boolean).join(" · ")}</span>
            : pending > 0 ? <span className="t-secondary">{pending} in progress</span>
            : <span className="t-secondary">{evidence === "complete" ? (releases.length > 0 ? "all deployed" : "") : "none known failed (last snapshot)"}</span>}
          {attention > 0 && pending - stuck > 0 && <span className="t-secondary">{pending - stuck} in progress</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary" title="Read with helm list on demand - not a live watch.">snapshot</span>
          <UpdatedAt at={snapshotAt} />
          <button className="l-btn" onClick={() => void listHelmReleases(cluster)} disabled={mine && helm.loading}>{mine && helm.loading ? "Reading…" : "Refresh"}</button>
        </>
      }
      controls={
        <>
          <label style={{ display: "flex" }}>
            <span className="sr-only">Namespace</span>
            <select className="l-input" value={namespace} onChange={(e) => setNamespace(e.target.value)}>
              <option value="">All namespaces</option>
              {namespaces.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
            </select>
          </label>
          <button className="l-chip" aria-pressed={filter === "all"} onClick={() => setFilter("all")}>All</button>
          <button className="l-chip" aria-pressed={filter === "failed"} onClick={() => setFilter("failed")}>Failed {failed > 0 && <span style={{ color: "var(--color-text-danger)" }}>{failed}</span>}</button>
          <button className="l-chip" aria-pressed={filter === "pending"} onClick={() => setFilter("pending")}>Pending {pending > 0 && <span style={{ color: stuck > 0 ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{pending}</span>}</button>
          <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
            <span className="sr-only">Search Helm releases</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search release, chart, version" />
          </label>
        </>
      }
      notice={listError && releases.length > 0 ? <WatchErrorBanner tool="Helm" failure="helm list failed - showing the last good snapshot" error={listError} /> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
          <div className="lens-table-head" role="row" style={gridStyle}>
            {COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
          </div>
          <VirtualList
            ref={listRef}
            items={visible}
            rowHeight={TABLE_ROW_HEIGHT}
            rowKey={(r) => keyOf(r)}
            style={{ flex: 1, minHeight: 0 }}
            render={(r) => {
              const k = keyOf(r);
              const role = roleOf(r);
              return (
                <div key={k} role="button" tabIndex={0} className="lens-row" style={gridStyle}
                  aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                  onClick={() => { setCursorKey(k); autoSelect.current = "done"; select(k === selKey ? null : r); }}
                  onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); select(r); } }}>
                  {show("lamp") && <Lamp role={role} />}
                  {show("name") && (
                    <span style={{ display: "flex", alignItems: "center", gap: 4, minWidth: 0 }}>
                      <span className="lens-cell-name" title={k}><span style={{ color: "var(--color-text-tertiary)" }}>{r.namespace} / </span><span style={{ fontWeight: k === selKey ? 600 : 400 }}>{r.name}</span></span>
                      <CopyButton text={r.name} label={`copy release name ${r.name}`} appearOnRowHover />
                    </span>
                  )}
                  {show("status") && <span style={{ ...cellStyle, color: ROLE_TEXT[role.kind], fontWeight: role.kind === "fault" ? 600 : 400 }}>{role.kind === "fault" ? "▲ " : ""}{role.label}</span>}
                  {show("chart") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{r.chart}</span>}
                  {show("app") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}>{r.appVersion || "-"}</span>}
                  {show("rev") && <span style={{ color: "var(--color-text-secondary)" }}>{r.revision}</span>}
                  {show("updated") && <span style={{ color: "var(--color-text-tertiary)" }}><Age unixSeconds={r.updatedUnix} /></span>}
                </div>
              );
            }}
          />
        </LensTable>
      }
      footer={<span className="t-legend">j k navigate · ↵ inspect · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length} of {releases.length}{marker} releases · broken first</span>}
      inspector={selKey && inspected ? (
        <ReleaseInspector
          key={`${cluster}/${selKey}`}
          cluster={cluster}
          release={inspected}
          role={roleOf(inspected)}
          removed={removed}
          stale={evidence === "stale"}
          history={helm.history}
          values={helm.values}
          detailLoading={helm.detailLoading}
          historyError={helm.historyError ?? null}
          valuesError={helm.valuesError ?? null}
          isProtected={isProtected}
          nowS={nowS}
          onClose={close}
        />
      ) : undefined}
      inspectorLabel="Helm release details"
      onCloseInspector={close}
    />
  );
}

function RemovedNotice() {
  return (
    <div role="status" className="t-body" style={{ padding: "8px 10px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-secondary)" }}>
      <span className="t-status">Release removed.</span> The latest helm list snapshot no longer has it (uninstalled). The details below are its last evidence; actions are disabled.
    </div>
  );
}

// lastGood is the newest earlier revision that deployed successfully (helm
// marks a replaced good revision "superseded"; failed ones stay "failed").
function lastGood(history: HelmHistoryEntryDTO[], current: number): HelmHistoryEntryDTO | undefined {
  return [...history].filter((e) => e.revision < current && (e.status === "superseded" || e.status === "deployed")).sort((a, b) => b.revision - a.revision)[0];
}

// verdictDetail is the diagnosis in words - never tooltip-only. A failed or
// in-flight release's current revision description is helm's own account of
// what happened ("Upgrade "x" failed: context deadline exceeded").
// Pending wording is qualified: the snapshot shows the status and its age,
// never whether a helm process is still running.
function verdictDetail(r: HelmReleaseDTO, role: Role, good: HelmHistoryEntryDTO | undefined, nowS: number): string | null {
  const age = r.updatedUnix > 0 ? formatAge(nowS - r.updatedUnix) : null;
  if (role.kind === "degraded") {
    const way = good
      ? `If none is, rolling back to rev ${good.revision} (the last good revision) clears the pending state.`
      : "There is no earlier good revision to roll back to.";
    return `${r.status} at the last snapshot, last updated ${age} ago - past Klyx's ${PENDING_DEADLINE_S / 60}m attention threshold (twice helm's default 5m timeout). Elapsed time does not prove the operation stopped: check that no helm operation is still running (a CI job, another terminal, a controller). While it is pending, helm refuses new operations on this release. ${way}`;
  }
  if (role.kind === "progressing") return `${r.status} at the last snapshot${age ? `, updated ${age} ago` : " (update time unknown)"} - an operation was in flight when the snapshot was read.`;
  return null;
}

function ReleaseInspector({ cluster, release: r, role, removed, stale, history, values, detailLoading, historyError, valuesError, isProtected, nowS, onClose }: {
  cluster: string;
  release: HelmReleaseDTO;
  role: Role;
  removed: boolean;
  stale: boolean;
  history: HelmHistoryEntryDTO[];
  values: string;
  detailLoading: boolean;
  historyError: string | null;
  valuesError: string | null;
  isProtected: boolean;
  nowS: number;
  onClose: () => void;
}) {
  const ai = useInspectorAI({ kind: "helm" as const, namespace: r.namespace, name: r.name }, `HelmRelease/${r.namespace}/${r.name}`, !removed, cluster);
  // The pending rollback pins its full evidence at open (target and current
  // revision), and is cancelled the moment the release is removed - a later
  // snapshot restoring the name never revives the old confirmation.
  const [pendingRollback, setPendingRollback] = useState<{ revision: number; target?: HelmHistoryEntryDTO; current?: HelmHistoryEntryDTO } | null>(null);
  const live = !removed;
  useEffect(() => { if (removed) setPendingRollback(null); }, [removed]);
  const removedRef = useRef(removed);
  removedRef.current = removed;
  const currentRev = history.length > 0 ? Math.max(...history.map((e) => e.revision)) : r.revision;
  const current = history.find((e) => e.revision === currentRev);
  const good = lastGood(history, currentRev);
  const earlier = [...history].filter((e) => e.revision < currentRev).sort((a, b) => b.revision - a.revision);
  const detail = verdictDetail(r, role, good, nowS);
  const setRollbackTo = (revision: number) => setPendingRollback({ revision, target: history.find((e) => e.revision === revision), current });
  const reread = () => void openHelmRelease(cluster, r.namespace, r.name);

  const more: MenuItem[] = live && earlier.length > 0
    ? [{ kind: "choose", label: "Roll back to revision", danger: true, options: earlier.map((e) => ({ label: `rev ${e.revision} · ${e.status} · ${e.chart}`, onSelect: () => setRollbackTo(e.revision) })) }]
    : [];

  return (
    <div data-testid="helm-release-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout
        kind="Helm release"
        namespace={r.namespace}
        name={r.name}
        meta={[r.chart, r.appVersion ? `app ${r.appVersion}` : "", `rev ${r.revision}`].filter(Boolean).join(" · ")}
        onClose={onClose}
        verdict={removed ? <RemovedNotice /> : (
          <div>
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} reason={stale ? "last snapshot" : undefined} />
            {detail && <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: -4 }}>{detail}</div>}
            {(role.kind === "fault" || role.kind === "degraded" || role.kind === "progressing") && (
              current?.description
                ? <div className="t-body" style={{ color: ROLE_TEXT[role.kind], overflowWrap: "anywhere", marginTop: detail ? 4 : -4 }}>{current.description}</div>
                : detailLoading ? <div className="t-secondary" style={{ marginTop: -4 }}>Reading the release history for helm's account of it…</div>
                : historyError ? <div className="t-secondary" style={{ marginTop: -4 }}>helm's account is unavailable: the history read failed.</div>
                : null
            )}
          </div>
        )}
        primary={live ? { label: detailLoading ? "Reading…" : "Re-read history", onSelect: reread } : undefined}
        secondary={live && good ? { label: `Roll back to rev ${good.revision}…`, onSelect: () => setRollbackTo(good.revision) } : undefined}
        more={[...(more), ...ai.menu]}
      >
        {ai.block}
        <InspectorBlock title="History" count={history.length || undefined}>
          {historyError ? (
            <div className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>History unreadable: {historyError}</div>
          ) : detailLoading && history.length === 0 ? (
            <SkeletonRows rows={3} label="reading history" />
          ) : history.length === 0 ? (
            <span className="t-secondary">{removed ? "No history was read before it was removed." : "helm history returned no revisions."}</span>
          ) : (
            <ol style={{ listStyle: "none", margin: 0, padding: 0, display: "grid", gap: 6 }}>
              {[...history].sort((a, b) => b.revision - a.revision).map((e) => {
                const hRole = e.revision === currentRev ? role : e.status === "failed" ? { kind: "fault" as const, label: "failed" } : { kind: "unknown" as const, label: e.status };
                return (
                  <li key={e.revision} style={{ display: "grid", gap: 2, paddingBottom: 6, borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
                    <div className="t-body" style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "baseline" }}>
                      <span style={{ fontFamily: "var(--font-mono)", fontWeight: e.revision === currentRev ? 600 : 400 }}>rev {e.revision}</span>
                      {/* earlier failures are history: muted, they never glow */}
                      <span style={{ color: e.revision === currentRev ? ROLE_TEXT[hRole.kind] : "var(--color-text-tertiary)" }}>{e.status}{e.revision === currentRev ? " · current" : ""}</span>
                      <span className="t-secondary">{e.chart}{e.appVersion ? ` (${e.appVersion})` : ""}</span>
                      <span className="t-secondary" style={{ marginLeft: "auto" }}><Age unixSeconds={e.updatedUnix} suffix=" ago" /></span>
                    </div>
                    {e.description && <div className="t-secondary" style={{ overflowWrap: "anywhere" }}>{e.description}</div>}
                  </li>
                );
              })}
            </ol>
          )}
        </InspectorBlock>
        <InspectorBlock title="Values">
          <div className="t-secondary" style={{ marginBottom: 6 }}>User-supplied values; secret-shaped values are masked. The chart source in Git holds the full set.</div>
          {valuesError ? (
            <div className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Values unreadable: {valuesError}</div>
          ) : detailLoading && values === "" ? (
            <SkeletonRows rows={2} label="reading values" />
          ) : values === "" ? (
            <span className="t-secondary">No user-supplied values.</span>
          ) : (
            <YamlBlock text={values} copyLabel="release values" maxHeight={400} />
          )}
        </InspectorBlock>
      </InspectorLayout>
      {pendingRollback && live && (
        <RollbackComposerDialog
          cluster={cluster}
          namespace={r.namespace}
          name={r.name}
          current={pendingRollback.current}
          target={pendingRollback.target}
          targetRevision={pendingRollback.revision}
          isProtected={isProtected}
          onCancel={() => setPendingRollback(null)}
          onConfirm={(opts) => {
            const p = pendingRollback;
            setPendingRollback(null);
            if (removedRef.current) return; // never dispatch against removed evidence
            void helmRollback(cluster, r.namespace, r.name, p.revision, opts);
          }}
        />
      )}
    </div>
  );
}

export { PENDING_DEADLINE_S };
