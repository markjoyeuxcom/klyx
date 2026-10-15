import { useEffect, useMemo } from "react";
import { create } from "zustand";
import { useFleet, type ExpiryScanResultDTO } from "./fleet";
import { getExpiryScan } from "../bridge/expiry";
import { envRank } from "../nav/stationOrder";

// The fleet's expiry scans for this session (The Line slice 5 §6), shared by
// the fleet screen's Horizon and the Expiry view: each cluster is scanned
// once; a cluster that joins mid-session is scanned once when it joins, and
// the others are never rescanned for it; a cluster that leaves drops its
// result. Refresh starts over. A late reply from before a Refresh, or for a
// cluster that left, never lands.
type State = { results: Record<string, ExpiryScanResultDTO>; asked: Record<string, number>; gen: number; readAt: number | null };
export const useExpiry = create<State>(() => ({ results: {}, asked: {}, gen: 0, readAt: null }));

export function resetExpiryStore(): void {
  useExpiry.setState({ results: {}, asked: {}, gen: 0, readAt: null });
}

function scan(name: string, gen: number) {
  // a rejected scan is a named failure, never a hang
  void getExpiryScan(name).catch((e: unknown): ExpiryScanResultDTO => ({ items: [], warnings: [], error: e instanceof Error ? e.message : "expiry scan failed" })).then((r) => {
    const st = useExpiry.getState();
    if (st.gen !== gen || st.asked[name] !== gen) return;
    useExpiry.setState({ results: { ...st.results, [name]: r }, readAt: Date.now() });
  });
}

// ensureExpiryScans scans every listed cluster not yet asked in this session,
// and drops the results of clusters no longer listed.
export function ensureExpiryScans(names: string[]): void {
  const st = useExpiry.getState();
  const keep = new Set(names);
  const results = Object.fromEntries(Object.entries(st.results).filter(([n]) => keep.has(n)));
  const asked = Object.fromEntries(Object.entries(st.asked).filter(([n]) => keep.has(n)));
  const fresh = names.filter((n) => !(n in asked));
  for (const n of fresh) asked[n] = st.gen;
  useExpiry.setState({ results, asked });
  for (const n of fresh) scan(n, st.gen);
}

export function refreshExpiryScans(names: string[]): void {
  const gen = useExpiry.getState().gen + 1;
  useExpiry.setState({ results: {}, asked: Object.fromEntries(names.map((n) => [n, gen])), gen, readAt: null });
  for (const n of names) scan(n, gen);
}

export type ExpiryMember = { name: string; env: string; region: string };

// useExpiryScans ensures the current fleet is scanned and returns the
// shared results, members in station rank (dev first).
export function useExpiryScans(): { members: ExpiryMember[]; results: Record<string, ExpiryScanResultDTO>; pending: number; readAt: number | null; refresh: () => void } {
  const clusters = useFleet((s) => s.clusters);
  const key = clusters.map((c) => c.name).sort().join("\n");
  const members = useMemo(() => clusters.map((c) => ({ name: c.name, env: c.env, region: c.region }))
    .sort((a, b) => envRank((a.env ?? "").trim().toLowerCase()) - envRank((b.env ?? "").trim().toLowerCase()) || a.name.localeCompare(b.name)),
  // eslint-disable-next-line react-hooks/exhaustive-deps
  [key]);
  useEffect(() => { ensureExpiryScans(members.map((m) => m.name)); }, [members]);
  const results = useExpiry((s) => s.results);
  const readAt = useExpiry((s) => s.readAt);
  return {
    members,
    results,
    pending: members.filter((m) => !(m.name in results)).length,
    readAt,
    refresh: () => refreshExpiryScans(members.map((m) => m.name)),
  };
}
