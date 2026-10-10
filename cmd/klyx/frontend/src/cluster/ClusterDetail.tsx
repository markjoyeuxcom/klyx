import { useFleet } from "../store/fleet";
import { Overview } from "./Overview";
import { GitOps } from "./GitOps";
import { GitOpsLegacy } from "./GitOpsLegacy";
import { ArgoView } from "./ArgoView";
import { ArgoViewLegacy } from "./ArgoViewLegacy";
import { HelmView } from "./HelmView";
import { CertsView } from "./CertsView";
import { CertsViewLegacy } from "./CertsViewLegacy";
import { ExternalSecretsView } from "./ExternalSecretsView";
import { CRDBrowser } from "./CRDBrowser";
import { BuiltinsView } from "./BuiltinsView";
import { InstanceList } from "./InstanceList";
import { InstanceListLegacy } from "./InstanceListLegacy";
import { GatewayApiView } from "./GatewayApiView";
import { WorkloadsView } from "./WorkloadsView";
import { WorkloadsViewLegacy } from "./WorkloadsViewLegacy";
import { PodsView } from "./PodsView";
import { PodsViewLegacy } from "./PodsViewLegacy";

// Reversible rollout switches for the 2026-10 workspace redesign: setting
// localStorage "klyx.layout.v1.legacy<Lens>" = "1" (Pods, Nodes, Workloads)
// falls back to that lens's old implementation as a whole - never a mix of
// layout owners. Each is removed once its migration is accepted.
function legacyLens(lens: "Pods" | "Nodes" | "Workloads" | "Resources" | "Flux" | "Argo" | "Certs"): boolean {
  try { return localStorage.getItem(`klyx.layout.v1.legacy${lens}`) === "1"; } catch { return false; }
}
import { EventsView } from "./EventsView";
import { NodesView } from "./NodesView";
import { NodesViewLegacy } from "./NodesViewLegacy";
import { AccessView } from "./AccessView";
import { UpgradeView } from "./UpgradeView";
import { ConnectivityView } from "./ConnectivityView";
import { KarpenterView } from "./KarpenterView";
import { Placeholder } from "../chrome/Placeholder";

export function ClusterDetail() {
  const route = useFleet((s) => s.route);
  const cluster = useFleet((s) => {
    if (s.route.name !== "cluster") return undefined;
    const clusterName = s.route.cluster;
    return s.clusters.find((x) => x.name === clusterName);
  });

  if (route.name !== "cluster") return null;
  if (!cluster) {
    return (
      <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>
        This cluster is no longer in the fleet.
      </div>
    );
  }
  if (route.section === "overview") return <Overview c={cluster} />;
  if (route.section === "gitops") return legacyLens("Flux") ? <GitOpsLegacy cluster={cluster.name} /> : <GitOps cluster={cluster.name} />;
  if (route.section === "argo") return legacyLens("Argo") ? <ArgoViewLegacy cluster={cluster.name} mode={route.argoView ?? "applications"} /> : <ArgoView cluster={cluster.name} mode={route.argoView ?? "applications"} />;
  if (route.section === "helm") return <HelmView cluster={cluster.name} />;
  if (route.section === "certs") return legacyLens("Certs") ? <CertsViewLegacy cluster={cluster.name} /> : <CertsView cluster={cluster.name} />;
  if (route.section === "externalsecrets") return <ExternalSecretsView cluster={cluster.name} />;
  if (route.section === "resources") {
    // An open instance renders as a side panel INSIDE the list (master-detail,
    // the Pods pattern) - never as a full-page swap that throws the list away.
    if (route.resource) return legacyLens("Resources") ? <InstanceListLegacy cluster={cluster.name} resource={route.resource} /> : <InstanceList cluster={cluster.name} resource={route.resource} />;
    return <BuiltinsView cluster={cluster.name} />;
  }
  if (route.section === "crds") {
    if (route.resource) return legacyLens("Resources") ? <InstanceListLegacy cluster={cluster.name} resource={route.resource} /> : <InstanceList cluster={cluster.name} resource={route.resource} />;
    return <CRDBrowser cluster={cluster.name} />;
  }
  if (route.section === "gatewayApi") return <GatewayApiView cluster={cluster.name} />;
  if (route.section === "workloads") return legacyLens("Workloads") ? <WorkloadsViewLegacy cluster={cluster.name} /> : <WorkloadsView cluster={cluster.name} />;
  if (route.section === "pods") return legacyLens("Pods") ? <PodsViewLegacy cluster={cluster.name} /> : <PodsView cluster={cluster.name} />;
  if (route.section === "events") return <EventsView cluster={cluster.name} />;
  if (route.section === "nodes") return legacyLens("Nodes") ? <NodesViewLegacy cluster={cluster.name} /> : <NodesView cluster={cluster.name} />;
  if (route.section === "access") return <AccessView cluster={cluster.name} />;
  if (route.section === "upgrade") return <UpgradeView cluster={cluster.name} />;
  if (route.section === "connectivity") return <ConnectivityView cluster={cluster.name} />;
  if (route.section === "karpenter") return <KarpenterView cluster={cluster.name} />;
  return <Placeholder section={route.section} c={cluster} />;
}
