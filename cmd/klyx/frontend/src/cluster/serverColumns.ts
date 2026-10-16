import { columnsSignature, type InstanceColumnDTO, type InstanceDTO } from "../store/fleet";
import { conditionAspect, polarityOf } from "../chrome/conditions";
import type { AspectKind } from "../chrome/primitives/Aspect";

// The server's printer columns in the generic list (The Line slice 15a).
// Cells are positional: every column keeps its index into a row's cells.

export type IndexedColumn = { col: InstanceColumnDTO; idx: number };

// splitColumns: priority 0 is kubectl's default view and joins the list;
// 1 and above is -o wide, shown in the inspector's summary. The mark column
// is the first column whose JSONPath proves it is a condition's status and
// whose type has a known polarity - a neutral type never takes the mark from
// a known one after it (implementation review 1).
export function splitColumns(cols: InstanceColumnDTO[] | null | undefined): { list: IndexedColumn[]; mark: IndexedColumn | null } {
  if (!cols) return { list: [], mark: null };
  const indexed = cols.map((col, idx) => ({ col, idx }));
  return {
    list: indexed.filter((c) => c.col.priority === 0),
    mark: indexed.find((c) => !!c.col.conditionType && polarityOf(c.col.conditionType) !== "neutral") ?? null,
  };
}

// rowMark reads the recorded condition a proven column holds - never a
// health verdict. null when the row carries no mark.
export function rowMark(row: InstanceDTO, mark: IndexedColumn | null): { aspect: AspectKind; label: string } | null {
  if (!mark || !row.cells) return null;
  const type = mark.col.conditionType ?? "";
  const value = row.cells[mark.idx] ?? "";
  const aspect = conditionAspect(type, value);
  if (aspect === "none") return null;
  return { aspect, label: `${type} ${value} (from .status.conditions[${type}])` };
}

const GENERATED = "Custom resource definition column (in JSONPath format): ";

// columnTitle: a header's hover text. The server's generated description is
// the JSONPath; an author's description is shown as written.
export function columnTitle(c: InstanceColumnDTO): string {
  if (c.description.startsWith(GENERATED)) return `JSONPath ${c.jsonPath || c.description.slice(GENERATED.length)}`;
  return c.description;
}

// fleetColumns: the fleet list draws columns only when every cluster that
// answered read the same ones (design review 1). Clusters with no table
// count as their own signature, so a mix fails closed too.
export function fleetColumns(answers: (InstanceColumnDTO[] | null)[]): { columns: InstanceColumnDTO[] | null; mismatch: boolean } {
  if (answers.length === 0) return { columns: null, mismatch: false };
  const sigs = new Set(answers.map(columnsSignature));
  if (sigs.size > 1) return { columns: null, mismatch: true };
  return { columns: answers[0], mismatch: false };
}

// columnWidth: a floor and a share of spare width for one column.
export function columnWidth(c: InstanceColumnDTO): { width: number; grow?: number } {
  if (c.numeric) return { width: 72 };
  if (c.type === "date") return { width: 64 };
  if (c.type === "boolean") return { width: 72 };
  if (c.conditionType) return { width: 80 };
  return { width: 120, grow: 1 };
}
