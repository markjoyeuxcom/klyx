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
import { InstanceDetail } from "./InstanceDetail";
import { GatewayApiView } from "./GatewayApiView";
import { WorkloadsView } from "./WorkloadsView";
import { PodsView } from "./PodsView";
import { EventsView } from "./EventsView";
import { NodesView } from "./NodesView";
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
  if (route.section === "gitops") return <GitOps cluster={cluster.name} />;
  if (route.section === "argo") return <ArgoView cluster={cluster.name} mode={route.argoView ?? "applications"} />;
  if (route.section === "helm") return <HelmView cluster={cluster.name} />;
  if (route.section === "certs") return <CertsView cluster={cluster.name} />;
  if (route.section === "externalsecrets") return <ExternalSecretsView cluster={cluster.name} />;
  if (route.section === "resources") {
    if (route.resource && route.instance) return <InstanceDetail cluster={cluster.name} resource={route.resource} instance={route.instance} />;
    if (route.resource) return <InstanceList cluster={cluster.name} resource={route.resource} />;
    return <BuiltinsView cluster={cluster.name} />;
  }
  if (route.section === "crds") {
    if (route.resource && route.instance) return <InstanceDetail cluster={cluster.name} resource={route.resource} instance={route.instance} />;
    if (route.resource) return <InstanceList cluster={cluster.name} resource={route.resource} />;
    return <CRDBrowser cluster={cluster.name} />;
  }
  if (route.section === "gatewayApi") return <GatewayApiView cluster={cluster.name} />;
  if (route.section === "workloads") return <WorkloadsView cluster={cluster.name} />;
  if (route.section === "pods") return <PodsView cluster={cluster.name} />;
  if (route.section === "events") return <EventsView cluster={cluster.name} />;
  if (route.section === "nodes") return <NodesView cluster={cluster.name} />;
  return <Placeholder section={route.section} c={cluster} />;
}
