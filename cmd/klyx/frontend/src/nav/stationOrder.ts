import type { ClusterDTO } from "../store/fleet";

// Station order (The Line slice 4 §2.2): six similar AKS names read best
// grouped dev → test → stg → prd, each group in fleet order. Unknown
// environments follow alphabetically, then clusters with none. With no
// environment tags, or one environment for all, the fleet is one flat list.
// The order is the frontend's own, never the LineDTO's placement: it must
// hold before the first DTO and for clusters with no Observer. Go's
// internal/line env_parity_test.go pins this table to line.EnvRank.
const ENV_RANK: [RegExp, number][] = [
  [/^(dev|develop|development)$/, 0],
  [/^(test|testing|qa|int|integration|uat)$/, 1],
  [/^(stg|stage|staging|preprod|pre-prod)$/, 2],
  [/^(prd|prod|production)$/, 3],
];
const envRank = (env: string) => ENV_RANK.find(([re]) => re.test(env))?.[1] ?? 4;

export type StationGroup = { env: string | null; clusters: ClusterDTO[] };

export function stationGroups(clusters: ClusterDTO[]): StationGroup[] {
  if (!clusters.some((c) => c.env)) return [{ env: null, clusters }];
  const by = new Map<string, ClusterDTO[]>();
  for (const c of clusters) {
    const env = (c.env ?? "").trim().toLowerCase();
    by.set(env, [...(by.get(env) ?? []), c]);
  }
  // One environment for the whole fleet is no grouping: no label.
  if (by.size === 1) return [{ env: null, clusters }];
  return [...by.entries()]
    .sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : envRank(a) - envRank(b) || a.localeCompare(b)))
    .map(([env, cs]) => ({ env: env || "other", clusters: cs }));
}

// stationOrder is the order mod+1..9 follow, and the station switcher's.
export function stationOrder(clusters: ClusterDTO[]): ClusterDTO[] {
  return stationGroups(clusters).flatMap((g) => g.clusters);
}

// stationLabel names a station as the fleet indicator and the switcher list
// it: "env · region" when either is set, then the cluster's name.
export function stationLabel(c: Pick<ClusterDTO, "name" | "env" | "region">): { place: string; name: string } {
  const place = [c.env?.trim(), c.region?.trim()].filter(Boolean).join(" · ");
  return { place, name: c.name };
}
