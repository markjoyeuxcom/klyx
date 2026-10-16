import { useEffect } from "react";
import { IconAlertTriangle, IconChevronRight, IconComponents } from "@tabler/icons-react";
import { useFleet, CRDGroupDTO, CRDKindDTO, CRDGroupBy, crdCountKey } from "../store/fleet";
import { listCRDs, countKind } from "../bridge/crd";
import { EmptyState, SkeletonRows } from "../chrome/primitives";
import { LensLayout, LensTable, type DatasetState } from "../layout/LensLayout";
import { WatchErrorBanner } from "../chrome/WatchErrorBanner";

type FlatKind = CRDKindDTO & { group: string; category: string };

function flatten(groups: CRDGroupDTO[]): FlatKind[] {
  return groups.flatMap((g) => g.kinds.map((k) => ({ ...k, group: g.group, category: g.category })));
}

// reshape turns the api-group groups into display sections per the groupBy mode.
function reshape(groups: CRDGroupDTO[], groupBy: CRDGroupBy): { label: string; category: string; kinds: FlatKind[] }[] {
  if (groupBy === "group") {
    return groups.map((g) => ({ label: g.group, category: g.category, kinds: g.kinds.map((k) => ({ ...k, group: g.group, category: g.category })) }));
  }
  const flat = flatten(groups);
  if (groupBy === "alphabetical") {
    return [{ label: "all kinds", category: "", kinds: [...flat].sort((a, b) => a.kind.localeCompare(b.kind)) }];
  }
  const keyOf = (k: FlatKind) => (groupBy === "scope" ? (k.scope || "unknown") : (k.operator || "unattributed"));
  const buckets = new Map<string, FlatKind[]>();
  for (const k of flat) {
    const key = keyOf(k);
    const arr = buckets.get(key) ?? [];
    arr.push(k);
    buckets.set(key, arr);
  }
  return [...buckets.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([label, kinds]) => ({ label, category: "", kinds: kinds.sort((a, b) => a.kind.localeCompare(b.kind)) }));
}

function matches(k: FlatKind, q: string): boolean {
  if (!q) return true;
  const s = q.toLowerCase();
  return k.kind.toLowerCase().includes(s) || k.group.toLowerCase().includes(s) || (k.operator ?? "").toLowerCase().includes(s);
}

const GROUP_BYS: CRDGroupBy[] = ["group", "operator", "scope", "alphabetical"];
const GROUP_BY_LABEL: Record<CRDGroupBy, string> = { group: "api group", operator: "operator", scope: "scope", alphabetical: "alphabetical" };

// The custom-resource catalogue on the workspace template: grouped by API
// group (or operator/scope/alphabetical) with live instance counts. No
// inspector - a kind opens the generic Resources lens.
export function CRDBrowser({ cluster }: { cluster: string }) {
  const crd = useFleet((s) => s.crd);
  const setGroupBy = useFleet((s) => s.setCRDGroupBy);
  const setSearch = useFleet((s) => s.setCRDSearch);

  useEffect(() => {
    listCRDs(cluster).catch((e) => console.error("listCRDs", e));
    return () => useFleet.getState().clearCRDs();
  }, [cluster]);

  const groups = crd.cluster === cluster ? crd.groups : [];
  const sections = reshape(groups, crd.groupBy)
    .map((sec) => ({ ...sec, kinds: sec.kinds.filter((k) => matches(k, crd.search)) }))
    .filter((sec) => sec.kinds.length > 0);
  const totalKinds = flatten(groups).length;
  const shownKinds = sections.reduce((n, s) => n + s.kinds.length, 0);

  // A failed read is not "no custom resources" (review 2026-10-03 #10).
  let state: DatasetState = "ready";
  if (crd.loading && groups.length === 0) state = "loading";
  else if (crd.error && groups.length === 0) state = "unreadable";
  else if (groups.length === 0) state = "empty";
  else if (sections.length === 0) state = "no-match";
  const stateView = state === "loading" ? <div style={{ width: "100%", alignSelf: "flex-start" }}><SkeletonRows rows={7} label="loading custom resources" /></div>
    : state === "unreadable" ? <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="Custom resources could not be read." hint={crd.error ?? undefined} action={{ label: "Retry", onClick: () => { listCRDs(cluster).catch((e) => console.error("listCRDs", e)); } }} />
    : state === "empty" ? <EmptyState icon={<IconComponents size={28} stroke={1.2} />} title="No custom resources on this cluster." hint="Only built-in kinds are served here." />
    : <EmptyState icon={<IconComponents size={28} stroke={1.2} />} title="No custom resources match the search." action={{ label: "Clear search", onClick: () => setSearch("") }} />;

  return (
    <LensLayout
      label="Custom resources"
      title="API browser"
      summary={groups.length > 0 ? (
        <span className="t-secondary"><b className="t-number" style={{ color: "var(--color-text-primary)" }}>{groups.length}</b> groups · <b className="t-number" style={{ color: "var(--color-text-primary)" }}>{totalKinds}</b> kinds</span>
      ) : null}
      controls={
        <>
          <span className="t-secondary">Group by</span>
          <div role="group" aria-label="Group by" className="l-seg">
            {GROUP_BYS.map((g) => (
              <button key={g} className="l-chip" aria-pressed={crd.groupBy === g} onClick={() => setGroupBy(g)}>{GROUP_BY_LABEL[g]}</button>
            ))}
          </div>
          <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
            <span className="sr-only">Search custom resources</span>
            <input className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={crd.search} onChange={(e) => setSearch(e.target.value)} placeholder="kind, group, operator…" />
          </label>
        </>
      }
      notice={crd.error && groups.length > 0 ? <WatchErrorBanner tool="Custom resources" failure="read failed - showing the last good read" error={crd.error} /> : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={520}>
          <div data-testid="crd-resource-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
            {sections.map((sec) => (
              <Section key={sec.label} cluster={cluster} label={sec.label} category={sec.category} kinds={sec.kinds} grouped={crd.groupBy === "group"} />
            ))}
          </div>
        </LensTable>
      }
      footer={<span className="t-legend">↵ open · counts are live reads; ? = unreadable, never zero</span>}
      footerEnd={<span className="t-secondary">{shownKinds} of {totalKinds} kinds{crd.search ? " · filter: search" : ""}</span>}
    />
  );
}

const CRD_GRID = "16px minmax(200px, 1fr) 96px 64px minmax(120px, 0.8fr)";

function Section({ cluster, label, category, kinds, grouped }: { cluster: string; label: string; category: string; kinds: FlatKind[]; grouped: boolean }) {
  const expanded = useFleet((s) => s.crd.expanded);
  const counts = useFleet((s) => s.crd.counts);
  const toggle = useFleet((s) => s.toggleCRDGroup);
  const openResource = useFleet((s) => s.openResource);
  const open = !grouped || expanded.includes(label);

  // Re-firing on `counts` is safe and convergent: the `!counts[key]` guard skips
  // already-fetched kinds, and countKind's in-flight Set dedupes the window
  // before a count lands - so each kind is counted exactly once.
  useEffect(() => {
    if (!open) return;
    for (const k of kinds) {
      if (!counts[crdCountKey(cluster, k.group, k.version, k.plural)]) {
        void countKind(cluster, k.group, k.version, k.plural);
      }
    }
  }, [open, cluster, kinds, counts]);

  const sectionInstances = kinds.reduce((n, k) => {
    const c = counts[crdCountKey(cluster, k.group, k.version, k.plural)];
    return n + (c && !c.error ? c.count : 0);
  }, 0);
  const sectionCounted = kinds.every((k) => counts[crdCountKey(cluster, k.group, k.version, k.plural)]);
  const sectionCapped = kinds.some((k) => counts[crdCountKey(cluster, k.group, k.version, k.plural)]?.capped);
  // A section with any unreadable count has no definitive total: show what WAS
  // read as a lower bound with a "?" marker, never a confident (possibly 0)
  // number (Codex batch-2 review R6).
  const sectionUnreadable = kinds.filter((k) => counts[crdCountKey(cluster, k.group, k.version, k.plural)]?.error).length;
  const sectionTotal = !sectionCounted
    ? "…"
    : sectionUnreadable > 0
      ? `${sectionInstances}+? instances`
      : `${sectionInstances}${sectionCapped ? "+" : ""} instances`;

  return (
    <section aria-label={label}>
      <div
        className="lens-table-head"
        role={grouped ? "button" : undefined}
        tabIndex={grouped ? 0 : undefined}
        aria-expanded={grouped ? open : undefined}
        onClick={() => grouped && toggle(label)}
        onKeyDown={(e) => { if (grouped && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); toggle(label); } }}
        style={{ gridTemplateColumns: CRD_GRID, background: "var(--color-background-secondary)", cursor: grouped ? "pointer" : "default" }}
      >
        <span style={{ display: "inline-flex" }}>{grouped ? <IconChevronRight size={13} stroke={1.7} style={{ transform: open ? "rotate(90deg)" : "none", transition: "transform 100ms ease" }} /> : null}</span>
        <span className="t-identifier" style={{ color: "var(--color-text-primary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {label}{category ? <span className="t-secondary" style={{ marginLeft: 8, fontFamily: "var(--font-sans)" }}>{category}</span> : null}
        </span>
        <span className="t-secondary">{kinds.length} kinds</span>
        <span />
        <span
          className="t-secondary"
          style={{ color: sectionUnreadable > 0 ? "var(--color-text-warning)" : undefined, justifySelf: "end" }}
          title={sectionUnreadable > 0 ? `${sectionUnreadable} kind${sectionUnreadable === 1 ? "" : "s"} could not be counted - the total is a lower bound` : undefined}
        >
          {sectionTotal}
        </span>
      </div>
      {open && kinds.map((k) => {
        const c = counts[crdCountKey(cluster, k.group, k.version, k.plural)];
        // A failed count is "?" - never a zero (review 2026-10-03 #10).
        const display = c ? (c.error ? "?" : c.capped ? `${c.count}+` : `${c.count}`) : "…";
        const go = () => openResource({ group: k.group, version: k.version, plural: k.plural, kind: k.kind, scope: k.scope });
        return (
          <div key={`${k.group}/${k.kind}`} role="button" tabIndex={0} className="lens-row" style={{ gridTemplateColumns: CRD_GRID }}
            onClick={go}
            onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); go(); } }}>
            <span />
            <span className="lens-cell-name">{k.kind} {k.shortNames[0] && <span style={{ color: "var(--color-text-tertiary)" }}>{k.shortNames[0]}</span>}</span>
            <span className="t-secondary">{k.scope === "Cluster" ? "cluster-scoped" : "namespaced"}</span>
            <span style={{ justifySelf: "end", color: c?.error ? "var(--color-text-warning)" : undefined }} title={c?.error ? "count could not be read" : undefined}>{display}</span>
            <span className="t-secondary" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{k.operator}</span>
          </div>
        );
      })}
    </section>
  );
}
