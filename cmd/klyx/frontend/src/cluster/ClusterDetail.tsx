import { useFleet } from "../store/fleet";
import { Overview } from "./Overview";
import { GitOps } from "./GitOps";
import { ArgoView } from "./ArgoView";
import { HelmView } from "./HelmView";
import { CertsView } from "./CertsView";
import { ExternalSecretsView } from "./ExternalSecretsView";
import { CRDBrowser } from "./CRDBrowser";
import { BuiltinsView } from "./BuiltinsView";
import { InstanceList } from "./InstanceList";
import { GatewayApiView } from "./GatewayApiView";
import { HubbleFlowsView } from "./HubbleFlowsView";
import { WorkloadsView } from "./WorkloadsView";
import { PodsView } from "./PodsView";
import { EventsView } from "./EventsView";
import { NodesView } from "./NodesView";
import { AccessView } from "./AccessView";
import { UpgradeView } from "./UpgradeView";
import { ConnectivityView } from "./ConnectivityView";
import { KarpenterView } from "./KarpenterView";
import { VeleroView } from "./VeleroView";
import { AdmissionView } from "./AdmissionView";
import { DRAView } from "./DRAView";
import { AutoscalingView } from "./AutoscalingView";
import { LoadBalancersView } from "./LoadBalancersView";
import { AlertsView } from "./AlertsView";

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
  if (route.section === "gitops") return <GitOps cluster={cluster.name} />;
  if (route.section === "argo") return <ArgoView cluster={cluster.name} mode={route.argoView ?? "applications"} />;
  if (route.section === "helm") return <HelmView cluster={cluster.name} />;
  if (route.section === "certs") return <CertsView cluster={cluster.name} />;
  if (route.section === "externalsecrets") return <ExternalSecretsView cluster={cluster.name} />;
  if (route.section === "resources") {
    // An open instance renders as a side panel INSIDE the list (master-detail,
    // the Pods pattern) - never as a full-page swap that throws the list away.
    if (route.resource) return <InstanceList cluster={cluster.name} resource={route.resource} />;
    return <BuiltinsView cluster={cluster.name} />;
  }
  if (route.section === "crds") {
    if (route.resource) return <InstanceList cluster={cluster.name} resource={route.resource} />;
    return <CRDBrowser cluster={cluster.name} />;
  }
  if (route.section === "gatewayApi") return <GatewayApiView cluster={cluster.name} />;
  if (route.section === "flows") return <HubbleFlowsView cluster={cluster.name} />;
  if (route.section === "workloads") return <WorkloadsView cluster={cluster.name} />;
  if (route.section === "pods") return <PodsView cluster={cluster.name} />;
  if (route.section === "events") return <EventsView cluster={cluster.name} />;
  if (route.section === "nodes") return <NodesView cluster={cluster.name} />;
  if (route.section === "access") return <AccessView cluster={cluster.name} />;
  if (route.section === "upgrade") return <UpgradeView cluster={cluster.name} />;
  if (route.section === "connectivity") return <ConnectivityView cluster={cluster.name} />;
  if (route.section === "karpenter") return <KarpenterView cluster={cluster.name} />;
  if (route.section === "velero") return <VeleroView cluster={cluster.name} />;
  if (route.section === "admission") return <AdmissionView cluster={cluster.name} />;
  if (route.section === "dra") return <DRAView cluster={cluster.name} />;
  if (route.section === "loadbalancers") return <LoadBalancersView cluster={cluster.name} />;
  if (route.section === "autoscaling") return <AutoscalingView cluster={cluster.name} />;
  if (route.section === "alerts") return <AlertsView cluster={cluster.name} />;
  // Every section is routed above; a persisted unknown section never reaches
  // here (persist.ts validates it), but say so rather than render nothing.
  return <div className="t-body" style={{ padding: 24 }}>This view is not available in this version of Klyx.</div>;
}
