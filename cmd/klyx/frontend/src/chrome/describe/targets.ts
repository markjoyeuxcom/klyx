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
  // the Flux Operator (coverage S1a)
  FluxInstance: "fluxcd.controlplane.io",
  FluxReport: "fluxcd.controlplane.io",
  ResourceSet: "fluxcd.controlplane.io",
  ResourceSetInputProvider: "fluxcd.controlplane.io",
  // Flux image automation (coverage S1b)
  ImageRepository: "image.toolkit.fluxcd.io",
  ImagePolicy: "image.toolkit.fluxcd.io",
  ImageUpdateAutomation: "image.toolkit.fluxcd.io",
  // source composition and notifications (coverage S1c)
  ExternalArtifact: "source.toolkit.fluxcd.io",
  ArtifactGenerator: "source.extensions.fluxcd.io",
  Receiver: "notification.toolkit.fluxcd.io",
  Provider: "notification.toolkit.fluxcd.io",
  Alert: "notification.toolkit.fluxcd.io",
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
  ListenerSet: "gateway.networking.k8s.io", // coverage S7a
  // Velero (coverage S2a)
  Schedule: "velero.io",
  Backup: "velero.io",
  Restore: "velero.io",
  BackupStorageLocation: "velero.io",
  // load balancing (coverage S4)
  IPAddressPool: "metallb.io",
  L2Advertisement: "metallb.io",
  BGPAdvertisement: "metallb.io",
  ConfigurationState: "metallb.io",
  CiliumLoadBalancerIPPool: "cilium.io",
  // admission (coverage S3a)
  ValidatingWebhookConfiguration: "admissionregistration.k8s.io",
  MutatingWebhookConfiguration: "admissionregistration.k8s.io",
  ValidatingAdmissionPolicy: "admissionregistration.k8s.io",
  MutatingAdmissionPolicy: "admissionregistration.k8s.io",
  ValidatingAdmissionPolicyBinding: "admissionregistration.k8s.io",
  MutatingAdmissionPolicyBinding: "admissionregistration.k8s.io",
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
