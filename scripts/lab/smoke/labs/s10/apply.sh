#!/bin/zsh
# Coverage S10 lab on kind-klyx. The real EC2NodeClass (karpenter-provider-aws
# v1.15.0) and AKSNodeClass (karpenter-provider-azure v1.14.5) CRDs from the
# read-only source clones, a stub EKS Auto Mode NodeClass CRD (closed source),
# and the existing karpenter.sh stubs. No controller runs: every status is
# written and stands. Everything is cluster-scoped (Karpenter's kinds are),
# labelled klyx.dev/lab=s10; nothing is exposed.
#   ec2  lab-ready        Ready, every dependent True
#   ec2  lab-subnets      SubnetsReady False SubnetsNotFound, a subnet ID in the message
#   ec2  lab-terminating  held by a finalizer: terminating
#   ec2  lab-behind       conditions at generation 1, the spec at 2: syncing
#   ec2  lab-ami-error    AMIsReady False from the last generation (the AMI
#                         reconciler errored, leaving it), the rest and Ready
#                         False current: not ready by Ready, the old AMIsReady
#                         an earlier note (Codex's implementation review, 1)
#   aks  lab-aks          SubnetsReady False SubnetNotFound, the subnet's ARM ID in the message
#   auto lab-auto-role    InstanceProfileReady False UnauthorizedNodeRole
#   pools: lab-ok, lab-subnets-a and lab-subnets-b (one class failure, two pools),
#          lab-missing (no such class), lab-gcp (a provider Klyx does not read),
#          lab-recovered (lab-ready, its copy still False), lab-terminating,
#          lab-behind, lab-aks, lab-auto, lab-registration (nodes fail to register)
# The pre-S10 stub pool general-purpose-spot gains a nodeClassRef to lab-ready
# (a v1 NodePool must have one); teardown removes it again.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 180 kube "$@"; }
lab() { k label "$@" klyx.dev/lab=s10 --overwrite >/dev/null; }
# the providers' node class CRDs, karpenter-provider-aws v1.15.0 and
# karpenter-provider-azure v1.14.5, each pinned by its digest
k apply --server-side --force-conflicts --field-manager=klyx-lab -f $(fetch https://raw.githubusercontent.com/aws/karpenter-provider-aws/v1.15.0/pkg/apis/crds/karpenter.k8s.aws_ec2nodeclasses.yaml 6c7e7c1785149a3c512d625d2f99375e2731a2c8d80f88fe708464257a62e935 karpenter-aws-v1.15.0-ec2nodeclasses.yaml) >/dev/null
k apply --server-side --force-conflicts --field-manager=klyx-lab -f $(fetch https://raw.githubusercontent.com/Azure/karpenter-provider-azure/v1.14.5/pkg/apis/crds/karpenter.azure.com_aksnodeclasses.yaml 71526db92856e5db8c9fd37624322fe8b9a223f2c7a1570075dbd3b7179c811f karpenter-azure-v1.14.5-aksnodeclasses.yaml) >/dev/null
k apply --server-side --force-conflicts --field-manager=klyx-lab -f - >/dev/null <<YAML
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: nodeclasses.eks.amazonaws.com, labels: {klyx.dev/lab: s10}}
spec:
  group: eks.amazonaws.com
  names: {kind: NodeClass, listKind: NodeClassList, plural: nodeclasses, singular: nodeclass}
  scope: Cluster
  versions:
  - name: v1
    served: true
    storage: true
    subresources: {status: {}}
    schema: {openAPIV3Schema: {type: object, x-kubernetes-preserve-unknown-fields: true}}
YAML
for c in ec2nodeclasses.karpenter.k8s.aws aksnodeclasses.karpenter.azure.com nodeclasses.eks.amazonaws.com; do lab crd $c; done
k wait --for condition=established --timeout=60s crd/ec2nodeclasses.karpenter.k8s.aws crd/aksnodeclasses.karpenter.azure.com crd/nodeclasses.eks.amazonaws.com >/dev/null

now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# c <type> <status> <reason> <generation> [message]
c() { printf '{"type":"%s","status":"%s","reason":"%s","message":"%s","lastTransitionTime":"%s","observedGeneration":%s}' "$1" "$2" "$3" "${5:-}" "$now" "$4"; }
status() { k patch "$1" "$2" --subresource=status --type=merge -p "{\"status\":$3}" >/dev/null; }
gen() { k get "$1" "$2" -o jsonpath='{.metadata.generation}'; }

ec2spec='{role: KarpenterNodeRole-lab, amiSelectorTerms: [{alias: al2023@latest}], subnetSelectorTerms: [{tags: {karpenter.sh/discovery: lab}}], securityGroupSelectorTerms: [{tags: {karpenter.sh/discovery: lab}}]}'
k apply --server-side --force-conflicts --field-manager=klyx-lab -f - >/dev/null <<YAML
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata: {name: lab-ready, labels: {klyx.dev/lab: s10}}
spec: $ec2spec
---
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata: {name: lab-subnets, labels: {klyx.dev/lab: s10}}
spec: {role: KarpenterNodeRole-lab, amiSelectorTerms: [{alias: al2023@latest}], subnetSelectorTerms: [{id: subnet-0a1b2c3d4e5f60718}], securityGroupSelectorTerms: [{tags: {karpenter.sh/discovery: lab}}]}
---
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata: {name: lab-terminating, labels: {klyx.dev/lab: s10}, finalizers: [klyx.dev/lab-hold]}
spec: $ec2spec
---
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata: {name: lab-behind, labels: {klyx.dev/lab: s10}}
spec: $ec2spec
---
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata: {name: lab-ami-error, labels: {klyx.dev/lab: s10}}
spec: $ec2spec
---
apiVersion: karpenter.azure.com/v1beta1
kind: AKSNodeClass
metadata: {name: lab-aks, labels: {klyx.dev/lab: s10}}
spec: {imageFamily: AzureLinux, vnetSubnetID: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-klyx-lab/providers/Microsoft.Network/virtualNetworks/vnet-lab/subnets/snet-nodes}
---
apiVersion: eks.amazonaws.com/v1
kind: NodeClass
metadata: {name: lab-auto-role, labels: {klyx.dev/lab: s10}}
spec: {role: AutoNodeRole-lab, subnetSelectorTerms: [{tags: {Name: lab}}], securityGroupSelectorTerms: [{tags: {Name: lab}}]}
YAML

ok() { c "$1" True "$1" "$2"; }
g=$(gen ec2nodeclasses.karpenter.k8s.aws lab-ready)
status ec2nodeclasses.karpenter.k8s.aws lab-ready "{\"subnets\":[{\"id\":\"subnet-0aaaaaaaaaaaaaaa1\",\"zone\":\"eu-west-1a\"},{\"id\":\"subnet-0aaaaaaaaaaaaaaa2\",\"zone\":\"eu-west-1b\"}],\"securityGroups\":[{\"id\":\"sg-0aaaaaaaaaaaaaaa1\",\"name\":\"lab\"}],\"amis\":[{\"id\":\"ami-0aaaaaaaaaaaaaaa1\",\"name\":\"al2023\",\"requirements\":[]}],\"instanceProfile\":\"lab_profile_ready\",\"conditions\":[$(ok AMIsReady $g),$(ok SubnetsReady $g),$(ok SecurityGroupsReady $g),$(ok InstanceProfileReady $g),$(ok ValidationSucceeded $g),$(ok PlacementGroupReady $g),$(ok Ready $g)]}"
g=$(gen ec2nodeclasses.karpenter.k8s.aws lab-subnets)
status ec2nodeclasses.karpenter.k8s.aws lab-subnets "{\"conditions\":[$(ok AMIsReady $g),$(c SubnetsReady False SubnetsNotFound $g 'SubnetSelector did not match any Subnets: subnet-0a1b2c3d4e5f60718'),$(ok SecurityGroupsReady $g),$(ok InstanceProfileReady $g),$(ok ValidationSucceeded $g),$(c Ready False UnhealthyDependents $g 'SubnetsReady=False')]}"
g=$(gen ec2nodeclasses.karpenter.k8s.aws lab-terminating)
status ec2nodeclasses.karpenter.k8s.aws lab-terminating "{\"conditions\":[$(ok Ready $g)]}"
k delete ec2nodeclasses.karpenter.k8s.aws lab-terminating --wait=false >/dev/null
g=$(gen ec2nodeclasses.karpenter.k8s.aws lab-behind)
status ec2nodeclasses.karpenter.k8s.aws lab-behind "{\"conditions\":[$(ok SubnetsReady $g),$(ok Ready $g)]}"
# a new value every run: the spec always moves past the written conditions
k patch ec2nodeclasses.karpenter.k8s.aws lab-behind --type=merge -p "{\"spec\":{\"tags\":{\"klyx.dev/lab-run\":\"$(date +%s)\"}}}" >/dev/null
k patch ec2nodeclasses.karpenter.k8s.aws lab-ami-error --type=merge -p "{\"spec\":{\"tags\":{\"klyx.dev/lab-run\":\"$(date +%s)\"}}}" >/dev/null
g=$(gen ec2nodeclasses.karpenter.k8s.aws lab-ami-error)
status ec2nodeclasses.karpenter.k8s.aws lab-ami-error "{\"conditions\":[$(c AMIsReady False AMINotFound $((g-1)) 'AMISelector did not match any AMIs'),$(ok SubnetsReady $g),$(ok SecurityGroupsReady $g),$(ok InstanceProfileReady $g),$(ok ValidationSucceeded $g),$(c Ready False UnhealthyDependents $g 'AMIsReady=False')]}"
g=$(gen aksnodeclasses.karpenter.azure.com lab-aks)
status aksnodeclasses.karpenter.azure.com lab-aks "{\"kubernetesVersion\":\"1.33.2\",\"images\":[{\"id\":\"/CommunityGalleries/AKSAzureLinux/images/V3gen2/versions/202610.01.0\",\"requirements\":[]}],\"conditions\":[$(ok ImagesReady $g),$(ok KubernetesVersionReady $g),$(c SubnetsReady False SubnetNotFound $g 'subnet /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-klyx-lab/providers/Microsoft.Network/virtualNetworks/vnet-lab/subnets/snet-nodes not found'),$(ok ValidationSucceeded $g),$(c Ready False UnhealthyDependents $g 'SubnetsReady=False')]}"
g=$(gen nodeclasses.eks.amazonaws.com lab-auto-role)
status nodeclasses.eks.amazonaws.com lab-auto-role "{\"conditions\":[$(ok SubnetsReady $g),$(ok SecurityGroupsReady $g),$(c InstanceProfileReady False UnauthorizedNodeRole $g 'node role AutoNodeRole-lab has no access entry'),$(c Ready False UnhealthyDependents $g 'InstanceProfileReady=False')]}"

pool() { # pool <name> <group> <kind> <class>
	cat <<YAML
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata: {name: $1, labels: {klyx.dev/lab: s10}}
spec: {template: {spec: {nodeClassRef: {group: $2, kind: $3, name: $4}, requirements: []}}, limits: {cpu: "16"}}
YAML
}
k apply --server-side --force-conflicts --field-manager=klyx-lab -f - >/dev/null <<YAML
$(pool lab-ok karpenter.k8s.aws EC2NodeClass lab-ready)
$(pool lab-subnets-a karpenter.k8s.aws EC2NodeClass lab-subnets)
$(pool lab-subnets-b karpenter.k8s.aws EC2NodeClass lab-subnets)
$(pool lab-missing karpenter.k8s.aws EC2NodeClass lab-does-not-exist)
$(pool lab-gcp karpenter.k8s.gcp GCENodeClass lab-gce)
$(pool lab-recovered karpenter.k8s.aws EC2NodeClass lab-ready)
$(pool lab-terminating karpenter.k8s.aws EC2NodeClass lab-terminating)
$(pool lab-behind karpenter.k8s.aws EC2NodeClass lab-behind)
$(pool lab-aks karpenter.azure.com AKSNodeClass lab-aks)
$(pool lab-auto eks.amazonaws.com NodeClass lab-auto-role)
$(pool lab-registration karpenter.k8s.aws EC2NodeClass lab-ready)
YAML
# each pool's conditions are written for its own generation
copy() { c NodeClassReady False "$1" "$pg" "$2"; }
pst() { pg=$(gen nodepools.karpenter.sh "$1"); status nodepools.karpenter.sh "$1" "$(eval "echo \"$2\"")"; }
cg=$(gen ec2nodeclasses.karpenter.k8s.aws lab-ready)
pst lab-ok '{\"nodeClassObservedGeneration\":$cg,\"conditions\":[$(ok ValidationSucceeded $pg),$(ok NodeClassReady $pg),$(ok NodeRegistrationHealthy $pg),$(ok Ready $pg)]}'
for p in lab-subnets-a lab-subnets-b; do
	pst $p '{\"conditions\":[$(ok ValidationSucceeded $pg),$(copy SubnetsNotFound "SubnetSelector did not match any Subnets: subnet-0a1b2c3d4e5f60718"),$(c Ready False UnhealthyDependents $pg "NodeClassReady=False")]}'
done
pst lab-missing '{\"conditions\":[$(ok ValidationSucceeded $pg),$(copy NodeClassNotFound "NodeClass not found on cluster"),$(c Ready False UnhealthyDependents $pg "NodeClassReady=False")]}'
pst lab-recovered '{\"conditions\":[$(ok ValidationSucceeded $pg),$(copy SubnetsNotFound "SubnetSelector did not match any Subnets"),$(c Ready False UnhealthyDependents $pg "NodeClassReady=False")]}'
pst lab-terminating '{\"conditions\":[$(ok ValidationSucceeded $pg),$(copy NodeClassTerminating "NodeClass is terminating"),$(c Ready False UnhealthyDependents $pg "NodeClassReady=False")]}'
pst lab-behind '{\"conditions\":[$(ok ValidationSucceeded $pg),$(ok NodeClassReady $pg),$(ok Ready $pg)]}'
pst lab-aks '{\"conditions\":[$(ok ValidationSucceeded $pg),$(copy SubnetNotFound "subnet /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-klyx-lab/providers/Microsoft.Network/virtualNetworks/vnet-lab/subnets/snet-nodes not found"),$(c Ready False UnhealthyDependents $pg "NodeClassReady=False")]}'
pst lab-auto '{\"conditions\":[$(ok ValidationSucceeded $pg),$(copy UnauthorizedNodeRole "node role AutoNodeRole-lab has no access entry"),$(c Ready False UnhealthyDependents $pg "NodeClassReady=False")]}'
pst lab-registration '{\"nodeClassObservedGeneration\":$cg,\"conditions\":[$(ok ValidationSucceeded $pg),$(ok NodeClassReady $pg),$(c NodeRegistrationHealthy False RegistrationFailed $pg "Failed to register node"),$(ok Ready $pg)]}'
k patch nodepools.karpenter.sh general-purpose-spot --type=merge -p '{"spec":{"template":{"spec":{"nodeClassRef":{"group":"karpenter.k8s.aws","kind":"EC2NodeClass","name":"lab-ready"}}}}}' >/dev/null
echo "lab-s10 applied"
