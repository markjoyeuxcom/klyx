import { useEffect, useState } from "react";
import { useFleet, ResourceRef, crdCountKey } from "../store/fleet";
import { countKind } from "../bridge/crd";
import { BUILTIN_CATALOG, BuiltinEntry } from "./builtins";
import { Chip, btn, ellipsis } from "../chrome/primitives";
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

  const isEmpty = categories.length === 0;

  return (
    <div style={{ padding: "14px 16px", height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box", display: "flex", flexDirection: "column" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 8, flexShrink: 0 }}>
        <div style={{ fontSize: 12, color: "var(--color-text-secondary)" }}>
          Built-in resource catalog
        </div>
        <div style={{ flex: 1 }} />
        <button onClick={() => setComposing(true)} style={btn} title="Deploy scratch/diagnostic YAML (labeled klyx.dev/adhoc, not in Git)">deploy yaml</button>
        <button onClick={() => setScratchOpen(true)} style={btn} title="Klyx-created scratch objects: launch a debug pod, list, clean up">scratch</button>
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="kind, group…"
          style={{ width: 200, height: 28, paddingLeft: 10, fontSize: 12, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" }}
        />
      </div>
      {composing && <AdhocComposer cluster={cluster} onClose={() => setComposing(false)} />}
      {scratchOpen && <ScratchPanel cluster={cluster} onClose={() => setScratchOpen(false)} />}

      {/* Category chip row */}
      <div style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: 12, flexWrap: "wrap", flexShrink: 0 }}>
        <Chip on={builtinCategory === null} onClick={() => setBuiltinCategory(null)}>all</Chip>
        {BUILTIN_CATALOG.map((cat) => (
          <Chip
            key={cat.label}
            on={builtinCategory === cat.label}
            onClick={() => setBuiltinCategory(builtinCategory === cat.label ? null : cat.label)}
          >
            {cat.label}
          </Chip>
        ))}
      </div>

      {isEmpty ? (
        <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>No built-in resources match your search.</div>
      ) : (
        <div data-testid="builtin-resource-scroll" style={{ border: "0.5px solid var(--color-border-tertiary)", borderRadius: "var(--border-radius-md)", overflowY: "auto", overflowX: "hidden", flex: 1, minHeight: 0 }}>
          {categories.map((cat) => (
            <BuiltinCategorySection key={cat.label} cluster={cluster} label={cat.label} entries={cat.entries} />
          ))}
        </div>
      )}
    </div>
  );
}

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
      if (!counts[crdCountKey(ref.group, ref.version, ref.plural)]) {
        void countKind(cluster, ref.group, ref.version, ref.plural);
      }
    }
  }, [cluster, gvrRefs, counts]);

  return (
    <div>
      <div style={{ display: "grid", gridTemplateColumns: "18px 1fr 90px 70px", gap: 10, alignItems: "center", padding: "7px 12px", background: "var(--color-background-secondary)", borderTop: "0.5px solid var(--color-border-tertiary)" }}>
        <span />
        <div style={{ fontSize: 11, fontWeight: 600, color: "var(--color-text-secondary)", ...ellipsis }}>{label}</div>
        <span />
        <span />
      </div>
      {entries.map((entry) => {
        if (entry.kind === "lens") {
          return (
            <div
              key={`lens/${entry.section}/${entry.label}`}
              onClick={() => setSection(entry.section)}
              style={{ display: "grid", gridTemplateColumns: "18px 1fr 90px 70px", gap: 10, alignItems: "center", padding: "6px 12px", borderTop: "0.5px solid var(--color-border-tertiary)", fontSize: 11, cursor: "pointer" }}
            >
              <span />
              <div style={{ fontFamily: "var(--font-mono)", ...ellipsis }}>{entry.label}</div>
              <span style={{ color: "var(--color-text-tertiary)", fontSize: 10, justifySelf: "start", ...ellipsis }}>{entry.hint} →</span>
              <span style={{ color: "var(--color-text-tertiary)" }}>—</span>
            </div>
          );
        }
        const ref = entry.ref;
        const c = counts[crdCountKey(ref.group, ref.version, ref.plural)];
        // A failed count is "?" - never a zero (review 2026-10-03 #10).
        const display = c ? (c.error ? "?" : c.capped ? `${c.count}+` : `${c.count}`) : "…";
        return (
          <div
            key={`builtin/${ref.group}/${ref.plural}`}
            onClick={() => openResource(ref)}
            style={{ display: "grid", gridTemplateColumns: "18px 1fr 90px 70px", gap: 10, alignItems: "center", padding: "6px 12px", borderTop: "0.5px solid var(--color-border-tertiary)", fontSize: 11, cursor: "pointer" }}
          >
            <span />
            <div style={{ fontFamily: "var(--font-mono)", ...ellipsis }}>{ref.kind}</div>
            <span style={{ background: "var(--color-background-secondary)", color: "var(--color-text-secondary)", fontSize: 9, padding: "1px 5px", borderRadius: 3, justifySelf: "start" }}>{ref.scope.toLowerCase()}</span>
            <span style={{ fontWeight: 500 }}>{display}</span>
          </div>
        );
      })}
    </div>
  );
}
