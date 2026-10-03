import { useEffect } from "react";
import { IconChevronRight, IconComponents } from "@tabler/icons-react";
import { useFleet, CRDGroupDTO, CRDKindDTO, CRDGroupBy, crdCountKey } from "../store/fleet";
import { listCRDs, countKind } from "../bridge/crd";
import { EmptyState, SkeletonRows, ellipsis } from "../chrome/primitives";
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

  if (crd.loading && groups.length === 0) {
    return <div style={{ padding: "14px 16px" }}><SkeletonRows rows={7} label="loading custom resources" /></div>;
  }
  // A failed read is not "no custom resources" (review 2026-10-03 #10).
  if (crd.error && groups.length === 0) {
    return (
      <div style={{ padding: "14px 16px" }}>
        <WatchErrorBanner tool="Custom resources" failure="read failed" error={crd.error} />
      </div>
    );
  }
  if (groups.length === 0 || (crd.search && sections.length === 0)) {
    return (
      <EmptyState
        icon={<IconComponents size={28} stroke={1.2} />}
        title={groups.length === 0 ? "No custom resources on this cluster." : "No custom resources match the search."}
        hint={groups.length === 0 ? "Only built-in kinds are served here." : undefined}
        action={groups.length > 0 ? { label: "Clear search", onClick: () => setSearch("") } : undefined}
      />
    );
  }

  return (
    <div style={{ padding: "14px 16px", height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box", display: "flex", flexDirection: "column" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 12, flexShrink: 0 }}>
        <div style={{ fontSize: 12, color: "var(--color-text-secondary)" }}>
          <b style={{ color: "var(--color-text-primary)" }}>{groups.length}</b> groups · <b style={{ color: "var(--color-text-primary)" }}>{totalKinds}</b> kinds
        </div>
        <div style={{ flex: 1 }} />
        <input
          value={crd.search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="kind, group, operator…"
          style={{ width: 200, height: 28, paddingLeft: 10, fontSize: 12, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" }}
        />
      </div>

      <div style={{ display: "flex", gap: 6, marginBottom: 10, fontSize: 11, alignItems: "center", flexShrink: 0 }}>
        <span style={{ color: "var(--color-text-tertiary)" }}>group by:</span>
        {GROUP_BYS.map((g) => (
          <button
            key={g}
            onClick={() => setGroupBy(g)}
            style={{
              padding: "3px 9px", borderRadius: 999, cursor: "pointer", fontSize: 11,
              border: "0.5px solid var(--color-border-tertiary)",
              background: crd.groupBy === g ? "var(--color-background-info)" : "transparent",
              color: crd.groupBy === g ? "var(--color-text-info)" : "var(--color-text-secondary)",
            }}
          >
            {GROUP_BY_LABEL[g]}
          </button>
        ))}
      </div>

      <div data-testid="crd-resource-scroll" style={{ border: "0.5px solid var(--color-border-tertiary)", borderRadius: "var(--border-radius-md)", overflowY: "auto", overflowX: "hidden", flex: 1, minHeight: 0 }}>
        {sections.map((sec) => (
          <Section key={sec.label} cluster={cluster} label={sec.label} category={sec.category} kinds={sec.kinds} grouped={crd.groupBy === "group"} />
        ))}
      </div>
    </div>
  );
}

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

  return (
    <div>
      <div
        onClick={() => grouped && toggle(label)}
        style={{ display: "grid", gridTemplateColumns: "18px 1fr 90px 70px 1fr", gap: 10, alignItems: "center", padding: "8px 12px", background: "var(--color-background-secondary)", cursor: grouped ? "pointer" : "default", borderTop: "0.5px solid var(--color-border-tertiary)" }}
      >
        <span style={{ color: "var(--color-text-secondary)", display: "inline-flex" }}>{grouped ? <IconChevronRight size={13} stroke={1.7} style={{ transform: open ? "rotate(90deg)" : "none", transition: "transform 100ms ease" }} /> : null}</span>
        <div style={{ fontFamily: "var(--font-mono)", fontSize: 12, fontWeight: 500, ...ellipsis }}>{label}</div>
        {category ? <span style={{ background: "var(--color-background-primary)", color: "var(--color-text-secondary)", fontSize: 9, padding: "1px 6px", borderRadius: 3, letterSpacing: 0.3, justifySelf: "start" }}>{category}</span> : <span />}
        <span style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>{kinds.length} kinds</span>
        <span style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>{sectionCounted ? `${sectionInstances}${sectionCapped ? "+" : ""} instances` : "…"}</span>
      </div>
      {open && kinds.map((k) => {
        const c = counts[crdCountKey(cluster, k.group, k.version, k.plural)];
        // A failed count is "?" - never a zero (review 2026-10-03 #10).
        const display = c ? (c.error ? "?" : c.capped ? `${c.count}+` : `${c.count}`) : "…";
        return (
          <div
            key={`${k.group}/${k.kind}`}
            onClick={() => openResource({ group: k.group, version: k.version, plural: k.plural, kind: k.kind, scope: k.scope })}
            style={{ display: "grid", gridTemplateColumns: "18px 1fr 90px 70px 1fr", gap: 10, alignItems: "center", padding: "6px 12px", borderTop: "0.5px solid var(--color-border-tertiary)", fontSize: 11, cursor: "pointer" }}
          >
            <span />
            <div style={{ fontFamily: "var(--font-mono)", ...ellipsis }}>{k.kind} {k.shortNames[0] && <span style={{ color: "var(--color-text-tertiary)" }}>{k.shortNames[0]}</span>}</div>
            <span style={{ background: "var(--color-background-secondary)", color: "var(--color-text-secondary)", fontSize: 9, padding: "1px 5px", borderRadius: 3, justifySelf: "start" }}>{k.scope.toLowerCase()}</span>
            <span style={{ fontWeight: 500 }}>{display}</span>
            <span style={{ color: "var(--color-text-tertiary)", ...ellipsis }}>{k.operator}</span>
          </div>
        );
      })}
    </div>
  );
}
