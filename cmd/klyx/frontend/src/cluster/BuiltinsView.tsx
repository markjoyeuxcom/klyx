import { useEffect, useState } from "react";
import { useFleet, ResourceRef, crdCountKey } from "../store/fleet";
import { countKind } from "../bridge/crd";
import { BUILTIN_CATALOG, BuiltinEntry } from "./builtins";
import { EmptyState } from "../chrome/primitives";
import { IconBox } from "@tabler/icons-react";
import { LensLayout, LensTable } from "../layout/LensLayout";
import { AdhocComposer } from "./AdhocComposer";
import { ScratchPanel } from "./ScratchPanel";

function matchesEntry(entry: BuiltinEntry, q: string): boolean {
  if (!q) return true;
  const s = q.toLowerCase();
  if (entry.kind === "lens") {
    return entry.label.toLowerCase().includes(s) || entry.section.toLowerCase().includes(s);
  }
  const ref = entry.ref;
  return ref.kind.toLowerCase().includes(s) || ref.plural.toLowerCase().includes(s) || ref.group.toLowerCase().includes(s);
}

// The built-in resource catalogue on the workspace template: header, filters
// in the controls band, one scrolling table surface. No inspector - every row
// navigates (to a task lens, or to the generic Resources lens).
export function BuiltinsView({ cluster }: { cluster: string }) {
  const search = useFleet((s) => s.crd.search);
  const setSearch = useFleet((s) => s.setCRDSearch);
  const builtinCategory = useFleet((s) => s.crd.builtinCategory);
  const setBuiltinCategory = useFleet((s) => s.setBuiltinCategory);
  const [composing, setComposing] = useState(false);
  const [scratchOpen, setScratchOpen] = useState(false);

  const categories = BUILTIN_CATALOG
    .filter((cat) => builtinCategory === null || cat.label === builtinCategory)
    .map((cat) => ({ ...cat, entries: cat.entries.filter((e) => matchesEntry(e, search)) }))
    .filter((cat) => cat.entries.length > 0);
  const totalKinds = BUILTIN_CATALOG.reduce((n, c) => n + c.entries.length, 0);
  const shownKinds = categories.reduce((n, c) => n + c.entries.length, 0);
  const filtered = search !== "" || builtinCategory !== null;

  return (
    <>
      <LensLayout
        label="Built-in resources"
        title="Built-in resources"
        summary={<span className="t-secondary">{totalKinds} kinds in {BUILTIN_CATALOG.length} categories · counts load per category</span>}
        headerEnd={
          <>
            <button className="l-btn" onClick={() => setComposing(true)} title="Deploy scratch/diagnostic YAML (labeled klyx.dev/adhoc, not in Git)">Deploy YAML</button>
            <button className="l-btn" onClick={() => setScratchOpen(true)} title="Klyx-created scratch objects: launch a debug pod, list, clean up">Scratch</button>
          </>
        }
        controls={
          <>
            <button className="l-chip" aria-pressed={builtinCategory === null} onClick={() => setBuiltinCategory(null)}>all</button>
            {BUILTIN_CATALOG.map((cat) => (
              <button key={cat.label} className="l-chip" aria-pressed={builtinCategory === cat.label} onClick={() => setBuiltinCategory(builtinCategory === cat.label ? null : cat.label)}>
                {cat.label}
              </button>
            ))}
            <label style={{ display: "flex", flex: "0 1 240px", minWidth: 140, marginLeft: "auto" }}>
              <span className="sr-only">Search built-in kinds</span>
              <input className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false} value={search} onChange={(e) => setSearch(e.target.value)} placeholder="kind, group…" />
            </label>
          </>
        }
        state={categories.length === 0 ? "no-match" : "ready"}
        stateView={
          <EmptyState
            icon={<IconBox size={28} stroke={1.2} />}
            title="No built-in resources match your search."
            action={{ label: "Clear filters", onClick: () => { setSearch(""); setBuiltinCategory(null); } }}
          />
        }
        body={
          <LensTable minWidth={420}>
            <div data-testid="builtin-resource-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {categories.map((cat) => (
                <BuiltinCategorySection key={cat.label} cluster={cluster} label={cat.label} entries={cat.entries} />
              ))}
            </div>
          </LensTable>
        }
        footer={<span className="t-legend">↵ open · counts are live reads; ? = unreadable, never zero</span>}
        footerEnd={<span className="t-secondary">{shownKinds} of {totalKinds} kinds{filtered ? " · filtered" : ""}</span>}
      />
      {composing && <AdhocComposer cluster={cluster} onClose={() => setComposing(false)} />}
      {scratchOpen && <ScratchPanel cluster={cluster} onClose={() => setScratchOpen(false)} />}
    </>
  );
}

const CATALOG_GRID = "minmax(200px, 1fr) 180px 64px";

// BuiltinCategorySection renders one category from the static builtin catalog,
// always expanded. GVR counts are loaded lazily; lens entries navigate to the
// dedicated lens section instead of opening the generic instance list.
function BuiltinCategorySection({ cluster, label, entries }: { cluster: string; label: string; entries: BuiltinEntry[] }) {
  const counts = useFleet((s) => s.crd.counts);
  const openResource = useFleet((s) => s.openResource);
  const setSection = useFleet((s) => s.setSection);

  const gvrRefs: ResourceRef[] = entries.flatMap((e) => e.kind === "gvr" ? [e.ref] : []);

  useEffect(() => {
    for (const ref of gvrRefs) {
      if (!counts[crdCountKey(cluster, ref.group, ref.version, ref.plural)]) {
        void countKind(cluster, ref.group, ref.version, ref.plural);
      }
    }
  }, [cluster, gvrRefs, counts]);

  return (
    <section aria-label={label}>
      <div className="lens-table-head" style={{ gridTemplateColumns: CATALOG_GRID, background: "var(--color-background-secondary)" }}>
        <h3 className="t-section-label" style={{ margin: 0 }}>{label}</h3>
        <span />
        <span style={{ justifySelf: "end" }}>Count</span>
      </div>
      {entries.map((entry) => {
        if (entry.kind === "lens") {
          return (
            <div key={`lens/${entry.section}/${entry.label}`} role="button" tabIndex={0} className="lens-row" style={{ gridTemplateColumns: CATALOG_GRID }}
              onClick={() => setSection(entry.section)}
              onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSection(entry.section); } }}>
              <span className="lens-cell-name">{entry.label}</span>
              <span className="t-secondary" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{entry.hint} →</span>
              <span style={{ color: "var(--color-text-tertiary)", justifySelf: "end" }}>-</span>
            </div>
          );
        }
        const ref = entry.ref;
        const c = counts[crdCountKey(cluster, ref.group, ref.version, ref.plural)];
        // A failed count is "?" - never a zero (review 2026-10-03 #10).
        const display = c ? (c.error ? "?" : c.capped ? `${c.count}+` : `${c.count}`) : "…";
        return (
          <div key={`builtin/${ref.group}/${ref.plural}`} role="button" tabIndex={0} className="lens-row" style={{ gridTemplateColumns: CATALOG_GRID }}
            onClick={() => openResource(ref)}
            onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openResource(ref); } }}>
            <span className="lens-cell-name">{ref.kind}</span>
            <span className="t-secondary">{ref.scope === "Cluster" ? "cluster-scoped" : "namespaced"}</span>
            <span style={{ justifySelf: "end", color: c?.error ? "var(--color-text-warning)" : "var(--color-text-primary)" }} title={c?.error ? "count could not be read" : undefined}>{display}</span>
          </div>
        );
      })}
    </section>
  );
}
