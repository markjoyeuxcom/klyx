import { useMemo } from "react";
import type { ExtendedTotalDTO, ReservationDTO } from "../store/fleet";
import type { HealthPart } from "../store/health";
import { useRows } from "./useRows";

// Coverage S11a §2, §6: the extended task's two evidence parts, read by the
// Nodes lens alone. Each figure is qualified by the reads it depends on
// (§2.1): allocatable and capacity by the nodes read; requested and not
// advertised by both the nodes and the pods reads; served by DRA by the pods
// read alone.

// Mark is a figure's qualification: "?" not established, "+" a lower bound
// (a capped read), "*" last observed, "+*" both, "" current and whole.
export type Mark = "" | "?" | "+" | "*" | "+*";

type Kind = { kind: string; coverage: string; truncated?: boolean };

/** markOf qualifies a figure by the kinds it reads: unestablished when any
 * has no current or stale read, else "+" for a capped read and "*" for a
 * stale one, kept together. */
export function markOf(kinds: (Kind | undefined)[]): Mark {
  let lower = false;
  let stale = false;
  for (const k of kinds) {
    if (!k || (k.coverage !== "live" && k.coverage !== "stale")) return "?";
    if (k.truncated) lower = true;
    if (k.coverage === "stale") stale = true;
  }
  return `${lower ? "+" : ""}${stale ? "*" : ""}` as Mark;
}

export type ExtendedMarks = {
  // allocatable and capacity: the nodes read
  nodes: Mark;
  // requested and not advertised: the nodes and pods reads
  requested: Mark;
  // served by DRA: the pods read
  dra: Mark;
};

/** marksOf qualifies a part's figures by the kinds its capture read; held
 * rows (a newer read pending or failed) are as of their own capture, so an
 * established mark gains "*" (Codex's S11a implementation review,
 * finding 1). */
export function marksOf(part: HealthPart | null, held = false): ExtendedMarks {
  if (!part) return { nodes: "?", requested: "?", dra: "?" };
  const kind = (k: string) => part.kinds.find((x) => x.kind === k);
  const h = (m: Mark): Mark => (!held || m === "?" || m.includes("*") ? m : (`${m}*` as Mark));
  return { nodes: h(markOf([kind("nodes")])), requested: h(markOf([kind("nodes"), kind("pods")])), dra: h(markOf([kind("pods")])) };
}

/** PartState is one extended part's own subscription state: the open's
 * failure, a failed refresh, and whether its rows are held behind a newer
 * read (as of their capture). */
export type PartState = { error: string | null; refreshError: string | null; held: boolean; asOf: string | null };

type Sub = { part: HealthPart | null; error: string | null; refreshError: string | null; evaluatedAt: string | null; counts: { updating?: boolean; refreshing?: boolean } };
const stateOf = (r: Sub): PartState => ({ error: r.error, refreshError: r.refreshError,
  held: !!r.part && (!!r.counts.updating || !!r.counts.refreshing || !!r.refreshError), asOf: r.evaluatedAt });

export type ExtendedRows = {
  reservations: Map<string, ReservationDTO>; // by node UID
  reservationMarks: ExtendedMarks;
  // nodes whose rows the per-cluster bound left out (Evidence - rows)
  reservationsDropped: number;
  totals: ExtendedTotalDTO[];
  totalMarks: ExtendedMarks;
  // accelerator names past the totals bound
  totalsOmitted: number;
  // a current part with rows of its own
  reservationsRead: boolean;
  totalsRead: boolean;
  // each part's own subscription state (finding 1)
  reservationsState: PartState;
  totalsState: PartState;
};

export function useExtendedRows(cluster: string | null): ExtendedRows {
  const res = useRows<ReservationDTO>(cluster, "extended", "reservations");
  const tot = useRows<ExtendedTotalDTO>(cluster, "extended", "totals");
  return useMemo(() => {
    const rows = res.part ? res.rows ?? [] : [];
    const totals = tot.part ? tot.rows ?? [] : [];
    const reservations = new Map<string, ReservationDTO>();
    for (const r of rows) reservations.set(r.uid, r);
    const rs = stateOf(res);
    const ts = stateOf(tot);
    return {
      reservations,
      reservationMarks: marksOf(res.part, rs.held),
      reservationsDropped: res.part?.evidence != null ? Math.max(0, res.part.evidence - rows.length) : 0,
      totals,
      totalMarks: marksOf(tot.part, ts.held),
      totalsOmitted: tot.part?.evidence != null ? Math.max(0, tot.part.evidence - totals.length) : 0,
      reservationsRead: !!res.part,
      totalsRead: !!tot.part,
      reservationsState: rs,
      totalsState: ts,
    };
    // every state the figures are qualified by (finding 1)
  }, [res.rows, res.part, res.error, res.refreshError, res.evaluatedAt, res.counts.updating, res.counts.refreshing,
    tot.rows, tot.part, tot.error, tot.refreshError, tot.evaluatedAt, tot.counts.updating, tot.counts.refreshing]);
}
