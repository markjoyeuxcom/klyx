import type { ExtendedDTO, ExtendedEntryDTO, ExtendedTotalDTO, ReservationDTO, ReservedDTO } from "../store/fleet";
import type { ExtendedMarks, Mark } from "../health/useExtendedRows";

// Coverage S11a §5, §6: the Nodes lens's extended resources, in words.
// Figures are decimal strings ("" absent, "-" not established), compared
// and subtracted with BigInt so a figure past 2^53 stays exact.

const digits = (s: string | undefined) => !!s && /^\d+$/.test(s);

/** big reads a figure; null when it is absent or not established. */
export function big(s: string | undefined): bigint | null {
  return digits(s) ? BigInt(s as string) : null;
}

/** figure words one figure: "not established" for "-", "not reported" for "". */
export function figure(s: string | undefined): string {
  if (s === "-") return "not established";
  if (!s) return "not reported";
  return s;
}

/** gapOf is capacity minus allocatable, when both are established and the
 * capacity is higher. */
export function gapOf(e: ExtendedEntryDTO): bigint | null {
  const c = big(e.capacity);
  const a = big(e.allocatable);
  return c !== null && a !== null && c > a ? c - a : null;
}

/** accelerators of a node, in the node's order. */
export const acceleratorsOf = (x: ExtendedDTO | undefined) => (x?.entries ?? []).filter((e) => e.accelerator);

/** reservedOf finds a node's reservation entry for one name. */
export const reservedOf = (r: ReservationDTO | undefined, name: string): ReservedDTO | undefined => r?.entries.find((e) => e.name === name);

/** requestedText is a node's requested figure with its qualification:
 * "?" when the reservations part has no current read, the node's row was
 * left out by the per-cluster bound, or the row has no entry for the name.
 * A row lists every name its node advertised in its own capture, so a
 * missing entry means another capture, never a zero (Codex's S11a
 * implementation review, finding 2): only the entry itself says 0. */
export function requestedText(r: ReservationDTO | undefined, name: string, mark: Mark, rowMissing: boolean): string {
  if (mark === "?" || rowMissing) return "?";
  const e = reservedOf(r, name);
  if (!e) return "?";
  return e.requested === "-" ? "?" : `${e.requested}${mark}`;
}

/** gpuCell is the GPU column for one node: one accelerator reads
 * "requested / allocatable" with its gap or "0 advertised"; several read
 * "N resources". "" when the node advertises none. */
export function gpuCell(x: ExtendedDTO | undefined, r: ReservationDTO | undefined, marks: ExtendedMarks, rowMissing: boolean): { text: string; title: string } {
  const acc = acceleratorsOf(x);
  if (acc.length === 0) return { text: "", title: "" };
  const line = (e: ExtendedEntryDTO) => {
    if (e.capacity === "0") return `${e.name}: 0 advertised`;
    const gap = gapOf(e);
    return `${e.name}: ${requestedText(r, e.name, marks.requested, rowMissing)} requested / ${figure(e.allocatable)}${marks.nodes} allocatable${gap !== null ? ` · ${gap} not allocatable` : ""}`;
  };
  // the node's entries are cut at 16, accelerators first: when every kept
  // entry is an accelerator and some were cut, more accelerators may exist
  // (Codex's S11a implementation review, finding 5)
  const cut = (x?.omitted ?? 0) > 0 && acc.length === (x?.entries ?? []).length;
  const title = acc.map(line).join("\n") + ((x?.omitted ?? 0) > 0 ? `\n${x?.omitted} more extended resource${x?.omitted === 1 ? "" : "s"} not shown` : "");
  if (acc.length > 1 || cut) return { text: `${acc.length}${cut ? "+" : ""} resources`, title };
  const e = acc[0];
  if (e.capacity === "0") return { text: "0 advertised", title };
  const gap = gapOf(e);
  return { text: `${requestedText(r, e.name, marks.requested, rowMissing)} / ${figure(e.allocatable)}${marks.nodes}${gap !== null ? ` · ${gap} not allocatable` : ""}`, title };
}

/** markWords says a qualification in words. */
export function markWords(mark: Mark, what: string): string {
  switch (mark) {
    case "?": return `${what} not established: the read it depends on is not current`;
    case "*": return `${what} from the last observed read`;
    case "+": return `${what} is a lower bound: the pods read is capped`;
    case "+*": return `${what} is a lower bound, from the last observed read`;
  }
  return "";
}

/** factLabel names a vendor fact's key. */
export function factLabel(key: string): string {
  switch (key) {
    case "memory": return "memory (MiB)";
    case "product-name": return "product";
    case "vram": return "VRAM";
    case "compute-memory-partition": return "partition";
    case "driver-version": return "driver";
    case "sharing-strategy": return "sharing";
  }
  return key;
}

/** totalsText is the footer's totals: at most `max` names shown, every kept
 * name in the title, each qualified by its own reads; names past the totals
 * bound are counted, never summed from the displayed rows. */
export function totalsText(totals: ExtendedTotalDTO[], marks: ExtendedMarks, omitted: number, max = 2): { text: string; title: string } {
  if (totals.length === 0 && omitted === 0) return { text: "", title: "" };
  const one = (t: ExtendedTotalDTO) =>
    `${figure(t.allocatable)}${marks.nodes} ${t.name} allocatable (${t.requested === "-" || marks.requested === "?" ? "?" : `${t.requested}${marks.requested}`} requested)`;
  const parts = totals.slice(0, max).map(one);
  if (totals.length > max) parts.push(`${totals.length - max} more`);
  if (omitted > 0) parts.push(`totals for ${omitted} more resource${omitted === 1 ? "" : "s"} not shown`);
  return { text: parts.join(" · "), title: totals.map(one).join("\n") };
}
