import { useEffect, useState } from "react";
import { useFleet, WorkloadCompareDTO } from "../store/fleet";
import { workloadCompare } from "../bridge/workloads";
import { btn } from "../chrome/primitives";
import { IconX } from "@tabler/icons-react";

// FleetCompare is the cross-environment drift view: the same workload
// (kind/namespace/name identity) side-by-side across every connected cluster,
// ordered dev -> stg -> prd, with drift AGAINST THE ORIGIN CLUSTER highlighted.
// Read-only. Columns render as each cluster answers; a failed lookup renders as
// degraded (its error), never as "not present".
//
// Drift semantics (vs the origin column):
//   - image differs        -> warning on that container row
//   - replicas differ      -> info (often intentional: prd runs more)
//   - Flux revision differs-> warning (the "prd is behind" answer)
//   - spec hash differs while images+replicas match -> amber "other spec drift"
//     (env, volumes, probes, resources... - the hash keeps it honest)

const envRank: Record<string, number> = { dev: 0, development: 0, stg: 1, staging: 1, prd: 2, prod: 2, production: 2 };

function rankOfEnv(env: string): number {
  const r = envRank[env.toLowerCase()];
  return r === undefined ? 3 : r;
}

type Column = { cluster: string; env: string; region: string; result: WorkloadCompareDTO | null };

export function FleetCompare({ origin, kind, namespace, name, onClose }: {
  origin: string; kind: string; namespace: string; name: string; onClose: () => void;
}) {
  const clusters = useFleet((s) => s.clusters);
  const ordered = [...clusters].sort((a, b) =>
    rankOfEnv(a.env) - rankOfEnv(b.env) || a.env.localeCompare(b.env) || a.name.localeCompare(b.name));
  const [results, setResults] = useState<Record<string, WorkloadCompareDTO | null>>({});

  useEffect(() => {
    let live = true;
    setResults({});
    for (const c of clusters) {
      void workloadCompare(c.name, kind, namespace, name).then((r) => {
        if (live) setResults((prev) => ({ ...prev, [c.name]: r }));
      }).catch(() => {
        if (live) setResults((prev) => ({ ...prev, [c.name]: { found: false, kind, namespace, name, desiredReplicas: 0, readyReplicas: 0, containers: [], specHash: "", ownerKind: "", ownerNamespace: "", ownerName: "", ownerRevision: "", error: "compare call failed" } }));
      });
    }
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, namespace, name]);

  const columns: Column[] = ordered.map((c) => ({ cluster: c.name, env: c.env, region: c.region, result: results[c.name] ?? null }));
  const base = results[origin];

  const tone = (drifted: boolean, color: string) => (drifted ? color : "var(--color-text-primary)");

  return (
    <div role="dialog" aria-label="fleet compare" style={{ position: "fixed", inset: 0, background: "color-mix(in srgb, var(--color-background-primary) 72%, transparent)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 60 }}>
      <div style={{ width: "min(92vw, 1180px)", maxHeight: "88vh", overflow: "auto", background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-secondary)", borderRadius: "var(--border-radius-md)", padding: 16, display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <div style={{ fontSize: 13, fontWeight: 600 }}>Fleet compare</div>
          <div style={{ fontFamily: "var(--font-mono)", fontSize: 12, color: "var(--color-text-secondary)" }}>{kind} {namespace}/{name}</div>
          <div style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>drift vs {origin}</div>
          <div style={{ flex: 1 }} />
          <button onClick={onClose} aria-label="close fleet compare" style={{ ...btn, display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
        </div>

        <div style={{ display: "grid", gridTemplateColumns: `repeat(${columns.length}, minmax(220px, 1fr))`, gap: 8, alignItems: "start" }}>
          {columns.map((col) => {
            const r = col.result;
            const isOrigin = col.cluster === origin;
            return (
              <div key={col.cluster} data-testid={`compare-col-${col.cluster}`} style={{ display: "flex", flexDirection: "column", gap: 8, padding: "10px 12px", borderRadius: 4, border: `0.5px solid var(--color-border-${isOrigin ? "secondary" : "tertiary"})`, background: "var(--color-background-secondary)" }}>
                <div style={{ display: "flex", alignItems: "baseline", gap: 6, minWidth: 0 }}>
                  <span style={{ fontFamily: "var(--font-mono)", fontSize: 12, fontWeight: 600, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{col.cluster}</span>
                  {col.env && <span style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>{col.env}</span>}
                  {col.region && <span style={{ fontSize: 9, color: "var(--color-text-tertiary)" }}>{col.region}</span>}
                  {isOrigin && <span style={{ fontSize: 9, color: "var(--color-text-info)" }}>origin</span>}
                </div>

                {r === null ? (
                  <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>loading…</div>
                ) : r.error !== "" ? (
                  <div style={{ fontSize: 11, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>check failed: {r.error}</div>
                ) : !r.found ? (
                  <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>not present on this cluster</div>
                ) : (
                  <div style={{ display: "flex", flexDirection: "column", gap: 6, fontFamily: "var(--font-mono)", fontSize: 11 }}>
                    <div>
                      <span style={{ color: "var(--color-text-tertiary)" }}>replicas </span>
                      <span style={{ color: tone(!!base?.found && base.desiredReplicas !== r.desiredReplicas, "var(--color-text-info)") }}>
                        {r.readyReplicas}/{r.desiredReplicas}
                      </span>
                    </div>
                    {r.containers.map((ct) => {
                      const baseCt = base?.found ? base.containers.find((b) => b.name === ct.name) : undefined;
                      const imageDrift = !!baseCt && baseCt.image !== ct.image;
                      const resDrift = !!baseCt && baseCt.resources !== ct.resources;
                      return (
                        <div key={ct.name} style={{ display: "flex", flexDirection: "column", gap: 1, minWidth: 0 }}>
                          <span style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>{ct.name}</span>
                          <span title={ct.image} style={{ color: tone(imageDrift, "var(--color-text-warning)"), overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{ct.image}</span>
                          {ct.resources && <span style={{ fontSize: 10, color: resDrift ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>{ct.resources}</span>}
                        </div>
                      );
                    })}
                    {r.ownerKind !== "" && (
                      <div style={{ display: "flex", flexDirection: "column", gap: 1, minWidth: 0 }}>
                        <span style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>gitops</span>
                        <span style={{ color: "var(--color-text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{r.ownerKind} {r.ownerNamespace ? `${r.ownerNamespace}/` : ""}{r.ownerName}</span>
                        {r.ownerRevision && (
                          <span title={r.ownerRevision} style={{ color: tone(!!base?.found && base.ownerRevision !== r.ownerRevision, "var(--color-text-warning)"), overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{r.ownerRevision}</span>
                        )}
                      </div>
                    )}
                    {base?.found && r.specHash !== base.specHash &&
                      r.containers.every((ct) => base.containers.find((b) => b.name === ct.name)?.image === ct.image) &&
                      r.containers.every((ct) => base.containers.find((b) => b.name === ct.name)?.resources === ct.resources) &&
                      base.containers.length === r.containers.length && (
                      <div style={{ fontSize: 10, color: "var(--color-text-warning)" }}>
                        other spec drift (env, volumes, probes, …) — templates differ beyond images/resources
                      </div>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>

        <div style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>
          Same kind/namespace/name across the fleet · read-only · drift highlighted against the origin column
        </div>
      </div>
    </div>
  );
}
