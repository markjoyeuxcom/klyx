import { useFleet, type RevealRef, type WorkloadKind } from "../store/fleet";
import { openHelmRelease } from "../bridge/helm";

// Top links enter the emitting part's view (health 1c slice 4 §5.4): by
// task, part and ref, through selection only - the existing select-*
// intents, or `reveal`, which the part's lens consumes by switching to the
// part's view, revealing the object past its filters and selecting it.

const last = (gvr: string) => gvr.split("/").pop() ?? "";
const WORKLOAD_KINDS: Record<string, WorkloadKind> = { deployments: "Deployment", statefulsets: "StatefulSet", daemonsets: "DaemonSet" };
const FLUX_KINDS: Record<string, string> = {
  kustomizations: "Kustomization", helmreleases: "HelmRelease", gitrepositories: "GitRepository", ocirepositories: "OCIRepository",
  buckets: "Bucket", helmrepositories: "HelmRepository", helmcharts: "HelmChart",
};
const OBJECT_KINDS: Record<string, string> = {
  ...FLUX_KINDS, issuers: "Issuer", clusterissuers: "ClusterIssuer", secretstores: "SecretStore", clustersecretstores: "ClusterSecretStore",
};

/** kindOfRef: the object's kind from its GVR's resource, or "". */
export const kindOfRef = (ref: Pick<RevealRef, "gvr">) => OBJECT_KINDS[last(ref.gvr)] ?? "";

// Every part's lens selects the object it reveals; a part not listed opens
// its view and the link says so.
const SELECTS = new Set([
  "workloads/workloads", "pods/pods", "nodes/nodes", "helm/releases", "flux/reconciliations", "flux/sources",
  "argo/applications", "argo/applicationsets", "certs/certificates", "certs/issuers", "eso/externalsecrets", "eso/stores",
  "gateway/gateways", "gateway/routes", "karpenter/nodeclaims", "karpenter/nodepools",
]);
export const selectsOnReveal = (task: string, part: string) => SELECTS.has(`${task}/${part}`);

// The lens section (and route view) of each task.
const SECTIONS: Record<string, "certs" | "externalsecrets" | "gatewayApi" | "karpenter"> = {
  certs: "certs", eso: "externalsecrets", gateway: "gatewayApi", karpenter: "karpenter",
};

export function reveal(cluster: string, task: string, part: string, ref: RevealRef): void {
  const nav = useFleet.getState();
  if (nav.route.name !== "cluster" || nav.route.cluster !== cluster) nav.openCluster(cluster);
  const namespace = ref.namespace ?? "";
  switch (`${task}/${part}`) {
    case "pods/pods":
      nav.setSection("pods");
      nav.requestNavIntent({ kind: "select-pod", cluster, namespace, name: ref.name });
      return;
    case "workloads/workloads":
      nav.setSection("workloads");
      nav.requestNavIntent({ kind: "select-workload", cluster, workloadKind: WORKLOAD_KINDS[last(ref.gvr)] ?? "Deployment", namespace, name: ref.name });
      return;
    case "nodes/nodes":
      nav.setSection("nodes");
      nav.requestNavIntent({ kind: "select-node", cluster, node: ref.name });
      return;
    case "helm/releases":
      nav.setSection("helm");
      void openHelmRelease(cluster, namespace, ref.name);
      return;
    case "flux/reconciliations":
      nav.openFlux("reconciliation");
      nav.requestNavIntent({ kind: "select-flux", cluster, fluxKind: kindOfRef(ref), namespace, name: ref.name });
      return;
    case "flux/sources":
      nav.openFlux("sources");
      break;
    case "argo/applications":
    case "argo/applicationsets":
      nav.openArgo(part === "applicationsets" ? "applicationsets" : "applications");
      break;
    default: {
      const section = SECTIONS[task];
      if (!section) return;
      nav.setSection(section);
    }
  }
  nav.requestNavIntent({ kind: "reveal", cluster, task, part, ref });
}
