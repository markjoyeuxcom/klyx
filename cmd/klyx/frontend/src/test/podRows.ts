import type { HealthLike } from "../health/verdict";
import type { PodRowDTO, PodSummaryDTO } from "../store/fleet";

// rowOf is the rows transport's bounded pod (Go's toPodRowDTO, 1c slice 3
// addendum §3.2): the summary without its verdict message, its containers'
// detail, its relative age or the legacy rank/reason/phase. Tests seed rows
// with it and keep the full summary for the detail.
export function rowOf(p: PodSummaryDTO): PodRowDTO {
  const health = p.health ? (Object.fromEntries(Object.entries(p.health).filter(([k]) => k !== "message")) as HealthLike) : undefined;
  return {
    namespace: p.namespace, name: p.name, uid: p.uid ?? "", gvr: p.gvr, generation: p.generation,
    ownerKind: p.ownerKind, ownerName: p.ownerName, ownerUid: p.ownerUid, node: p.node, ip: p.ip,
    ready: p.ready, restarts: p.restarts, createdUnix: p.createdUnix, health,
    containers: p.containers.map((c) => ({ name: c.name, init: c.init, ready: c.ready, restarts: c.restarts, state: c.state })),
  };
}
