import { reveal } from "../nav/reveal";

// ClaimLink names a ResourceClaim and opens it in the DRA lens (coverage
// S11b §5), styled as the pod inspector's owner link.
export function ClaimLink({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }) {
  return (
    <button type="button" data-testid="claim-link" title={`Open ResourceClaim ${namespace}/${name} in Devices (DRA)`}
      style={{ background: "none", border: 0, padding: 0, textAlign: "left", cursor: "pointer", color: "var(--color-text-info)", font: "inherit", fontFamily: "var(--font-mono)", overflowWrap: "anywhere" }}
      onClick={() => reveal(cluster, "dra", "claims", { gvr: "resource.k8s.io/v1/resourceclaims", namespace, name })}>
      {name}
    </button>
  );
}
