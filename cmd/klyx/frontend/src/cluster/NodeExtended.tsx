import type { ExtendedEntryDTO, NodeRowDTO, ReservationDTO } from "../store/fleet";
import type { ExtendedMarks } from "../health/useExtendedRows";
import { InspectorBlock } from "../layout/InspectorLayout";
import { factLabel, figure, gapOf, markWords, requestedText, reservedOf } from "./extended";

// Coverage S11a §6: a node's extended resources in its inspector, rendered
// from the joined rows (node row plus reservations row), never from the
// detail fetch, so a status or label change re-renders it.

const fig = (s: string, mark: string) => (s === "-" ? "?" : `${s}${mark}`);

function EntryLine({ e, r, marks, rowMissing }: { e: ExtendedEntryDTO; r: ReservationDTO | undefined; marks: ExtendedMarks; rowMissing: boolean }) {
  const reserved = reservedOf(r, e.name);
  const gap = gapOf(e);
  const parts: string[] = [];
  if (e.capacity === "0") parts.push("0 advertised");
  else {
    parts.push(`${requestedText(r, e.name, marks.requested, rowMissing)} requested`);
    parts.push(e.allocatable === "" ? "allocatable not yet reported" : `${figure(e.allocatable)}${marks.nodes} allocatable`);
    parts.push(`${figure(e.capacity)}${marks.nodes} capacity`);
    if (gap !== null) parts.push(`${gap} not allocatable`);
  }
  if (reserved && reserved.servedByDra !== "0") parts.push(`${fig(reserved.servedByDra, marks.dra)} served by DRA`);
  if (reserved && reserved.notAdvertised !== "0") parts.push(`${fig(reserved.notAdvertised, marks.requested)} not advertised by this node`);
  const unit = e.unit === "partition" && e.partition ? `partition: ${e.partition}` : e.unit ? `unit: ${e.unit}` : "";
  const facts = e.facts.filter((f) => f.key !== "sharing-strategy" && f.key !== "replicas").map((f) => `${factLabel(f.key)} ${f.value}`);
  return (
    <div className="t-body" style={{ overflowWrap: "anywhere", padding: "3px 0" }} data-testid="node-extended-line">
      <span style={{ fontFamily: "var(--font-mono)" }}>{e.name}</span>
      <span className="t-secondary"> · {parts.join(" · ")}</span>
      {(unit || e.renamedFrom) && <div className="t-secondary">{[unit, e.renamedFrom ? `renamed from ${e.renamedFrom} (the plugin's default shared rename)` : ""].filter(Boolean).join(" · ")}</div>}
      {e.sharing && <div className="t-secondary">{e.sharing}</div>}
      {facts.length > 0 && <div className="t-secondary">{facts.join(" · ")}</div>}
    </div>
  );
}

export function NodeExtendedBlock({ row, reservation, marks, rowMissing }: { row: NodeRowDTO; reservation: ReservationDTO | undefined; marks: ExtendedMarks; rowMissing: boolean }) {
  const x = row.extended;
  const entries = x?.entries ?? [];
  const advertised = new Set(entries.map((e) => e.name));
  // requested names this node does not advertise (DRA-served or not advertised)
  const others = (reservation?.entries ?? []).filter((e) => !advertised.has(e.name));
  // a node with no extended resource and no request for one shows nothing
  if (entries.length === 0 && others.length === 0) return null;
  const notes: string[] = [];
  if ((x?.omitted ?? 0) > 0) notes.push(`${x?.omitted} more extended resource${x?.omitted === 1 ? "" : "s"} not shown`);
  if ((x?.factsOmitted ?? 0) > 0) notes.push(`vendor facts for ${x?.factsOmitted} more resource${x?.factsOmitted === 1 ? "" : "s"} not shown`);
  if ((reservation?.omitted ?? 0) > 0) notes.push(`${reservation?.omitted} more requested resource${reservation?.omitted === 1 ? "" : "s"} not shown`);
  if (reservation?.untracked) notes.push("more than 256 distinct names are requested here: the served-by-DRA and not-advertised figures of the rest are not established");
  if (rowMissing && marks.requested !== "?") notes.push("requested not shown: this node's reservations are not in the current read (the per-cluster bound may have left it out)");
  for (const w of [markWords(marks.requested, "requested"), marks.dra !== marks.requested ? markWords(marks.dra, "served by DRA") : "", markWords(marks.nodes, "allocatable and capacity")]) {
    if (w) notes.push(w);
  }
  return (
    <InspectorBlock title="Extended resources" count={entries.length}>
      <div data-testid="node-extended">
        {x?.migStrategy && <div className="t-secondary" style={{ padding: "2px 0" }}>MIG strategy: {x.migStrategy}{x.migStrategy === "mixed" ? " (each profile is its own resource)" : ""}</div>}
        {entries.map((e) => <EntryLine key={e.name} e={e} r={reservation} marks={marks} rowMissing={rowMissing} />)}
        {others.map((o) => (
          <div key={o.name} className="t-body" style={{ overflowWrap: "anywhere", padding: "3px 0" }} data-testid="node-extended-line">
            <span style={{ fontFamily: "var(--font-mono)" }}>{o.name}</span>
            <span className="t-secondary"> · {[o.servedByDra !== "0" ? `${fig(o.servedByDra, marks.dra)} served by DRA` : "", o.notAdvertised !== "0" ? `${fig(o.notAdvertised, marks.requested)} requested, not advertised by this node (a cluster-level resource, or served by DRA without a mapping)` : ""].filter(Boolean).join(" · ")}</span>
          </div>
        ))}
        <div className="t-secondary" style={{ marginTop: 6 }}>Requested is what pods reserve, not what they use; units are the resource's own.</div>
        {notes.map((n) => <div key={n} className="t-secondary" style={{ color: "var(--color-text-warning)" }} data-testid="node-extended-note">{n}</div>)}
      </div>
    </InspectorBlock>
  );
}
