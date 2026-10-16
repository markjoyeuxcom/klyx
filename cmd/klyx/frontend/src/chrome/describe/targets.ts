// Describe from every lens inspector (The Line slice 14c). One table names
// each lens kind's API group; kubectl takes singular.group ("kustomization.
// kustomize.toolkit.fluxcd.io" - the singular is the kind in lower case), the
// shape DescribeCmd validates. A kind the table does not know gets no item:
// Klyx never guesses a group.
const GROUPS: Record<string, string> = {
  // Flux
  Kustomization: "kustomize.toolkit.fluxcd.io",
  HelmRelease: "helm.toolkit.fluxcd.io",
  GitRepository: "source.toolkit.fluxcd.io",
  OCIRepository: "source.toolkit.fluxcd.io",
  HelmRepository: "source.toolkit.fluxcd.io",
  HelmChart: "source.toolkit.fluxcd.io",
  Bucket: "source.toolkit.fluxcd.io",
  // Argo CD
  Application: "argoproj.io",
  ApplicationSet: "argoproj.io",
  // cert-manager
  Certificate: "cert-manager.io",
  Issuer: "cert-manager.io",
  ClusterIssuer: "cert-manager.io",
  // External Secrets
  ExternalSecret: "external-secrets.io",
  SecretStore: "external-secrets.io",
  ClusterSecretStore: "external-secrets.io",
  // Gateway API
  Gateway: "gateway.networking.k8s.io",
  HTTPRoute: "gateway.networking.k8s.io",
  GRPCRoute: "gateway.networking.k8s.io",
  // workloads
  Deployment: "apps",
  StatefulSet: "apps",
  DaemonSet: "apps",
  ReplicaSet: "apps",
  Job: "batch",
  CronJob: "batch",
  // the definitions themselves (the API browser's definition inspector)
  CustomResourceDefinition: "apiextensions.k8s.io",
  // Karpenter
  NodePool: "karpenter.sh",
  NodeClaim: "karpenter.sh",
};

// describeArg: the kubectl resource argument for a lens kind, or null.
export function describeArg(kind: string): string | null {
  const group = GROUPS[kind];
  return group ? `${kind.toLowerCase()}.${group}` : null;
}
