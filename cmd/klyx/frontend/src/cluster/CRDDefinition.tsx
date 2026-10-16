import { useEffect, useMemo, useRef, useState } from "react";
import { IconChevronRight } from "@tabler/icons-react";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { InspectorStatus } from "./ResourceInspector";
import { Age, absoluteTime } from "../chrome/Age";
import { Aspect, type AspectKind } from "../chrome/primitives/Aspect";
import { getCRDDefinition, copyText } from "../bridge/crd";
import { useDescribe } from "../chrome/describe/useDescribe";
import type { ConditionDTO, CRDConversionDTO, CRDDefinitionDTO, SchemaNodeDTO } from "../store/fleet";
import { schemaTitle } from "./schemaTitles";

// The definition inspector (The Line slice 15c): one CRD read in full - its
// recorded conditions and what each implies, names, versions and storage,
// conversion in words, printer columns and a navigable schema
// (kubectl explain as a view). Read-only.

export type CRDSelection = { group: string; plural: string; kind: string; version: string; scope: string };

// definitionFindings reads the CRD's recorded conditions, never claiming
// availability (design reviews 1 and 2): the API server serves a served
// version once NamesAccepted or Established is True, so a condition alone
// is not serving. Established and NamesAccepted are expected; a missing one
// is checking. NonStructuralSchema is optional and negative: absent or
// False adds nothing. An explicit Unknown of any type is checking.
export type Finding = { aspect: AspectKind; text: string };
export function definitionFindings(conditions: ConditionDTO[]): Finding[] {
  const by = new Map(conditions.map((c) => [c.type, c]));
  const out: Finding[] = [];
  const est = by.get("Established");
  const names = by.get("NamesAccepted");
  const nss = by.get("NonStructuralSchema");
  if (!est) out.push({ aspect: "checking", text: "Established is not recorded" });
  else if (est.status === "Unknown") out.push({ aspect: "checking", text: "Established is Unknown" });
  else if (est.status !== "True") out.push({ aspect: "attention", text: `Not yet established${names ? ` (names ${names.status === "True" ? "accepted" : names.status === "False" ? "not accepted" : names.status.toLowerCase()})` : ""}${est.reason ? `: ${est.reason}` : ""}` });
  if (!names) out.push({ aspect: "checking", text: "NamesAccepted is not recorded" });
  else if (names.status === "Unknown") out.push({ aspect: "checking", text: "NamesAccepted is Unknown" });
  else if (names.status === "False") out.push({ aspect: "attention", text: `Names not accepted: ${names.message || names.reason || "no reason recorded"}` });
  if (nss?.status === "True") out.push({ aspect: "attention", text: `Non-structural schema: ${nss.message || nss.reason || "no message recorded"}` });
  else if (nss?.status === "Unknown") out.push({ aspect: "checking", text: "NonStructuralSchema is Unknown" });
  return out;
}

// conversionWords: spec.conversion as configured, in plain words.
export function conversionWords(c: CRDConversionDTO): string {
  if (c.strategy !== "Webhook") return "None: the API server changes only apiVersion; other fields pass through unchanged, even where the versions' schemas differ.";
  const target = c.url ? c.url : c.serviceName ? `${c.serviceNamespace}/${c.serviceName}:${c.servicePort ?? 443}${c.servicePath ?? ""}` : "no target recorded";
  const review = c.reviewVersions?.length ? `, review versions ${c.reviewVersions.join(", ")}` : "";
  return `Webhook: ${target}${review}.`;
}

const segText = (s: { k: string; n?: string }) => (s.k === "p" ? s.n ?? "" : s.k === "i" ? "[]" : "{}");
const pathText = (n: SchemaNodeDTO) => n.path.map(segText).join(".");

export function CRDDefinitionInspector({ cluster, sel, count, onClose, onOpen }: { cluster: string; sel: CRDSelection; count?: string; onClose: () => void; onOpen: () => void }) {
  const name = `${sel.plural}.${sel.group}`;
  const [version, setVersion] = useState("");
  const [def, setDef] = useState<CRDDefinitionDTO | null>(null);
  const gen = useRef(0);
  useEffect(() => { setVersion(""); }, [cluster, name]);
  useEffect(() => {
    const mine = ++gen.current;
    setDef(null);
    void getCRDDefinition(cluster, name, version).then((d) => { if (gen.current === mine) setDef(d); });
  }, [cluster, name, version]);

  const describe = useDescribe(cluster, { kind: "CustomResourceDefinition", namespace: "", name, uid: def?.uid });
  const more: MenuItem[] = [...describe.menu, { label: "Copy name", onSelect: () => void copyText(name) }];
  const findings = def && !def.error ? definitionFindings(def.conditions) : [];
  const verdict = !def ? <div className="gen-note">Reading the definition…</div>
    : def.error ? <InspectorStatus tone="danger" label="The definition could not be read" reason={def.error} />
    : findings.length === 0 ? <InspectorStatus tone="success" label="Established" reason="names accepted" />
    : <InspectorStatus tone={findings.some((f) => f.aspect === "attention") ? "warning" : "info"} label={findings[0].text} reason={findings.slice(1).map((f) => f.text).join(" · ") || undefined} />;

  return (
    <div data-testid="crd-definition-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster, uid: def?.uid || undefined }} kind="CustomResourceDefinition" name={name}
        meta={`${sel.group} · ${sel.scope === "Cluster" ? "cluster-scoped" : "namespaced"}`}
        onClose={onClose} verdict={verdict} primary={{ label: "Open instances", onSelect: onOpen }} more={more}>
        {def && !def.error && <DefinitionBody def={def} count={count} onVersion={setVersion} />}
      </InspectorLayout>
      {describe.sheet}
    </div>
  );
}

function DefinitionBody({ def, count, onVersion }: { def: CRDDefinitionDTO; count?: string; onVersion: (v: string) => void }) {
  const served = new Set(def.versions.filter((v) => v.served).map((v) => v.name));
  const storedNotServed = def.storedVersions.filter((v) => !served.has(v));
  const facts: [string, string][] = [
    ["Kind", def.kind], ["Plural", def.plural], ["Singular", def.singular], ["List kind", def.listKind],
    ["Short names", def.shortNames.join(", ")], ["Categories", def.categories.join(", ")], ["Operator", def.operator],
    ["Instances", count ?? ""],
  ];
  const schemaVersion = def.versions.find((v) => v.name === def.schemaVersion);
  return (
    <>
      <InspectorBlock title="Definition">
        <div className="insp-kv gen-kv" data-testid="crd-facts">
          {facts.filter(([, v]) => v).map(([k, v]) => <FactRow key={k} k={k} v={v} />)}
          {def.created && <><span>Created</span><span className="gen-val">{absoluteTime(Date.parse(def.created))} <span className="gen-dim">· <Age iso={def.created} suffix=" ago" /></span></span></>}
        </div>
      </InspectorBlock>
      <InspectorBlock title="Versions" count={def.versions.length}>
        {/* a well like the printer columns: a long deprecation warning wraps
            (and only then takes the wrap column's width) or scrolls inside it */}
        <div className="describe-table-wrap">
          <table className="describe-table crd-versions" data-testid="crd-versions">
            <thead><tr><th>Version</th><th>Served</th><th>Storage</th><th>Deprecated</th></tr></thead>
            <tbody>
              {def.versions.map((v) => (
                <tr key={v.name}>
                  <td>{v.name}</td><td>{v.served ? "yes" : "no"}</td><td>{v.storage ? "yes" : "no"}</td>
                  <td data-wrap={v.deprecated && v.deprecationWarning ? "true" : undefined}>{v.deprecated ? (v.deprecationWarning || "yes") : "no"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {storedNotServed.map((v) => (
          <div key={v} className="gen-flag crd-stored"><Aspect kind="attention" label="stored but not served" /> Objects may still be stored as {v}, which is no longer served.</div>
        ))}
      </InspectorBlock>
      <InspectorBlock title="Conversion">
        <div className="gen-val" data-testid="crd-conversion">{conversionWords(def.conversion)}</div>
      </InspectorBlock>
      {(schemaVersion?.columns.length ?? 0) > 0 && (
        <InspectorBlock title={`Printer columns · ${def.schemaVersion}`} count={schemaVersion!.columns.length}>
          <div className="describe-table-wrap">
            <table className="describe-table" data-testid="crd-columns">
              <thead><tr><th>Name</th><th>Type</th><th>JSONPath</th><th>Priority</th><th>Description</th></tr></thead>
              <tbody>
                {schemaVersion!.columns.map((c) => (
                  <tr key={c.name}><td>{c.name}</td><td>{c.type}{c.format ? `, ${c.format}` : ""}</td><td>{c.jsonPath}</td><td>{c.priority}</td><td data-wrap="true">{c.description ?? ""}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        </InspectorBlock>
      )}
      <InspectorBlock title="Schema" count={def.schema.filter((n) => n.path.length > 0).length}>
        {def.versions.length > 1 && (
          <div role="group" aria-label="Schema version" className="l-seg crd-version-switch">
            {def.versions.map((v) => (
              <button key={v.name} className="l-chip" aria-pressed={v.name === def.schemaVersion} onClick={() => onVersion(v.name)}>{v.name}</button>
            ))}
          </div>
        )}
        <SchemaTree nodes={def.schema} recorded={def.schemaRecorded ?? def.schema.length > 0} truncated={!!def.schemaTruncated} />
      </InspectorBlock>
    </>
  );
}

function FactRow({ k, v }: { k: string; v: string }) {
  return <><span>{k}</span><span className="gen-val">{v}</span></>;
}

// SchemaTree draws the flattened schema: the root's own facts, then the
// fields - depth from the path, children shown once their parent is open
// (the first two levels open), a filter on the field path that shows
// matches and their ancestors. A recorded schema with no properties says
// "no fields declared", never "no schema" (implementation review 1).
export function SchemaTree({ nodes: all, recorded, truncated }: { nodes: SchemaNodeDTO[]; recorded: boolean; truncated: boolean }) {
  const root = all[0]?.path.length === 0 ? all[0] : null;
  const nodes = useMemo(() => (root ? all.slice(1) : all), [all, root]);
  const [filter, setFilter] = useState("");
  // toggles override the default: the top level (spec, status) is open.
  const [toggles, setToggles] = useState<Map<string, boolean>>(() => new Map());
  const keyed = useMemo(() => nodes.map((n) => ({
    n, key: JSON.stringify(n.path), depth: n.path.length - 1, text: pathText(n),
    ancestors: n.path.slice(0, -1).map((_, i) => JSON.stringify(n.path.slice(0, i + 1))),
  })), [nodes]);
  const parents = useMemo(() => new Set(keyed.flatMap((k) => k.ancestors)), [keyed]);
  const isOpen = (key: string, depth: number) => toggles.get(key) ?? depth < 1;
  const q = filter.trim().toLowerCase();
  const visible = useMemo(() => {
    if (q) {
      const keep = new Set<string>();
      for (const k of keyed) if (k.text.toLowerCase().includes(q)) { keep.add(k.key); k.ancestors.forEach((a) => keep.add(a)); }
      return keyed.filter((k) => keep.has(k.key));
    }
    return keyed.filter((k) => k.ancestors.every((a, i) => toggles.get(a) ?? i < 1));
  }, [keyed, q, toggles]);
  const toggle = (key: string, depth: number) => setToggles((m) => new Map(m).set(key, !(m.get(key) ?? depth < 1)));
  return (
    <div className="schema-tree" data-testid="crd-schema">
      {root && <SchemaRoot n={root} />}
      {nodes.length > 0 && (
        <label className="lens-search schema-filter">
          <span className="sr-only">Filter fields</span>
          <input className="l-input" type="search" placeholder="Filter fields (spec.mode)" value={filter} onChange={(e) => setFilter(e.target.value)} />
        </label>
      )}
      {!recorded && <div className="gen-note">This version records no schema.</div>}
      {recorded && nodes.length === 0 && (
        <div className="gen-note" data-testid="crd-schema-nofields">
          No fields declared.{root?.preserveUnknown ? " Unknown fields are kept, so the object's fields are not pruned." : ""}
        </div>
      )}
      {visible.map(({ n, key, depth }) => {
        const container = parents.has(key);
        const last = n.path[n.path.length - 1];
        return (
          <div key={key} className="otree-row schema-row" style={{ ["--depth" as string]: depth }} title={schemaTitle(n)}>
            <span className="otree-key">
              {container && !q ? (
                <button className="otree-toggle" aria-expanded={isOpen(key, depth)} aria-label={`${isOpen(key, depth) ? "Collapse" : "Expand"} ${segText(last)}`} onClick={() => toggle(key, depth)}>
                  <IconChevronRight size={13} stroke={1.5} className="otree-chev" aria-hidden />
                  <span className="schema-name">{segText(last)}</span>
                </button>
              ) : <span className="schema-name">{segText(last)}</span>}
              {n.required && <span className="schema-req" title="required">*</span>}
            </span>
            <span className="otree-val schema-type">
              {[n.type || (n.intOrString ? "int-or-string" : ""), n.format].filter(Boolean).join(", ")}
              {n.enum?.length ? <span className="gen-dim"> · {n.enum.length}{n.enumMore ? `+${n.enumMore}` : ""} values</span> : null}
              {n.description ? <span className="schema-desc">{n.description.split("\n")[0]}</span> : null}
            </span>
          </div>
        );
      })}
      {truncated && <div className="gen-note">The schema has more fields than this view keeps; kubectl explain reads the rest.</div>}
    </div>
  );
}

// SchemaRoot: what the schema says of the object as a whole - its type,
// whether it keeps unknown fields, the constraints the tree does not draw
// and its description.
function SchemaRoot({ n }: { n: SchemaNodeDTO }) {
  const facts = [
    n.type || (n.intOrString ? "int-or-string" : ""),
    n.preserveUnknown ? "keeps unknown fields" : "",
    n.embeddedResource ? "an embedded resource" : "",
    n.constraints?.length ? `constraints not shown: ${n.constraints.join(", ")}` : "",
  ].filter(Boolean);
  if (facts.length === 0 && !n.description) return null;
  return (
    <div className="schema-root" data-testid="crd-schema-root" title={schemaTitle(n)}>
      <span className="schema-root-k">Root</span>
      <span className="schema-root-v">
        {facts.join(" · ")}
        {n.description ? <span className="schema-desc">{n.description.split("\n")[0]}</span> : null}
      </span>
    </div>
  );
}
