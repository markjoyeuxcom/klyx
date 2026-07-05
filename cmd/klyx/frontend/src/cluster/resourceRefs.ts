import type { ResourceRef } from "../store/fleet";

// Well-known group/kind -> ResourceRef mappings shared by every surface that
// turns a bare object reference (Argo app resources, Flux inventory entries,
// cert/ESO target secrets) into a navigable Resources route. A CRD-browser
// lookup can override these when the live cluster serves a different version;
// this table is the offline fallback for the kinds Klyx links to by name.
export const WELL_KNOWN_RESOURCE_REFS: Record<string, ResourceRef> = {
  "apps/Deployment": { group: "apps", version: "v1", plural: "deployments", kind: "Deployment", scope: "Namespaced" },
  "apps/StatefulSet": { group: "apps", version: "v1", plural: "statefulsets", kind: "StatefulSet", scope: "Namespaced" },
  "apps/DaemonSet": { group: "apps", version: "v1", plural: "daemonsets", kind: "DaemonSet", scope: "Namespaced" },
  "apps/ReplicaSet": { group: "apps", version: "v1", plural: "replicasets", kind: "ReplicaSet", scope: "Namespaced" },
  "/Pod": { group: "", version: "v1", plural: "pods", kind: "Pod", scope: "Namespaced" },
  "/Service": { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" },
  "/ConfigMap": { group: "", version: "v1", plural: "configmaps", kind: "ConfigMap", scope: "Namespaced" },
  "/Secret": { group: "", version: "v1", plural: "secrets", kind: "Secret", scope: "Namespaced" },
  "/ServiceAccount": { group: "", version: "v1", plural: "serviceaccounts", kind: "ServiceAccount", scope: "Namespaced" },
  "/Namespace": { group: "", version: "v1", plural: "namespaces", kind: "Namespace", scope: "Cluster" },
  "networking.k8s.io/Ingress": { group: "networking.k8s.io", version: "v1", plural: "ingresses", kind: "Ingress", scope: "Namespaced" },
  "networking.k8s.io/NetworkPolicy": { group: "networking.k8s.io", version: "v1", plural: "networkpolicies", kind: "NetworkPolicy", scope: "Namespaced" },
  "batch/Job": { group: "batch", version: "v1", plural: "jobs", kind: "Job", scope: "Namespaced" },
  "batch/CronJob": { group: "batch", version: "v1", plural: "cronjobs", kind: "CronJob", scope: "Namespaced" },
  "autoscaling/HorizontalPodAutoscaler": { group: "autoscaling", version: "v2", plural: "horizontalpodautoscalers", kind: "HorizontalPodAutoscaler", scope: "Namespaced" },
  "policy/PodDisruptionBudget": { group: "policy", version: "v1", plural: "poddisruptionbudgets", kind: "PodDisruptionBudget", scope: "Namespaced" },
  "gateway.networking.k8s.io/Gateway": { group: "gateway.networking.k8s.io", version: "v1", plural: "gateways", kind: "Gateway", scope: "Namespaced" },
  "gateway.networking.k8s.io/HTTPRoute": { group: "gateway.networking.k8s.io", version: "v1", plural: "httproutes", kind: "HTTPRoute", scope: "Namespaced" },
  "gateway.networking.k8s.io/GRPCRoute": { group: "gateway.networking.k8s.io", version: "v1", plural: "grpcroutes", kind: "GRPCRoute", scope: "Namespaced" },
  "cert-manager.io/Certificate": { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" },
  "cert-manager.io/CertificateRequest": { group: "cert-manager.io", version: "v1", plural: "certificaterequests", kind: "CertificateRequest", scope: "Namespaced" },
  "cert-manager.io/Issuer": { group: "cert-manager.io", version: "v1", plural: "issuers", kind: "Issuer", scope: "Namespaced" },
  "cert-manager.io/ClusterIssuer": { group: "cert-manager.io", version: "v1", plural: "clusterissuers", kind: "ClusterIssuer", scope: "Cluster" },
  "external-secrets.io/ExternalSecret": { group: "external-secrets.io", version: "v1beta1", plural: "externalsecrets", kind: "ExternalSecret", scope: "Namespaced" },
  "external-secrets.io/SecretStore": { group: "external-secrets.io", version: "v1beta1", plural: "secretstores", kind: "SecretStore", scope: "Namespaced" },
  "external-secrets.io/ClusterSecretStore": { group: "external-secrets.io", version: "v1beta1", plural: "clustersecretstores", kind: "ClusterSecretStore", scope: "Cluster" },
};

export function wellKnownResourceRef(group: string, kind: string): ResourceRef | null {
  return WELL_KNOWN_RESOURCE_REFS[`${group || ""}/${kind}`] ?? null;
}

export const SECRET_REF: ResourceRef = WELL_KNOWN_RESOURCE_REFS["/Secret"];
