import type { AspectKind } from "../primitives/Aspect";
import { CONDITION_KEYS, type DField, type DNode, type DTable, type DTree } from "./parse";

// The Describe sheet's view model (The Line slice 14b). It lays the parse
// tree out as the Formatted view draws it - sections, key/value rows, items,
// tables - and gives every piece of kubectl's text a Run, numbered in the
// order it renders, so find counts and rings exactly what is on screen.
// Nothing here adds a word to the report: ages, counts and marks are the
// sheet's own, outside the runs.

export type Run = { id: number; text: string; muted?: boolean; timeMs?: number };
// extra: a row's further lines (an event message's own newlines), drawn in
// the column extraCol, right after that cell's value.
export type VRow = { cells: Run[]; extra: Run[]; aspect?: AspectKind; aspectLabel?: string };
export type VTable = { k: "table"; kind: "events" | "conditions" | "plain"; head: Run[]; rows: VRow[]; wrapCol: number; extraCol: number };
export type VKv = { k: "kv"; key: Run; values: Run[]; children: VNode[] };
export type VNode =
  | VKv
  | VTable
  | { k: "line"; run: Run }
  | { k: "item"; first: boolean; children: VNode[] }
  | { k: "blank" }
  | { k: "opaque"; lines: Run[] };
export type VSection = {
  id: string;
  // label names the section in the index; null for an unlabelled run of
  // top-level fields between sections.
  label: string | null;
  title: Run | null;
  value: Run[];
  nodes: VNode[];
  count?: number;
  aspect?: AspectKind;
  aspectLabel?: string;
  // [from, to): the ids of the runs this section draws, so find can open
  // the sections that hold a match.
  from: number;
  to: number;
};
export type VModel = { sections: VSection[]; runs: Run[]; uid: string | null };

// Condition marks belong to Describe (review 1): a mark describes the
// recorded condition, never the object's health, and only for types whose
// polarity is known. Every other type, and Unknown, carries no mark.
const POSITIVE = new Set(["Ready", "Available", "Accepted", "Programmed", "ResolvedRefs", "Synced", "Established", "NamesAccepted",
  "Initialized", "ContainersReady", "PodScheduled", "PodReadyToStartContainers", "Progressing", "Complete"]);
const NEGATIVE = new Set(["MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable", "Stalled", "ReplicaFailure", "Failed"]);
export function conditionFault(type: string, status: string): boolean {
  return (POSITIVE.has(type) && status === "False") || (NEGATIVE.has(type) && status === "True");
}

const MONTHS: Record<string, number> = { Jan: 0, Feb: 1, Mar: 2, Apr: 3, May: 4, Jun: 5, Jul: 6, Aug: 7, Sep: 8, Oct: 9, Nov: 10, Dec: 11 };
// timeOf: an RFC 3339 value, or kubectl's RFC 1123Z ("Sat, 10 Oct 2026
// 12:29:38 +0100"); the whole value, never a time inside other words.
export function timeOf(text: string): number | undefined {
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.test(text)) {
    const ms = Date.parse(text);
    return Number.isNaN(ms) ? undefined : ms;
  }
  const m = /^(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun), (\d{2}) (\w{3}) (\d{4}) (\d{2}):(\d{2}):(\d{2}) ([+-])(\d{2})(\d{2})$/.exec(text);
  if (!m || !(m[2] in MONTHS)) return undefined;
  const off = (m[7] === "-" ? -1 : 1) * (Number(m[8]) * 60 + Number(m[9]));
  return Date.UTC(Number(m[3]), MONTHS[m[2]], Number(m[1]), Number(m[4]), Number(m[5]), Number(m[6])) - off * 60_000;
}

const MUTED = /^<[a-z ]+>$/;
// The section a top-level field earns: a table, a few lines of nesting, or
// one of the report's anchors. Smaller nesting stays inline.
const ANCHORS = new Set(["Events", "Conditions"]);

function lineCount(n: DNode): number {
  switch (n.t) {
    case "field": return 1 + n.cont.length + n.children.reduce((a, c) => a + lineCount(c), 0);
    case "item": return n.children.reduce((a, c) => a + lineCount(c), 0);
    case "table": return n.header.length + n.rows.reduce((a, r) => a + 1 + r.extra.length, 0);
    case "opaque": return n.raw.length;
    default: return 1;
  }
}
const hasTable = (f: DField) => f.children.some((c) => c.t === "table");

export function buildModel(tree: DTree): VModel {
  const runs: Run[] = [];
  const run = (text: string): Run => {
    const r: Run = { id: runs.length, text };
    if (MUTED.test(text)) r.muted = true;
    const ms = timeOf(text);
    if (ms !== undefined) r.timeMs = ms;
    runs.push(r);
    return r;
  };

  const valueRuns = (f: DField): Run[] => [...(f.value ? [run(f.value)] : []), ...f.cont.map((c) => run(c.text))];

  const tableOf = (t: DTable, owner: string): VTable => {
    const kind = owner === "Events" ? "events" : owner === "Conditions" ? "conditions" : "plain";
    const head = t.head.map(run);
    const ti = t.head.indexOf("Type");
    const si = t.head.indexOf("Status");
    const wrapCol = t.head.indexOf("Message");
    const extraCol = wrapCol >= 0 ? wrapCol : t.head.length - 1;
    const rows = t.rows.map((r): VRow => {
      // Runs in the order they draw: the extra lines right after their cell.
      const cells: Run[] = [];
      let extra: Run[] = [];
      r.cells.forEach((c, j) => {
        cells.push(run(c));
        if (j === extraCol) extra = r.extra.filter((x) => x.trim() !== "").map((x) => run(x.trim()));
      });
      const row: VRow = { cells, extra };
      if (kind === "events" && r.cells[0] === "Warning") Object.assign(row, { aspect: "attention", aspectLabel: "Warning" });
      if (kind === "conditions" && ti >= 0 && si >= 0 && conditionFault(r.cells[ti], r.cells[si])) Object.assign(row, { aspect: "fault", aspectLabel: `${r.cells[ti]} ${r.cells[si]}` });
      return row;
    });
    return { k: "table", kind, head, rows, wrapCol, extraCol };
  };

  // conditionsTable: a known conditions list (marked by the parser for its
  // kind and path) whose every item is scalar fields from the standard keys,
  // with Type and Status. Columns: Type, Status, Reason, the other keys in
  // kubectl's order, Message last - every value in a cell.
  const conditionsTable = (f: DField): VTable | null => {
    if (f.list !== "conditions") return null;
    const groups: DField[][] = (f.children.some((c) => c.t === "item") ? f.children.flatMap((c) => (c.t === "item" ? [c.children] : [])) : [f.children])
      .map((g) => g.filter((c): c is DField => c.t === "field"));
    const ok = groups.length > 0 && groups.every((g) =>
      g.every((x) => x.key in CONDITION_KEYS && x.children.length === 0 && x.cont.every((c) => c.kind === "aligned")) &&
      g.some((x) => x.key === "Type") && g.some((x) => x.key === "Status"));
    if (!ok) return null;
    const order: string[] = [];
    for (const g of groups) for (const x of g) if (!order.includes(x.key)) order.push(x.key);
    const lead = ["Type", "Status", "Reason"].filter((k) => order.includes(k));
    const cols = [...lead, ...order.filter((k) => !lead.includes(k) && k !== "Message"), ...(order.includes("Message") ? ["Message"] : [])];
    const head = cols.map(run);
    const rows = groups.map((g): VRow => {
      const get = (k: string) => g.find((x) => x.key === k);
      const cells = cols.map((k) => run([get(k)?.value ?? "", ...(get(k)?.cont.map((c) => c.text) ?? [])].join(" ").trim()));
      const type = get("Type")?.value ?? "";
      const status = get("Status")?.value ?? "";
      return conditionFault(type, status) ? { cells, extra: [], aspect: "fault", aspectLabel: `${type} ${status}` } : { cells, extra: [] };
    });
    return { k: "table", kind: "conditions", head, rows, wrapCol: cols.indexOf("Message"), extraCol: cols.length - 1 };
  };

  const nodeOf = (n: DNode, owner: string): VNode => {
    switch (n.t) {
      case "field": {
        const key = run(n.key);
        const values = valueRuns(n);
        const table = conditionsTable(n);
        return { k: "kv", key, values, children: table ? [table] : nodesOf(n.children, n.key) };
      }
      case "table": return tableOf(n, owner);
      case "item": return { k: "item", first: false, children: nodesOf(n.children, owner) };
      case "opaque": return { k: "opaque", lines: n.raw.map((l) => run(l.replace(/\r$/, ""))) };
      case "blank": return { k: "blank" };
      default: return { k: "line", run: run(n.text) };
    }
  };
  const nodesOf = (ns: DNode[], owner: string): VNode[] => {
    const out = ns.map((c) => nodeOf(c, owner));
    const first = out.find((v) => v.k === "item");
    if (first && first.k === "item") first.first = true;
    return out;
  };

  // Sections, in kubectl's order: the leading top-level fields are the
  // Overview; a field that earns a section is one; top-level fields between
  // sections form an unlabelled block.
  const sections: VSection[] = [];
  let loose: VSection | null = null;
  const earns = (n: DNode) =>
    n.t === "opaque" || (n.t === "field" && n.indent === 0 && (hasTable(n) || ANCHORS.has(n.key) || (n.children.length > 0 && lineCount(n) >= 4)));
  for (const n of tree.nodes) {
    if (!earns(n)) {
      if (n.t === "blank") continue;
      if (!loose) {
        loose = { id: sections.length === 0 ? "overview" : `s${sections.length}`, label: sections.length === 0 ? "Overview" : null, title: null, value: [], nodes: [], from: runs.length, to: runs.length };
        sections.push(loose);
      }
      loose.nodes.push(nodeOf(n, ""));
      loose.to = runs.length;
      continue;
    }
    loose = null;
    const from = runs.length;
    if (n.t === "opaque") {
      const nodes = [nodeOf(n, "")];
      sections.push({ id: `s${sections.length}`, label: n.title, title: null, value: [], nodes, from, to: runs.length });
      continue;
    }
    const f = n as DField;
    const title = run(f.key);
    const value = valueRuns(f);
    const table = conditionsTable(f);
    const nodes = table ? [table] : nodesOf(f.children, f.key);
    const sec: VSection = { id: `s${sections.length}`, label: f.key, title, value, nodes, from, to: runs.length };
    const tables = collectTables(sec.nodes);
    if (f.key === "Events") {
      const rows = tables.filter((t) => t.kind === "events").flatMap((t) => t.rows);
      sec.count = rows.length;
      const warnings = rows.filter((r) => r.aspect === "attention").length;
      if (warnings) Object.assign(sec, { aspect: "attention", aspectLabel: `${warnings} warning${warnings === 1 ? "" : "s"}` });
    }
    const faults = tables.filter((t) => t.kind === "conditions").flatMap((t) => t.rows).filter((r) => r.aspect === "fault").length;
    if (faults) Object.assign(sec, { aspect: "fault", aspectLabel: `${faults} condition${faults === 1 ? "" : "s"} at fault` });
    sections.push(sec);
  }

  const meta = tree.generic ? tree.nodes.find((x): x is DField => x.t === "field" && x.indent === 0 && x.key === "Metadata") : undefined;
  const uid = meta?.children.find((c): c is DField => c.t === "field" && c.key === "UID")?.value ?? null;
  return { sections, runs, uid: uid || null };
}

function collectTables(ns: VNode[]): VTable[] {
  const out: VTable[] = [];
  const walk = (n: VNode) => {
    if (n.k === "table") out.push(n);
    else if (n.k === "kv" || n.k === "item") n.children.forEach(walk);
  };
  ns.forEach(walk);
  return out;
}
