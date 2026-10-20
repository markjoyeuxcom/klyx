import type { DRAClaimDTO, DRAPoolDTO, DeviceHealthDTO, PodClaimRefDTO } from "../bridge/dra";
import { useDRAClaims, useDRAPools } from "../health/useDRARows";
import { InspectorBlock } from "../layout/InspectorLayout";
import { outcomeWords } from "./DRAView";
import { ClaimLink } from "./ClaimLink";

// Coverage S11b §5: the node inspector's DRA devices and the pod
// inspector's claims, joined from the live dra rows (never from a detail
// fetch alone), so a claim's or a pool's change re-renders them.

// NodeDRABlock lists the pools that name this node: its node-local view of
// each. Pools offered to all nodes or by a selector are in the DRA lens,
// since Klyx does not evaluate node selectors.
export function NodeDRABlock({ cluster, node }: { cluster: string; node: string }) {
  const pools = useDRAPools(cluster);
  const here = pools.rows.flatMap((p) => p.views.filter((v) => v.node === node).map((v) => ({ p, v })));
  if (pools.loading || (here.length === 0 && pools.rows.length === 0)) return null;
  // a pool row keeps its worst views only: a node it leaves out may be
  // among its slices (Codex's implementation review, finding 7)
  const cut = pools.rows.filter((p) => (p.viewsOmitted ?? 0) > 0 && !p.views.some((v) => v.node === node));
  const absent = cut.length > 0 ? `Not established: ${cut.length === 1 ? "a pool lists only its" : `${cut.length} pools list only their`} worst nodes, and this node is not among them (${cut.map((p) => p.key).join(", ")}).`
    : pools.complete ? "No slices name this node." : "No slices name this node in the current read, which is not whole.";
  const asOf = pools.held || pools.coverage.some((k) => k.coverage === "stale") ? ` As of ${pools.asOf ? new Date(pools.asOf).toLocaleTimeString() : "the last read"}.` : "";
  return (
    <InspectorBlock title="DRA devices on this node" count={here.length}>
      <div data-testid="node-dra">
        {here.length === 0 ? <div className="t-secondary">{absent}{asOf} Pools offered to all nodes or by a selector are in the DRA lens.</div>
          : here.map(({ p, v }) => <div key={p.key} className="t-body" data-testid="node-dra-pool" style={{ overflowWrap: "anywhere", padding: "2px 0" }}>
            <span style={{ fontFamily: "var(--font-mono)" }}>{p.driver} / {p.pool}</span> <span className="t-secondary">· {outcomeWords(v)}</span>
          </div>)}
        {here.length > 0 && cut.length > 0 && <div className="t-secondary">{cut.length === 1 ? "One more pool lists" : `${cut.length} more pools list`} only its worst nodes, so whether it names this node is not established.</div>}
        <div className="t-secondary" style={{ marginTop: 4 }}>Slices are the driver's: a node without them is "no slices", never "not registered".</div>
      </div>
    </InspectorBlock>
  );
}

const SOURCE: Record<string, string> = { claim: "claim", template: "from a template", extended: "the scheduler's claim for DRA-backed extended resources" };

// The unhealthy devices a health line names by the kubelet's ID (a CDI ID,
// never joined to driver/pool/device), so another device turning unhealthy
// at the same count reads differently.
const MAX_HEALTH_IDS = 4;

// healthOf reads a claim's device health by its status entry's name: the
// pod's claim name, or for the extended claim the claim's own name (Codex's
// implementation review, finding 3). The counts are over every device the
// kubelet reports; the IDs named are the kept ones.
function healthOf(health: DeviceHealthDTO[], name: string, bound: boolean, omitted: number): string {
  const mine = health.filter((h) => h.name === `claim:${name}` || h.name.startsWith(`claim:${name}/`));
  // S11b §3.4: no report is said, never read as healthy (the gate may be
  // off); a report Klyx cut is not established, never "no report"
  if (mine.length === 0) return omitted > 0 ? `device health not established: Klyx kept the pod's first ${health.length} reports, ${omitted} more not read` : bound ? "no device health reported" : "";
  const count = (h: DeviceHealthDTO, k: "unhealthy" | "unknown" | "healthy", health: string) => h[k] ?? h.devices.filter((d) => d.health === health).length;
  const bad = mine.reduce((n, h) => n + count(h, "unhealthy", "Unhealthy"), 0);
  const unknown = mine.reduce((n, h) => n + count(h, "unknown", "Unknown"), 0);
  const ok = mine.reduce((n, h) => n + count(h, "healthy", "Healthy"), 0);
  if (bad) {
    const ids = mine.flatMap((h) => h.devices).filter((d) => d.health === "Unhealthy").map((d) => d.id);
    const shown = ids.slice(0, MAX_HEALTH_IDS);
    const more = bad - shown.length > 0 ? `, +${bad - shown.length} more` : "";
    return `${bad} device${bad === 1 ? "" : "s"} unhealthy (reported by the kubelet): ${shown.join(", ")}${more}`;
  }
  if (unknown && !ok) return "health Unknown (reported by the kubelet)";
  return `${ok} healthy${unknown ? `, ${unknown} Unknown` : ""}`;
}

// PodClaimsBlock lists the claims a pod references (S11b §3.5), each joined
// to the live claims rows by namespace and name.
export function PodClaimsBlock(props: { cluster: string; namespace: string; podUid?: string; claims: PodClaimRefDTO[]; health: DeviceHealthDTO[]; healthOmitted?: number; unbound: boolean }) {
  // a pod without claims reads no DRA rows at all
  return props.claims.length === 0 ? null : <PodClaims {...props} />;
}

function PodClaims({ cluster, namespace, podUid, claims, health, healthOmitted = 0, unbound }: {
  cluster: string; namespace: string; podUid?: string; claims: PodClaimRefDTO[]; health: DeviceHealthDTO[]; healthOmitted?: number; unbound: boolean;
}) {
  const rows = useDRAClaims(cluster);
  const byName = new Map<string, DRAClaimDTO>(rows.rows.filter((c) => c.namespace === namespace).map((c) => [c.name, c]));
  const line = (ref: PodClaimRefDTO) => {
    if (ref.state === "none-needed") return "no claim was needed";
    if (ref.state === "not-created") return "not created yet";
    const c = ref.claim ? byName.get(ref.claim) : undefined;
    if (c && ref.source === "template") {
      // a name correlation is not ownership (Codex's implementation
      // review, finding 8): the claim is this pod's only when it owns it
      if (!c.ownerUid || !podUid) return "ownership not established: the claim's or the pod's identity is not read";
      if (c.ownerUid !== podUid) return `not this pod's claim: it is owned by ${c.ownerKind ?? "another object"} ${c.ownerName ?? ""}`.trimEnd();
    }
    if (c) return `${c.reading.label}${c.reading.message ? ` · ${c.reading.message}` : ""}`;
    if (!rows.complete) return "not established: the claims read is not current and whole";
    return unbound ? "does not exist: the scheduler waits for it without an event" : "does not exist";
  };
  return (
    <InspectorBlock title="Claims" count={claims.length}>
      <div data-testid="pod-claims">
        {claims.map((ref) => {
          const statusName = ref.source === "extended" ? ref.claim : ref.podClaim;
          const h = statusName && ref.state === "resolved" ? healthOf(health, statusName, !unbound, healthOmitted) : "";
          return (
            <div key={`${ref.source}/${ref.podClaim}/${ref.claim ?? ""}`} className="t-body" data-testid="pod-claim" style={{ overflowWrap: "anywhere", padding: "2px 0" }}>
              {ref.claim && byName.has(ref.claim) ? <ClaimLink cluster={cluster} namespace={namespace} name={ref.claim} />
                : <span style={{ fontFamily: "var(--font-mono)" }}>{ref.claim || ref.podClaim}</span>}
              <span className="t-secondary"> · {SOURCE[ref.source] ?? ref.source}{ref.podClaim && ref.claim && ref.claim !== ref.podClaim ? ` (the pod's "${ref.podClaim}")` : ""} · {line(ref)}</span>
              {h && <div className="t-secondary">{h}</div>}
            </div>
          );
        })}
        <div className="t-secondary" style={{ marginTop: 4 }}>A device's health is the kubelet's report; no report is not proof of a healthy device.</div>
      </div>
    </InspectorBlock>
  );
}

export type { DRAPoolDTO };
