import type { DRAClaimDTO, DRAPoolDTO, DeviceHealthDTO, PodClaimRefDTO } from "../bridge/dra";
import { useDRAClaims, useDRAPools } from "../health/useDRARows";
import { InspectorBlock } from "../layout/InspectorLayout";
import { outcomeWords } from "./DRAView";

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
  return (
    <InspectorBlock title="DRA devices on this node" count={here.length}>
      <div data-testid="node-dra">
        {here.length === 0 ? <div className="t-secondary">{pools.complete ? "No slices name this node." : "No slices name this node in the current read, which is not whole."} Pools offered to all nodes or by a selector are in the DRA lens.</div>
          : here.map(({ p, v }) => <div key={p.key} className="t-body" data-testid="node-dra-pool" style={{ overflowWrap: "anywhere", padding: "2px 0" }}>
            <span style={{ fontFamily: "var(--font-mono)" }}>{p.driver} / {p.pool}</span> <span className="t-secondary">· {outcomeWords(v)}</span>
          </div>)}
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

function healthOf(health: DeviceHealthDTO[], podClaim: string, bound: boolean): string {
  const mine = health.filter((h) => h.name === `claim:${podClaim}` || h.name.startsWith(`claim:${podClaim}/`));
  // S11b §3.4: no report is said, never read as healthy (the gate may be off)
  if (mine.length === 0) return bound ? "no device health reported" : "";
  const devs = mine.flatMap((h) => h.devices);
  const bad = devs.filter((d) => d.health === "Unhealthy");
  const unknown = devs.filter((d) => d.health === "Unknown").length;
  if (bad.length) {
    const ids = bad.slice(0, MAX_HEALTH_IDS).map((d) => d.id).join(", ");
    const more = bad.length > MAX_HEALTH_IDS ? `, +${bad.length - MAX_HEALTH_IDS} more` : "";
    return `${bad.length} device${bad.length === 1 ? "" : "s"} unhealthy (reported by the kubelet): ${ids}${more}`;
  }
  if (unknown === devs.length) return "health Unknown (reported by the kubelet)";
  return `${devs.length - unknown} healthy${unknown ? `, ${unknown} Unknown` : ""}`;
}

// PodClaimsBlock lists the claims a pod references (S11b §3.5), each joined
// to the live claims rows by namespace and name.
export function PodClaimsBlock(props: { cluster: string; namespace: string; claims: PodClaimRefDTO[]; health: DeviceHealthDTO[]; unbound: boolean }) {
  // a pod without claims reads no DRA rows at all
  return props.claims.length === 0 ? null : <PodClaims {...props} />;
}

function PodClaims({ cluster, namespace, claims, health, unbound }: {
  cluster: string; namespace: string; claims: PodClaimRefDTO[]; health: DeviceHealthDTO[]; unbound: boolean;
}) {
  const rows = useDRAClaims(cluster);
  const byName = new Map<string, DRAClaimDTO>(rows.rows.filter((c) => c.namespace === namespace).map((c) => [c.name, c]));
  const line = (ref: PodClaimRefDTO) => {
    if (ref.state === "none-needed") return "no claim was needed";
    if (ref.state === "not-created") return "not created yet";
    const c = ref.claim ? byName.get(ref.claim) : undefined;
    if (c) return `${c.reading.label}${c.reading.message ? ` · ${c.reading.message}` : ""}`;
    if (!rows.complete) return "not established: the claims read is not current and whole";
    return unbound ? "does not exist: the scheduler waits for it without an event" : "does not exist";
  };
  return (
    <InspectorBlock title="Claims" count={claims.length}>
      <div data-testid="pod-claims">
        {claims.map((ref) => {
          const h = ref.podClaim && ref.state === "resolved" ? healthOf(health, ref.podClaim, !unbound) : "";
          return (
            <div key={`${ref.source}/${ref.podClaim}/${ref.claim ?? ""}`} className="t-body" data-testid="pod-claim" style={{ overflowWrap: "anywhere", padding: "2px 0" }}>
              <span style={{ fontFamily: "var(--font-mono)" }}>{ref.claim || ref.podClaim}</span>
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
