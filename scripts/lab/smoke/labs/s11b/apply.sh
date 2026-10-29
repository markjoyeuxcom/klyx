#!/bin/zsh
# The S11b lab on kind-klyx: the upstream example DRA driver (a Helm release
# in its own namespace) and, in a labelled namespace, the claims and pods of
# S11b §9, plus two fixture pools stated as fixtures (all-nodes slices under a
# driver name no plugin serves, so the kubelet never removes them).
set -e
source ${0:A:h}/common.sh && target
# the upstream example driver's chart 0.5.0, pinned by its digest
h upgrade -i --create-namespace --namespace $DRIVER_NS dra-example-driver $(chart oci://registry.k8s.io/dra-example-driver/charts/dra-example-driver 0.5.0 ab5232c4f5cc7348d9c67bbcc9e0aa7a5f52a793e41dbd458bf3b2f07d32cb4a) --set gpuDeviceStatus=true --set deviceClass.extendedResourceName=example.com/dra-gpu --wait --timeout 180s >/dev/null
k label namespace $DRIVER_NS klyx.dev/lab=s11b --overwrite >/dev/null
until_ok "the driver's slice for $NODE" sh -c "$kubesh get resourceslices -o jsonpath='{.items[*].spec.nodeName}' | grep -q $NODE"
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace $NS klyx.dev/lab=s11b --overwrite >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata: {name: gpu-one, namespace: $NS, labels: {klyx.dev/lab: s11b}}
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly: {deviceClassName: gpu.example.com, allocationMode: ExactCount, count: 1}
---
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata: {name: gpu-nine, namespace: $NS, labels: {klyx.dev/lab: s11b}}
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly: {deviceClassName: gpu.example.com, allocationMode: ExactCount, count: 9}
---
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata: {name: lab-shared, namespace: $NS, labels: {klyx.dev/lab: s11b}}
spec:
  devices:
    requests:
    - name: gpu
      exactly: {deviceClassName: gpu.example.com, allocationMode: ExactCount, count: 1}
---
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata: {name: lab-unused, namespace: $NS, labels: {klyx.dev/lab: s11b}}
spec:
  devices:
    requests:
    - name: gpu
      exactly: {deviceClassName: gpu.example.com, allocationMode: ExactCount, count: 1}
YAML
pod() { # pod <name> <claim yaml>
  k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: $1, namespace: $NS, labels: {klyx.dev/lab: s11b}}
spec:
  terminationGracePeriodSeconds: 1
  containers:
  - name: app
    image: registry.k8s.io/e2e-test-images/busybox:1.36.1-1
    command: ["sh", "-c", "sleep 1000000"]
    resources: {claims: [{name: gpu}]}
  resourceClaims:
  - $2
YAML
}
pod lab-template "{name: gpu, resourceClaimTemplateName: gpu-one}"
pod lab-share-a "{name: gpu, resourceClaimName: lab-shared}"
pod lab-share-b "{name: gpu, resourceClaimName: lab-shared}"
pod lab-too-many "{name: gpu, resourceClaimTemplateName: gpu-nine}"
# the implicit DeviceClass name (KEP-5004): S11a's "served by DRA", live.
# Not fatal: the API server rejects the name where the feature is off.
k apply -f - >/dev/null <<YAML || echo "note: the implicit DeviceClass name was not accepted (DRAExtendedResource off?)"
apiVersion: v1
kind: Pod
metadata: {name: lab-implicit, namespace: $NS, labels: {klyx.dev/lab: s11b}}
spec:
  terminationGracePeriodSeconds: 1
  containers:
  - name: app
    image: registry.k8s.io/e2e-test-images/busybox:1.36.1-1
    command: ["sh", "-c", "sleep 1000000"]
    resources:
      requests: {deviceclass.resource.kubernetes.io/gpu.example.com: "1"}
      limits: {deviceclass.resource.kubernetes.io/gpu.example.com: "1"}
YAML
# fixture pools, stated as fixtures (S11b §9): incomplete and invalid
k apply -f - >/dev/null <<YAML
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata: {name: klyx-s11b-incomplete-a, labels: {klyx.dev/lab: s11b, klyx.dev/fixture: "true"}}
spec:
  driver: fixture.klyx.dev
  pool: {name: lab-incomplete, generation: 1, resourceSliceCount: 2}
  allNodes: true
  devices: [{name: dev-0}]
---
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata: {name: klyx-s11b-invalid-a, labels: {klyx.dev/lab: s11b, klyx.dev/fixture: "true"}}
spec:
  driver: fixture.klyx.dev
  pool: {name: lab-invalid, generation: 1, resourceSliceCount: 2}
  allNodes: true
  devices: [{name: dev-0}]
---
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata: {name: klyx-s11b-invalid-b, labels: {klyx.dev/lab: s11b, klyx.dev/fixture: "true"}}
spec:
  driver: fixture.klyx.dev
  pool: {name: lab-invalid, generation: 1, resourceSliceCount: 2}
  allNodes: true
  devices: [{name: dev-0}]
YAML
until_ok "lab-template running" sh -c "$kubesh -n $NS get pod lab-template -o jsonpath='{.status.phase}' | grep -q Running"
until_ok "lab-share-b running" sh -c "$kubesh -n $NS get pod lab-share-b -o jsonpath='{.status.phase}' | grep -q Running"
until_ok "lab-too-many unschedulable" sh -c "$kubesh -n $NS get pod lab-too-many -o jsonpath='{.status.conditions[?(@.type==\"PodScheduled\")].reason}' | grep -q Unschedulable"
echo "lab-s11b applied"
