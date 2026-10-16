import { useMemo, useState } from "react";
import { IconChevronRight } from "@tabler/icons-react";
import { Age } from "./Age";
import { ClampedText } from "./ClampedText";
import { Arr, Num, Obj, type JValue } from "./losslessJSON";

// ObjectTree draws an object's own fields as a tree (The Line slice 15b):
// real structure from the object's JSON. The work is bounded, not only the
// bytes (design review 1): the visible rows are computed in one pure pass
// (flattenTree) that only descends into open containers, draws at most
// CHILD_PAGE children per container (each "Show more" adds one bounded
// page), stops the whole tree at MAX_ROWS rows and at MAX_DEPTH levels, and
// says every omission. Values are lossless (losslessJSON): numbers are the
// server's own text, and a container is read by index - a page is a slice
// and a size a length, never a pass over every child (implementation
// review 1).

export const CHILD_PAGE = 100;
export const MAX_ROWS = 2000;
export const MAX_DEPTH = 12;
const OPEN_MAX_CHILDREN = 20;

// A schema path: a property, an array's items or a map's values (slice 15c).
export type PathSeg = { k: "p" | "i" | "m"; n?: string };
export type TitleOf = (path: PathSeg[]) => string | undefined;

type Container = Arr | Obj;
export type TreeRow =
  | { t: "field"; id: string; depth: number; label: string; item: boolean; path: PathSeg[]; value: JValue }
  | { t: "container"; id: string; depth: number; label: string; item: boolean; path: PathSeg[]; size: number; array: boolean; open: boolean }
  | { t: "more"; id: string; depth: number; hidden: number }
  | { t: "deeper"; id: string; depth: number }
  | { t: "stop"; id: string; depth: number };

const isContainer = (v: JValue): v is Container => v instanceof Arr || v instanceof Obj;
const sizeOf = (v: Container) => (v instanceof Arr ? v.items.length : v.entries.length);
// childAt reads one child; an array item's key is its index
const childAt = (v: Container, i: number): [string, JValue] => (v instanceof Arr ? [String(i), v.items[i]] : v.entries[i]);

function childPath(path: PathSeg[], parent: Container, key: string, titleOf?: TitleOf): PathSeg[] {
  if (parent instanceof Arr) return [...path, { k: "i" }];
  const prop: PathSeg[] = [...path, { k: "p", n: key }];
  // a key the schema does not name as a property is a map value
  if (titleOf && titleOf(prop) === undefined && titleOf([...path, { k: "m" }]) !== undefined) return [...path, { k: "m" }];
  return prop;
}

// flattenTree lists the rows to draw. toggles overrides the default open
// state per row id; pages holds each container's drawn child count.
export function flattenTree(root: JValue, base: PathSeg[], toggles: Map<string, boolean>, pages: Map<string, number>, titleOf?: TitleOf): TreeRow[] {
  const rows: TreeRow[] = [];
  let stopped = false;
  const walk = (value: Container, id: string, path: PathSeg[], depth: number) => {
    const total = sizeOf(value);
    const shown = Math.min(total, pages.get(id) ?? CHILD_PAGE);
    const array = value instanceof Arr;
    for (let i = 0; i < shown; i++) {
      if (rows.length >= MAX_ROWS) {
        if (!stopped) rows.push({ t: "stop", id: `${id}/__stop`, depth });
        stopped = true;
        return;
      }
      const [k, v] = childAt(value, i);
      const cid = `${id}/${JSON.stringify(k)}`;
      const p = childPath(path, value, k, titleOf);
      const label = array ? String(i + 1) : k;
      if (isContainer(v) && sizeOf(v) > 0) {
        const size = sizeOf(v);
        const open = toggles.get(cid) ?? (depth < 2 && size <= OPEN_MAX_CHILDREN);
        rows.push({ t: "container", id: cid, depth, label, item: array, path: p, size, array: v instanceof Arr, open });
        if (open) {
          if (depth + 1 >= MAX_DEPTH) rows.push({ t: "deeper", id: `${cid}/__deeper`, depth: depth + 1 });
          else walk(v, cid, p, depth + 1);
          if (stopped) return;
        }
      } else {
        rows.push({ t: "field", id: cid, depth, label, item: array, path: p, value: v });
      }
    }
    if (total > shown && !stopped) rows.push({ t: "more", id: `${id}/__more`, depth, hidden: total - shown });
  };
  if (isContainer(root)) walk(root, "", base, 0);
  return rows;
}

const NO_BASE: PathSeg[] = [];

export function ObjectTree({ value, base = NO_BASE, titleOf }: { value: JValue; base?: PathSeg[]; titleOf?: TitleOf }) {
  const [toggles, setToggles] = useState<Map<string, boolean>>(() => new Map());
  const [pages, setPages] = useState<Map<string, number>>(() => new Map());
  const rows = useMemo(() => flattenTree(value, base, toggles, pages, titleOf), [value, base, toggles, pages, titleOf]);
  const toggle = (id: string, open: boolean) => setToggles((m) => new Map(m).set(id, !open));
  const more = (id: string) => {
    const parent = id.slice(0, -"/__more".length);
    setPages((m) => new Map(m).set(parent, (m.get(parent) ?? CHILD_PAGE) + CHILD_PAGE));
  };
  return (
    <div className="otree" role="tree">
      {rows.map((r) => (
        <div key={r.id} className="otree-row" style={{ ["--depth" as string]: r.depth }} role="treeitem" aria-level={r.depth + 1}
          aria-expanded={r.t === "container" ? r.open : undefined}>
          {r.t === "field" && (
            <>
              <span className={r.item ? "otree-key otree-index" : "otree-key"} title={titleOf?.(r.path)}>{r.label}</span>
              <span className="otree-val"><ScalarValue value={r.value} /></span>
            </>
          )}
          {r.t === "container" && (
            <>
              <span className={r.item ? "otree-key otree-index" : "otree-key"} title={titleOf?.(r.path)}>{r.label}</span>
              <button className="otree-toggle" aria-expanded={r.open} aria-label={`${r.open ? "Collapse" : "Expand"} ${r.label}`} onClick={() => toggle(r.id, r.open)}>
                <IconChevronRight size={13} stroke={1.5} className="otree-chev" aria-hidden />
                <span className="describe-muted">{r.size} {r.array ? (r.size === 1 ? "item" : "items") : (r.size === 1 ? "field" : "fields")}</span>
              </button>
            </>
          )}
          {r.t === "more" && (
            <button className="otree-more" onClick={() => more(r.id)}>Show {Math.min(CHILD_PAGE, r.hidden)} more ({r.hidden.toLocaleString()} not shown)</button>
          )}
          {r.t === "deeper" && <span className="otree-note">Nested deeper; see Manifest.</span>}
          {r.t === "stop" && <span className="otree-note" data-testid="otree-stopped">The tree stops at {MAX_ROWS.toLocaleString()} rows; the rest is in Manifest.</span>}
        </div>
      ))}
    </div>
  );
}

const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

// ScalarValue draws one leaf: a number as the server wrote it, an empty
// container as its brackets.
export function ScalarValue({ value }: { value: JValue }) {
  if (value === null) return <span className="describe-muted">null</span>;
  if (value instanceof Num) return <span className="otree-num">{value.text}</span>;
  if (value instanceof Arr) return <span className="describe-muted">[]</span>;
  if (value instanceof Obj) return <span className="describe-muted">{"{}"}</span>;
  if (typeof value === "string") {
    if (value === "") return <span className="describe-muted">""</span>;
    if (RFC3339.test(value)) return <>{value}<span className="describe-age"> · <Age iso={value} suffix=" ago" /></span></>;
    if (value.length > 200 || value.includes("\n")) return <ClampedText text={value} lines={3} />;
    return <>{value}</>;
  }
  return <>{String(value)}</>;
}
