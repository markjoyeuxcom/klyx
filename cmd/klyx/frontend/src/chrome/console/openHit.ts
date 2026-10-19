import { useFleet, type RevealRef } from "../../store/fleet";
import { reveal } from "../../nav/reveal";
import type { IndexHit } from "../../bridge/fleetIndex";

// Opening an index hit (The Line slice 9 note §4.2): its lens, its object
// selected and pinned to the hit's cluster and UID. A kind with a lens of
// its own reveals there; every other kind opens in Resources, the generic
// inspector, with the UID expected.

type Lens = { task: string; part: string; label: string };

const LENSES: Record<string, Lens> = {
  "/pods": { task: "pods", part: "pods", label: "Pods" },
  "apps/deployments": { task: "workloads", part: "workloads", label: "Workloads" },
  "apps/statefulsets": { task: "workloads", part: "workloads", label: "Workloads" },
  "apps/daemonsets": { task: "workloads", part: "workloads", label: "Workloads" },
  "/nodes": { task: "nodes", part: "nodes", label: "Nodes" },
  "kustomize.toolkit.fluxcd.io/kustomizations": { task: "flux", part: "reconciliations", label: "GitOps" },
  "helm.toolkit.fluxcd.io/helmreleases": { task: "flux", part: "reconciliations", label: "GitOps" },
  "source.toolkit.fluxcd.io/gitrepositories": { task: "flux", part: "sources", label: "GitOps sources" },
  "source.toolkit.fluxcd.io/ocirepositories": { task: "flux", part: "sources", label: "GitOps sources" },
  "source.toolkit.fluxcd.io/buckets": { task: "flux", part: "sources", label: "GitOps sources" },
  "source.toolkit.fluxcd.io/helmrepositories": { task: "flux", part: "sources", label: "GitOps sources" },
  "source.toolkit.fluxcd.io/helmcharts": { task: "flux", part: "sources", label: "GitOps sources" },
  "argoproj.io/applications": { task: "argo", part: "applications", label: "Argo CD" },
  "argoproj.io/applicationsets": { task: "argo", part: "applicationsets", label: "Argo CD" },
  "cert-manager.io/certificates": { task: "certs", part: "certificates", label: "Certificates" },
  "cert-manager.io/issuers": { task: "certs", part: "issuers", label: "Certificates" },
  "cert-manager.io/clusterissuers": { task: "certs", part: "issuers", label: "Certificates" },
  "external-secrets.io/externalsecrets": { task: "eso", part: "externalsecrets", label: "External Secrets" },
  "external-secrets.io/secretstores": { task: "eso", part: "stores", label: "External Secrets" },
  "external-secrets.io/clustersecretstores": { task: "eso", part: "stores", label: "External Secrets" },
  "gateway.networking.k8s.io/gateways": { task: "gateway", part: "gateways", label: "Gateway API" },
  "gateway.networking.k8s.io/httproutes": { task: "gateway", part: "routes", label: "Gateway API" },
  // coverage S7: every route kind and the ListenerSets have the lens's views
  "gateway.networking.k8s.io/grpcroutes": { task: "gateway", part: "routes", label: "Gateway API" },
  "gateway.networking.k8s.io/tlsroutes": { task: "gateway", part: "routes", label: "Gateway API" },
  "gateway.networking.k8s.io/tcproutes": { task: "gateway", part: "routes", label: "Gateway API" },
  "gateway.networking.k8s.io/udproutes": { task: "gateway", part: "routes", label: "Gateway API" },
  "gateway.networking.k8s.io/listenersets": { task: "gateway", part: "listenersets", label: "Gateway API" },
  "karpenter.sh/nodeclaims": { task: "karpenter", part: "nodeclaims", label: "Karpenter" },
  "karpenter.sh/nodepools": { task: "karpenter", part: "nodepools", label: "Karpenter" },
  "velero.io/schedules": { task: "velero", part: "schedules", label: "Velero" },
  "velero.io/backupstoragelocations": { task: "velero", part: "locations", label: "Velero" },
  "velero.io/backups": { task: "velero", part: "backups", label: "Velero" },
  "admissionregistration.k8s.io/validatingwebhookconfigurations": { task: "admission", part: "webhooks", label: "Admission" },
  "admissionregistration.k8s.io/mutatingwebhookconfigurations": { task: "admission", part: "webhooks", label: "Admission" },
  "admissionregistration.k8s.io/validatingadmissionpolicies": { task: "admission", part: "policies", label: "Admission" },
  "admissionregistration.k8s.io/mutatingadmissionpolicies": { task: "admission", part: "policies", label: "Admission" },
  // coverage S4: a Service opens in Resources (only a LoadBalancer has a
  // verdict, and a hit does not carry the type)
  "metallb.io/ipaddresspools": { task: "lb", part: "pools", label: "Load balancers" },
  "cilium.io/ciliumloadbalancerippools": { task: "lb", part: "pools", label: "Load balancers" },
  "metallb.io/configurationstates": { task: "lb", part: "metallb", label: "Load balancers" },
  // coverage S9
  "autoscaling/horizontalpodautoscalers": { task: "autoscaling", part: "hpas", label: "Autoscaling" },
  "keda.sh/scaledobjects": { task: "keda", part: "scaledobjects", label: "Autoscaling" },
  "keda.sh/scaledjobs": { task: "keda", part: "scaledjobs", label: "Autoscaling" },
};

const keyOf = (group: string, resource: string) => `${group}/${resource}`;

/** lensOf names where a kind opens: its own lens, or Resources. */
export function lensOf(group: string, resource: string): string {
  return LENSES[keyOf(group, resource)]?.label ?? "Resources";
}

/** lensOfKey is lensOf for an index kind key (group/resource). */
export function lensOfKey(key: string): string {
  return LENSES[key]?.label ?? "Resources";
}

export function openHit(h: IndexHit): void {
  const gvr = h.group ? `${h.group}/${h.version}/${h.resource}` : `${h.version}/${h.resource}`;
  const lens = LENSES[keyOf(h.group, h.resource)];
  if (lens) {
    const ref: RevealRef = { gvr, namespace: h.namespace || undefined, name: h.name, uid: h.uid };
    reveal(h.cluster, lens.task, lens.part, ref);
    return;
  }
  const nav = useFleet.getState();
  if (nav.route.name !== "cluster" || nav.route.cluster !== h.cluster) nav.openCluster(h.cluster);
  nav.setSection("resources");
  nav.openResource({ group: h.group, version: h.version, plural: h.resource, kind: h.kind, scope: h.namespace ? "Namespaced" : "Cluster" });
  nav.openInstance(h.namespace, h.name, h.uid);
}
