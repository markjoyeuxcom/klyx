import type { ClusterDTO, ClusterSection } from "../store/fleet";

function message(section: ClusterSection, c: ClusterDTO): string {
  switch (section) {
    case "gitops":
      return c.gitopsTier === "Absent"
        ? "No Flux or Argo installed on this cluster."
        : "GitOps reconciliation + inline drift arrives in M3.";
    case "gatewayApi":
      return c.networkTier === "Absent"
        ? "No Gateway API or Cilium installed on this cluster."
        : "Gateway topology arrives in M5.";
    case "flows":
      return "";
    case "resources":
      return "";
    case "crds":
      return "";
    case "overview":
      return "";
    case "helm":
      return "";
    case "argo":
      return "";
    case "certs":
      return "";
    case "externalsecrets":
      return "";
    case "workloads":
      return "";
    case "pods":
      return "";
    case "events":
      return "";
    case "nodes":
      return "";
    case "access":
      return "";
    case "upgrade":
      return "";
    case "connectivity":
      return "";
    case "karpenter":
      return "";
  }
}

export function Placeholder({ section, c }: { section: ClusterSection; c: ClusterDTO }) {
  return (
    <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>
      {message(section, c)}
    </div>
  );
}
